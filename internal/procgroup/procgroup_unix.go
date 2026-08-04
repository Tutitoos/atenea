//go:build unix

package procgroup

import (
	"errors"
	"os/exec"
	"syscall"
)

// isolate puts the child in a process group of its own, the half of contain
// that has nothing to do with a context: Setpgid is what makes the group
// exist at all, and it is what Terminate and Kill both depend on regardless
// of whether anything above them ever wires cmd.Cancel.
//
// Setpgid is what makes the group exist: without it the child joins Atenea's
// own group, and signaling the group would signal Atenea. With it, the
// child's pid is also the group id, so the negative pid Terminate and Kill
// use names every descendant that has not since detached.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// contain adds a Cancel on top of isolate, so a caller that lets a
// context's cancellation stop the tree gets the group killed and not just
// the single process.
//
// The kill is SIGKILL and not SIGINT, because by the time this runs the
// decision is already made and nothing is waiting for a tidy exit. A client
// asked to shut down politely can take as long as it likes, which is the
// behavior being fixed.
func contain(cmd *exec.Cmd) {
	isolate(cmd)
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

// terminate sends SIGTERM to the group. See contain for why the group and
// not the single process.
func terminate(cmd *exec.Cmd) error {
	return killGroup(cmd, syscall.SIGTERM)
}

// kill sends SIGKILL to the group, the same signal contain's Cancel uses.
func kill(cmd *exec.Cmd) error {
	return killGroup(cmd, syscall.SIGKILL)
}

// killGroup signals the negative pid, which is the group. ESRCH is not an
// error here: it means the tree exited on its own between the caller's check
// and the signal, which is the outcome being asked for either way.
func killGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
