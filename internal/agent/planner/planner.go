// Package planner is the orchestrator, split in two.
//
// One commission produces two agent runs. `explore` looks at the project and
// says what it found; `plan` reads that finding and returns a graph. They are
// two because the retry is what decides it: a merged agent asked for a second
// try re-explores, paying the expensive half again to fix the cheap half.
// Split, the relaunch redispatches only the planner against the exploration
// that already passed -- and "explored well, planned badly" is two verdicts on
// the record instead of one.
//
// The plan comes back as TOML text in a single result field, not as a JSON
// graph Atenea re-encodes. Three reasons, and the third is the one that
// matters: there is one plan format rather than two, every mistake a model can
// make lands in the compiler where the refusal has a sentence worth reading,
// and the bytes a reviewer approved are the bytes the engine runs.
//
// Neither of these agents judges its own output. `plan-check` compiles the
// graph, and its refusal is what the second attempt is handed.
package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Tutitoos/atenea/internal/agent/model"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The result fields these two agents answer in. They are constants because
// plan-check reads the second one and the settings file declares both: three
// places that have to agree, and a typo in any of them is a refusal nobody
// can explain.
const (
	// SummaryField is what the project looks like for this commission.
	SummaryField = "summary"
	// FindingsField is the concrete part: which files and areas the
	// commission touches.
	FindingsField = "findings"
	// PlanField is the graph, as TOML.
	PlanField = "plan"
)

// assignment is the half of the wire these agents read.
type assignment struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Task   task   `json:"task"`
	Limits struct {
		MaxSeconds float64 `json:"max_seconds"`
		MaxTokens  int     `json:"max_tokens"`
	} `json:"limits"`
	BudgetUSD *float64 `json:"budget_usd"`
	// CommissionUSD is what the run this planner belongs to was granted --
	// the figure a graph written here divides. BudgetUSD above is this one
	// turn's own allowance, and dividing that instead was measured: eleven
	// runs allocated the same $0.90 whether the commission granted $3.50 or
	// $10.00, because $0.90 was the plan step's own share.
	CommissionUSD *float64                   `json:"commission_usd"`
	Context       map[string]json.RawMessage `json:"context"`
	// Subject is the exploration this plan is built from. Rejected is this
	// planner's own last graph, refused by the compile reviewer: two cards,
	// because a second attempt needs the finding AND the complaint.
	Subject  *subject `json:"subject"`
	Rejected *subject `json:"rejected"`
}

type task struct {
	Objective string   `json:"objective"`
	Files     []string `json:"files"`
	Criterion string   `json:"criterion"`
}

type subject struct {
	RunID     string         `json:"run_id"`
	Type      string         `json:"type"`
	Attempt   int            `json:"attempt"`
	Task      task           `json:"task"`
	Result    map[string]any `json:"result"`
	Verdict   string         `json:"verdict"`
	Reason    *reason        `json:"reason"`
	Rejection *reason        `json:"rejection"`
}

type report struct {
	Result     map[string]any `json:"result"`
	Verdict    string         `json:"verdict"`
	Reason     *reason        `json:"reason,omitempty"`
	Discovered []discovery    `json:"discovered,omitempty"`
	Spent      *charge        `json:"spent,omitempty"`
}

type reason struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type discovery struct {
	Level string `json:"level"`
	Note  string `json:"note"`
}

type charge struct {
	InputTokens      int      `json:"input_tokens"`
	OutputTokens     int      `json:"output_tokens"`
	CacheReadTokens  int      `json:"cache_read_tokens"`
	CacheWriteTokens int      `json:"cache_write_tokens"`
	USD              *float64 `json:"usd,omitempty"`
	PricedBy         string   `json:"priced_by,omitempty"`
}

// Explore runs the first half: look at the project, say what is there.
func Explore(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	return run(ctx, stdin, stdout, explore)
}

// Plan runs the second half: read the exploration, return a graph.
func Plan(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	return run(ctx, stdin, stdout, plan)
}

// turn is one of the two halves. It is handed everything it needs so that both
// halves share the reading, the settings and the answering, and differ only in
// what they ask the model.
type turn func(ctx context.Context, in assignment, cfg config.Config, d deps) report

// deps is what a turn is allowed to reach outside itself: one model, and one
// way to hand that model Atenea's own capabilities. Both are interfaces rather
// than the concrete client because the sorting these two agents do -- an answer
// in the wrong shape, an empty answer, a turn that died holding a charge -- is
// the part worth testing, and a test that needs a live model to reach it is a
// test nobody runs.
type deps struct {
	client caller
	tools  func() (string, error)
}

// caller is the half of the model client these agents use.
type caller interface {
	Turn(ctx context.Context, req model.Request) (model.Answer, error)
}

func run(ctx context.Context, stdin io.Reader, stdout io.Writer, do turn) error {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading the assignment: %w", err)
	}
	var in assignment
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("the assignment is not readable: %w", err)
	}

	// The settings are read here rather than served as context: what these
	// agents need is the declared model role and the declared agent types,
	// and neither is one of the four context levels.
	//
	// Effective settings, from the repository this assignment names. A type a
	// repository declared in its own `.atenea/config.toml` is one this run may
	// spawn, so a planner reading the global file alone writes graphs against
	// a menu that is missing them -- which is what happened on 2026-08-14, on
	// a real run, with the type accepted everywhere else. The directory comes
	// from the assignment rather than from `os.Getwd`, because the working
	// directory of a spawned agent is whatever its spawner chose.
	where := repositoryRoot(in)
	if where == "" {
		// No repository level: nothing names a tree, so fall back to the one
		// this process was started in.
		where = "."
	}
	cfg, cfgErr := config.LoadEffectiveIn("", where)
	if cfgErr != nil {
		return answer(stdout, unavailable("the settings could not be read, so no model was called: "+
			contract.MessageOf(cfgErr)))
	}
	if in.BudgetUSD != nil && *in.BudgetUSD == 0 {
		// Granted none is not the same as ungranted. These two agents cannot
		// work without calling a model, and a model call costs money, so a
		// zero share is an authorization this run does not have. Refusing is
		// the honest answer; spending anyway on a grant of nothing is the
		// failure the whole grant column exists to prevent.
		return answer(stdout, report{
			Result:  map[string]any{},
			Verdict: "failed",
			Reason: &reason{
				Kind: "permission_denied",
				Text: "granted $0.00: this agent calls a model, and no model call is free. " +
					"Give the step a share of the grant with `budget_usd`, or hand this work " +
					"to an agent type that spends nothing.",
			},
		})
	}
	// The settings struct is config's own and carries the same fields in the
	// same order on purpose: internal/config cannot import this package's
	// model client without a cycle, so the conversion is the seam.
	client, clientErr := model.New(model.Options(cfg.Model))
	if clientErr != nil {
		return answer(stdout, unavailable(contract.MessageOf(clientErr)))
	}
	return answer(stdout, do(ctx, in, cfg, deps{client: client, tools: model.AteneaTools}))
}

func answer(stdout io.Writer, out report) error {
	return json.NewEncoder(stdout).Encode(out)
}

func explore(ctx context.Context, in assignment, cfg config.Config, d deps) report {
	tools, err := d.tools()
	if err != nil {
		// The explorer's whole job is to use Atenea's own capabilities. With
		// no service to reach them through it did not do badly -- it could
		// not run, which is `incomplete` with an `unavailable` reason and
		// never a refusal of the project it never looked at.
		return unavailable(contract.MessageOf(err))
	}

	answer, err := d.client.Turn(ctx, model.Request{
		Role:      model.RoleExplore,
		Prompt:    explorePrompt(in),
		Schema:    exploreSchema(),
		Dir:       repositoryRoot(in),
		BudgetUSD: budget(in),
		Tools:     tools,
		// Read and Glob, and nothing else. There is no "read this file"
		// capability, so without Read the explorer can find a symbol and
		// never see the code around it; Glob is how it learns a tree it has
		// no index of yet.
		//
		// Grep is deliberately absent: it is `code.search`, and leaving both
		// on is precisely what let the first three explorations spend $1.87
		// answering nothing while dispatching zero capabilities. Bash is
		// absent because a read-only agent that can run a shell is read-only
		// by hope.
		Builtins: []string{"Read", "Glob"},
	})
	if err != nil {
		return fromModelError(err, answer.Spent)
	}

	var out struct {
		Summary  string `json:"summary"`
		Findings string `json:"findings"`
	}
	if err := json.Unmarshal(answer.Structured, &out); err != nil {
		return refused("the model's answer is not in the shape it was given: "+err.Error(), answer.Spent)
	}
	if strings.TrimSpace(out.Summary) == "" || strings.TrimSpace(out.Findings) == "" {
		return refused("the model answered with an empty exploration", answer.Spent)
	}

	got := report{
		Verdict: "ok",
		Result: map[string]any{
			SummaryField:  out.Summary,
			FindingsField: out.Findings,
		},
		Spent: spent(answer.Spent),
	}
	// What was learned outlives the commission. A note is a sentence, not a
	// transcript: the ceiling truncates, and a truncated paragraph teaches
	// the next run nothing.
	for _, note := range firstSentences(out.Findings, 3) {
		got.Discovered = append(got.Discovered, discovery{Level: "repository", Note: note})
	}
	return got
}

func plan(ctx context.Context, in assignment, cfg config.Config, d deps) report {
	if in.Subject == nil {
		return unavailable("nothing to plan from: this assignment carries no exploration")
	}
	if v := in.Subject.Verdict; v != "ok" {
		// The exploration is the planner's whole input. Planning on top of a
		// finding nobody accepted would produce a graph whose grounds are a
		// run that said it fell short.
		return unavailable(fmt.Sprintf("the exploration came back %s, so there is nothing to plan from: %s",
			v, reasonText(in.Subject)))
	}

	answer, err := d.client.Turn(ctx, model.Request{
		Role:      model.RolePlan,
		Prompt:    planPrompt(in, cfg),
		Schema:    planSchema(),
		Dir:       repositoryRoot(in),
		BudgetUSD: budget(in),
	})
	if err != nil {
		return fromModelError(err, answer.Spent)
	}

	var out struct {
		Plan string `json:"plan"`
	}
	if err := json.Unmarshal(answer.Structured, &out); err != nil {
		return refused("the model's answer is not in the shape it was given: "+err.Error(), answer.Spent)
	}
	if strings.TrimSpace(out.Plan) == "" {
		return refused("the model answered with an empty plan", answer.Spent)
	}
	return report{
		Verdict: "ok",
		Result:  map[string]any{PlanField: out.Plan},
		Spent:   spent(answer.Spent),
	}
}

// fromModelError sorts a failed turn, keeping the charge whatever happened.
//
// A turn that ran for a minute and then died occupied the machine for that
// minute and was billed for it. Dropping its usage because it failed is how a
// baseline learns that failures are free.
func fromModelError(err error, c contract.Charge) report {
	out := unavailable(contract.MessageOf(err))
	if contract.KindOf(err) == contract.FailureInvalidInput {
		out = refused(contract.MessageOf(err), c)
	}
	out.Spent = spent(c)
	return out
}

// refused is the verdict for an answer that arrived and is wrong. It earns a
// relaunch.
func refused(text string, c contract.Charge) report {
	return report{
		Result:  map[string]any{},
		Verdict: "failed",
		Reason:  &reason{Kind: "invalid_input", Text: text},
		Spent:   spent(c),
	}
}

// unavailable is the verdict for work that could not be done at all. It is
// `incomplete` rather than `failed` for the reason that word exists: the agent
// did not do badly, it never got to try, and a relaunch of it would repeat the
// same outage.
func unavailable(text string) report {
	return report{
		Result:  map[string]any{},
		Verdict: "incomplete",
		Reason:  &reason{Kind: "unavailable", Text: text},
	}
}

// spent converts a charge for the wire, and returns nil when there is nothing
// to report. Nil is what unmeasured looks like: a zeroed charge written out in
// full would read as a run that cost nothing.
func spent(c contract.Charge) *charge {
	if !c.Measured() {
		return nil
	}
	out := &charge{
		InputTokens:      c.InputTokens,
		OutputTokens:     c.OutputTokens,
		CacheReadTokens:  c.CacheReadTokens,
		CacheWriteTokens: c.CacheWriteTokens,
		PricedBy:         c.PricedBy,
	}
	if c.USD != nil {
		amount := *c.USD
		out.USD = &amount
	}
	return out
}

// budget is what the model client is told. Nil means nobody granted money --
// every dispatch outside a workflow -- and the client reads zero as "no
// ceiling". The other reading of zero, a share of nothing, never reaches here:
// run refuses it above.
func budget(in assignment) float64 {
	if in.BudgetUSD == nil {
		return 0
	}
	return *in.BudgetUSD
}

func reasonText(s *subject) string {
	if s.Reason == nil || strings.TrimSpace(s.Reason.Text) == "" {
		return "no reason given"
	}
	return s.Reason.Text
}

// repositoryRoot reads the one context level both halves use.
func repositoryRoot(in assignment) string {
	raw, ok := in.Context["repository"]
	if !ok {
		return ""
	}
	var level struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal(raw, &level); err != nil {
		return ""
	}
	if level.Root == "" {
		return ""
	}
	if _, err := os.Stat(level.Root); err != nil {
		return ""
	}
	return level.Root
}

// firstSentences cuts prose into at most n notes short enough to survive the
// discovery ceiling whole.
func firstSentences(text string, n int) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		for _, sentence := range strings.SplitAfter(line, ". ") {
			trimmed := strings.TrimSpace(sentence)
			if trimmed == "" || len(trimmed) > contract.MaxDiscoveryLength {
				continue
			}
			out = append(out, trimmed)
			if len(out) == n {
				return out
			}
		}
	}
	return out
}

// declaredTypes is the menu the planner writes against: every agent type this
// machine has, with what it does, what it may cause, and whether it is the
// repository's own.
//
// The origin is marked because a type a repository declared is a fact about
// the project as much as a capability -- a `migrations-reviewer` says this
// codebase has migrations worth auditing separately. The marker is written
// here, from a flag the merge set, never from a field a file could fill: the
// only repository-authored text on the line is the summary, and that one is
// held to a single control-character-free line so it cannot forge a second
// entry.
//
// It is assembled here rather than served as context because a planner that
// has to guess at the names writes graphs that refuse to compile, and the
// refusal it gets back -- "no agent type %q: declared are ..." -- is the same
// list arriving one round trip and one model call later.
func declaredTypes(cfg config.Config) string {
	lines := make([]string, 0, len(cfg.Agents))
	for _, declared := range cfg.Agents {
		effects := make([]string, 0, len(declared.Effects))
		for _, effect := range declared.Effects {
			effects = append(effects, effect.String())
		}
		origin := ""
		if declared.Local {
			origin = ", this repository's own"
		}
		line := fmt.Sprintf("- %s (%s%s): %s. effects: %s",
			declared.Spec.Name, declared.Pool.String(), origin, declared.Summary,
			strings.Join(effects, ", "))
		switch {
		case declared.Pool == config.PoolReview:
			line += ". Review pool: every step of this type needs `subject = \"<step id>\"`" +
				" naming the step it audits."
		case declared.ReadsSubject:
			line += ". Reads a subject: `subject = \"<step id>\"` hands this step that" +
				" step's whole answer as its input."
		default:
			line += ". Reads no subject: it is handed its objective and files, never" +
				" another step's answer."
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
