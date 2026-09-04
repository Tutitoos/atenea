package mcpstdio

import (
	"context"
	"io"
	"testing"
	"time"
)

// TestAuditPipeWriteIgnoresDeadline checks the regression scenario: audit pipe write ignores deadline.
func TestAuditPipeWriteIgnoresDeadline(t *testing.T) {
	read, write := io.Pipe()
	outRead, outWrite := io.Pipe()
	defer read.Close()
	defer outWrite.Close()
	defer outRead.Close()
	s := New(write, outRead, Options{})
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Initialize(ctx) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected deadline")
		}
	case <-time.After(time.Second):
		t.Fatal("write exceeded deadline")
	}
	if s.Err() == nil {
		t.Fatal("partially written session remains reusable")
	}
}
