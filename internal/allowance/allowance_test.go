package allowance_test

import (
	"math"
	"testing"

	"github.com/Tutitoos/atenea/internal/allowance"
)

// Tokens(1) is the number every refusal's provenance sentence quotes as "N
// input-equivalent tokens to the dollar": readShare of one dollar, converted
// -- 0.5 x 1 x 166,000.
func TestTokensOfOneDollarIsEightyThreeThousand(t *testing.T) {
	if got := allowance.Tokens(1); got != 83_000 {
		t.Errorf("Tokens(1) = %d, want 83,000", got)
	}
}

// The live reading this whole rule derives from: one real turn's first
// assistant event, measured 2026-08-14 surveying this repository --
// input_tokens 2, no cache read yet, 32,799 cache-creation tokens, output 5.
// The number this reproduces is the one the ReadTokens doc comment carried
// as a comment for a full day before anything refused on it.
func TestWeighReproducesTheFirstEventThatStartedThisRule(t *testing.T) {
	if got := allowance.Weigh(2, 5, 0, 32_799); got != 65_625 {
		t.Errorf("Weigh(2, 5, 0, 32799) = %d, want 65625", got)
	}
}

// The measurement that split the two readings apart, 2026-08-15: two probes
// of the same step against a loopback-recorded and then a live run showed
// the prefix is written to cache once and read by every turn after it, at a
// twentieth of the price. Same prefix, same input/output pair, two weights.
// A change that made these equal would have silently restored the rule that
// charged twenty steps for one write.
func TestTheWarmFirstEventIsATwentiethOfTheColdOne(t *testing.T) {
	const prefix = 26_603
	cold := allowance.StartWeight(prefix, 2, 4)
	warm := allowance.WarmStartWeight(prefix, 2, 4)
	if cold != 53_228 {
		t.Errorf("StartWeight(26603, 2, 4) = %d, want 53,228", cold)
	}
	if warm != 2_682 {
		t.Errorf("WarmStartWeight(26603, 2, 4) = %d, want 2,682", warm)
	}
	// The 22 tokens of input and output ride on both, so the ratio is on
	// the prefix alone: x2 against x0.1.
	if got, want := (cold-22)/(warm-22), 20; got != want {
		t.Errorf("cold/warm prefix ratio = %d, want %d", got, want)
	}
}

// What the split does to the admission rule, in the unit a person reads:
// the same real row that demanded $0.65 a step demands $0.04 once the
// prefix it is derived from is priced as the cache read it actually is.
func TestTheWarmThresholdIsTheOneAStepPays(t *testing.T) {
	const prefix = 26_603
	cold := allowance.MinShareUSD(allowance.StartWeight(prefix, 2, 4))
	warm := allowance.MinShareUSD(allowance.WarmStartWeight(prefix, 2, 4))
	if got := math.Ceil(cold*100) / 100; got != 0.65 {
		t.Errorf("cold threshold = %.2f, want 0.65", got)
	}
	if got := math.Ceil(warm*100) / 100; got != 0.04 {
		t.Errorf("warm threshold = %.2f, want 0.04", got)
	}
}

// MinShareUSD has one job: hand back a share that, funded and converted
// back through Tokens, clears the weight it was computed for -- for every
// weight, not only the ones this codebase happens to have measured. w=0 is
// a turn that costs nothing to start; w=1 is one token off nothing; the
// rest are real first-event weights named elsewhere in this codebase
// (reader, explore, the live reading above). A MinShareUSD that ever
// rounds a token short prints a number that refuses the person who typed
// it back.
func TestTokensOfTheMinimumShareClearsItsOwnWeight(t *testing.T) {
	for _, w := range []int{0, 1, 9_478, 50_704, 65_625} {
		share := allowance.MinShareUSD(w)
		if got := allowance.Tokens(share); got <= w {
			t.Errorf("weight %d: Tokens(MinShareUSD(%d)) = %d, want > %d -- the printed "+
				"share does not clear the weight it was computed for", w, w, got, w)
		}
	}
}
