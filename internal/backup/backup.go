// Package backup keeps a rotating set of complete copies of Atenea's state.
//
// The whole shape follows from one sentence in the design: five copies in
// rotation, and the sixth pushes the oldest out. That is what rules out a
// delta chain. A chain keeps one full tree and stores the others as the
// differences between them, so deleting the oldest tears the floor out from
// under every copy stacked on it. Rotation and chains cannot both be had.
//
// So a snapshot here is a whole directory tree, and "incremental" happens one
// level down, in the filesystem: a file that has not been touched since the
// previous snapshot is hard-linked rather than copied. Hardlinks are
// refcounted, so the copies share one instance of every unchanged file -- only
// what changed costs bytes -- while each tree stays independently complete.
// Dropping the oldest drops references; the bytes go only when the last tree
// naming them goes.
//
// Nothing here shells out. The machine this has to protect is a bare Debian
// with no rsync, no borg and possibly no cron, so Atenea backs itself up with
// what the standard library gives it, wherever it happens to be running.
//
// # Live databases
//
// SQLite and DuckDB files are copied through their engines into self-contained
// snapshots. Their mutable journals are excluded and databases are never
// shared using hard links based on main-file modification time. Caller checkpointing still
// flushes buffered measurements, but is not the consistency boundary. Each
// database has its own consistent point; the directory is not a distributed
// transaction across databases. Publication and rotation happen only after all
// copies and durability checks succeed.
package backup

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// syncDir is the directory fsync, reached through a variable so a test can see
// which directories a snapshot actually made durable. An fsync has no other
// observable effect: a backup that quietly stopped calling it looks exactly
// like one that never stopped, right up until the power cut that the call was
// there for.
var syncDir = syncDirectory

// nameLayout is what a snapshot is called: the instant it was taken, in UTC,
// in a form that sorts lexicographically by time. The name is the only record
// of when a tree was made -- there is no index file that could fall out of
// step with the folder.
const nameLayout = "20060102T150405Z"

// partialPrefix and partialSuffix bracket a tree still being written. The name
// cannot parse as a timestamp, which is what keeps a half-written tree out of
// List for free.
const (
	partialPrefix = "."
	partialSuffix = ".partial"
)

// Extra is a single file from outside the source tree that every snapshot
// includes alongside the state it copies. It follows the same hardlink
// optimisation as the main tree: a file that has not changed since the
// previous snapshot costs no bytes.
type Extra struct {
	// Source is the absolute path to the file to protect.
	Source string
	// Dest is where it lands inside the snapshot, relative to the snapshot
	// root. Parent directories are created as needed.
	Dest string
}

// Options says what to copy, where to put it, and how many copies survive.
type Options struct {
	// Source is the tree to protect. It does not have to exist yet.
	Source string
	// Dir is the folder of snapshots. It must not sit inside Source.
	Dir string
	// Keep is how many snapshots a rotation leaves behind. At least one.
	Keep int
	// Extras are individual files from outside the source tree to include in
	// every snapshot. A source that does not exist at snapshot time is skipped
	// silently: a file that has not been commissioned yet must not break a run.
	Extras []Extra
}

// Snapshot is one complete copy of the source, and what making it cost.
type Snapshot struct {
	// Name is the instant the copy was taken, formatted as nameLayout.
	Name string
	At   time.Time
	Path string

	// Files is everything in the finished tree, and is always Linked + Copied.
	Files int
	// Linked was shared with the previous snapshot and cost no bytes.
	Linked int
	// Copied was written fresh.
	Copied int
	// Bytes is what actually reached the disk.
	Bytes int64
}

// Store is a folder of snapshots.
//
// Its methods are not safe to call concurrently: two runs at once would sweep
// each other's half-written trees. Atenea runs maintenance in a single lane
// (internal/clock), which is where that is enforced.
type Store struct {
	source string
	dir    string
	keep   int
	extras []Extra
}

// New checks the arrangement makes sense and returns the store.
//
// The folder is not created here. A machine that never takes a snapshot should
// not be left with an empty directory it did not ask for.
func New(opts Options) (*Store, error) {
	if opts.Source == "" {
		return nil, contract.Fail(contract.FailureInvalidInput, "backup: no source to copy")
	}
	if opts.Dir == "" {
		return nil, contract.Fail(contract.FailureInvalidInput, "backup: no folder to copy into")
	}
	if opts.Keep < 1 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"backup: at least one snapshot has to survive a rotation, keep is %d", opts.Keep)
	}
	source, err := filepath.Abs(opts.Source)
	if err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"backup: cannot place %s on this machine: %v", opts.Source, err)
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"backup: cannot place %s on this machine: %v", opts.Dir, err)
	}
	// A folder of copies inside the tree it copies is two faults at once: the
	// walk descends into the snapshots it is still writing, and the copies die
	// with the tree they exist to survive -- one `rm -rf` takes the original
	// and the whole history with it. internal/platform puts the folder beside
	// the state root for that reason; refuse anything that undoes it.
	if under(dir, source) {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"backup: folder %s is inside the source %s it would copy", dir, source)
	}
	for i, e := range opts.Extras {
		if e.Source == "" {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"backup: extra %d has no source path", i)
		}
		if e.Dest == "" {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"backup: extra %d has no destination path", i)
		}
		if filepath.IsAbs(e.Dest) {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"backup: extra %d destination must be a relative path, got %s", i, e.Dest)
		}
		clean := filepath.Clean(e.Dest)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"backup: extra %d destination escapes the snapshot: %s", i, e.Dest)
		}
	}
	return &Store{source: source, dir: dir, keep: opts.Keep, extras: opts.Extras}, nil
}

// Dir reports where snapshots are written.
func (s *Store) Dir() string { return s.dir }

// List reports the finished snapshots, newest first.
//
// Only names are read. Anything that does not parse as a timestamp is not a
// snapshot, which covers the trees left behind by interrupted runs and
// whatever else somebody dropped in the folder. The counters stay at zero:
// they describe the run that produced a tree rather than the tree itself, and
// rebuilding them would mean walking every snapshot on every listing.
func (s *Store) List() ([]Snapshot, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		// No folder is no snapshots. A machine that has never backed up is not
		// a machine whose backups are broken.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot read %s: %v", s.dir, err)
	}
	snapshots := make([]Snapshot, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		at, err := time.Parse(nameLayout, entry.Name())
		if err != nil {
			continue
		}
		snapshots = append(snapshots, Snapshot{
			Name: entry.Name(),
			At:   at,
			Path: filepath.Join(s.dir, entry.Name()),
		})
	}
	slices.SortFunc(snapshots, func(a, b Snapshot) int { return b.At.Compare(a.At) })
	return snapshots, nil
}

// Restore copies one complete snapshot into a new target directory.
//
// The target must not exist. Refusing to replace an existing tree makes the
// operation recoverable: an operator can inspect the restored copy before
// deciding how to swap it into place. The copy is assembled under a partial
// name and published with one rename, so an interrupted restore never looks
// like a complete target.
func (s *Store) Restore(ctx context.Context, name, target string) error {
	if err := ctx.Err(); err != nil {
		return contract.Fail(contract.FailureCanceled, "backup restore stopped: %v", err)
	}
	if strings.TrimSpace(target) == "" {
		return contract.Fail(contract.FailureInvalidInput, "backup: restore target is required")
	}
	if _, err := time.Parse(nameLayout, name); err != nil {
		return contract.Fail(contract.FailureInvalidInput,
			"backup: snapshot %q is not a valid snapshot name", name)
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput,
			"backup: restore target is not a usable path: %q", target)
	}
	snapshots, err := s.List()
	if err != nil {
		return err
	}
	var source string
	for _, snapshot := range snapshots {
		if snapshot.Name == name {
			source = snapshot.Path
			break
		}
	}
	if source == "" {
		return contract.Fail(contract.FailureNotFound,
			"backup: snapshot %s was not found in %s", name, s.dir)
	}
	if under(target, source) || under(target, s.dir) {
		return contract.Fail(contract.FailureInvalidInput,
			"backup: restore target %s must be outside the backup store", target)
	}
	if _, err := os.Lstat(target); err == nil {
		return contract.Fail(contract.FailureInvalidInput,
			"backup: restore target %s already exists; choose a new directory", target)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot inspect restore target %s: %v", target, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot create restore parent for %s: %v", target, err)
	}
	partial := target + ".atenea-restore.partial"
	if _, err := os.Lstat(partial); err == nil {
		return contract.Fail(contract.FailureInvalidInput,
			"backup: incomplete restore already exists at %s", partial)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot inspect incomplete restore %s: %v", partial, err)
	}
	if _, err := s.copyTree(ctx, source, partial, ""); err != nil {
		_ = os.RemoveAll(partial)
		return err
	}
	if err := os.Rename(partial, target); err != nil {
		_ = os.RemoveAll(partial)
		return contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot publish restore %s: %v", target, err)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot durable-publish restore %s: %v", target, err)
	}
	return nil
}

// RestoreInPlace restores a snapshot over an existing directory using a
// reversible two-rename swap. The old directory is retained at
// target+".atenea-previous" so an operator can inspect or roll back it
// explicitly; an existing previous path is refused rather than overwritten.
func (s *Store) RestoreInPlace(ctx context.Context, name, target string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", contract.Fail(contract.FailureCanceled, "backup restore stopped: %v", err)
	}
	if strings.TrimSpace(target) == "" {
		return "", contract.Fail(contract.FailureInvalidInput, "backup: restore target is required")
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return "", contract.Fail(contract.FailureInvalidInput,
			"backup: restore target is not a usable path: %q", target)
	}
	if under(target, s.dir) {
		return "", contract.Fail(contract.FailureInvalidInput,
			"backup: restore target %s must be outside the backup store", target)
	}
	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return "", contract.Fail(contract.FailureNotFound,
			"backup: in-place restore target %s does not exist", target)
	}
	if err != nil {
		return "", contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot inspect restore target %s: %v", target, err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return "", contract.Fail(contract.FailureInvalidInput,
			"backup: in-place restore target %s must be a real directory", target)
	}
	source, err := s.restoreSource(name)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot create restore parent for %s: %v", target, err)
	}
	partial := target + ".atenea-restore.partial"
	previous := target + ".atenea-previous"
	for _, path := range []string{partial, previous} {
		if _, err := os.Lstat(path); err == nil {
			return "", contract.Fail(contract.FailureInvalidInput,
				"backup: restore sidecar already exists at %s", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", contract.Fail(contract.FailurePermissionDenied,
				"backup: cannot inspect restore sidecar %s: %v", path, err)
		}
	}
	if _, err := s.copyTree(ctx, source, partial, ""); err != nil {
		_ = os.RemoveAll(partial)
		return "", err
	}
	if err := os.Rename(target, previous); err != nil {
		_ = os.RemoveAll(partial)
		return "", contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot stage current target %s: %v", target, err)
	}
	if err := os.Rename(partial, target); err != nil {
		_ = os.Rename(previous, target)
		_ = os.RemoveAll(partial)
		return "", contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot publish in-place restore %s: %v", target, err)
	}
	if err := syncDirectory(parent); err != nil {
		return previous, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot durable-publish in-place restore %s: %v", target, err)
	}
	return previous, nil
}

// PromotePrevious rolls back the last in-place restore. The current target is
// retained at target+".atenea-current" so this rollback is itself reversible.
func (s *Store) PromotePrevious(ctx context.Context, target string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", contract.Fail(contract.FailureCanceled, "backup rollback stopped: %v", err)
	}
	target, err := s.restoreTarget(target)
	if err != nil {
		return "", err
	}
	if under(target, s.dir) {
		return "", contract.Fail(contract.FailureInvalidInput,
			"backup: rollback target %s must be outside the backup store", target)
	}
	previous := target + ".atenea-previous"
	current := target + ".atenea-current"
	for _, entry := range []struct {
		path  string
		label string
	}{{target, "rollback target"}, {previous, "previous state"}} {
		info, statErr := os.Lstat(entry.path)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				return "", contract.Fail(contract.FailureNotFound,
					"backup: %s %s does not exist", entry.label, entry.path)
			}
			return "", contract.Fail(contract.FailurePermissionDenied,
				"backup: cannot inspect %s %s: %v", entry.label, entry.path, statErr)
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return "", contract.Fail(contract.FailureInvalidInput,
				"backup: %s %s must be a real directory", entry.label, entry.path)
		}
	}
	if _, statErr := os.Lstat(current); statErr == nil {
		return "", contract.Fail(contract.FailureInvalidInput,
			"backup: rollback sidecar already exists at %s", current)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot inspect rollback sidecar %s: %v", current, statErr)
	}
	if err := os.Rename(target, current); err != nil {
		return "", contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot stage current target %s: %v", target, err)
	}
	if err := os.Rename(previous, target); err != nil {
		_ = os.Rename(current, target)
		return "", contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot promote previous state into %s: %v", target, err)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return current, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot durable-publish rollback %s: %v", target, err)
	}
	return current, nil
}

// DiscardPrevious deletes the retained pre-restore directory. The CLI requires
// an explicit confirmation flag before calling this destructive operation.
func (s *Store) DiscardPrevious(ctx context.Context, target string) error {
	if err := ctx.Err(); err != nil {
		return contract.Fail(contract.FailureCanceled, "backup cleanup stopped: %v", err)
	}
	target, err := s.restoreTarget(target)
	if err != nil {
		return err
	}
	if under(target, s.dir) {
		return contract.Fail(contract.FailureInvalidInput,
			"backup: cleanup target %s must be outside the backup store", target)
	}
	previous := target + ".atenea-previous"
	info, err := os.Lstat(previous)
	if errors.Is(err, fs.ErrNotExist) {
		return contract.Fail(contract.FailureNotFound,
			"backup: previous state %s does not exist", previous)
	}
	if err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot inspect previous state %s: %v", previous, err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"backup: previous state %s must be a real directory", previous)
	}
	if err := os.RemoveAll(previous); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot discard previous state %s: %v", previous, err)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot durable-publish cleanup %s: %v", previous, err)
	}
	return nil
}

func (s *Store) restoreTarget(target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", contract.Fail(contract.FailureInvalidInput, "backup: target is required")
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return "", contract.Fail(contract.FailureInvalidInput,
			"backup: target is not a usable path: %q", target)
	}
	return target, nil
}

func (s *Store) restoreSource(name string) (string, error) {
	if _, err := time.Parse(nameLayout, name); err != nil {
		return "", contract.Fail(contract.FailureInvalidInput,
			"backup: snapshot %q is not a valid snapshot name", name)
	}
	snapshots, err := s.List()
	if err != nil {
		return "", err
	}
	for _, snapshot := range snapshots {
		if snapshot.Name == name {
			return snapshot.Path, nil
		}
	}
	return "", contract.Fail(contract.FailureNotFound,
		"backup: snapshot %s was not found in %s", name, s.dir)
}

// settle is what the caller does to the state root before it is copied. It runs
// only when a copy is actually about to be taken, and a failure there stops the
// copy: a snapshot of a tree somebody could not put in order is exactly the
// snapshot this argument exists to avoid.
//
// It is a parameter rather than something this package does, because this
// package copies a directory and has no idea which files in it are databases.
// Whoever commissions the snapshot holds the handles.
type settle func(context.Context) error

// SnapshotIfDue takes a copy only when the newest one on disk is older than
// every, and reports whether it did.
//
// The rhythm is read from the folder rather than held in a timer, and that is
// the point of the method. Atenea is a service that restarts -- a reboot, an
// upgrade, a crash -- more often than it backs up, and a six-hour timer in
// memory on a machine rebooted every four hours would mean the backup never
// happens once, silently, forever. The metrics compaction reached for the same
// answer for the same reason (see metrics.CompactIfDue): the mark lives where
// the work lands, so restarting cannot lose it.
func (s *Store) SnapshotIfDue(ctx context.Context, now time.Time, every time.Duration, ready settle) (Snapshot, bool, error) {
	if every <= 0 {
		return Snapshot{}, false, contract.Fail(contract.FailureInvalidInput,
			"backup: interval must be above 0, got %s", every)
	}
	existing, err := s.List()
	if err != nil {
		return Snapshot{}, false, err
	}
	if len(existing) > 0 && now.UTC().Sub(existing[0].At) < every {
		return Snapshot{}, false, nil
	}
	// Here, and not before the dueness check: quiescing a database costs a
	// checkpoint, and paying it on every beat of a rhythm that copies once
	// every six hours would be five wasted checkpoints out of six.
	if ready != nil {
		if err := ready(ctx); err != nil {
			return Snapshot{}, false, err
		}
	}
	snapshot, err := s.Snapshot(ctx, now)
	if err != nil {
		return Snapshot{}, false, err
	}
	// A missing source takes no copy and reports none, so a machine that has
	// never been commissioned does not read as one that has been backed up.
	return snapshot, snapshot.Name != "", nil
}

// Snapshot copies the source into a new tree and rotates the old ones out.
//
// A source that does not exist takes no copy: the zero Snapshot comes back
// with no error and nothing is written. Skipping rather than storing an empty
// tree, because an empty tree is a real snapshot -- it becomes the newest, it
// fills a slot, and five in a row would rotate away every copy that held
// something. A state folder that is briefly absent, mid-upgrade or behind a
// mount that came up late, must not be able to delete the history it cannot
// see.
func (s *Store) Snapshot(ctx context.Context, now time.Time) (Snapshot, error) {
	info, err := os.Stat(s.source)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot read %s: %v", s.source, err)
	}
	if !info.IsDir() {
		return Snapshot{}, contract.Fail(contract.FailureInvalidInput,
			"backup: source %s is not a folder", s.source)
	}
	// The source is followed here if it is itself a link. WalkDir would stop
	// at the link instead of descending, and the snapshot would come out as a
	// pointer to the live tree rather than a copy of it -- the one outcome
	// worse than no backup.
	root, err := filepath.EvalSymlinks(s.source)
	if err != nil {
		return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot resolve %s: %v", s.source, err)
	}

	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot create %s: %v", s.dir, err)
	}
	// Trees from runs that were killed hold hardlinks, so they keep the bytes
	// of snapshots that have already rotated away alive on disk. Nothing will
	// ever finish them, so they go before this run needs the room.
	if err := s.sweepPartials(); err != nil {
		return Snapshot{}, err
	}

	existing, err := s.List()
	if err != nil {
		return Snapshot{}, err
	}
	base := ""
	if len(existing) > 0 {
		base = existing[0].Path
	}

	name := now.UTC().Format(nameLayout)
	partial := filepath.Join(s.dir, partialPrefix+name+partialSuffix)
	final := filepath.Join(s.dir, name)

	// Everything is built under the partial name and renamed in one step at
	// the end. An interrupted run therefore never becomes the newest snapshot,
	// which matters twice: nobody restores from half a tree, and the next run
	// never hardlinks against one and inherits its gaps.
	if err := os.RemoveAll(partial); err != nil {
		return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot clear %s: %v", partial, err)
	}
	snapshot, err := s.copyTree(ctx, root, partial, base)
	if err != nil {
		_ = os.RemoveAll(partial)
		return Snapshot{}, err
	}
	for _, extra := range s.extras {
		counts, err := copyExtra(ctx, extra, partial, base)
		if err != nil {
			_ = os.RemoveAll(partial)
			return Snapshot{}, err
		}
		snapshot.Files += counts.Files
		snapshot.Linked += counts.Linked
		snapshot.Copied += counts.Copied
		snapshot.Bytes += counts.Bytes
	}
	// A second run inside the same second answers to the same name. The later
	// one holds the fresher state so it replaces: its copy is already on disk,
	// and its hardlinks keep whatever the tree being dropped shared.
	if err := os.RemoveAll(final); err != nil {
		_ = os.RemoveAll(partial)
		return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot clear %s: %v", final, err)
	}
	if err := os.Rename(partial, final); err != nil {
		_ = os.RemoveAll(partial)
		return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot publish %s: %v", final, err)
	}
	if err := syncDir(s.dir); err != nil {
		return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot durable-publish %s: %v", final, err)
	}
	snapshot.Name = name
	// The same instant the name carries, so a snapshot just taken and one read
	// back by List compare the same way against the interval.
	snapshot.At = now.UTC().Truncate(time.Second)
	snapshot.Path = final

	// The copy is on disk, so it comes back even when the housekeeping behind
	// it fails: the backup succeeded, only the sweep of the old ones did not.
	return snapshot, s.rotate()
}

// rotate drops the oldest trees until Keep of them remain.
//
// This is safe only because there is no chain. A tree holds nothing but its
// own files and hardlinks to files a newer tree may also name, so unlinking
// one leaves the other naming the same inode with the same bytes behind it.
func (s *Store) rotate() error {
	existing, err := s.List()
	if err != nil {
		return err
	}
	if len(existing) <= s.keep {
		return nil
	}
	for _, old := range existing[s.keep:] {
		if err := os.RemoveAll(old.Path); err != nil {
			return contract.Fail(contract.FailurePermissionDenied,
				"backup: cannot remove %s: %v", old.Path, err)
		}
	}
	return syncDir(s.dir)
}

// sweepPartials removes the trees of runs that never finished.
func (s *Store) sweepPartials() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot read %s: %v", s.dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, partialPrefix) || !strings.HasSuffix(name, partialSuffix) {
			continue
		}
		stale := filepath.Join(s.dir, name)
		if err := os.RemoveAll(stale); err != nil {
			return contract.Fail(contract.FailurePermissionDenied,
				"backup: cannot remove %s: %v", stale, err)
		}
	}
	return nil
}

// dirMode holds a directory's real permissions until the tree under it has
// been written.
type dirMode struct {
	path string
	mode fs.FileMode
}

// copyTree writes the whole of root into target, sharing with base whatever
// has not changed. It fills in the counters; the caller names the result once
// the tree is published.
func (s *Store) copyTree(ctx context.Context, root, target, base string) (Snapshot, error) {
	var snapshot Snapshot
	// Every directory is created writable by its owner whatever the source
	// says, and given its real permissions once the tree is complete. A source
	// directory that is not writable would otherwise be recreated first and
	// then refuse to take its own contents.
	var modes []dirMode

	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A file that went away between the listing and the visit cannot
			// be lost, so it is skipped. Anything else stops the run: a backup
			// that quietly drops what it could not read is worse than one that
			// failed, because it looks complete.
			if errors.Is(walkErr, fs.ErrNotExist) && name != root {
				return nil //nolint:nilerr // a file that vanished mid-walk cannot be lost
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)

		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(destination, info.Mode().Perm()|0o700); err != nil {
				return err
			}
			modes = append(modes, dirMode{path: destination, mode: info.Mode().Perm()})
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			// Recreated as a link and never followed. Following one would pull
			// whatever it points at into the snapshot, and a link pointing
			// back up the tree would pull the tree in twice.
			written, err := copyLink(name, destination)
			if err != nil {
				return err
			}
			snapshot.Copied++
			snapshot.Bytes += written
			return nil
		}
		if databaseSidecar(name) {
			return nil
		}
		if !entry.Type().IsRegular() {
			// A socket, a fifo or a device node is not data. There is nothing
			// in one to restore.
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if base != "" && databaseKind(name) == "" && link(filepath.Join(base, relative), destination, info) {
			snapshot.Linked++
			return nil
		}
		written, err := copyFile(name, destination, info, ctx)
		if err != nil {
			return err
		}
		snapshot.Copied++
		snapshot.Bytes += written
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Snapshot{}, contract.Fail(contract.FailureTimeout,
				"backup: copy of %s stopped: %v", root, ctxErr)
		}
		return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
			"backup: copying %s: %v", root, err)
	}
	// The real permissions go on deepest first, once nothing more has to be
	// written inside any of them, and each directory is synced immediately
	// after its own chmod. WalkDir hands out parents before children, so that
	// is this list backwards.
	//
	// Every directory, not just the one the rename publishes. copyFile syncs
	// the bytes of each file it writes, but a file's bytes being durable says
	// nothing about the directory entry that names it, and a hard-linked file
	// is a directory entry and nothing else -- so a snapshot of a tree that
	// shared most of its files with the previous one had almost none of itself
	// on the disk. Only s.dir was synced, after the rename, which made the
	// snapshot's own name durable while its contents were still a promise the
	// page cache was making.
	for i := len(modes) - 1; i >= 0; i-- {
		if err := os.Chmod(modes[i].path, modes[i].mode); err != nil {
			return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
				"backup: cannot set permissions on %s: %v", modes[i].path, err)
		}
		if err := syncDir(modes[i].path); err != nil {
			return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
				"backup: cannot durable-write %s: %v", modes[i].path, err)
		}
	}
	snapshot.Files = snapshot.Linked + snapshot.Copied
	return snapshot, nil
}

// copyExtra copies one extra file into target/extra.Dest, sharing with the
// previous snapshot if the file has not changed since. A source that does not
// exist is skipped silently: a file that has not yet been commissioned must
// not prevent a run from completing.
func copyExtra(ctx context.Context, extra Extra, target, base string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, contract.Fail(contract.FailureTimeout,
			"backup: copy stopped: %v", err)
	}
	info, err := os.Stat(extra.Source)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot read %s: %v", extra.Source, err)
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, contract.Fail(contract.FailureInvalidInput,
			"backup: extra %s is not a regular file", extra.Source)
	}
	destination := filepath.Join(target, extra.Dest)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot create parent directory for %s: %v", destination, err)
	}
	// Extras land after copyTree has already made the tree durable, so each
	// one syncs its own way back up to the snapshot root: a hard-linked extra
	// writes nothing but a directory entry, and a directory created for it
	// here needs its own name in its parent on the disk as well.
	if base != "" && databaseKind(extra.Source) == "" && link(filepath.Join(base, extra.Dest), destination, info) {
		if err := syncUpTo(filepath.Dir(destination), target); err != nil {
			return Snapshot{}, err
		}
		return Snapshot{Files: 1, Linked: 1}, nil
	}
	written, err := copyFile(extra.Source, destination, info, ctx)
	if err != nil {
		return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
			"backup: cannot copy %s: %v", extra.Source, err)
	}
	if err := syncUpTo(filepath.Dir(destination), target); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Files: 1, Copied: 1, Bytes: written}, nil
}

// syncUpTo makes dir durable and then every directory above it as far as
// target, deepest first.
//
// The chain matters as much as the leaf. A directory whose entries are on the
// disk while the entry naming it in its own parent is not is a directory that
// does not exist after a power cut, however durable its contents were.
func syncUpTo(dir, target string) error {
	for {
		if err := syncDir(dir); err != nil {
			return contract.Fail(contract.FailurePermissionDenied,
				"backup: cannot durable-write %s: %v", dir, err)
		}
		parent := filepath.Dir(dir)
		if dir == target || parent == dir || !under(parent, target) {
			return nil
		}
		dir = parent
	}
}

// copyLink copies a symlink by making the same symlink, and reports the bytes
// it cost: nothing is shared with the previous snapshot, and the only thing
// written is the target string.
//
// A link is never a candidate for sharing. Whether link(2) points at the link
// or at what it names is not the same answer on every filesystem, and a link
// costs so little that finding out is not worth the risk of hardlinking the
// wrong inode into a snapshot.
func copyLink(source, destination string) (int64, error) {
	target, err := os.Readlink(source)
	if err != nil {
		return 0, err
	}
	if err := os.Symlink(target, destination); err != nil {
		return 0, err
	}
	return int64(len(target)), nil
}

// link points destination at the copy the previous snapshot already holds,
// when the file has not been touched since. Size and modification time are the
// whole comparison; no content is read, which is what makes a run that changed
// little cost little.
//
// The two times are compared exactly rather than to the nearest second.
// Rounding would call a file unchanged that was rewritten in the same second
// as the last snapshot, and the copy would quietly hold the old bytes -- a
// backup lying about what it holds. Comparing too strictly only costs disk.
//
// It reports whether the link was made. Every failure falls back to a copy and
// none of them is fatal: the folder may be on another filesystem, which
// link(2) refuses to cross, or on one with no hardlinks at all. A snapshot
// that cost full bytes is still a snapshot.
func link(previous, destination string, info fs.FileInfo) bool {
	old, err := os.Lstat(previous)
	if err != nil || !old.Mode().IsRegular() {
		return false
	}
	if old.Size() != info.Size() || !old.ModTime().Equal(info.ModTime()) {
		return false
	}
	// The shared inode carries the older snapshot's permissions, so a chmod
	// with no edit behind it is not caught here -- and must not be, since
	// changing them now would rewrite the older snapshot too. Content is what
	// this protects.
	return os.Link(previous, destination) == nil
}

// copyFile writes a fresh copy and gives it the source's permissions and
// modification time.
//
// The time is not cosmetic. The next run compares size and time against this
// copy, so a file left carrying the time of the copy would look changed for
// ever after and nothing would be shared again.
func copyFile(source, destination string, info fs.FileInfo, contexts ...context.Context) (int64, error) {
	if kind := databaseKind(source); kind != "" {
		ctx := context.Background()
		if len(contexts) > 0 {
			ctx = contexts[0]
		}
		if err := snapshotDatabase(ctx, kind, source, destination); err != nil {
			return 0, err
		}
		file, err := os.OpenFile(destination, os.O_RDWR, info.Mode().Perm())
		if err != nil {
			return 0, err
		}
		if err = file.Sync(); err != nil {
			_ = file.Close()
			return 0, err
		}
		if err = file.Close(); err != nil {
			return 0, err
		}
		if err = os.Chmod(destination, info.Mode().Perm()); err != nil {
			return 0, err
		}
		result, err := os.Stat(destination)
		if err != nil {
			return 0, err
		}
		return result.Size(), nil
	}

	from, err := os.Open(source)
	if err != nil {
		return 0, err
	}
	defer func() { _ = from.Close() }()

	// Created narrow and widened afterwards: the mode handed to OpenFile goes
	// through the umask, which would drop bits the source has.
	to, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(to, from)
	if err != nil {
		_ = to.Close()
		return written, err
	}
	if err := to.Sync(); err != nil {
		_ = to.Close()
		return written, err
	}
	if err := to.Close(); err != nil {
		return written, err
	}
	if err := os.Chmod(destination, info.Mode().Perm()); err != nil {
		return written, err
	}
	return written, os.Chtimes(destination, info.ModTime(), info.ModTime())
}

// under reports whether child sits at or below parent.
func under(child, parent string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
