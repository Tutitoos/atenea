// Package workspacecontext coordinates one code.context child per explicitly
// named repository. It is an operation boundary, not a catalog capability:
// each child still receives exactly one Repository and goes through the
// ordinary provider/cache/quality path supplied by the caller.
package workspacecontext

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/Tutitoos/atenea/pkg/contract"
)

const (
	// Capability is part of ATENEA's public orchestration contract.
	Capability = "workspace.context"
	// ChildCapability is part of ATENEA's public orchestration contract.
	ChildCapability = "code.context"
	// DefaultMaxTargets is part of ATENEA's public orchestration contract.
	DefaultMaxTargets = 8
	// DefaultMaxParallel is part of ATENEA's public orchestration contract.
	DefaultMaxParallel = 4
	// maxOpaqueCursorBytes bounds continuation state without changing bytes
	// for cursors that fit. Oversized cursors are invalid input/output.
	maxOpaqueCursorBytes = contract.MaxPersistedRaw
)

// Target is one explicit repository in a workspace request. Repository is
// optional at the boundary; the coordinator fills it from ID and Root after
// canonicalization, so the child always has exactly one repository.
type Target struct {
	ID string `json:"id"`
	// Root is an internal canonical path. Public MCP/CLI callers normally
	// provide only ID; Core resolves the configured root before Run.
	Root       string `json:"-"`
	rootAlias  string
	Repository contract.Repository `json:"-"`
	Payload    map[string]any      `json:"payload,omitempty"`
	Cursor     string              `json:"cursor,omitempty"`
	Permission contract.Permission `json:"-"`
}

// Continuation binds a cursor to one repository. Cursors are opaque and may
// never be supplied for another target.
type Continuation struct {
	RepositoryID string `json:"repository"`
	Cursor       string `json:"cursor"`
}

// Request is the workspace operation. Targets are deliberately a slice, not a
// map: output order follows this order and duplicate/alias targets are errors.
type Request struct {
	Targets       []Target            `json:"targets"`
	Payload       map[string]any      `json:"payload,omitempty"`
	Continuations []Continuation      `json:"continuations,omitempty"`
	Permission    contract.Permission `json:"-"`
	BudgetUSD     float64             `json:"budget_usd,omitempty"`
	MaxParallel   int                 `json:"max_parallel,omitempty"`
	MaxTargets    int                 `json:"max_targets,omitempty"`
	// BeforeDispatch is an optional host-owned activity seam. It is called in
	// target order, after complete preflight and immediately before fan-out.
	// It is deliberately not part of the wire format or a durability claim.
	BeforeDispatch func(context.Context, Target) error `json:"-"`
}

// ChildRequest is the only request a provider sees. It names one repository;
// there is no workspace list to accidentally flatten into a code.context call.
type ChildRequest struct {
	Capability  string
	Repository  contract.Repository
	Payload     map[string]any
	Cursor      string
	Permission  contract.Permission
	TargetIndex int
}

// ChildResult is the provider-neutral result returned by a child runner.
type ChildResult struct {
	Result         map[string]any
	Evidence       []contract.QueryEvidence
	Provider       string
	Implementation string
	Notices        []string
	OutOfScope     int
	NextCursor     string
	Partial        bool
	Truncated      bool
	Error          error
}

// RepositoryResult is one output row. The slice order is the request order.
type RepositoryResult struct {
	ID string `json:"id"`
	// Root remains available to trusted in-process callers but is never emitted
	// by the chat/MCP/CLI JSON envelope.
	Root           string                   `json:"-"`
	Result         map[string]any           `json:"result,omitempty"`
	Evidence       []contract.QueryEvidence `json:"evidence,omitempty"`
	Provider       string                   `json:"provider,omitempty"`
	Implementation string                   `json:"implementation,omitempty"`
	Notices        []string                 `json:"notices,omitempty"`
	OutOfScope     int                      `json:"out_of_scope,omitempty"`
	Cursor         string                   `json:"cursor,omitempty"`
	NextCursor     string                   `json:"next_cursor,omitempty"`
	Error          string                   `json:"error,omitempty"`
	Started        bool                     `json:"started"`
	Partial        bool                     `json:"partial"`
	Truncated      bool                     `json:"truncated"`
}

// Result is the aggregate envelope. No aggregate result/cache is produced;
// every answer remains attached to its repository row.
type Result struct {
	Repositories []RepositoryResult `json:"repositories"`
	Partial      bool               `json:"partial"`
}

// Dispatch is called once per started child. It must execute the normal
// code.context path, including its per-repository cache and quality handling.
type Dispatch func(context.Context, ChildRequest) (ChildResult, error)

// Authorize is called for every canonicalized target before any Dispatch.
type Authorize func(Target) error

// Config controls coordinator-wide limits. MaxParallel may be lowered from
// four, never raised by a request; MaxTargets is fixed at eight by default.
type Config struct {
	MaxParallel int
	MaxTargets  int
	// StandingBudgetUSD is resolved once by the host when a request leaves its
	// budget at zero. The coordinator then divides that effective ceiling
	// among children before any Agent call is made.
	StandingBudgetUSD float64
}

// Coordinator validates and runs workspace.context operations.
type Coordinator struct {
	maxParallel    int
	maxTargets     int
	standingBudget float64
}

// New is part of ATENEA's public orchestration contract.
func New(cfg Config) (*Coordinator, error) {
	maxTargets := cfg.MaxTargets
	if maxTargets == 0 {
		maxTargets = DefaultMaxTargets
	}
	if maxTargets < 1 || maxTargets > DefaultMaxTargets {
		return nil, fmt.Errorf("workspace.context: max_targets must be between 1 and %d", DefaultMaxTargets)
	}
	maxParallel := cfg.MaxParallel
	if maxParallel == 0 {
		maxParallel = DefaultMaxParallel
	}
	if maxParallel < 1 || maxParallel > DefaultMaxParallel {
		return nil, fmt.Errorf("workspace.context: max_parallel must be between 1 and %d", DefaultMaxParallel)
	}
	if !realBudget(cfg.StandingBudgetUSD) {
		return nil, errors.New("workspace.context: standing_budget_usd must be finite and non-negative")
	}
	return &Coordinator{maxParallel: maxParallel, maxTargets: maxTargets, standingBudget: cfg.StandingBudgetUSD}, nil
}

// Run validates the complete request and authorization set before starting a
// worker. Invalid, duplicate, aliased, unauthorized or foreign-cursor input
// therefore causes zero provider calls.
func (c *Coordinator) Run(ctx context.Context, req Request, authorize Authorize, dispatch Dispatch) (Result, error) {
	if c == nil {
		return Result{}, errors.New("workspace.context: nil coordinator")
	}
	if dispatch == nil {
		return Result{}, errors.New("workspace.context: dispatch is required")
	}
	if len(req.Targets) < 1 || len(req.Targets) > c.maxTargets {
		return Result{}, fmt.Errorf("workspace.context: targets must contain 1..%d repositories", c.maxTargets)
	}
	if !realBudget(req.BudgetUSD) {
		return Result{}, fmt.Errorf("workspace.context: budget must be finite and non-negative")
	}
	if req.MaxParallel < 0 || req.MaxParallel > c.maxParallel {
		return Result{}, fmt.Errorf("workspace.context: max_parallel may only lower the configured limit to %d", c.maxParallel)
	}
	parallel := c.maxParallel
	if req.MaxParallel > 0 {
		parallel = req.MaxParallel
	}
	commonPermission := req.Permission.Clone()
	if commonPermission.Task == "" {
		commonPermission.Task = Capability
	}
	if err := commonPermission.Validate(); err != nil {
		return Result{}, err
	}
	if len(req.Targets) > 1 {
		if cursor, present, err := payloadCursor(req.Payload); err != nil {
			return Result{}, err
		} else if present && cursor != "" {
			return Result{}, errors.New("workspace.context: common payload cursor is ambiguous for multiple targets; use target cursor or continuation")
		}
	}
	canonical, err := preflightTargets(req, commonPermission)
	if err != nil {
		return Result{}, err
	}
	cursors, err := validateContinuations(canonical, req.Continuations)
	if err != nil {
		return Result{}, err
	}
	for i := range canonical {
		if cursor, ok := cursors[canonical[i].ID]; ok {
			if canonical[i].Cursor != "" && canonical[i].Cursor != cursor {
				return Result{}, fmt.Errorf("workspace.context: conflicting cursors for repository %q", canonical[i].ID)
			}
			if existing, ok := canonical[i].Payload["cursor"].(string); ok && existing != cursor {
				return Result{}, fmt.Errorf("workspace.context: continuation cursor conflicts with payload cursor for repository %q", canonical[i].ID)
			}
			canonical[i].Cursor = cursor
			if canonical[i].Payload == nil {
				canonical[i].Payload = map[string]any{}
			}
			canonical[i].Payload["cursor"] = cursor
		}
	}
	if authorize != nil {
		for _, target := range canonical {
			if err := authorize(target); err != nil {
				return Result{}, fmt.Errorf("workspace.context: authorize %s: %w", target.ID, err)
			}
		}
	}
	// Persist/emit all preambles in request order before a worker can dispatch.
	// This keeps activity ordering deterministic even when child work is
	// concurrent, and cancellation here means no child has started.
	if req.BeforeDispatch != nil {
		for _, target := range canonical {
			if err := ctx.Err(); err != nil {
				return cancellationResult(canonical, err), nil
			}
			if err := req.BeforeDispatch(ctx, target); err != nil {
				return Result{}, err
			}
		}
	}

	// Equal reservation is conservative and makes the shared ceiling
	// enforceable even while children run concurrently. Unused shares are never
	// silently reallocated to a later child.
	effectiveBudget := req.BudgetUSD
	if effectiveBudget == 0 {
		effectiveBudget = c.standingBudget
	}
	share := 0.0
	if effectiveBudget > 0 {
		share = effectiveBudget / float64(len(canonical))
	}
	result := Result{Repositories: make([]RepositoryResult, len(canonical))}
	for i, target := range canonical {
		result.Repositories[i] = RepositoryResult{ID: target.ID, Root: target.Root, Cursor: target.Cursor}
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	worker := func() {
		defer wg.Done()
		for {
			var index int
			var ok bool
			select {
			case <-ctx.Done():
				return
			case index, ok = <-jobs:
				if !ok {
					return
				}
			}
			if ctx.Err() != nil {
				mu.Lock()
				result.Repositories[index].Error = safeDiagnostic(ctx.Err().Error())
				result.Repositories[index].Partial = true
				result.Partial = true
				mu.Unlock()
				continue
			}
			target := canonical[index]
			permission := target.Permission.Clone()
			permission.BudgetUSD = share
			child := ChildRequest{Capability: ChildCapability, Repository: target.Repository.Clone(), Payload: cloneMap(target.Payload), Cursor: target.Cursor, Permission: permission, TargetIndex: index}
			childResult, childErr := dispatch(ctx, child)
			if childErr == nil {
				childErr = childResult.Error
			}
			sanitizedResult, sanitizeLoss := sanitizeResult(childResult.Result, target.Root, target.ID, target.rootAlias)
			sanitizedEvidence, evidenceLoss, evidenceCursorLoss := sanitizeEvidence(childResult.Evidence, target.Root, target.ID, target.rootAlias)
			sanitizedNotices, noticesLoss := safeNotices(childResult.Notices, target.Root, target.ID, target.rootAlias)
			nextCursor, nextCursorLoss := boundedOpaqueCursor(childResult.NextCursor)
			row := RepositoryResult{
				ID: target.ID, Root: target.Root, Result: sanitizedResult,
				Evidence: sanitizedEvidence, Provider: childResult.Provider,
				Implementation: childResult.Implementation, Notices: sanitizedNotices,
				OutOfScope: childResult.OutOfScope, Cursor: target.Cursor, NextCursor: nextCursor,
				Started: true, Partial: childResult.Partial, Truncated: childResult.Truncated,
			}
			if row.NextCursor == "" {
				for _, evidence := range row.Evidence {
					if evidence.NextCursor != "" {
						row.NextCursor = evidence.NextCursor
						break
					}
				}
			}
			var errorLoss bool
			if childErr != nil {
				row.Error, errorLoss = safeDiagnosticFor(childErr.Error(), target.Root, target.ID, target.rootAlias)
				row.Partial = true
			}
			row.Partial = row.Partial || incompleteEvidence(row.Evidence, row.Result)
			row.Truncated = row.Truncated || structuralTruncated(row.Evidence, row.Result)
			if row.Truncated {
				row.Partial = true
			}
			if sanitizeLoss || evidenceLoss || noticesLoss || errorLoss {
				row.Partial = true
				row.Truncated = true
				row.Notices = append(row.Notices, "result sanitized: bounded value omitted")
			}
			if nextCursorLoss || evidenceCursorLoss {
				row.Partial = true
				row.Truncated = true
				row.Notices = append(row.Notices, "cursor omitted: exceeds maximum size")
				if row.Error == "" {
					row.Error = "cursor omitted: exceeds maximum size"
				}
			}
			mu.Lock()
			result.Repositories[index] = row
			result.Partial = result.Partial || row.Partial || row.Error != ""
			mu.Unlock()
		}
	}
	wg.Add(parallel)
	for i := 0; i < parallel; i++ {
		go worker()
	}
feed:
	for i := range canonical {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		result.Partial = true
		for i := range result.Repositories {
			if !result.Repositories[i].Started && result.Repositories[i].Error == "" {
				result.Repositories[i].Error = safeDiagnostic(err.Error())
				result.Repositories[i].Partial = true
			}
		}
	}
	return result, nil
}

func cancellationResult(targets []Target, err error) Result {
	rows := make([]RepositoryResult, len(targets))
	message := safeDiagnostic(err.Error())
	for i, target := range targets {
		rows[i] = RepositoryResult{ID: target.ID, Root: target.Root, Cursor: target.Cursor, Error: message, Partial: true}
	}
	return Result{Repositories: rows, Partial: len(rows) > 0}
}

// Run is a convenience for callers that do not need to retain a Coordinator.
func Run(ctx context.Context, req Request, authorize Authorize, dispatch Dispatch) (Result, error) {
	coordinator, err := New(Config{})
	if err != nil {
		return Result{}, err
	}
	return coordinator.Run(ctx, req, authorize, dispatch)
}

func preflightTargets(req Request, common contract.Permission) ([]Target, error) {
	out := make([]Target, len(req.Targets))
	ids := make(map[string]int, len(req.Targets))
	roots := make(map[string]string, len(req.Targets))
	for i, input := range req.Targets {
		id := strings.TrimSpace(input.ID)
		if id == "" {
			return nil, fmt.Errorf("workspace.context: target %d has empty id", i)
		}
		if err := validateOpaqueCursor(input.Cursor); err != nil {
			return nil, fmt.Errorf("workspace.context: target %s: %w", id, err)
		}
		if previous, ok := ids[id]; ok {
			return nil, fmt.Errorf("workspace.context: duplicate target id %q at %d and %d", id, previous, i)
		}
		ids[id] = i
		rootAlias, err := filepath.Abs(filepath.Clean(strings.TrimSpace(input.Root)))
		if err != nil {
			return nil, fmt.Errorf("workspace.context: target %s root: %w", id, err)
		}
		root, err := CanonicalRoot(input.Root)
		if err != nil {
			return nil, fmt.Errorf("workspace.context: target %s root: %w", id, err)
		}
		if previous, ok := roots[root]; ok {
			return nil, fmt.Errorf("workspace.context: targets %q and %q resolve to the same physical root %s", id, previous, root)
		}
		roots[root] = id
		repo := input.Repository.Clone()
		repo.ID, repo.Path = id, root
		if err := repo.Validate(); err != nil {
			return nil, fmt.Errorf("workspace.context: target %s: %w", id, err)
		}
		permission := input.Permission.Clone()
		if permission.Task == "" {
			permission = common.Clone()
		}
		if err := permission.Validate(); err != nil {
			return nil, fmt.Errorf("workspace.context: target %s permission: %w", id, err)
		}
		payload := cloneMap(req.Payload)
		for key, value := range input.Payload {
			if payload == nil {
				payload = map[string]any{}
			}
			payload[key] = value
		}
		commonCursor, commonPresent, err := payloadCursor(req.Payload)
		if err != nil {
			return nil, err
		}
		targetCursor, targetPresent, err := payloadCursor(input.Payload)
		if err != nil {
			return nil, fmt.Errorf("workspace.context: target %s: %w", id, err)
		}
		explicitCursor := input.Cursor
		if targetPresent && commonPresent && targetCursor != "" && commonCursor != "" && targetCursor != commonCursor {
			return nil, fmt.Errorf("workspace.context: target %s cursor conflicts with common payload cursor", id)
		}
		effectiveCursor := explicitCursor
		if effectiveCursor == "" {
			effectiveCursor = targetCursor
		}
		if effectiveCursor == "" {
			effectiveCursor = commonCursor
		}
		if explicitCursor != "" && ((targetPresent && targetCursor != "" && targetCursor != explicitCursor) || (commonPresent && commonCursor != "" && commonCursor != explicitCursor)) {
			return nil, fmt.Errorf("workspace.context: target %s cursor conflicts with payload cursor", id)
		}
		if effectiveCursor != "" {
			if payload == nil {
				payload = map[string]any{}
			}
			payload["cursor"] = effectiveCursor
		}
		out[i] = Target{ID: id, Root: root, rootAlias: rootAlias, Repository: repo, Payload: payload, Cursor: effectiveCursor, Permission: permission}
	}
	return out, nil
}

func payloadCursor(payload map[string]any) (string, bool, error) {
	if payload == nil {
		return "", false, nil
	}
	raw, ok := payload["cursor"]
	if !ok || raw == nil {
		return "", false, nil
	}
	cursor, ok := raw.(string)
	if !ok {
		return "", true, errors.New("workspace.context: cursor must be a string")
	}
	if err := validateOpaqueCursor(cursor); err != nil {
		return "", true, err
	}
	return cursor, true, nil
}

func validateContinuations(targets []Target, continuations []Continuation) (map[string]string, error) {
	result := make(map[string]string, len(continuations))
	if len(continuations) == 0 {
		return result, nil
	}
	byID := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		byID[target.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(continuations))
	for _, continuation := range continuations {
		if continuation.RepositoryID == "" || continuation.Cursor == "" {
			return nil, errors.New("workspace.context: continuation requires repository and cursor")
		}
		if err := validateOpaqueCursor(continuation.Cursor); err != nil {
			return nil, fmt.Errorf("workspace.context: continuation for repository %q: %w", continuation.RepositoryID, err)
		}
		if _, ok := byID[continuation.RepositoryID]; !ok {
			return nil, fmt.Errorf("workspace.context: cursor belongs to unknown repository %q", continuation.RepositoryID)
		}
		if _, ok := seen[continuation.RepositoryID]; ok {
			return nil, fmt.Errorf("workspace.context: duplicate cursor for repository %q", continuation.RepositoryID)
		}
		seen[continuation.RepositoryID] = struct{}{}
		result[continuation.RepositoryID] = continuation.Cursor
	}
	return result, nil
}

func validateOpaqueCursor(cursor string) error {
	if len(cursor) > maxOpaqueCursorBytes {
		return fmt.Errorf("cursor exceeds maximum size of %d bytes", maxOpaqueCursorBytes)
	}
	return nil
}

func boundedOpaqueCursor(cursor string) (string, bool) {
	if cursor == "" {
		return "", false
	}
	if validateOpaqueCursor(cursor) != nil {
		return "", true
	}
	return cursor, false
}

// CanonicalRoot is part of ATENEA's public orchestration contract.
func CanonicalRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("root is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("root does not exist")
		}
		return "", err
	}
	info, err := os.Stat(physical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("root is not a directory")
	}
	return filepath.Clean(physical), nil
}

func realBudget(value float64) bool { return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) }

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func safeDiagnostic(raw string) string { return contract.RedactRaw(strings.TrimSpace(raw)) }

func safeDiagnosticFor(raw, root, repositoryID string, aliases ...string) (string, bool) {
	return sanitizeText(raw, root, repositoryID, aliases...)
}

func safeNotices(notices []string, root, repositoryID string, aliases ...string) ([]string, bool) {
	if len(notices) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(notices))
	loss := false
	for _, notice := range notices {
		if safe, truncated := sanitizeText(notice, root, repositoryID, aliases...); safe != "" {
			out = append(out, safe)
			loss = loss || truncated
		}
	}
	return out, loss
}

func sanitizeResult(result map[string]any, root, repositoryID string, aliases ...string) (map[string]any, bool) {
	if result == nil {
		return nil, false
	}
	state := sanitizeState{active: make(map[sanitizeVisit]bool)}
	raw, complete := sanitizeNested(result, root, repositoryID, 0, &state, aliases...)
	sanitized, _ := raw.(map[string]any)
	return sanitized, !complete
}

func sanitizeEvidence(evidence []contract.QueryEvidence, root, repositoryID string, aliases ...string) ([]contract.QueryEvidence, bool, bool) {
	if len(evidence) == 0 {
		return nil, false, false
	}
	out := slices.Clone(evidence)
	loss := false
	cursorLoss := false
	for i := range out {
		var truncated bool
		out[i].Tool, truncated = sanitizeText(out[i].Tool, root, repositoryID, aliases...)
		loss = loss || truncated
		out[i].NextCursor, truncated = boundedOpaqueCursor(out[i].NextCursor)
		cursorLoss = cursorLoss || truncated
	}
	return out, loss, cursorLoss
}

const (
	maxSanitizeDepth = 16
	maxSanitizeNodes = 4096
	maxSanitizeItems = 256
)

type sanitizeState struct {
	nodes  int
	active map[sanitizeVisit]bool
}

type sanitizeVisit struct {
	kind reflect.Kind
	typ  reflect.Type
	ptr  uintptr
	len  int
	cap  int
}

// sanitizeNested handles the JSON-shaped values used by capability results.
// A depth/node ceiling also makes malicious cyclic in-memory maps safe: the
// public envelope is bounded even when a trusted test double returns a cycle.
func sanitizeNested(value any, root, repositoryID string, depth int, state *sanitizeState, aliases ...string) (any, bool) {
	result, complete, _ := sanitizeReflect(reflect.ValueOf(value), root, repositoryID, depth, state, maxSanitizeNodes, aliases...)
	return result, complete
}

// sanitizeReflect converts provider values into the small JSON-shaped subset
// accepted by the public workspace envelope. Reflection is intentional here:
// adapters commonly return named map and slice types, which a type switch
// would otherwise pass through verbatim and could expose paths or secrets.
func sanitizeReflect(value reflect.Value, root, repositoryID string, depth int, state *sanitizeState, budget int, aliases ...string) (any, bool, int) {
	if !value.IsValid() {
		return nil, true, 0
	}
	if depth > maxSanitizeDepth || budget <= 0 || state.nodes >= maxSanitizeNodes {
		return "[nested value omitted]", false, 0
	}
	state.nodes++
	used := 1
	budget--
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil, true, used
		}
		result, complete, childUsed := sanitizeReflect(value.Elem(), root, repositoryID, depth+1, state, budget, aliases...)
		return result, complete, used + childUsed
	case reflect.String:
		text, truncated := sanitizeText(value.String(), root, repositoryID, aliases...)
		return text, !truncated, used
	case reflect.Bool:
		return value.Bool(), true, used
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), true, used
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true, used
	case reflect.Float32, reflect.Float64:
		result := value.Float()
		if math.IsNaN(result) || math.IsInf(result, 0) {
			return "[scalar omitted]", false, used
		}
		return result, true, used
	case reflect.Pointer:
		if value.IsNil() {
			return nil, true, used
		}
		return sanitizeComposite(value, root, repositoryID, depth, state, used, budget, func(childBudget int) (any, bool, int) {
			return sanitizeReflect(value.Elem(), root, repositoryID, depth+1, state, childBudget, aliases...)
		})
	case reflect.Map:
		if value.IsNil() {
			return nil, true, used
		}
		if value.Type().Key().Kind() != reflect.String {
			return "[map omitted: non-string key]", false, used
		}
		return sanitizeComposite(value, root, repositoryID, depth, state, used, budget, func(childBudget int) (any, bool, int) {
			type entry struct {
				raw string
				key reflect.Value
			}
			// MapRange avoids materializing an attacker-controlled number of
			// keys. Keep only the lexicographically smallest bounded prefix;
			// insertion keeps the retained set deterministic independent of
			// map iteration order.
			entries := make([]entry, 0, min(value.Len(), maxSanitizeItems))
			iter := value.MapRange()
			for iter.Next() {
				item := entry{raw: iter.Key().String(), key: iter.Key()}
				index := sort.Search(len(entries), func(index int) bool { return item.raw < entries[index].raw })
				if len(entries) == maxSanitizeItems && index == len(entries) {
					continue
				}
				if len(entries) < maxSanitizeItems {
					entries = append(entries, entry{})
				}
				copy(entries[index+1:], entries[index:])
				entries[index] = item
				if len(entries) > maxSanitizeItems {
					entries = entries[:maxSanitizeItems]
				}
			}
			out := make(map[string]any, min(len(entries), maxSanitizeItems)+1)
			limit := min(len(entries), maxSanitizeItems)
			complete := true
			consumed := 0
			stopped := false
			collision := false
			for index, item := range entries[:limit] {
				child := value.MapIndex(item.key)
				safeKey, keyTruncated := sanitizeText(item.raw, root, repositoryID, aliases...)
				itemBudget := fairSanitizeBudget(childBudget, consumed, limit-index)
				if itemBudget == 0 {
					stopped = true
					complete = false
					break
				}
				var safeValue any
				var childComplete bool
				var childUsed int
				if isOpaqueCursorKey(item.raw) {
					safeValue, childComplete, childUsed = sanitizeOpaqueCursor(child, state, itemBudget)
				} else {
					safeValue, childComplete, childUsed = sanitizeReflect(child, root, repositoryID, depth+1, state, itemBudget, aliases...)
				}
				consumed += childUsed
				complete = complete && !keyTruncated && childComplete
				if _, exists := out[safeKey]; exists {
					collision = true
				} else {
					out[safeKey] = safeValue
				}
				if childUsed == 0 {
					stopped = true
					complete = false
					break
				}
			}
			if stopped || collision || value.Len() > limit {
				addCollectionMarker(out, state, &consumed, childBudget)
				complete = false
			}
			return out, complete, consumed
		})
	case reflect.Slice, reflect.Array:
		return sanitizeComposite(value, root, repositoryID, depth, state, used, budget, func(childBudget int) (any, bool, int) {
			limit := min(value.Len(), maxSanitizeItems)
			out := make([]any, 0, limit+1)
			complete := true
			consumed := 0
			stopped := false
			for i := 0; i < limit; i++ {
				itemBudget := fairSanitizeBudget(childBudget, consumed, limit-i)
				if itemBudget == 0 {
					stopped = true
					complete = false
					break
				}
				item, itemComplete, itemUsed := sanitizeReflect(value.Index(i), root, repositoryID, depth+1, state, itemBudget, aliases...)
				consumed += itemUsed
				complete = complete && itemComplete
				out = append(out, item)
				if itemUsed == 0 {
					stopped = true
					complete = false
					break
				}
			}
			if stopped || value.Len() > limit {
				if addCollectionMarker(nil, state, &consumed, childBudget) {
					out = append(out, "[collection truncated]")
				}
				complete = false
			}
			return out, complete, consumed
		})
	default:
		// Functions, channels, unsafe pointers, complex numbers and other
		// runtime values are not JSON-safe. Never return them verbatim.
		return "[value omitted: unsupported type]", false, used
	}
}

func isOpaqueCursorKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "cursor", "next_cursor", "continuation_cursor":
		return true
	default:
		return false
	}
}

// sanitizeOpaqueCursor preserves provider cursors byte-for-byte. Cursors are
// continuation contracts, rather than diagnostics: redaction or path
// replacement would make a valid next request impossible.
func sanitizeOpaqueCursor(value reflect.Value, state *sanitizeState, budget int) (any, bool, int) {
	if !value.IsValid() {
		return nil, true, 0
	}
	if budget <= 0 || state.nodes >= maxSanitizeNodes {
		return "[nested value omitted]", false, 0
	}
	state.nodes++
	used := 1
	budget--
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil, true, used
		}
		cursor, complete, nestedUsed := sanitizeOpaqueCursor(value.Elem(), state, budget)
		return cursor, complete, used + nestedUsed
	}
	if value.Kind() == reflect.String {
		cursor := value.String()
		if len(cursor) > maxOpaqueCursorBytes {
			return "[cursor omitted: exceeds maximum size]", false, used
		}
		return cursor, true, used
	}
	return "[cursor omitted: expected string]", false, used
}

func sanitizeComposite(value reflect.Value, root, repositoryID string, depth int, state *sanitizeState, used, budget int, visit func(int) (any, bool, int)) (any, bool, int) {
	visitID, ok := sanitizeVisitID(value)
	if ok {
		if state.active == nil {
			state.active = make(map[sanitizeVisit]bool)
		}
		if state.active[visitID] {
			return "[cyclic value omitted]", false, used
		}
		state.active[visitID] = true
		defer delete(state.active, visitID)
	}
	result, complete, nestedUsed := visit(budget)
	return result, complete, used + nestedUsed
}

func fairSanitizeBudget(available, used int, remaining int) int {
	if remaining <= 0 || available <= used {
		return 0
	}
	budget := (available - used) / remaining
	if budget == 0 {
		return 1
	}
	return budget
}

func addCollectionMarker(output map[string]any, state *sanitizeState, consumed *int, budget int) bool {
	if budget <= *consumed || state.nodes >= maxSanitizeNodes {
		return false
	}
	marker := ""
	if output != nil {
		const markerBase = "[atenea sanitized]"
		for suffix := 0; suffix <= maxSanitizeItems; suffix++ {
			candidate := markerBase
			if suffix > 0 {
				candidate = fmt.Sprintf("%s-%d", markerBase, suffix)
			}
			if _, exists := output[candidate]; !exists {
				marker = candidate
				break
			}
		}
		if marker == "" {
			return false
		}
	}
	*consumed++
	state.nodes++
	// The marker is counted as one emitted scalar and is inserted at most once
	// for each collection, even when many remaining children are omitted.
	if output != nil {
		output[marker] = "[nested value omitted]"
	}
	return true
}

func sanitizeVisitID(value reflect.Value) (sanitizeVisit, bool) {
	switch value.Kind() {
	case reflect.Pointer, reflect.Map:
		return sanitizeVisit{kind: value.Kind(), typ: value.Type(), ptr: value.Pointer()}, value.Pointer() != 0
	case reflect.Slice:
		if value.IsNil() {
			return sanitizeVisit{}, false
		}
		return sanitizeVisit{kind: value.Kind(), typ: value.Type(), ptr: value.Pointer(), len: value.Len(), cap: value.Cap()}, value.Pointer() != 0
	default:
		return sanitizeVisit{}, false
	}
}

func sanitizeText(raw, root, repositoryID string, aliases ...string) (string, bool) {
	text := contract.RedactRaw(raw)
	truncated := strings.HasSuffix(text, "\n[TRUNCATED]")
	if text == "" || repositoryID == "" {
		return text, truncated
	}
	text = filepath.ToSlash(text)
	roots := append([]string{root}, aliases...)
	for _, candidate := range roots {
		candidate = filepath.ToSlash(filepath.Clean(candidate))
		if candidate == "" || candidate == "." {
			continue
		}
		for {
			index := strings.Index(text, candidate)
			if index < 0 || !pathBoundary(text, index, len(candidate)) {
				break
			}
			text = text[:index] + repositoryID + text[index+len(candidate):]
		}
	}
	return text, truncated
}

func pathBoundary(text string, start, length int) bool {
	if start > 0 {
		previous := text[start-1]
		if (previous >= 'a' && previous <= 'z') || (previous >= 'A' && previous <= 'Z') || (previous >= '0' && previous <= '9') || previous == '_' {
			return false
		}
	}
	end := start + length
	if end < len(text) {
		next := text[end]
		if next != '/' && ((next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') || (next >= '0' && next <= '9') || next == '_') {
			return false
		}
	}
	return true
}

func incompleteEvidence(evidence []contract.QueryEvidence, result map[string]any) bool {
	if structuralTruncated(evidence, result) {
		return true
	}
	for _, item := range evidence {
		if strings.EqualFold(item.Completeness, "partial") || strings.EqualFold(item.Completeness, "lower_bound") || strings.EqualFold(item.Completeness, "unknown") || strings.EqualFold(item.Completeness, "truncated") {
			return true
		}
	}
	return false
}

func structuralTruncated(evidence []contract.QueryEvidence, result map[string]any) bool {
	if contract.StructuralPartial(result) {
		return true
	}
	for _, item := range evidence {
		if item.Truncated {
			return true
		}
	}
	return false
}
