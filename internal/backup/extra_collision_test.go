package backup

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtraCollisionAfterRestorePreservesOlderSnapshot(t *testing.T) {
	source, dir, external := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "atenea.toml")
	relative := "config/atenea.toml"
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	write := func(path, body string, at time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(source, "state.txt"), "state", stamp)
	write(external, "old", stamp)
	store, err := New(Options{Source: source, Dir: dir, Keep: 3, Extras: []Extra{{Source: external, Dest: relative}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Snapshot(t.Context(), stamp.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(first.Path, relative)
	firstBody, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	firstHash := sha256.Sum256(firstBody)
	if _, err := store.RestoreInPlace(t.Context(), first.Name, source); err != nil {
		t.Fatal(err)
	}
	write(external, "new", stamp.Add(time.Minute))
	second, err := store.Snapshot(t.Context(), stamp.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "old" || sha256.Sum256(body) != firstHash {
		t.Fatalf("second snapshot rewrote first snapshot: got %q, want old", body)
	}
	if got, err := os.ReadFile(filepath.Join(second.Path, relative)); err != nil || string(got) != "new" {
		t.Fatalf("new snapshot config = %q, %v; want new", got, err)
	}
	if second.Files != 2 || second.Files != second.Copied+second.Linked {
		t.Fatalf("overridden path was counted twice: %+v", second)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := store.Restore(t.Context(), second.Name, restored); err != nil {
		t.Fatalf("new snapshot cannot be restored: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(restored, relative)); err != nil || string(got) != "new" {
		t.Fatalf("restored config = %q, %v; want new", got, err)
	}
	third, err := store.Snapshot(t.Context(), stamp.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(firstPath); err != nil || sha256.Sum256(got) != firstHash {
		t.Fatalf("later unchanged extra modified first snapshot: %q, %v", got, err)
	}
	if third.Files != 2 || third.Files != third.Copied+third.Linked {
		t.Fatalf("unchanged overridden path was counted twice: %+v", third)
	}
	firstInfo, err := os.Stat(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	thirdInfo, err := os.Stat(filepath.Join(third.Path, relative))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(firstInfo, thirdInfo) {
		t.Fatal("changed extra still shares the first snapshot's inode")
	}
	if err := os.Remove(external); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Snapshot(ctx, stamp.Add(4*time.Hour)); err == nil {
		t.Fatal("canceled snapshot unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, stamp.Add(4*time.Hour).Format(nameLayout))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("canceled snapshot was published: %v", err)
	}
	if got, err := os.ReadFile(firstPath); err != nil || sha256.Sum256(got) != firstHash {
		t.Fatalf("canceled snapshot modified first snapshot: %q, %v", got, err)
	}
}

func TestExtraParentSymlinkCannotWriteOutsideSnapshot(t *testing.T) {
	source, dir, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(source, "config")); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "atenea.toml")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(extra, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := New(Options{Source: source, Dir: dir, Keep: 2,
		Extras: []Extra{{Source: extra, Dest: "config/atenea.toml"}}})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, err := store.Snapshot(t.Context(), time.Now()); err == nil || snapshot.Name != "" {
		t.Fatalf("symlinked extra parent published a snapshot: %+v, %v", snapshot, err)
	}
	if body, err := os.ReadFile(sentinel); err != nil || string(body) != "untouched" {
		t.Fatalf("extra changed file outside snapshot: %q, %v", body, err)
	}
	if snapshots, err := store.List(); err != nil || len(snapshots) != 0 {
		t.Fatalf("failed snapshot was published: %+v, %v", snapshots, err)
	}
}
