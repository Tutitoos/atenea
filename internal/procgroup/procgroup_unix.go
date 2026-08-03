//go:build unix

package procgroup

import (
	"os/exec"
	"syscall"
)

// contain puts the child in a process group of its own and kills that group
// rather than the single process.
//
// Setpgid is what makes the group exist: without it the child joins Atenea's
// own group, and signaling the group would signal Atenea. With it, the
// child's pid is also the group id, so the negative pid below names every
// descendant that has not since detached.
//
// The kill is SIGKILL and not SIGINT, because by the time this runs the
// decision is already made and nothing is waiting for a tidy exit. A client
// asked to shut down politely can take as long as it likes, which is the
// behavior being fixed.
func contain(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative pid is the group. Errors are swallowed on purpose: the
		// only interesting one is "no such process", which means the tree
		// exited on its own between the check and the signal.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return nil
	}
}
