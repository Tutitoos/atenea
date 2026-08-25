package workflow_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
)

// A refused redo has to leave the step exactly as it found it.
//
// It did not. checkFunding ran last -- after Regrant, after the gate was asked
// and approved, and after every named step had been Reset and resharded -- so a
// refusal there left the step `pending`. CutAtItsCeiling() is false for a
// pending step, so a second redo refused it; the run was still closed, so
// resume refused it too. The step was unreachable by every command, the money
// it had spent was counted twice (once on the live row, once in the archive),
// and the only record of what happened was a gate answered "approved" for work
// that never ran.
func TestARefusedRedoLeavesTheStepRedoableAgain(t *testing.T) {
	dir := t.TempDir()
	cheap := floorsOf("current", "reader", "claude-opus-5", measuredFloor(0.01))
	h := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(), Repository: "current", Floors: cheap,
		ModelFor: func(string) string { return "claude-opus-5" },
	}, dir, declared("reader", cutAtCeiling(t, dir, "reader", 0.50), config.PoolAgent))

	run, err := h.engine.Start(t.Context(), commissioned(funded("a", "reader", 0.10)))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := statuses(t, run)["a"]; got != "incomplete" {
		t.Fatalf("step a is %s, want incomplete", got)
	}
	before, ok := rowFor(run, "a")
	if !ok {
		t.Fatal("the run has no step a")
	}

	// $0.50 dead spend x 1.08 = $0.54, so $0.52 is a real raise over the old
	// $0.10 share and still short of what the rule says the step needs.
	if _, err := h.engine.Redo(t.Context(), run.ID,
		[]workflow.Raise{{StepID: "a", USD: 0.52}}, 0.52); err == nil {
		t.Fatal("a raise under its own dead spend was dispatched")
	}

	after, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	row, ok := rowFor(after, "a")
	if !ok {
		t.Fatal("the run lost step a")
	}
	if row.Status != before.Status {
		t.Errorf("step a is %s after a refused redo, want the %s it was: "+
			"a refusal that moves the step is a refusal nobody can retry",
			row.Status, before.Status)
	}
	if !row.CutAtItsCeiling() {
		t.Error("step a is no longer redoable after a refused redo")
	}
	if row.Step.Permission.BudgetUSD != before.Step.Permission.BudgetUSD {
		t.Errorf("share = $%.2f after a refused redo, want the $%.2f it was",
			row.Step.Permission.BudgetUSD, before.Step.Permission.BudgetUSD)
	}
	if len(after.Superseded) != 0 {
		t.Errorf("a refused redo filed %d attempt(s): nothing was replaced",
			len(after.Superseded))
	}

	// And the proof that it is really recoverable: the raise the rule does
	// accept still works, on the same step, right after the refusal.
	if _, err := h.engine.Redo(t.Context(), run.ID,
		[]workflow.Raise{{StepID: "a", USD: 0.60}}, 0.60); err != nil {
		if !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("the step could not be redone after a refusal: %v", err)
		}
	}
}

// The grant is not raised either. A redo refused for underfunding that had
// already moved the ceiling would leave the run authorized for money nobody
// approved spending.
func TestARefusedRedoDoesNotMoveTheGrant(t *testing.T) {
	dir := t.TempDir()
	cheap := floorsOf("current", "reader", "claude-opus-5", measuredFloor(0.01))
	h := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(), Repository: "current", Floors: cheap,
		ModelFor: func(string) string { return "claude-opus-5" },
	}, dir, declared("reader", cutAtCeiling(t, dir, "reader", 0.50), config.PoolAgent))

	run, err := h.engine.Start(t.Context(), commissioned(funded("a", "reader", 0.10)))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	granted := run.GrantUSD

	if _, err := h.engine.Redo(t.Context(), run.ID,
		[]workflow.Raise{{StepID: "a", USD: 0.52}}, 5.00); err == nil {
		t.Fatal("a raise under its own dead spend was dispatched")
	}
	after, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.GrantUSD != granted {
		t.Errorf("grant = $%.2f after a refused redo, want the $%.2f it was",
			after.GrantUSD, granted)
	}
}
