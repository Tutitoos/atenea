//go:build !unix

package procgroup

import "os/exec"

// contain has no process group to make on a platform without Setpgid, so the
// default kill stands: the child dies and its own children do not. Saying so
// is the honest answer.
//
// The pipe half is still fixed, because WaitDelay is set by the caller and
// needs nothing from the platform. A canceled call therefore returns on time
// here too; what it cannot promise is that nothing survives it.
func contain(*exec.Cmd) {}
