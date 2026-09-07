package workflow_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
)

func TestCancelIsDurableAndIdempotent(t *testing.T) {
	h := newHarness(t, noCeiling(), declared("worker", answers(t, t.TempDir(), "worker"), config.PoolAgent))
	run, _, err := h.engine.Create(t.Context(), graphOf(step("one", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	first, err := h.state.Cancel(t.Context(), run.ID, time.Now())
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	second, err := h.state.Cancel(t.Context(), run.ID, time.Now())
	if err != nil {
		t.Fatalf("second Cancel: %v", err)
	}
	for name, got := range map[string]workflow.Run{"first": first, "second": second} {
		if got.Stop != workflow.StopAborted || got.Closed || got.WriterPID != 0 {
			t.Errorf("%s cancellation snapshot = stop %q closed %v writer %d, want durable aborted open run",
				name, got.Stop, got.Closed, got.WriterPID)
		}
	}
}

func TestCancelCutsTheRunningEngineAndLeavesNoNewDispatch(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	body := "touch " + marker + "\nsleep 20\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'"
	h := newHarness(t, noCeiling(), declared("worker", stub(t, dir, "worker", body), config.PoolAgent))
	done := make(chan workflow.Run, 1)
	errs := make(chan error, 1)
	go func() {
		run, err := h.engine.Start(t.Context(), graphOf(
			step("one", "worker", nil), step("two", "worker", nil)))
		done <- run
		errs <- err
	}()
	waitFor(t, dir, "started")
	if _, err := h.state.Cancel(t.Context(), "", time.Now()); err == nil {
		t.Fatal("canceling an empty workflow unexpectedly succeeded")
	}
	// The id is available in the persisted list as soon as Create commits.
	runs, err := h.state.List(t.Context(), 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("List after start = %v, %v", runs, err)
	}
	if _, err := h.state.Cancel(t.Context(), runs[0].ID, time.Now()); err != nil {
		t.Fatalf("Cancel running workflow: %v", err)
	}
	run := <-done
	if err := <-errs; err == nil {
		t.Fatal("Start returned nil after durable cancellation")
	}
	if run.Stop != workflow.StopAborted || run.Closed {
		t.Fatalf("run after cancellation = stop %q closed %v, want aborted/open", run.Stop, run.Closed)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("running step did not start: %v", err)
	}
	for _, row := range run.Steps {
		if row.Status == workflow.StatusPending && row.Attempt != 0 {
			t.Errorf("pending step %s was dispatched after cancellation: attempt %d", row.Step.ID, row.Attempt)
		}
	}
}

func TestSameProcessEnginesCannotDispatchOneWorkflowTwice(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	body := "touch " + marker + "\nsleep 10\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'"
	types := []config.AgentType{declared("worker", stub(t, dir, "worker", body), config.PoolAgent)}
	first := newHarnessWith(t, workflow.Options{Lanes: noCeiling()}, dir, types...)
	second := newHarnessOver(t, dir, noCeiling(), types...)
	run, _, err := first.engine.Create(t.Context(), graphOf(step("one", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approve(t, first, run.ID)
	done := make(chan error, 1)
	go func() {
		_, runErr := first.engine.Run(t.Context(), run.ID)
		done <- runErr
	}()
	waitFor(t, dir, "started")
	if _, err := second.engine.Run(t.Context(), run.ID); err == nil {
		t.Fatal("second engine with the same pid dispatched the workflow")
	}
	if _, err := first.state.Cancel(t.Context(), run.ID, time.Now()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := <-done; err == nil {
		t.Fatal("first engine returned nil after cancellation")
	}
}

func TestResumeOwnDoesNotClearAConcurrentCancellation(t *testing.T) {
	h := newHarness(t, noCeiling(), declared("worker", answers(t, t.TempDir(), "worker"), config.PoolAgent))
	run, _, err := h.engine.Create(t.Context(), graphOf(step("one", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := h.state.Cancel(t.Context(), run.ID, time.Now()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := h.state.ResumeOwn(t.Context(), run.ID, os.Getpid(), 0, workflow.StopNone); err == nil {
		t.Fatal("ResumeOwn cleared cancellation observed as StopNone")
	}
	current, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if current.Stop != workflow.StopAborted {
		t.Fatalf("stop after rejected ResumeOwn = %q, want aborted", current.Stop)
	}
}
