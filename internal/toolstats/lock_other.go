//go:build !unix

package toolstats

import (
	"fmt"
	"os"
)

// lockOwner fails explicitly where reliable writer recovery is unsupported.
func lockOwner(_ string) (*os.File, error) {
	return nil, fmt.Errorf("stats writer locking is unsupported on this platform")
}

// ownerBusy never treats unsupported locking as proof of a live writer.
func ownerBusy(_ error) bool { return false }
