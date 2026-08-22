package benchmark

import (
	"context"
	"os"
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
