//go:build linux || darwin

package dbaccess

import (
	"errors"
	"os"
	"syscall"
)

// tryLock acquires a nonblocking shared or exclusive descriptor lock.
func tryLock(f *os.File, exclusive bool) (bool, error) {
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}
