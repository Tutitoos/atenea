package checkpoint

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Lock claims the exclusive right to continue one run.
//
// Two processes picking up the same interrupted commission would each read
// the same "N steps remaining" state and each redispatch it, which is not a
// conflict either side can detect from its own side of the race: both would
// simply run, and the receipt would end up with whichever wrote last. The
// lock turns that into a clean refusal instead.
//
// It is a plain file, not a kernel flock: the file carries the holder's pid
// so a later attempt can tell a run that is genuinely still going from one
// whose process died before it got to clean up after itself. Disabled the
// same way the rest of the store is -- nothing to lock when nothing is
// written.
func (s *Store) Lock(id string) (release func(), err error) {
	if !s.Enabled() {
		return func() {}, nil
	}
	if !runID.MatchString(id) {
		return nil, contract.Fail(contract.FailureInvalidInput, "run id %q is not a safe file name", id)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, contract.Fail(contract.FailurePermissionDenied,
			"checkpoint directory %s: %v", s.dir, err)
	}
	path := filepath.Join(s.dir, id+".lock")
	if err := s.claim(id, path, true); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

// claim makes one attempt at the lock file, and -- only once, on a lock left
// behind by a pid that no longer exists -- clears it and tries again. A
// second collision after that is a real holder, not a leftover.
func (s *Store) claim(id, path string, retryStale bool) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		_, writeErr := fmt.Fprintf(f, "%d", os.Getpid())
		closeErr := f.Close()
		if writeErr != nil {
			return contract.Fail(contract.FailurePermissionDenied, "run %s: lock: %v", id, writeErr)
		}
		if closeErr != nil {
			return contract.Fail(contract.FailurePermissionDenied, "run %s: lock: %v", id, closeErr)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return contract.Fail(contract.FailurePermissionDenied, "run %s: lock: %v", id, err)
	}
	if retryStale && stale(path) {
		_ = os.Remove(path)
		return s.claim(id, path, false)
	}
	return contract.Fail(contract.FailureUnavailable,
		"run %s is already being resumed elsewhere", id)
}

// stale reports whether the pid written in the lock file is no longer alive.
// An unreadable or unparsable file is treated as a live holder: refusing a
// resume that could in fact proceed costs an operator a retry, and stealing a
// lock that was never actually stale costs a receipt two writers at once.
func stale(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return false
	}
	return !alive(pid)
}
