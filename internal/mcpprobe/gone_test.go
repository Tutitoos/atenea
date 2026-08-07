package mcpprobe

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)

// In package mcpprobe, not mcpprobe_test, because the decision worth pinning is
// unexported and the alternative is a test that races.
//
// TestAServerThatStartsAndDiesSaysSoAndCarriesItsStderr covers the same rule
// from the outside, and it passed on the machine this was written on for days
// while failing on a CI runner from the same commit: whether the write to a
// dead child's stdin sees EPIPE, or the request fits in the pipe buffer and the
// death surfaces one ReadString later, is not something a caller can arrange. A
// test that can only observe whichever side won is not evidence about the rule.

// TestARealBrokenPipeIsReadAsAGoneChild uses a real pipe and a real write to a
// closed read end rather than a fabricated syscall.EPIPE, because the value that
// has to be recognized is whatever the OS actually produces here -- an
// *fs.PathError wrapping the errno, not the errno.
func TestARealBrokenPipeIsReadAsAGoneChild(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := r.Close(); err != nil {
		t.Fatalf("close read end: %v", err)
	}

	// Go's runtime only lets SIGPIPE kill the process for writes to fd 1 and 2,
	// so a write here returns the error rather than dying.
	_, writeErr := w.Write([]byte("initialize\n"))
	if writeErr == nil {
		t.Skip("this platform buffered a write to a pipe with no reader")
	}
	if !errors.Is(writeErr, syscall.EPIPE) {
		t.Fatalf("write error = %v, want it to wrap EPIPE", writeErr)
	}
	if !childIsGone(writeErr) {
		t.Errorf("childIsGone(%v) = false, want true: this is what a dead child's pipe returns", writeErr)
	}
}

// TestAWriteFailureThatIsNotAGoneChildKeepsItsOwnWording is the other half.
// Collapsing every write error into "exited without answering" would trade one
// wrong sentence for another: when the write really is the news, it has to say
// so.
func TestAWriteFailureThatIsNotAGoneChildKeepsItsOwnWording(t *testing.T) {
	for name, err := range map[string]error{
		"permission":     &os.PathError{Op: "write", Path: "|1", Err: syscall.EACCES},
		"no space":       &os.PathError{Op: "write", Path: "|1", Err: syscall.ENOSPC},
		"wrapped errno":  fmt.Errorf("stdin: %w", syscall.EIO),
		"plain sentence": errors.New("stdin went away"),
	} {
		t.Run(name, func(t *testing.T) {
			if childIsGone(err) {
				t.Errorf("childIsGone(%v) = true, want false", err)
			}
		})
	}
}

// TestTheDeadChildSentenceIsAProcessFact is the regression this file exists for.
// The bug was not a wrong sentence, it was two sentences for one fact with a
// race deciding which one a caller saw. One sentinel, shared by both paths, is
// how that stays fixed -- a future edit that reintroduces a second literal has
// to delete this test to do it.
func TestTheDeadChildSentenceIsAProcessFact(t *testing.T) {
	if !strings.Contains(errExited.Error(), "exited") {
		t.Errorf("errExited = %q, want it to name the process fact", errExited)
	}
	// A connection metaphor is exactly what this sentence replaced: "closed
	// before it could be asked: write |1: broken pipe" sent a reader to the
	// network for a process that never got off the ground.
	for _, banned := range []string{"closed", "connection", "broken pipe"} {
		if strings.Contains(errExited.Error(), banned) {
			t.Errorf("errExited = %q, want no %q in it: there is no connection here", errExited, banned)
		}
	}
}
