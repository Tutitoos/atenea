// Package pidlock claims a named right for one process at a time.
//
// It is a plain file carrying the holder's pid, plus an advisory lock the
// kernel holds on that same file. Each answers a question the other cannot.
//
// The kernel's lock is the exclusion. It is taken atomically and, more
// importantly, it is dropped by the kernel when the holder is killed outright
// -- the one case no protocol written on top of a plain file gets right.
// Recovering a lock left by a dead pid without one means noticing the pid is
// gone, removing the file and creating it again, and claimants racing through
// those three steps can all come out believing they won: sixteen of them
// against one stale lock ended with more than one holder in three runs out of
// twenty, which is the measurement that put the flock here. Re-reading the
// file between the check and the removal narrows that window; it does not
// close it, because there is no compare-and-delete to close it with.
//
// The pid in the file is the explanation. A kernel lock carries no name, and
// "no" is not a usable refusal for a person waiting to be told which process
// is holding their service down. It is also the whole exclusion on a system
// with no advisory lock to take -- see lock_other.go -- where this degrades to
// exactly the cooperative check it always was.
//
// Errors here are plain errors on purpose. What bin a refusal belongs in and how
// it is worded depend on what was being claimed, and only the caller knows that.
package pidlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrHeld is returned when the claim belongs to a process that is still alive.
// It is the one outcome callers are expected to recognize and rephrase; every
// other error is the file system refusing to cooperate.
var ErrHeld = errors.New("held by a live process")

// Claim takes the lock at path for this process and returns the release. The
// directory is created if it is missing.
//
// A lock left behind by a pid that no longer exists is taken over, because the
// kernel already dropped that process's hold on it when it died; nothing has
// to be removed and re-created for that to be safe. A lock whose pid is still
// alive is refused with ErrHeld even when the advisory lock is somehow free,
// which is what keeps the refusal honest on a system that has no advisory lock
// at all and against a holder from an older build of this binary.
func Claim(path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("lock directory: %w", err)
	}
	// Not O_TRUNC: the pid already in the file is what decides whether this
	// claim may proceed at all, and truncating it on the way in would erase
	// the answer before reading it.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if !lockFile(f) {
		_ = f.Close()
		return nil, ErrHeld
	}
	if occupied(path) {
		_ = f.Close()
		return nil, ErrHeld
	}
	if err := writePID(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { releaseClaim(path, f) }, nil
}

// Holder reports the pid written in the lock file, or zero when there is no
// readable claim. It is for a message a person has to read: by the time they
// read it the pid may be gone, so nothing may act on the answer.
func Holder(path string) int {
	pid, ok := readPID(path)
	if !ok {
		return 0
	}
	return pid
}

// Alive reports whether a pid still exists on this machine.
//
// Exported because the trace store asks the same question about a different
// artifact: a row left open names the Atenea that opened it, and closing it
// while that process is still working would be worse than leaving it open.
// One probe, written once -- a second copy of a signal-0 check is how two
// parts of the same binary end up disagreeing about what "gone" means.
//
// It answers about a pid, not about a run. A pid recycled after a reboot
// reads as alive, which leaves a stale row open longer than it should; that
// is the harmless direction, and the loud one, because an open row is
// visible.
func Alive(pid int) bool { return pid > 0 && alive(pid) }

// occupied reports whether what the lock file says makes this claim somebody
// else's to refuse, whatever the advisory lock had to say about it.
//
// A file with no bytes in it is nobody's: it is a claim that died between
// creating the file and writing its pid into it. Reading that as a holder --
// which an unparsable file is deliberately read as -- meant the file could
// never be cleared and the service it guarded could not be started again
// without deleting it by hand.
//
// Bytes that are not a pid are treated as a live holder instead. On Linux and
// macOS that is over-cautious, since the advisory lock already answered the
// question; it is the only guard there is on a system without one, and being
// refused costs an operator one deletion where guessing wrong costs two
// writers at once.
func occupied(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return false
	}
	pid, err := strconv.Atoi(trimmed)
	if err != nil {
		return true
	}
	return alive(pid)
}

// writePID puts this process's pid in the file, replacing whatever a previous
// holder left there. Truncating first matters: a shorter pid written over a
// longer one would leave the tail of the old number behind, and a lock file
// reading "12345" when the holder is 123 names somebody else's process.
func writePID(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	return err
}

// releaseClaim gives the lock back: the file is emptied so the next Holder
// call finds nobody, and closing the file is what drops the kernel's hold.
//
// The file itself stays. Unlinking it is how a lock whose exclusion lives on
// an inode ends up with two holders -- one claimant that opened the old inode
// and one that created a fresh file at the same path, each holding a lock the
// other cannot see.
//
// Emptying it is conditional for a smaller reason: a lock that has since
// changed hands is somebody else's claim, and erasing their pid would take
// away the only name a refusal could offer.
func releaseClaim(path string, f *os.File) {
	if pid, ok := readPID(path); !ok || pid == os.Getpid() {
		_ = f.Truncate(0)
	}
	_ = f.Close()
}

func readPID(path string) (int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, false
	}
	return pid, true
}
