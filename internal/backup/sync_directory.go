//go:build linux || darwin || freebsd

package backup

import "os"

// syncDirectory makes a completed rename durable on filesystems that expose
// directory fsync. The file contents are synced by copyFile before close;
// this second sync protects the published directory entry itself.
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
