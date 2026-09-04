package workflow_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
)

func TestApprovedExpansionSurvivesRestartExactlyOnce(t *testing.T) {
	for _, applied := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("applied=%v/legacy=%v", applied, legacy), func(t *testing.T) {
				dir := t.TempDir()
				worker := declared("worker", answers(t, dir, "worker"), config.PoolAgent)
				h := newHarnessOver(t, dir, noCeiling(), worker)
				run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
				if err != nil {
					t.Fatal(err)
				}
				approve(t, h, run.ID)
				gate, err := h.state.Ask(t.Context(), run.ID, workflow.KindApprove, workflow.Proposal{Steps: []workflow.Step{step("b", "worker", nil)}}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				gate, err = h.state.Answer(t.Context(), run.ID, gate.Ordinal, workflow.DecisionApproved, "fixture", "", time.Now())
				if err != nil {
					t.Fatal(err)
				}
				plan := workflow.Plan{Pools: map[string]config.Pool{"b": config.PoolAgent}}
				if applied {
					if err := h.state.Apply(t.Context(), run.ID, gate, plan); err != nil {
						t.Fatal(err)
					}
				}
				if legacy {
					db, err := sql.Open("sqlite", filepath.Join(dir, "traces.db"))
					if err != nil {
						t.Fatal(err)
					}
					_, err = db.Exec(`UPDATE workflow_gate SET applied=NULL WHERE workflow_id=? AND ordinal=?`, run.ID, gate.Ordinal)
					_ = db.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := h.state.Close(); err != nil {
					t.Fatal(err)
				}
				second := newHarnessOver(t, dir, noCeiling(), worker)
				out, err := second.engine.Resume(t.Context(), run.ID, nil)
				if err != nil {
					t.Fatal(err)
				}
				if !out.Closed || len(out.Steps) != 2 {
					t.Fatalf("incomplete graph: %+v", out)
				}
				for _, row := range out.Steps {
					if row.Status != workflow.StatusOK || row.Attempt != 1 {
						t.Fatalf("step repeated or omitted: %+v", row)
					}
				}
				if err := second.state.Apply(t.Context(), run.ID, gate, plan); err != nil {
					t.Fatalf("replay: %v", err)
				}
				if _, pending, err := second.state.PendingGate(t.Context(), run.ID); err != nil || pending {
					t.Fatalf("pending after application: %v %v", pending, err)
				}
			})
		}
	}
}

func TestGateApplicationRollsBackWithGraph(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(), declared("worker", answers(t, dir, "worker"), config.PoolAgent))
	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatal(err)
	}
	approve(t, h, run.ID)
	gate, err := h.state.Ask(t.Context(), run.ID, workflow.KindApprove, workflow.Proposal{Steps: []workflow.Step{step("b", "worker", nil), step("c", "worker", nil)}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	gate, err = h.state.Answer(t.Context(), run.ID, gate.Ordinal, workflow.DecisionApproved, "fixture", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "traces.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER reject_c BEFORE INSERT ON workflow_step WHEN NEW.id='c' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	plan := workflow.Plan{Pools: map[string]config.Pool{"b": config.PoolAgent, "c": config.PoolAgent}}
	if err := h.state.Apply(t.Context(), run.ID, gate, plan); err == nil {
		t.Fatal("failure fixture not triggered")
	}
	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil || len(loaded.Steps) != 1 {
		t.Fatalf("partial graph persisted: %+v %v", loaded, err)
	}
	if _, pending, err := h.state.PendingGate(t.Context(), run.ID); err != nil || !pending {
		t.Fatalf("approval lost on rollback: %v %v", pending, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_c`); err != nil {
		t.Fatal(err)
	}
	if err := h.state.Apply(t.Context(), run.ID, gate, plan); err != nil {
		t.Fatal(err)
	}
	if err := h.state.Apply(t.Context(), run.ID, gate, plan); err != nil {
		t.Fatal(err)
	}
}
