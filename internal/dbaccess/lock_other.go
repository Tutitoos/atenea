//go:build !linux && !darwin

package dbaccess

import (
	"fmt"
	"os"
)

const lockingSupported = false

// tryLock fails closed on platforms without the supported locking primitive.
func tryLock(_ *os.File, _ bool) (bool, error) {
	return false, fmt.Errorf("database access locking is unsupported on this platform")
}
