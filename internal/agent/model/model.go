// Package model is the seam through which an Atenea-built agent calls a
// model of its own.
//
// internal/adapter/claudecode answers a capability the core dispatches to a
// funnel of providers, and the whole point of that shape is that any of them
// could have answered instead. The two built-in agents this package serves --
// explore and plan -- are not choosing between providers: internal/config's
// own [model] table fixes which model each one gets, by role, in the
// settings file rather than in code. So this package is not a funnel entry
// and never ranks anything. It drives the same CLI claudecode does, the same
// envelope and the same measured traps, reworded here where they bear on a
// caller that already knows which model it is calling and just needs the
// turn run.
package model

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	agentopencode "github.com/Tutitoos/atenea/internal/agent/opencode"
	"github.com/Tutitoos/atenea/internal/allowance"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/ipc"
	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/internal/toolversion"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const (
	// BackendClaude selects Claude Code's native envelope and stream protocol.
	BackendClaude = "claude"
	// BackendOpenCode selects OpenCode's isolated JSON event protocol.
	BackendOpenCode = "opencode"
)

// DefaultBinary is the command looked up on PATH when Options names none.
const DefaultBinary = "claude"

// DefaultTimeout caps one turn when neither the request nor Options says
// otherwise.
//
// Chosen, not measured. claudecode's own 90s came from timing two
// code.search turns -- a single narrow capability, bounded at a $0.25
// ceiling. Explore and plan are broader: a whole repository to read, a plan
// to reason through, and each carries its own larger Request.BudgetUSD, so
// the ceiling that actually stops a runaway turn is --max-budget-usd, not
// this. All this guards against is a turn stuck on a lock or a dead socket,
// spending nothing and going nowhere. Five minutes gives that kind of turn
// real room without becoming a leash nobody would wait out.
const DefaultTimeout = 5 * time.Minute

// Role picks which of internal/config's two configured models a Turn calls.
//
// A closed set of two rather than a free string, because the whole point of
// fixing model choice by role in settings (see internal/config's Model) is
// that a caller cannot name a third one. A built-in agent this package
// grows a third role for widens this list and internal/config's Model
// together, deliberately, as a settings-file change and a Go change in the
// same commit.
type Role string

const (
	// RoleExplore is the read-only agent that gathers context before a plan
	// is written.
	RoleExplore Role = "explore"
	// RolePlan is the agent that turns gathered context into a workflow
	// graph.
	RolePlan Role = "plan"
)

// Options configure a Client. Everything here is what internal/config's
// [model] table fixes, so retuning it never means touching Go.
type Options struct {
	// Backend selects the model CLI protocol. Empty means BackendClaude.
	Backend string
	// Binary is the CLI executable. A bare name is looked up on PATH.
	Binary string
	// Timeout is the fallback a Request takes when it does not set its own.
	// Zero takes DefaultTimeout.
	Timeout time.Duration
	// Explore is the model Client calls for RoleExplore turns: an alias the
	// CLI resolves itself ("sonnet", "opus") or a full name.
	Explore string
	// Plan is the model Client calls for RolePlan turns.
	Plan string
	// ExploreFallbacks and PlanFallbacks are explicit model names to try when
	// the primary model is unavailable or overloaded. Claude's plan role is
	// pinned by the decision/config layers, so its lower-reasoning fallback
	// list is intentionally ignored here as a final dispatch guard.
	ExploreFallbacks []string
	PlanFallbacks    []string
}

// Client calls a model for whichever caller holds it.
//
// One Client for both roles, not one each: Options fixes both explore's and
// plan's model once, at construction, and Request.Role picks between them
// per Turn. A caller working through both roles in the same run -- explore
// then plan -- reuses the one Client instead of juggling two.
type Client struct {
	backend          string
	binary           string
	timeout          time.Duration
	explore          string
	plan             string
	exploreFallbacks []string
	planFallbacks    []string
	opencode         *agentopencode.Runner
	// version answers what the CLI calls itself, for Floor's CLIVersion --
	// memoised for the life of this Client, the same tradeoff
	// internal/toolversion's own doc explains: an upgrade on disk underneath
	// a live Atenea is picked up at the next restart, not mid-process.
	version *toolversion.Probe
}

// New validates the options and returns a Client.
//
// A missing binary is deliberately not an error here, the same reason it is
// not in claudecode's adapter: a client nobody installed is discovered as
// FailureUnavailable on the first Turn, not refused before it was ever asked
// to do anything. A role left with no model name is discovered the same
// lazy way, by whichever Turn asks for it.
func New(opts Options) (*Client, error) {
	if opts.Timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"model client: timeout must not be negative, got %s", opts.Timeout)
	}
	backend := strings.ToLower(strings.TrimSpace(opts.Backend))
	if backend == "" {
		backend = BackendClaude
	}
	if backend != BackendClaude && backend != BackendOpenCode {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"model client: unknown backend %q", opts.Backend)
	}
	client := &Client{
		backend:          backend,
		binary:           strings.TrimSpace(opts.Binary),
		timeout:          opts.Timeout,
		explore:          strings.TrimSpace(opts.Explore),
		plan:             strings.TrimSpace(opts.Plan),
		exploreFallbacks: append([]string(nil), opts.ExploreFallbacks...),
		planFallbacks:    append([]string(nil), opts.PlanFallbacks...),
	}
	if client.binary == "" {
		if backend == BackendOpenCode {
			client.binary = agentopencode.DefaultBinary
		} else {
			client.binary = DefaultBinary
		}
	}
	if client.timeout == 0 {
		client.timeout = DefaultTimeout
	}
	if backend == BackendOpenCode {
		runner, err := agentopencode.New(agentopencode.Options{Binary: client.binary, Timeout: client.timeout})
		if err != nil {
			return nil, err
		}
		client.opencode = runner
	}
	client.version = toolversion.New(client.binary, "--version")
	return client, nil
}

// modelFor resolves which model name backs one Role.
//
// Checked here, inside args, rather than eagerly in New: a Client built with
// one role's model unconfigured is only a problem for whichever Turn asks
// for that role, the same lazy discovery a missing binary gets in invoke.
func (c *Client) modelFor(role Role) (string, error) {
	var name string
	switch role {
	case RoleExplore:
		name = c.explore
	case RolePlan:
		name = c.plan
	default:
		// Request.Validate already refuses any other Role before Turn ever
		// reaches here; this default is what makes a third Role added to
		// the type without a case here fail loudly instead of silently
		// asking the CLI for "".
		return "", contract.Fail(contract.FailureInvalidInput,
			"request: role %q is not explore or plan", role)
	}
	if name == "" {
		return "", contract.Fail(contract.FailureInvalidInput,
			"request: role %q has no model configured", role)
	}
	return name, nil
}

// ateneaServer is the key this package registers Atenea's tools under, and
// the name the CLI's allow-list addresses them by (`mcp__<server>`). One
// constant because the two have to agree: rename the key alone and the turn
// is handed a server it is then forbidden to call.
const ateneaServer = "atenea"

// ateneaToolsConfig builds the MCP config AteneaTools hands to --mcp-config:
// one stdio server, `atenea mcp`, the same bridge cmd/atenea/mcp.go relays
// for every other MCP client.
//
// The command is this process's own path, not the bare name. A model CLI is
// a third party looked up on PATH because the settings named it; Atenea is
// the program already running, and it can say where it is -- the same reason
// the shipped agent declarations spawn `$atenea` rather than `atenea`. A
// bare name fails exactly where it hurts: an install that is not on the
// spawned CLI's PATH gets a model with no tools, and an exploration that
// looks merely thin.
func ateneaToolsConfig() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", contract.Fail(contract.FailureUnavailable,
			"cannot name this binary to the model, so it would be given no atenea tools: %v", err)
	}
	cfg, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			ateneaServer: map[string]any{"command": self, "args": []string{"mcp"}},
		},
	})
	if err != nil {
		return "", contract.Fail(contract.FailureInvalidInput, "building the mcp config: %v", err)
	}
	return string(cfg), nil
}

// AteneaTools builds an MCP config that gives a Turn Atenea's own tools, so
// a model-backed agent reaches Atenea's catalog exactly the way any other
// MCP client does -- through the same socket cmd/atenea/mcp.go relays,
// spawned here as the turn's own MCP server subprocess.
//
// The socket is probed before anything is built or spawned, for the same
// reason cmd/atenea/mcp.go probes it before relaying: a turn that spawned
// `atenea mcp` against a dead socket would fail inside that subprocess, and
// the CLI would report it as a garbled "MCP server failed to start" instead
// of this package's own FailureUnavailable. Dialing first gives one clear
// answer instead of a second place the same failure could be worded
// differently.
//
// The refusal sentence below is copied from cmd/atenea/mcp.go's own dial,
// word for word, because package main cannot be imported and a caller told
// the service is down should be told it the same way regardless of which
// door it knocked on. Keep the two in sync by hand.
func AteneaTools() (string, error) {
	conn, err := ipc.Dial(core.SocketPath())
	if err != nil {
		return "", contract.Fail(contract.FailureUnavailable,
			"no atenea service is listening at %s: start it with `systemctl --user start atenea.service` "+
				"(or `atenea service install` first), then reconnect this client",
			core.SocketPath())
	}
	_ = conn.Close()
	return ateneaToolsConfig()
}

// Request is one model turn.
type Request struct {
	// Role picks which of the Client's two configured models this turn
	// calls.
	Role Role
	// Prompt is what the model is told, verbatim -- the caller's whole
	// instruction, not a template this package fills in. Explore and plan
	// each know what they are asking; this package only knows how to ask it.
	Prompt string
	// Schema is the JSON Schema the answer must match, encoded the one way
	// the CLI's --json-schema flag takes it. Nil means free text:
	// Answer.Text carries the whole answer and Answer.Structured stays nil.
	Schema map[string]any
	// Dir is the working directory the turn runs in. Empty inherits this
	// process's own.
	Dir string
	// BudgetUSD is the ceiling this one turn may spend. Zero is a valid
	// request, not a mistake -- it means the caller is granting this turn no
	// ceiling of its own, not that the turn may spend nothing. A caller that
	// truly meant "spend nothing" has no reason to call Turn at all. See
	// Validate for the one value this field does refuse.
	BudgetUSD float64
	// MaxTokens is the declared total token ceiling. Provider CLIs do not
	// expose one uniform hard flag, so model clients enforce it at the local
	// answer boundary and reject an over-limit result.
	MaxTokens int
	// ReadTokens is the soft allowance, in input-equivalent tokens, that
	// this turn may spend before it is told to stop reading and answer with
	// what it has. Zero means off, and a turn with it off is the single shot
	// it was before this field existed.
	//
	// It protects a turn that CALLS TOOLS, and only that turn. Measured
	// 2026-08-15 on the production argv: a turn with no tools emits two
	// assistant events in 133 seconds -- one at 31.6s, one arriving with
	// the result -- and both carry the usage of the prompt alone
	// (output_tokens: 2), never the answer accumulating behind them. A
	// tool-using turn gets a fresh event per round trip, which is the only
	// reason an accumulator can climb at all. So a toolless turn offers one
	// usable observation, a quarter of the way in, carrying a number that
	// does not move: if the allowance is not already crossed there it never
	// will be. `plan` is that turn, and it died at a $0.90 share whose
	// allowance cleared the threshold every surviving step cleared.
	//
	// And the allowance has a floor of its own, below which this field is
	// worse than useless. Measured 2026-08-15 on a nineteen-step plan: all
	// thirteen model-backed steps died empty, every one of them funded with
	// an allowance between 9,960 and 33,200 -- under the ~70,000 named
	// below as the weight of a turn's own first event. Under that line the
	// nudge does not fail to fire, it fires on the arrival of the prompt,
	// telling a model that has not opened a file to answer with what it
	// has. Above it, four for four answered. A caller setting this field
	// below ~70,000 has bought the empty death and paid for the message
	// too; internal/allowance is where that arithmetic lives now, and
	// `atenea workflow create` refuses a step funded below what it derives
	// rather than merely leaving the number available.
	//
	// Measured 2026-08-14: twelve of twelve explore steps hit BudgetUSD and
	// came back with result_len 0. $3.78, and then $4.09 on the re-run,
	// bought no answers at all, because the kill lands in exactly the wrong
	// place -- after a turn has paid to read everything and before it has
	// written anything. A real turn stopped at its ceiling prints no
	// `result` field whatsoever; see envelope.TerminalReason. Raising the
	// ceiling does not move the kill, it only moves where the same empty
	// death happens.
	//
	// Tokens, and not dollars, for two measured reasons that compound. The
	// CLI reports no cost mid-turn at all -- a figure appears only on the
	// `result` event that ends a turn -- so a dollar allowance can only ever
	// be checked at a turn boundary. And the steps this exists for never
	// reach one: measured on the re-run, an explore step does all of its
	// work inside turn 1, so the first result event never arrives and a
	// boundary-checked allowance never fires. It did not fire: the re-run
	// with a dollar allowance of 0.165 of 0.22 died exactly as before, 12 of
	// 12, zero passes. Usage, unlike cost, is on the stream continuously:
	// every assistant event carries the usage of its own request, which is
	// what makes a mid-turn trigger possible at all. See weigh for the
	// arithmetic and conversation.spend for where it fires.
	//
	// The unit is input-equivalent tokens: every kind of token weighed by its
	// price relative to an input token. Do not convert a dollar share by hand
	// here: the rate is allowance.tokensPerUSD, and it is deliberately not
	// the 333,333 the weighting implies by construction. Reconciled
	// 2026-08-14 against two real receipts, a short turn came out at 332,700
	// per dollar and a reading-heavy explore turn at 166,200 -- half as many
	// -- so allowance took the pessimistic end. This paragraph carried the
	// derogated figure, and a caller converting with it reserves twice the
	// tokens the money buys, which puts the nudge past the ceiling that kills
	// the turn. Call allowance.Tokens and see weigh for how the ratios were
	// checked against a real receipt.
	//
	// The figure is in tens of thousands, and a caller guessing in thousands
	// has written an allowance that fires on the first event of every turn.
	// Measured 2026-08-14, one live turn surveying this repository: the very
	// first assistant event already weighed 65,625 input-equivalent tokens --
	// about $0.20 -- because the CLI's system prompt and tool definitions are
	// cached on it, 32,799 cache-creation tokens against `input_tokens: 2`.
	// The whole 14-second turn weighed 87,004, which is the $0.26 the CLI
	// charged for it. So an allowance under ~70,000 buys no reading at all on
	// this CLI, and one over the ceiling's worth buys the old empty death.
	//
	// What pays for the answer is whatever BudgetUSD has left when the nudge
	// lands, so this figure and that one are set together even though they
	// are in different units: an allowance so high that the CLI's own
	// ceiling arrives first is an allowance that never fires. Nothing here
	// can check that for the caller -- the two units only meet in a price
	// this package is not entitled to assume -- so it is the caller's
	// arithmetic to do, and internal/agent/planner does it.
	//
	// The reserve is not a rounding allowance. Measured 2026-08-14 on one
	// live turn of this exact path: an allowance of 66,000 (about $0.20)
	// against a ceiling of $0.60 was nudged as intended, and the answer it
	// then wrote -- 4,006 output tokens on top of everything already read --
	// took the turn to $0.5363. Writing cost more than reading was allowed.
	// A reserve of a few cents would have bought the nudge and then died
	// writing, which is the original failure with extra steps.
	ReadTokens int
	// Timeout caps this one turn. Zero takes the Client's own.
	Timeout time.Duration
	// Tools is an MCP config -- built by AteneaTools, a JSON string or a
	// path, the CLI's --mcp-config accepts either -- that gives the turn
	// Atenea's own tools. Empty means the turn gets none of them, only
	// whatever the CLI's built-in set still allows under --safe-mode.
	Tools string
	// Builtins names the CLI's own tools this turn may call, beside Atenea's
	// capabilities. It is a complete list, never an addition to a default:
	// nil means Atenea's tools and nothing else.
	//
	// Measured 2026-08-14, before this field existed: three explorations of
	// this repository, $1.87 and 1.05M tokens, dispatched **zero**
	// capabilities. The turn was handed Atenea's tools and the CLI's whole
	// built-in set at once, and a model given both reaches for the one it
	// has read a hundred thousand examples of. Offering a capability beside
	// the tool it was meant to replace is not offering it.
	Builtins []string
	// Stream asks for the event stream on stdout while still passing the
	// prompt as the CLI's positional argument -- what a measurement needs
	// and a real step does not: usage PER MESSAGE rather than one total for
	// the whole turn. A single-shot turn's `--output-format json` reports
	// only the sum, and the sum cannot tell the prefix apart from the block
	// that arrives with the first tool result. Measured 2026-08-15, those
	// two are 5,650 and 41,930 tokens on this machine, and charging a step
	// for the second as if it recurred is the defect this field exists to
	// let a probe measure instead of assume.
	//
	// It is ignored on a turn that reserves an answer: that path already
	// streams, and its stdin protocol takes the prompt instead. Nothing in
	// internal/agent sets this outside Client.FirstCall.
	Stream bool
}

// Validate checks the request can even be attempted.
func (r Request) Validate() error {
	if strings.TrimSpace(r.Prompt) == "" {
		return contract.Fail(contract.FailureInvalidInput, "request: prompt is required")
	}
	switch r.Role {
	case RoleExplore, RolePlan:
	default:
		return contract.Fail(contract.FailureInvalidInput,
			"request: role %q is not explore or plan", r.Role)
	}
	if r.BudgetUSD < 0 || math.IsNaN(r.BudgetUSD) || math.IsInf(r.BudgetUSD, 0) {
		// Zero is deliberately allowed: see BudgetUSD's own doc for why it
		// means "no ceiling", not "no spending" -- no accounting a caller
		// could have done on its way here produces a negative figure on
		// purpose. NaN and Inf are refused because `NaN < 0` is false, so a
		// budget arrived at by dividing by a zero share used to pass this
		// check and reach the CLI as the literal argv `--max-budget-usd NaN`.
		// opencode.Run refuses the same three values in the same words; the
		// two backends have to agree on which requests are attemptable at
		// all, or which one is configured decides whether a broken figure is
		// caught here or by a provider halfway through spending money.
		return contract.Fail(contract.FailureInvalidInput,
			"request: budget_usd must be finite and non-negative, got %v", r.BudgetUSD)
	}
	if r.ReadTokens < 0 {
		// The two figures are in different units and no comparison between
		// them belongs here: turning tokens into dollars needs a price, and
		// a price is exactly what this package refuses to assume -- see
		// pricedByCLI. Negative is the only value that is wrong on its own
		// terms.
		return contract.Fail(contract.FailureInvalidInput,
			"request: read_tokens must not be negative, got %d", r.ReadTokens)
	}
	if r.MaxTokens < 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"request: max_tokens must not be negative, got %d", r.MaxTokens)
	}
	if r.Timeout < 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"request: timeout must not be negative, got %s", r.Timeout)
	}
	return nil
}

// reservesAnswer reports whether this turn holds back an allowance for the
// answer, which is what decides between the two paths through Turn.
//
// Both conditions are the request's own, so the decision is derivable from
// it and never passed around: one predicate, read by Turn and by args, which
// is what keeps the command line from ever disagreeing with the code driving
// it.
//
// A Schema is required and not merely preferred. The reserved answer is a
// claim the model makes about its own answer -- completeness, and what it
// did not reach -- and a free-text turn has nowhere to put either: the two
// properties live in the schema the caller declared. A schema-less turn is
// therefore left as the single shot it always was.
func (r Request) reservesAnswer() bool {
	return r.ReadTokens > 0 && len(r.Schema) > 0
}

// sentPrompt is the prompt as it actually goes out, protocol and all.
//
// One function for both paths, because recordPrompt writes what this returns
// and the turn sends what this returns: a log that can disagree with the
// wire is the exact drift PromptLogEnv exists to rule out. A single-shot
// turn gets the caller's own string back unchanged, byte for byte.
func (r Request) sentPrompt() string {
	if !r.reservesAnswer() {
		return r.Prompt
	}
	return r.Prompt + "\n" + passProtocol
}

// passProtocol is what sentPrompt appends to a reserved-answer prompt: the
// paragraph that makes an answer exist before the money runs out.
//
// Measured 2026-08-14: twelve of twelve agent steps hit their ceiling and
// twelve came back with result_len 0. $3.78 bought no answers at all,
// because each step spent its whole grant reading and was killed before it
// wrote anything. A turn that answers on every pass cannot be killed empty;
// the worst it can be killed with is an answer that covers less.
//
// It says nothing about money, deliberately. The model is never given a
// dollar figure, because the figure that decides anything is read between
// passes from what the CLI reports having spent -- and there is no mid-turn
// cost signal for a model to reason about even if it were told to (see
// crossed). What it is given instead is the one instruction that survives
// being cut off at any point: answer everything, every time.
const passProtocol = `Work in passes, and treat every pass as if it were your last.
On every pass, answer the whole schema with what you have so far. Never leave a
field for a later pass and never fill one with a placeholder. Set completeness to
the fraction of the objective you have actually covered and stopped_at to what you
have not reached yet; use completeness 1 only when the objective is fully answered.
You will be told when to stop reading and answer for good.`

// Answer is what one Turn produced.
type Answer struct {
	// Text is the model's plain-text result, always populated on a clean
	// answer whether or not a Schema was asked for.
	Text string
	// Structured is the raw structured_output bytes the model answered
	// with, non-nil only when Request carried a Schema and the model
	// answered it. Left as raw JSON rather than decoded, so a caller decodes
	// it into whatever shape it actually expects instead of through this
	// package's own map and back.
	Structured json.RawMessage
	// Spent is what this turn cost. The zero value means unmeasured -- the
	// same reading contract.Charge already gives an agent that spends no
	// tokens: a Turn that failed before an envelope ever printed leaves this
	// zero, because nothing was measured, not because the turn was free.
	Spent contract.Charge
	// Notices explain a successful fallback without pretending the provider
	// error was part of the model's answer.
	Notices []string
	// Completeness is the fraction of the objective the model says its own
	// answer actually covers, or nil when it claimed nothing -- a
	// single-shot turn, or a schema the caller declared without the
	// property. Never zero: a turn that answered covered something, and a
	// figure this package could not read as a fraction is dropped to
	// unclaimed rather than repaired into one. See claimOf.
	//
	// When it is set it is greater than 0 and at most 1, and anything below
	// 1 comes with StoppedAt filled in. Both are guaranteed here so a
	// caller can hand them straight to a contract.Report, whose own
	// validation refuses either shape.
	Completeness *float64
	// StoppedAt is what the model says it did not reach. Empty on an answer
	// that claims the whole objective, and on any turn that claimed
	// nothing.
	StoppedAt string
	// Passes is how many times the model answered during this turn: 1 for a
	// single-shot turn, and on a reserved-answer turn one per pass that
	// actually produced an answer. Zero means nobody answered, so a Turn
	// reporting zero here always returns an error too.
	//
	// The converse does not hold, and reading it as if it did would be the
	// mistake to make here: a pass that answered in a shape the schema does
	// not accept, or a turn refused an action it needed, is counted and
	// still refused. Passes says what the money bought, not whether the
	// caller got an answer -- the error says that.
	Passes int
	// ToolCalls is diagnostic evidence when the backend exposes tool-use
	// events. It is empty for Claude's final envelope and tool-less turns.
	ToolCalls []string
}

// resolveDir turns a Request's Dir into the absolute path invoke actually
// runs the turn in. Empty stays empty, which inherits this process's own --
// see Request.Dir. Shared by Turn and Floor so a real turn and the floor
// probe standing in for one resolve a working directory exactly the same
// way.
func resolveDir(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", contract.Fail(contract.FailureInvalidInput, "request: dir %q: %v", dir, err)
	}
	return abs, nil
}

// Turn runs one model turn and reads back what it answered.
//
// The request is checked before anything spawns: a turn that cannot run is
// refused outright, not attempted and then blamed on the provider. Spent is
// filled in on every path once an envelope exists, including the failing
// ones -- a turn that ran for a minute and then died at its ceiling occupied
// the machine and charged for it, and leaving that off the answer would put
// the minute and the money on nobody's receipt.
//
// There are two ways to run one: a single shot, which is what the CLI does
// natively, and a conversation held open across passes, which is what
// Request.ReadTokens asks for. Only the request decides which -- see
// reservesAnswer -- and everything downstream of the spawn is shared, so the
// two paths differ in how the answer is obtained and in nothing about how it
// is read.
func (c *Client) Turn(ctx context.Context, req Request) (Answer, error) {
	if err := req.Validate(); err != nil {
		return Answer{}, err
	}
	primary, err := c.modelFor(req.Role)
	if err != nil {
		return Answer{}, err
	}
	candidates := append([]string{primary}, c.fallbacksFor(req.Role)...)
	if len(candidates) == 1 {
		return c.turnOnce(ctx, req)
	}
	var total contract.Charge
	var notices []string
	for index, candidate := range candidates {
		attempt := *c
		if req.Role == RolePlan {
			attempt.plan = candidate
			attempt.planFallbacks = nil
		} else {
			attempt.explore = candidate
			attempt.exploreFallbacks = nil
		}
		answer, turnErr := attempt.turnOnce(ctx, req)
		total = total.Plus(answer.Spent)
		answer.Spent = total
		if turnErr == nil {
			answer.Notices = append(answer.Notices, notices...)
			return answer, nil
		}
		if index == len(candidates)-1 || !fallbackRetryable(turnErr) {
			return answer, turnErr
		}
		// What the failed attempt cost decides how much of the ceiling the
		// next candidate may still have, and the two ways it can come back
		// unmeasurable are the two the chain exists for. A request with
		// BudgetUSD zero was granted no ceiling at all -- see the field's own
		// doc -- so there is nothing to subtract from and nothing to refuse
		// on. And the failures fallback is for print no envelope whatsoever:
		// a missing binary, an unauthenticated session, a provider that is
		// down. Those charge nothing, so the whole ceiling is still intact
		// and handing it to the next candidate cannot double-charge anyone.
		// Refusing the chain on either of those, as this did, meant fallback
		// only ever ran after a failure that had already paid for itself --
		// which is the rarest of them.
		spentUSD, source := retryCost(total)
		remaining, provenance := req.BudgetUSD, source
		switch {
		case req.BudgetUSD <= 0:
			provenance = "no ceiling was granted"
		case spentUSD <= 0:
			provenance = "the failed attempt reported no spend"
		default:
			remaining = req.BudgetUSD - spentUSD
			if remaining <= 0 {
				// The original failure's raw provider text survives the
				// rewording: this sentence explains why the chain stopped,
				// and the thing a human has to search for verbatim is still
				// whatever the model printed on its way out.
				return answer, contract.Fail(contract.FailurePermissionDenied,
					"primary model %s failed (%s); fallback %s refused because no budget remains",
					candidate, contract.MessageOf(turnErr), candidates[index+1]).
					WithRaw(contract.RawOf(turnErr))
			}
		}
		if req.BudgetUSD <= 0 {
			notices = append(notices, fmt.Sprintf("fallback: %s failed with %s; retrying %s with no ceiling (%s)",
				candidate, contract.KindOf(turnErr), candidates[index+1], provenance))
		} else {
			notices = append(notices, fmt.Sprintf("fallback: %s failed with %s; retrying %s with $%.2f remaining (%s)",
				candidate, contract.KindOf(turnErr), candidates[index+1], remaining, provenance))
		}
		req.BudgetUSD = remaining
	}
	return Answer{Spent: total}, contract.Fail(contract.FailureUnavailable, "all configured models failed")
}

func retryCost(c contract.Charge) (float64, string) {
	if c.USD != nil {
		return *c.USD, "provider-reported cost"
	}
	if c.Tokens() == 0 {
		return 0, ""
	}
	return allowance.EstimatedUSD(c.InputTokens, c.OutputTokens, c.CacheReadTokens, c.CacheWriteTokens),
		"conservative token estimate"
}

func fallbackRetryable(err error) bool {
	kind := contract.KindOf(err)
	return kind == contract.FailureUnavailable || kind == contract.FailureTimeout
}

func (c *Client) turnOnce(ctx context.Context, req Request) (Answer, error) {
	dir, err := resolveDir(req.Dir)
	if err != nil {
		return Answer{}, err
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = c.timeout
	}
	if c.backend == BackendOpenCode {
		return c.turnOpenCode(ctx, dir, timeout, req)
	}

	if req.reservesAnswer() {
		answer, err := c.converse(ctx, dir, timeout, req)
		return enforceMaxTokens(answer, req.MaxTokens, err)
	}

	env, err := c.invoke(ctx, dir, timeout, req)
	answer := Answer{Spent: chargeFrom(env)}
	if err != nil {
		return answer, err
	}
	text, structured, err := readAnswer(env, req)
	if err != nil {
		return answer, err
	}
	answer.Text, answer.Structured, answer.Passes = text, structured, 1
	// The claim, on this path too.
	//
	// Only converse read it, so a single-shot turn came back with
	// Completeness nil -- and the planner refuses an answer that states no
	// coverage, with "the model answered without stating completeness". A
	// turn is single-shot whenever ReadTokens is zero, which is whenever
	// there is no budget to derive an allowance from, so `atenea agent
	// explore --objective ...` and every RunReviewed with no BudgetUSD failed
	// that way after paying for the turn. The OpenCode backend has always
	// filled this in; the default backend did not.
	if claimed, ok := claimOf(env); ok {
		answer.Completeness, answer.StoppedAt = claimed.reported()
	}
	return enforceMaxTokens(answer, req.MaxTokens, nil)
}

// enforceMaxTokens refuses an answer the provider reported above the limit the
// agent type declared.
//
// It counts input and output, and deliberately not the cache lines.
//
// Charge.Tokens() sums all four, which is the right total for a bill: cached
// reads and writes are charged. It is the wrong total for this ceiling. A
// cache read is context the provider had already stored, and it dwarfs the
// rest -- 356,434 cache_read tokens observed on a single explore turn against
// a real repository -- so a limit meant to bound the size of a request was
// being compared against a number dominated by what the provider had cached
// from previous ones. Every type that calls a model declares 200,000
// (explore, reader, plan) or 100,000 (semantic-reviewer), so this refused
// completed, paid-for answers on ordinary turns and reported them as
// `unavailable`.
//
// What a provider means by max_tokens is the request, so that is what this
// holds it to.
func enforceMaxTokens(answer Answer, limit int, err error) (Answer, error) {
	if err != nil || limit <= 0 {
		return answer, err
	}
	requested := answer.Spent.InputTokens + answer.Spent.OutputTokens
	if requested <= limit {
		return answer, err
	}
	return answer, contract.Fail(contract.FailurePermissionDenied,
		"model reported %d tokens above the requested limit of %d", requested, limit).
		WithRaw(fmt.Sprintf("observed_tokens=%d max_tokens=%d cached_tokens=%d",
			requested, limit, answer.Spent.CacheReadTokens+answer.Spent.CacheWriteTokens))
}

// floorPrompt is what a Floor probe asks the model to do: nothing. It is a
// named constant, not inlined, because the one property this string needs --
// answering it costs nothing beyond starting the turn itself, no reading, no
// writing, no reasoning a real step's own prompt would pay for on top of the
// floor -- is exactly what a caller re-reading this file has to be able to
// confirm without diffing a literal against itself.
const floorPrompt = "Reply with exactly: ok"

// FloorRequest is one probe: the shape of turn internal/workflow's refusal
// wants priced, spelled out in this package's own vocabulary rather than
// repeated by the caller.
type FloorRequest struct {
	// Role picks which of the Client's two configured models the probe
	// calls -- the same Role a real step of that agent type would get.
	Role Role
	// Dir is the working directory a real step would run in. See Request.Dir.
	Dir string
	// Tools is the --mcp-config a real step would carry. See Request.Tools.
	Tools string
	// Builtins is the CLI's own tools a real step would be allowed to call
	// beside Atenea's. See Request.Builtins.
	Builtins []string
}

// FloorMeasurement is what one Floor probe found: the cost of starting a
// turn on Model, in this repository, with these tools, before any real work
// happened.
//
// It is never written down as a constant -- see internal/floor.Measurement,
// which is what stores one of these. The floor is per repository and per
// model, and it drifts as the CLI's own system prompt and tool schemas
// change, so a figure divorced from what produced it is a figure nobody can
// tell is stale.
type FloorMeasurement struct {
	USD              float64
	CacheWriteTokens int
	// CacheReadTokens is how many tokens of the cached prefix this turn read
	// back at cache-read price instead of writing fresh. On its own it says
	// nothing about what starting a turn costs -- see PrefixTokens, which is
	// the field a floor is actually built on.
	CacheReadTokens int
	// PrefixTokens is CacheWriteTokens + CacheReadTokens: the size of the
	// system-prompt-and-tool-definitions prefix this turn paid for, written
	// or read. Measured 2026-08-15, the same tool surface probed cold and
	// warm an hour apart: the cold probe wrote 26,603 tokens of cache and
	// read 0; the warm probe read 23,278 and wrote 3,325. Two different
	// splits, identical totals to the token -- 26,603 both times. The split
	// moves with cache state; PrefixTokens does not, which is why a floor is
	// built on this field and never on CacheWriteTokens alone.
	PrefixTokens int
	// FirstCallTokens is the same total for the SECOND assistant message: the
	// block that arrives with the first tool result, written or read. Zero
	// from a Floor probe, which never makes a tool call and therefore never
	// sees it; a real figure only from FirstCall. Measured 2026-08-15 on this
	// machine it is ~41,930 tokens against a ~5,650-token prefix, so a cost
	// model built on the prefix alone is describing an eighth of what a step
	// pays to get started.
	FirstCallTokens int
	InputTokens     int
	OutputTokens    int
	// Cold is true when none of this prefix was already cached --
	// CacheReadTokens == 0. It is never true just because the tool surface
	// is new: the refusal this field replaced was written an hour before
	// this doc and was measurably too strict, because a NEW tool surface can
	// never be cold either -- the machine-wide system-prompt prefix Claude
	// Code ships with is already resident server-side before the first
	// probe of it ever runs. Measured 2026-08-15: a repository never probed
	// before still came back 23,278 tokens read and only 3,325 written on
	// its first-ever probe. Only the per-repository, per-surface remainder
	// can still be genuinely cold, and only CacheReadTokens == 0 says so.
	Cold bool
	// Model is the model name Role actually resolved to. A measurement is
	// only ever valid for this exact pair with the repository, never for
	// Role alone -- internal/config can repoint a Role at a different model
	// without this package knowing, and the old figure would silently be
	// read as still describing the new one.
	Model string
	// CLIVersion is the version token `claude --version` answered right
	// after this probe ran -- the banner's first field, trimmed of
	// whatever trails it (a real answer reads "2.1.232 (Claude Code)"; this
	// keeps "2.1.232"). Empty means the probe could not get a version at
	// all, the same "silence is an answer" reading
	// internal/toolversion.Probe.Version already gives an unreachable or
	// uncooperative binary -- never a placeholder like "unknown", which
	// would print as if it were a real answer.
	//
	// The system prompt and tool schemas ship WITH the CLI, so a new CLI is
	// a new floor -- this is what lets a stale figure be spotted instead of
	// quietly trusted.
	CLIVersion string
}

// Floor measures the cost of starting a turn -- system prompt, tool
// definitions, one minimal exchange -- with nothing asked of the model
// beyond floorPrompt.
//
// CALLING THIS SPENDS REAL MONEY: one turn, priced at roughly the floor
// itself. That is exactly why nothing in this package, and nothing in
// internal/workflow, calls it implicitly -- a refusal that is allowed to pay
// for its own evidence on every check is a refusal nobody approved the cost
// of. A caller wants a stored Floor probe measurement it took once, on
// purpose, and reads it back through internal/floor; it does not call this
// to get a fresh one on every plan.
//
// The turn this runs is shaped exactly like the turn a real step of Role
// gets: the same model, the same --mcp-config, the same builtins, the same
// working directory, the same single-shot flags. Reusing invoke -- the exact
// path a real single-shot step already takes through args and command -- is
// what keeps the two identical; a Floor built by duplicating the spawn would
// drift the first time args changed and nobody remembered to change this
// too.
//
// A measurement this returns is refused, not merely noted, when it would
// mislead whoever reads it back. A turn that reports having used a tool
// (num_turns > 1, or an action the far side denied) priced real work into
// the same total the floor is meant to be, and a turn the CLI never priced
// at all has no USD to report honestly. A floor measured on a turn that did
// work is not a floor.
func (c *Client) Floor(ctx context.Context, req FloorRequest) (FloorMeasurement, error) {
	if c.backend == BackendOpenCode {
		return FloorMeasurement{}, contract.Fail(contract.FailureUnavailable,
			"opencode floor probes are not supported: its event stream does not expose the Claude cache-prefix measurement")
	}
	turn := Request{
		Role:     req.Role,
		Prompt:   floorPrompt,
		Dir:      req.Dir,
		Tools:    req.Tools,
		Builtins: req.Builtins,
	}
	if err := turn.Validate(); err != nil {
		return FloorMeasurement{}, err
	}
	modelName, err := c.modelFor(turn.Role)
	if err != nil {
		return FloorMeasurement{}, err
	}
	dir, err := resolveDir(turn.Dir)
	if err != nil {
		return FloorMeasurement{}, err
	}

	env, err := c.invoke(ctx, dir, c.timeout, turn)
	if err != nil {
		return FloorMeasurement{}, err
	}
	if env.NumTurns > 1 || len(env.PermissionDenials) > 0 {
		return FloorMeasurement{}, contract.Fail(contract.FailureInvalidInput,
			"floor probe: claude code used a tool (num_turns=%d, %d denial(s)) -- "+
				"a turn that did work is not a floor", env.NumTurns, len(env.PermissionDenials))
	}
	if env.TotalCostUSD == nil {
		return FloorMeasurement{}, contract.Fail(contract.FailureInvalidInput,
			"floor probe: claude code priced the turn as nothing -- what was measured is not a floor")
	}
	prefixTokens := env.Usage.CacheWrite + env.Usage.CacheRead
	return FloorMeasurement{
		USD:              *env.TotalCostUSD,
		CacheWriteTokens: env.Usage.CacheWrite,
		CacheReadTokens:  env.Usage.CacheRead,
		PrefixTokens:     prefixTokens,
		InputTokens:      env.Usage.InputTokens,
		OutputTokens:     env.Usage.OutputTokens,
		Cold:             env.Usage.CacheRead == 0,
		Model:            modelName,
		CLIVersion:       VersionToken(c.version.Version(ctx)),
	}, nil
}

// firstCallPrompt is what the warm probe asks for: exactly one tool call,
// against a pattern chosen to match nothing, and then two characters of
// answer.
//
// The tool call is the measurement's whole subject and its result is
// deliberately empty. What arrives at the first tool result is not the
// result: measured 2026-08-15, a 452-token result and an 8,002-token result
// were followed by blocks of 41,973 and 53,036 tokens -- the file is 7.3x of
// that difference and 17.7x of itself, so the block is overwhelmingly the
// CLI's own re-sent scaffolding and not the payload. A probe that read a real
// file would price that file into a figure meant to describe every step.
//
// Glob rather than Read: it is on every tool-carrying surface this package
// spawns (see planner.Surface), and a pattern that matches nothing cannot
// depend on what the repository happens to contain.
const firstCallPrompt = "Call the Glob tool exactly once, with the pattern " +
	"'atenea-floor-probe-matches-nothing-*'. Then reply with exactly: ok. " +
	"Call no other tool, and read no file."

// FirstCall measures both halves of what a step pays before it does any work
// of its own: the prefix that arrives with the prompt, and the block that
// arrives with the first tool result.
//
// CALLING THIS SPENDS REAL MONEY, on the same terms as Floor -- one turn,
// priced at roughly what it measures -- and for the same reason nothing calls
// it implicitly.
//
// It exists because Floor prices the wrong turn. Measured 2026-08-15 on two
// live probes and five loopback-recorded runs: the prefix is ~5,650 tokens and
// the block at the first tool call is ~41,930, so a floor built on the prefix
// alone describes 12% of what a step actually pays to get started. Both are
// written to cache once and read back at a twentieth of the price by every
// turn after -- cache_read pinned to the exact token across runs of different
// objectives, different files and different nonces -- which is why the two
// counts are what gets stored and the dollars are derived from them, per
// warmth, by internal/floor.
//
// One turn, not two: the two counts come off the SAME run, message by
// message. Subtracting a no-tool probe from a tool-using one would be two
// cache states and two receipts pretending to be one measurement, which is
// the mistake internal/floor's own PrefixTokens doc records.
func (c *Client) FirstCall(ctx context.Context, req FloorRequest) (FloorMeasurement, error) {
	if c.backend == BackendOpenCode {
		return FloorMeasurement{}, contract.Fail(contract.FailureUnavailable,
			"opencode first-call probes are not supported: its event stream does not expose the Claude message accounting")
	}
	turn := Request{
		Role:     req.Role,
		Prompt:   firstCallPrompt,
		Dir:      req.Dir,
		Tools:    req.Tools,
		Builtins: req.Builtins,
		Stream:   true,
	}
	if err := turn.Validate(); err != nil {
		return FloorMeasurement{}, err
	}
	if len(req.Builtins) == 0 && strings.TrimSpace(req.Tools) == "" {
		// A surface with nothing to call cannot be asked to call something.
		// Refused rather than quietly measured as a prefix probe: `plan` is
		// exactly this shape, and a row that recorded its no-tool turn under
		// a first-call figure would claim a measurement nobody took.
		return FloorMeasurement{}, contract.Fail(contract.FailureInvalidInput,
			"first-call probe: this surface carries no tools at all, so it has no first "+
				"tool call to price -- measure it with a floor probe instead")
	}
	modelName, err := c.modelFor(turn.Role)
	if err != nil {
		return FloorMeasurement{}, err
	}
	dir, err := resolveDir(turn.Dir)
	if err != nil {
		return FloorMeasurement{}, err
	}

	messages, receipt, err := c.observe(ctx, dir, c.timeout, turn)
	if err != nil {
		return FloorMeasurement{}, err
	}
	if len(receipt.PermissionDenials) > 0 {
		return FloorMeasurement{}, contract.Fail(contract.FailureInvalidInput,
			"first-call probe: claude code was denied %d action(s) -- a tool call that did "+
				"not happen prices nothing", len(receipt.PermissionDenials))
	}
	if receipt.NumTurns < 2 || len(messages) < 2 {
		// The probe asked for a tool call and did not get one, so the second
		// message this measurement is built on does not exist. Answering
		// with the prefix alone is what shipped the wrong floor in the first
		// place.
		return FloorMeasurement{}, contract.Fail(contract.FailureInvalidInput,
			"first-call probe: claude code answered without calling a tool (num_turns=%d, "+
				"%d assistant message(s)) -- there is no first tool call to price",
			receipt.NumTurns, len(messages))
	}
	if receipt.TotalCostUSD == nil {
		return FloorMeasurement{}, contract.Fail(contract.FailureInvalidInput,
			"first-call probe: claude code priced the turn as nothing -- what was measured "+
				"is not a cost")
	}
	prefix, firstCall := messages[0], messages[1]
	return FloorMeasurement{
		USD:              *receipt.TotalCostUSD,
		CacheWriteTokens: prefix.CacheWrite,
		CacheReadTokens:  prefix.CacheRead,
		PrefixTokens:     prefix.CacheWrite + prefix.CacheRead,
		FirstCallTokens:  firstCall.CacheWrite + firstCall.CacheRead,
		InputTokens:      prefix.InputTokens,
		OutputTokens:     prefix.OutputTokens,
		// Cold is the PREFIX's own state, the same reading Floor gives it:
		// the first message is where a machine that had never held this
		// prefix says so. The second message is warm or cold with it.
		Cold:       prefix.CacheRead == 0,
		Model:      modelName,
		CLIVersion: VersionToken(c.version.Version(ctx)),
	}, nil
}

// observe runs one streamed turn and reports what each assistant MESSAGE
// reported about itself, in arrival order, plus the result envelope.
//
// Per message and never per event: measured 2026-08-14, one message's content
// blocks arrive as separate events restating identical usage, so a reader that
// appended every event would count one message several times. The id is what
// separates them -- the same reading conversation.spend is built on, which is
// why the rule lives in one place and this function follows it.
func (c *Client) observe(ctx context.Context, dir string, timeout time.Duration, req Request) ([]usage, envelope, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, err := c.command(ctx, dir, req)
	if err != nil {
		return nil, envelope{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, envelope{}, contract.Fail(contract.FailureUnavailable,
			"cannot read claude code's event stream: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, envelope{}, failureFor("", err)
	}

	var messages []usage
	var ids []string
	var receipt envelope
	reader := bufio.NewReaderSize(stdout, eventBuffer)
	for {
		line, readErr := reader.ReadBytes('\n')
		if ev, ok := readEvent(line); ok {
			switch ev.Type {
			case "assistant":
				// Last reading wins for a message already seen: a message's
				// later events restate its usage, and the final restatement
				// is the one the CLI itself totals.
				if n := len(ids); n > 0 && ids[n-1] == ev.Message.ID {
					messages[n-1] = ev.Message.Usage
				} else {
					ids = append(ids, ev.Message.ID)
					messages = append(messages, ev.Message.Usage)
				}
			case "result":
				receipt = ev.envelope
			}
		}
		if readErr != nil {
			break
		}
	}
	waitErr := cmd.Wait()
	said := strings.TrimSpace(stderr.String())
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, envelope{}, contract.Stopped(ctxErr, "claude code", timeout).WithRaw(said)
	}
	if receipt.IsError {
		return nil, receipt, failureFor(receipt.reason(), waitErr)
	}
	if receipt.Type == "" {
		// No result event at all: the turn died before pricing itself, and
		// whatever the messages reported is an account of a turn nobody was
		// billed for in a way this can read.
		return nil, envelope{}, failureFor(said, waitErr)
	}
	return messages, receipt, nil
}

// VersionToken trims a version probe's banner to its first whitespace-
// delimited field. Read out of the shipped CLI: `claude --version` answers
// "2.1.232 (Claude Code)", and the token a stale-floor comparison actually
// wants to match is "2.1.232", not the trailing name repeated on every
// version this CLI will ever print. Empty stays empty -- see
// FloorMeasurement.CLIVersion.
//
// Exported (was versionToken) so cmd/atenea/floor.go can trim the running
// CLI's own banner the same way before comparing it against a stored row's
// CLIVersion. Measured 2026-08-14: a row written seconds earlier still
// listed "stale" because the stored side was trimmed ("2.1.232") but the
// list command compared it against the raw banner ("2.1.232 (Claude
// Code)") -- two different strings for the same version, never equal. One
// function, called on both sides, is what makes that comparison meaningful.
func VersionToken(banner string) string {
	fields := strings.Fields(banner)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// args builds the command line: the same flags claudecode measured for this
// exact CLI, plus --model and --mcp-config for what this package's callers
// need that a capability call does not.
func (c *Client) args(req Request) ([]string, error) {
	argv := []string{"--print"}
	if req.reservesAnswer() {
		// The three flags that hold the process open, and they are not a
		// choice of three. Read out of the shipped binary (2.1.232), each
		// one a hard error the CLI exits 1 on rather than a preference:
		// "--input-format=stream-json requires output-format=stream-json",
		// "--input-format=stream-json requires --print", and "When using
		// --print, --output-format=stream-json requires --verbose". Drop any
		// of them and the turn does not start at all.
		//
		// No positional prompt here, deliberately: the same binary's
		// getInputPrompt reads stdin and never looks at the prompt argument
		// once the input format is stream-json. A prompt passed here would
		// look sent, be recorded as sent, and never arrive.
		argv = append(argv,
			"--input-format", "stream-json",
			"--output-format", "stream-json",
			"--verbose")
	} else if req.Stream {
		// The positional prompt of a single shot with the event stream of a
		// conversation. Read out of the shipped binary (2.1.232): with
		// --print, --output-format stream-json is a hard error without
		// --verbose, and --input-format is what would take the prompt off
		// the command line -- so it is deliberately absent here. See
		// Request.Stream.
		argv = append(argv, req.sentPrompt(),
			"--output-format", "stream-json",
			"--verbose")
	} else {
		// --print takes no value of its own; the prompt that follows it is
		// the CLI's positional [prompt] argument, not --print's value.
		argv = append(argv, req.sentPrompt(), "--output-format", "json")
	}
	argv = append(argv,
		// Ambient customization off: this machine's settings, the repository's
		// hooks, and the skills any of them install must not be able to change
		// what a turn does.
		//
		// NOT --safe-mode, which claudecode uses and which this package used
		// until it was measured. That flag disables MCP servers too -- its own
		// --help says so, in those words -- so a turn carrying --mcp-config
		// got no Atenea tools at all and answered by reading files instead.
		// Measured 2026-08-14 against the live CLI: with --safe-mode the turn
		// lists no mcp__ tool; without it, all eight. Four real explorations
		// of a repository, $4.43, dispatched zero capabilities before this
		// line was found, and the comment that used to sit here asserted the
		// opposite without ever having been checked.
		//
		// The one thing --safe-mode still covered and this does not is
		// CLAUDE.md auto-discovery: the repository being explored can put
		// text in the turn's context. --bare suppresses that but also skips
		// keychain reads, and a turn with no OAuth session cannot run at all
		// ("Not logged in", measured the same day). Known, and left honest
		// rather than traded for a turn that never works.
		"--setting-sources", "",
		"--disable-slash-commands",
		// Measured on this same CLI, in claudecode: reusing a --session-id
		// fails outright on the second run. A fresh, unsaved session per
		// turn is what keeps two callers from ever sharing a far side.
		"--no-session-persistence",
		// A session this process throws away still gets named, and naming it
		// is a second model call: measured 2026-08-15 against a loopback
		// recorder, the CLI fires a claude-opus-5 request carrying this
		// turn's whole commission -- `effort: "high"`, `max_tokens: 64000`
		// -- whose only output is a 3-7 word title for a session
		// --no-session-persistence has already discarded. Supplying a name
		// is what stops it: 4 of 8 control runs made the call, 0 of 8 did
		// with this flag. Intermittent because the CLI fires it without
		// waiting, so a turn that finishes first never pays -- which is a
		// reason to remove it rather than to price it, since the runs that
		// do pay are the slow ones that were already the expensive ones.
		//
		// The value is not shown to anybody: no picker, no terminal title,
		// no saved session. It exists so the CLI has an answer and skips
		// asking a model for one.
		"--name", "atenea-"+string(req.Role),
	)
	// Omitted, not zero, when the caller granted no ceiling for this call --
	// see Request.BudgetUSD's own doc for why zero means that rather than
	// "spend nothing". The CLI's flag has no separate "unlimited" spelling;
	// not passing it is the only one.
	if req.BudgetUSD > 0 {
		argv = append(argv, "--max-budget-usd", strconv.FormatFloat(req.BudgetUSD, 'f', -1, 64))
	}
	if len(req.Schema) > 0 {
		schema, err := json.Marshal(req.Schema)
		if err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"request: schema cannot be expressed as JSON: %v", err)
		}
		argv = append(argv, "--json-schema", string(schema))
	}
	modelName, err := c.modelFor(req.Role)
	if err != nil {
		return nil, err
	}
	argv = append(argv, "--model", modelName)
	if fallbacks := c.fallbacksFor(req.Role); len(fallbacks) > 0 && c.backend == BackendClaude {
		argv = append(argv, "--fallback-model", strings.Join(fallbacks, ","))
	}
	// The complete set this turn may call, always passed. Handing a turn
	// Atenea's capabilities and leaving the CLI's built-ins beside them is
	// what made the first three real explorations bypass Atenea entirely --
	// see Request.Builtins for the measurement. --tools is what the CLI
	// loads; --allowedTools is what it may use without asking a person who,
	// under --print, is not there to answer.
	if allowed := allowedTools(req); len(allowed) > 0 {
		list := strings.Join(allowed, ",")
		argv = append(argv, "--tools", list, "--allowedTools", list)
	}
	if tools := strings.TrimSpace(req.Tools); tools != "" {
		// --mcp-config is the one flag here the CLI declares variadic
		// (<configs...>), so it has to stay last in argv: anything placed
		// after it would be read as one more config source instead of as
		// its own flag. --strict-mcp-config makes this the only server list
		// the turn ever sees -- the whole reason Request carries this
		// field, built by AteneaTools, is to hand the turn Atenea's own
		// tools and nothing else this machine happens to have registered.
		argv = append(argv, "--mcp-config", tools, "--strict-mcp-config")
	}
	return argv, nil
}

func (c *Client) fallbacksFor(role Role) []string {
	if role == RolePlan && c.backend == BackendClaude {
		return nil
	}
	if role == RolePlan {
		out := make([]string, 0, len(c.planFallbacks))
		for _, candidate := range c.planFallbacks {
			if highReasoningPlanModel(candidate) {
				out = append(out, candidate)
			}
		}
		return out
	}
	return c.exploreFallbacks
}

func highReasoningPlanModel(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(name, "opus") ||
		strings.Contains(name, "gpt-5.6-sol") ||
		strings.Contains(name, "gpt-5.6-luna")
}

// allowedTools is the complete set a turn may call: Atenea's whole server
// when it was given one, plus whatever built-ins the caller named.
//
// The server is named as a unit rather than tool by tool. A capability added
// to the catalog tomorrow is reachable the same day, and a list enumerated
// here would silently be the older, shorter catalog.
//
// A turn with no MCP config and no built-ins gets no list at all -- the CLI
// would read an empty --tools as "nothing", and a turn that can call nothing
// is a turn that should not have been started.
func allowedTools(req Request) []string {
	var out []string
	if strings.TrimSpace(req.Tools) != "" {
		out = append(out, "mcp__"+ateneaServer)
	}
	return append(out, req.Builtins...)
}

// command prepares the process both paths run: same binary, same argv, same
// containment, same record on the way out. Only how it is then driven --
// once, or held open across passes -- belongs to the caller.
//
// The timeout context is the caller's to build and cancel, because the defer
// that releases it has to live for as long as the process is being read
// from, which is a scope this function does not have.
func (c *Client) command(ctx context.Context, dir string, req Request) (*exec.Cmd, error) {
	binary, err := exec.LookPath(c.binary)
	if err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"claude code is not installed: %q is not on PATH", c.binary)
	}
	argv, err := c.args(req)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Dir = dir
	// A model turn spawns a tree of its own -- tool subprocesses among them
	// -- and without this a canceled turn leaves them running and blocks
	// here until the longest-lived one exits. See internal/procgroup.
	procgroup.Contain(cmd)
	recordPrompt(req, argv)
	return cmd, nil
}

// invoke runs one turn and hands back the envelope, sorted into the shared
// failure bins when it did not answer cleanly.
func (c *Client) invoke(ctx context.Context, dir string, timeout time.Duration, req Request) (envelope, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, err := c.command(ctx, dir, req)
	if err != nil {
		return envelope{}, err
	}
	stdout, runErr := cmd.Output()

	var stderr string
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		stderr = strings.TrimSpace(string(exit.Stderr))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return envelope{}, contract.Stopped(ctxErr, "claude code", timeout).WithRaw(stderr)
	}

	out, parseErr := parse(stdout)
	if parseErr != nil {
		// A turn that failed before it could print an envelope says so in
		// plain text on stderr -- a rejected flag, a broken schema. There is
		// no structure to sort, so the raw line travels for whoever debugs.
		if runErr != nil {
			return envelope{}, failureFor(stderr, runErr)
		}
		return envelope{}, parseErr
	}
	if out.IsError {
		// The envelope travels with the error on purpose: a turn that ran
		// for a minute and then died at its ceiling occupied the machine and
		// charged for it, and chargeFrom reads that usage right off out.
		return out, failureFor(out.reason(), runErr)
	}
	return out, nil
}

// maxPasses bounds how many times one reserved-answer turn may answer.
//
// A guard against a model that never converges, not a knob: the read
// allowance is what normally ends a turn, and this is what ends one whose
// cost the CLI never priced or whose completeness never reaches 1. Eight
// leaves a real exploration room to widen and still notices a stuck turn
// inside the turn rather than inside a bill. Reaching it is not a kill: the
// last pass a turn gets is always a finalize pass, so the cap ends with an
// answer like every other stopping condition here.
const maxPasses = 8

// finalizeMessage is the message that replaces the kill.
//
// It is sent once and never twice: a second would be paying a whole pass to
// tell the model something it has already been told. It names the two
// properties explicitly because a model asked only to "answer now" answers
// the objective and leaves the fields that say how far it got at whatever
// the last pass claimed -- and a stale completeness is worse than none, it
// reports coverage nobody has.
const finalizeMessage = "Stop reading. Answer now, in the required schema, with exactly what " +
	"you already have. Set completeness to the fraction covered and stopped_at to what " +
	"you did not reach."

// continueMessage is what a pass with allowance left is told.
//
// Short on purpose: every message here is read back on the next pass at the
// caller's expense, and this one carries no instruction the pass protocol in
// the prompt has not already given. Its whole job is to be a turn boundary
// the model can answer past.
const continueMessage = "Keep going, then answer the whole schema again with completeness " +
	"and stopped_at updated."

// eventBuffer sizes the reader over the CLI's event stream. A starting size
// rather than a ceiling -- ReadBytes grows past it -- and large because a
// result event carries the whole structured answer on one line.
const eventBuffer = 1 << 20

// converse runs one turn as a conversation held open across passes.
//
// THE CENTRAL GUARANTEE, and the only reason this path exists: once any pass
// has answered, the CLI dying -- at the hard ceiling, on a signal, on
// anything at all -- costs nothing but the passes that had not happened yet.
// The best answer so far comes back with what it spent and no error. Only a
// death with no pass behind it is still a failure, because only then is there
// genuinely nothing to hand back.
//
// Measured 2026-08-14, before this existed: 12 of 12 agent steps died at
// their ceiling with result_len 0, and the $3.78 they had already spent
// bought nothing whatsoever. The death is not what this fixes -- a turn can
// still run out of money. What it fixes is the death costing the answer.
func (c *Client) converse(ctx context.Context, dir string, timeout time.Duration, req Request) (Answer, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, err := c.command(ctx, dir, req)
	if err != nil {
		return Answer{}, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Answer{}, contract.Fail(contract.FailureUnavailable,
			"cannot hold claude code's input open: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Answer{}, contract.Fail(contract.FailureUnavailable,
			"cannot read claude code's event stream: %v", err)
	}
	// Collected rather than inherited, and read only after Wait -- the one
	// point os/exec guarantees its own copier has finished. This is where a
	// CLI that refused a flag or died without framing an event says so.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return Answer{}, failureFor("", err)
	}

	// Pass one goes in as a message, not as an argument. Measured on this
	// CLI: a user message on the same stdin is acted on by the same process
	// with the context intact -- both between turns and, measured on the
	// real backend repository, in the middle of one: a message written 5.8s
	// in, before the model's first tool call, had it finish that call and
	// answer the schema immediately. That is what makes an answer securable
	// before the ceiling rather than after it. Session resume across
	// processes is not an option here; see --no-session-persistence in args.
	handoff := say(stdin, req.sentPrompt())

	var conv conversation
	var limitErr error
	stop := handoff != nil
	reader := bufio.NewReaderSize(stdout, eventBuffer)
	for !stop {
		line, readErr := reader.ReadBytes('\n')
		if ev, ok := readEvent(line); ok {
			// The two kinds of line that decide anything. An assistant event
			// is the only signal that arrives while a turn is still running,
			// and a result event is the only place an answer appears. Every
			// other line -- the init system line, tool results, the echo of
			// what was just said -- is read past.
			switch ev.Type {
			case "assistant":
				stop = conv.spend(ev.Message.ID, ev.Message.Usage, stdin, req.ReadTokens)
			case "result":
				stop = conv.hear(ev.envelope, stdin, req.ReadTokens)
			}
			if limitErr == nil {
				limitErr = conv.limitFailure(req)
				if limitErr != nil {
					// Tokens are observable on assistant events even before a
					// result exists. Stop at that boundary instead of paying for
					// another model/tool round. Cost keeps the CLI's existing
					// terminal-event semantics because a completed pass may still
					// be the best answer to return after the receipt arrives.
					stop = true
				}
			}
		}
		if readErr != nil {
			// EOF ends a turn that answered and exited, and it ends a turn
			// killed at its hard ceiling. Neither is sorted here: what
			// decides between them is whether any pass answered, below.
			stop = true
		}
	}

	// Stdin first: the end of its input is what tells the CLI the
	// conversation is over, and it leaves on its own. Measured against the
	// live CLI: the same argv this path builds, given no message at all and
	// then EOF, exits 0 without calling anything.
	_ = stdin.Close()
	// Then drain, for two reasons that both end in a hang. os/exec's Wait
	// must not run while a read on the pipe is still in flight, and a CLI
	// blocked writing into a stdout pipe nobody is reading would never reach
	// the end of stdin that tells it to stop. A pass that answered early
	// leaves exactly that: events still queued behind the one this loop
	// stopped at.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(io.Discard, stdout)
	}()
	settle := func() bool {
		select {
		case <-drained:
			return true
		case <-time.After(procgroup.Grace):
			return false
		}
	}
	if !settle() {
		// It is not leaving on its own and the answer is already in hand, so
		// stop paying for the wait. Killing the group closes the write end,
		// which is what lets the drain finish.
		_ = procgroup.Kill(cmd)
		settle()
	}
	waitErr := cmd.Wait()
	said := strings.TrimSpace(stderr.String())

	answer := Answer{Spent: conv.charge(), Passes: conv.answered}
	if conv.answered == 0 {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Sorted before anything the CLI said, the same order invoke
			// reads it in: the caller stopped waiting, and that outranks
			// whatever the process managed to print on its way out.
			return answer, contract.Stopped(ctxErr, "claude code", timeout).WithRaw(said)
		}
		if limitErr != nil {
			return answer, limitErr
		}
		return answer, conv.emptyDeath(req, said, cmp.Or(handoff, waitErr))
	}
	if limitErr != nil {
		return answer, limitErr
	}
	// The guarantee, in code: everything that could have gone wrong with the
	// process -- waitErr, a killed group, a context that expired -- is
	// deliberately not consulted past this line. A pass answered, so the turn
	// produced something, and how the process ended is not the caller's
	// problem. The turn timeout becomes a ceiling on how long a turn may keep
	// reading rather than a reason to throw away what it read.
	text, structured, err := readAnswer(conv.best, req)
	if err != nil {
		// The one failure that still outranks an answer: the shape is wrong,
		// or the turn was refused an action it needed. Both are things a
		// caller has to act on rather than read around, and readAnswer words
		// them the same way for both paths.
		return answer, err
	}
	answer.Text, answer.Structured = text, structured
	answer.Completeness, answer.StoppedAt = conv.claimed.reported()
	return answer, nil
}

// conversation is what the passes of one turn have produced so far, which is
// exactly what has to survive the process for the guarantee to mean anything.
type conversation struct {
	// best is the last pass that carried an answer, and claimed is what that
	// pass said about itself. Not necessarily the last pass: a turn whose
	// final event is a ceiling death keeps the answer from before it.
	best    envelope
	claimed claim
	// receipt is the last result event of any kind, answered or not.
	//
	// Read out of the shipped binary (2.1.232): every result event carries
	// `usage: this.totalUsage`, an accumulator that starts at zero and is
	// summed field by field on each message_stop, and `total_cost_usd` from
	// the same running total. So the last event is the whole turn's receipt,
	// and adding the events up would double-count every pass.
	receipt envelope
	// settled is what the assistant events reported for every message before
	// the one in flight, and inflight is the latest reading for that one.
	// Together they are the only account of what a turn has spent that exists
	// while it is still running.
	//
	// Kept per message rather than per event, because the events repeat.
	// Measured 2026-08-14 against the live CLI, one turn surveying this
	// repository: 4 assistant events carried 2 message ids, and the two
	// events of a message carried byte-identical usage -- one content block
	// each, one request's usage restated. Summed per event that turn read
	// 78,386 cache-creation tokens; summed per message, 39,193, which is
	// exactly what the CLI's own result event then reported. So a per-event
	// sum is not an estimate that runs high, it is double-counting, and it
	// would also be the figure a caller is handed when no result event
	// arrives at all.
	//
	// A message id the CLI stopped sending would collapse every event into
	// one message and under-count instead. That direction is the deliberate
	// one: it nudges late rather than charging a caller for tokens nobody
	// measured, and the magnitudes measured below leave it firing anyway.
	settled    usage
	inflight   usage
	inflightID string
	// answered counts passes that produced an answer; rounds counts result
	// events whether they answered or not. They are different numbers and
	// each has one job: answered is what the caller is told and what decides
	// the guarantee, rounds is what bounds the loop, because a model that
	// answers nothing would otherwise be nudged forever.
	answered int
	rounds   int
	// finalized records that the one finalize message has gone out.
	finalized bool
}

// read is what the assistant events have accounted for so far: the messages
// that are done, plus the latest reading of the one in flight.
func (conv *conversation) read() usage {
	return conv.settled.add(conv.inflight)
}

// weighed is what this turn has run up so far, in the input-equivalent tokens
// ReadTokens is denominated in.
//
// The larger of the two readings, which is also the only way they can be
// combined honestly: the assistant events are all there is mid-turn, and the
// result event's own total is the CLI's own count and therefore the authority
// whenever one has arrived. Taking the larger keeps the figure monotone across
// a boundary, so an allowance already crossed cannot appear uncrossed again --
// measured on the live turn below, the two readings straddle a boundary at
// 81,699 and 87,004, and only the streamed one is short.
func (conv *conversation) weighed() int {
	return max(weigh(conv.read()), weigh(conv.receipt.Usage))
}

func (conv *conversation) limitFailure(req Request) error {
	charge := conv.charge()
	if req.MaxTokens > 0 && charge.Tokens() > req.MaxTokens {
		return contract.Fail(contract.FailurePermissionDenied,
			"model reported %d tokens above the requested limit of %d", charge.Tokens(), req.MaxTokens).
			WithRaw(fmt.Sprintf("observed_tokens=%d max_tokens=%d", charge.Tokens(), req.MaxTokens))
	}
	return nil
}

// charge is what the turn cost: the CLI's own price, against whichever
// account of the tokens is larger.
//
// The price can only come from the result event -- the CLI never prints one
// anywhere else -- and a price invented here is precisely what
// contract.Charge.PricedBy exists to make impossible. The TOKENS are a
// separate question, and the result event is not the authority on them.
//
// Measured 2026-08-16 on the live CLI, reproducing the four deaths of
// wf1786845363956-1 in 4.4 seconds for $0.41: a turn killed at
// --max-budget-usd prints `terminal_reason: budget_exhausted` with
// `total_cost_usd: 0.41228` and `usage` ALL ZEROS -- every lane, not a small
// figure but no figure. Read out of the shipped binary (2.1.232), the two
// come from different accumulators: `total_cost_usd: OA()` is
// `costLedger.totalCostUSD()`, charged as the work happens, while
// `usage: this.totalUsage` is summed only at `message_stop`, which a killed
// message never reaches. The same stream's assistant events carried 40,956
// cache-creation tokens for that money. Preferring the receipt threw them
// away and recorded a dollar figure against no usage -- a row from which
// nobody can tell whether the step read anything at all.
//
// So the larger reading wins, which is what weighed() already does for the
// allowance and for the same reason: whole, never blended lane by lane, so
// the figure is always one the CLI actually printed and never an arithmetic
// combination nobody measured. Where a turn settled its messages normally the
// receipt is larger and still wins, because the streamed output_tokens run
// short -- 6 against the 1,067 the same turn's result event reported -- an
// assistant event being printed while its message is still being written.
//
// When no result event arrived at all -- the exact shape of the 12 deaths this
// path exists for -- the assistant events are all there is, and per message
// they are the same field-by-field total the CLI would have printed
// (measured: 39,193 cache-creation tokens either way). That charge carries no
// dollar figure, because the CLI never printed one.
func (conv *conversation) charge() contract.Charge {
	streamed := conv.read()
	if conv.rounds == 0 {
		return chargeFrom(envelope{Usage: streamed})
	}
	charge := chargeFrom(conv.receipt)
	if weigh(streamed) <= weigh(conv.receipt.Usage) {
		return charge
	}
	// The receipt's price against the stream's tokens. Both halves are
	// measured; only the pairing is this package's, and it is the pairing the
	// record needs to be readable.
	out := chargeFrom(envelope{Usage: streamed})
	out.USD, out.PricedBy = charge.USD, charge.PricedBy
	return out
}

// spend takes one assistant event's account of itself and fires the finalize
// nudge the moment the turn crosses its allowance -- mid-turn, without waiting
// for a boundary that may never come.
//
// This is the trigger a cost check could not be, for two measured reasons. The
// CLI reports no cost mid-turn at all, and an explore step does all of its work
// inside turn 1: 12 of 12 steps died with zero passes because the boundary
// where an allowance was checked never arrived.
//
// Measured 2026-08-14, live, and this is the whole mechanism working: a turn
// surveying this repository was sent the finalize message 2.75s in, mid-turn,
// right after its first tool call and with no result event anywhere. It
// finished that call, and answered the schema -- completeness 0.05, naming
// what it had not reached -- for $0.26, exit 0. The pass protocol makes the
// answer possible; this is what asks for it in time.
func (conv *conversation) spend(id string, reported usage, stdin io.Writer, allowance int) bool {
	if id != conv.inflightID {
		conv.settled = conv.read()
		conv.inflightID = id
	}
	conv.inflight = reported
	if conv.finalized || conv.weighed() < allowance {
		return false
	}
	if err := say(stdin, finalizeMessage); err != nil {
		return true // the far side is gone; what is in hand is what there is
	}
	conv.finalized = true
	return false
}

// hear takes one pass and decides what the turn does next, reporting whether
// it is over.
//
// Every path out of here either stops or has just written exactly one message
// to stdin, which is what keeps the passes strictly alternating: the CLI is
// never given two messages to answer, and never left waiting on one that was
// not sent.
func (conv *conversation) hear(event envelope, stdin io.Writer, allowance int) bool {
	conv.rounds++
	conv.receipt = event
	pass, carried := claimOf(event)
	if carried {
		conv.best, conv.claimed, conv.answered = event, pass, conv.answered+1
	}
	switch {
	case carried && pass.complete():
		// The model says the objective is fully covered. There is nothing
		// left to buy.
		return true
	case conv.finalized:
		// The finalize message already went out, so this pass is the answer
		// it asked for -- whatever that turned out to be, including nothing,
		// in which case an earlier pass is still held above.
		return true
	case conv.rounds+1 >= maxPasses, conv.weighed() >= allowance:
		// The boundary half of the same trigger, kept because it needs
		// nothing from the assistant events: a result event carries the
		// CLI's own cumulative usage, so a turn whose assistant events said
		// nothing about usage is still stopped here. Either the allowance is
		// gone or the next pass is the last one this turn gets, and both mean
		// the same thing to the model.
		if err := say(stdin, finalizeMessage); err != nil {
			return true // the far side is gone; what is in hand is what there is
		}
		conv.finalized = true
		return false
	default:
		return say(stdin, continueMessage) != nil
	}
}

// emptyDeath sorts a turn that never got a pass out of the far side: the one
// case the guarantee cannot cover, because there is no answer to hand back
// and the failure is the whole story.
//
// It sorts the same shapes the same way invoke does, deliberately. A caller
// cannot tell which of the two paths ran, so it must not be able to tell from
// the failure either.
func (conv *conversation) emptyDeath(req Request, said string, runErr error) error {
	if conv.rounds == 0 {
		// Nothing was ever framed. A CLI that died before it could is a CLI
		// that said why in plain text on stderr -- a rejected flag, a broken
		// schema -- so the raw line travels for whoever debugs.
		if runErr != nil {
			return failureFor(said, runErr)
		}
		return contract.Fail(contract.FailureUnavailable,
			"claude code ended the turn without ever answering a pass").WithRaw(said)
	}
	if conv.receipt.IsError {
		return failureFor(conv.receipt.reason(), runErr)
	}
	// A pass was framed, said it was fine, and carried nothing this package
	// could keep. readAnswer names which of those it was, in the same words
	// the single-shot path uses for the same envelope.
	if _, _, err := readAnswer(conv.receipt, req); err != nil {
		return err
	}
	// readAnswer accepted it and claimOf did not, which leaves one shape:
	// bytes that open like an object and do not parse as one. readAnswer
	// checks the first byte, claimOf parses the whole thing.
	return contract.Fail(contract.FailureInvalidInput,
		"claude code's structured answer is not readable JSON").
		WithRaw(string(conv.receipt.StructuredOutput))
}

// say writes one user message on the CLI's stdin.
//
// The shape is the one --input-format stream-json takes, and it is worth
// getting exactly right rather than approximately: read out of the shipped
// binary (2.1.232), a line whose message.role is not "user" comes back as
// "Error: Expected message role 'user'", and a line carrying a type it does
// not know is dropped with "Ignoring unknown message type" -- silently, which
// would look from here exactly like a model that stopped answering. The
// trailing newline is the frame; the CLI reads its input a line at a time.
func say(w io.Writer, text string) error {
	line, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
	if err != nil {
		return err
	}
	_, err = w.Write(append(line, '\n'))
	return err
}

// event is one line of the stream, read for both of the things a line can
// carry that this package acts on.
//
// One decode rather than two: the embedded envelope is what a `result` line
// is, whole, and Message is where an `assistant` line puts its own account of
// itself. The two never collide -- a result event has no `message`, an
// assistant event has no top-level `usage` -- so one pass over the bytes
// answers whichever kind arrived.
//
// The id is read because the usage beside it is not per event. Measured
// 2026-08-14 on the live CLI: one message's content blocks arrive as separate
// events restating identical usage, so the id is what tells a second reading
// of one request from a second request. See conversation.settled.
type event struct {
	envelope
	Message struct {
		ID    string `json:"id"`
		Usage usage  `json:"usage"`
	} `json:"message"`
}

// readEvent reads one line of the event stream, reporting whether it was JSON
// at all. Which kind of event it is, and whether that kind matters, is the
// caller's to decide -- see the switch in converse, the one place the CLI's
// event names are spelled.
//
// Read out of the shipped binary (2.1.232): with --output-format stream-json
// the CLI installs a guard over its own stdout that lets only whole valid
// JSON lines through and diverts everything else to stderr, so a line here is
// either an event or nothing. An unreadable one is therefore skipped rather
// than reported: it is not this package's news to break.
func readEvent(line []byte) (event, bool) {
	text := bytes.TrimSpace(line)
	if len(text) == 0 || text[0] != '{' {
		return event{}, false
	}
	var out event
	if err := json.Unmarshal(text, &out); err != nil {
		return event{}, false
	}
	return out, true
}

// claim is what one pass says about its own answer: the two properties the
// pass protocol adds to the caller's schema, decoded out of the same
// structured output the caller will decode for its own fields.
type claim struct {
	Completeness *float64 `json:"completeness"`
	StoppedAt    string   `json:"stopped_at"`
}

// claimOf reads a pass's account of itself, reporting whether the pass
// carried an answer at all.
//
// The two are one question on purpose: a pass whose structured output is
// missing, is not an object, or does not parse has not answered, and cannot
// become the answer this turn hands back. That reading is exact rather than
// approximate: in the shipped binary (2.1.232) the list a result event's
// structured_output is taken from is built per submitted message, so an
// absent structure means this pass produced none -- never that an earlier
// pass's answer is being repeated here.
//
// A pass that answered without claiming anything still counts. The claim is
// then empty, which reads as unclaimed, and an answer nobody measured is
// still an answer.
func claimOf(env envelope) (claim, bool) {
	shape := bytes.TrimSpace(env.StructuredOutput)
	if len(shape) == 0 || shape[0] != '{' {
		return claim{}, false
	}
	var out claim
	if err := json.Unmarshal(shape, &out); err != nil {
		return claim{}, false
	}
	return out, true
}

// complete reports whether the pass says the objective is fully answered,
// which is the one thing that ends a turn with allowance still on the table.
//
// Read as ">= 1" rather than "== 1" so a model that over-claims is taken at
// its word about being done. It is the cheapest claim to believe: the
// alternative is paying for another pass to be told the same thing.
func (c claim) complete() bool {
	return c.Completeness != nil && *c.Completeness >= 1
}

// reported is what a claim becomes on an Answer: a fraction and the remainder
// it implies, or neither.
//
// An over-claim is clamped to 1 rather than dropped. Above 1 already means
// "whole" -- complete() ends a turn on `>= 1` for the same reason -- and
// dropping it would hand the caller an absence, which is the one reading that
// is not a measurement of anything: the planner refuses an answer that states
// no coverage, so a model saying "1.2" would be refused for saying nothing.
//
// At or below 0 stays nil, and is refused upstream: a claim of no coverage
// beside an answer there is contradicts itself, and pkg/contract's Report
// accepts (0, 1] for the same reason.
//
// What is never dropped is a partial answer's remainder. A pass claiming less
// than the whole objective and naming nothing it missed would build a Report
// that Report's own validation refuses, and what that would cost is the
// answer -- so it is named the way deniedTools names a refusal the provider
// did not name: badly, but at all.
func (c claim) reported() (*float64, string) {
	stoppedAt := strings.TrimSpace(c.StoppedAt)
	if c.Completeness == nil || *c.Completeness <= 0 {
		return nil, stoppedAt
	}
	covered := min(*c.Completeness, 1)
	if covered < 1 && stoppedAt == "" {
		stoppedAt = unnamedRemainder
	}
	return &covered, stoppedAt
}

// unnamedRemainder stands in for a stopped_at the model left empty on a pass
// that claimed less than the whole objective. See claim.reported.
const unnamedRemainder = "the model did not say what it did not reach"

// weigh is the adapter to allowance.Weigh for this package's own usage
// shape -- see allowance.Weigh for the full doc comment this moved
// from, the measured ratios, and the receipts they were checked
// against.
func weigh(u usage) int {
	return allowance.Weigh(u.InputTokens, u.OutputTokens, u.CacheRead, u.CacheWrite)
}

// add returns the two usages summed, field by field -- the same arithmetic the
// CLI itself does to build the total it prints (read out of the shipped binary
// 2.1.232: totalUsage starts at zero and each message's usage is added into it
// at message_stop). Per message, and never per event: see
// conversation.settled for the measurement that separates the two.
func (u usage) add(other usage) usage {
	return usage{
		InputTokens:  u.InputTokens + other.InputTokens,
		OutputTokens: u.OutputTokens + other.OutputTokens,
		CacheRead:    u.CacheRead + other.CacheRead,
		CacheWrite:   u.CacheWrite + other.CacheWrite,
	}
}

// PromptLogEnv names a directory where every prompt this package sends is
// written before the turn runs, one file per turn.
//
// It exists because a prompt that cannot be read cannot be measured. Four
// planning runs on 2026-08-14 were compared against a prompt captured from a
// stub binary standing in for the CLI, which is a different string than the
// real planner reads, and the difference was found by a grep returning a
// figure from the wrong run. A record written at the call site cannot drift
// from what was sent: it is the same value, on the way out.
//
// Unset means no recording, which is the normal case -- prompts carry the
// repository's own text and belong in a directory an operator named, not in
// a default one they did not.
const PromptLogEnv = "ATENEA_PROMPT_LOG"

// recordPrompt writes the prompt and argv of one turn, when asked to.
//
// Failures are silent by design: this is instrumentation, and a turn that
// would have answered must not fail because a debugging directory is
// read-only. The cost of that choice is that an unwritable path looks like
// no turns, so the caller reading an empty directory checks the path first.
func recordPrompt(req Request, argv []string) {
	dir := strings.TrimSpace(os.Getenv(PromptLogEnv))
	if dir == "" {
		return
	}
	// 0700 and 0600 below, not 0755 and 0644.
	//
	// What lands here is the prompt as sent, which carries the repository's
	// own text -- source, file names, whatever the task quoted. This is the
	// same posture the state root, the settings file and the socket already
	// take, and it was the one directory in this project that did not.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	// Role is a declared value on the Request rather than something a caller
	// types, but it reaches a filename here, and a filename is the one place
	// where "it is always one of four words" stops being an argument: the
	// cost of being wrong is a write outside the directory the operator named.
	name := fmt.Sprintf("%d-%s-%d.txt",
		time.Now().UnixNano(), filenameSafe(string(req.Role)), os.Getpid())
	var b strings.Builder
	fmt.Fprintf(&b, "role: %s\nbudget_usd: %v\nread_tokens: %d\ntools: %v\n",
		req.Role, req.BudgetUSD, req.ReadTokens, req.Builtins)
	// sentPrompt, not Prompt: a reserved-answer turn sends the pass protocol
	// too, and a record of the caller's half alone would be a record of a
	// string nobody was ever asked. On a single-shot turn the two are the
	// same value.
	fmt.Fprintf(&b, "argv: %q\n\n----- prompt -----\n%s\n", argv, req.sentPrompt())
	_ = os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o600)
}

// filenameSafe reduces a value to the characters a filename may carry here.
// Anything else becomes a dash, so a value that is not one of the declared
// roles cannot reach outside the directory or collide with a path separator.
func filenameSafe(value string) string {
	safe := make([]rune, 0, len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			safe = append(safe, r)
		default:
			safe = append(safe, '-')
		}
	}
	if len(safe) == 0 {
		return "unnamed"
	}
	return string(safe)
}

// envelope is the JSON one headless turn prints -- the same shape claudecode
// reads, because this package drives the identical CLI and inherits the same
// measured traps. It is also, line for line, the `result` event a streaming
// turn prints once per pass: one shape read two ways, which is why a pass and
// a whole single-shot turn are sorted by the same functions below.
type envelope struct {
	// Type names which event this is, and matters only where there are
	// several to tell apart -- resultEvent, reading the stream a
	// reserved-answer turn prints. A single-shot turn carries it too
	// (measured: a real --output-format json run prints "type":"result") and
	// has no reason to read it, since a single-shot turn prints one object
	// and that object is the answer.
	Type string `json:"type"`
	// IsError is the only field that tells the truth about failure.
	// Measured, on this same CLI, in claudecode: a turn that failed to
	// authenticate still reported subtype "success", so subtype can never be
	// read before this.
	IsError bool `json:"is_error"`
	// Subtype names how the turn ended. It reports "success" on some turns
	// that failed, so it is read last, when nothing else said anything.
	Subtype string `json:"subtype"`
	// Result is the text answer, and on a failure the message when there is
	// one.
	Result string `json:"result"`
	// TerminalReason and Errors are where the reason lives when the turn
	// died without producing a result at all. Measured on a real turn
	// stopped at its spending ceiling: no `result` field is printed
	// whatsoever, while `errors` and `terminal_reason` both name it.
	TerminalReason string   `json:"terminal_reason"`
	Errors         []string `json:"errors"`
	// StructuredOutput is where --json-schema puts the answer.
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            usage           `json:"usage"`
	// TotalCostUSD is a pointer because its absence and an explicit 0 are
	// different facts: absence is "the cli did not price this turn",
	// explicit 0 is "the cli's own arithmetic says this turn was free". A
	// plain float64 cannot tell the two apart, which is exactly the
	// unmeasured-read-as-zero failure contract.Charge exists to rule out.
	TotalCostUSD *float64 `json:"total_cost_usd"`
	NumTurns     int      `json:"num_turns"`
	// PermissionDenials lists what the turn was refused. A non-empty list is
	// the permission bin regardless of how the turn otherwise ended.
	PermissionDenials []json.RawMessage `json:"permission_denials"`
}

// reason is the best sentence the far side gave for a failure, read in the
// same measured order claudecode reads it: `result` carries the message when
// the turn produced one, `errors` and `terminal_reason` carry it when the
// turn died before producing anything, `subtype` comes last because it
// reports "success" on turns that failed.
func (e envelope) reason() string {
	for _, candidate := range []string{e.Result, strings.Join(e.Errors, "; "), e.TerminalReason, e.Subtype} {
		if text := strings.TrimSpace(candidate); text != "" {
			return text
		}
	}
	return ""
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheRead    int `json:"cache_read_input_tokens"`
	CacheWrite   int `json:"cache_creation_input_tokens"`
}

// parse reads the envelope out of stdout.
func parse(stdout []byte) (envelope, error) {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return envelope{}, contract.Fail(contract.FailureUnavailable, "claude code printed nothing")
	}
	var out envelope
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return envelope{}, contract.Fail(contract.FailureUnavailable,
			"claude code printed something this package cannot read").WithRaw(trimmed)
	}
	return out, nil
}

// failureFor sorts one failure into the shared bins.
//
// It matches claudecode's own sorting, with one deliberate difference. There,
// a spending ceiling is contract.FailurePermissionDenied, because that budget
// was cut from a commission's shared grant by the core, and running out of a
// grant somebody else is managing is a refusal. Request.BudgetUSD here is not
// cut from anything -- it is this one call's own ceiling, with no larger pool
// behind it -- so a turn that hits it was not refused by policy, it simply
// could not produce an answer inside what it was given. That reads the same
// as a missing binary or an unreachable session: the provider did not
// deliver, which is FailureUnavailable, the bin that drives a caller to try
// again rather than to ask for a bigger grant that does not exist here.
func failureFor(message string, runErr error) *contract.Failure {
	text := strings.TrimSpace(message)
	lower := strings.ToLower(text)
	if text == "" && runErr != nil {
		text = runErr.Error()
		lower = strings.ToLower(text)
	}
	switch {
	case strings.Contains(lower, "authenticate"),
		strings.Contains(lower, "oauth"),
		strings.Contains(lower, "login"),
		strings.Contains(lower, "not logged in"):
		return contract.Fail(contract.FailureUnavailable,
			"claude code is not logged in on this machine").WithRaw(text)
	case strings.Contains(lower, "budget"):
		return contract.Fail(contract.FailureUnavailable,
			"claude code stopped at its spending ceiling before it could answer").WithRaw(text)
	case strings.Contains(lower, "permission"), strings.Contains(lower, "denied"):
		return contract.Fail(contract.FailurePermissionDenied,
			"claude code refused the work").WithRaw(text)
	case strings.Contains(lower, "max turns"), strings.Contains(lower, "max_turns"):
		return contract.Fail(contract.FailureTimeout,
			"claude code stopped at its turn ceiling").WithRaw(text)
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"):
		return contract.Fail(contract.FailureTimeout,
			"claude code timed out").WithRaw(text)
	case strings.Contains(lower, "no such file"), strings.Contains(lower, "not found"):
		return contract.Fail(contract.FailureNotFound,
			"claude code could not find what it was pointed at").WithRaw(text)
	default:
		return contract.Fail(contract.FailureUnavailable,
			"claude code did not answer").WithRaw(text)
	}
}

// pricedByCLI names exactly whose arithmetic a populated Charge.USD is.
//
// Measured on this machine, an OAuth session with no API key attached: a
// turn still came back with a non-zero total_cost_usd, computed at list-price
// rates against a plan that does not bill per call. That is the "list-price
// arithmetic on subscription traffic" a dollar figure must never be shown as
// if it settles a bill. The number is kept anyway -- the shared rule is to
// say whose price it used, not to hide it -- and this string is that
// disclosure, carried on every Charge that sets USD.
const pricedByCLI = "claude code cli's own total_cost_usd (its list-price arithmetic, not a reconciled bill)"

// chargeFrom builds a contract.Charge from one envelope.
//
// Tokens come straight from usage, whatever they read, because the provider
// counted them and that count is the same fact on any billing plan. USD is
// populated only when the envelope actually carried total_cost_usd -- see the
// field's own doc for why a nil check, not a zero check, is what decides that.
func chargeFrom(env envelope) contract.Charge {
	charge := contract.Charge{
		InputTokens:      env.Usage.InputTokens,
		OutputTokens:     env.Usage.OutputTokens,
		CacheReadTokens:  env.Usage.CacheRead,
		CacheWriteTokens: env.Usage.CacheWrite,
	}
	if env.TotalCostUSD != nil {
		amount := *env.TotalCostUSD
		charge.USD = &amount
		charge.PricedBy = pricedByCLI
	}
	return charge
}

// readAnswer turns a clean envelope into what Turn hands back.
//
// A malformed answer -- missing structure, or structure that is not a JSON
// object, when a Schema was asked for -- is FailureInvalidInput: the model
// answered, just not in the shape it was told to, which is a payload not
// matching a declared schema, the exact case that bin's own doc names. It is
// not FailureUnavailable, because retrying against a different provider is
// not an option this package's callers have -- config already fixed which
// model this is.
func readAnswer(env envelope, req Request) (text string, structured json.RawMessage, err error) {
	if len(env.PermissionDenials) > 0 {
		return "", nil, contract.Fail(contract.FailurePermissionDenied,
			"claude code was refused %d action(s) it needed: %s",
			len(env.PermissionDenials), deniedTools(env.PermissionDenials)).WithRaw(env.Result)
	}
	if len(req.Schema) == 0 {
		return env.Result, nil, nil
	}
	shape := bytes.TrimSpace(env.StructuredOutput)
	if len(shape) == 0 {
		return "", nil, contract.Fail(contract.FailureInvalidInput,
			"claude code answered without the structure it was asked for").WithRaw(env.Result)
	}
	if shape[0] != '{' {
		return "", nil, contract.Fail(contract.FailureInvalidInput,
			"claude code's structured answer is not a JSON object").WithRaw(string(env.StructuredOutput))
	}
	return env.Result, env.StructuredOutput, nil
}

// deniedTools names what a turn was refused, for the sentence a person reads
// when nothing else about the failure is actionable.
//
// A count alone -- "refused 1 action(s)" -- was the shipped message, and it
// cost a real debugging session on 2026-08-14: the run had spent $1.26 and
// the operator could not tell whether the model had reached for a shell, a
// write, or a tool nobody had granted. The provider's own field name is used
// as-is, and an entry that does not carry one is reported as unnamed rather
// than dropped: a refusal nobody can name is still a refusal that happened.
func deniedTools(denials []json.RawMessage) string {
	names := make([]string, 0, len(denials))
	for _, raw := range denials {
		var one struct {
			ToolName string `json:"tool_name"`
		}
		if err := json.Unmarshal(raw, &one); err != nil || strings.TrimSpace(one.ToolName) == "" {
			names = append(names, "unnamed")
			continue
		}
		names = append(names, one.ToolName)
	}
	return strings.Join(names, ", ")
}
