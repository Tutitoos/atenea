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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/pkg/contract"
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
}

// Client calls a model for whichever caller holds it.
//
// One Client for both roles, not one each: Options fixes both explore's and
// plan's model once, at construction, and Request.Role picks between them
// per Turn. A caller working through both roles in the same run -- explore
// then plan -- reuses the one Client instead of juggling two.
type Client struct {
	binary  string
	timeout time.Duration
	explore string
	plan    string
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
	client := &Client{
		binary:  strings.TrimSpace(opts.Binary),
		timeout: opts.Timeout,
		explore: strings.TrimSpace(opts.Explore),
		plan:    strings.TrimSpace(opts.Plan),
	}
	if client.binary == "" {
		client.binary = DefaultBinary
	}
	if client.timeout == 0 {
		client.timeout = DefaultTimeout
	}
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
	conn, err := net.Dial("unix", core.SocketPath())
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
	if r.BudgetUSD < 0 {
		// Only negative is refused. Zero is deliberately not: see
		// BudgetUSD's own doc for why it means "no ceiling", not "no
		// spending" -- no accounting a caller could have done on its way
		// here produces a negative figure on purpose.
		return contract.Fail(contract.FailureInvalidInput,
			"request: budget_usd must not be negative, got %v", r.BudgetUSD)
	}
	if r.Timeout < 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"request: timeout must not be negative, got %s", r.Timeout)
	}
	return nil
}

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
}

// Turn runs one model turn and reads back what it answered.
//
// The request is checked before anything spawns: a turn that cannot run is
// refused outright, not attempted and then blamed on the provider. Spent is
// filled in on every path once an envelope exists, including the failing
// ones -- a turn that ran for a minute and then died at its ceiling occupied
// the machine and charged for it, and leaving that off the answer would put
// the minute and the money on nobody's receipt.
func (c *Client) Turn(ctx context.Context, req Request) (Answer, error) {
	if err := req.Validate(); err != nil {
		return Answer{}, err
	}
	dir := req.Dir
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return Answer{}, contract.Fail(contract.FailureInvalidInput,
				"request: dir %q: %v", dir, err)
		}
		dir = abs
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = c.timeout
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
	answer.Text, answer.Structured = text, structured
	return answer, nil
}

// args builds the command line: the same flags claudecode measured for this
// exact CLI, plus --model and --mcp-config for what this package's callers
// need that a capability call does not.
func (c *Client) args(req Request) ([]string, error) {
	argv := []string{
		// --print takes no value of its own; the prompt that follows it is
		// the CLI's positional [prompt] argument, not --print's value.
		"--print", req.Prompt,
		"--output-format", "json",
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
	}
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

// invoke runs one turn and hands back the envelope, sorted into the shared
// failure bins when it did not answer cleanly.
func (c *Client) invoke(ctx context.Context, dir string, timeout time.Duration, req Request) (envelope, error) {
	binary, err := exec.LookPath(c.binary)
	if err != nil {
		return envelope{}, contract.Fail(contract.FailureUnavailable,
			"claude code is not installed: %q is not on PATH", c.binary)
	}
	argv, err := c.args(req)
	if err != nil {
		return envelope{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Dir = dir
	// A model turn spawns a tree of its own -- tool subprocesses among them
	// -- and without this a canceled turn leaves them running and blocks
	// here until the longest-lived one exits. See internal/procgroup.
	procgroup.Contain(cmd)
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

// envelope is the JSON one headless turn prints -- the same shape claudecode
// reads, because this package drives the identical CLI and inherits the same
// measured traps.
type envelope struct {
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
