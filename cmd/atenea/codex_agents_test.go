package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adaptercodex "github.com/Tutitoos/atenea/internal/adapter/codex"
)

type failAfterWriter struct {
	writes int
	failAt int
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.writes >= w.failAt {
		return 0, errors.New("write failed")
	}
	w.writes++
	return len(p), nil
}

func TestPrintCodexSyncReportPropagatesEveryWriteFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		report adaptercodex.SyncReport
		failAt int
	}{
		{name: "header", failAt: 0},
		{name: "pruned", report: adaptercodex.SyncReport{Pruned: []string{"old"}}, failAt: 1},
		{name: "skipped", report: adaptercodex.SyncReport{Skipped: []string{"foreign"}}, failAt: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := printCodexSyncReport("Codex agents", test.report, false, &failAfterWriter{failAt: test.failAt}); err == nil {
				t.Fatal("write failure was ignored")
			}
		})
	}
}

func TestCodexPlanModeCLIUsesGlobalAndProjectSkillDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-fixture"))
	var output bytes.Buffer
	if err := run([]string{"codex", "plan-mode", "sync", "--global"}, &output); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, "codex-fixture", "skills", "atenea-plan-mode", "SKILL.md")
	if _, err := os.Stat(global); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Codex Plan mode:") {
		t.Fatalf("sync output = %s", output.String())
	}
	project := t.TempDir()
	output.Reset()
	if err := run([]string{"codex", "plan-mode", "sync", "--project", project}, &output); err != nil {
		t.Fatal(err)
	}
	projectSkill := filepath.Join(project, ".agents", "skills", "atenea-plan-mode", "SKILL.md")
	if _, err := os.Stat(projectSkill); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"codex", "plan-mode", "check", "--project", project, "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"matches":true`) {
		t.Fatalf("check output = %s", output.String())
	}
}

func TestCodexPlanModeCheckDefaultsToGlobalAndDoesNotMutate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-fixture"))
	err := run([]string{"codex", "plan-mode", "check"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "out of date") {
		t.Fatalf("check error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "codex-fixture", "skills")); !os.IsNotExist(statErr) {
		t.Fatalf("check mutated the skills directory: %v", statErr)
	}
}

func TestCodexAgentsCLIUsesFixtureHomesAndProjectDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-fixture"))
	var output bytes.Buffer
	if err := run([]string{"codex", "agents", "sync", "--global"}, &output); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, "codex-fixture", "agents")
	if _, err := os.Stat(filepath.Join(global, "atenea-implement.toml")); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	output.Reset()
	if err := run([]string{"codex", "agents", "sync", "--project", project}, &output); err != nil {
		t.Fatal(err)
	}
	projectAgents := filepath.Join(project, ".codex", "agents")
	if _, err := os.Stat(filepath.Join(projectAgents, "atenea-audit.toml")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"codex", "agents", "check", "--project", project, "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"matches":true`) {
		t.Fatalf("check output = %s", output.String())
	}
}

func TestCodexAgentsCLIPreservesForeignFile(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, ".codex", "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "atenea-research.toml")
	original := "[foreign]\nmodel = \"do-not-touch\"\n"
	if err := os.WriteFile(foreign, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"codex", "agents", "sync", "--project", project}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatal("foreign profile was overwritten")
	}
}
