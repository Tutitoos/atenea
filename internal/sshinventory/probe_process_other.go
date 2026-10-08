//go:build !darwin && !linux

package sshinventory

import "os/exec"

func configureProbeProcess(_ *exec.Cmd, _ bool) {}

func stopProbeProcess(cmd *exec.Cmd, _ bool) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
