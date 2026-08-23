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
// The exploring half ships under two agent types, `explore` and `reader`,
// which are one implementation handed two different tool surfaces -- see
// Surface, where the price of the difference is written down.
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
	"github.com/Tutitoos/atenea/internal/allowance"
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
	Route         *route                     `json:"route"`
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

type route struct {
	Model        string            `json:"model"`
	Fallbacks    []string          `json:"fallbacks"`
	Backend      string            `json:"backend"`
	Binary       string            `json:"binary"`
	Capabilities []string          `json:"capabilities"`
	Providers    map[string]string `json:"providers"`
	Tools        []string          `json:"tools"`
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
	// Completeness and StoppedAt carry a pass's own claim about its coverage.
	// coverage is the only place that fills them in, and it refuses before
	// either is set on an answer that states no coverage at all, or claims
	// less than whole without saying where it stopped. A whole answer carries
	// the 1 it claimed rather than an absence: see coverage.
	Completeness *float64 `json:"completeness,omitempty"`
	StoppedAt    string   `json:"stopped_at,omitempty"`
	// Notices are caveats that are not failures -- mirrors contract.Report's
	// own field. A partial answer earns one naming what it did not reach.
	Notices []string `json:"notices,omitempty"`
}

// claim writes a pass's accepted coverage onto the report, and the caveat a
// short answer earns. The notice is for a reader of the result, who would
// otherwise have to open two other fields to find out the answer stops early;
// a whole answer has nothing to caveat.
func (r *report) claim(completeness *float64, stoppedAt string) {
	if completeness == nil {
		return
	}
	r.Completeness = completeness
	if *completeness >= 1 {
		return
	}
	r.StoppedAt = stoppedAt
	r.Notices = append(r.Notices, partialNotice(*completeness, stoppedAt))
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

// Explore runs the first half: look at the project through Atenea's own
// capabilities, say what is there.
func Explore(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	return run(ctx, stdin, stdout, explore)
}

// Read runs that same half over files somebody already named, with none of
// Atenea's capabilities behind it. See Surface for what that is worth and
// what it costs.
func Read(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	return run(ctx, stdin, stdout, reader)
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
	if in.Route != nil {
		if in.Route.Backend != "" {
			cfg.Model.Backend = in.Route.Backend
		}
		if in.Route.Binary != "" {
			cfg.Model.Binary = in.Route.Binary
		}
		if in.Route.Model != "" {
			if in.Type == "plan" {
				cfg.Model.Plan = in.Route.Model
				cfg.Model.PlanFallbacks = append([]string(nil), in.Route.Fallbacks...)
			} else {
				cfg.Model.Explore = in.Route.Model
				cfg.Model.ExploreFallbacks = append([]string(nil), in.Route.Fallbacks...)
			}
		}
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

// readTokens is the observed read boundary passed to model.Request. It starts
// from allowance.Tokens of the step's own dollar budget and is narrowed by a
// declared max_tokens when one is smaller. This is deliberately an observed
// boundary, not a provider hard cap: model clients may receive an in-flight
// event before they can stop reading. budget(in) is zero for an ungranted run,
// which keeps ReadTokens at zero too. readShare and tokensPerUSD lived here
// until 2026-08-15 -- see internal/allowance for the arithmetic and the
// measurements it is built on, now enforced at workflow create rather than
// merely reserved from a grant.
func readTokens(in assignment) int {
	grant := budget(in)
	if grant <= 0 {
		return 0
	}

	tokens := allowance.Tokens(grant)
	if in.Limits.MaxTokens > 0 && in.Limits.MaxTokens < tokens {
		return in.Limits.MaxTokens
	}
	return tokens
}

// Surface is the shape of the turn one built-in agent type gets: which of
// the two configured models answers it, whether it is handed Atenea's own
// capabilities as an --mcp-config, and which of the CLI's own tools it may
// call beside them.
//
// It is a type rather than three literals spread over this package because
// the tool surface is a property of the AGENT TYPE, not of the step: two
// steps of one type get the same tools whatever their objective says, and
// the floor probe that prices a type has to measure the turn a real step of
// it actually gets. A table restated in the command that prints the floor is
// a table that will one day price a surface nothing runs.
//
// What the difference is worth, measured 2026-08-15 on taxiprime-backend
// against claude-opus-5, cold: an explore turn costs $0.27 and 26,603 tokens
// of prefix before the model has read one line of the repository, and 81% of
// that is the definitions of Atenea's capabilities -- the same probe with no
// tools came back at $0.06 and 4,991 tokens. The figure does not move with
// the prompt cache: warm, the same surface read 23,278 and wrote 3,325,
// which is the identical 26,603 tokens split differently. On one real
// 18-step plan the catalog was $2.64 of a $5.29 requirement, and twelve of
// those steps read files the commission had already named and never
// dispatched a single capability. Those twelve are what `reader` is for.
type Surface struct {
	// Role is which of the two configured models answers this type's turns.
	Role model.Role
	// Capabilities says whether the turn is handed Atenea's own tools. It
	// is a bool rather than the --mcp-config itself because building that
	// config dials the service, and a caller asking what a type's surface
	// is has not asked to open a socket.
	Capabilities bool
	// Builtins are the CLI's own tools this type's turns may call. See
	// readingTools for why the list is what it is.
	Builtins []string
}

// SurfaceOf answers what turn a built-in agent runs, keyed by the name
// `agent-exec` is given -- which is args[1] of the declared type, and so is
// also the right key for a repository's own type that borrows a shipped
// command with `runs`.
//
// False is the answer for `filereader`, `reviewer` and `plan-check`: those
// three are deterministic Go on this side of the spawn, they call no model,
// and a type with no turn has neither a surface to shape nor a floor to
// price.
func SurfaceOf(agentType string) (Surface, bool) {
	switch agentType {
	case "explore":
		return exploreSurface(), true
	case "reader":
		return readerSurface(), true
	case "plan":
		return planSurface(), true
	}
	return Surface{}, false
}

// exploreSurface is what an `explore` turn gets: Atenea's own capabilities
// beside the reading built-ins.
func exploreSurface() Surface {
	return Surface{Role: model.RoleExplore, Capabilities: true, Builtins: readingTools()}
}

// readerSurface is what a `reader` turn gets: the same built-ins and nothing
// else. No --mcp-config means no capability catalog to pay for before the
// model has read a line.
func readerSurface() Surface {
	return Surface{Role: model.RoleExplore, Builtins: readingTools()}
}

// planSurface is what a `plan` turn gets: nothing at all. The planner plans
// from the exploration it was handed, and a second, unrecorded look at the
// code would mean the exploration on the record is no longer what the graph
// came from.
func planSurface() Surface { return Surface{Role: model.RolePlan} }

// readingTools is the CLI's own tools an exploring turn may call.
//
// Read, because there is no "read this file" capability, so without it the
// explorer can find a symbol and never see the code around it; Glob, because
// it is how a tree with no index gets learned.
//
// Grep is deliberately absent: it is `code.search`, and leaving both on is
// precisely what let the first three explorations spend $1.87 answering
// nothing while dispatching zero capabilities. Bash is absent because a
// read-only agent that can run a shell is read-only by hope. The list is
// built fresh on every call so that nothing downstream can edit the surface
// of every future turn by appending to a shared slice.
func readingTools() []string { return []string{"Read", "Glob"} }

// explore is the `explore` agent type: the exploring half with Atenea's own
// capabilities behind it.
func explore(ctx context.Context, in assignment, _ config.Config, d deps) report {
	return exploring(ctx, in, d, exploreSurface())
}

// reader is the `reader` agent type: the same function, the same role, the
// same schema and the same answer, on the cheaper surface. It calls exploring
// rather than copying it because a copy is how the two would come to differ
// in something other than their tools.
func reader(ctx context.Context, in assignment, _ config.Config, d deps) report {
	return exploring(ctx, in, d, readerSurface())
}

func exploring(ctx context.Context, in assignment, d deps, s Surface) report {
	var tools string
	if s.Capabilities {
		got, err := d.tools()
		if err != nil {
			// This surface's whole job is to use Atenea's own capabilities.
			// With no service to reach them through it did not do badly --
			// it could not run, which is `incomplete` with an `unavailable`
			// reason and never a refusal of the project it never looked at.
			//
			// A `reader` never reaches this branch, and that is a property
			// worth having on purpose: a step that reads files somebody
			// already named does not need the service up to read them.
			return unavailable(contract.MessageOf(err))
		}
		tools = got
	}

	answer, err := d.client.Turn(ctx, model.Request{
		Role:      s.Role,
		Prompt:    explorePrompt(in, s),
		Schema:    exploreSchema(),
		Dir:       repositoryRoot(in),
		BudgetUSD: budget(in),
		// ReadTokens holds back readShare's complement for the answer -- see
		// readShare and tokensPerUSD for why this is tokens, not dollars.
		ReadTokens: readTokens(in),
		Tools:      tools,
		Builtins:   s.Builtins,
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
	completeness, stoppedAt, refusal := coverage(answer)
	if refusal != nil {
		return *refusal
	}

	got := report{
		Verdict: "ok",
		Result: map[string]any{
			SummaryField:  out.Summary,
			FindingsField: out.Findings,
		},
		Spent:   spent(answer.Spent),
		Notices: append([]string(nil), answer.Notices...),
	}
	got.claim(completeness, stoppedAt)
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

	// The planner's own surface, read from the same table the floor probe
	// prices: nothing at all, which is why no --mcp-config is built here and
	// the service is never dialed. See planSurface.
	s := planSurface()
	answer, err := d.client.Turn(ctx, model.Request{
		Role:      s.Role,
		Prompt:    planPrompt(in, cfg),
		Schema:    planSchema(),
		Dir:       repositoryRoot(in),
		BudgetUSD: budget(in),
		// ReadTokens holds back readShare's complement for the answer -- see
		// readShare and tokensPerUSD for why this is tokens, not dollars.
		ReadTokens: readTokens(in),
		Builtins:   s.Builtins,
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
	completeness, stoppedAt, refusal := coverage(answer)
	if refusal != nil {
		return *refusal
	}

	got := report{
		Verdict: "ok",
		Result:  map[string]any{PlanField: out.Plan},
		Spent:   spent(answer.Spent),
		Notices: append([]string(nil), answer.Notices...),
	}
	got.claim(completeness, stoppedAt)
	return got
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

// coverage turns a pass's own completeness claim into the report's fields, or
// a refusal when the claim cannot be trusted.
//
// An absent claim is refused, not defaulted. Both schemas mark `completeness`
// required and the prompt asks for it every pass, so a turn that answers
// without one broke a protocol it was told about twice -- and the two readings
// available cost the same to compute and disagree about everything: read as 1,
// a turn that stopped a fifth of the way in is recorded as a whole answer no
// counter downstream can tell from a real one. Measured 2026-08-15: one reader
// step of nineteen came back `ok` with `completeness` NULL, and its own summary
// began "I cannot describe this project". Defaulting is the engine asserting
// coverage nobody measured; a refusal costs the same turn its verdict and
// names why.
//
// A stated claim is kept even when it is 1. Nil-ing it made "claimed whole"
// and "never claimed" the same row on disk, which is why the question this
// rule answers could not be answered from 62 stored `ok` steps: the record has
// to distinguish a claim from its absence, or the next instance is undiagnosable
// too. `completeness` NULL now means one thing only -- an agent type that calls
// no model, and so makes no claim about coverage.
//
// Below 1 it is a partial, and a partial that will not say where it stopped is
// not auditable -- `ok` with an unnamed gap reads as whole to every counter
// downstream of it, which is worse than the empty answer the existing refusals
// already catch. `ok` with completeness and stopped_at named is the honest
// shape: the harness told it to answer with what it had, and it did.
func coverage(a model.Answer) (completeness *float64, stoppedAt string, refusal *report) {
	if a.Completeness == nil {
		out := refused(
			"the model answered without stating completeness, which both schemas require: "+
				"an answer that does not say how much of the commission it covers cannot be "+
				"told from a whole one", a.Spent)
		return nil, "", &out
	}
	if *a.Completeness >= 1 {
		return a.Completeness, "", nil
	}
	if strings.TrimSpace(a.StoppedAt) == "" {
		out := refused(fmt.Sprintf(
			"the model answered completeness %.2f with no stopped_at: a partial that will not say where it stopped is not auditable",
			*a.Completeness), a.Spent)
		return nil, "", &out
	}
	return a.Completeness, a.StoppedAt, nil
}

// partialNotice is the caveat a partial answer earns, naming what it did not
// reach so a reader of the result does not have to open completeness and
// stopped_at separately to find out the answer is short.
func partialNotice(completeness float64, stoppedAt string) string {
	return fmt.Sprintf("this answer is partial: %.0f%% complete, stopped at %s", completeness*100, stoppedAt)
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
