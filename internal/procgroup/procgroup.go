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
