package workflow_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const sharedDeadlineRegressionDigest = "sha256:global-slot-deadline-regression"

func TestTwoWorkflowsShareGlobalSlotsWithoutDeadlock(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "one"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "two"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "worker")
	marker := filepath.Join(root, "started-")
	script := fmt.Sprintf("#!/bin/sh\ncat >/dev/null\ntouch %s$$\nwhile [ $(find %s -maxdepth 1 -name 'started-*' | wc -l) -lt 2 ]; do sleep 0.01; done\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'\n", marker, root)
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := []config.WorkflowProfile{{Name: "global-slot-regression", Version: "v1", MaxParallelAgent: 2, MaxParallelReview: 2}}
	options := workflow.Options{Lanes: config.Workflow{MaxParallelAgent: 2, MaxParallelReview: 2},
		ProfileName: "global-slot-regression", Profiles: profile}
	h1 := newHarnessWith(t, options, filepath.Join(root, "one"), declared("worker", command, config.PoolAgent))
	h2 := newHarnessWith(t, options, filepath.Join(root, "two"), declared("worker", command, config.PoolAgent))
	graph := graphOf(step("first", "worker", nil), step("second", "worker", nil))
	type result struct {
		run workflow.Run
		err error
	}
	results := make(chan result, 2)
	go func() { run, err := h1.engine.Start(t.Context(), graph); results <- result{run: run, err: err} }()
	go func() { run, err := h2.engine.Start(t.Context(), graph); results <- result{run: run, err: err} }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("workflow finished with error: %v", got.err)
			}
		case <-deadline.C:
			t.Fatal("two workflows deadlocked while competing for global slots")
		}
	}
}

func TestGlobalSlotDeadlineIsAnUnjudgedExpiration(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "holder-started")
	holder := exec.Command(os.Args[0], "-test.run=TestGlobalSlotHolderProcess", "--")
	holder.Env = append(os.Environ(),
		"ATENEA_GLOBAL_SLOT_HOLDER=1",
		"ATENEA_GLOBAL_SLOT_HOLDER_MARKER="+marker,
	)
	if err := holder.Start(); err != nil {
		t.Fatalf("start global-slot holder: %v", err)
	}
	t.Cleanup(func() {
		if holder.Process != nil {
			_ = holder.Process.Kill()
		}
	})
	waitForGlobalSlotMarker(t, marker)

	dir := t.TempDir()
	profile := []config.WorkflowProfile{{Name: "shared-deadline-regression", Version: "v1",
		Digest: sharedDeadlineRegressionDigest, MaxDuration: 60 * time.Millisecond,
		MaxParallelAgent: 1, MaxParallelReview: 1}}
	h := newHarnessWith(t, workflow.Options{ProfileName: profile[0].Name, Profiles: profile}, dir,
		declared("reader", answers(t, dir, "reader"), config.PoolAgent))
	run, err := h.engine.Start(t.Context(), graphOf(step("read", "reader", nil)))
	if err == nil || contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("workflow waiting for global slot = %v, want unavailable deadline failure", err)
	}
	if run.Stop != workflow.StopUnjudged || run.Closed {
		t.Fatalf("workflow after global-slot deadline = stop %q closed %v, want unjudged/open", run.Stop, run.Closed)
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("global-slot holder: %v", err)
	}
}

func TestDurationExpiredBeforeDispatchIsPersistedUnjudged(t *testing.T) {
	dir := t.TempDir()
	profile := []config.WorkflowProfile{{Name: "expired-before-dispatch", Version: "v1",
		MaxDuration: time.Nanosecond, MaxParallelAgent: 1, MaxParallelReview: 1}}
	h := newHarnessWith(t, workflow.Options{ProfileName: profile[0].Name, Profiles: profile}, dir,
		declared("reader", answers(t, dir, "reader"), config.PoolAgent))
	run, err := h.engine.Start(t.Context(), graphOf(step("read", "reader", nil)))
	if err == nil || contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("expired workflow = %v, want unavailable", err)
	}
	if run.Stop != workflow.StopUnjudged || run.Closed {
		t.Fatalf("expired workflow = stop %q closed %v, want unjudged/open", run.Stop, run.Closed)
	}
	loaded, loadErr := h.state.Load(t.Context(), run.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.Stop != workflow.StopUnjudged || loaded.Closed {
		t.Fatalf("persisted workflow = stop %q closed %v", loaded.Stop, loaded.Closed)
	}
}

func TestGlobalSlotHolderProcess(t *testing.T) {
	if os.Getenv("ATENEA_GLOBAL_SLOT_HOLDER") != "1" {
		t.Skip("subprocess helper")
	}
	marker := os.Getenv("ATENEA_GLOBAL_SLOT_HOLDER_MARKER")
	dir := t.TempDir()
	body := "touch " + marker + "\nsleep 1\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'"
	profile := []config.WorkflowProfile{{Name: "shared-deadline-regression", Version: "v1",
		Digest: sharedDeadlineRegressionDigest, MaxDuration: 3 * time.Second,
		MaxParallelAgent: 1, MaxParallelReview: 1}}
	h := newHarnessWith(t, workflow.Options{ProfileName: profile[0].Name, Profiles: profile}, dir,
		declared("reader", stub(t, dir, "reader", body), config.PoolAgent))
	if _, err := h.engine.Start(t.Context(), graphOf(step("read", "reader", nil))); err != nil {
		t.Fatalf("holder workflow: %v", err)
	}
}

func waitForGlobalSlotMarker(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for global-slot holder marker %s", marker)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
