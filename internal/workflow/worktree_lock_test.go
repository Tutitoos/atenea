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

func TestEffectfulWorkflowsShareOneCanonicalWorktreeLease(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	firstDir, secondDir, agentDir := t.TempDir(), t.TempDir(), t.TempDir()
	started := filepath.Join(agentDir, "started")
	body := "touch " + started + "\nsleep 2\necho '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'"
	types := []config.AgentType{declared("writer", stub(t, agentDir, "writer", body), config.PoolAgent, contract.EffectRead, contract.EffectWrite)}
	first := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Repository: "repo", RepositoryRoot: repo}, firstDir, types...)
	second := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Repository: "repo", RepositoryRoot: filepath.Join(repo, ".")}, secondDir, types...)
	firstRun, _, err := first.engine.Create(t.Context(), graphOf(step("one", "writer", nil, contract.EffectRead, contract.EffectWrite)))
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	approve(t, first, firstRun.ID)
	done := make(chan error, 1)
	go func() {
		_, runErr := first.engine.Run(t.Context(), firstRun.ID)
		done <- runErr
	}()
	waitFor(t, agentDir, "started")
	secondRun, _, err := second.engine.Create(t.Context(), graphOf(step("two", "writer", nil, contract.EffectRead, contract.EffectWrite)))
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	approve(t, second, secondRun.ID)
	if _, err := second.engine.Run(t.Context(), secondRun.ID); err == nil || !strings.Contains(err.Error(), "worktree is busy") {
		t.Fatalf("second effectful Run = %v, want worktree lease refusal", err)
	}
	if _, err := first.state.Cancel(t.Context(), firstRun.ID, time.Now()); err != nil {
		t.Fatalf("Cancel first: %v", err)
	}
	if err := <-done; err == nil {
		t.Fatal("first Run returned nil after cancellation")
	}
	if _, err := second.engine.Run(t.Context(), secondRun.ID); err != nil {
		t.Fatalf("second Run after first released the worktree: %v", err)
	}
}
