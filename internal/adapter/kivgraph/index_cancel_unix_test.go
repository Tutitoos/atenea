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
	deadline := time.Now().Add(5 * time.Second)
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
	case <-time.After(4 * time.Second):
		t.Fatal("index cancellation left a pipe held by an analyzer")
	}
}
