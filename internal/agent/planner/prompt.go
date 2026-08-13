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

	fmt.Fprintf(&b, `

The grant for the whole graph is $%.2f. Every step takes a share of it, and the
shares must not add up to more than the grant -- money is split, never copied.

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
    needs = ["other-step"]        # optional: run after these finish OK

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
  - A review-pool type needs a subject; a type that is not review-pool must not
    have one.

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

// grant is what the planner may allocate. A step handed no budget still has to
// print a number in the format above, and printing the ceiling as zero would
// teach the model to write plans that allocate nothing.
func grant(in assignment) float64 {
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
