//go:build unix

package kivgraph

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An analyzer grandchild inherits stdout. Cancellation must close that pipe,
// not leave RunConfiguredIndex waiting for the orphan's normal exit.
func TestIndexCancellationClosesAnalyzerGrandchildren(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(root, "ready")
	script := filepath.Join(root, "indexer")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 60 &\necho ready > \"$READY\"\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := RunConfiguredIndex(ctx, script, []string{"READY=" + ready}, root, "full")
		done <- err
	}()
	// The full race suite runs package binaries concurrently. Process startup
	// can exceed five seconds on a saturated runner even though cancellation
	// remains prompt once this fixture has actually started.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("index fixture did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled index succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("index cancellation left a pipe held by an analyzer")
	}
}
