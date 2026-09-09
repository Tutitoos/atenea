package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanModeSkillSyncIsDeterministicAndDiscoverable(t *testing.T) {
	dir := t.TempDir()
	first, err := SyncSkills(SkillSyncOptions{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || first.Digest == "" {
		t.Fatalf("first sync = %+v", first)
	}
	path := filepath.Join(dir, "atenea-plan-mode", "SKILL.md")
	data := mustRead(t, path)
	for _, want := range []string{
		"name: atenea-plan-mode",
		"current Codex collaboration mode is Plan",
		"The mode signal, not words such as \"plan\"",
		"`decision.plan` once",
		"Do not edit files",
		"Do not substitute a shell command containing untrusted user text",
	} {
		if !strings.Contains(data, want) {
			t.Fatalf("SKILL.md does not contain %q", want)
		}
	}
	if mode := mustMode(t, path).Perm(); mode != 0o600 {
		t.Fatalf("mode = %o", mode)
	}
	second, err := SyncSkills(SkillSyncOptions{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed || !second.Matches || second.Digest != first.Digest {
		t.Fatalf("second sync = %+v", second)
	}
	check, err := CheckSkills(dir, nil)
	if err != nil || !check.Matches || check.Changed {
		t.Fatalf("check = %+v, err = %v", check, err)
	}
}

func TestPlanModeSkillSyncPreservesForeignAndTamperedFiles(t *testing.T) {
	dir := t.TempDir()
	canonicalDir := filepath.Join(dir, "atenea-plan-mode")
	if err := os.MkdirAll(canonicalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(canonicalDir, "SKILL.md")
	foreign := "---\nname: atenea-plan-mode\ndescription: foreign\n---\n"
	if err := os.WriteFile(path, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := SyncSkills(SkillSyncOptions{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 1 || mustRead(t, path) != foreign {
		t.Fatalf("foreign skill was not preserved: %+v", report)
	}

	tamperedDir := t.TempDir()
	if _, err := SyncSkills(SkillSyncOptions{Path: tamperedDir}); err != nil {
		t.Fatal(err)
	}
	tamperedPath := filepath.Join(tamperedDir, "atenea-plan-mode", "SKILL.md")
	tampered := strings.Replace(mustRead(t, tamperedPath), "Do not edit files", "Edit files", 1)
	if err := os.WriteFile(tamperedPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	tamperedReport, err := SyncSkills(SkillSyncOptions{Path: tamperedDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(tamperedReport.Skipped) != 1 || mustRead(t, tamperedPath) != tampered {
		t.Fatalf("tampered skill was overwritten: %+v", tamperedReport)
	}
}

func TestPlanModeSkillPrunesOnlyOwnedSkillFile(t *testing.T) {
	dir := t.TempDir()
	obsolete := Skill{Name: "atenea-old-plan", Description: "Old managed skill.", Body: "# Old\n\nManaged content.\n"}
	if _, err := SyncSkills(SkillSyncOptions{Path: dir, Skills: []Skill{obsolete}}); err != nil {
		t.Fatal(err)
	}
	obsoleteDir := filepath.Join(dir, obsolete.Name)
	foreignResource := filepath.Join(obsoleteDir, "notes.txt")
	if err := os.WriteFile(foreignResource, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := SyncSkills(SkillSyncOptions{Path: dir, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Pruned) != 1 || report.Pruned[0] != obsolete.Name {
		t.Fatalf("prune = %+v", report)
	}
	if _, err := os.Stat(filepath.Join(obsoleteDir, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("obsolete managed file remains: %v", err)
	}
	if got := mustRead(t, foreignResource); got != "keep" {
		t.Fatalf("foreign resource = %q", got)
	}
}

func TestPlanModeSkillCheckDetectsDriftWithoutChangingIt(t *testing.T) {
	dir := t.TempDir()
	if _, err := SyncSkills(SkillSyncOptions{Path: dir}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "atenea-plan-mode", "SKILL.md")
	drift := strings.Replace(mustRead(t, path), "Plan mode", "Planning mode", 1)
	if err := os.WriteFile(path, []byte(drift), 0o600); err != nil {
		t.Fatal(err)
	}
	check, err := CheckSkills(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if check.Matches || !check.Changed || mustRead(t, path) != drift {
		t.Fatalf("check = %+v", check)
	}
}

func TestPlanModeSkillSyncDoesNotFollowSkillDirectorySymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(dir, "atenea-plan-mode")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	report, err := SyncSkills(SkillSyncOptions{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 1 {
		t.Fatalf("symlink report = %+v", report)
	}
	if _, err := os.Stat(filepath.Join(outside, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("sync followed the skill directory symlink: %v", err)
	}
}
