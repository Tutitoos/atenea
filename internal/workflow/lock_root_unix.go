//go:build darwin || linux

package workflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// ateneaLockRoot is the per-user root shared by every kernel-owned workflow
// lock. It deliberately does not use os.TempDir or os.UserCacheDir: both can
// be redirected by process environment, which would let two Atenea processes
// for the same user disagree about a lock. The effective UID keeps accounts
// from colliding even when they share a home directory, and the absolute
// /tmp path is the same on macOS and Linux regardless of HOME, TMPDIR,
// XDG_CACHE_HOME, or any other process setting.
func ateneaLockRoot() string {
	return filepath.Join("/tmp", "atenea-"+strconv.FormatUint(uint64(os.Geteuid()), 10))
}

func ensureAteneaLockDir(name string) (string, error) {
	root := ateneaLockRoot()
	if err := ensureOwnedLockDir(root); err != nil {
		return "", fmt.Errorf("root: %w", err)
	}
	dir := filepath.Join(root, name)
	if err := ensureOwnedLockDir(dir); err != nil {
		return "", fmt.Errorf("subdirectory: %w", err)
	}
	return dir, nil
}

func ensureOwnedLockDir(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && uint64(stat.Uid) != uint64(os.Geteuid()) {
		return fmt.Errorf("%s is owned by another user", path)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	return nil
}
