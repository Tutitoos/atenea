//go:build !unix

package procgroup

import "os/exec"

// isolate has no process group to make on a platform without Setpgid, so
// there is nothing to do: the child dies alone on this platform regardless
// of which of isolate or contain a caller used. Saying so is the honest
// answer.
func isolate(*exec.Cmd) {}

// contain has no process group to make here either, so the default kill
// stands: the child dies and its own children do not.
//
// The pipe half is still fixed, because WaitDelay is set by the caller and
// needs nothing from the platform. A canceled call therefore returns on time
// here too; what it cannot promise is that nothing survives it.
func contain(*exec.Cmd) {}

// terminate has no polite signal to send on a platform without SIGTERM
// semantics reaching a process group, so it goes straight to the same hard
// kill Contain's Cancel already uses. Saying so is the honest answer.
func terminate(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}

// kill stops the single process Contain could reach here; see contain for
// what that does and does not promise.
func kill(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
