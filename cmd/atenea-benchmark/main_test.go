package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/benchmark"
)

func TestParseBenchmarkOutput(t *testing.T) {
	raw := "BenchmarkExample-10 123 456.7 ns/op 8192 B/op 12 allocs/op\n"
	ns, bytesOp, allocsOp, ok := parseBenchmarkOutput(raw)
	if !ok || ns != 456.7 || bytesOp != 8192 || allocsOp != 12 {
		t.Fatalf("parsed benchmark = %v %v %v %v", ns, bytesOp, allocsOp, ok)
	}
}

func TestParseBenchmarkOutputRejectsMissingMetric(t *testing.T) {
	if _, _, _, ok := parseBenchmarkOutput("PASS\n"); ok {
		t.Fatal("missing benchmark output was accepted")
	}
}

func TestCoverageAndUtilityHelpers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.out")
	data := "mode: set\ngithub.com/Tutitoos/atenea/internal/benchmark/benchmark.go:1.1,2.2 2 1\ngithub.com/Tutitoos/atenea/internal/benchmark/benchmark.go:3.1,4.2 3 0\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	coverage, packages, err := readCoverage(path)
	if err != nil || coverage != 40 || packages["github.com/Tutitoos/atenea/internal/benchmark"] != 40 {
		t.Fatalf("coverage = %v, packages = %#v, err = %v", coverage, packages, err)
	}
	if got := packageFromProfilePath("github.com/Tutitoos/atenea/internal/benchmark/benchmark.go"); got != "github.com/Tutitoos/atenea/internal/benchmark" {
		t.Fatalf("package = %q", got)
	}
	if got := scale([]float64{2, 4}, 0.5); got[0] != 1 || got[1] != 2 {
		t.Fatalf("scale = %v", got)
	}
	suites := []benchmark.TestSuite{{Package: "z"}, {Package: "a"}}
	sortSuites(suites)
	if suites[0].Package != "a" {
		t.Fatalf("suites = %v", suites)
	}
	t.Setenv("TMPDIR", "/var/folders/very-long-path")
	if got := benchmarkEnvironment(); gotEnv(got, "TMPDIR") != "/tmp" {
		t.Fatalf("benchmark environment = %v", gotEnv(got, "TMPDIR"))
	}
	if sourceState(true) != "DIRTY" || sourceState(false) != "CLEAN" {
		t.Fatal("source state helper returned unexpected value")
	}
}

func gotEnv(env []string, key string) string {
	prefix := key + "="
	for _, value := range env {
		if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
			return value[len(prefix):]
		}
	}
	return ""
}

func TestRunTestsAndBenchmarksProduceEvidence(t *testing.T) {
	output := t.TempDir()
	if err := os.MkdirAll(filepath.Join(output, "raw"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := runTestsForPackages(context.Background(), output, "./internal/benchmark")
	if run.Err != nil || run.Totals.Passed == 0 || run.Coverage <= 0 {
		t.Fatalf("test run = %+v", run)
	}
	results := runBenchmarks(context.Background(), output, 1, "quick")
	if len(results) != 5 {
		t.Fatalf("benchmark count = %d", len(results))
	}
	for _, result := range results {
		if !result.Valid || result.NanosecondsOp <= 0 {
			t.Fatalf("invalid benchmark result = %+v", result)
		}
	}
}

func TestDefaultWrappersAreConfigurableForFocusedRuns(t *testing.T) {
	output := t.TempDir()
	oldPackages := testPackages
	testPackages = []string{"./internal/benchmark"}
	t.Cleanup(func() { testPackages = oldPackages })
	run := runTests(context.Background(), output)
	if run.Err != nil || run.Totals.Passed == 0 {
		t.Fatalf("wrapped test run = %+v", run)
	}

	oldRoot := docsRoot
	docsRoot = t.TempDir()
	t.Cleanup(func() { docsRoot = oldRoot })
	if err := renderDocs(benchmark.Summary{Manifest: benchmark.Manifest{RunID: "run", Profile: "quick"}}); err != nil {
		t.Fatalf("wrapped docs render: %v", err)
	}
}

func TestReportsDocsAndBaseline(t *testing.T) {
	manifest := benchmark.Manifest{SchemaVersion: benchmark.SchemaVersion, RunID: "run-1", Profile: "quick", Commit: "same", Environment: benchmark.Environment{OS: "darwin", Arch: "arm64", Go: "go1.26.7"}}
	result := benchmark.BenchmarkResult{Name: "sample", Category: "micro", Package: "./sample", Command: "go test", Valid: true, Samples: 1, NanosecondsOp: 100, Throughput: 1e7, BytesOp: 1, AllocsOp: 1, LatencyMS: benchmark.Distribution{Samples: 1, Median: 0.1}, Status: benchmark.Green}
	previous := benchmark.Summary{Manifest: manifest, Benchmarks: []benchmark.BenchmarkResult{result}}
	output := t.TempDir()
	if err := benchmark.WriteJSON(filepath.Join(output, "summary.json"), previous); err != nil {
		t.Fatal(err)
	}
	result.NanosecondsOp = 110
	results := []benchmark.BenchmarkResult{result}
	applyBaseline(results, output, manifest)
	if results[0].Baseline != "run-1" || results[0].DeltaPercent != 10 {
		t.Fatalf("baseline result = %+v", results[0])
	}
	if err := writeReport(filepath.Join(output, "summary.md"), previous); err != nil {
		t.Fatal(err)
	}
	if err := renderDocsAt(previous, output); err != nil {
		t.Fatal(err)
	}
	dirtyResults := []benchmark.BenchmarkResult{result}
	dirtyManifest := manifest
	dirtyManifest.SourceDirty = true
	applyBaseline(dirtyResults, output, dirtyManifest)
	if dirtyResults[0].Baseline != "" {
		t.Fatalf("dirty baseline was applied: %+v", dirtyResults[0])
	}
	for _, path := range []string{"summary.md", "docs/content/benchmarks/_index.md", "docs/data/benchmarks/latest.json"} {
		if _, err := os.Stat(filepath.Join(output, path)); err != nil {
			t.Fatalf("missing report %s: %v", path, err)
		}
	}
}
