package checkpoint

import (
	"errors"
	"path/filepath"

	"github.com/Tutitoos/atenea/internal/pidlock"
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
// It is a plain file carrying the holder's pid rather than a kernel flock, for
// the reason internal/pidlock explains: a run whose process died before it could
// clean up must not stay locked forever. Disabled the same way the rest of the
// store is -- nothing to lock when nothing is written.
func (s *Store) Lock(id string) (release func(), err error) {
	if !s.Enabled() {
		return func() {}, nil
	}
	if !runID.MatchString(id) {
		return nil, contract.Fail(contract.FailureInvalidInput, "run id %q is not a safe file name", id)
	}
	release, err = pidlock.Claim(filepath.Join(s.dir, id+".lock"))
	switch {
	case errors.Is(err, pidlock.ErrHeld):
		return nil, contract.Fail(contract.FailureUnavailable,
			"run %s is already being resumed elsewhere", id)
	case err != nil:
		return nil, contract.Fail(contract.FailurePermissionDenied, "run %s: lock: %v", id, err)
	}
	return release, nil
}
