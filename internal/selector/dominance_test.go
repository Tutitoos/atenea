package selector_test

import (
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Three candidates, and the funnel used to pick the one beaten on both axes.
//
// `cheaper` is Pareto dominance: two implementations trading tokens for
// seconds are incomparable on purpose, because nobody has decided what a
// second is worth. Handing that partial order to slices.SortFunc with an id
// tiebreak on top makes the comparator intransitive, and an intransitive
// comparator does not merely order oddly -- with the insertion sort SortFunc
// uses for small inputs, the losing pair may never be compared at all.
//
// alpha {2000 tokens, 2s} is beaten outright by charlie {1000, 1s}. bravo
// {500, 9s} is incomparable with both, and its id sits between theirs, so it
// screens them from each other. The registry hands candidates over in id
// order, which is exactly the arrangement that hides the comparison.
func TestTheFunnelNeverChoosesACandidateAnotherBeatsOnBothAxes(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("alpha", estimated(2*time.Second, 2000)),
			impl("bravo", estimated(9*time.Second, 500)),
			impl("charlie", estimated(1*time.Second, 1000)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID == "alpha" {
		t.Fatalf("chose alpha, which charlie beats on tokens AND on time: %v", decision.Reason)
	}
	if decision.Chosen.ID != "charlie" && decision.Chosen.ID != "bravo" {
		t.Fatalf("chose %q, which is not a candidate", decision.Chosen.ID)
	}
}

// The order is also stable under the arrangement the input arrives in. An
// ordering that depends on which permutation the registry happened to produce
// is an ordering nobody can reason about, and it is the symptom transitivity
// buys you a cure for.
func TestTheRankingDoesNotDependOnTheOrderCandidatesArriveIn(t *testing.T) {
	a := impl("alpha", estimated(2*time.Second, 2000))
	b := impl("bravo", estimated(9*time.Second, 500))
	c := impl("charlie", estimated(1*time.Second, 1000))

	var first string
	for i, order := range [][]contract.Implementation{
		{a, b, c}, {c, b, a}, {b, a, c}, {b, c, a}, {a, c, b}, {c, a, b},
	} {
		decision, err := mustSelector(t).Select(selector.Request{
			Capability: "code.search",
			Repository: smallGoRepo(),
			Candidates: order,
		})
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		if i == 0 {
			first = decision.Chosen.ID
			continue
		}
		if decision.Chosen.ID != first {
			t.Fatalf("permutation %d chose %q where the first chose %q: "+
				"the winner depends on the order the candidates arrived in",
				i, decision.Chosen.ID, first)
		}
	}
}

// The plain case still holds: with nothing incomparable in the way, the one
// that beats the other on both axes wins.
func TestDominanceStillDecidesBetweenTwo(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("alpha", estimated(2*time.Second, 2000)),
			impl("charlie", estimated(1*time.Second, 1000)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "charlie" {
		t.Fatalf("chose %q, want the one cheaper on both axes", decision.Chosen.ID)
	}
}
