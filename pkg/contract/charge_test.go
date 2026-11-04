package contract_test

import (
	"math"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func ptrOf(f float64) *float64 { return &f }

// Every comparison against NaN is false, so a validator written as
// `amount < 0` refuses negatives and waves NaN straight through. +Inf gets
// past the same test for the opposite reason: a receipt carrying either is a
// figure nothing can enforce.
func TestMoneyThatIsNotARealNumberIsRefused(t *testing.T) {
	for name, amount := range map[string]float64{
		"NaN":               math.NaN(),
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
	} {
		t.Run("charge of "+name, func(t *testing.T) {
			charge := contract.Charge{InputTokens: 10, USD: ptrOf(amount), PricedBy: "a price list"}
			if err := charge.Validate(); err == nil {
				t.Fatal("a receipt figure that is not a real number was accepted")
			}
		})
	}
}

// Plus is folded over a run's charges one at a time, which is what makes both
// of these matter. The pointer must not be the operand's own, or the running
// total is an alias of whichever report was measured; and the provenance must
// name each source once, or fifteen turns priced by the same list read back as
// that list fifteen times.
func TestChargePlusNeitherAliasesNorRepeatsItsSources(t *testing.T) {
	priced := contract.Charge{InputTokens: 10, USD: ptrOf(0.25), PricedBy: "anthropic"}
	// The zero Charge is what an agent that spent nothing hands back, and it
	// is the operand that sends Plus down the two shortcuts. It is the start
	// of every fold, so the shortcut runs on the first addition of every run.
	unmeasured := contract.Charge{}

	total := unmeasured.Plus(priced)
	if total.USD == priced.USD {
		t.Fatal("the running total shares the measured charge's dollar figure")
	}
	total = priced.Plus(unmeasured)
	if total.USD == priced.USD {
		t.Fatal("the running total shares its own operand's dollar figure")
	}
	if total.USD == nil || *total.USD != 0.25 {
		t.Fatalf("USD = %v, want the one measured figure", total.USD)
	}

	// Three turns, two of them priced by the same list. The fold must say
	// each source once.
	second := contract.Charge{InputTokens: 4, USD: ptrOf(0.10), PricedBy: "anthropic"}
	third := contract.Charge{InputTokens: 6, USD: ptrOf(0.05), PricedBy: "openrouter"}
	folded := priced.Plus(second).Plus(third).Plus(second)
	if got := strings.Count(folded.PricedBy, "anthropic"); got != 1 {
		t.Fatalf("priced_by = %q names anthropic %d times, want once", folded.PricedBy, got)
	}
	if folded.PricedBy != "anthropic and openrouter" {
		t.Fatalf("priced_by = %q", folded.PricedBy)
	}
}
