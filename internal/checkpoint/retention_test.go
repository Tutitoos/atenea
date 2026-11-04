package checkpoint_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The mark is what lets a pass that is not due cost one file read: a second
// call inside the interval removes nothing even when a receipt has since aged
// out, and a call after it picks that receipt up.
func TestRetentionRunsOncePerInterval(t *testing.T) {
	store := pruneStore(t)
	now := time.Now()
	keep, every := 90*24*time.Hour, 24*time.Hour

	save(t, store, "20260101T000000-old000", true, now.Add(-100*24*time.Hour))
	if removed, err := store.PruneIfDue(now, keep, every); err != nil || removed != 1 {
		t.Fatalf("first pass: removed = %d, err = %v; want 1", removed, err)
	}

	save(t, store, "20260102T000000-old001", true, now.Add(-100*24*time.Hour))
	if removed, err := store.PruneIfDue(now.Add(time.Hour), keep, every); err != nil || removed != 0 {
		t.Fatalf("pass inside the interval: removed = %d, err = %v; want the mark to skip it", removed, err)
	}
	if removed, err := store.PruneIfDue(now.Add(every+time.Hour), keep, every); err != nil || removed != 1 {
		t.Fatalf("pass after the interval: removed = %d, err = %v; want 1", removed, err)
	}
}

// The mark is not a run: it must not show up as one, and an unreadable mark
// must not switch retention off.
func TestRetentionMarkIsNotARunAndACorruptOneIsDue(t *testing.T) {
	store := pruneStore(t)
	now := time.Now()
	save(t, store, "20260101T000000-old000", true, now.Add(-100*24*time.Hour))
	if _, err := store.PruneIfDue(now, 90*24*time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	ids, err := store.List()
	if err != nil || len(ids) != 0 {
		t.Fatalf("ids = %v, err = %v; the mark was listed as a run", ids, err)
	}

	if err := os.WriteFile(filepath.Join(store.Dir(), "retention.mark"), []byte("not a time"), 0o600); err != nil {
		t.Fatal(err)
	}
	save(t, store, "20260102T000000-old001", true, now.Add(-100*24*time.Hour))
	if removed, err := store.PruneIfDue(now, 90*24*time.Hour, time.Hour); err != nil || removed != 1 {
		t.Fatalf("corrupt mark: removed = %d, err = %v; want it treated as due", removed, err)
	}
}

// Keep of zero is the operator saying to forget nothing, and a store that was
// never written has nothing old in it.
func TestRetentionOffOrEmptyDoesNothing(t *testing.T) {
	store := pruneStore(t)
	save(t, store, "20260101T000000-old000", true, time.Now().Add(-1000*24*time.Hour))
	if removed, err := store.PruneIfDue(time.Now(), 0, time.Hour); err != nil || removed != 0 {
		t.Fatalf("keep 0: removed = %d, err = %v", removed, err)
	}
	if _, err := store.PruneIfDue(time.Now(), time.Hour, 0); err == nil {
		t.Fatal("an interval of zero was accepted")
	}
}
