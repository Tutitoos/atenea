//go:build unix

package kivgraph

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProgressPersistenceFailureStopsIndexProcess(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "kivgraph")
	body := "#!/bin/sh\nprintf '{\"event\":\"progress\",\"progress\":{\"phase\":\"indexing\"}}\\n'\nsleep 30\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	jobs := &maintenanceJobs{path: filepath.Join(blocked, "job.json")}
	ctx := context.WithValue(context.Background(), maintenancePhaseKey{}, jobs)
	started := time.Now()
	_, err := RunConfiguredIndex(ctx, script, nil, root, "full")
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("progress persistence error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("index process survived progress failure for %s", elapsed)
	}
}
