package workflow_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type boundedSpecialistDispatcher struct {
	ids     atomic.Int64
	forked  atomic.Int64
	started chan string
	release chan struct{}
}

func (d *boundedSpecialistDispatcher) NextID() string {
	return fmt.Sprintf("specialist-%d", d.ids.Add(1))
}

func (d *boundedSpecialistDispatcher) Dispatch(ctx context.Context, call agent.Dispatch) (contract.Report, contract.Assignment, error) {
	select {
	case d.started <- call.ID:
	case <-ctx.Done():
		return contract.Report{}, contract.Assignment{}, ctx.Err()
	}
	select {
	case <-d.release:
		return contract.Report{Verdict: contract.VerdictOK}, contract.Assignment{}, nil
	case <-ctx.Done():
		return contract.Report{}, contract.Assignment{}, ctx.Err()
	}
}

func (d *boundedSpecialistDispatcher) PrepareNativeChild(_ context.Context, _ agent.Dispatch) (string, error) {
	return fmt.Sprintf("native-child-%d", d.forked.Add(1)), nil
}

func TestCoordinatorRunsAtMostTwoSpecialistsAtOnce(t *testing.T) {
	store, err := workflow.Open(t.Context(), filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	limits := contract.Limits{MaxDuration: time.Minute, MaxTokens: 100}
	parent := contract.RootAssignment("coordinator", "atenea-coordinator", contract.AgentOrchestrator,
		contract.Task{Objective: "coordinate", Criterion: "two specialists at once"}, limits)
	parent.Context = []contract.ContextLevel{contract.ContextRepository}
	parent.Effects = []contract.Effect{contract.EffectRead}
	parent.Route = &contract.Route{ThreadID: "coordinator-thread"}
	dispatcher := &boundedSpecialistDispatcher{started: make(chan string, 3), release: make(chan struct{}, 3)}
	worker := declared("worker", "/bin/true", config.PoolAgent)
	engine, err := workflow.New(workflow.Options{Runner: dispatcher, Store: store, Types: []config.AgentType{worker}, Lanes: noCeiling(), Parent: &parent})
	if err != nil {
		t.Fatal(err)
	}
	steps := make([]workflow.Step, 3)
	for i := range steps {
		steps[i] = workflow.Step{ID: fmt.Sprintf("work-%d", i), TypeName: "worker", Limits: limits,
			Task: contract.Task{Objective: "inspect", Criterion: "answer"}, Permission: contract.Permission{Effects: []contract.Effect{contract.EffectRead}},
			Route: &contract.Route{Model: "gpt-5.6-sol", RequestedModel: "gpt-5.6-sol", Backend: "codex", Role: "research", VisibilityRequired: true}}
	}
	run, _, err := engine.Create(t.Context(), workflow.Graph{Task: "work", GrantUSD: 1, Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := engine.Launch(t.Context(), run.ID); done <- err }()
	for i := 0; i < 2; i++ {
		select {
		case <-dispatcher.started:
		case <-time.After(2 * time.Second):
			t.Fatal("two specialists did not start")
		}
	}
	select {
	case third := <-dispatcher.started:
		t.Fatalf("third specialist %s started before a slot was released", third)
	case <-time.After(100 * time.Millisecond):
	}
	dispatcher.release <- struct{}{}
	select {
	case <-dispatcher.started:
	case <-time.After(2 * time.Second):
		t.Fatal("third specialist did not start after a slot was released")
	}
	dispatcher.release <- struct{}{}
	dispatcher.release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("workflow did not finish")
	}
	if got := dispatcher.forked.Load(); got != 3 {
		t.Fatalf("native forks = %d, want one per specialist", got)
	}
}
