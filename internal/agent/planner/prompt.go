package planner

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tutitoos/atenea/internal/config"
)

// The prompts live in their own file because they are the part of this package
// most likely to be edited by somebody who is not editing Go, and because a
// prompt buried between two functions gets changed without anybody reading
// what it used to say.

func explorePrompt(in assignment) string {
	var b strings.Builder
	b.WriteString(`You are the exploring half of Atenea's orchestrator.

Somebody has asked for a piece of work. Before anybody plans it, your job is to
understand the project well enough that the plan is about this codebase rather
than about codebases in general. You do not plan, and you do not change
anything.

Use the atenea tools you have been given. They are the point: code.search finds
literal text, symbol.definition and symbol.references answer questions about a
named symbol, symbol.overview lists what a file declares, and
catalog.repositories says what is registered on this machine. Grep-by-eye over
whole files is what these exist to replace.

The commission:
`)
	b.WriteString("  " + in.Task.Objective + "\n")
	if in.Task.Criterion != "" {
		b.WriteString("  It will be judged on: " + in.Task.Criterion + "\n")
	}
	if len(in.Task.Files) > 0 {
		b.WriteString("  Files named in the ask: " + strings.Join(in.Task.Files, ", ") + "\n")
	}
	if past := pastRuns(in); past != "" {
		b.WriteString(`
What earlier runs of this agent already found. These were paid for once
already; confirm what you rely on, and do not spend the commission
rediscovering them:
`)
		b.WriteString(past)
	}
	if rejection := rejectionOf(in); rejection != "" {
		b.WriteString("\nYour previous attempt was refused: " + rejection + "\n")
	}
	b.WriteString(`
Answer with two things:

  summary  - what this project is and how it is laid out, as it bears on this
             commission. A reader who has never opened the repository should be
             able to follow the plan afterwards.
  findings - the concrete part. Which files, packages and symbols the
             commission actually touches, named exactly, with what each one
             does. Say what you could not determine rather than filling it in.
`)
	return b.String()
}

func planPrompt(in assignment, cfg config.Config) string {
	var b strings.Builder
	b.WriteString(`You are the planning half of Atenea's orchestrator.

You are handed an exploration of the project and you return a graph of agent
steps, as TOML. You do not do the work and you do not run anything: the graph
you return is read by a person, compiled, and only then executed.

The commission:
`)
	b.WriteString("  " + in.Task.Objective + "\n")
	if in.Task.Criterion != "" {
		b.WriteString("  It will be judged on: " + in.Task.Criterion + "\n")
	}

	b.WriteString("\nWhat the exploration found:\n\n")
	b.WriteString(indent(explorationOf(in)))

	b.WriteString("\nThe agent types declared on this machine. You may name these and\nnothing else:\n\n")
	b.WriteString(declaredTypes(cfg))
	b.WriteString(measuredCosts(in, cfg))

	fmt.Fprintf(&b, `

This commission was granted $%.2f. That figure is the whole run's grant, not
your own allowance for writing this plan, and it is what the graph you return
divides: every step takes a share of it, and the shares must not add up to
more than the grant -- money is split, never copied.

The format, which is compiled before it runs:

    task = "one sentence: the purpose of the whole graph"
    budget_usd = %.2f

    [[step]]
    id = "read-a"                 # unique, referenced by other steps
    agent = "<one of the declared types>"
    objective = "what this step is asked to do"
    criterion = "what a correct answer looks like"
    files = ["path/one.go"]       # optional
    effects = ["read"]            # what it may cause
    budget_usd = 0.25             # its share
    needs = ["other-step"]        # optional: run after these finish OK.
                                  # Order only -- see below.

    [[step]]
    id = "audit-a"
    agent = "<a review-pool type>"
    subject = "read-a"            # required for review-pool types: the step it audits
    on = "answered"               # optional: "ok" (default) or "answered"
    objective = "..."
    criterion = "..."
    effects = ["read"]
    budget_usd = 0.25

Rules the compiler enforces, so getting them wrong costs a round trip:

  - Every agent name must be one of the declared types above.
  - needs and subject must name steps that exist in this graph.
  - No cycles, and no step waiting on or reviewing itself.
  - Shares must not exceed the grant.
  - Two steps that could run at the same time must not both touch a file when
    one of them writes it. Order them with needs, or give them different files.
  - A review-pool type needs a subject. Any other type may only have one if it
    is marked above as reading a subject.

What an edge carries, which is the mistake that costs the most money:

  - needs carries order and NOTHING ELSE. A step waiting on another is still
    handed only its own objective, criterion and files. It never sees what the
    step before it found. A step whose whole purpose is to produce input for a
    later step is money spent on an answer nobody receives.
  - subject is the only edge an answer travels along. It names one upstream
    step, and the step reading it is handed that step's whole report.
  - So if a step needs to know something, one of these is true: it is a type
    that reads a subject and you gave it one; or its own objective and files
    tell it enough to find out itself. There is no third way. When no declared
    type can receive what you wanted to pass, do not add a step to fetch it --
    write the fetching into the objective of the step that needs it.

Return the TOML and nothing else: no fences, no commentary around it.
`, grant(in), grant(in))

	if rejection := rejectionOf(in); rejection != "" {
		b.WriteString(`
Your previous plan was compiled and refused. This is the compiler's own
sentence -- fix exactly this and change nothing else that already worked:

  `)
		b.WriteString(rejection + "\n")
		if previous := previousPlan(in); previous != "" {
			b.WriteString("\nThe plan that was refused:\n\n")
			b.WriteString(indent(previous))
		}
	}
	return b.String()
}

// grant is what the graph this planner writes may allocate: the commission's
// grant, which is the run's ceiling, not this turn's own share of it.
//
// The two were the same value for eleven runs because only the second was on
// the card, and the prompt printed it as "the grant for the whole graph". A
// planner dividing its own allowance produces the same plan at $3.50 and at
// $10.00, which is what was measured on 2026-08-14 before CommissionUSD
// existed.
//
// Falling back to this turn's budget when there is no commission is
// deliberate: outside a workflow no run is above this one, and a planner
// printing a ceiling of zero teaches the model to write plans that allocate
// nothing.
func grant(in assignment) float64 {
	if in.CommissionUSD != nil && *in.CommissionUSD > 0 {
		return *in.CommissionUSD
	}
	if b := budget(in); b > 0 {
		return b
	}
	return 0
}

// explorationOf is the finding the planner builds on. On a relaunch the
// subject is the planner's own refused attempt rather than the exploration, so
// the finding is carried on the task instead -- see cmdAgentRun, which is
// where the two are stitched together.
func explorationOf(in assignment) string {
	if in.Subject == nil {
		return "(nothing)"
	}
	summary, _ := in.Subject.Result[SummaryField].(string)
	findings, _ := in.Subject.Result[FindingsField].(string)
	out := strings.TrimSpace(summary + "\n\n" + findings)
	if out == "" {
		return "(nothing)"
	}
	return out
}

// previousPlan is the graph that was refused, so the relaunch edits rather
// than rewrites. A planner handed only the complaint produces a new plan with
// a new set of mistakes.
func previousPlan(in assignment) string {
	if in.Rejected == nil {
		return ""
	}
	text, _ := in.Rejected.Result[PlanField].(string)
	return text
}

func rejectionOf(in assignment) string {
	if in.Rejected == nil || in.Rejected.Rejection == nil {
		return ""
	}
	return strings.TrimSpace(in.Rejected.Rejection.Text)
}

// pastRuns renders the history level as prose. It is prose and not JSON
// because it is going into a prompt: the model reads it as background, and a
// nested object invites it to answer in the same shape.
func pastRuns(in assignment) string {
	raw, ok := in.Context["history"]
	if !ok {
		return ""
	}
	var level struct {
		Runs []struct {
			Objective  string   `json:"objective"`
			Verdict    string   `json:"verdict"`
			Discovered []string `json:"discovered"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(raw, &level); err != nil {
		return ""
	}
	var b strings.Builder
	for _, run := range level.Runs {
		for _, note := range run.Discovered {
			b.WriteString("  - " + note + "\n")
		}
	}
	return b.String()
}

func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}
	return strings.Join(lines, "\n") + "\n"
}

// The schemas below are the structured-output contract with the model. They
// are strict for the same reason the agent result schema is: a field nobody
// declared is a field nobody checked.

func exploreSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			SummaryField: map[string]any{
				"type":        "string",
				"description": "What this project is and how it is laid out, as it bears on the commission.",
			},
			FindingsField: map[string]any{
				"type":        "string",
				"description": "The files, packages and symbols the commission touches, named exactly.",
			},
		},
		"required":             []any{SummaryField, FindingsField},
		"additionalProperties": false,
	}
}

func planSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			PlanField: map[string]any{
				"type":        "string",
				"description": "The graph, as TOML, in the format given. No fences, no commentary.",
			},
		},
		"required":             []any{PlanField},
		"additionalProperties": false,
	}
}

// measuredCosts renders what each declared type has cost, one line per type,
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
func measuredCosts(in assignment, cfg config.Config) string {
	raw, ok := in.Context["workspace"]
	if !ok {
		return ""
	}
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
