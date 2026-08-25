package orchestrator

import (
	"math"
	"sync"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func near(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// The whole defect in one test. Four steps used to be handed the same ceiling
// and could spend it four times over; four shares of a quarter add up to the
// one grant however hard each of them tries.
func TestAWaveCannotSpendTheGrantMoreThanOnce(t *testing.T) {
	purse := newGrant(1.0)
	shares := purse.shares(4)

	var total float64
	for _, share := range shares {
		near(t, share, 0.25, "share")
		total += share
	}
	near(t, total, 1.0, "the shares together")
}

// A share is a ceiling, not a charge. Handing them out must not move the
// books: what a step actually costs is only known when it closes, and most
// steps cost nothing at all.
func TestCuttingSharesSpendsNothing(t *testing.T) {
	purse := newGrant(1.0)
	purse.shares(4)
	near(t, purse.remaining(), 1.0, "remaining after cutting shares")
}

// What a wave leaves behind is divided again by the wave after it. This is the
// accordion the design asks for: money nobody drew is not stranded with the
// step that was offered it.
func TestTheNextWaveDividesWhatIsLeft(t *testing.T) {
	purse := newGrant(1.0)
	purse.shares(4)
	purse.spend(0.40)

	second := purse.shares(2)
	near(t, second[0], 0.30, "the second wave's share")
	near(t, second[0]+second[1], 0.60, "the second wave together")
}

// A commission that ran out hands out zeroes, and a zero is a refusal rather
// than an unlimited ride: contract.Permission.Funded is what reads it.
func TestAnExhaustedGrantHandsOutNothing(t *testing.T) {
	purse := newGrant(0.10)
	purse.spend(0.10)

	for _, share := range purse.shares(3) {
		if share != 0 {
			t.Errorf("an exhausted grant offered %v", share)
		}
	}
	// The two sides of the seam have to agree on what zero means, or an
	// exhausted share would buy a free call from a far side that charges.
	if (contract.Permission{BudgetUSD: purse.shares(1)[0]}).Funded() {
		t.Error("an exhausted share reads as funded")
	}
}

// A far side that billed past what it was handed must not leave the books in
// debt: the next wave would read a negative share, which is not a ceiling but
// a claim. What actually happened is on the receipt either way.
func TestOverspendingStopsAtZeroRatherThanGoingNegative(t *testing.T) {
	purse := newGrant(0.10)
	purse.spend(0.50)
	near(t, purse.remaining(), 0, "remaining after an overspend")
	for _, share := range purse.shares(2) {
		if share < 0 {
			t.Errorf("share = %v, which is a debt and not a ceiling", share)
		}
	}
}

// Steps of one wave close at the same time, which is the only concurrency
// here. The books have to survive it: the race detector runs this suite.
func TestChargesFromOneWaveLandSafely(t *testing.T) {
	purse := newGrant(1.0)
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			purse.spend(0.001)
		}()
	}
	wg.Wait()
	near(t, purse.remaining(), 0.9, "remaining after 100 concurrent charges")
}

// Zero steps is a wave that was entirely blocked upstream. Dividing by it
// would be a panic on a path nobody would think to test.
func TestAnEmptyWaveAsksForNothing(t *testing.T) {
	purse := newGrant(1.0)
	if got := purse.shares(0); got != nil {
		t.Errorf("shares(0) = %v, want nothing", got)
	}
	near(t, purse.remaining(), 1.0, "an empty wave spent something")
}

// A grant cannot open in debt. Nothing upstream should offer one, and if
// something does the answer is a commission that may not spend rather than a
// commission that is owed money.
func TestANegativeGrantOpensEmpty(t *testing.T) {
	near(t, newGrant(-5).remaining(), 0, "a negative grant")
}

// A step that spends nothing must not have to say so. Free providers are the
// common case and the quiet path has to work.
func TestAFreeStepNeedsToSayNothing(t *testing.T) {
	purse := newGrant(1.0)
	purse.shares(3)
	purse.spend(0)
	purse.spend(-1)
	near(t, purse.remaining(), 1.0, "remaining after free steps")
}

// TestAMixedCommissionStillReportsTheDollarsItMeasured is the case that made
// this function worth a test of its own.
//
// A commission whose steps did not all report a price is the ordinary shape,
// not the exotic one: a step served by ripgrep costs nothing and says nothing,
// and a step blocked by a failed prerequisite never reaches a provider at all.
// The first version of totalUSD returned (0, false) as soon as it met one of
// those, which threw away the dollars the metered steps had genuinely been
// charged -- so a $1.20 invoice was reported as no money at all, in the
// partially-failed commission an operator is most likely to be auditing.
func TestAMixedCommissionStillReportsTheDollarsItMeasured(t *testing.T) {
	priced := func(usd float64) StepResult {
		return StepResult{Outcome: contract.Outcome{SpentUSD: usd, SpentUSDKnown: true}}
	}
	silent := StepResult{Outcome: contract.Outcome{}}

	usd, known := totalUSD([]StepResult{priced(0.40), priced(0.40), priced(0.40), silent})
	if !known {
		t.Error("three measured charges and one silent step reported no measurement at all")
	}
	if usd < 1.1999 || usd > 1.2001 {
		t.Errorf("measured total is %v, want the $1.20 the three metered steps were charged", usd)
	}

	// A commission nobody priced is still unknown rather than a measured zero,
	// which is the whole reason the second return value exists.
	if _, known := totalUSD([]StepResult{silent, silent}); known {
		t.Error("a commission where no step reported a price claims to have measured one")
	}
	if _, known := totalUSD(nil); known {
		t.Error("a commission with no steps claims to have measured a total")
	}

	// And a measured zero stays measured: a provider that charged nothing and
	// said so is a different fact from a provider that said nothing.
	if usd, known := totalUSD([]StepResult{priced(0)}); !known || usd != 0 {
		t.Errorf("a measured zero came back as (%v, %v), want (0, true)", usd, known)
	}
}
