package selector

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// sorted ranks candidates the way the funnel's last stage does, with the
// break-in rotation live -- which is the only state where any of this differs.
func sorted(impls ...contract.Implementation) []contract.Implementation {
	out := slices.Clone(impls)
	slices.SortStableFunc(out, rankWith(true))
	return out
}

// The break-in turn is a purchase: a dispatch spent on an unmeasured provider
// buys the base a number it does not have yet. This is what happens when the
// purchase never completes.
//
// Measured on this machine, which is why the test exists. claude.search failed
// seven dispatches in a row against a repository where ripgrep had fourteen
// clean measurements averaging 959ms, and the funnel picked claude.search for
// the eighth. Every failure cost about thirty cents and roughly a minute, and
// none of them produced a measurement -- so the provider stayed at zero
// samples, and the break-in rule promotes the fewest samples. Failing was
// literally what kept it winning.
func TestABreakInTurnIsNotUnlimited(t *testing.T) {
	proven := contract.Implementation{
		ID: "ripgrep", Capability: "code.search", Provider: "ripgrep",
		Cost: contract.Cost{
			Measured: contract.Sample{Duration: 959 * time.Millisecond},
			Samples:  14,
		},
	}
	// Seven attempts, no measurement to show for any of them.
	spendthrift := contract.Implementation{
		ID: "claude.search", Capability: "code.search", Provider: "claude-code",
		Cost: contract.Cost{
			Estimated: contract.Sample{Duration: 3 * time.Second, Tokens: 900},
			Samples:   0,
			Attempts:  7,
		},
	}

	ranked := sorted(proven, spendthrift)

	if ranked[0].ID != "ripgrep" {
		t.Errorf("the funnel picked %s over a provider with 14 measurements; "+
			"seven paid attempts bought nothing and the eighth would not either",
			ranked[0].ID)
	}
}

// The rule the above must not break: a provider genuinely on its first outing
// still gets its turn ahead of a measured one. That rotation is the only way an
// estimate ever gets corrected, and without it the estimated-cheapest wins
// every dispatch forever and nothing else is ever measured.
func TestAFirstOutingStillOutranksAMeasuredRival(t *testing.T) {
	proven := contract.Implementation{
		ID: "ripgrep", Capability: "code.search", Provider: "ripgrep",
		Cost: contract.Cost{
			Measured: contract.Sample{Duration: 959 * time.Millisecond},
			Samples:  14,
		},
	}
	fresh := contract.Implementation{
		ID: "claude.search", Capability: "code.search", Provider: "claude-code",
		Cost: contract.Cost{
			Estimated: contract.Sample{Duration: 3 * time.Second, Tokens: 900},
		},
	}

	ranked := sorted(proven, fresh)

	if ranked[0].ID != "claude.search" {
		t.Errorf("a provider nobody has ever measured was ranked below one that "+
			"has been, so its estimate can never be corrected: got %s", ranked[0].ID)
	}
}

// Halfway through the rotation is still inside it. A provider that has earned
// one of the two samples it owes is being measured successfully and must keep
// its turn -- the ceiling is for attempts that produce nothing, not for a
// purchase that is partway done.
func TestAPartlyMeasuredProviderKeepsItsTurn(t *testing.T) {
	proven := contract.Implementation{
		ID: "ripgrep", Capability: "code.search", Provider: "ripgrep",
		Cost: contract.Cost{Measured: contract.Sample{Duration: time.Second}, Samples: 14},
	}
	halfway := contract.Implementation{
		ID: "claude.search", Capability: "code.search", Provider: "claude-code",
		Cost: contract.Cost{
			// Expensive on both axes under EITHER figure, so the only thing that
			// can put it first is the turn itself. Without the estimate filled
			// in, its Effective cost is a zero that reads as free, no rival can
			// be cheaper on both axes, and the assertion passes on alphabetical
			// order having proved nothing.
			Estimated: contract.Sample{Duration: 30 * time.Second, Tokens: 900},
			Measured:  contract.Sample{Duration: 20 * time.Second, Tokens: 800},
			Samples:   1, Attempts: 9,
		},
	}

	ranked := sorted(proven, halfway)

	if ranked[0].ID != "claude.search" {
		t.Errorf("a provider one sample short of the base's quota lost its turn: got %s", ranked[0].ID)
	}
}

// And the reader has to be told which of the two it is looking at, because the
// two read identically on screen -- the same provider, ranked below the same
// rival -- and mean opposite things. One is the rotation working; the other is
// the rotation having been cut off after paying for nothing.
func TestTheReasonSaysWhenARotationWasCutOff(t *testing.T) {
	proven := contract.Implementation{
		ID: "ripgrep", Capability: "code.search",
		Cost: contract.Cost{Measured: contract.Sample{Duration: time.Second}, Samples: 14},
	}
	spendthrift := contract.Implementation{
		ID: "claude.search", Capability: "code.search",
		Cost: contract.Cost{
			Estimated: contract.Sample{Duration: 3 * time.Second, Tokens: 900},
			Attempts:  7,
		},
	}

	reason := reasonFor(sorted(proven, spendthrift), true)

	if !strings.Contains(reason, "claude.search") {
		t.Errorf("the reason does not name who lost its turn: %q", reason)
	}
	if !strings.Contains(reason, "7") {
		t.Errorf("the reason does not say how many attempts bought nothing: %q", reason)
	}
}
