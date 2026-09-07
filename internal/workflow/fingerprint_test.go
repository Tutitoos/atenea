package workflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestResumeRejectsChangedSourcesBeforeReusingAcceptedResults(t *testing.T) {
	dbDir, agentDir, repoDir := t.TempDir(), t.TempDir(), t.TempDir()
	source := filepath.Join(repoDir, "main.go")
	if err := os.WriteFile(source, []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	quickCount := filepath.Join(agentDir, "quick-count")
	quick := stub(t, agentDir, "quick", "echo run >> "+quickCount+"\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'")
	slow := stub(t, agentDir, "slow", "touch "+filepath.Join(agentDir, "slow-started")+"\nsleep 10\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'")
	h := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Repository: "repo", RepositoryRoot: repoDir}, dbDir,
		declared("quick", quick, config.PoolAgent), declared("slow", slow, config.PoolAgent))
	done := make(chan struct{})
	go func() {
		_, _ = h.engine.Start(t.Context(), graphOf(step("quick", "quick", nil), step("slow", "slow", []string{"quick"})))
		close(done)
	}()
	waitFor(t, agentDir, "slow-started")
	runs, err := h.state.List(t.Context(), 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("List = %v, %v", runs, err)
	}
	if _, err := h.state.Cancel(t.Context(), runs[0].ID, time.Now()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	<-done
	run, err := h.state.Load(t.Context(), runs[0].ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stepOf(t, run, "quick").Status != workflow.StatusOK {
		t.Fatalf("quick status = %s, want accepted before resume", stepOf(t, run, "quick").Status)
	}
	before, err := os.ReadFile(quickCount)
	if err != nil {
		t.Fatalf("read quick count: %v", err)
	}
	if err := os.WriteFile(source, []byte("package main\n\nvar changed = true\n"), 0o600); err != nil {
		t.Fatalf("change source: %v", err)
	}
	if _, err := h.engine.Resume(t.Context(), run.ID, nil); err == nil || !strings.Contains(err.Error(), "source state changed") {
		t.Fatalf("Resume after source change = %v, want source-state refusal", err)
	}
	afterRun, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load after refused resume: %v", err)
	}
	if afterRun.Stop != workflow.StopAborted {
		t.Fatalf("stop after refused resume = %q, want durable aborted", afterRun.Stop)
	}
	after, err := os.ReadFile(quickCount)
	if err != nil {
		t.Fatalf("read quick count after refusal: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("accepted quick effect was repeated after source change: before %q after %q", before, after)
	}
}

func TestExplicitRedoRebasesAChangedInterruptedWrite(t *testing.T) {
	dbDir, agentDir, repoDir := t.TempDir(), t.TempDir(), t.TempDir()
	source := filepath.Join(repoDir, "main.go")
	if err := os.WriteFile(source, []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	writer := stub(t, agentDir, "writer", "echo '// partial write' >> "+source+"\ntouch "+filepath.Join(agentDir, "writer-started")+"\nsleep 2\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'")
	h := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Repository: "repo", RepositoryRoot: repoDir}, dbDir,
		declared("writer", writer, config.PoolAgent, contract.EffectRead, contract.EffectWrite))
	done := make(chan struct{})
	go func() {
		_, _ = h.engine.Start(t.Context(), graphOf(step("writer", "writer", nil, contract.EffectRead, contract.EffectWrite)))
		close(done)
	}()
	waitFor(t, agentDir, "writer-started")
	runs, err := h.state.List(t.Context(), 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("List = %v, %v", runs, err)
	}
	if _, err := h.state.Cancel(t.Context(), runs[0].ID, time.Now()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	<-done
	_, err = h.engine.Resume(t.Context(), runs[0].ID, nil)
	if err == nil || !strings.Contains(err.Error(), "source state changed") {
		t.Fatalf("Resume without explicit redo = %v, want source-state refusal", err)
	}
	run, err := h.engine.Resume(t.Context(), runs[0].ID, []string{"writer"})
	if err != nil {
		t.Fatalf("Resume with explicit redo: %v", err)
	}
	if stepOf(t, run, "writer").Status != workflow.StatusOK {
		t.Fatalf("writer status after explicit redo = %s, want ok", stepOf(t, run, "writer").Status)
	}
}

func TestResumeRejectsChangedSourcesAfterAnInterruptedStep(t *testing.T) {
	dbDir, agentDir, repoDir := t.TempDir(), t.TempDir(), t.TempDir()
	source := filepath.Join(repoDir, "main.go")
	if err := os.WriteFile(source, []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	writer := stub(t, agentDir, "writer", "touch "+filepath.Join(agentDir, "writer-started")+"\nsleep 10\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'")
	h := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Repository: "repo", RepositoryRoot: repoDir}, dbDir,
		declared("writer", writer, config.PoolAgent, contract.EffectRead, contract.EffectWrite))
	done := make(chan struct{})
	go func() {
		_, _ = h.engine.Start(t.Context(), graphOf(step("writer", "writer", nil, contract.EffectRead, contract.EffectWrite)))
		close(done)
	}()
	waitFor(t, agentDir, "writer-started")
	runs, err := h.state.List(t.Context(), 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("List = %v, %v", runs, err)
	}
	if _, err := h.state.Cancel(t.Context(), runs[0].ID, time.Now()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	<-done
	if err := os.WriteFile(source, []byte("package main\n\nvar changed = true\n"), 0o600); err != nil {
		t.Fatalf("change source: %v", err)
	}
	if _, err := h.engine.Resume(t.Context(), runs[0].ID, nil); err == nil || !strings.Contains(err.Error(), "source state changed") {
		t.Fatalf("Resume after interrupted source change = %v, want source-state refusal", err)
	}
}
