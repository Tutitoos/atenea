package benchmark

import (
	"fmt"
	"math"
)

// ValidateSummary checks the invariants needed for a report to be published.
// It deliberately stays dependency-free so CI can validate artifacts anywhere
// the Go toolchain is available.
func ValidateSummary(summary Summary) error {
	if summary.Manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", summary.Manifest.SchemaVersion)
	}
	if summary.Manifest.RunID == "" || summary.Manifest.Profile == "" {
		return fmt.Errorf("manifest run_id and profile are required")
	}
	if summary.Manifest.Environment.OS == "" || summary.Manifest.Environment.Arch == "" || summary.Manifest.Environment.Go == "" {
		return fmt.Errorf("manifest environment is incomplete")
	}
	if !percentage(summary.CoveragePercent) || !percentage(summary.CoverageFloor) || !percentage(summary.CoverageTarget) {
		return fmt.Errorf("coverage percentages must be finite values between 0 and 100")
	}
	if summary.Tests.Discovered < summary.Tests.Executed+summary.Tests.Skipped || summary.Tests.Passed+summary.Tests.Failed != summary.Tests.Executed {
		return fmt.Errorf("test totals do not reconcile with discovered/executed counts")
	}
	if !validStatus(summary.OverallStatus) {
		return fmt.Errorf("invalid overall status %q", summary.OverallStatus)
	}
	for _, suite := range summary.Suites {
		if suite.Package == "" || !validStatus(suite.Status) || !percentage(suite.CoveragePercent) || !percentage(suite.PassRate) {
			return fmt.Errorf("invalid test suite %q", suite.Package)
		}
		if suite.Passed+suite.Failed != suite.Executed {
			return fmt.Errorf("suite %q totals do not reconcile", suite.Package)
		}
	}
	for _, result := range summary.Benchmarks {
		if result.Name == "" || result.Package == "" || !validStatus(result.Status) {
			return fmt.Errorf("invalid benchmark metadata")
		}
		if result.Valid {
			if result.Samples < 1 || result.NanosecondsOp <= 0 || result.Throughput <= 0 || !finite(result.BytesOp) || !finite(result.AllocsOp) {
				return fmt.Errorf("valid benchmark %q has incomplete measurements", result.Name)
			}
			if result.LatencyMS.Samples != result.Samples {
				return fmt.Errorf("benchmark %q distribution sample count mismatch", result.Name)
			}
		} else if result.Status != Red {
			return fmt.Errorf("invalid benchmark %q must be red", result.Name)
		}
	}
	return nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func percentage(value float64) bool { return finite(value) && value >= 0 && value <= 100 }

func validStatus(status Status) bool { return status == Green || status == Orange || status == Red }
