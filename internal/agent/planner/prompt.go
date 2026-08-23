package planner

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Tutitoos/atenea/internal/config"
)

// The prompts live in their own file because they are the part of this package
// most likely to be edited by somebody who is not editing Go, and because a
// prompt buried between two functions gets changed without anybody reading
// what it used to say.

// explorePrompt is the exploring half's commission, on the surface it was
// given. The tool paragraph is the one part that moves: a turn told to reach
// for code.search when it holds no such tool spends the commission looking
// for it, and one told to read files it does have never learns that the
// capabilities were the point.
func explorePrompt(in assignment, s Surface) string {
	var b strings.Builder
	b.WriteString(`You are the exploring half of Atenea's orchestrator.

Somebody has asked for a piece of work. Before anybody plans it, your job is to
understand the project well enough that the plan is about this codebase rather
than about codebases in general. You do not plan, and you do not change
anything.

`)
	if s.Capabilities {
		b.WriteString(`Use the atenea tools you have been given. They are the point. Your FIRST move
for a task stated only in prose is code.context: it finds the declarations,
relationships and (in plan mode) tests the task is actually about. After that,
code.search finds literal text; symbol.definition and symbol.references answer
questions about a named symbol; symbol.overview lists what a file declares; and
catalog.repositories says what is registered on this machine. Grep-by-eye over
whole files is what these exist to replace.
`)
	} else {
		b.WriteString(`You have Read and Glob, and no other tools at all. That is deliberate: this
step was chosen for work whose files are already named, and carrying Atenea's
capability catalog would cost about five times as much to start as it does to
read them. Read what the commission names, glob to resolve a name or find the
file beside it, and answer from what you actually read. You cannot search the
text of the code and you cannot ask about a symbol -- when an answer would
need that, say so in findings instead of guessing, and whoever reads this can
dispatch an explorer for that one question.
`)
	}
	b.WriteString(`
The commission:
`)
	b.WriteString("  " + in.Task.Objective + "\n")
	if in.Route != nil {
		b.WriteString("\nThe execution route has already been decided by Atenea; do not substitute another model or tool surface.\n")
		if in.Route.Model != "" {
			b.WriteString("  model: " + in.Route.Model + " (" + in.Route.Backend + ")\n")
		}
		if len(in.Route.Capabilities) > 0 {
			b.WriteString("  capabilities: " + strings.Join(in.Route.Capabilities, ", ") + "\n")
		}
		if len(in.Route.Providers) > 0 {
			providers := make([]string, 0, len(in.Route.Providers))
			for capability, provider := range in.Route.Providers {
				providers = append(providers, capability+"="+provider)
			}
			sort.Strings(providers)
			b.WriteString("  providers: " + strings.Join(providers, ", ") + "\n")
			b.WriteString("  When calling a capability, pass its selected implementation in the optional `_atenea_prefer` argument. Do not use a different provider.\n")
		}
		if len(in.Route.Tools) > 0 {
			b.WriteString("  tools: " + strings.Join(in.Route.Tools, ", ") + "\n")
		}
	}
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
	// No cost section. It was here until 2026-08-14, and fifteen frozen
	// replays measured what it did: plans with it carried 2-3 exploring
	// steps against 6-9 without it, allocating the same money. It was added
	// to stop under-allocation and never moved allocation in any
	// configuration -- that was a contract bug, fixed by CommissionUSD. The
	// renderer is kept in costs.go for a consumer shown to benefit.

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
    budget_usd = <its share of the grant>
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
    budget_usd = <its share>

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

Every <...> above is a placeholder: replace each one with a real value.

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
//
// completeness and stopped_at are on both, and both are required rather than
// optional. The model is the only party that knows, pass to pass, how much of
// the commission it actually covered -- the harness cannot measure it, only
// price the turn after the fact -- so the schema is written so a pass that
// forgets to state its own coverage is refused by the CLI before it ever
// reaches this package, rather than silently read as complete.

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
			"completeness": completenessProperty(),
			"stopped_at":   stoppedAtProperty(),
		},
		"required":             []any{SummaryField, FindingsField, "completeness", "stopped_at"},
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
			"completeness": completenessProperty(),
			"stopped_at":   stoppedAtProperty(),
		},
		"required":             []any{PlanField, "completeness", "stopped_at"},
		"additionalProperties": false,
	}
}

// completenessProperty and stoppedAtProperty are shared between both schemas
// so the two pass protocols cannot drift into asking the same thing two
// different ways.
func completenessProperty() map[string]any {
	return map[string]any{
		"type":        "number",
		"minimum":     0,
		"maximum":     1,
		"description": "How much of the commission this answer covers, 1 meaning whole. State this every pass, honestly: it is what tells the harness whether to keep reading or finalize.",
	}
}

func stoppedAtProperty() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": "What was not reached, when completeness is below 1. Empty when completeness is 1.",
	}
}
