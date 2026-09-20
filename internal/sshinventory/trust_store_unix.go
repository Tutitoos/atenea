//go:build darwin || linux

package sshinventory

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func createPrivateTrustDirectory(path string) error { return os.Mkdir(path, 0o700) }

func preparePrivateTrustFile(_ string, _ *os.File) error { return nil }

func privateTrustDirectory(_ string, info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o077 == 0
}

func privateTrustFile(info os.FileInfo, _ *os.File) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0
}

// O_NOFOLLOW refuses a final-component symlink inserted after Lstat;
// O_NONBLOCK prevents a swapped FIFO from hanging a diagnostic read.
func openPrivatePin(root *os.Root, name string) (*os.File, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, ErrProbeUnsupported
		}
		return nil, err
	}
	return file, nil
}

func lockPrivateTrustRecord(root *os.Root, name string) (func(), error) {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, statErr := root.Lstat(name)
	opened, openErr := file.Stat()
	if statErr != nil || openErr != nil || !privateTrustFile(opened, file) || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, ErrProbeUnsupported
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrTrustBusy
		}
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
