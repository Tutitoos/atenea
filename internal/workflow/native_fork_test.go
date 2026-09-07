package workflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type nativeForkDispatcher struct {
	forkCalls int
	dispatch  agent.Dispatch
	forkErr   error
}

func (d *nativeForkDispatcher) NextID() string { return "native-specialist-run" }

func (d *nativeForkDispatcher) PrepareNativeChild(_ context.Context, call agent.Dispatch) (string, error) {
	d.forkCalls++
	if d.forkErr != nil {
		return "", d.forkErr
	}
	if call.Route.NativeForkState != "pending" || call.Route.ParentThreadID != "coordinator-thread" {
		return "", errors.New("fork was not durably reserved before provider call")
	}
	return "specialist-thread", nil
}

func (d *nativeForkDispatcher) Dispatch(_ context.Context, call agent.Dispatch) (contract.Report, contract.Assignment, error) {
	d.dispatch = call
	return contract.Report{Verdict: contract.VerdictOK, ThreadID: call.Route.ThreadID}, contract.Assignment{}, nil
}

func nativeForkEngine(t *testing.T, dispatcher workflow.Dispatcher, activity ...func([]workflow.ActivityNotice) error) (*workflow.Engine, *workflow.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := workflow.Open(t.Context(), filepath.Join(dir, "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	limits := contract.Limits{MaxDuration: time.Minute, MaxTokens: 100}
	parent := contract.RootAssignment("coordinator-run", "atenea-coordinator", contract.AgentOrchestrator,
		contract.Task{Objective: "coordinate", Criterion: "bounded"}, limits)
	parent.Context = []contract.ContextLevel{contract.ContextRepository}
	parent.Effects = []contract.Effect{contract.EffectRead}
	parent.Route = &contract.Route{ThreadID: "coordinator-thread"}
	worker := declared("worker", "/bin/true", config.PoolAgent)
	opts := workflow.Options{
		Runner: dispatcher, Store: store, Types: []config.AgentType{worker},
		Lanes: noCeiling(), Parent: &parent,
	}
	if len(activity) == 1 {
		opts.Activity = activity[0]
	}
	engine, err := workflow.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return engine, store
}

func nativeForkStep() workflow.Step {
	s := step("specialist", "worker", nil)
	s.Route = &contract.Route{
		Model: "gpt-5.6-sol", RequestedModel: "gpt-5.6-sol", Backend: "codex",
		Role: "research", ReasoningEffort: "medium", RequestedReasoningEffort: "medium",
		VisibilityRequired: true,
	}
	return s
}

func TestVisibleCodexSpecialistForksAndPersistsChildBeforeDispatch(t *testing.T) {
	dispatcher := &nativeForkDispatcher{}
	engine, store := nativeForkEngine(t, dispatcher)
	run, _, err := engine.Create(t.Context(), graphOf(nativeForkStep()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Launch(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if dispatcher.forkCalls != 1 {
		t.Fatalf("fork calls = %d, want 1", dispatcher.forkCalls)
	}
	if dispatcher.dispatch.Route == nil || dispatcher.dispatch.Route.ThreadID != "specialist-thread" ||
		dispatcher.dispatch.Route.ParentThreadID != "coordinator-thread" || dispatcher.dispatch.Route.NativeForkState != "complete" {
		t.Fatalf("dispatch route = %+v", dispatcher.dispatch.Route)
	}
	loaded, err := store.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := stepOf(t, loaded, "specialist").Step.Route
	if got == nil || got.ThreadID != "specialist-thread" || got.NativeForkState != "complete" {
		t.Fatalf("persisted route = %+v", got)
	}
}

func TestUncertainNativeForkBlocksASecondProviderAttempt(t *testing.T) {
	dispatcher := &nativeForkDispatcher{forkErr: errors.New("provider connection lost")}
	engine, store := nativeForkEngine(t, dispatcher)
	run, _, err := engine.Create(t.Context(), graphOf(nativeForkStep()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Launch(t.Context(), run.ID); err == nil {
		t.Fatal("launch succeeded despite uncertain provider outcome")
	}
	if dispatcher.forkCalls != 1 {
		t.Fatalf("fork calls = %d, want 1", dispatcher.forkCalls)
	}
	loaded, err := store.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := stepOf(t, loaded, "specialist").Step.Route
	if got == nil || got.NativeForkState != "pending" || got.ThreadID != "" {
		t.Fatalf("persisted uncertain route = %+v", got)
	}
	if _, err := store.ReserveNativeFork(t.Context(), run.ID, "specialist", "coordinator-thread"); err == nil {
		t.Fatal("second reservation succeeded after uncertain external effect")
	}
	if dispatcher.forkCalls != 1 {
		t.Fatalf("fork calls after retry = %d, want 1", dispatcher.forkCalls)
	}
}

type noNativeForkDispatcher struct{}

func (*noNativeForkDispatcher) NextID() string { return "unsupported-native-run" }
func (*noNativeForkDispatcher) Dispatch(context.Context, agent.Dispatch) (contract.Report, contract.Assignment, error) {
	return contract.Report{}, contract.Assignment{}, errors.New("must not dispatch")
}

type canceledNativeForkDispatcher struct {
	entered chan struct{}
}

func (*canceledNativeForkDispatcher) NextID() string { return "canceled-native-run" }
func (d *canceledNativeForkDispatcher) PrepareNativeChild(ctx context.Context, _ agent.Dispatch) (string, error) {
	close(d.entered)
	<-ctx.Done()
	return "", ctx.Err()
}
func (*canceledNativeForkDispatcher) Dispatch(context.Context, agent.Dispatch) (contract.Report, contract.Assignment, error) {
	return contract.Report{}, contract.Assignment{}, errors.New("must not dispatch")
}

func TestCancellationDuringNativeForkLeavesOneUncertainReservation(t *testing.T) {
	dispatcher := &canceledNativeForkDispatcher{entered: make(chan struct{})}
	engine, store := nativeForkEngine(t, dispatcher)
	run, _, err := engine.Create(t.Context(), graphOf(nativeForkStep()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, launchErr := engine.Launch(ctx, run.ID)
		done <- launchErr
	}()
	select {
	case <-dispatcher.entered:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("native fork was not attempted")
	}
	if err := <-done; err == nil {
		t.Fatal("canceled native fork launch succeeded")
	}
	loaded, err := store.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := stepOf(t, loaded, "specialist").Step.Route
	if got == nil || got.NativeForkState != "pending" || got.ThreadID != "" {
		t.Fatalf("canceled fork route = %+v", got)
	}
}

func TestUnsupportedRunnerFailsBeforeReservingNativeFork(t *testing.T) {
	engine, store := nativeForkEngine(t, &noNativeForkDispatcher{})
	run, _, err := engine.Create(t.Context(), graphOf(nativeForkStep()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Launch(t.Context(), run.ID); err == nil {
		t.Fatal("launch succeeded without native fork support")
	}
	loaded, err := store.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := stepOf(t, loaded, "specialist").Step.Route
	if got == nil || got.NativeForkState != "" || got.ParentThreadID != "" || got.ThreadID != "" {
		t.Fatalf("unsupported runner mutated route = %+v", got)
	}
}

func TestNativeForkWaitsForActivityPublication(t *testing.T) {
	dispatcher := &nativeForkDispatcher{}
	engine, store := nativeForkEngine(t, dispatcher, func(batch []workflow.ActivityNotice) error {
		for _, notice := range batch {
			if notice.InvocationID == "native-specialist-run" {
				return errors.New("chat unavailable")
			}
		}
		return nil
	})
	run, _, err := engine.Create(t.Context(), graphOf(nativeForkStep()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Launch(t.Context(), run.ID); err == nil {
		t.Fatal("launch succeeded despite rejected invocation publication")
	}
	if dispatcher.forkCalls != 0 {
		t.Fatalf("fork calls = %d, want 0 before successful publication", dispatcher.forkCalls)
	}
	loaded, err := store.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	row := stepOf(t, loaded, "specialist")
	if row.Status != workflow.StatusInterrupted || row.Step.Route.NativeForkState != "" {
		t.Fatalf("step after publication failure = %+v", row)
	}
}

func TestOperatorCanBindAnInspectedPendingNativeFork(t *testing.T) {
	dispatcher := &nativeForkDispatcher{}
	engine, store := nativeForkEngine(t, dispatcher)
	run, _, err := engine.Create(t.Context(), graphOf(nativeForkStep()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveNativeFork(t.Context(), run.ID, "specialist", "coordinator-thread"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindNativeFork(t.Context(), run.ID, "specialist", "coordinator-thread"); err == nil {
		t.Fatal("operator bound the parent as its own child")
	}
	bound, err := store.BindNativeFork(t.Context(), run.ID, "specialist", "verified-child-thread")
	if err != nil {
		t.Fatal(err)
	}
	if bound.NativeForkState != "complete" || bound.ParentThreadID != "coordinator-thread" || bound.ThreadID != "verified-child-thread" {
		t.Fatalf("bound route = %+v", bound)
	}
	loaded, err := store.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := stepOf(t, loaded, "specialist").Step.Route
	if got == nil || got.NativeForkState != "complete" || got.ThreadID != "verified-child-thread" {
		t.Fatalf("persisted bound route = %+v", got)
	}
}
