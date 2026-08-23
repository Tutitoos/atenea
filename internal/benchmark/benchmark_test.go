package benchmark

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDistributionOfReportsAdvancedStatistics(t *testing.T) {
	got := DistributionOf([]float64{1, 2, 3, 4, 5})
	if got.P50 != 3 || got.P95 != 4.8 || got.Min != 1 || got.Max != 5 {
		t.Fatalf("distribution = %+v", got)
	}
	if got.CoefficientVar <= 0 {
		t.Fatalf("coefficient of variation = %v, want positive", got.CoefficientVar)
	}
}

func TestSuiteStatusUsesCoverageAndFailures(t *testing.T) {
	base := TestSuite{Executed: 10, Passed: 10, CoveragePercent: 82}
	if got := SuiteStatus(base, 60, 80); got != Green {
		t.Fatalf("green suite = %s", got)
	}
	base.CoveragePercent = 72
	if got := SuiteStatus(base, 60, 80); got != Orange {
		t.Fatalf("orange suite = %s", got)
	}
	base.Failed = 1
	if got := SuiteStatus(base, 60, 80); got != Red {
		t.Fatalf("red suite = %s", got)
	}
}

func TestCollectEnvironmentDoesNotExposeHardwareIdentity(t *testing.T) {
	env := CollectEnvironment(context.Background())
	if env.OS == "" || env.Arch == "" || env.Go == "" {
		t.Fatalf("environment = %+v", env)
	}
}

func TestPassRateWithNoExecutedTestsIsZero(t *testing.T) {
	if got := PassRate(0, 0); got != 0 {
		t.Fatalf("pass rate = %v", got)
	}
}

func TestDistributionOfEmptyValuesIsZero(t *testing.T) {
	if got := DistributionOf(nil); got.Samples != 0 || got.Mean != 0 {
		t.Fatalf("empty distribution = %+v", got)
	}
}

func TestOverallStatusPropagatesRedAndOrange(t *testing.T) {
	summary := Summary{CoveragePercent: 85, CoverageFloor: 60, CoverageTarget: 80}
	summary.Suites = []TestSuite{{Status: Orange}}
	if got := OverallStatus(summary); got != Orange {
		t.Fatalf("orange overall = %s", got)
	}
	summary.Suites = []TestSuite{{Status: Red}}
	if got := OverallStatus(summary); got != Red {
		t.Fatalf("red overall = %s", got)
	}
}

func TestWriteJSONAndTextPublishArtifacts(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "nested", "summary.json")
	if err := WriteJSON(jsonPath, map[string]int{"ok": 1}); err != nil {
		t.Fatal(err)
	}
	if err := WriteText(filepath.Join(dir, "nested", "report.md"), "report\n"); err != nil {
		t.Fatal(err)
	}
}

func TestUsageAcceptsNilProcessState(t *testing.T) {
	if got := Usage((*os.ProcessState)(nil)); got.RSSBytes != 0 || got.CPUTimeMS != 0 {
		t.Fatalf("nil process usage = %+v", got)
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if got := Usage(cmd.ProcessState); got.CPUTimeMS < 0 {
		t.Fatalf("process usage = %+v", got)
	}
}

func TestAtomicWriteReportsPublishFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing-dir")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteText(path, "data"); err == nil {
		t.Fatal("writing over a directory succeeded")
	}
}

func TestStatusManifestAndValidationBranches(t *testing.T) {
	for _, status := range []Status{Green, Orange, Red} {
		if status.Indicator() == "" {
			t.Fatalf("empty indicator for %s", status)
		}
	}
	manifest := NewManifest(context.Background(), "quick", "test")
	if manifest.SchemaVersion != SchemaVersion || manifest.Profile != "quick" || manifest.Environment.OS == "" {
		t.Fatalf("manifest = %+v", manifest)
	}
	commit, _ := GitState(context.Background())
	if commit == "" {
		t.Fatal("git state did not capture a commit")
	}
	base := Summary{
		Manifest: Manifest{SchemaVersion: SchemaVersion, RunID: "run", Profile: "quick", Environment: Environment{OS: "darwin", Arch: "arm64", Go: "go1.26.7"}},
		Tests:    TestTotals{Discovered: 1, Executed: 1, Passed: 1}, CoveragePercent: 80, CoverageFloor: 60, CoverageTarget: 80, OverallStatus: Green,
	}
	cases := []func(*Summary){
		func(s *Summary) { s.Manifest.SchemaVersion = 99 },
		func(s *Summary) { s.Manifest.RunID = "" },
		func(s *Summary) { s.Manifest.Environment.OS = "" },
		func(s *Summary) { s.CoveragePercent = 101 },
		func(s *Summary) { s.Tests.Discovered = 0 },
		func(s *Summary) { s.OverallStatus = Status("UNKNOWN") },
		func(s *Summary) { s.Suites = []TestSuite{{Package: "pkg", Passed: 1, Executed: 2, Status: Green}} },
		func(s *Summary) {
			s.Benchmarks = []BenchmarkResult{{Name: "bad", Package: "pkg", Status: Green, Valid: false}}
		},
	}
	for index, mutate := range cases {
		candidate := base
		mutate(&candidate)
		if err := ValidateSummary(candidate); err == nil {
			t.Errorf("case %d was accepted", index)
		}
	}
}

func TestPortableCommandAndStatusBranches(t *testing.T) {
	if got := commandOutput(context.Background(), "true"); got != "" {
		t.Fatalf("empty command output = %q", got)
	}
	if got := commandOutput(context.Background(), "sh", "-c", "printf portable"); got != "portable" {
		t.Fatalf("command output = %q", got)
	}
	if got := commandOutput(context.Background(), "sh", "-c", "exit 1"); got != "" {
		t.Fatalf("failed command output = %q", got)
	}
	if valueAfter("no matching label", "Missing:") != "" {
		t.Fatal("missing label produced a value")
	}
	if got := OverallStatus(Summary{CoveragePercent: 50, CoverageFloor: 60, CoverageTarget: 80}); got != Red {
		t.Fatalf("floor status = %s", got)
	}
	if got := OverallStatus(Summary{CoveragePercent: 70, CoverageFloor: 60, CoverageTarget: 80}); got != Orange {
		t.Fatalf("target status = %s", got)
	}
}

func TestValidateSummary(t *testing.T) {
	summary := Summary{
		Manifest: Manifest{SchemaVersion: SchemaVersion, RunID: "run", Profile: "quick", Environment: Environment{OS: "darwin", Arch: "arm64", Go: "go1.26.7"}},
		Tests:    TestTotals{Discovered: 2, Executed: 2, Passed: 2}, CoveragePercent: 80, CoverageFloor: 60, CoverageTarget: 80,
		OverallStatus: Green,
	}
	if err := ValidateSummary(summary); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}
	summary.Tests.Passed = 1
	if err := ValidateSummary(summary); err == nil {
		t.Fatal("inconsistent totals accepted")
	}
}
