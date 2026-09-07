package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/benchmark/corpus"
	"github.com/Tutitoos/atenea/internal/dartcoverage"
)

func TestGenerateWritesStableReportWithoutAbsolutePaths(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repository := filepath.Join(filepath.Dir(file), "..", "..")
	output := filepath.Join(t.TempDir(), "report.json")
	if err := generate(runOptions{
		FixtureRoot: filepath.Join(repository, "internal", "dartcoverage", "testdata"),
		CorpusRoot:  filepath.Join(repository, "benchmarks", "corpus", "v1"),
		CorpusHash:  corpus.CanonicalCorpusSHA256,
		Date:        "2026-09-06",
		Output:      output,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), repository) || strings.Contains(string(data), "/Users/") {
		t.Fatal("report leaked a local absolute path")
	}
	var report dartcoverage.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimSuffix(output, ".json") + ".md"); err != nil {
		t.Fatal(err)
	}
}
