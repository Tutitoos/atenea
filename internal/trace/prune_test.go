package trace_test

import (
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// closed writes a row that ran and finished at the time given.
func closed(t *testing.T, store *trace.Store, id string, at time.Time) {
	t.Helper()
	if err := store.Begin(t.Context(), trace.Row{
		ID: id, TypeName: "reader", Kind: contract.AgentSpecialized,
		Objective: "read something", StartedAt: at,
	}); err != nil {
		t.Fatalf("Begin %s: %v", id, err)
	}
	if err := store.Complete(t.Context(), id, at, contract.VerdictOK, contract.Reason{}, nil); err != nil {
		t.Fatalf("Complete %s: %v", id, err)
	}
}

// The record of what Atenea did grew forever. It is the only thing on this
// machine that carries the sentence somebody typed and what the run found, so
// how long it lives is a decision -- and forever was the absence of one.
func TestClosedTracesOlderThanTheWindowAreRemoved(t *testing.T) {
	store := store(t)
	now := time.Now()

	closed(t, store, "old", now.Add(-100*24*time.Hour))
	closed(t, store, "recent", now.Add(-2*24*time.Hour))

	removed, err := store.PruneIfDue(t.Context(), now, 90*24*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("PruneIfDue: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want the one older than the window", removed)
	}
	rows, err := store.List(t.Context(), trace.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "recent" {
		t.Errorf("rows = %v, want only the recent one", rows)
	}
}

// An open row is a run nobody saw finish, which is the one kind worth keeping
// past its age: it is the evidence that something died, and SweepOrphans is
// what turns it into a closed row with a reason.
func TestAnOpenTraceSurvivesTheWindow(t *testing.T) {
	store := store(t)
	now := time.Now()

	if err := store.Begin(t.Context(), trace.Row{
		ID: "orphan", TypeName: "reader", Kind: contract.AgentSpecialized,
		Objective: "died before it answered", StartedAt: now.Add(-200 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if _, err := store.PruneIfDue(t.Context(), now, 90*24*time.Hour, 24*time.Hour); err != nil {
		t.Fatalf("PruneIfDue: %v", err)
	}
	rows, err := store.List(t.Context(), trace.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("the open row was pruned: the evidence that a run died is what "+
			"the sweep exists to read, and this removed it first (rows = %v)", rows)
	}
}

// The mark is in the same database as the rows, so a second Atenea starting a
// moment later does not walk the whole history again.
func TestASecondPassWithinTheIntervalDoesNothing(t *testing.T) {
	store := store(t)
	now := time.Now()
	closed(t, store, "old", now.Add(-100*24*time.Hour))

	if removed, err := store.PruneIfDue(t.Context(), now, 90*24*time.Hour, 24*time.Hour); err != nil || removed != 1 {
		t.Fatalf("first pass: removed = %d, err = %v", removed, err)
	}
	closed(t, store, "older", now.Add(-120*24*time.Hour))

	removed, err := store.PruneIfDue(t.Context(), now.Add(time.Hour), 90*24*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d an hour after a pass whose interval is a day", removed)
	}

	// And a day later it is due again.
	removed, err = store.PruneIfDue(t.Context(), now.Add(25*time.Hour), 90*24*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d once the interval had passed, want the row left behind", removed)
	}
}

// Keeping everything is a legitimate policy for a state root managed
// elsewhere, and it must be sayable rather than implied by an absent block.
func TestAZeroWindowKeepsEverything(t *testing.T) {
	store := store(t)
	now := time.Now()
	closed(t, store, "ancient", now.Add(-10*365*24*time.Hour))

	removed, err := store.PruneIfDue(t.Context(), now, 0, 24*time.Hour)
	if err != nil {
		t.Fatalf("PruneIfDue: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d with retention off", removed)
	}
}
