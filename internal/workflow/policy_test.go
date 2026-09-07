package workflow_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestWorkflowPolicyIsPersistedAcrossReconnect(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessWith(t, workflow.Options{
		Lanes:       config.Workflow{MaxParallelAgent: 2, MaxParallelReview: 1},
		ProfileName: "workflow-test-v2", MaxDuration: time.Minute, MaxRetries: 2,
	}, dir, declared("reader", answers(t, dir, "reader"), config.PoolAgent))
	graph := graphOf(step("read", "reader", nil, contract.EffectRead))
	run, _, err := h.engine.Create(t.Context(), graph)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Policy.Version != "workflow-test-v2" || loaded.Policy.MaxDuration != time.Minute || loaded.Policy.MaxRetries != 2 {
		t.Fatalf("policy = %+v, want version, deadline and retry ceiling persisted", loaded.Policy)
	}
	if loaded.Policy.MaxParallelAgent != 2 || loaded.Policy.MaxParallelReview != 1 {
		t.Fatalf("policy lanes = %+v, want persisted lane ceilings", loaded.Policy)
	}
	// A new engine with a wider current configuration still reads the stored
	// ceiling. Reconnects cannot widen an already-created workflow.
	reconnected := newHarnessWith(t, workflow.Options{
		Lanes:       config.Workflow{MaxParallelAgent: 99, MaxParallelReview: 99},
		ProfileName: "workflow-wider", MaxDuration: 10 * time.Hour, MaxRetries: 99,
	}, dir, declared("reader", answers(t, dir, "reader2"), config.PoolAgent))
	got, err := reconnected.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("reconnected Load: %v", err)
	}
	if got.Policy.Version != loaded.Policy.Version || got.Policy.MaxDuration != loaded.Policy.MaxDuration || got.Policy.MaxRetries != loaded.Policy.MaxRetries {
		t.Fatalf("reconnected policy widened: got %+v, want %+v", got.Policy, loaded.Policy)
	}
}

func TestGraphCoordinatorMetadataAndDispatchLimitsSurviveReconnect(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessWith(t, workflow.Options{}, dir,
		declared("reader", answers(t, dir, "reader"), config.PoolAgent))
	graph := graphOf(step("read", "reader", nil))
	graph.Criterion = "all findings cite a source"
	graph.Limits = contract.Limits{MaxDuration: time.Minute, MaxTokens: 321}
	run, _, err := h.engine.Create(t.Context(), graph)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Policy.Criterion != graph.Criterion || loaded.Policy.MaxDuration != time.Minute || loaded.Policy.MaxTokens != 321 {
		t.Fatalf("policy metadata = %+v", loaded.Policy)
	}
	if got := stepOf(t, loaded, "read").Step.Limits; got != graph.Limits {
		t.Fatalf("step limits = %+v, want %+v", got, graph.Limits)
	}
}

func TestSelectedWorkflowProfileIsMaterializedWithDigestAndBudget(t *testing.T) {
	dir := t.TempDir()
	profiles := []config.WorkflowProfile{{Name: "bounded", Version: "v3", MaxBudgetUSD: 4,
		MaxDuration: time.Minute, MaxRetries: 2, MaxParallelAgent: 1, MaxParallelReview: 1}}
	h := newHarnessWith(t, workflow.Options{ProfileName: "bounded", Profiles: profiles}, dir,
		declared("reader", answers(t, dir, "reader"), config.PoolAgent))
	graph := graphOf(step("read", "reader", nil, contract.EffectRead))
	run, _, err := h.engine.Create(t.Context(), graph)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Policy.Name != "bounded" || loaded.Policy.Version != "v3" || loaded.Policy.Digest == "" || loaded.Policy.MaxBudgetUSD != 4 {
		t.Fatalf("policy = %+v, want selected profile materialized", loaded.Policy)
	}
	if loaded.Policy.MaxParallelAgent != 1 || loaded.Policy.MaxParallelReview != 1 {
		t.Fatalf("policy lanes = %+v", loaded.Policy)
	}
}

func TestRegrantCannotExceedPersistedWorkflowPolicyBudget(t *testing.T) {
	dir := t.TempDir()
	profiles := []config.WorkflowProfile{{Name: "bounded", Version: "v1", MaxBudgetUSD: 1.00}}
	h := newHarnessWith(t, workflow.Options{ProfileName: "bounded", Profiles: profiles}, dir,
		declared("reader", answers(t, dir, "reader"), config.PoolAgent))
	one := step("read", "reader", nil, contract.EffectRead)
	one.Permission.BudgetUSD = 0.40
	graph := graphOf(one)
	graph.GrantUSD = 0.40
	run, _, err := h.engine.Create(t.Context(), graph)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Regrant(t.Context(), run.ID, 1.01); err == nil {
		t.Fatal("Regrant above the immutable policy ceiling succeeded")
	} else if !strings.Contains(err.Error(), "policy budget ceiling") {
		t.Fatalf("Regrant refusal = %v, want policy ceiling", err)
	}
	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.GrantUSD != 0.40 {
		t.Fatalf("grant changed after refused regrant: %.2f", loaded.GrantUSD)
	}
}

func TestWorkflowDeadlineIsCheckedAfterReconnect(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessWith(t, workflow.Options{MaxDuration: time.Nanosecond}, dir,
		declared("reader", answers(t, dir, "reader"), config.PoolAgent))
	run, gate, err := h.engine.Create(t.Context(), graphOf(step("read", "reader", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approveGate(t, h, gate)
	time.Sleep(time.Millisecond)
	if _, err := h.engine.Run(t.Context(), run.ID); err == nil || !strings.Contains(err.Error(), "active duration limit") {
		t.Fatalf("Run after deadline = %v, want durable deadline refusal", err)
	}
}

func TestActiveIntervalExcludesHumanWait(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessWith(t, workflow.Options{}, dir,
		declared("reader", answers(t, dir, "reader"), config.PoolAgent))
	run, _, err := h.engine.Create(t.Context(), graphOf(step("read", "reader", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	if err := h.state.StartActive(t.Context(), run.ID, base); err != nil {
		t.Fatalf("StartActive: %v", err)
	}
	if err := h.state.EndActive(t.Context(), run.ID, base.Add(2*time.Second)); err != nil {
		t.Fatalf("EndActive: %v", err)
	}
	// The interval between these calls represents a person considering a
	// gate. It is deliberately not bracketed by active markers.
	if err := h.state.StartActive(t.Context(), run.ID, base.Add(10*time.Second)); err != nil {
		t.Fatalf("StartActive after gate: %v", err)
	}
	if err := h.state.EndActive(t.Context(), run.ID, base.Add(11*time.Second)); err != nil {
		t.Fatalf("EndActive after gate: %v", err)
	}
	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ActiveDuration < 2990*time.Millisecond || loaded.ActiveDuration > 3010*time.Millisecond {
		t.Fatalf("active duration = %s, want 3s excluding human wait", loaded.ActiveDuration)
	}
}

func approveGate(t *testing.T, h *harness, gate workflow.Gate) {
	t.Helper()
	if _, err := h.state.Answer(t.Context(), gate.RunID, gate.Ordinal, workflow.DecisionApproved, workflow.Hand("test"), "", time.Now()); err != nil {
		t.Fatalf("answer launch: %v", err)
	}
}
