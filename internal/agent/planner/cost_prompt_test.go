package planner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// withCosts puts a served workspace level on an assignment, the way the
// runner serves it.
func withCosts(in assignment, level map[string]any) assignment {
	raw, err := json.Marshal(level)
	if err != nil {
		panic(err)
	}
	if in.Context == nil {
		in.Context = map[string]json.RawMessage{}
	}
	in.Context["workspace"] = raw
	return in
}

func planningTypes() config.Config {
	return config.Config{Agents: []config.AgentType{
		{Spec: contract.AgentTypeSpec{Name: "explore"}, Pool: config.PoolAgent},
		{Spec: contract.AgentTypeSpec{Name: "filereader"}, Pool: config.PoolAgent},
	}}
}

// A type nobody has priced must say so in those words. Anything else -- a
// zero, a dash, an omitted line -- reads as "cheap", and the whole point of
// measuring is that "we do not know" is a different fact from "not much".
func TestATypeWithNoRunsSaysNeverMeasured(t *testing.T) {
	in := withCosts(planAssignment("ok", exploration()), map[string]any{
		"repositories": []string{"atenea"},
		"costs": map[string]any{
			"repository": "atenea",
			"covers":     "workflow steps only",
			"types": map[string]any{
				"explore": map[string]any{"median_usd": 1.63, "min_usd": 1.26, "max_usd": 2.16, "n": 3},
			},
		},
	})
	got := planPrompt(in, planningTypes())

	if !strings.Contains(got, "filereader: never measured") {
		t.Errorf("a type with no rows must say `never measured`:\n%s", got)
	}
}

// The section ends at the last figure. The paragraph that used to close it
// was measured on 2026-08-14 and removed: its sentence about `never measured`
// not meaning free was ignored, and its warning about under-funding is the
// most plausible reason a planner facing all-unpriced types picked the ones
// that plausibly cost nothing.
func TestTheCostSectionEndsAtTheFigures(t *testing.T) {
	got := planPrompt(costsOf(t, "explore", map[string]any{
		"median_usd": 1.63, "min_usd": 1.26, "max_usd": 2.16, "n": 3,
	}), planningTypes())

	for _, gone := range []string{
		"These are observations, not prices and not ceilings",
		"will stop at its ceiling having produced nothing",
		"barely",
	} {
		if strings.Contains(got, gone) {
			t.Errorf("the closing paragraph is back: %q\n%s", gone, got)
		}
	}
}

// n travels with the median. A median over three runs and one over thirty are
// different claims, and a planner can only discount the first if it is told.
func TestTheMedianIsPrintedWithItsSampleSize(t *testing.T) {
	in := withCosts(planAssignment("ok", exploration()), map[string]any{
		"costs": map[string]any{
			"covers": "workflow steps only",
			"types": map[string]any{
				"explore": map[string]any{"median_usd": 1.63, "min_usd": 1.26, "max_usd": 2.16, "n": 3},
			},
		},
	})
	got := planPrompt(in, planningTypes())

	if !strings.Contains(got, "median $1.63 over n=3 run(s), range $1.26-$2.16") {
		t.Errorf("the median must carry its sample size and range:\n%s", got)
	}
}

// The rows left out of the median are named where the median is read, not in
// a footnote and not nowhere.
func TestExcludedRowsAreCountedOutLoud(t *testing.T) {
	in := withCosts(planAssignment("ok", exploration()), map[string]any{
		"costs": map[string]any{
			"covers": "workflow steps only",
			"types": map[string]any{
				"explore": map[string]any{
					"median_usd": 1.63, "min_usd": 1.26, "max_usd": 2.16, "n": 3,
					"at_ceiling": 1, "unmeasured": 1,
				},
			},
		},
	})
	got := planPrompt(in, planningTypes())

	if !strings.Contains(got, "excluded: 1 stopped at its ceiling, 1 ran unpriced") {
		t.Errorf("the censored rows are not counted where the median is read:\n%s", got)
	}
}

// A table read off other repositories' rows must not be presented as this
// repository's price, and the estimate must not imply it covers single agent
// runs -- agent_trace has no spend column at all.
func TestAMachineWideTableSaysSoAndNamesWhatItCovers(t *testing.T) {
	in := withCosts(planAssignment("ok", exploration()), map[string]any{
		"costs": map[string]any{
			"covers": "workflow steps only",
			"types": map[string]any{
				"explore": map[string]any{"median_usd": 1.63, "min_usd": 1.63, "max_usd": 1.63, "n": 1},
			},
		},
	})
	got := planPrompt(in, planningTypes())

	if !strings.Contains(got, "machine-wide (nothing has been recorded against this repository yet)") {
		t.Errorf("an unscoped table must say it is machine-wide:\n%s", got)
	}
	if !strings.Contains(got, "workflow steps only") {
		t.Error("the prompt never says what the figures cover")
	}
}

// A machine with no measurements at all says nothing rather than printing a
// table of zeros. The absence is the honest output.
func TestNoCostTableMeansNoSection(t *testing.T) {
	got := planPrompt(planAssignment("ok", exploration()), planningTypes())
	if strings.Contains(got, "What these types have actually cost") {
		t.Errorf("a machine with no measurements printed a cost table:\n%s", got)
	}
}

// costsOf builds a served workspace level with one type's figures.
func costsOf(t *testing.T, name string, fields map[string]any) assignment {
	t.Helper()
	return withCosts(planAssignment("ok", exploration()), map[string]any{
		"costs": map[string]any{
			"covers": "workflow steps only",
			"types":  map[string]any{name: fields},
		},
	})
}

// One sample is an anecdote. Published as a median beside types that read
// `never measured`, it manufactures a distinction the data cannot support --
// measured 2026-08-14, a planner handed exactly that dropped the only type
// that could search from the graph.
func TestAMedianOfOneIsNotPublished(t *testing.T) {
	got := planPrompt(costsOf(t, "explore", map[string]any{
		"median_usd": 1.29, "min_usd": 1.29, "max_usd": 1.29, "n": 1,
	}), planningTypes())

	if strings.Contains(got, "median $1.29") {
		t.Errorf("a median built from one run was published:\n%s", got)
	}
	if !strings.Contains(got, "explore: never measured") {
		t.Errorf("a withheld median must read the same as no data at all:\n%s", got)
	}
}

// Withholding the figure is not withholding the fact that runs exist: the
// count is honest, cheap, and cannot be mistaken for a price.
func TestAWithheldMedianStillPrintsItsSampleCount(t *testing.T) {
	got := planPrompt(costsOf(t, "explore", map[string]any{
		"median_usd": 1.29, "min_usd": 1.29, "max_usd": 1.29, "n": 2, "at_ceiling": 1,
	}), planningTypes())

	if !strings.Contains(got, "2 clean runs so far, too few for a median") {
		t.Errorf("the sample count behind a withheld median is missing:\n%s", got)
	}
	if !strings.Contains(got, "1 stopped at its ceiling") {
		t.Errorf("the exclusions are lost when the median is withheld:\n%s", got)
	}
}

// At the threshold a median is a middle rather than a pick, and it is
// published. The line either side of the boundary is the whole rule.
func TestTheThirdCleanRunPublishesTheMedian(t *testing.T) {
	got := planPrompt(costsOf(t, "explore", map[string]any{
		"median_usd": 1.63, "min_usd": 1.26, "max_usd": 2.16, "n": 3,
	}), planningTypes())

	if !strings.Contains(got, "explore: median $1.63 over n=3 run(s)") {
		t.Errorf("three clean runs must publish:\n%s", got)
	}
}

// The grant a planner divides is the commission's, not its own share of it.
//
// Measured 2026-08-14: eleven runs allocated the same $0.90 whether the
// commission granted $3.50 or $10.00, because the prompt printed the plan
// step's own budget under the name "the grant for the whole graph".
func TestThePromptCarriesTheCommissionsGrant(t *testing.T) {
	in := planAssignment("ok", exploration())
	own, commission := 0.90, 10.00
	in.BudgetUSD = &own
	in.CommissionUSD = &commission

	got := planPrompt(in, planningTypes())

	if !strings.Contains(got, "granted $10.00") {
		t.Errorf("the planner was not told the commission's grant:\n%s", got)
	}
	if strings.Contains(got, "$0.90") {
		t.Errorf("the planner's own allowance reached the prompt as the grant:\n%s", got)
	}
}

// Outside a workflow no run is above this one, and printing a ceiling of zero
// teaches the model to write plans that allocate nothing.
func TestWithoutACommissionTheOwnBudgetIsTheCeiling(t *testing.T) {
	in := planAssignment("ok", exploration())
	own := 0.90
	in.BudgetUSD = &own

	if got := planPrompt(in, planningTypes()); !strings.Contains(got, "granted $0.90") {
		t.Errorf("a planner outside a workflow was given no ceiling:\n%s", got)
	}
}
