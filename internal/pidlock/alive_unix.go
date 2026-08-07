//go:build unix

package pidlock

import "syscall"

// alive reports whether pid still exists. Signal 0 sends nothing; the kernel
// only checks that the process is there and returns ESRCH when it is not.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
