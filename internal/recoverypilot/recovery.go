// Package recoverypilot contains the bounded, evidence-carrying recovery
// policy used by the P10 fixture pilot. It is deliberately independent of a
// provider: production adapters can supply Runner and AttemptStore without
// giving recovery permission to choose another model or route.
package recoverypilot

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/adapter/kivgraph"
	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/supervisor"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// EvidenceLevel is part of ATENEA's public orchestration contract.
type EvidenceLevel string

const (
	// EvidenceFixture is part of ATENEA's public orchestration contract.
	EvidenceFixture EvidenceLevel = "fixture"
	// EvidenceCopy is part of ATENEA's public orchestration contract.
	EvidenceCopy EvidenceLevel = "copy"
	// EvidenceReal is part of ATENEA's public orchestration contract.
	EvidenceReal EvidenceLevel = "real"
	// EvidenceUnknown is part of ATENEA's public orchestration contract.
	EvidenceUnknown EvidenceLevel = "unknown"
)

// Valid is part of ATENEA's public orchestration contract.
func (e EvidenceLevel) Valid() bool {
	return e == EvidenceFixture || e == EvidenceCopy || e == EvidenceReal || e == EvidenceUnknown
}

// Route is immutable recovery identity. A retry must compare every field and
// reuse this value; recovery never selects a fallback model or provider.
type Route struct {
	ID              string `json:"id"`
	Backend         string `json:"backend"`
	Provider        string `json:"provider"`
	RequestedModel  string `json:"requested_model"`
	ObservedModel   string `json:"observed_model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// Valid is part of ATENEA's public orchestration contract.
func (r Route) Valid() error {
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.Backend) == "" || strings.TrimSpace(r.RequestedModel) == "" {
		return errors.New("recovery route requires id, backend and requested model")
	}
	return nil
}

// Same is part of ATENEA's public orchestration contract.
func (r Route) Same(other Route) bool {
	return r.ID == other.ID && r.Backend == other.Backend && r.Provider == other.Provider && r.RequestedModel == other.RequestedModel && r.ObservedModel == other.ObservedModel && r.ReasoningEffort == other.ReasoningEffort
}

// Result is part of ATENEA's public orchestration contract.
type Result struct {
	Status  string
	Failure contract.FailureKind
	Route   Route
	CostUSD *float64
	// InvokedKnown distinguishes a preflight/down result from a provider
	// invocation. A known zero cost is still an invocation when Invoked is
	// true; an unknown invocation is kept conservative and never certified as
	// a free call.
	Invoked      bool
	InvokedKnown bool
	Duration     time.Duration
	Effects      []contract.Effect
	Operations   []contract.Operation
	External     bool
	Canceled     bool
	Operation    bool
	Output       map[string]any
}

// Runner is part of ATENEA's public orchestration contract.
type Runner interface {
	Run(context.Context, Route) (Result, error)
}

// Attempt is part of ATENEA's public orchestration contract.
type Attempt struct {
	Number          int      `json:"number"`
	Route           Route    `json:"route"`
	Status          string   `json:"status"`
	Failure         string   `json:"failure,omitempty"`
	CostUSD         *float64 `json:"cost_usd,omitempty"`
	CostKnown       bool     `json:"cost_known"`
	Invoked         bool     `json:"invoked"`
	InvokedKnown    bool     `json:"invoked_known"`
	DurationMS      int64    `json:"duration_ms"`
	RetryUsed       int      `json:"retry_used"`
	RetryLimit      int      `json:"retry_limit"`
	PersistedBefore bool     `json:"persisted_before_next"`
	Reason          string   `json:"reason,omitempty"`
}

// AttemptStore is part of ATENEA's public orchestration contract.
type AttemptStore interface {
	PersistAttempt(context.Context, Attempt) error
}

// RebuildRequest describes the only rebuild shape recovery may admit. A
// recovery probe cannot silently turn a partial or incremental operation into
// a write: a full rebuild and an explicit or standing authorization must be
// visible in the request itself.
type RebuildRequest struct {
	Root                  string
	Generation            string
	Mode                  string
	ExplicitAuthorization bool
	StandingAuthorization bool
}

// RebuildLease is an in-process lock for one repository generation. It is a
// small provider-neutral seam used by supervisor/Kivgraph adapters; it does
// not claim that an index exists until Finish receives a positive count.
type RebuildLease struct {
	coordinator *RebuildCoordinator
	root        string
	generation  string
	coalesced   bool
	done        bool
}

// RebuildCoordinator prevents duplicate rebuild writers and records the last
// completed generation. A concurrent request for the same root is coalesced;
// a different generation is rejected while the writer is active.
type RebuildCoordinator struct {
	mu        sync.Mutex
	active    map[string]string
	completed map[string]string
}

// Start is part of ATENEA's public orchestration contract.
func (c *RebuildCoordinator) Start(request RebuildRequest) (RebuildLease, error) {
	root, err := filepath.Abs(filepath.Clean(request.Root))
	if err != nil || strings.TrimSpace(request.Root) == "" || root == "." {
		return RebuildLease{}, errors.New("rebuild root is required")
	}
	if request.Mode != "full" {
		return RebuildLease{}, errors.New("recovery only admits full rebuilds")
	}
	if strings.TrimSpace(request.Generation) == "" {
		return RebuildLease{}, errors.New("rebuild generation is required")
	}
	if !request.ExplicitAuthorization && !request.StandingAuthorization {
		return RebuildLease{}, errors.New("rebuild requires explicit or standing authorization")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		c.active = make(map[string]string)
	}
	if c.completed == nil {
		c.completed = make(map[string]string)
	}
	if generation, ok := c.active[root]; ok {
		if generation == request.Generation {
			return RebuildLease{coordinator: c, root: root, generation: generation, coalesced: true}, nil
		}
		return RebuildLease{}, fmt.Errorf("rebuild already active for %s at generation %s", root, generation)
	}
	if c.completed[root] == request.Generation {
		return RebuildLease{}, fmt.Errorf("rebuild generation %s is already complete", request.Generation)
	}
	c.active[root] = request.Generation
	return RebuildLease{coordinator: c, root: root, generation: request.Generation}, nil
}

// Finish publishes a generation only when the provider reports a non-zero
// index. A zero count is evidence of no index, never a successful rebuild.
func (l *RebuildLease) Finish(indexCount int) error {
	if l == nil || l.coordinator == nil {
		return errors.New("invalid rebuild lease")
	}
	if l.coalesced {
		return errors.New("coalesced rebuild has no writer lease")
	}
	if l.done {
		return errors.New("rebuild lease already closed")
	}
	if indexCount <= 0 {
		return errors.New("rebuild produced no index evidence")
	}
	l.coordinator.mu.Lock()
	defer l.coordinator.mu.Unlock()
	if l.coordinator.active[l.root] != l.generation {
		return errors.New("rebuild lease is no longer active")
	}
	delete(l.coordinator.active, l.root)
	l.coordinator.completed[l.root] = l.generation
	l.done = true
	return nil
}

// Abort releases a writer without publishing a generation.
func (l *RebuildLease) Abort() {
	if l == nil || l.coordinator == nil || l.coalesced || l.done {
		return
	}
	l.coordinator.mu.Lock()
	if l.coordinator.active[l.root] == l.generation {
		delete(l.coordinator.active, l.root)
	}
	l.coordinator.mu.Unlock()
	l.done = true
}

// Policy is part of ATENEA's public orchestration contract.
type Policy struct {
	MaxRetries    int
	BudgetUSD     float64
	MaxDuration   time.Duration
	ReadOnly      bool
	Authorization string
	// AttemptBudgetUSD is an optional provider/step spending ceiling. Reaching
	// it is a terminal ceiling result, never a reason to dispatch again.
	AttemptBudgetUSD float64
}

// Request is part of ATENEA's public orchestration contract.
type Request struct {
	RunID      string
	WorkflowID string
	Route      Route
	Effects    []contract.Effect
	Operations []contract.Operation
	External   bool
	Policy     Policy
	Evidence   EvidenceLevel
}

// Report is part of ATENEA's public orchestration contract.
type Report struct {
	RunID          string        `json:"run_id"`
	WorkflowID     string        `json:"workflow_id"`
	Evidence       EvidenceLevel `json:"evidence"`
	Status         string        `json:"status"`
	Reason         string        `json:"reason"`
	NextOrStopped  string        `json:"next_or_stopped,omitempty"`
	RequestedModel string        `json:"requested_model"`
	ObservedModel  string        `json:"observed_model"`
	Backend        string        `json:"backend"`
	Provider       string        `json:"provider"`
	RetryUsed      int           `json:"retry_used"`
	RetryLimit     int           `json:"retry_limit"`
	Attempts       []Attempt     `json:"attempts"`
	SpentUSD       float64       `json:"spent_usd"`
	BudgetUSD      float64       `json:"budget_usd"`
	DurationMS     int64         `json:"duration_ms"`
	Scenarios      []Scenario    `json:"scenarios,omitempty"`
}

// Scenario is part of ATENEA's public orchestration contract.
type Scenario struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Status   string        `json:"status"`
	Detail   string        `json:"detail"`
	Evidence EvidenceLevel `json:"evidence"`
}

// Markdown is part of ATENEA's public orchestration contract.
func (r Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Recovery pilot\n\nEvidence: `%s` · status: `%s` · retry: `%d/%d`\n\n", r.Evidence, r.Status, r.RetryUsed, r.RetryLimit)
	fmt.Fprintf(&b, "Route: `%s` / backend `%s` / provider `%s` / requested model `%s` / observed model `%s`\n\n", r.RouteID(), r.Backend, r.Provider, r.RequestedModel, r.ObservedModel)
	fmt.Fprintf(&b, "Reason: %s\n\n", r.Reason)
	b.WriteString("| Attempt | Status | Failure | Cost | Retry | Route | Reason |\n|---:|---|---|---:|---:|---|---|\n")
	for _, a := range r.Attempts {
		cost := "unknown"
		if a.CostKnown && a.CostUSD != nil {
			cost = fmt.Sprintf("$%.2f", *a.CostUSD)
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %d/%d | %s | %s |\n", a.Number, a.Status, a.Failure, cost, a.RetryUsed, a.RetryLimit, a.Route.ID, a.Reason)
	}
	if len(r.Scenarios) > 0 {
		b.WriteString("\n| Matrix | Status | Evidence | Detail |\n|---|---|---|---|\n")
		for _, s := range r.Scenarios {
			fmt.Fprintf(&b, "| %s %s | %s | %s | %s |\n", s.ID, s.Name, s.Status, s.Evidence, strings.ReplaceAll(strings.ReplaceAll(s.Detail, "|", "\\|"), "\n", " "))
		}
	}
	return b.String()
}

// RouteID is part of ATENEA's public orchestration contract.
func (r Report) RouteID() string {
	if len(r.Attempts) == 0 {
		return ""
	}
	return r.Attempts[0].Route.ID
}

// JSON is part of ATENEA's public orchestration contract.
func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Options is part of ATENEA's public orchestration contract.
type Options struct {
	Runner Runner
	Store  AttemptStore
	Now    func() time.Time
}

// Execute is part of ATENEA's public orchestration contract.
func Execute(ctx context.Context, request Request, options Options) (Report, error) {
	if options.Runner == nil || options.Store == nil {
		return Report{}, errors.New("recovery requires runner and durable attempt store")
	}
	if err := request.Route.Valid(); err != nil {
		return Report{}, err
	}
	if request.Evidence == "" {
		request.Evidence = EvidenceUnknown
	}
	if !request.Evidence.Valid() {
		return Report{}, fmt.Errorf("unknown evidence level %q", request.Evidence)
	}
	if request.Policy.MaxRetries < 0 || request.Policy.BudgetUSD < 0 || request.Policy.MaxDuration < 0 {
		return Report{}, errors.New("recovery policy limits cannot be negative")
	}
	if !request.Policy.ReadOnly || request.External || len(request.Operations) > 0 || hasWriteEffect(request.Effects) {
		return Report{}, errors.New("recovery pilot accepts read-only work only")
	}
	if request.Policy.MaxDuration == 0 {
		return Report{}, errors.New("recovery requires a verifiable max duration")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	started := now()
	deadline := started.Add(request.Policy.MaxDuration)
	report := Report{RunID: request.RunID, WorkflowID: request.WorkflowID, Evidence: request.Evidence, Status: "unknown", RequestedModel: request.Route.RequestedModel, Backend: request.Route.Backend, Provider: request.Route.Provider, RetryLimit: request.Policy.MaxRetries, BudgetUSD: request.Policy.BudgetUSD}
	var spent float64
	var elapsed time.Duration
	route := request.Route
	for number := 1; number <= request.Policy.MaxRetries+1; number++ {
		if err := ctx.Err(); err != nil {
			report.Status, report.Reason = "canceled", "caller canceled recovery before the next attempt"
			break
		}
		remaining := request.Policy.MaxDuration - elapsed
		if options.Now == nil {
			if wallRemaining := time.Until(deadline); wallRemaining < remaining {
				remaining = wallRemaining
			}
		}
		if remaining <= 0 {
			report.Status, report.Reason = "blocked", "duration limit reached before the next attempt"
			break
		}
		attemptCtx, cancel := context.WithTimeout(ctx, remaining)
		attemptStarted := time.Now()
		result, runErr := options.Runner.Run(attemptCtx, route)
		attemptErr := attemptCtx.Err()
		callerCanceled := ctx.Err() != nil || errors.Is(attemptErr, context.Canceled)
		attemptTimedOut := errors.Is(attemptErr, context.DeadlineExceeded)
		attemptDuration := result.Duration
		if attemptDuration <= 0 && options.Now == nil {
			attemptDuration = time.Since(attemptStarted)
		}
		if attemptDuration < 0 {
			attemptDuration = 0
		}
		elapsed += attemptDuration
		cancel()
		if result.Route.ID == "" {
			result.Route = request.Route
		}
		if report.ObservedModel == "" {
			report.ObservedModel = result.Route.ObservedModel
		}
		failure := result.Failure
		if failure == contract.FailureUnspecified && runErr != nil {
			failure = contract.KindOf(runErr)
		}
		if failure == contract.FailureUnspecified && result.Canceled {
			failure = contract.FailureCanceled
		}
		if failure == contract.FailureUnspecified && attemptTimedOut {
			failure = contract.FailureTimeout
		}
		if failure == contract.FailureUnspecified && runErr != nil {
			failure = contract.FailureUnavailable
		}
		// A runner is not allowed to turn work that was canceled by the caller
		// into a certified success merely by returning an otherwise successful
		// result after its context was canceled.
		if callerCanceled {
			failure = contract.FailureCanceled
			result.Canceled = true
		}
		costKnown := result.CostUSD != nil && validCost(result.CostUSD)
		costInvalid := result.CostUSD != nil && !validCost(result.CostUSD)
		status := result.Status
		if callerCanceled {
			status = "canceled"
		}
		if status == "" {
			// An empty status is a failed attempt when its failure is known;
			// without a failure it is incomplete evidence. It is never an
			// implicit success.
			status = "incomplete"
			if failure != contract.FailureUnspecified {
				status = "failed"
			}
		}
		if costInvalid && !callerCanceled {
			failure = contract.FailureInvalidInput
			status = "blocked"
		}
		if costKnown {
			spent += *result.CostUSD
		}
		attemptCost := result.CostUSD
		if costInvalid {
			// JSON cannot represent NaN or infinities. Drop malformed provider
			// accounting before the durable receipt is built; the failure and
			// reason below remain the evidence that the charge was invalid.
			attemptCost = nil
		}
		attempt := Attempt{Number: number, Route: result.Route, Status: status, Failure: failure.String(), CostUSD: attemptCost, CostKnown: costKnown, Invoked: result.Invoked, InvokedKnown: result.InvokedKnown, DurationMS: attemptDuration.Milliseconds(), RetryUsed: number - 1, RetryLimit: request.Policy.MaxRetries}
		if costInvalid && !callerCanceled {
			attempt.Reason = "cost invalid; recovery stopped before another attempt"
		}
		sameRoute := result.Route.ID == route.ID && result.Route.Backend == route.Backend && result.Route.Provider == route.Provider && result.Route.RequestedModel == route.RequestedModel && result.Route.ReasoningEffort == route.ReasoningEffort && observedModelSame(route.ObservedModel, result.Route.ObservedModel, report.ObservedModel)
		ceilingReached := request.Policy.AttemptBudgetUSD > 0 && costKnown && *result.CostUSD >= request.Policy.AttemptBudgetUSD
		// Cost validity is established before interpreting invocation metadata:
		// a malformed charge can never be turned into a free preflight retry.
		preflight := !costInvalid && result.InvokedKnown && !result.Invoked
		preflightRetry := preflight && (result.CostUSD == nil || *result.CostUSD == 0)
		invokedRetry := !costInvalid && result.InvokedKnown && result.Invoked && costKnown
		// A confirmed preflight failure is retryable without a charge: the
		// provider was never invoked, so there is no unknown spend to protect.
		// Once invocation started, a missing or invalid charge remains a durable
		// stop condition and must not be retried.
		willRetry := sameRoute && transient(failure) && (preflightRetry || invokedRetry) && !ceilingReached && !result.External && !result.Operation && len(result.Operations) == 0 && !hasWriteEffect(result.Effects) && number < request.Policy.MaxRetries+1 && (request.Policy.BudgetUSD <= 0 || spent < request.Policy.BudgetUSD) && elapsed < request.Policy.MaxDuration
		if willRetry {
			attempt.PersistedBefore = true
			attempt.Reason = "persisted transient failure before retrying the same route"
		}
		// The result is already in hand. Persist its accounting even when the
		// provider context was canceled; otherwise a cancellation can erase the
		// very attempt that explains why no retry was dispatched.
		if err := options.Store.PersistAttempt(context.WithoutCancel(ctx), attempt); err != nil {
			return report, fmt.Errorf("persist recovery attempt %d: %w", number, err)
		}
		report.Attempts = append(report.Attempts, attempt)
		report.RetryUsed = number - 1
		if costInvalid && !callerCanceled {
			report.Status, report.Reason = "blocked", "cost invalid; recovery stopped before another attempt"
			break
		}
		if !sameRoute {
			report.Status, report.Reason = "blocked", "retry route/backend/model changed; no fallback was selected"
			break
		}
		if failure == contract.FailureUnspecified || failure == 0 {
			if status == "incomplete" || status == "partial" {
				report.Status, report.Reason = status, "result was incomplete without a classified failure; no automatic retry"
			} else if status != "ok" {
				report.Status, report.Reason = "failed", "failure kind was not classified; no automatic retry"
			} else {
				report.Status, report.Reason = "ok", "completed on the selected route"
			}
			break
		}
		if failure == contract.FailureCanceled || errors.Is(runErr, context.Canceled) || ctx.Err() != nil {
			report.Status, report.Reason = "canceled", "caller canceled recovery; no retry"
			break
		}
		if !transient(failure) {
			report.Status, report.Reason = "failed", failure.String()+" is not an automatic recovery condition"
			break
		}
		if result.External || result.Operation || len(result.Operations) > 0 || hasWriteEffect(result.Effects) {
			report.Status, report.Reason = "blocked", "effectful or external result; no automatic retry"
			break
		}
		if ceilingReached {
			report.Status, report.Reason = "blocked", "spending ceiling reached; recovery stopped before another attempt"
			break
		}
		if !result.InvokedKnown {
			report.Status, report.Reason = "blocked", "invocation state unknown; recovery stopped before another attempt"
			break
		}
		if preflight && costKnown && *result.CostUSD != 0 {
			report.Status, report.Reason = "blocked", "preflight reported a non-zero cost; recovery stopped before another attempt"
			break
		}
		if !preflight && !costKnown {
			report.Status, report.Reason = "blocked", "cost unknown; recovery stopped before another attempt"
			if costInvalid {
				report.Reason = "cost invalid; recovery stopped before another attempt"
			}
			break
		}
		if request.Policy.BudgetUSD > 0 && spent >= request.Policy.BudgetUSD {
			report.Status, report.Reason = "blocked", "budget exhausted before another attempt"
			break
		}
		if number >= request.Policy.MaxRetries+1 {
			report.Status, report.Reason = "failed", "transient failure exhausted the retry limit"
			break
		}
		if elapsed >= request.Policy.MaxDuration {
			report.Status, report.Reason = "blocked", "duration limit reached before another attempt"
			break
		}
		report.Reason = "persisted transient failure; retrying the same route"
		if route.ObservedModel == "" && result.Route.ObservedModel != "" {
			route.ObservedModel = result.Route.ObservedModel
		}
	}
	if report.Status == "unknown" {
		report.Reason = "no attempt completed"
	}
	report.SpentUSD = spent
	switch report.Status {
	case "ok":
		report.NextOrStopped = "complete"
	case "canceled":
		report.NextOrStopped = "stopped:canceled"
	default:
		report.NextOrStopped = "stopped:" + report.Reason
	}
	if elapsed > 0 {
		report.DurationMS = elapsed.Milliseconds()
	} else if !started.IsZero() && options.Now == nil {
		report.DurationMS = now().Sub(started).Milliseconds()
	}
	return report, nil
}

func validCost(cost *float64) bool {
	return cost != nil && *cost >= 0 && !math.IsNaN(*cost) && !math.IsInf(*cost, 0)
}

func observedModelSame(requested, observed, firstObserved string) bool {
	if requested != "" {
		return observed == requested
	}
	if firstObserved != "" {
		return observed == firstObserved
	}
	return observed == ""
}

func transient(kind contract.FailureKind) bool {
	return kind == contract.FailureUnavailable || kind == contract.FailureTimeout
}

func hasWriteEffect(effects []contract.Effect) bool {
	for _, effect := range effects {
		if effect != contract.EffectRead {
			return true
		}
	}
	return false
}

// FileStore is part of ATENEA's public orchestration contract.

// FileStore is part of ATENEA's public orchestration contract.
type FileStore struct {
	Path string
	mu   sync.Mutex
}

// PersistAttempt is part of ATENEA's public orchestration contract.

// PersistAttempt is part of ATENEA's public orchestration contract.
func (s *FileStore) PersistAttempt(ctx context.Context, attempt Attempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(s.Path) == "" {
		return errors.New("attempt store path is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := json.NewEncoder(f).Encode(attempt); err != nil {
		return err
	}
	return f.Sync()
}

// LoadAttempts is part of ATENEA's public orchestration contract.

// LoadAttempts is part of ATENEA's public orchestration contract.
func LoadAttempts(path string) ([]Attempt, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Attempt
	scanner := bufio.NewScanner(io.LimitReader(f, 1<<20))
	for scanner.Scan() {
		var attempt Attempt
		if err := json.Unmarshal(scanner.Bytes(), &attempt); err != nil {
			return nil, err
		}
		out = append(out, attempt)
	}
	return out, scanner.Err()
}

// HashFile is part of ATENEA's public orchestration contract.

// HashFile is part of ATENEA's public orchestration contract.
func HashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// FixtureOptions is part of ATENEA's public orchestration contract.

// FixtureOptions is part of ATENEA's public orchestration contract.
type FixtureOptions struct {
	Root     string
	Sentinel string
	// duringKivgraph is an internal adversarial fixture hook. It is kept
	// private so production callers cannot silently alter the external
	// sentinel while the managedFresh stage is running.
	duringKivgraph func()
}

// RunFixture is part of ATENEA's public orchestration contract.

// RunFixture is part of ATENEA's public orchestration contract.
func RunFixture(ctx context.Context, options FixtureOptions) (Report, error) {
	root := filepath.Clean(options.Root)
	sentinel := filepath.Clean(options.Sentinel)
	if root == "." || sentinel == "." || strings.TrimSpace(options.Root) == "" || strings.TrimSpace(options.Sentinel) == "" {
		return Report{}, errors.New("fixture requires root and pre-existing sentinel")
	}
	if _, err := os.Stat(sentinel); err != nil {
		return Report{}, fmt.Errorf("sentinel must pre-exist: %w", err)
	}
	rel, err := filepath.Rel(root, sentinel)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return Report{}, errors.New("sentinel must be outside the fixture root")
	}
	if info, err := os.Stat(root); err == nil {
		if !info.IsDir() {
			return Report{}, errors.New("fixture root must be a new or empty directory")
		}
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			return Report{}, fmt.Errorf("fixture root: %w", readErr)
		}
		if len(entries) != 0 {
			return Report{}, errors.New("fixture root must be a new or empty directory")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
			return Report{}, err
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			return Report{}, fmt.Errorf("fixture root: %w", err)
		}
	} else {
		return Report{}, fmt.Errorf("fixture root: %w", err)
	}
	before, err := HashFile(sentinel)
	if err != nil {
		return Report{}, err
	}
	statePath := filepath.Join(root, "recoverypilot-attempts.jsonl")
	if err := createExclusive(statePath, 0o600); err != nil {
		return Report{}, fmt.Errorf("fixture state: %w", err)
	}
	store := &FileStore{Path: statePath}
	route := Route{ID: "fixture-read", Backend: "fixture-backend", Provider: "fixture-provider", RequestedModel: "gpt-5.6-luna", ObservedModel: "gpt-5.6-luna", ReasoningEffort: "xhigh"}
	runner := &fixtureRunner{}
	report, err := Execute(ctx, Request{RunID: "fixture-run", WorkflowID: "fixture-workflow", Route: route, Effects: []contract.Effect{contract.EffectRead}, Policy: Policy{MaxRetries: 1, BudgetUSD: 1, MaxDuration: time.Second, ReadOnly: true}, Evidence: EvidenceFixture}, Options{Runner: runner, Store: store, Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	if err != nil {
		return Report{}, err
	}
	sentinelStable := true
	verifySentinel := func() error {
		current, hashErr := HashFile(sentinel)
		if hashErr != nil {
			return hashErr
		}
		if current != before {
			sentinelStable = false
		}
		return nil
	}
	if err := verifySentinel(); err != nil {
		return Report{}, err
	}
	budgetOK, budgetDetail := fixtureBudgetAndDuration(ctx)
	if err := verifySentinel(); err != nil {
		return Report{}, err
	}
	workflowResult, workflowDetail := runFixtureWorkflow(ctx, root)
	if err := verifySentinel(); err != nil {
		return Report{}, err
	}
	scenarios := []Scenario{
		{ID: "A", Name: "same-route transient retry", Status: statusFor(workflowResult.ok && len(report.Attempts) == 2 && report.Status == "ok"), Detail: workflowDetail, Evidence: EvidenceFixture},
		{ID: "B", Name: "attempt persistence", Status: statusFor(workflowResult.ok && workflowResult.archived == 1), Detail: "workflow engine archived the first attempt before the second dispatch", Evidence: EvidenceFixture},
		{ID: "C", Name: "read-only effects", Status: statusFor(report.Status == "ok"), Detail: "fixture carries read permission only", Evidence: EvidenceFixture},
		{ID: "D", Name: "budget and duration", Status: statusFor(budgetOK), Detail: budgetDetail, Evidence: EvidenceFixture},
		{ID: "E", Name: "sentinel isolation", Status: statusFor(sentinelStable), Detail: "external sentinel hash unchanged at the end of every fixture stage", Evidence: EvidenceFixture},
		{ID: "F", Name: "no model fallback", Status: statusFor(report.RequestedModel == report.ObservedModel), Detail: "requested and observed model stay on the selected route", Evidence: EvidenceFixture},
		{ID: "G", Name: "reopen", Status: statusFor(workflowResult.ok && workflowResult.reopened), Detail: "workflow store reopens the run without replaying an invocation", Evidence: EvidenceFixture},
	}
	supervisorOK, supervisorDetail := runFixtureSupervisor(ctx, root)
	if err := verifySentinel(); err != nil {
		return Report{}, err
	}
	scenarios = append(scenarios, Scenario{ID: "H", Name: "supervisor fixture", Status: statusFor(supervisorOK), Detail: supervisorDetail, Evidence: EvidenceFixture})
	indexOK, indexDetail := runFixtureKivgraph(ctx, root, options.duringKivgraph)
	if err := verifySentinel(); err != nil {
		return Report{}, err
	}
	scenarios[4].Status = statusFor(sentinelStable)
	scenarios = append(scenarios, Scenario{ID: "I", Name: "Kivgraph managedFresh", Status: statusFor(indexOK), Detail: indexDetail, Evidence: EvidenceFixture})
	report.Scenarios = scenarios
	report.Status = "pass"
	for _, scenario := range scenarios {
		if scenario.Status != "pass" {
			report.Status = "fail"
			break
		}
	}
	return report, nil
}

// createExclusive creates fixture-owned files without ever replacing a file
// supplied by the caller. RunFixture requires an empty root as a second,
// independent guard against accidental fixture writes.
func createExclusive(path string, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	return f.Close()
}

func writeExclusive(path string, contents []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(contents); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func statusFor(ok bool) string {
	if ok {
		return "pass"
	}
	return "fail"
}

type fixtureWorkflowResult struct {
	ok        bool
	archived  int
	reopened  bool
	lastError error
}

type fixtureWorkflowDispatcher struct{ calls int }

func (d *fixtureWorkflowDispatcher) NextID() string {
	return fmt.Sprintf("fixture-dispatch-%d", d.calls+1)
}

func (d *fixtureWorkflowDispatcher) Dispatch(ctx context.Context, assignment agent.Dispatch) (contract.Report, contract.Assignment, error) {
	if err := ctx.Err(); err != nil {
		return contract.Report{Verdict: contract.VerdictIncomplete, Reason: contract.Reason{Kind: contract.FailureCanceled, Text: "fixture canceled"}}, contract.Assignment{}, err
	}
	d.calls++
	spent := 0.10
	if d.calls == 1 {
		return contract.Report{Verdict: contract.VerdictIncomplete, Reason: contract.Reason{Kind: contract.FailureUnavailable, Text: "fixture backend unavailable"}, Spent: contract.Charge{USD: &spent, PricedBy: "fixture"}, Invoked: true, InvokedKnown: true}, contract.Assignment{}, contract.Fail(contract.FailureUnavailable, "fixture backend unavailable")
	}
	return contract.Report{Verdict: contract.VerdictOK, Result: map[string]any{"ok": true}, Spent: contract.Charge{USD: &spent, PricedBy: "fixture"}, Invoked: true, InvokedKnown: true}, contract.Assignment{}, nil
}

// runFixtureWorkflow drives the production workflow.Engine and its durable
// SQLite store. The dispatcher is a deterministic fixture seam; all retry,
// archive and reopen assertions therefore exercise the same engine used by a
// real workflow rather than a second recovery-only loop.
func runFixtureWorkflow(ctx context.Context, root string) (fixtureWorkflowResult, string) {
	path := filepath.Join(root, "workflow-fixture.db")
	store, err := workflow.Open(ctx, path)
	if err != nil {
		return fixtureWorkflowResult{lastError: err}, "workflow store could not open"
	}
	dispatcher := &fixtureWorkflowDispatcher{}
	typeDef := config.AgentType{
		Spec:    contract.AgentTypeSpec{Name: "fixture", Kind: contract.AgentSpecialized, Result: []contract.Field{{Name: "ok", Type: contract.TypeBool, Required: true}}},
		Command: "/bin/sh", Context: []contract.ContextLevel{contract.ContextRepository}, Effects: []contract.Effect{contract.EffectRead},
		Limits: contract.Limits{MaxDuration: time.Second, MaxTokens: 100}, Pool: config.PoolAgent,
	}
	engine, err := workflow.New(workflow.Options{Runner: dispatcher, Store: store, Types: []config.AgentType{typeDef}, Repository: "fixture", RepositoryRoot: root, MaxRetries: 1, MaxBudgetUSD: 1, PID: os.Getpid()})
	if err != nil {
		_ = store.Close()
		return fixtureWorkflowResult{lastError: err}, "workflow engine could not open"
	}
	step := workflow.Step{ID: "fixture-read", TypeName: "fixture", Task: contract.Task{Objective: "fixture read", Criterion: "fixture answers"}, Permission: contract.Permission{Effects: []contract.Effect{contract.EffectRead}}, Route: &contract.Route{Model: "gpt-5.6-luna", RequestedModel: "gpt-5.6-luna", ObservedModel: "gpt-5.6-luna", Backend: "fixture"}}
	run, err := engine.Start(ctx, workflow.Graph{Task: "fixture workflow", GrantUSD: 1, Steps: []workflow.Step{step}})
	if err != nil {
		_ = store.Close()
		return fixtureWorkflowResult{lastError: err}, "workflow engine did not complete: " + err.Error()
	}
	archived, archiveErr := store.Attempts(ctx, run.ID, step.ID)
	loaded, loadErr := store.Load(ctx, run.ID)
	if err := store.Close(); err != nil && loadErr == nil {
		loadErr = err
	}
	if loadErr != nil || archiveErr != nil {
		return fixtureWorkflowResult{lastError: errors.Join(loadErr, archiveErr)}, "workflow archive/reopen read failed"
	}
	reopenedStore, err := workflow.Open(ctx, path)
	if err != nil {
		return fixtureWorkflowResult{lastError: err}, "workflow store could not reconnect"
	}
	_, reopenErr := reopenedStore.Load(ctx, run.ID)
	_ = reopenedStore.Close()
	ok := loaded.ID == run.ID && loaded.Recovery != nil && loaded.Recovery.RetryUsed == 1 && len(archived) == 1 && reopenErr == nil && dispatcher.calls == 2
	if !ok {
		return fixtureWorkflowResult{lastError: fmt.Errorf("calls=%d archived=%d retry=%v reopen=%v", dispatcher.calls, len(archived), loaded.Recovery, reopenErr)}, "workflow did not prove same-route retry and reconnect"
	}
	return fixtureWorkflowResult{ok: true, archived: len(archived), reopened: true}, "workflow engine retried one unavailable read on the same route and reopened its durable state"
}

func fixtureBudgetAndDuration(ctx context.Context) (bool, string) {
	route := Route{ID: "fixture-budget", Backend: "fixture-backend", Provider: "fixture-provider", RequestedModel: "gpt-5.6-luna", ObservedModel: "gpt-5.6-luna"}
	budgetCost := 1.0
	budgetRunner := &fixtureSequenceRunner{results: []Result{{Status: "failed", Failure: contract.FailureUnavailable, Route: route, CostUSD: &budgetCost, Invoked: true, InvokedKnown: true}}}
	budget, budgetErr := Execute(ctx, Request{RunID: "budget", WorkflowID: "budget", Route: route, Effects: []contract.Effect{contract.EffectRead}, Policy: Policy{MaxRetries: 1, BudgetUSD: 1, MaxDuration: time.Second, ReadOnly: true}, Evidence: EvidenceFixture}, Options{Runner: budgetRunner, Store: &fixtureMemoryStore{}})
	durationCost := 0.1
	durationRunner := &fixtureSequenceRunner{results: []Result{{Status: "failed", Failure: contract.FailureUnavailable, Route: route, CostUSD: &durationCost, Duration: time.Second, Invoked: true, InvokedKnown: true}}}
	duration, durationErr := Execute(ctx, Request{RunID: "duration", WorkflowID: "duration", Route: route, Effects: []contract.Effect{contract.EffectRead}, Policy: Policy{MaxRetries: 1, BudgetUSD: 1, MaxDuration: time.Second, ReadOnly: true}, Evidence: EvidenceFixture}, Options{Runner: durationRunner, Store: &fixtureMemoryStore{}})
	ok := budgetErr == nil && durationErr == nil && budget.Status == "blocked" && duration.Status == "blocked" && budgetRunner.calls == 1 && durationRunner.calls == 1
	detail := fmt.Sprintf("budget=%s calls=%d; duration=%s calls=%d; both stop before a second dispatch", budget.Status, budgetRunner.calls, duration.Status, durationRunner.calls)
	return ok, detail
}

type fixtureSequenceRunner struct {
	results []Result
	calls   int
}

type fixtureMemoryStore struct{}

func (*fixtureMemoryStore) PersistAttempt(context.Context, Attempt) error { return nil }

func (r *fixtureSequenceRunner) Run(ctx context.Context, route Route) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{Route: route, Failure: contract.FailureCanceled, Canceled: true}, err
	}
	if r.calls >= len(r.results) {
		return Result{Route: route, Failure: contract.FailureUnavailable, Invoked: true, InvokedKnown: true}, contract.Fail(contract.FailureUnavailable, "fixture sequence exhausted")
	}
	result := r.results[r.calls]
	r.calls++
	if result.Route.ID == "" {
		result.Route = route
	}
	return result, nil
}

func runFixtureSupervisor(ctx context.Context, root string) (bool, string) {
	path := filepath.Join(root, "supervisor-fixture.sh")
	state := filepath.Join(root, "supervisor-fixture.state")
	script := "#!/bin/sh\nn=$(cat \"$ATENEA_FIXTURE_SUPERVISOR_STATE\" 2>/dev/null || echo 0)\nn=$((n+1))\nprintf '%s' \"$n\" > \"$ATENEA_FIXTURE_SUPERVISOR_STATE\"\nif [ \"$n\" = 1 ]; then exit 7; fi\nwhile IFS= read -r request; do\n  case \"$request\" in\n    *initialize*) printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-06-18\"}}'; if [ \"$n\" = 2 ]; then sleep 0.05; exit 8; fi ;;\n  esac\ndone\n"
	if err := writeExclusive(path, []byte(script), 0o700); err != nil {
		return false, "could not create the supervisor fixture: " + err.Error()
	}
	sup, err := supervisor.New(supervisor.Spec{ID: "fixture", Command: path, Env: []string{"ATENEA_FIXTURE_SUPERVISOR_STATE=" + state}, Lifecycle: supervisor.OnDemand, Transport: supervisor.TransportStdio, ReadyTimeout: 2 * time.Second, RestartLimit: 2, RestartDelay: 20 * time.Millisecond, StableAfter: time.Hour, IdleTimeout: 10 * time.Second, StopGrace: 100 * time.Millisecond})
	if err != nil {
		return false, "supervisor fixture was not constructed: " + err.Error()
	}
	defer sup.Stop()
	if _, err := sup.EnsureReady(ctx, "fixture"); err != nil {
		return false, "supervisor fixture did not reach ready: " + err.Error()
	}
	firstSession, sessionErr := sup.Session("fixture")
	if sessionErr != nil || firstSession == nil {
		return false, "supervisor did not expose a live session after the pre-ready restart"
	}
	deadline := time.Now().Add(2 * time.Second)
	var status supervisor.Status
	for time.Now().Before(deadline) {
		status = sup.Status()[0]
		if status.State == supervisor.StateReady && status.Restarts >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.State != supervisor.StateReady || status.Restarts != 2 {
		return false, fmt.Sprintf("supervisor restart budget was not exact: state=%s restarts=%d", status.State, status.Restarts)
	}
	secondSession, sessionErr := sup.Session("fixture")
	if sessionErr != nil || secondSession == nil {
		return false, "supervisor did not expose a renewed session after the after-ready failure"
	}
	sup.Stop()
	if _, err := sup.Session("fixture"); err == nil {
		return false, "supervisor exposed a child after stop"
	}
	return true, "supervisor seam proved pre-ready and after-ready failures, exact restart limit, renewed session and no child after stop"
}

// fixtureKivgraphSession is a copy-safe MCP seam for the real Kivgraph Runner.
// It exposes only graph_status; all writes still pass through Runner's
// managedFresh and official full Indexer boundary.
type fixtureKivgraphSession struct {
	mu         sync.Mutex
	root       string
	generation int
	fresh      bool
}

func (s *fixtureKivgraphSession) Call(ctx context.Context, tool string, _ map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if tool != "graph_status" {
		return "", fmt.Errorf("fixture Kivgraph rejects non-status tool %q", tool)
	}
	s.mu.Lock()
	generation, fresh, root := s.generation, s.fresh, s.root
	s.mu.Unlock()
	state := "stale"
	if fresh {
		state = "fresh"
	}
	return marshalFixtureGraphStatus(root, generation, state)
}

func marshalFixtureGraphStatus(root string, generation int, state string) (string, error) {
	raw, err := json.Marshal(map[string]any{"results": map[string]any{
		"status": "ready", "snapshot_id": generation, "snapshot_built_at": "2026-09-06T00:00:00Z",
		"symbols": 7, "edges": 11, "files": 1, "repositories": 1, "unresolved": 0,
		"repository_freshness": []map[string]any{{"name": "fixture", "path": root}},
		"content_freshness":    map[string]any{"generation": generation, "state": state, "input_digest": "fixture-digest"},
	}})
	return string(raw), err
}

// fixtureKivgraphCommand creates every executable and call log inside the
// fixture root. The files are exclusive so the pilot cannot overwrite a
// caller-owned path while proving the real RunConfiguredIndex boundary.
func fixtureKivgraphCommand(root, name, script string) (string, string, error) {
	path := filepath.Join(root, name+".sh")
	calls := filepath.Join(root, name+".calls")
	if err := writeExclusive(path, []byte(script), 0o700); err != nil {
		return "", "", err
	}
	if err := createExclusive(calls, 0o600); err != nil {
		return "", "", err
	}
	return path, calls, nil
}

func fixtureKivgraphArgsExact(path string) bool {
	raw, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(raw)) == "index --full --json"
}

func fixtureIndexWait(ctx context.Context, indexTimeout time.Duration) time.Duration {
	wait := indexTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
	}
	if wait <= 0 {
		return time.Nanosecond
	}
	return wait
}

func fixtureIndexTimeout(ctx context.Context) time.Duration {
	const generousFixtureTimeout = 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < generousFixtureTimeout {
			return remaining
		}
	}
	return generousFixtureTimeout
}

func runFixtureKivgraph(ctx context.Context, root string, duringKivgraph func()) (bool, string) {
	repo := contract.NewRepository("fixture", root, nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	authorized := contract.Permission{Task: "fixture Kivgraph freshness", Effects: []contract.Effect{contract.EffectRead, contract.EffectWrite, contract.EffectProcess}}
	unauthorized := contract.Permission{Task: "fixture Kivgraph freshness", Effects: []contract.Effect{contract.EffectRead}}
	indexTimeout := fixtureIndexTimeout(ctx)
	const successfulIndexScript = "#!/bin/sh\n[ \"$1\" = index ] && [ \"$2\" = --full ] && [ \"$3\" = --json ] || exit 2\nprintf '%s\\n' \"$*\" >> \"$ATENEA_FIXTURE_INDEX_CALLS\"\nprintf '%s\\n' '{\"event\":\"result\",\"result\":{\"passed\":true,\"generation_id\":\"2\",\"counts\":{\"symbols\":7,\"edges\":11}}}'\n"
	const failingIndexScript = "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$ATENEA_FIXTURE_INDEX_CALLS\"\nexit 17\n"
	const cancelIndexScript = "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$ATENEA_FIXTURE_INDEX_CALLS\"\nprintf started > \"$ATENEA_FIXTURE_INDEX_STARTED\"\ntrap 'exit 143' TERM INT\nsleep 30\n"
	successPath, explicitIndexCalls, err := fixtureKivgraphCommand(root, "kivgraph-index-success", successfulIndexScript)
	if err != nil {
		return false, "could not create successful Kivgraph CLI fixture: " + err.Error()
	}
	standingPath, standingIndexCalls, err := fixtureKivgraphCommand(root, "kivgraph-index-standing", successfulIndexScript)
	if err != nil {
		return false, "could not create standing Kivgraph CLI fixture: " + err.Error()
	}
	failurePath, failureIndexCalls, err := fixtureKivgraphCommand(root, "kivgraph-index-failure", failingIndexScript)
	if err != nil {
		return false, "could not create failed Kivgraph CLI fixture: " + err.Error()
	}
	cancelPath, cancelIndexCalls, err := fixtureKivgraphCommand(root, "kivgraph-index-cancel", cancelIndexScript)
	if err != nil {
		return false, "could not create canceled Kivgraph CLI fixture: " + err.Error()
	}
	cancelIndexStarted := filepath.Join(root, "kivgraph-index-cancel.started")
	if err := createExclusive(cancelIndexStarted, 0o600); err != nil {
		return false, "could not create canceled Kivgraph readiness marker: " + err.Error()
	}

	var explicitCalls int
	explicitSession := &fixtureKivgraphSession{root: root, generation: 1}
	explicitRunner, err := kivgraph.New(kivgraph.Options{
		Session:              func(context.Context) (kivgraph.Session, error) { return explicitSession, nil },
		MaintenanceDirectory: filepath.Join(root, "kivgraph-explicit-maintenance"),
		IndexTimeout:         indexTimeout,
		Index: func(indexCtx context.Context, _, mode string) (kivgraph.IndexReport, error) {
			if mode != "full" {
				return kivgraph.IndexReport{}, errors.New("fixture admitted a non-full Kivgraph rebuild")
			}
			if err := indexCtx.Err(); err != nil {
				return kivgraph.IndexReport{}, err
			}
			report, err := kivgraph.RunConfiguredIndex(indexCtx, successPath, []string{"ATENEA_FIXTURE_INDEX_CALLS=" + explicitIndexCalls}, root, "full")
			if err != nil {
				return kivgraph.IndexReport{}, err
			}
			explicitCalls++
			explicitSession.mu.Lock()
			explicitSession.generation, explicitSession.fresh = 2, true
			explicitSession.mu.Unlock()
			return report, nil
		},
	})
	if err != nil {
		return false, "Kivgraph diagnostic runner could not be constructed: " + err.Error()
	}
	if _, err := explicitRunner.ProbeManagedFresh(ctx, repo, unauthorized); contract.KindOf(err) != contract.FailurePermissionDenied || explicitCalls != 0 {
		return false, "unauthorized freshness attempted an index or returned the wrong permission result"
	}
	explicit, err := explicitRunner.ProbeManagedFresh(ctx, repo, authorized)
	if err != nil || explicit.Status != "fresh" || explicit.Generation != 2 || !explicit.Rebuilt || explicitCalls != 1 || !fixtureKivgraphArgsExact(explicitIndexCalls) {
		return false, fmt.Sprintf("authorized managedFresh failed: report=%+v calls=%d err=%v", explicit, explicitCalls, err)
	}

	// Standing authorization uses the production background-maintenance path.
	standingSession := &fixtureKivgraphSession{root: root, generation: 1}
	var standingCalls int
	started, release := make(chan struct{}), make(chan struct{})
	var startOnce sync.Once
	standingRunner, err := kivgraph.New(kivgraph.Options{
		RequireFresh:          true,
		AutoReindexRegistered: true,
		MaintenanceDirectory:  filepath.Join(root, "kivgraph-standing-maintenance"),
		IndexTimeout:          indexTimeout,
		Session:               func(context.Context) (kivgraph.Session, error) { return standingSession, nil },
		Index: func(indexCtx context.Context, _, mode string) (kivgraph.IndexReport, error) {
			if mode != "full" {
				return kivgraph.IndexReport{}, errors.New("standing Kivgraph maintenance admitted a non-full rebuild")
			}
			standingCalls++
			startOnce.Do(func() { close(started) })
			report, err := kivgraph.RunConfiguredIndex(indexCtx, standingPath, []string{"ATENEA_FIXTURE_INDEX_CALLS=" + standingIndexCalls}, root, "full")
			if err != nil {
				return kivgraph.IndexReport{}, err
			}
			select {
			case <-indexCtx.Done():
				return kivgraph.IndexReport{}, indexCtx.Err()
			case <-release:
			}
			standingSession.mu.Lock()
			standingSession.generation, standingSession.fresh = 2, true
			standingSession.mu.Unlock()
			return report, nil
		},
	})
	if err != nil {
		return false, "standing Kivgraph runner could not be constructed: " + err.Error()
	}
	if err := standingRunner.EnableBackground(); err != nil {
		return false, "standing Kivgraph maintenance could not start: " + err.Error()
	}
	defer standingRunner.CloseMaintenance()
	autoRequest := contract.RunRequest{
		Capability:     contract.Capability{ID: kivgraph.CapabilityIntent, Version: contract.Version{Major: 1}, Summary: "fixture intent", Effects: []contract.Effect{contract.EffectRead}, Inputs: []contract.Field{{Name: "intent", Type: contract.TypeString, Required: true}}, Outputs: []contract.Field{{Name: "matches", Type: contract.TypeRecordList, Required: true}}},
		Implementation: contract.Implementation{ID: kivgraph.ImplIntent, Provider: "kivgraph", Capability: kivgraph.CapabilityIntent},
		Repository:     repo, Payload: map[string]any{"intent": "fixture"}, Permission: contract.Permission{Task: "fixture", Effects: []contract.Effect{contract.EffectRead}},
	}
	autoErr := make(chan error, 1)
	go func() { _, callErr := standingRunner.Run(ctx, autoRequest); autoErr <- callErr }()
	select {
	case <-started:
	case <-time.After(fixtureIndexWait(ctx, indexTimeout)):
		return false, "standing Kivgraph maintenance did not reach the full index seam"
	}
	if standingCalls != 1 {
		return false, fmt.Sprintf("standing maintenance started %d full calls before coalescing", standingCalls)
	}
	if duringKivgraph != nil {
		duringKivgraph()
	}
	if err := <-autoErr; contract.CodeOf(err) != "maintenance_pending" {
		return false, "automatic freshness did not report its pending shared job"
	}
	joined := make(chan struct {
		report kivgraph.ManagedFreshReport
		err    error
	}, 1)
	go func() {
		report, callErr := standingRunner.ProbeManagedFresh(ctx, repo, authorized)
		joined <- struct {
			report kivgraph.ManagedFreshReport
			err    error
		}{report, callErr}
	}()
	close(release)
	joinedResult := <-joined
	if joinedResult.err != nil || joinedResult.report.Status != "fresh" || joinedResult.report.Generation != 2 || standingCalls != 1 || !fixtureKivgraphArgsExact(standingIndexCalls) {
		return false, fmt.Sprintf("standing coalesced result=%+v calls=%d err=%v", joinedResult.report, standingCalls, joinedResult.err)
	}

	// An ordinary indexer failure is one failed full invocation. It must not
	// publish a generation or silently retry the same generation.
	failureSession := &fixtureKivgraphSession{root: root, generation: 1}
	var failureCalls int
	failureRunner, err := kivgraph.New(kivgraph.Options{
		MaintenanceDirectory: filepath.Join(root, "kivgraph-failure-maintenance"),
		IndexTimeout:         indexTimeout,
		Session:              func(context.Context) (kivgraph.Session, error) { return failureSession, nil },
		Index: func(indexCtx context.Context, _, mode string) (kivgraph.IndexReport, error) {
			if mode != "full" {
				return kivgraph.IndexReport{}, errors.New("failed Kivgraph maintenance admitted a non-full rebuild")
			}
			failureCalls++
			return kivgraph.RunConfiguredIndex(indexCtx, failurePath, []string{"ATENEA_FIXTURE_INDEX_CALLS=" + failureIndexCalls}, root, "full")
		},
	})
	if err != nil {
		return false, "failed Kivgraph runner could not be constructed: " + err.Error()
	}
	if _, err := failureRunner.ProbeManagedFresh(ctx, repo, authorized); err == nil || failureCalls != 1 || !fixtureKivgraphArgsExact(failureIndexCalls) || failureSession.generation != 1 || failureSession.fresh {
		return false, fmt.Sprintf("ordinary index failure published or retried: calls=%d generation=%d fresh=%v err=%v", failureCalls, failureSession.generation, failureSession.fresh, err)
	}

	// Cancellation is a failed full attempt, not permission to retry blindly.
	cancelSession := &fixtureKivgraphSession{root: root, generation: 1}
	var cancelCalls int
	cancelStarted := make(chan struct{})
	var cancelStartOnce sync.Once
	cancelRunner, err := kivgraph.New(kivgraph.Options{
		MaintenanceDirectory: filepath.Join(root, "kivgraph-cancel-maintenance"),
		IndexTimeout:         indexTimeout,
		Session:              func(context.Context) (kivgraph.Session, error) { return cancelSession, nil },
		Index: func(indexCtx context.Context, _, mode string) (kivgraph.IndexReport, error) {
			if mode != "full" {
				return kivgraph.IndexReport{}, errors.New("canceled Kivgraph maintenance admitted a non-full rebuild")
			}
			cancelCalls++
			result := make(chan struct {
				report kivgraph.IndexReport
				err    error
			}, 1)
			go func() {
				report, runErr := kivgraph.RunConfiguredIndex(indexCtx, cancelPath, []string{
					"ATENEA_FIXTURE_INDEX_CALLS=" + cancelIndexCalls,
					"ATENEA_FIXTURE_INDEX_STARTED=" + cancelIndexStarted,
				}, root, "full")
				result <- struct {
					report kivgraph.IndexReport
					err    error
				}{report, runErr}
			}()
			for {
				select {
				case completed := <-result:
					return completed.report, completed.err
				case <-indexCtx.Done():
					return kivgraph.IndexReport{}, indexCtx.Err()
				case <-time.After(time.Millisecond):
					if raw, readErr := os.ReadFile(cancelIndexStarted); readErr == nil && strings.TrimSpace(string(raw)) == "started" {
						cancelStartOnce.Do(func() { close(cancelStarted) })
					}
				}
				select {
				case <-cancelStarted:
					completed := <-result
					return completed.report, completed.err
				default:
				}
			}
		},
	})
	if err != nil {
		return false, "cancellation Kivgraph runner could not be constructed: " + err.Error()
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	canceled := make(chan error, 1)
	go func() { _, callErr := cancelRunner.ProbeManagedFresh(cancelCtx, repo, authorized); canceled <- callErr }()
	select {
	case <-cancelStarted:
	case <-time.After(fixtureIndexWait(ctx, indexTimeout)):
		return false, "cancellation fixture did not reach the full index seam"
	}
	cancel()
	if err := <-canceled; err == nil || cancelCalls != 1 || !fixtureKivgraphArgsExact(cancelIndexCalls) {
		return false, fmt.Sprintf("canceled managedFresh result err=%v calls=%d", err, cancelCalls)
	}
	return true, "Kivgraph Runner managedFresh proved unauthorized zero calls, exact full-only RunConfiguredIndex argv, standing coalescing, ordinary failure without generation publication or retry, and cancellation without retry"
}

type fixtureRunner struct{ calls int }

func (r *fixtureRunner) Run(ctx context.Context, route Route) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{Route: route, Failure: contract.FailureCanceled, Canceled: true}, err
	}
	r.calls++
	cost := 0.10
	if r.calls == 1 {
		return Result{Status: "failed", Failure: contract.FailureUnavailable, Route: route, CostUSD: &cost, Duration: 10 * time.Millisecond, Invoked: true, InvokedKnown: true}, contract.Fail(contract.FailureUnavailable, "fixture backend unavailable")
	}
	return Result{Status: "ok", Failure: contract.FailureUnspecified, Route: route, CostUSD: &cost, Duration: 10 * time.Millisecond, Invoked: true, InvokedKnown: true, Output: map[string]any{"fixture": true}}, nil
}
