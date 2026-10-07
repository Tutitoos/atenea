package decisioncapture

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/buildinfo"
)

func TestCaptureDefaultOffAndPrivateDeduplicated(t *testing.T) {
	t.Setenv(EnvDir, "")
	if err := Capture("mcp.decision.plan", "repo", "hello", "plan"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvDir, dir)
	objective := "Revisa https://example.org/client?token=abc con alice@example.org en /Users/alice/private y password=hunter2"
	for i := 0; i < 2; i++ {
		if err := Capture("mcp.decision.plan", "repo", objective, "plan"); err != nil {
			t.Fatal(err)
		}
	}
	name := filepath.Join(dir, "candidates.private.jsonl")
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("missing record")
	}
	var record Record
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if scanner.Scan() {
		t.Fatal("duplicate record")
	}
	if record.Source != "mcp.decision.plan" || record.ReviewState != "automatic_unreviewed" {
		t.Fatalf("wrong provenance: %+v", record)
	}
	if revision, modified := buildinfo.Source(); revision != "" && !modified && record.Revision != revision {
		t.Fatalf("captured revision = %q, want clean build %q", record.Revision, revision)
	}
	for _, private := range []string{"alice", "example.org", "hunter2", "/Users"} {
		if strings.Contains(record.Objective, private) {
			t.Fatalf("private token retained: %s", private)
		}
	}
}

func TestCaptureRejectsUnsafeStorage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvDir, dir)
	name := filepath.Join(dir, "candidates.private.jsonl")
	if err := os.WriteFile(name, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Capture("mcp.decision.plan", "repo", "hello", "plan"); err == nil || err.Error() != "capture file must be private" {
		t.Fatalf("public file was not rejected for its permissions: %v", err)
	}
	if content, err := os.ReadFile(name); err != nil || string(content) != "old" {
		t.Fatal("modified unsafe file")
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "target"), name); err != nil {
		t.Fatal(err)
	}
	if err := Capture("mcp.decision.plan", "repo", "hello", "plan"); err == nil || err.Error() != "capture file must be private" {
		t.Fatalf("symlink was not rejected as an unsafe file: %v", err)
	}
}

func TestRedactCommonCredentialForms(t *testing.T) {
	redacted := Redact("Bearer abcdefghijklmnop sk-abcdefghijklmnop ghp_abcdefghijklmnop")
	for _, secret := range []string{"abcdefghijklmnop", "Bearer", "sk-", "ghp_"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("credential fragment retained: %s", secret)
		}
	}
}
