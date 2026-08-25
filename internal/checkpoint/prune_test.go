package checkpoint_test

import (
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
)

func pruneStore(t *testing.T) *checkpoint.Store {
	t.Helper()
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store
}

func save(t *testing.T, store *checkpoint.Store, id string, closed bool, updated time.Time) {
	t.Helper()
	if err := store.Save(checkpoint.Run{
		ID: id, Kind: checkpoint.KindTask, Task: "find every TODO",
		Started: updated, Updated: updated, Closed: closed, Verdict: "ok",
	}); err != nil {
		t.Fatalf("Save %s: %v", id, err)
	}
}

// A receipt is the only record that a commission happened, and it carries the
// sentence somebody typed. Keeping them forever was not a decision, it was the
// absence of one.
func TestClosedReceiptsOlderThanTheCutoffAreRemoved(t *testing.T) {
	store := pruneStore(t)
	now := time.Now()

	save(t, store, "20260101T000000-old000", true, now.Add(-100*24*time.Hour))
	save(t, store, "20260801T000000-recent", true, now.Add(-2*24*time.Hour))

	removed, err := store.Prune(now.Add(-90 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want the one past the cutoff", removed)
	}
	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 1 || ids[0] != "20260801T000000-recent" {
		t.Errorf("ids = %v, want only the recent receipt", ids)
	}
}

// An open receipt is a commission somebody may still resume. Removing one by
// age would take away the resume rather than the record of it, and Candidates
// is built out of exactly these.
func TestAnOpenReceiptSurvivesTheCutoff(t *testing.T) {
	store := pruneStore(t)
	now := time.Now()

	save(t, store, "20250101T000000-open00", false, now.Add(-400*24*time.Hour))

	removed, err := store.Prune(now.Add(-90 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 0 {
		t.Fatal("an open receipt was pruned: that is a commission somebody could " +
			"still have resumed")
	}
}

// The age comes from the run's own Updated, not from the file's mtime. A
// restore, a backup or an editor touches the file and says nothing about when
// the run happened.
func TestAgeIsReadFromTheRunAndNotFromTheFile(t *testing.T) {
	store := pruneStore(t)
	now := time.Now()

	// Written now, so its mtime is now; the run says it closed long ago.
	save(t, store, "20250101T000000-stale0", true, now.Add(-400*24*time.Hour))

	removed, err := store.Prune(now.Add(-90 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Error("a receipt written today but closed a year ago survived: the age " +
			"is being read from the file rather than from the run")
	}
}

// A store with no directory is a core running with checkpoints off, and it
// must not turn a disabled feature into an error on a rhythm.
func TestPruningADisabledStoreIsQuiet(t *testing.T) {
	store, err := checkpoint.New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	removed, err := store.Prune(time.Now())
	if err != nil || removed != 0 {
		t.Errorf("removed = %d, err = %v; want a quiet no-op", removed, err)
	}
}
