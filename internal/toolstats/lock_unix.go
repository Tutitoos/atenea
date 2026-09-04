//go:build unix

package toolstats

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockOwner holds an exclusive nonblocking lock until the returned file closes.
func lockOwner(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// ownerBusy identifies a live writer holding its lock.
func ownerBusy(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}

// inspectOwner opens only an existing lock and holds a shared lock when no writer owns it.
func inspectOwner(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = unix.EINVAL
	}
	if err == nil {
		err = unix.Flock(fd, unix.LOCK_SH|unix.LOCK_NB)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
