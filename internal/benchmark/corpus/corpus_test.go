package corpus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/benchmark"
)

func testCorpusRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "benchmarks", "corpus", "v1")
}

func testManifest(t *testing.T) benchmark.Manifest {
	t.Helper()
	return benchmark.Manifest{
		SchemaVersion: benchmark.SchemaVersion,
		RunID:         "corpus-test",
		Profile:       "corpus",
		Commit:        "fixture-commit",
		Environment:   benchmark.Environment{OS: "test", Arch: "test", Go: "test"},
	}
}

func TestLoadFixedCorpusHasTwelveScenariosAndHashes(t *testing.T) {
	manifest, artifacts, err := Load(testCorpusRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "atenea-acceptance" || len(manifest.Scenarios) != 12 {
		t.Fatalf("manifest = %+v", manifest)
	}
	if len(artifacts) != 11 {
		t.Fatalf("artifact count = %d, want manifest, schema, and nine unique fixtures", len(artifacts))
	}
	for _, artifact := range artifacts {
		if len(artifact.SHA256) != 64 || artifact.Path == "" {
			t.Fatalf("invalid artifact hash = %+v", artifact)
		}
	}
	if got := artifactHash(artifacts, ManifestName); got == "" {
		t.Fatal("manifest hash is empty")
	}
}

func TestRunProducesProviderFreeInitialEvidence(t *testing.T) {
	evidence, err := Run(context.Background(), testCorpusRoot(t), testManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Counts.Total != 12 || evidence.Counts.Passed != 0 || evidence.Counts.Unsupported != 12 || evidence.Counts.Failed != 0 {
		t.Fatalf("counts = %+v", evidence.Counts)
	}
	if evidence.ManifestHash == "" || evidence.CorpusHash == "" || len(evidence.Results) != 12 {
		t.Fatalf("evidence identity = %+v", evidence)
	}
	if strings.Contains(strings.ToLower(Markdown(evidence)), "provider invoked") {
		t.Fatal("provider-free corpus claimed a provider invocation")
	}
	for _, result := range evidence.Results {
		if !result.Valid {
			t.Fatalf("scenario %s does not conform: %+v", result.ID, result)
		}
	}
}

func TestRunMarksAChangedFixtureFailed(t *testing.T) {
	root := t.TempDir()
	copyCorpus(t, testCorpusRoot(t), root)
	path := filepath.Join(root, "fixtures", "go", "repository.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "type Repository interface", "type Missing interface", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := LoadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := RunPinned(context.Background(), root, testManifest(t), snapshot.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Counts.Failed == 0 {
		t.Fatalf("changed fixture was accepted: %+v", evidence.Counts)
	}
	for _, result := range evidence.Results {
		if result.ID == "S01" {
			if result.State != Failed || result.Valid || result.Failure == "" {
				t.Fatalf("S01 result = %+v", result)
			}
			return
		}
	}
	t.Fatal("S01 result missing")
}

func TestRunUsesOneSnapshotAfterDiskChanges(t *testing.T) {
	root := t.TempDir()
	copyCorpus(t, testCorpusRoot(t), root)
	snapshot, err := LoadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "fixtures", "go", "repository.go")
	if err := os.WriteFile(path, []byte("package broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	evidence := runSnapshot(context.Background(), snapshot, testManifest(t), timeNowForTest())
	for _, result := range evidence.Results {
		if result.ID == "S01" && (result.Preflight != Passed || result.State != Unsupported) {
			t.Fatalf("snapshot was reread after disk change: %+v", result)
		}
	}
}

func TestLoadRejectsRootAndFixtureSymlinks(t *testing.T) {
	rootLink := filepath.Join(t.TempDir(), "corpus-link")
	if err := os.Symlink(testCorpusRoot(t), rootLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := LoadSnapshot(rootLink); err == nil {
		t.Fatal("symlink root was accepted")
	}
	root := t.TempDir()
	copyCorpus(t, testCorpusRoot(t), root)
	fixture := filepath.Join(root, "fixtures", "go", "repository.go")
	if err := os.Remove(fixture); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(testCorpusRoot(t), "fixtures", "go", "repository.go"), fixture); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := LoadSnapshot(root); err == nil {
		t.Fatal("symlink fixture was accepted")
	}
}

func TestManifestCannotChangeCanonicalCheck(t *testing.T) {
	root := t.TempDir()
	copyCorpus(t, testCorpusRoot(t), root)
	path := filepath.Join(root, ManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), `"check": "go_context"`, `"check": "recovery_integration"`, 1)
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(root); err == nil {
		t.Fatal("manifest changed a canonical check")
	}
}

func TestMarkdownEscapesDynamicMetadata(t *testing.T) {
	evidence := Evidence{CorpusID: "x|<script>\nattack", CorpusVersion: "v`1", Manifest: benchmark.Manifest{RunID: "run|1", Commit: "a\nb", Environment: benchmark.Environment{OS: "darwin|<", Arch: "arm64", Go: "go"}}, Results: []ScenarioResult{{ID: "S|1", Title: "title\n|<", State: Passed, AllowedStates: []State{Passed}, Valid: true, Evidence: "ok\n|<script>"}}}
	output := Markdown(evidence)
	for _, raw := range []string{"<script>", "|<", "title\n", "![", "](https://"} {
		if strings.Contains(output, raw) {
			t.Fatalf("markdown contains unescaped %q: %q", raw, output)
		}
	}
}

func TestProtocolAndWorkspaceChecksAreStructured(t *testing.T) {
	root := t.TempDir()
	copyCorpus(t, testCorpusRoot(t), root)
	protocol := filepath.Join(root, "fixtures", "protocol-2025.json")
	data, err := os.ReadFile(protocol)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protocol, []byte(strings.Replace(string(data), "2025-06-18", "2026-07-28", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := LoadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := RunPinned(context.Background(), root, testManifest(t), snapshot.Hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range evidence.Results {
		if result.ID == "S08" {
			if result.State != Failed {
				t.Fatalf("protocol mismatch was not failed: %+v", result)
			}
			return
		}
	}
	t.Fatal("S08 result missing")
}

func TestSafePathRejectsEscape(t *testing.T) {
	if _, err := safePath(t.TempDir(), "../outside"); err == nil {
		t.Fatal("path escape was accepted")
	}
	if _, err := safePath(t.TempDir(), "/absolute"); err == nil {
		t.Fatal("absolute fixture path was accepted")
	}
}

func TestRunPinnedRejectsAnEmptyPin(t *testing.T) {
	if _, err := RunPinned(context.Background(), testCorpusRoot(t), testManifest(t), ""); err == nil {
		t.Fatal("empty corpus pin was accepted")
	}
}

func TestRunPinnedReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	evidence, err := RunPinned(ctx, testCorpusRoot(t), testManifest(t), CanonicalCorpusSHA256)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if evidence.Counts.Total != 0 {
		t.Fatalf("canceled evidence counted %d results", evidence.Counts.Total)
	}
}

func TestFinalizeEvidenceCountsAPartialRun(t *testing.T) {
	evidence := Evidence{Results: []ScenarioResult{
		{State: Unsupported, Valid: true},
		{State: Failed, Valid: false},
	}}
	finalizeEvidence(&evidence)
	if evidence.Counts.Total != 2 || evidence.Counts.Unsupported != 1 || evidence.Counts.Failed != 1 {
		t.Fatalf("partial counts = %+v", evidence.Counts)
	}
	if evidence.FinishedAt.IsZero() {
		t.Fatal("partial evidence has no finished_at")
	}
}

func TestValidateManifestRejectsWrongScenarioCount(t *testing.T) {
	manifest, _, err := Load(testCorpusRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Scenarios = manifest.Scenarios[:11]
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("manifest with eleven scenarios was accepted")
	}
}

func copyCorpus(t *testing.T, source, destination string) {
	t.Helper()
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		src := filepath.Join(source, entry.Name())
		dst := filepath.Join(destination, entry.Name())
		if entry.IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			copyCorpus(t, src, dst)
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func timeNowForTest() time.Time { return time.Unix(1, 0).UTC() }
