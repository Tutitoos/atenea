// Package coordination persists the small amount of state that joins several
// repository-scoped workflow runs into one user-visible commission.
//
// A workflow remains the authority for execution, permissions and effects.
// This manifest only records the coordinator's immutable request and the
// child workflow ids, so a reconnect can find the same work without creating
// a second workflow or guessing which repository was served.
package coordination

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

const (
	// StatusRunning is part of ATENEA's public orchestration contract.
	StatusRunning = "running"
	// StatusCompleted is part of ATENEA's public orchestration contract.
	StatusCompleted = "completed"
	// StatusStopped is part of ATENEA's public orchestration contract.
	StatusStopped = "stopped"
	// StatusFailed is part of ATENEA's public orchestration contract.
	StatusFailed = "failed"
	// StateRunning is part of ATENEA's public orchestration contract.
	StateRunning = "running"
	// StateAttentionRequired is part of ATENEA's public orchestration contract.
	StateAttentionRequired = "attention_required"
	// StateUncertain is part of ATENEA's public orchestration contract.
	StateUncertain = "uncertain"
	// MaxSpecialists is part of ATENEA's public orchestration contract.
	MaxSpecialists = 2
	// MaxFollowUps is part of ATENEA's public orchestration contract.
	MaxFollowUps = 3
	// MaxAstraExecutions is part of ATENEA's public orchestration contract.
	MaxAstraExecutions = 2
	// WatchdogTimeout is part of ATENEA's public orchestration contract.
	WatchdogTimeout = 5 * time.Minute
)

// ReviewReceipt is the identity-bearing receipt of one fresh review in a
// coordinator cycle. Requested and observed values are kept separate so an
// unavailable provider cannot be silently substituted.
type ReviewReceipt struct {
	CycleID                  string    `json:"cycle_id"`
	WorkflowID               string    `json:"workflow_id"`
	Role                     string    `json:"role"`
	RunID                    string    `json:"run_id"`
	Attempt                  int       `json:"attempt"`
	SourceFingerprint        string    `json:"source_fingerprint"`
	RequestedModel           string    `json:"requested_model,omitempty"`
	ObservedModel            string    `json:"observed_model,omitempty"`
	RequestedReasoningEffort string    `json:"requested_reasoning_effort,omitempty"`
	ObservedReasoningEffort  string    `json:"observed_reasoning_effort,omitempty"`
	Verdict                  string    `json:"verdict"`
	Fresh                    bool      `json:"fresh"`
	At                       time.Time `json:"at"`
}

// Validate is part of ATENEA's public orchestration contract.
func (r ReviewReceipt) Validate(cycleID, role string) error {
	if strings.TrimSpace(r.CycleID) != cycleID || strings.TrimSpace(r.Role) != role || strings.TrimSpace(r.WorkflowID) == "" || strings.TrimSpace(r.RunID) == "" || r.Attempt < 1 || strings.TrimSpace(r.SourceFingerprint) == "" || !r.Fresh {
		return contract.Fail(contract.FailureInvalidInput, "coordination: %s review is not a fresh receipt for cycle %q", role, cycleID)
	}
	if strings.ToLower(strings.TrimSpace(r.Verdict)) != "ok" {
		return contract.Fail(contract.FailureInvalidInput, "coordination: %s review did not accept the result", role)
	}
	if role == "sol" || role == "astra" {
		if strings.TrimSpace(r.RequestedModel) == "" || strings.TrimSpace(r.ObservedModel) == "" || strings.TrimSpace(r.RequestedReasoningEffort) == "" || strings.TrimSpace(r.ObservedReasoningEffort) == "" {
			return contract.Fail(contract.FailureUnavailable, "coordination: %s review has unverified model or effort identity", role)
		}
		wantModel, wantEffort := "gpt-5.6-sol", "medium"
		if role == "astra" {
			wantModel = "gpt-6-astra"
		}
		if r.RequestedModel != wantModel || r.ObservedModel != wantModel || r.RequestedReasoningEffort != wantEffort || r.ObservedReasoningEffort != wantEffort {
			return contract.Fail(contract.FailureUnavailable, "coordination: %s review identity is not canonical", role)
		}
	}
	if r.RequestedModel != "" && r.ObservedModel != "" && r.RequestedModel != r.ObservedModel {
		return contract.Fail(contract.FailureUnavailable, "coordination: %s observed model %q differs from requested %q", role, r.ObservedModel, r.RequestedModel)
	}
	if r.RequestedReasoningEffort != "" && r.ObservedReasoningEffort != "" && r.RequestedReasoningEffort != r.ObservedReasoningEffort {
		return contract.Fail(contract.FailureUnavailable, "coordination: %s observed effort %q differs from requested %q", role, r.ObservedReasoningEffort, r.RequestedReasoningEffort)
	}
	return nil
}

// Child is the durable link to one repository-scoped workflow.
type Child struct {
	Repository string          `json:"repository"`
	WorkflowID string          `json:"workflow_id"`
	Graph      json.RawMessage `json:"graph,omitempty"`
	Status     string          `json:"status,omitempty"`
	BoundAt    time.Time       `json:"bound_at"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// Record is the coordinator receipt. Repositories and children are kept in
// sorted deterministic order on disk; the repository ids themselves remain
// the only public identity, never physical roots.
type Record struct {
	ID                    string          `json:"id"`
	Objective             string          `json:"objective"`
	Criterion             string          `json:"criterion"`
	Repositories          []string        `json:"repositories"`
	Limits                contract.Limits `json:"limits"`
	BudgetUSD             float64         `json:"budget_usd"`
	Coordinator           string          `json:"coordinator"`
	CoordinatorWorkflowID string          `json:"coordinator_workflow_id,omitempty"`
	CoordinatorThreadID   string          `json:"coordinator_thread_id,omitempty"`
	CoordinatorGraph      json.RawMessage `json:"coordinator_graph,omitempty"`
	Specialists           []string        `json:"specialists"`
	Status                string          `json:"status"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
	Error                 string          `json:"error,omitempty"`
	Children              []Child         `json:"children"`
	State                 string          `json:"state"`
	FollowUps             int             `json:"follow_ups"`
	AstraExecutions       int             `json:"astra_executions"`
	LastProgressAt        time.Time       `json:"last_progress_at"`
	SolReview             *ReviewReceipt  `json:"sol_review,omitempty"`
	AstraAudits           []ReviewReceipt `json:"astra_audits,omitempty"`
	ActiveCycleID         string          `json:"active_cycle_id,omitempty"`
	AcceptedCycleIDs      []string        `json:"accepted_cycle_ids,omitempty"`
}

func (r Record) clone() Record {
	r.Repositories = append([]string(nil), r.Repositories...)
	r.Specialists = append([]string(nil), r.Specialists...)
	r.Children = append([]Child(nil), r.Children...)
	r.CoordinatorGraph = append(json.RawMessage(nil), r.CoordinatorGraph...)
	for i := range r.Children {
		r.Children[i].Graph = append(json.RawMessage(nil), r.Children[i].Graph...)
	}
	r.AstraAudits = append([]ReviewReceipt(nil), r.AstraAudits...)
	r.AcceptedCycleIDs = append([]string(nil), r.AcceptedCycleIDs...)
	if r.SolReview != nil {
		reviewCopy := *r.SolReview
		r.SolReview = &reviewCopy
	}
	return r
}

// Validate checks the coordinator invariants before any child is dispatched.
func (r Record) Validate() error {
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.Objective) == "" || strings.TrimSpace(r.Criterion) == "" {
		return contract.Fail(contract.FailureInvalidInput, "coordination: id, objective and criterion are required")
	}
	if len(r.Repositories) == 0 {
		return contract.Fail(contract.FailureInvalidInput, "coordination: at least one repository is required")
	}
	seen := make(map[string]struct{}, len(r.Repositories))
	for _, repo := range r.Repositories {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			return contract.Fail(contract.FailureInvalidInput, "coordination: repository id is empty")
		}
		if _, ok := seen[repo]; ok {
			return contract.Fail(contract.FailureInvalidInput, "coordination: repository %q is repeated", repo)
		}
		seen[repo] = struct{}{}
	}
	if len(r.Specialists) == 0 || len(r.Specialists) > MaxSpecialists {
		return contract.Fail(contract.FailureInvalidInput, "coordination: expected one or two specialists, got %d", len(r.Specialists))
	}
	if strings.TrimSpace(r.Coordinator) == "" {
		return contract.Fail(contract.FailureInvalidInput, "coordination: coordinator is required")
	}
	switch r.Status {
	case StatusRunning, StatusCompleted, StatusStopped, StatusFailed:
	default:
		return contract.Fail(contract.FailureInvalidInput, "coordination: unknown status %q", r.Status)
	}
	if r.State == "" {
		r.State = StateRunning
	}
	if r.State != StateRunning && r.State != StateAttentionRequired && r.State != StateUncertain {
		return contract.Fail(contract.FailureInvalidInput, "coordination: unknown state %q", r.State)
	}
	if r.FollowUps < 0 || r.FollowUps > MaxFollowUps {
		return contract.Fail(contract.FailurePermissionDenied, "coordination: follow-up limit %d exceeded", MaxFollowUps)
	}
	if r.AstraExecutions < 0 || r.AstraExecutions > MaxAstraExecutions {
		return contract.Fail(contract.FailurePermissionDenied, "coordination: Astra execution limit %d exceeded", MaxAstraExecutions)
	}
	if r.SolReview != nil {
		if err := r.SolReview.Validate(r.ActiveCycleID, "sol"); err != nil {
			return err
		}
	}
	if len(r.AstraAudits) > 0 && r.SolReview == nil {
		return contract.Fail(contract.FailureInvalidInput, "coordination: Astra audit requires a Sol review")
	}
	if len(r.AstraAudits) > r.AstraExecutions {
		return contract.Fail(contract.FailurePermissionDenied, "coordination: Astra audit has no reserved execution")
	}
	for _, audit := range r.AstraAudits {
		if err := audit.Validate(r.ActiveCycleID, "astra"); err != nil {
			return err
		}
		if audit.WorkflowID != r.SolReview.WorkflowID || audit.SourceFingerprint != r.SolReview.SourceFingerprint {
			return contract.Fail(contract.FailureInvalidInput, "coordination: Sol/Astra receipts do not share workflow and source")
		}
	}
	return nil
}

// BeginReviewCycle binds fresh review receipts to one concrete workflow
// generation. Repeating an accepted cycle is idempotent; a different cycle
// clears transient receipts so older evidence cannot be reused.
func (s *Store) BeginReviewCycle(ctx context.Context, id, cycleID string, at time.Time) (Record, error) {
	cycleID = strings.TrimSpace(cycleID)
	if cycleID == "" {
		return Record{}, contract.Fail(contract.FailureInvalidInput, "coordination: review cycle id is required")
	}
	return s.update(ctx, id, func(r *Record) error {
		if slices.Contains(r.AcceptedCycleIDs, cycleID) {
			return nil
		}
		if r.ActiveCycleID != cycleID {
			r.ActiveCycleID = cycleID
			r.SolReview = nil
			r.AstraAudits = nil
			r.AstraExecutions = 0
		}
		r.LastProgressAt = at
		return nil
	})
}

// Store is an atomic JSON store. It is intentionally separate from workflow
// SQLite because a coordinator may link several repository databases.
type Store struct {
	path string
	mu   sync.Mutex
}

// PathFor keeps coordinator receipts beside the workflow database while
// leaving the existing SQLite file untouched.
func PathFor(workflowPath string) string { return workflowPath + ".coordination.json" }

// Open is part of ATENEA's public orchestration contract.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, contract.Fail(contract.FailureInvalidInput, "coordination: path is required")
	}
	return &Store{path: path}, nil
}

// Path is part of ATENEA's public orchestration contract.
func (s *Store) Path() string { return s.path }

func (s *Store) lock(ctx context.Context) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, contract.Fail(contract.FailureUnavailable, "coordination: create state directory: %v", err)
	}
	file, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, contract.Fail(contract.FailureUnavailable, "coordination: open state lock: %v", err)
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = file.Close()
			return nil, contract.Fail(contract.FailureUnavailable, "coordination: lock state: %v", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, contract.Fail(contract.FailureCanceled, "coordination: waiting for state lock: %v", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func unlock(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func (s *Store) loadLocked() (map[string]Record, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, contract.Fail(contract.FailureUnavailable, "coordination: read %s: %v", s.path, err)
	}
	var rows map[string]Record
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, contract.Fail(contract.FailureUnavailable, "coordination: read %s: %v", s.path, err)
	}
	if rows == nil {
		rows = map[string]Record{}
	}
	for id, record := range rows {
		if record.ID == "" {
			record.ID = id
		}
		if err := record.Validate(); err != nil {
			return nil, err
		}
		rows[id] = record
	}
	return rows, nil
}

func (s *Store) saveLocked(rows map[string]Record) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return contract.Fail(contract.FailureUnavailable, "coordination: create state directory: %v", err)
	}
	raw, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "coordination: encode state: %v", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".coordination-*.tmp")
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "coordination: create temporary state: %v", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return contract.Fail(contract.FailureUnavailable, "coordination: secure temporary state: %v", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return contract.Fail(contract.FailureUnavailable, "coordination: write state: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return contract.Fail(contract.FailureUnavailable, "coordination: sync state: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return contract.Fail(contract.FailureUnavailable, "coordination: close state: %v", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return contract.Fail(contract.FailureUnavailable, "coordination: replace state: %v", err)
	}
	return nil
}

func newID(now time.Time) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("coord-%d", now.UnixNano())
	}
	return "coord-" + now.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(b[:])
}

// Create writes a new coordinator receipt before any child workflow is made.
func (s *Store) Create(ctx context.Context, objective, criterion string, repositories []string, limits contract.Limits, budget float64, specialists []string, now time.Time) (Record, error) {
	repos := append([]string(nil), repositories...)
	sort.Strings(repos)
	record := Record{ID: newID(now), Objective: strings.TrimSpace(objective), Criterion: strings.TrimSpace(criterion), Repositories: repos,
		Limits: limits, BudgetUSD: budget, Coordinator: "atenea-coordinator", Specialists: append([]string(nil), specialists...), Status: StatusRunning, State: StateRunning, LastProgressAt: now, CreatedAt: now, UpdatedAt: now}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer unlock(lock)
	rows, err := s.loadLocked()
	if err != nil {
		return Record{}, err
	}
	rows[record.ID] = record
	if err := s.saveLocked(rows); err != nil {
		return Record{}, err
	}
	return record.clone(), nil
}

// Load is part of ATENEA's public orchestration contract.
func (s *Store) Load(ctx context.Context, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer unlock(lock)
	rows, err := s.loadLocked()
	if err != nil {
		return Record{}, err
	}
	record, ok := rows[id]
	if !ok {
		return Record{}, contract.Fail(contract.FailureNotFound, "coordination %s was not found", id)
	}
	return record.clone(), nil
}

func (s *Store) update(ctx context.Context, id string, fn func(*Record) error) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := s.lock(ctx)
	if err != nil {
		return Record{}, err
	}
	defer unlock(lock)
	rows, err := s.loadLocked()
	if err != nil {
		return Record{}, err
	}
	record, ok := rows[id]
	if !ok {
		return Record{}, contract.Fail(contract.FailureNotFound, "coordination %s was not found", id)
	}
	if err := fn(&record); err != nil {
		return Record{}, err
	}
	record.UpdatedAt = time.Now().UTC()
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	rows[id] = record
	if err := s.saveLocked(rows); err != nil {
		return Record{}, err
	}
	return record.clone(), nil
}

// BindChild is idempotent for the same repository/workflow and rejects a
// conflicting rebinding, which prevents a reconnect from duplicating effects.
func (s *Store) BindChild(ctx context.Context, id, repository, workflowID string, now time.Time) (Record, error) {
	return s.BindChildPrepared(ctx, id, repository, workflowID, nil, now)
}

// BindChildPrepared persists the graph before its workflow row is created.
// A process dying between those writes can recreate exactly the reserved row
// instead of guessing a new plan or duplicating an effect.
func (s *Store) BindChildPrepared(ctx context.Context, id, repository, workflowID string, graph json.RawMessage, now time.Time) (Record, error) {
	return s.update(ctx, id, func(r *Record) error {
		for i := range r.Children {
			if r.Children[i].Repository != repository {
				continue
			}
			if r.Children[i].WorkflowID != workflowID {
				return contract.Fail(contract.FailureInvalidInput, "coordination %s already binds repository %q to workflow %q", id, repository, r.Children[i].WorkflowID)
			}
			if len(r.Children[i].Graph) == 0 && len(graph) > 0 {
				r.Children[i].Graph = append(json.RawMessage(nil), graph...)
			}
			return nil
		}
		r.Children = append(r.Children, Child{Repository: repository, WorkflowID: workflowID, Graph: append(json.RawMessage(nil), graph...), Status: StatusRunning, BoundAt: now})
		sort.Slice(r.Children, func(i, j int) bool { return r.Children[i].Repository < r.Children[j].Repository })
		r.LastProgressAt = now
		return nil
	})
}

// BindCoordinator is part of ATENEA's public orchestration contract.
func (s *Store) BindCoordinator(ctx context.Context, id, workflowID string) (Record, error) {
	return s.BindCoordinatorPrepared(ctx, id, workflowID, nil)
}

// BindCoordinatorPrepared records the root graph in the same durable update
// as its reserved workflow id so resume can complete an interrupted create.
func (s *Store) BindCoordinatorPrepared(ctx context.Context, id, workflowID string, graph json.RawMessage) (Record, error) {
	if strings.TrimSpace(workflowID) == "" {
		return Record{}, contract.Fail(contract.FailureInvalidInput, "coordination: coordinator workflow id is required")
	}
	return s.update(ctx, id, func(r *Record) error {
		if r.CoordinatorWorkflowID != "" && r.CoordinatorWorkflowID != workflowID {
			return contract.Fail(contract.FailureInvalidInput, "coordination %s already binds coordinator workflow %q", id, r.CoordinatorWorkflowID)
		}
		r.CoordinatorWorkflowID = workflowID
		if len(r.CoordinatorGraph) == 0 && len(graph) > 0 {
			r.CoordinatorGraph = append(json.RawMessage(nil), graph...)
		}
		return nil
	})
}

// Prepare atomically reserves the root and every declared repository before
// any workflow is launched. Resume can therefore reconstruct the complete
// commission after a crash at any later instruction.
func (s *Store) Prepare(ctx context.Context, id, rootWorkflowID string, rootGraph json.RawMessage, children []Child, now time.Time) (Record, error) {
	return s.update(ctx, id, func(r *Record) error {
		if r.CoordinatorWorkflowID != "" || len(r.Children) != 0 {
			return contract.Fail(contract.FailureInvalidInput, "coordination %s was prepared already", id)
		}
		if strings.TrimSpace(rootWorkflowID) == "" || len(rootGraph) == 0 {
			return contract.Fail(contract.FailureInvalidInput, "coordination %s requires a prepared root", id)
		}
		byRepository := make(map[string]Child, len(children))
		for _, child := range children {
			if strings.TrimSpace(child.Repository) == "" || strings.TrimSpace(child.WorkflowID) == "" || len(child.Graph) == 0 {
				return contract.Fail(contract.FailureInvalidInput, "coordination %s has an incomplete child reservation", id)
			}
			if _, exists := byRepository[child.Repository]; exists {
				return contract.Fail(contract.FailureInvalidInput, "coordination %s repeats repository %q", id, child.Repository)
			}
			child.Status, child.BoundAt = StatusRunning, now
			byRepository[child.Repository] = child
		}
		if len(byRepository) != len(r.Repositories) {
			return contract.Fail(contract.FailureInvalidInput, "coordination %s prepared %d/%d repositories", id, len(byRepository), len(r.Repositories))
		}
		ordered := make([]Child, 0, len(r.Repositories))
		for _, repository := range r.Repositories {
			child, ok := byRepository[repository]
			if !ok {
				return contract.Fail(contract.FailureInvalidInput, "coordination %s did not prepare repository %q", id, repository)
			}
			ordered = append(ordered, child)
		}
		r.CoordinatorWorkflowID = rootWorkflowID
		r.CoordinatorGraph = append(json.RawMessage(nil), rootGraph...)
		r.Children = ordered
		r.LastProgressAt = now
		return nil
	})
}

// ObserveCoordinatorThread is part of ATENEA's public orchestration contract.
func (s *Store) ObserveCoordinatorThread(ctx context.Context, id, threadID string) (Record, error) {
	if strings.TrimSpace(threadID) == "" {
		return Record{}, contract.Fail(contract.FailureInvalidInput, "coordination: coordinator thread id is required")
	}
	return s.update(ctx, id, func(r *Record) error {
		if r.CoordinatorThreadID != "" && r.CoordinatorThreadID != threadID {
			return contract.Fail(contract.FailureInvalidInput, "coordination %s observed a second coordinator thread", id)
		}
		r.CoordinatorThreadID = threadID
		return nil
	})
}

// FinishChild is part of ATENEA's public orchestration contract.
func (s *Store) FinishChild(ctx context.Context, id, repository, status, message string, now time.Time) (Record, error) {
	return s.update(ctx, id, func(r *Record) error {
		for i := range r.Children {
			if r.Children[i].Repository == repository {
				r.Children[i].Status, r.Children[i].FinishedAt, r.Children[i].Error = status, now, strings.TrimSpace(message)
				r.LastProgressAt = now
				return nil
			}
		}
		return contract.Fail(contract.FailureNotFound, "coordination %s has no child for repository %q", id, repository)
	})
}

// SetStatus is part of ATENEA's public orchestration contract.
func (s *Store) SetStatus(ctx context.Context, id, status, message string) (Record, error) {
	return s.update(ctx, id, func(r *Record) error {
		if status == StatusCompleted {
			if len(r.Children) != len(r.Repositories) {
				return contract.Fail(contract.FailureInvalidInput, "coordination %s cannot complete with %d/%d repositories", id, len(r.Children), len(r.Repositories))
			}
			for _, child := range r.Children {
				if child.Status != StatusCompleted {
					return contract.Fail(contract.FailureInvalidInput, "coordination %s cannot complete while repository %q is %s", id, child.Repository, child.Status)
				}
			}
		}
		r.Status, r.Error = status, strings.TrimSpace(message)
		r.LastProgressAt = time.Now().UTC()
		return nil
	})
}

// TouchProgress persists the last durable coordinator event. It is separate
// from UpdatedAt so status changes and progress can be audited independently.
func (s *Store) TouchProgress(ctx context.Context, id string, at time.Time) (Record, error) {
	return s.update(ctx, id, func(r *Record) error {
		r.LastProgressAt = at
		// The watchdog may have paused an in-flight child between its provider
		// response and this durable accounting event. Preserve that pause while
		// allowing the accounting write to complete; callers still cannot
		// dispatch because RecordFollowUp/BeginAstraExecution reject it.
		return nil
	})
}

// RecordFollowUp consumes one of the three follow-ups in the current cycle.
// The increment is durable before a caller is allowed to dispatch the next
// turn, so a reconnect cannot spend the same follow-up twice.
func (s *Store) RecordFollowUp(ctx context.Context, id string, at time.Time) (Record, error) {
	return s.update(ctx, id, func(r *Record) error {
		if r.State != StateRunning {
			return contract.Fail(contract.FailurePermissionDenied, "coordination %s is not running", id)
		}
		if r.FollowUps >= MaxFollowUps {
			return contract.Fail(contract.FailurePermissionDenied, "coordination %s reached the follow-up limit of %d", id, MaxFollowUps)
		}
		r.FollowUps++
		r.LastProgressAt = at
		return nil
	})
}

// BeginAstraExecution reserves one audit slot before starting Astra. A
// failed or unavailable audit still consumes the slot: otherwise a broken
// provider would be retried indefinitely while the receipt claimed a limit.
func (s *Store) BeginAstraExecution(ctx context.Context, id string, at time.Time) (Record, error) {
	return s.update(ctx, id, func(r *Record) error {
		if r.State != StateRunning {
			return contract.Fail(contract.FailurePermissionDenied, "coordination %s is not running", id)
		}
		if r.AstraExecutions >= MaxAstraExecutions {
			return contract.Fail(contract.FailurePermissionDenied, "coordination %s reached the Astra limit of %d", id, MaxAstraExecutions)
		}
		r.AstraExecutions++
		r.LastProgressAt = at
		return nil
	})
}

// RecordSolReview is part of ATENEA's public orchestration contract.
func (s *Store) RecordSolReview(ctx context.Context, id string, review ReviewReceipt, at time.Time) (Record, error) {
	return s.update(ctx, id, func(r *Record) error {
		if r.State != StateRunning {
			return contract.Fail(contract.FailurePermissionDenied, "coordination %s is not running", id)
		}
		if err := review.Validate(r.ActiveCycleID, "sol"); err != nil {
			return err
		}
		r.SolReview = &review
		r.LastProgressAt = at
		return nil
	})
}

// RecordAstraAudit records a fresh Astra result only after the matching fresh
// Sol review exists. Acceptance cannot be laundered from an audit of an older
// attempt or from a substituted model.
func (s *Store) RecordAstraAudit(ctx context.Context, id string, audit ReviewReceipt, at time.Time) (Record, error) {
	return s.update(ctx, id, func(r *Record) error {
		if r.State != StateRunning {
			return contract.Fail(contract.FailurePermissionDenied, "coordination %s is not running", id)
		}
		if r.SolReview == nil || r.SolReview.CycleID != audit.CycleID || !r.SolReview.Fresh {
			return contract.Fail(contract.FailureInvalidInput, "coordination %s requires a fresh Sol review before Astra", id)
		}
		if err := audit.Validate(r.ActiveCycleID, "astra"); err != nil {
			return err
		}
		if r.AstraExecutions <= len(r.AstraAudits) {
			return contract.Fail(contract.FailurePermissionDenied, "coordination %s has no reserved Astra execution", id)
		}
		if audit.WorkflowID != r.SolReview.WorkflowID || audit.SourceFingerprint != r.SolReview.SourceFingerprint {
			return contract.Fail(contract.FailureInvalidInput, "coordination %s Sol/Astra receipts do not share workflow and source", id)
		}
		r.AstraAudits = append(r.AstraAudits, audit)
		r.LastProgressAt = at
		return nil
	})
}

// AcceptReviews closes the review gate for a cycle. Both receipts must be
// fresh, successful, from this cycle, and identity-compatible.
func (s *Store) AcceptReviews(ctx context.Context, id string, at time.Time) (Record, error) {
	return s.update(ctx, id, func(r *Record) error {
		if r.SolReview == nil {
			return contract.Fail(contract.FailureInvalidInput, "coordination %s has no Sol review", id)
		}
		if err := r.SolReview.Validate(r.ActiveCycleID, "sol"); err != nil {
			return err
		}
		if len(r.AstraAudits) == 0 {
			return contract.Fail(contract.FailureInvalidInput, "coordination %s has no Astra audit", id)
		}
		last := r.AstraAudits[len(r.AstraAudits)-1]
		if err := last.Validate(r.ActiveCycleID, "astra"); err != nil {
			return err
		}
		if last.CycleID != r.SolReview.CycleID {
			return contract.Fail(contract.FailureInvalidInput, "coordination %s reviews are from different cycles", id)
		}
		if last.WorkflowID != r.SolReview.WorkflowID || last.SourceFingerprint != r.SolReview.SourceFingerprint {
			return contract.Fail(contract.FailureInvalidInput, "coordination %s Sol/Astra receipts do not share workflow and source", id)
		}
		if !slices.Contains(r.AcceptedCycleIDs, r.ActiveCycleID) {
			r.AcceptedCycleIDs = append(r.AcceptedCycleIDs, r.ActiveCycleID)
		}
		r.State, r.LastProgressAt = StateRunning, at
		return nil
	})
}

// SetState mirrors a durable child watchdog decision into the coordinator.
func (s *Store) SetState(ctx context.Context, id, state, message string, at time.Time) (Record, error) {
	return s.update(ctx, id, func(r *Record) error {
		if state != StateRunning && state != StateAttentionRequired && state != StateUncertain {
			return contract.Fail(contract.FailureInvalidInput, "coordination: unknown state %q", state)
		}
		r.State, r.LastProgressAt, r.Error = state, at, strings.TrimSpace(message)
		if state != StateRunning {
			r.Status = StatusStopped
		}
		return nil
	})
}

// Watchdog checks progress without retrying anything. At the deadline it
// pauses the coordinator and marks the outcome uncertain when a child could
// have caused an effect. The caller must obtain an explicit human decision
// before resuming.
func (s *Store) Watchdog(ctx context.Context, id string, now time.Time, timeout time.Duration, effectInFlight bool) (Record, bool, error) {
	if timeout <= 0 {
		timeout = WatchdogTimeout
	}
	var tripped bool
	record, err := s.update(ctx, id, func(r *Record) error {
		if r.State != StateRunning || r.Status != StatusRunning || r.LastProgressAt.IsZero() || now.Sub(r.LastProgressAt) < timeout {
			return nil
		}
		tripped = true
		if effectInFlight {
			r.State = StateUncertain
		} else {
			r.State = StateAttentionRequired
		}
		r.LastProgressAt = now
		r.Status = StatusStopped
		r.Error = "watchdog: no progress for " + timeout.String()
		return nil
	})
	return record, tripped, err
}
