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

// Options says what to copy, where to put it, and how many copies survive.
type Options struct {
	// Source is the tree to protect. It does not have to exist yet.
	Source string
	// Dir is the folder of snapshots. It must not sit inside Source.
	Dir string
	// Keep is how many snapshots a rotation leaves behind. At least one.
	Keep int
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
	return &Store{source: source, dir: dir, keep: opts.Keep}, nil
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
func (s *Store) SnapshotIfDue(ctx context.Context, now time.Time, every time.Duration) (Snapshot, bool, error) {
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
	return nil
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
		if !entry.Type().IsRegular() {
			// A socket, a fifo or a device node is not data. There is nothing
			// in one to restore.
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if base != "" && link(filepath.Join(base, relative), destination, info) {
			snapshot.Linked++
			return nil
		}
		written, err := copyFile(name, destination, info)
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
	// written inside any of them. WalkDir hands out parents before children,
	// so that is this list backwards.
	for i := len(modes) - 1; i >= 0; i-- {
		if err := os.Chmod(modes[i].path, modes[i].mode); err != nil {
			return Snapshot{}, contract.Fail(contract.FailurePermissionDenied,
				"backup: cannot set permissions on %s: %v", modes[i].path, err)
		}
	}
	snapshot.Files = snapshot.Linked + snapshot.Copied
	return snapshot, nil
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
func copyFile(source, destination string, info fs.FileInfo) (int64, error) {
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
