//go:build linux || darwin

package pidlock

import (
	"os"
	"syscall"
)

// lockFile takes the kernel's exclusive hold on f, without blocking, and
// reports whether it got it. The hold lasts until this file is closed or the
// process dies, which is the property the whole package rests on: a holder
// that was killed outright leaves nothing for the next claimant to clean up.
//
// flock rather than fcntl locking, because flock conflicts between two
// descriptors even inside one process. Two components of one Atenea claiming
// the same lock is a mistake worth refusing, and fcntl locks -- which are
// owned by the process, not the descriptor -- would grant both.
func lockFile(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}
