//go:build !linux && !darwin

package pidlock

import "os"

// lockFile has no advisory lock to take outside the two systems Atenea
// installs itself on, so it reports success and leaves the exclusion to the
// pid check in Claim -- the cooperative protocol this package used everywhere
// before the flock. Refusing every claim instead would make the lock file the
// thing that stops Atenea from starting on such a machine, which is a worse
// answer than the one weaker guarantee it can honestly offer.
func lockFile(*os.File) bool { return true }
