package workflow_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Two Ateneas resuming the same id used to both win.
//
// takeOver read writer_pid, found the holder gone, and Own overwrote it with an
// unconditional UPDATE -- so between the read and the write there was nothing
// at all. Both processes saw a free run, both claimed it, and both executed the
// graph: every step dispatched twice, the grant charged twice, both write
// effects applied, and the two of them overwriting each other's Finish on the
// same row.
//
// The claim is now conditional on what the caller observed, so the loser finds
// out at the moment it would have started dispatching.
func TestARunCanOnlyBeTakenOverOnce(t *testing.T) {
	dir := t.TempDir()
	worker := declared("worker", answers(t, dir, "worker"), config.PoolAgent)
	first := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(), PID: 11, Alive: func(int) bool { return false },
	}, dir, worker)

	run, _, err := first.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	recorded, err := first.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// The winner: it claims against what it actually observed.
	if err := first.state.Own(t.Context(), run.ID, 11, recorded.WriterPID); err != nil {
		t.Fatalf("the first claim was refused: %v", err)
	}

	// The loser: a second Atenea that read the same row a moment earlier, and
	// is only now getting around to writing.
	err = first.state.Own(t.Context(), run.ID, 22, recorded.WriterPID)
	if err == nil {
		t.Fatal("a second Atenea took over a run that had already been claimed")
	}
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Errorf("kind = %v, want unavailable: somebody else holds it, nothing is malformed",
			contract.KindOf(err))
	}
	// Naming the winner is what turns "try again" into something actionable.
	if !strings.Contains(err.Error(), "11") {
		t.Errorf("refusal = %q, want it to name the pid that won", err)
	}

	// The holder re-entering its own run is not a race with itself.
	if err := first.state.Own(t.Context(), run.ID, 11, 11); err != nil {
		t.Errorf("the holder could not re-enter its own run: %v", err)
	}
}

// A claim against an id that is not there is a different answer from a claim
// somebody else won, and a caller acts differently on each.
func TestClaimingAnAbsentRunSaysSo(t *testing.T) {
	h := newHarness(t, noCeiling(), declared("worker", answers(t, t.TempDir(), "worker"), config.PoolAgent))
	err := h.state.Own(t.Context(), "wf-nothing", 1, 0)
	if err == nil {
		t.Fatal("claiming a workflow that does not exist was accepted")
	}
	if contract.KindOf(err) != contract.FailureNotFound {
		t.Errorf("kind = %v, want not_found", contract.KindOf(err))
	}
}
