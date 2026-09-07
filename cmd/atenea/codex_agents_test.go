package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
