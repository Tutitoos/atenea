package selector_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// measuring runs the funnel with a base attached, which is the only state in
// which break-in turns mean anything.
func measuring(t *testing.T, candidates []contract.Implementation, rules ...selector.Rule) selector.Decision {
	t.Helper()
	decision, err := mustSelector(t, rules...).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: candidates,
		Measuring:  true,
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	return decision
}

// The trap the whole break-in rule exists for. Ranking on cost alone hands
// every call to the estimated-cheapest, so the other never runs, is never
// measured, and its estimate is never corrected -- the hybrid never starts.
// The one that owes the base measurements goes first until it has them.
func TestTheUnmeasuredOneGetsTheTurn(t *testing.T) {
	decision := measuring(t, []contract.Implementation{
		impl("ripgrep", estimated(80*time.Microsecond, 0), measured(80*time.Microsecond, 0, 40)),
		impl("serena.search", estimated(2*time.Second, 900)),
	})

	if decision.Chosen.ID != "serena.search" {
		t.Errorf("chosen %s, want serena.search: the unmeasured one is owed its turn", decision.Chosen.ID)
	}
	if !strings.Contains(decision.Reason, "break-in turn") {
		t.Errorf("reason = %q, want it to name the break-in turn", decision.Reason)
	}
}

// A break-in turn is not a cost decision, and the trace has to say so in those
// words. A reader who took this for "serena is cheaper" would go hunting a
// cost bug that does not exist.
func TestTheTraceRefusesToCallABreakInTurnACostDecision(t *testing.T) {
	decision := measuring(t, []contract.Implementation{
		impl("ripgrep", estimated(80*time.Microsecond, 0), measured(80*time.Microsecond, 0, 40)),
		impl("serena.search", estimated(2*time.Second, 900)),
	})

	if strings.Contains(decision.Reason, "cheapest") {
		t.Errorf("reason = %q: a break-in turn was dressed up as a cost decision", decision.Reason)
	}
	if !strings.Contains(decision.Reason, "not a cost decision") {
		t.Errorf("reason = %q, want it to disclaim cost outright", decision.Reason)
	}
	// The count is what makes it checkable: 0 of 2 says how much is still owed.
	if !strings.Contains(decision.Reason, "0 of 2") {
		t.Errorf("reason = %q, want the samples owed spelled out", decision.Reason)
	}
}

// The rotation has to end. Two providers starting from nothing alternate until
// both hold their samples, and from that point cost decides every time -- a
// funnel that kept rotating would never let the cheap one be cheap.
func TestBreakInConvergesAndThenCostTakesOver(t *testing.T) {
	samples := map[string]int{"ripgrep": 0, "serena.search": 0}
	// ripgrep is genuinely the cheaper one, measured or estimated, so once
	// break-in is over it must win every remaining round.
	spend := map[string]contract.Sample{
		"ripgrep":       {Duration: 80 * time.Microsecond},
		"serena.search": {Duration: 2 * time.Second, Tokens: 900},
	}

	var order []string
	for range 6 {
		candidates := make([]contract.Implementation, 0, 2)
		for _, id := range []string{"ripgrep", "serena.search"} {
			candidates = append(candidates, impl(id,
				estimated(spend[id].Duration, spend[id].Tokens),
				measured(spend[id].Duration, spend[id].Tokens, samples[id])))
		}
		chosen := measuring(t, candidates).Chosen.ID
		order = append(order, chosen)
		samples[chosen]++
	}

	for id, got := range samples {
		if got < selector.BreakInSamples {
			t.Errorf("%s ended on %d samples, never earned its %d: %v",
				id, got, selector.BreakInSamples, order)
		}
	}
	// Once both are measured the cheaper one wins outright, so the tail of the
	// run is ripgrep and nothing else.
	tail := order[len(order)-2:]
	for _, id := range tail {
		if id != "ripgrep" {
			t.Errorf("after break-in the funnel still rotated: %v", order)
			break
		}
	}
}

// Break-in earns numbers; it does not override the funnel's fixed rules. A
// provider that is down or degraded must not be handed work just because
// nobody has measured it -- health is a fact about now, and a measurement
// bought from a sick provider is a measurement of the sickness.
func TestHealthStillOutranksTheBreakInTurn(t *testing.T) {
	decision := measuring(t, []contract.Implementation{
		impl("ripgrep", health(contract.HealthAlive, 1), measured(80*time.Microsecond, 0, 40)),
		impl("serena.search", health(contract.HealthDegraded, 0.4), estimated(2*time.Second, 900)),
	})

	if decision.Chosen.ID != "ripgrep" {
		t.Errorf("chosen %s, want ripgrep: a degraded provider took the break-in turn", decision.Chosen.ID)
	}
	if !strings.Contains(decision.Reason, "healthiest") {
		t.Errorf("reason = %q, want health to be named as what settled it", decision.Reason)
	}
}

// The user's word comes before Atenea's opinion, and a break-in turn is an
// opinion. Someone who pinned an implementation asked for that one, not for
// the one Atenea would like to measure.
func TestAUserRuleStillOutranksTheBreakInTurn(t *testing.T) {
	decision := measuring(t, []contract.Implementation{
		impl("ripgrep", measured(80*time.Microsecond, 0, 40)),
		impl("serena.search", estimated(2*time.Second, 900)),
	}, selector.Rule{Capability: "code.search", Prefer: "ripgrep"})

	if decision.Chosen.ID != "ripgrep" {
		t.Errorf("chosen %s, want ripgrep: the break-in turn overrode a user rule", decision.Chosen.ID)
	}
	if !strings.Contains(decision.Reason, "user rule") {
		t.Errorf("reason = %q, want the rule named", decision.Reason)
	}
}

// With no base attached there is nothing to earn: a turn handed to an
// unmeasured provider buys a measurement nobody writes down. So the funnel
// ranks on the declared estimates instead of rotating forever.
func TestWithNoBaseThereIsNoRotation(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("ripgrep", estimated(80*time.Microsecond, 0)),
			impl("serena.search", estimated(2*time.Second, 900)),
		},
		Measuring: false,
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "ripgrep" {
		t.Errorf("chosen %s, want ripgrep: with no base the estimate decides", decision.Chosen.ID)
	}
	if !strings.Contains(decision.Reason, "estimated") {
		t.Errorf("reason = %q, want it to admit the number is a guess", decision.Reason)
	}
}

// Sample counts drift apart forever once everybody is past break-in: the
// provider that answers most has the most measurements. That must not be read
// as owing something, or the funnel would spend eternity feeding work to
// whoever it used least.
func TestPastBreakInTheSampleCountStopsMattering(t *testing.T) {
	decision := measuring(t, []contract.Implementation{
		impl("ripgrep", measured(80*time.Microsecond, 0, 900)),
		impl("serena.search", measured(2*time.Second, 900, 2)),
	})

	if decision.Chosen.ID != "ripgrep" {
		t.Errorf("chosen %s, want ripgrep: a lopsided sample count was mistaken for a debt",
			decision.Chosen.ID)
	}
	if !strings.Contains(decision.Reason, "measured") {
		t.Errorf("reason = %q, want it to say the numbers are real", decision.Reason)
	}
}

// Owing measurements is not a fault. Cost only ever orders, and break-in is
// part of the ordering -- so nothing leaves the funnel for being unmeasured
// any more than for being expensive.
func TestOwingMeasurementsNeverRemovesAProvider(t *testing.T) {
	decision := measuring(t, []contract.Implementation{
		impl("ripgrep", measured(80*time.Microsecond, 0, 40)),
		impl("serena.search", estimated(2*time.Second, 900)),
	})

	choice := stage(t, decision, selector.StageChoice)
	if len(choice.In) != 2 {
		t.Errorf("the choice stage saw %v, want both implementations", choice.In)
	}
	if len(choice.Dropped) != 0 {
		t.Errorf("the funnel dropped %v; cost and break-in only order", choice.Dropped)
	}
}
