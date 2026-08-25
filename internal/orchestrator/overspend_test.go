package orchestrator_test

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// stepResult builds the two numbers Overspend reads and nothing else: what a
// step was granted, and what it was actually charged.
func stepResult(budgetUSD, spentUSD float64) orchestrator.StepResult {
	return orchestrator.StepResult{
		Step:    contract.Step{Permission: contract.Permission{BudgetUSD: budgetUSD}},
		Outcome: contract.Outcome{SpentUSD: spentUSD, SpentUSDKnown: true},
	}
}

// A step that stayed inside its share is the ordinary case, and it has to
// read as exactly nothing -- not a small negative number that a naive
// subtraction would produce.
func TestOverspendIsZeroWhenAStepStaysWithinItsShare(t *testing.T) {
	if got := orchestrator.Overspend(stepResult(0.25, 0.10)); got != 0 {
		t.Errorf("Overspend = %v, want 0", got)
	}
}

// Spending exactly the share is not overspending it. The boundary belongs to
// the step that stayed inside, not to the one that ran past.
func TestOverspendIsZeroAtExactlyTheShare(t *testing.T) {
	if got := orchestrator.Overspend(stepResult(0.25, 0.25)); got != 0 {
		t.Errorf("Overspend = %v, want 0", got)
	}
}

// A step that spent zero, because it was refused before spawning or answered
// for free, is never an overspend regardless of what share it was granted.
func TestOverspendIsZeroForAFreeOrRefusedStep(t *testing.T) {
	if got := orchestrator.Overspend(stepResult(0.25, 0)); got != 0 {
		t.Errorf("Overspend = %v, want 0", got)
	}
}

// The whole point: a far side's own ceiling let a turn finish after the
// budget for it was gone, and this is the number that says by how much --
// the same shape as the live $0.4480-of-$0.25 call that motivated it.
func TestOverspendIsTheAmountPastTheShare(t *testing.T) {
	got := orchestrator.Overspend(stepResult(0.25, 0.3540745))
	want := 0.3540745 - 0.25
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Overspend = %v, want %v", got, want)
	}
}
