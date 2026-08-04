// Package procgroup makes a canceled child process actually stop.
//
// Two things go wrong when a caller cancels a call that spawned a command, and
// neither is obvious from the code that has the bug. Go's exec kills the
// process it started and nothing below it, so a client that spawned helpers of
// its own -- language servers, MCP servers, hooks, a model turn's tool
// subprocesses -- leaves them running after the parent is gone. And Wait does
// not return until the output pipes reach EOF, which cannot happen while any
// of those survivors still holds the write end it inherited. The visible
// symptom is the one that hides the cause: ctrl-c appears to do nothing, and
// the call sits there for as long as the longest orphan lives.
//
// Contain fixes both, so cancellation means what it says.
package procgroup

import (
	"os/exec"
	"time"
)

// Grace is how long Wait may keep waiting on inherited pipes after the child
// has been killed. It is short on purpose: at this point the caller has
// already asked to stop, and the only thing left to collect is whatever the
// child managed to write before it died.
const Grace = 2 * time.Second

// Contain wires cmd so canceling its context stops the whole tree it started
// and returns promptly. Call it after exec.CommandContext and before the
// process is started.
func Contain(cmd *exec.Cmd) {
	// WaitDelay is the half that is not platform-specific, and the one that
	// turns an unbounded hang into a bounded wait: once the context is done,
	// Go gives the pipes this long and then closes them itself.
	cmd.WaitDelay = Grace
	contain(cmd)
}

// Isolate puts cmd in a process group of its own without wiring cmd.Cancel
// or cmd.WaitDelay. Call it after exec.Command -- not CommandContext -- and
// before the process is started, for a caller that manages a child's whole
// lifecycle itself on its own timers and calls Terminate or Kill directly
// rather than through a context.
//
// Contain and Isolate do not mix: Cancel only works on a Cmd that was built
// with CommandContext, and Start refuses one that carries a Cancel func
// otherwise. A caller driven by ctx cancellation wants Contain; a caller
// like internal/supervisor, driven by its own state machine, wants this.
func Isolate(cmd *exec.Cmd) {
	isolate(cmd)
}

// Terminate asks the tree cmd started to stop the polite way: SIGTERM to
// whichever part of it this platform can reach. Call it for an orderly stop,
// where something is still watching for the exit and will decide whether
// more force is needed -- unlike the Cancel wired by contain, which exists
// for a caller that has already stopped waiting and wants the fast answer.
func Terminate(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return terminate(cmd)
}

// Kill stops the tree cmd started outright. Call it once Terminate's grace
// period has run out and something is still standing.
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return kill(cmd)
}
