package backup_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/backup"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// base is a fixed instant so snapshot names are the same on every run, and so
// the intervals in these tests are the six hours the design asks for rather
// than whatever the machine took to get here.
var base = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

// partialSuffix is what the package brackets a tree it is still writing with.
// Named here rather than reached for, so a test can stage the wreckage a
// killed run leaves behind.
const partialSuffix = ".partial"

// Sharing is the whole design: complete trees that between them hold one copy
// of every unchanged file. Equal bytes would also be true of a plain copy, and
// a plain copy is the bug, so the claim is one inode named twice.
func TestAnUnchangedFileIsSharedWithThePreviousSnapshot(t *testing.T) {
	store, source := newStore(t, 5)
	writeFile(t, filepath.Join(source, "notes", "keep.txt"), "never touched again")

	first := mustSnapshot(t, store, base)
	second := mustSnapshot(t, store, base.Add(6*time.Hour))

	older := statFile(t, filepath.Join(first.Path, "notes", "keep.txt"))
	newer := statFile(t, filepath.Join(second.Path, "notes", "keep.txt"))
	if !os.SameFile(older, newer) {
		t.Fatalf("%s and %s hold separate copies of a file nobody edited", first.Name, second.Name)
	}
	if second.Bytes != 0 {
		t.Errorf("a run with nothing to write wrote %d bytes", second.Bytes)
	}
}

// A snapshot that follows the source is not a snapshot.
func TestAChangedFileIsCopiedAndTheOldSnapshotKeepsTheOldBytes(t *testing.T) {
	store, source := newStore(t, 5)
	file := filepath.Join(source, "state.db")
	writeFile(t, file, "before the change")

	first := mustSnapshot(t, store, base)
	writeFile(t, file, "after")
	second := mustSnapshot(t, store, base.Add(6*time.Hour))

	if got := readFile(t, filepath.Join(first.Path, "state.db")); got != "before the change" {
		t.Errorf("the older snapshot now reads %q, want the bytes it was taken from", got)
	}
	if got := readFile(t, filepath.Join(second.Path, "state.db")); got != "after" {
		t.Errorf("the newer snapshot reads %q, want the edited bytes", got)
	}
	if second.Copied != 1 || second.Linked != 0 {
		t.Errorf("the edited file was booked as %d copied and %d shared, want 1 and 0",
			second.Copied, second.Linked)
	}
}

// The dangerous edit is the one that leaves the size alone: a record rewritten
// in place, a counter going from 41 to 42. Size on its own would call that
// file unchanged and the snapshot would hold the bytes before the edit while
// claiming to hold the ones after.
func TestAFileRewrittenToTheSameLengthIsNotMistakenForUnchanged(t *testing.T) {
	store, source := newStore(t, 5)
	file := filepath.Join(source, "counter")
	writeFile(t, file, "41")

	first := mustSnapshot(t, store, base)
	writeFile(t, file, "42")
	second := mustSnapshot(t, store, base.Add(6*time.Hour))

	if second.Linked != 0 {
		t.Errorf("the rewritten file was shared with the older snapshot")
	}
	if got := readFile(t, filepath.Join(second.Path, "counter")); got != "42" {
		t.Errorf("the newer snapshot reads %q, want the rewritten bytes", got)
	}
	if got := readFile(t, filepath.Join(first.Path, "counter")); got != "41" {
		t.Errorf("the older snapshot reads %q, want the bytes it was taken from", got)
	}
}

// Stopping at the link would make the snapshot a pointer to the live tree
// instead of a copy of it, which reads as a backup right up to the moment
// somebody needs one.
func TestASourceThatIsItselfALinkIsFollowed(t *testing.T) {
	home := t.TempDir()
	actual := filepath.Join(home, "elsewhere")
	writeFile(t, filepath.Join(actual, "state.db"), "rows")
	source := filepath.Join(home, "state")
	if err := os.Symlink(actual, source); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	store, err := backup.New(backup.Options{
		Source: source,
		Dir:    filepath.Join(home, "backups"),
		Keep:   5,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	snapshot := mustSnapshot(t, store, base)

	info, err := os.Lstat(snapshot.Path)
	if err != nil {
		t.Fatalf("lstat %s: %v", snapshot.Path, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("the snapshot is a link to the tree it was meant to copy")
	}
	if got := readFile(t, filepath.Join(snapshot.Path, "state.db")); got != "rows" {
		t.Errorf("the snapshot holds %q, want the source's contents", got)
	}
}

func TestRotationKeepsTheNewestAndDropsTheOldest(t *testing.T) {
	store, source := newStore(t, 3)
	writeFile(t, filepath.Join(source, "state.db"), "rows")

	taken := make([]backup.Snapshot, 0, 5)
	for i := range 5 {
		taken = append(taken, mustSnapshot(t, store, base.Add(time.Duration(i)*6*time.Hour)))
	}

	listed, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := make([]string, len(listed))
	for i, snapshot := range listed {
		got[i] = snapshot.Name
	}
	want := []string{taken[4].Name, taken[3].Name, taken[2].Name}
	if !slices.Equal(got, want) {
		t.Errorf("kept %v, want the three newest %v", got, want)
	}
	for _, dropped := range taken[:2] {
		if _, err := os.Stat(dropped.Path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still on disk after rotation: %v", dropped.Name, err)
		}
	}
}

// The property that buys hardlinks over a delta chain. Every byte in these two
// trees was written into the one rotation has just deleted; a chain would have
// taken them with it.
func TestASurvivingSnapshotIsStillCompleteAfterTheOldestIsDropped(t *testing.T) {
	store, source := newStore(t, 2)
	want := map[string]string{
		"alpha.txt":     "the first file",
		"deep/beta.txt": "the second file",
	}
	for name, body := range want {
		writeFile(t, filepath.Join(source, filepath.FromSlash(name)), body)
	}

	first := mustSnapshot(t, store, base)
	second := mustSnapshot(t, store, base.Add(6*time.Hour))
	third := mustSnapshot(t, store, base.Add(12*time.Hour))

	// Without this the test would pass on an implementation that copies
	// everything every time, which proves nothing about sharing.
	if second.Linked != len(want) || third.Linked != len(want) {
		t.Fatalf("the survivors shared %d and %d files, want %d each",
			second.Linked, third.Linked, len(want))
	}
	if _, err := os.Stat(first.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the oldest snapshot %s was not dropped: %v", first.Name, err)
	}
	for _, survivor := range []backup.Snapshot{second, third} {
		for name, body := range want {
			got := readFile(t, filepath.Join(survivor.Path, filepath.FromSlash(name)))
			if got != body {
				t.Errorf("%s holds %q for %s, want %q", survivor.Name, got, name, body)
			}
		}
	}
}

// A six-hour timer held in memory on a machine that reboots every four hours
// means the backup never happens once, silently. So every decision below is
// taken by a Store that has just been built and knows nothing but the folder.
func TestTheRhythmIsReadFromDiskSoARestartCannotSkipABackup(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "state")
	dir := filepath.Join(home, "backups")
	writeFile(t, filepath.Join(source, "state.db"), "rows")

	const every = 6 * time.Hour
	restart := func() *backup.Store {
		t.Helper()
		store, err := backup.New(backup.Options{Source: source, Dir: dir, Keep: 5})
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		return store
	}

	if _, taken, err := restart().SnapshotIfDue(context.Background(), base, every); err != nil || !taken {
		t.Fatalf("nothing on disk yet: taken = %v, err = %v, want a backup", taken, err)
	}
	if _, taken, err := restart().SnapshotIfDue(context.Background(), base.Add(4*time.Hour), every); err != nil || taken {
		t.Fatalf("four hours in: taken = %v, err = %v, want the fresh snapshot to be enough", taken, err)
	}
	if _, taken, err := restart().SnapshotIfDue(context.Background(), base.Add(7*time.Hour), every); err != nil || !taken {
		t.Fatalf("seven hours in: taken = %v, err = %v, want a backup", taken, err)
	}

	listed, err := restart().List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Errorf("the folder holds %d snapshots, want the 2 that were due", len(listed))
	}
}

// What a killed run leaves behind is a tree that is missing whatever it had
// not reached yet. It must not count as a backup at any point.
func TestAnInterruptedRunIsNotASnapshotAndIsSweptAway(t *testing.T) {
	store, source := newStore(t, 5)
	writeFile(t, filepath.Join(source, "state.db"), "rows")

	finished := mustSnapshot(t, store, base)

	// A finished tree renamed back to the name a run that died would have left
	// it under, five hours newer than the only real snapshot.
	interrupted := mustSnapshot(t, store, base.Add(5*time.Hour))
	stale := filepath.Join(store.Dir(), "."+interrupted.Name+partialSuffix)
	if err := os.Rename(interrupted.Path, stale); err != nil {
		t.Fatalf("staging the interrupted run: %v", err)
	}

	listed, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 || listed[0].Name != finished.Name {
		t.Fatalf("listed %v, want only %s", listed, finished.Name)
	}

	// Six hours after the real snapshot and one after the half-written one. A
	// partial counted as the newest would postpone this indefinitely, one
	// killed run at a time.
	_, taken, err := store.SnapshotIfDue(context.Background(), base.Add(6*time.Hour), 6*time.Hour)
	if err != nil {
		t.Fatalf("snapshot if due: %v", err)
	}
	if !taken {
		t.Error("the half-written tree postponed a backup that was due")
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the half-written tree was not swept: %v", err)
	}
}

// A copy inside the tree it copies recurses into itself and dies with the
// thing it exists to survive.
func TestNewRefusesAFolderInsideTheSource(t *testing.T) {
	source := t.TempDir()
	_, err := backup.New(backup.Options{
		Source: source,
		Dir:    filepath.Join(source, "backups"),
		Keep:   5,
	})
	if err == nil {
		t.Fatal("a backup folder inside the source was accepted")
	}
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want invalid input", got)
	}
}

func TestAStoreThatWouldKeepNothingIsRefused(t *testing.T) {
	home := t.TempDir()
	for _, keep := range []int{0, -1} {
		_, err := backup.New(backup.Options{
			Source: filepath.Join(home, "state"),
			Dir:    filepath.Join(home, "backups"),
			Keep:   keep,
		})
		if err == nil {
			t.Fatalf("keep = %d was accepted; rotation would delete the snapshot it just took", keep)
		}
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("keep = %d: kind = %v, want invalid input", keep, got)
		}
	}
}

// A store with a blank path in it would copy the working directory, or copy
// into it, depending on which half was left out.
func TestAStoreWithNoSourceOrNoFolderIsRefused(t *testing.T) {
	home := t.TempDir()
	for what, opts := range map[string]backup.Options{
		"no source": {Dir: filepath.Join(home, "backups"), Keep: 5},
		"no folder": {Source: filepath.Join(home, "state"), Keep: 5},
	} {
		_, err := backup.New(opts)
		if err == nil {
			t.Errorf("%s was accepted", what)
			continue
		}
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("%s: kind = %v, want invalid input", what, got)
		}
	}
}

// An interval of zero is due at every tick, which is a backup running against
// itself for as long as the machine is up.
func TestAnIntervalThatIsNotAnIntervalIsRefused(t *testing.T) {
	store, _ := newStore(t, 5)
	for _, every := range []time.Duration{0, -time.Hour} {
		_, taken, err := store.SnapshotIfDue(context.Background(), base, every)
		if err == nil {
			t.Errorf("an interval of %s was accepted, taken = %v", every, taken)
			continue
		}
		if got := contract.KindOf(err); got != contract.FailureInvalidInput {
			t.Errorf("interval %s: kind = %v, want invalid input", every, got)
		}
	}
}

// A restore hands back what was taken. A credentials file that comes back
// readable by everybody is a copy of the bytes, not of the file.
func TestACopyKeepsThePermissionsOfWhatItCopied(t *testing.T) {
	store, source := newStore(t, 5)
	want := map[string]fs.FileMode{"credentials": 0o600, "public.txt": 0o644}
	for name, mode := range want {
		path := filepath.Join(source, name)
		writeFile(t, path, "whatever is in "+name)
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}
	}

	snapshot := mustSnapshot(t, store, base)

	for name, mode := range want {
		if got := statFile(t, filepath.Join(snapshot.Path, name)).Mode().Perm(); got != mode {
			t.Errorf("%s in the snapshot is %v, want the source's %v", name, got, mode)
		}
	}
}

func TestEveryFileInTheTreeIsEitherSharedOrCopied(t *testing.T) {
	store, source := newStore(t, 5)
	writeFile(t, filepath.Join(source, "one.txt"), "one")
	writeFile(t, filepath.Join(source, "deep", "two.txt"), "two")
	writeFile(t, filepath.Join(source, "deep", "three.txt"), "three")
	if err := os.Symlink("one.txt", filepath.Join(source, "alias")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	first := mustSnapshot(t, store, base)
	writeFile(t, filepath.Join(source, "deep", "two.txt"), "two, edited")
	second := mustSnapshot(t, store, base.Add(6*time.Hour))

	for _, snapshot := range []backup.Snapshot{first, second} {
		if snapshot.Linked+snapshot.Copied != snapshot.Files {
			t.Errorf("%s: %d shared + %d copied is not the %d files reported",
				snapshot.Name, snapshot.Linked, snapshot.Copied, snapshot.Files)
		}
		if got := countEntries(t, snapshot.Path); got != snapshot.Files {
			t.Errorf("%s: reported %d files, the tree holds %d", snapshot.Name, snapshot.Files, got)
		}
	}
	if second.Linked != 2 {
		t.Errorf("the second run shared %d files, want the 2 that did not change", second.Linked)
	}
}

// An empty tree is a real snapshot: it becomes the newest, it fills a slot,
// and five of them in a row would rotate away every copy that held something.
func TestAMissingSourceTakesNoSnapshotRatherThanAnEmptyOne(t *testing.T) {
	home := t.TempDir()
	store, err := backup.New(backup.Options{
		Source: filepath.Join(home, "never-commissioned"),
		Dir:    filepath.Join(home, "backups"),
		Keep:   5,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	snapshot, taken, err := store.SnapshotIfDue(context.Background(), base, 6*time.Hour)
	if err != nil {
		t.Fatalf("a machine with nothing to protect is not a failure: %v", err)
	}
	if taken {
		t.Errorf("a machine with nothing to protect reported the backup %q", snapshot.Name)
	}
	listed, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("the folder holds %d snapshots of a source that does not exist", len(listed))
	}
}

// Following a link would pull whatever it names into the snapshot, and one
// pointing back up the tree would pull the tree in twice.
func TestASymlinkIsRecreatedRatherThanFollowed(t *testing.T) {
	store, source := newStore(t, 5)
	writeFile(t, filepath.Join(source, "real.txt"), "the file itself")
	if err := os.Symlink("real.txt", filepath.Join(source, "alias")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	snapshot := mustSnapshot(t, store, base)

	copied := filepath.Join(snapshot.Path, "alias")
	info, err := os.Lstat(copied)
	if err != nil {
		t.Fatalf("lstat %s: %v", copied, err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("the link was followed and stored as a %v", info.Mode())
	}
	target, err := os.Readlink(copied)
	if err != nil {
		t.Fatalf("readlink %s: %v", copied, err)
	}
	if target != "real.txt" {
		t.Errorf("the copy points at %q, want %q", target, "real.txt")
	}
}

// A directory recreated with its real permissions before its contents are
// written would refuse to take them.
func TestADirectoryNobodyCanWriteIsStillCopied(t *testing.T) {
	store, source := newStore(t, 5)
	locked := filepath.Join(source, "locked")
	writeFile(t, filepath.Join(locked, "inside.txt"), "kept anyway")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Both copies have to be writable again or the temporary directory cannot
	// be torn down, which would fail the test for the wrong reason.
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	snapshot := mustSnapshot(t, store, base)
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(snapshot.Path, "locked"), 0o700) })

	if got := readFile(t, filepath.Join(snapshot.Path, "locked", "inside.txt")); got != "kept anyway" {
		t.Errorf("the snapshot holds %q, want the file inside the locked directory", got)
	}
	info := statFile(t, filepath.Join(snapshot.Path, "locked"))
	if got := info.Mode().Perm(); got != 0o500 {
		t.Errorf("the copied directory is %v, want the source's %v", got, fs.FileMode(0o500))
	}
}

// The tree is built under a name of its own and renamed in one step at the
// end, so a run that dies has nowhere to write except its own wreckage. Here
// it dies while carrying the name of a snapshot that already exists, which is
// what a run in place would have opened and emptied first.
func TestARunThatFailsLeavesTheFolderAsItWas(t *testing.T) {
	store, source := newStore(t, 5)
	writeFile(t, filepath.Join(source, "state.db"), "rows")
	kept := mustSnapshot(t, store, base)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Snapshot(ctx, base); err == nil {
		t.Fatal("a canceled copy reported a snapshot")
	} else if got := contract.KindOf(err); got != contract.FailureTimeout {
		t.Errorf("kind = %v, want timeout", got)
	}

	if got := readFile(t, filepath.Join(kept.Path, "state.db")); got != "rows" {
		t.Errorf("the snapshot that was already there now reads %q, want %q", got, "rows")
	}
	entries, err := os.ReadDir(store.Dir())
	if err != nil {
		t.Fatalf("read %s: %v", store.Dir(), err)
	}
	if len(entries) != 1 || entries[0].Name() != kept.Name {
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Errorf("the folder holds %v, want only %s", names, kept.Name)
	}
}

// A backup that quietly leaves out what it could not read is worse than one
// that failed, because it looks complete and nobody finds out until they need
// the file that is not in it.
func TestAFileThatCannotBeReadStopsTheRun(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a file with no permission bits, so there is nothing to refuse")
	}
	store, source := newStore(t, 5)
	writeFile(t, filepath.Join(source, "readable.txt"), "fine")
	sealed := filepath.Join(source, "sealed.txt")
	writeFile(t, sealed, "unreachable")
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, err := store.Snapshot(context.Background(), base); err == nil {
		t.Fatal("the run reported a snapshot that is missing a file")
	}
	listed, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("the folder holds %d snapshots, want none: %v", len(listed), listed)
	}
}

func TestAnExtraFileIsIncludedInEachSnapshot(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "state")
	writeFile(t, filepath.Join(source, "state.db"), "rows")
	extra := filepath.Join(home, "config", "atenea.toml")
	writeFile(t, extra, "version = 1")

	store, err := backup.New(backup.Options{
		Source: source,
		Dir:    filepath.Join(home, "backups"),
		Keep:   5,
		Extras: []backup.Extra{{Source: extra, Dest: "config/atenea.toml"}},
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	first := mustSnapshot(t, store, base)
	if got := readFile(t, filepath.Join(first.Path, "config", "atenea.toml")); got != "version = 1" {
		t.Errorf("first snapshot holds %q for the extra, want the original content", got)
	}

	second := mustSnapshot(t, store, base.Add(6*time.Hour))
	if got := readFile(t, filepath.Join(second.Path, "config", "atenea.toml")); got != "version = 1" {
		t.Errorf("second snapshot holds %q for the extra, want the original content", got)
	}
	older := statFile(t, filepath.Join(first.Path, "config", "atenea.toml"))
	newer := statFile(t, filepath.Join(second.Path, "config", "atenea.toml"))
	if !os.SameFile(older, newer) {
		t.Error("the unchanged extra was copied instead of shared with the previous snapshot")
	}

	writeFile(t, extra, "version = 2")
	third := mustSnapshot(t, store, base.Add(12*time.Hour))
	if got := readFile(t, filepath.Join(third.Path, "config", "atenea.toml")); got != "version = 2" {
		t.Errorf("third snapshot holds %q for the edited extra, want the new content", got)
	}
	if os.SameFile(
		statFile(t, filepath.Join(second.Path, "config", "atenea.toml")),
		statFile(t, filepath.Join(third.Path, "config", "atenea.toml")),
	) {
		t.Error("the edited extra was shared instead of copied fresh")
	}
}

func TestAnExtraFileThatDoesNotExistIsSkipped(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "state")
	writeFile(t, filepath.Join(source, "state.db"), "rows")

	store, err := backup.New(backup.Options{
		Source: source,
		Dir:    filepath.Join(home, "backups"),
		Keep:   5,
		Extras: []backup.Extra{{Source: filepath.Join(home, "config", "never.toml"), Dest: "config/atenea.toml"}},
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	snapshot, err := store.Snapshot(context.Background(), base)
	if err != nil {
		t.Fatalf("a missing extra must not fail the run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshot.Path, "config", "atenea.toml")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a missing extra must not appear in the snapshot")
	}
}

func TestAnExtraFileCountsInTheFileTally(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "state")
	writeFile(t, filepath.Join(source, "state.db"), "rows")
	extra := filepath.Join(home, "config", "atenea.toml")
	writeFile(t, extra, "version = 1")

	store, err := backup.New(backup.Options{
		Source: source,
		Dir:    filepath.Join(home, "backups"),
		Keep:   5,
		Extras: []backup.Extra{{Source: extra, Dest: "config/atenea.toml"}},
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	first := mustSnapshot(t, store, base)
	second := mustSnapshot(t, store, base.Add(6*time.Hour))

	for _, snapshot := range []backup.Snapshot{first, second} {
		if snapshot.Linked+snapshot.Copied != snapshot.Files {
			t.Errorf("%s: %d shared + %d copied != %d files reported",
				snapshot.Name, snapshot.Linked, snapshot.Copied, snapshot.Files)
		}
		if got := countEntries(t, snapshot.Path); got != snapshot.Files {
			t.Errorf("%s: reported %d files, tree holds %d", snapshot.Name, snapshot.Files, got)
		}
	}
}

func newStore(t *testing.T, keep int) (*backup.Store, string) {
	t.Helper()
	home := t.TempDir()
	source := filepath.Join(home, "state")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", source, err)
	}
	store, err := backup.New(backup.Options{
		Source: source,
		Dir:    filepath.Join(home, "backups"),
		Keep:   keep,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store, source
}

func mustSnapshot(t *testing.T, store *backup.Store, at time.Time) backup.Snapshot {
	t.Helper()
	snapshot, err := store.Snapshot(context.Background(), at)
	if err != nil {
		t.Fatalf("snapshot at %s: %v", at.Format(time.RFC3339), err)
	}
	if snapshot.Name == "" {
		t.Fatalf("no snapshot was taken at %s", at.Format(time.RFC3339))
	}
	return snapshot
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func statFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

// countEntries counts everything in a tree that is not a directory, links
// included, which is what Files claims to be.
func countEntries(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return count
}
