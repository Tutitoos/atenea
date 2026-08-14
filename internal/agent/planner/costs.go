// Cost rendering, kept and not delivered.
//
// This is the reader-facing half of the measurement in internal/workflow's
// CostByType: every declared type on one line, censored the same way the
// store censors, with n beside every median and `never measured` in those
// words for a type nobody has priced.
//
// It was in the planning prompt until 2026-08-14 and is not any more --
// fifteen frozen replays measured plans with it at 2-3 exploring steps
// against 6-9 without it, for the same allocation. The measurement is sound;
// delivering it to a planner is what was harmful. See
// docs/content/measuring-the-wrong-process.md, twelfth instrument.
//
// It stays exported, and under test, for a consumer that has been shown to
// benefit: a person reading what a repository costs, a plan-check that wants
// to say a share looks thin, a future prompt with evidence behind it. An
// untested renderer would rot, and the next consumer would inherit a broken
// measurement rather than none.
package planner

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tutitoos/atenea/internal/config"
)

// CostReport renders what each declared type has cost, one line per type,
// including the types nobody has ever priced.
//
// Every declared type gets a line. A table listing only what was measured
// reads as a complete price list with some rows missing, and the difference
// between "cheap" and "unknown" is the whole reason this exists -- so a type
// with no rows says **never measured** in those words, and the model is left
// to decide what to do about it rather than handed a number that was invented
// for it.
//
// n travels with every median. Three samples and thirty are different claims,
// and the reader can only discount the first if it is told which it has.
func CostReport(workspace json.RawMessage, cfg config.Config) string {
	if len(workspace) == 0 {
		return ""
	}
	raw := workspace
	var level struct {
		Costs *struct {
			Repository string `json:"repository"`
			Covers     string `json:"covers"`
			Types      map[string]struct {
				MedianUSD  float64 `json:"median_usd"`
				MinUSD     float64 `json:"min_usd"`
				MaxUSD     float64 `json:"max_usd"`
				N          int     `json:"n"`
				AtCeiling  int     `json:"at_ceiling"`
				Unmeasured int     `json:"unmeasured"`
			} `json:"types"`
		} `json:"costs"`
	}
	if err := json.Unmarshal(raw, &level); err != nil || level.Costs == nil {
		return ""
	}

	var b strings.Builder
	scope := "machine-wide (nothing has been recorded against this repository yet)"
	if level.Costs.Repository != "" {
		scope = "repository " + level.Costs.Repository
	}
	fmt.Fprintf(&b, "\nWhat these types have actually cost, %s, %s:\n\n",
		scope, level.Costs.Covers)

	for _, declared := range cfg.Agents {
		cost, measured := level.Costs.Types[declared.Spec.Name]
		if !measured || cost.N < publishableN {
			fmt.Fprintf(&b, "  - %s: never measured%s\n", declared.Spec.Name,
				parenthetical(sampleCount(cost.N), exclusions(cost.AtCeiling, cost.Unmeasured)))
			continue
		}
		fmt.Fprintf(&b, "  - %s: median $%.2f over n=%d run(s), range $%.2f-$%.2f%s\n",
			declared.Spec.Name, cost.MedianUSD, cost.N, cost.MinUSD, cost.MaxUSD,
			parenthetical(exclusions(cost.AtCeiling, cost.Unmeasured)))
	}
	// The section ends at the last figure, deliberately. It closed with a
	// paragraph telling the planner that observations are not ceilings, that
	// a share below what a type costs buys a step which stops having produced
	// nothing, and that `never measured` does not mean free. Measured
	// 2026-08-14: the sentence about never measured was ignored outright, and
	// the warning about under-funding is the reason this experiment exists --
	// told that under-funding is the danger and that nothing is priced, a
	// planner picking the types that plausibly cost nothing is reading the
	// paragraph correctly and answering the wrong question with it.
	//
	// It also carried a sentence about a median over one or two runs being
	// barely evidence, which publishableN made impossible to see. An inert
	// falsehood in a prompt is what the edge rule was before it cost seven
	// `needs` edges.
	return b.String()
}

// publishableN is the fewest clean runs a median may be built from before it
// is printed as one.
//
// Below it the type reads `never measured`, in the same words as a type with
// no runs at all, because that is what it is: one sample is an anecdote, and
// naming it a median publishes a false distinction between a type somebody
// happened to run once and a type nobody has run. Measured 2026-08-14: told
// `explore: median $1.29 over n=1` while every other type read never
// measured, a planner dropped explore from the graph entirely and wrote a
// plan that could not search the repository it was auditing. It acted on the
// asymmetry, not the number -- so the fix is to stop manufacturing the
// asymmetry, not to argue with it in prose.
//
// Three is the smallest n where a median is a middle rather than a pick.
const publishableN = 3

// sampleCount says how many clean runs are behind a withheld median. The
// count is a fact and costs nothing to know; the median it cannot support is
// the thing being withheld.
func sampleCount(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "1 clean run so far, too few for a median"
	default:
		return fmt.Sprintf("%d clean runs so far, too few for a median", n)
	}
}

// exclusions names the rows a median left out, so the exclusion is visible
// rather than silently improving the number.
func exclusions(atCeiling, unmeasured int) string {
	var parts []string
	if atCeiling > 0 {
		parts = append(parts, fmt.Sprintf("%d stopped at its ceiling", atCeiling))
	}
	if unmeasured > 0 {
		parts = append(parts, fmt.Sprintf("%d ran unpriced", unmeasured))
	}
	if len(parts) == 0 {
		return ""
	}
	return "excluded: " + strings.Join(parts, ", ")
}

// parenthetical joins the notes that follow a line, dropping the empty ones.
func parenthetical(notes ...string) string {
	var kept []string
	for _, note := range notes {
		if note != "" {
			kept = append(kept, note)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return " (" + strings.Join(kept, "; ") + ")"
}
