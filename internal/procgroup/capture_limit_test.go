package procgroup

import (
	"bytes"
	"errors"
	"os/exec"
	"testing"
)

// TestCaptureBoundaryReportsOverflow distinguishes exact-size and overflowing streams.
func TestCaptureBoundaryReportsOverflow(t *testing.T) {
	c := NewCapture(nil)
	_, _ = c.Write(bytes.Repeat([]byte("x"), MaxOutput))
	if c.Err() != nil || c.Len() != MaxOutput {
		t.Fatal("exact boundary refused")
	}
	_ = c.WriteByte('x')
	if !errors.Is(c.Err(), ErrOutputLimit) || c.Len() != MaxOutput {
		t.Fatal("overflow not reported")
	}
}

// TestOverflowKeepsExitError preserves diagnostics even when capture kills the process.
func TestOverflowKeepsExitError(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "echo fixture >&2; head -c 9000000 /dev/zero; exit 2")
	Contain(cmd)
	_, err := Output(cmd)
	var exit *exec.ExitError
	if !errors.Is(err, ErrOutputLimit) || !errors.As(err, &exit) || !bytes.Contains(exit.Stderr, []byte("fixture")) {
		t.Fatalf("lost process error: %v", err)
	}
}
