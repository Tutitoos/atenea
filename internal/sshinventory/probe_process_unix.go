//go:build darwin || linux

package sshinventory

import (
	"os/exec"
	"syscall"
)

func configureProbeProcess(cmd *exec.Cmd, hasJump bool) {
	if hasJump {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}

func stopProbeProcess(cmd *exec.Cmd, hasJump bool) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if hasJump {
		// ProxyJump starts another ssh. Kill the private process group so the
		// gateway connection cannot outlive a timed-out or completed probe.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}
