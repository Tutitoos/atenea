package mcpprobe

import (
	"io"
	"strings"
	"testing"
)

// The probe's stderr buffer has a roof, and clip() is not it.
//
// clip bounds the *message* an error carries, not the bytes held while the
// child is still running: io.Copy pours the child's whole stderr in here for
// the length of the probe, and ProbeAll runs every declared server at once,
// so a single server writing to stderr in a loop is one unbounded buffer per
// declaration held for the whole timeout and then thrown away two hundred
// characters later. What is kept is the beginning rather than the end, which
// is the right half for a process that lives for seconds: a probe fails
// during startup, and startup is where a stdio server prints the reason it
// cannot run.
func TestTheProbesStderrBufferKeepsTheBeginningAndHasACeiling(t *testing.T) {
	var buf said
	chunk := strings.Repeat("z", 4<<10)
	// The first thing the child says is the thing worth keeping.
	if _, err := buf.Write([]byte("cannot find module foo\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	for range 512 { // two megabytes of noise after it
		if _, err := buf.Write([]byte(chunk)); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}
	if got := len(buf.String()); got > saidBytes {
		t.Errorf("held %d bytes of stderr, want no more than %d", got, saidBytes)
	}
	if !strings.HasPrefix(buf.String(), "cannot find module foo") {
		t.Error("the first thing the child said was dropped: the ceiling is discarding the wrong end")
	}
}

// Write must never report short, whatever it decides to keep.
//
// io.Copy treats a writer that accepted fewer bytes than it was handed as
// io.ErrShortWrite and stops copying, which would close the pipe under a child
// that is still running -- turning a buffer ceiling into a truncated child.
func TestTheStderrCeilingNeverReportsAShortWrite(t *testing.T) {
	var buf said
	if _, err := io.Copy(&buf, strings.NewReader(strings.Repeat("y", saidBytes*3))); err != nil {
		t.Fatalf("copying past the ceiling: %v", err)
	}
}
