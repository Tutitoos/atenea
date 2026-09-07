package workflow_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type recoveryDispatcher struct {
	calls int
	mode  string
}

func (d *recoveryDispatcher) NextID() string { return fmt.Sprintf("recovery-dispatch-%d", d.calls+1) }

func (d *recoveryDispatcher) Dispatch(_ context.Context, _ agent.Dispatch) (contract.Report, contract.Assignment, error) {
	d.calls++
	if d.mode == "preflight" && d.calls == 1 {
		return contract.Report{InvokedKnown: true, Invoked: false, Verdict: contract.VerdictIncomplete, Reason: contract.Reason{Kind: contract.FailureUnavailable, Text: "provider preflight unavailable"}}, contract.Assignment{}, contract.Fail(contract.FailureUnavailable, "provider preflight unavailable")
	}
	amount := 0.10
	if d.calls == 1 || d.mode == "always" {
		return contract.Report{InvokedKnown: true, Invoked: true, Verdict: contract.VerdictIncomplete, Reason: contract.Reason{Kind: contract.FailureUnavailable, Text: "provider unavailable"}, Spent: contract.Charge{USD: &amount, PricedBy: "fixture"}}, contract.Assignment{}, contract.Fail(contract.FailureUnavailable, "provider unavailable")
	}
	return contract.Report{InvokedKnown: true, Invoked: true, Verdict: contract.VerdictOK, Result: map[string]any{"ok": true}, Spent: contract.Charge{USD: &amount, PricedBy: "fixture"}}, contract.Assignment{}, nil
}

func newExplicitRecoveryHarness(t *testing.T, mode string, opts workflow.Options) (*harness, *recoveryDispatcher) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "traces.db")
	traces, err := trace.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = traces.Close() })
	state, err := workflow.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	dispatcher := &recoveryDispatcher{mode: mode}
	typeDef := config.AgentType{
		Spec:    contract.AgentTypeSpec{Name: "reader", Kind: contract.AgentSpecialized, Result: []contract.Field{{Name: "ok", Type: contract.TypeBool, Required: true}}},
		Command: "/bin/sh", Context: []contract.ContextLevel{contract.ContextRepository}, Effects: []contract.Effect{contract.EffectRead},
		Limits: contract.Limits{MaxDuration: time.Second, MaxTokens: 100}, Pool: config.PoolAgent,
	}
	opts.Runner, opts.Store, opts.Types, opts.RepositoryRoot = dispatcher, state, []config.AgentType{typeDef}, dir
	engine, err := workflow.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{engine: engine, state: state, traces: traces, dir: dir}, dispatcher
}

func recoveryGraph() workflow.Graph {
	read := step("read", "reader", nil, contract.EffectRead)
	read.Route = &contract.Route{Model: "fixture-model", RequestedModel: "fixture-model", ObservedModel: "fixture-model", Backend: "fixture-backend"}
	return workflow.Graph{Task: "recovery test", GrantUSD: 2, Steps: []workflow.Step{read}}
}

type watchdogDispatcher struct {
	calls   atomic.Int32
	started chan struct{}
}

func (d *watchdogDispatcher) NextID() string {
	return fmt.Sprintf("watchdog-dispatch-%d", d.calls.Load()+1)
}

func (d *watchdogDispatcher) Dispatch(ctx context.Context, _ agent.Dispatch) (contract.Report, contract.Assignment, error) {
	if d.calls.Add(1) == 1 {
		close(d.started)
	}
	<-ctx.Done()
	return contract.Report{
		InvokedKnown: true,
		Invoked:      true,
		Verdict:      contract.VerdictIncomplete,
		Reason:       contract.Reason{Kind: contract.FailureCanceled, Text: "watchdog canceled fixture"},
	}, contract.Assignment{}, contract.Fail(contract.FailureCanceled, "watchdog canceled fixture")
}

func TestEngineWatchdogPausesWithoutRetryingAnInFlightStep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traces.db")
	traces, err := trace.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = traces.Close() })
	state, err := workflow.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })

	dispatcher := &watchdogDispatcher{started: make(chan struct{})}
	typeDef := config.AgentType{
		Spec:    contract.AgentTypeSpec{Name: "reader", Kind: contract.AgentSpecialized, Result: []contract.Field{{Name: "ok", Type: contract.TypeBool, Required: true}}},
		Command: "/bin/sh", Context: []contract.ContextLevel{contract.ContextRepository}, Effects: []contract.Effect{contract.EffectRead},
		Limits: contract.Limits{MaxDuration: time.Second, MaxTokens: 100}, Pool: config.PoolAgent,
	}
	clock := time.Now().Add(-time.Second)
	engine, err := workflow.New(workflow.Options{
		Runner: dispatcher, Store: state, Types: []config.AgentType{typeDef},
		Watchdog: 50 * time.Millisecond, Poll: 5 * time.Millisecond,
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}

	graph := graphOf(step("read", "reader", nil, contract.EffectRead))
	result := make(chan struct {
		run workflow.Run
		err error
	}, 1)
	go func() {
		run, runErr := engine.Start(context.Background(), graph)
		result <- struct {
			run workflow.Run
			err error
		}{run: run, err: runErr}
	}()
	select {
	case <-dispatcher.started:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture dispatch did not start")
	}
	select {
	case got := <-result:
		if got.err == nil || !strings.Contains(got.err.Error(), "watchdog") {
			t.Fatalf("Start err = %v, want watchdog pause", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not stop the in-flight workflow")
	}
	if got := dispatcher.calls.Load(); got != 1 {
		t.Fatalf("dispatch calls = %d, want exactly one after watchdog pause", got)
	}
	// The run id is stable in the returned record; reload it through the
	// store's list to verify the durable paused state without relying on an
	// in-memory signal.
	runs, err := state.List(t.Context(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("workflow list = %d, err=%v", len(runs), err)
	}
	loaded, err := state.Load(t.Context(), runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.WatchdogState != workflow.StateAttentionRequired || loaded.Stop != workflow.StopUnjudged || loaded.Closed {
		t.Fatalf("paused run = %+v, want attention_required/unjudged/open", loaded)
	}
}

func TestEngineAutomaticallyRecoversOneFixedReadRoute(t *testing.T) {
	h, dispatcher := newExplicitRecoveryHarness(t, "retry", workflow.Options{MaxRetries: 1, MaxBudgetUSD: 2})
	run, err := h.engine.Start(t.Context(), recoveryGraph())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	row := stepOf(t, run, "read")
	if row.Status != workflow.StatusOK || row.Attempt != 2 || dispatcher.calls != 2 {
		t.Fatalf("row = %#v calls=%d, want successful second attempt", row, dispatcher.calls)
	}
	if !row.InvokedKnown || !row.Invoked || run.Recovery == nil || run.Recovery.RetryUsed != 1 || run.Recovery.RetryLimit != 1 {
		t.Fatalf("recovery = %#v row=%#v, want one persisted retry", run.Recovery, row)
	}
	attempts, err := h.state.Attempts(t.Context(), run.ID, "read")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Reason.Kind != contract.FailureUnavailable || attempts[0].Spent.USD == nil || !attempts[0].Invoked {
		t.Fatalf("persisted recovery attempt = %#v", attempts)
	}
	reloaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil || reloaded.Recovery == nil || reloaded.Recovery.RetryUsed != 1 {
		t.Fatalf("reloaded recovery = %#v err=%v", reloaded.Recovery, err)
	}
	if !strings.Contains(reloaded.Recovery.Reason, "provider unavailable") {
		t.Fatalf("reloaded recovery reason = %q, want the recovered transient cause", reloaded.Recovery.Reason)
	}
}

func TestEngineDoesNotRetryAtSpendingCeiling(t *testing.T) {
	h, dispatcher := newExplicitRecoveryHarness(t, "always", workflow.Options{MaxRetries: 1, MaxBudgetUSD: 2})
	graph := recoveryGraph()
	graph.Steps[0].Permission.BudgetUSD = 0.10
	run, err := h.engine.Start(t.Context(), graph)
	if err == nil || contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("Start error = %v, want durable spending-ceiling stop", err)
	}
	row := stepOf(t, run, "read")
	if row.Status != workflow.StatusIncomplete || row.Attempt != 1 || dispatcher.calls != 1 {
		t.Fatalf("row = %#v calls=%d, want no retry at ceiling", row, dispatcher.calls)
	}
	if run.Recovery == nil || run.Recovery.NextOrStopped == "" || len(run.Superseded) != 0 {
		t.Fatalf("recovery = %#v superseded=%#v", run.Recovery, run.Superseded)
	}
}

func TestEngineRetriesKnownPreflightFailureWithNoCharge(t *testing.T) {
	h, dispatcher := newExplicitRecoveryHarness(t, "preflight", workflow.Options{MaxRetries: 1, MaxBudgetUSD: 2})
	run, err := h.engine.Start(t.Context(), recoveryGraph())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	row := stepOf(t, run, "read")
	if row.Status != workflow.StatusOK || row.Attempt != 2 || dispatcher.calls != 2 {
		t.Fatalf("row = %#v calls=%d, want preflight retry", row, dispatcher.calls)
	}
	attempts, err := h.state.Attempts(t.Context(), run.ID, "read")
	if err != nil || len(attempts) != 1 || !attempts[0].InvokedKnown || attempts[0].Invoked || attempts[0].Spent.USD != nil {
		t.Fatalf("preflight receipt = %#v err=%v", attempts, err)
	}
}

func TestEnginePersistsGlobalRecoveryLimitWithoutFallback(t *testing.T) {
	h, dispatcher := newExplicitRecoveryHarness(t, "always", workflow.Options{MaxRetries: 1, MaxBudgetUSD: 2})
	run, err := h.engine.Start(t.Context(), recoveryGraph())
	if err == nil || contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("Start error = %v, want bounded recovery failure", err)
	}
	row := stepOf(t, run, "read")
	if row.Status != workflow.StatusIncomplete || row.Attempt != 2 || dispatcher.calls != 2 {
		t.Fatalf("row = %#v calls=%d, want final bounded attempt", row, dispatcher.calls)
	}
	if run.Recovery == nil || run.Recovery.RetryUsed != 1 || run.Recovery.RetryLimit != 1 || len(run.Superseded) != 1 {
		t.Fatalf("recovery = %#v superseded=%#v", run.Recovery, run.Superseded)
	}
}
