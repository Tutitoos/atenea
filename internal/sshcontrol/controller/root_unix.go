//go:build darwin || linux

package controller

import (
	"os"
	"syscall"

	"github.com/Tutitoos/atenea/internal/sshcontrol/localipc"
)

func prepareRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return localipc.ErrPrivateRoot
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	return localipc.CheckRoot(root)
}
