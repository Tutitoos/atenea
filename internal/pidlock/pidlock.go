// Package pidlock claims a named right for one process at a time.
//
// It is a plain file carrying the holder's pid, not a kernel flock. The pid is
// the whole reason: a process that was killed outright cannot release anything
// on its way down, so a later attempt has to be able to tell a holder that is
// genuinely still there from one whose process died before it could clean up
// after itself. A flock would answer the first question for free and leave the
// second unanswerable -- there would be nothing on disk to read, and nothing to
// say in the refusal beyond "no".
//
// Errors here are plain errors on purpose. What bin a refusal belongs in and how
// it is worded depend on what was being claimed, and only the caller knows that.
package pidlock

import (
	"errors"
	"fmt"
	"io/fs"
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
// A lock left behind by a pid that no longer exists is cleared and the claim
// retried -- once. A second collision after that is a real holder, not a
// leftover, and there is no third attempt: two processes both clearing and both
// retrying would eventually both succeed.
func Claim(path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("lock directory: %w", err)
	}
	if err := claim(path, true); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
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

func claim(path string, retryStale bool) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		_, writeErr := fmt.Fprintf(f, "%d", os.Getpid())
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	if !errors.Is(err, fs.ErrExist) {
		return err
	}
	if retryStale && stale(path) {
		_ = os.Remove(path)
		return claim(path, false)
	}
	return ErrHeld
}

// stale reports whether the pid written in the lock file is no longer alive. An
// unreadable or unparsable file is treated as a live holder: refusing a claim
// that could in fact proceed costs an operator a retry, and stealing a lock that
// was never actually stale costs two writers at once.
func stale(path string) bool {
	pid, ok := readPID(path)
	if !ok {
		return false
	}
	return !alive(pid)
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
