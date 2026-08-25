package decision

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/floor"
)

// storedFloor writes one measured startup floor and hands back the store the
// estimator reads it from.
func storedFloor(t *testing.T, usd float64) *floor.Store {
	t.Helper()
	store, err := floor.Open(filepath.Join(t.TempDir(), "floors.json"))
	if err != nil {
		t.Fatalf("floor.Open: %v", err)
	}
	// One thousand prefix tokens and no first call: ColdStartUSD is then
	// exactly usd, so the test says what it means about the arithmetic
	// downstream rather than about the floor package's derivation.
	if err := store.Put(floor.Measurement{
		Repository: "repo", Agent: "explore", Model: "claude-opus-5",
		USD: usd, PrefixTokens: 1000, USDPerToken: usd / 1000, Cold: true,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	return store
}

// Source is the trace a person reads to find out where a forecast came from.
// A measured floor that comes in under the conservative baseline changes
// nothing about the figure returned, so it must not be named as its source:
// the number handed back is DefaultBudgetEstimator's, and a reader sent to a
// measurement that had no part in it cannot reproduce the estimate.
func TestAnEstimateTheMeasurementDidNotRaiseIsNotCalledMeasured(t *testing.T) {
	measured := MeasuredBudgetEstimator{Store: storedFloor(t, 0.02)}
	baseline := (DefaultBudgetEstimator{}).Estimate("repo", "explore", "claude-opus-5")

	estimate := measured.Estimate("repo", "explore", "claude-opus-5")

	if estimate.EstimatedUSD != baseline.EstimatedUSD {
		t.Fatalf("estimate = %v, want the baseline's %v: a $0.02 floor cannot raise it",
			estimate.EstimatedUSD, baseline.EstimatedUSD)
	}
	if strings.Contains(estimate.Source, "measured") {
		t.Errorf("source = %q for a figure the measurement never touched", estimate.Source)
	}
	if estimate.Source != baseline.Source {
		t.Errorf("source = %q, want the baseline's own %q", estimate.Source, baseline.Source)
	}
}

// The other half of the same rule: a floor high enough to raise the forecast
// is what produced the number, and the trace has to say so.
func TestAnEstimateTheMeasurementRaisedSaysWhichMeasurement(t *testing.T) {
	measured := MeasuredBudgetEstimator{Store: storedFloor(t, 1.00)}
	baseline := (DefaultBudgetEstimator{}).Estimate("repo", "explore", "claude-opus-5")

	estimate := measured.Estimate("repo", "explore", "claude-opus-5")

	if estimate.EstimatedUSD <= baseline.EstimatedUSD {
		t.Fatalf("estimate = %v, want a $1.00 floor plus headroom to beat the baseline's %v",
			estimate.EstimatedUSD, baseline.EstimatedUSD)
	}
	if !strings.Contains(estimate.Source, "measured repo/explore/claude-opus-5") {
		t.Errorf("source = %q, want the triple the figure was derived from", estimate.Source)
	}
}
