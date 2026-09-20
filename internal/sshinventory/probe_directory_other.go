//go:build !windows

package sshinventory

import "os"

func newPrivateProbeDirectory() (string, error) {
	return os.MkdirTemp("", "atenea-ssh-probe-")
}

func privateProbeDirectory(path string, initial os.FileInfo) bool {
	if initial == nil {
		return false
	}
	current, err := os.Lstat(path)
	return err == nil && current.IsDir() && current.Mode()&os.ModeSymlink == 0 &&
		current.Mode().Perm()&0o077 == 0 && os.SameFile(initial, current)
}
