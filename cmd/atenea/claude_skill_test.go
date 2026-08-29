package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSkillInstallIsIdempotentAndReplaceable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	changed, err := installClaudeSkill(false)
	if err != nil || !changed {
		t.Fatalf("first install = changed %v, err %v", changed, err)
	}
	path, err := claudeSkillPath()
	if err != nil {
		t.Fatalf("skill path: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(contents), "name: atenea") {
		t.Fatalf("installed skill = %q, err %v", contents, err)
	}

	changed, err = installClaudeSkill(false)
	if err != nil || changed {
		t.Fatalf("idempotent install = changed %v, err %v", changed, err)
	}
	if err := os.WriteFile(path, []byte("user skill"), 0o600); err != nil {
		t.Fatalf("replace fixture: %v", err)
	}
	if _, err := installClaudeSkill(false); err == nil {
		t.Fatal("foreign skill was overwritten without replace")
	}
	changed, err = installClaudeSkill(true)
	if err != nil || !changed {
		t.Fatalf("replace install = changed %v, err %v", changed, err)
	}
	if err := removeClaudeSkill(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := removeClaudeSkill(); err != nil {
		t.Fatalf("remove absent skill: %v", err)
	}
}

func TestRemoveClaudeSkillRefusesForeignFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := claudeSkillPath()
	if err != nil {
		t.Fatalf("skill path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("skill directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("user skill"), 0o600); err != nil {
		t.Fatalf("foreign fixture: %v", err)
	}
	if err := removeClaudeSkill(); err == nil {
		t.Fatal("foreign skill was removed")
	}
}
