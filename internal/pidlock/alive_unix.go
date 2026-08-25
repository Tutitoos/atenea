//go:build unix

package pidlock

import (
	"errors"
	"syscall"
)

// alive reports whether pid still exists. Signal 0 sends nothing; the kernel
// only looks the process up, and answers with ESRCH when there is nobody
// there and with EPERM when there is somebody there this user may not signal.
//
// EPERM is the case worth spelling out: init, a root daemon, another user's
// process. It means the pid exists, so reading it as absent -- which is what
// checking only for a nil error did -- would report a live holder's lock as
// stale and hand its claim to whoever asked next.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
