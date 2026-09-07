package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestCodexProfileSyncUsesManagedAgentDirectory(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "foreign.toml")
	if err := os.WriteFile(foreign, []byte("[foreign]\nvalue = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := SyncAgentProfiles(SyncOptions{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || first.Digest == "" {
		t.Fatalf("first sync = %+v", first)
	}
	if got, _ := os.ReadFile(foreign); string(got) != "[foreign]\nvalue = true\n" {
		t.Fatal("foreign file changed")
	}
	for _, profile := range CanonicalAgentProfiles() {
		path := filepath.Join(dir, "atenea-"+profile.Name+".toml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), managedBegin+" digest="+first.Digest) {
			t.Fatalf("missing marker in %s", path)
		}
		if mode := mustMode(t, path).Perm(); mode != 0o600 {
			t.Fatalf("mode = %o", mode)
		}
	}
	second, err := SyncAgentProfiles(SyncOptions{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed || !second.Matches {
		t.Fatalf("second sync = %+v", second)
	}
}

func TestCodexProfileSyncDoesNotOverwriteForeignManagedNameAndPrunesOnlyManaged(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "atenea-research.toml")
	if err := os.WriteFile(foreign, []byte("[foreign]\nvalue = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	obsolete := filepath.Join(dir, "atenea-old.toml")
	old := AgentProfile{Name: "old", Description: "old description", DeveloperInstructions: "Do not delegate.", Model: "old-model", ReasoningEffort: "medium", SandboxMode: "read-only"}
	if err := os.WriteFile(obsolete, []byte(renderProfileFile(old, "old-global")), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := SyncAgentProfiles(SyncOptions{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 1 || mustRead(t, foreign) != "[foreign]\nvalue = true\n" {
		t.Fatalf("foreign handling = %+v", report)
	}
	if _, err := os.Stat(obsolete); err != nil {
		t.Fatal("obsolete was removed without prune")
	}
	prune, err := SyncAgentProfiles(SyncOptions{Path: dir, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(prune.Pruned) != 1 {
		t.Fatalf("prune = %+v", prune)
	}
	if _, err := os.Stat(obsolete); !os.IsNotExist(err) {
		t.Fatalf("obsolete still exists: %v", err)
	}
}

func TestCodexProfileCheckAndDigestAreDeterministic(t *testing.T) {
	dir := t.TempDir()
	profiles := CanonicalAgentProfiles()
	digest, err := AgentProfilesDigest(profiles)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(profiles)-1; i < j; i, j = i+1, j-1 {
		profiles[i], profiles[j] = profiles[j], profiles[i]
	}
	if other, _ := AgentProfilesDigest(profiles); other != digest {
		t.Fatalf("digest changed with order")
	}
	if _, err := SyncAgentProfiles(SyncOptions{Path: dir}); err != nil {
		t.Fatal(err)
	}
	check, err := CheckAgentProfiles(dir, CanonicalAgentProfiles())
	if err != nil {
		t.Fatal(err)
	}
	if !check.Matches || check.Changed {
		t.Fatalf("check = %+v", check)
	}
}

func TestCodexProfileSyncNeverOverwritesTamperedOrForgedMarker(t *testing.T) {
	dir := t.TempDir()
	if _, err := SyncAgentProfiles(SyncOptions{Path: dir}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "atenea-research.toml")
	original := mustRead(t, path)
	if err := os.WriteFile(path, []byte(strings.Replace(original, "model = \"gpt-5.6-sol\"", "model = \"attacker-model\"", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := SyncAgentProfiles(SyncOptions{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 1 || mustRead(t, path) == original {
		t.Fatalf("tampered file was overwritten: report=%+v", report)
	}
	foreign := filepath.Join(dir, "atenea-review.toml")
	if err := os.WriteFile(foreign, []byte("[foreign]\nvalue = \""+managedBegin+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncAgentProfiles(SyncOptions{Path: dir, Prune: true}); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, foreign); !strings.Contains(got, managedBegin) {
		t.Fatal("forged marker file was modified")
	}
}

func TestCanonicalProfilesUseCodexTopLevelAgentSchema(t *testing.T) {
	type agentFile struct {
		Name                  string `toml:"name"`
		Description           string `toml:"description"`
		DeveloperInstructions string `toml:"developer_instructions"`
		Model                 string `toml:"model"`
		ReasoningEffort       string `toml:"model_reasoning_effort"`
		SandboxMode           string `toml:"sandbox_mode"`
	}
	dir := t.TempDir()
	if _, err := SyncAgentProfiles(SyncOptions{Path: dir}); err != nil {
		t.Fatal(err)
	}
	for _, profile := range CanonicalAgentProfiles() {
		path := filepath.Join(dir, "atenea-"+profile.Name+".toml")
		var parsed agentFile
		if _, err := toml.DecodeFile(path, &parsed); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if parsed.Name != profile.Name || parsed.Description != profile.Description || parsed.DeveloperInstructions != profile.DeveloperInstructions || parsed.Model != profile.Model || parsed.ReasoningEffort != profile.ReasoningEffort || parsed.SandboxMode != profile.SandboxMode {
			t.Fatalf("profile %s = %+v, want %+v", profile.Name, parsed, profile)
		}
		if !strings.Contains(strings.ToLower(parsed.DeveloperInstructions), "do not delegate") {
			t.Fatalf("profile %s allows delegation", profile.Name)
		}
		if profile.Name == "implement" && parsed.SandboxMode != "workspace-write" {
			t.Fatalf("implement sandbox = %q", parsed.SandboxMode)
		}
		if profile.Name != "implement" && parsed.SandboxMode != "read-only" {
			t.Fatalf("%s sandbox = %q", profile.Name, parsed.SandboxMode)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func mustMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}
