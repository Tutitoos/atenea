//go:build darwin || linux

package sshinventory

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

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
