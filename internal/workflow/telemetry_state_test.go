package workflow

import (
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestTokenMeasurementStateDoesNotTreatDollarOnlyAsMeasuredTokens(t *testing.T) {
	usd := 0.25
	report := contract.Report{Spent: contract.Charge{USD: &usd, PricedBy: "provider"}}
	if got := tokenMeasurementState(report); got != MeasurementUnknown {
		t.Fatalf("dollar-only report token state = %s, want unknown", got)
	}
	report.Spent.InputTokens = 1
	if got := tokenMeasurementState(report); got != MeasurementMeasured {
		t.Fatalf("token-bearing report state = %s, want measured", got)
	}
}
