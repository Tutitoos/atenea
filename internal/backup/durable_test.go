package backup

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// An fsync leaves no trace a test can look for afterwards, so the call itself
// is what gets observed: the directories a snapshot made durable, in the order
// it made them.
//
// The bug this covers is what that list used to contain. copyFile syncs the
// bytes of every file it writes, but a file's bytes being durable says nothing
// about the directory entry naming it, and a hard-linked file is a directory
// entry and nothing else -- so on a tree that shared most of its files with
// the previous snapshot, almost nothing was on the disk. The only directory
// synced was the snapshot folder, after the rename, which made the new
// snapshot's name durable while its contents were still a promise.
func TestEveryDirectoryOfASnapshotIsMadeDurableBeforeItIsPublished(t *testing.T) {
	source := t.TempDir()
	for _, dir := range []string{"receipts", filepath.Join("receipts", "2026"), "notebook"} {
		if err := os.MkdirAll(filepath.Join(source, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{
		"settings.toml",
		filepath.Join("receipts", "2026", "run-1.json"),
		filepath.Join("notebook", "incidents.jsonl"),
	} {
		if err := os.WriteFile(filepath.Join(source, file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	extra := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(extra, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	var synced []string
	// `real` shadowed a predeclared identifier; `wrapped` says the same thing
	// and does not.
	wrapped := syncDir
	syncDir = func(path string) error {
		synced = append(synced, path)
		return wrapped(path)
	}
	t.Cleanup(func() { syncDir = wrapped })

	s, err := New(Options{
		Source: source,
		Dir:    dir,
		Keep:   2,
		Extras: []Extra{{Source: extra, Dest: filepath.Join("config", "config.toml")}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	snapshot, err := s.Snapshot(context.Background(), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Every directory is named by where it ended up, which is under the
	// partial tree: the syncs all happen before the rename that publishes it.
	partial := filepath.Join(dir, partialPrefix+snapshot.Name+partialSuffix)
	for _, want := range []string{
		partial,
		filepath.Join(partial, "receipts"),
		filepath.Join(partial, "receipts", "2026"),
		filepath.Join(partial, "notebook"),
		filepath.Join(partial, "config"),
	} {
		if !slices.Contains(synced, want) {
			t.Errorf("%s was never synced: its entries are a promise the page cache is making", want)
		}
	}
	// The folder of snapshots itself, after the rename, so the published name
	// survives too.
	if len(synced) == 0 || synced[len(synced)-1] != dir {
		t.Errorf("the last sync was %v, want the snapshot folder after the rename", synced[len(synced)-1:])
	}

	// Children before parents. A parent made durable while a directory inside
	// it is not yet durable is a parent naming something that will not be
	// there, which is the same hole one level up.
	last := make(map[string]int, len(synced))
	for i, path := range synced {
		last[path] = i
	}
	for path, child := range last {
		if parent, ok := last[filepath.Dir(path)]; ok && parent < child {
			t.Errorf("%s was last synced before the %s inside it",
				filepath.Dir(path), path)
		}
	}
}
