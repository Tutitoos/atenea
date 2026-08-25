//go:build linux || darwin || freebsd

package dashboard

import "os"

// syncDirectory makes the rename durable on Unix filesystems that support
// syncing a directory. Windows is outside Atenea's supported build targets.
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
