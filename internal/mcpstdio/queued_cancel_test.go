package mcpstdio

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

type gatedContext struct {
	context.Context
	entered, release chan struct{}
	once             sync.Once
}

// Done makes the gate and cancellation ready together without a timing assumption.
func (c *gatedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return c.Context.Done()
}

type countWriter struct{ writes atomic.Int32 }

// Write records any accidental write by an already canceled queued request.
func (w *countWriter) Write(p []byte) (int, error) { w.writes.Add(1); return len(p), nil }

// Close satisfies the owned-pipe contract.
func (w *countWriter) Close() error { return nil }

// TestCanceledQueuedWriterLeavesSessionHealthy covers simultaneous gate/cancel readiness.
func TestCanceledQueuedWriterLeavesSessionHealthy(t *testing.T) {
	for range 32 {
		writer := &countWriter{}
		reader, output := io.Pipe()
		s := New(writer, reader, Options{})
		s.writeGate <- struct{}{}
		parent, cancel := context.WithCancel(t.Context())
		ctx := &gatedContext{Context: parent, entered: make(chan struct{}), release: make(chan struct{})}
		done := make(chan error, 1)
		go func() { done <- s.writeContext(ctx, []byte(`{}`)) }()
		<-ctx.entered
		cancel()
		<-s.writeGate
		close(ctx.release)
		err := <-done
		if err == nil || writer.writes.Load() != 0 || s.Err() != nil {
			t.Fatalf("canceled request touched session: writes=%d err=%v session=%v", writer.writes.Load(), err, s.Err())
		}
		_ = s.Close()
		_ = output.Close()
		_ = reader.Close()
	}
}
