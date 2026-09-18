package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type reviewForkDispatcher struct {
	ids, forks, reviews int
}

func (d *reviewForkDispatcher) NextID() string {
	d.ids++
	if d.ids == 1 {
		return "reader-run"
	}
	return "review-run"
}

func (d *reviewForkDispatcher) PrepareNativeChild(context.Context, agent.Dispatch) (string, error) {
	d.forks++
	return "review-thread", nil
}

func (d *reviewForkDispatcher) Dispatch(_ context.Context, call agent.Dispatch) (contract.Report, contract.Assignment, error) {
	if call.TypeName == "judge" {
		d.reviews++
	}
	return contract.Report{Verdict: contract.VerdictOK, Result: map[string]any{"ok": true}}, contract.Assignment{}, nil
}

func TestFullAuditChangedSubjectBlockedBeforeDispatch(t *testing.T) {
	repo, agents := t.TempDir(), t.TempDir()
	source := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(source, []byte("reviewed version"), 0600); err != nil {
		t.Fatal(err)
	}
	reviewer, saved := records(t, agents, "judge")
	changed := false
	h := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Repository: "repo", RepositoryRoot: repo,
		Activity: func(notices []workflow.ActivityNotice) error {
			for _, n := range notices {
				if n.Tool == "judge" {
					changed = true
					return os.WriteFile(source, []byte("different version"), 0600)
				}
			}
			return nil
		}}, "", declared("reader", answers(t, agents, "reader"), config.PoolAgent), declared("judge", reviewer, config.PoolReview))
	run, err := h.engine.Start(t.Context(), graphOf(step("w", "reader", nil), reviewing(step("r", "judge", nil), "w")))
	if !changed {
		t.Fatal("fixture did not mutate sources at review publication")
	}
	if _, statErr := os.Stat(saved); statErr == nil {
		t.Fatalf("reviewer was invoked after source mutation; workflow error=%v states=%v", err, statuses(t, run))
	}
	if err == nil {
		t.Fatal("source mutation was not reported")
	}
}

func TestReviewResultIsNotAcceptedAfterSourcesChangeDuringRun(t *testing.T) {
	repo, agents := t.TempDir(), t.TempDir()
	source := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(source, []byte("reviewed version"), 0600); err != nil {
		t.Fatal(err)
	}
	reviewer := stub(t, agents, "judge", "printf 'different version' > '"+source+"'\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'")
	h := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Repository: "repo", RepositoryRoot: repo}, "",
		declared("reader", answers(t, agents, "reader"), config.PoolAgent), declared("judge", reviewer, config.PoolReview))
	run, err := h.engine.Start(t.Context(), graphOf(step("w", "reader", nil), reviewing(step("r", "judge", nil), "w")))
	if err == nil {
		t.Fatal("stale review was not reported")
	}
	if got := statuses(t, run)["r"]; got != workflow.StatusIncomplete.String() {
		t.Fatalf("review of changed sources has status %s, want incomplete", got)
	}
	if run.Stop != workflow.StopUnjudged {
		t.Fatalf("review of changed sources has stop %s, want unjudged", run.Stop)
	}
}

func TestNativeReviewDoesNotForkAfterSourceMutation(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(source, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := workflow.Open(t.Context(), filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	parent := contract.RootAssignment("coordinator-run", "atenea-coordinator", contract.AgentOrchestrator,
		contract.Task{Objective: "coordinate", Criterion: "bounded"}, contract.Limits{MaxDuration: time.Minute, MaxTokens: 100})
	parent.Context = []contract.ContextLevel{contract.ContextRepository}
	parent.Effects = []contract.Effect{contract.EffectRead}
	parent.Route = &contract.Route{ThreadID: "coordinator-thread"}
	dispatcher := &reviewForkDispatcher{}
	engine, err := workflow.New(workflow.Options{Runner: dispatcher, Store: store,
		Types: []config.AgentType{declared("reader", "/bin/true", config.PoolAgent), declared("judge", "/bin/true", config.PoolReview)},
		Lanes: noCeiling(), Parent: &parent, Repository: "repo", RepositoryRoot: repo,
		Activity: func(notices []workflow.ActivityNotice) error {
			for _, notice := range notices {
				if notice.Tool == "judge" {
					return os.WriteFile(source, []byte("after"), 0600)
				}
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	review := reviewing(step("r", "judge", nil), "w")
	review.Route = &contract.Route{Model: "gpt-5.6-sol", RequestedModel: "gpt-5.6-sol", Backend: "codex",
		Role: "review", ReasoningEffort: "medium", RequestedReasoningEffort: "medium", VisibilityRequired: true}
	run, _, err := engine.Create(t.Context(), graphOf(step("w", "reader", nil), review))
	if err != nil {
		t.Fatal(err)
	}
	finished, err := engine.Launch(t.Context(), run.ID)
	if err == nil {
		t.Fatal("native review launched after source change")
	}
	if body, readErr := os.ReadFile(source); readErr != nil || string(body) != "after" {
		t.Fatalf("activity did not mutate sources: %q, %v", body, readErr)
	}
	if dispatcher.forks != 0 || dispatcher.reviews != 0 {
		t.Fatalf("native review forked or ran despite source mutation: forks=%d reviews=%d", dispatcher.forks, dispatcher.reviews)
	}
	if row := stepOf(t, finished, "r"); row.Status != workflow.StatusInterrupted || row.Step.Route.NativeForkState != "" {
		t.Fatalf("native review state after failed preflight: %+v", row)
	}
}
