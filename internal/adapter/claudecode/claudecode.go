// Package claudecode is the second client adapter: the far side of
// contract.Runner, backed by the Claude Code CLI already installed and logged
// in on this machine.
//
// It is the first far side that thinks. omp answers a search with a tool call;
// Claude Code answers it with a model turn, because its deterministic surface
// has no search in it -- `claude mcp serve` exposes 28 tools and 18 deferred
// ones, and neither Grep nor Glob is among them. Driving `Bash` instead would
// mean reporting ripgrep's work under Claude Code's name, so the honest wiring
// is the one where Claude Code is genuinely the provider.
//
// That makes this adapter the first implementation whose cost is real. It is
// slower and dearer than ripgrep at the same capability, and once the metrics
// base feeds the funnel that is exactly the contrast the cost stage exists to
// rank. Being outranked is not a defect here; it is the selector working.
//
// Nothing above contract.Runner changes. The intelligence on the far side
// belongs to Claude Code, not to this package: there is still no policy in
// this file, and still no second brain.
package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/internal/procstat"
	"github.com/Tutitoos/atenea/internal/toolpath"
	"github.com/Tutitoos/atenea/internal/toolversion"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// CodeSearch is the one capability this adapter is wired to.
const CodeSearch = "code.search"

// DefaultBinary is the command looked up on PATH when the settings name none.
const DefaultBinary = "claude"

// DefaultTimeout caps one invocation. A model turn is slower than a tool call
// by nature, so this sits above omp's ceiling; past it the turn is stuck
// rather than thinking, and the timeout bin is what lets the core fall back.
//
// Measured, which is why it is not the five minutes it used to be: two real
// `code.search` turns against this repository made 8 and 9 turns in 55s and
// 66s, about seven seconds a turn, and both were ended by the money ceiling
// rather than by time. Five minutes was a leash no dispatch had ever reached
// -- unreachable on a paid provider, because the grant always bites first --
// while being far longer than anybody waiting at a prompt will sit through.
// Ninety seconds is roughly thirteen turns of headroom.
//
// The two ceilings are not redundant and neither replaces the other: money
// stops a client that is working too expensively, and this stops one that is
// not working at all. A client wedged on a lock or a dead socket spends
// nothing, so no grant will ever end it.
const DefaultTimeout = 90 * time.Second

// defaultContextLines matches the capability's declared semantics.
const defaultContextLines = 2

// Options configure the adapter. Everything here is declared in the settings
// file, so retuning it never means touching Go.
//
// There is no ceiling here. What one call may spend arrives on the request, in
// contract.Permission, cut from the commission's grant by the core -- money is
// a permission, and permissions are granted per commission. An adapter holding
// a private ceiling could only ever cap one call, so a run of four steps spent
// it four times and no adapter could see the others doing the same.
type Options struct {
	// Binary is the claude executable. A bare name is looked up on PATH.
	// It is a legacy explicit override; new settings should use Source and the
	// candidate binaries below.
	Binary string
	// Source is "auto", "terminal" or "app". The app candidate, when used,
	// must be a headless Claude Code executable; Claude.app's GUI process is not
	// a supported runner.
	Source         string
	TerminalBinary string
	AppBinary      string
	// Implementations this adapter answers for, by implementation id.
	Implementations []string
	// Sensitive holds the path patterns that carry secrets. Unlike a tool
	// runner this adapter cannot filter the far side's reading after the fact,
	// so the patterns are handed to Claude Code as an instruction and the
	// answer is filtered again on the way back. Belt and braces, because the
	// far side is the one thing here that can decide for itself.
	Sensitive []string
	// Timeout caps one invocation. Zero takes DefaultTimeout.
	Timeout time.Duration
}

// Runner answers capabilities by driving the Claude Code CLI.
type Runner struct {
	binary          string
	source          string
	terminalBinary  string
	appBinary       string
	implementations []string
	sensitive       []string
	timeout         time.Duration
	// version asks the binary who it is, once. A client that updates itself
	// on a schedule is exactly the case a declared version cannot track.
	version *toolversion.Probe
}

// New validates the options and returns the adapter.
//
// A missing claude binary is deliberately not an error here, for the same
// reason it is not in the omp adapter: a client that is not installed is a
// provider that is unreachable, which is what the unavailable bin and the
// fallback it drives are for.
func New(opts Options) (*Runner, error) {
	for _, pattern := range opts.Sensitive {
		if _, err := path.Match(pattern, "probe"); err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"claude-code adapter: sensitive pattern %q: %v", pattern, err)
		}
	}
	if opts.Timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"claude-code adapter: timeout must not be negative, got %s", opts.Timeout)
	}
	runner := &Runner{
		binary:          strings.TrimSpace(opts.Binary),
		source:          strings.TrimSpace(opts.Source),
		terminalBinary:  strings.TrimSpace(opts.TerminalBinary),
		appBinary:       strings.TrimSpace(opts.AppBinary),
		implementations: slices.Clone(opts.Implementations),
		sensitive:       slices.Clone(opts.Sensitive),
		timeout:         opts.Timeout,
	}
	if runner.terminalBinary == "" {
		runner.terminalBinary = DefaultBinary
	}
	if runner.binary == "" {
		if err := toolpath.ValidateSource(runner.source, runner.candidates()); err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput, "claude-code adapter: %v", err)
		}
	}
	if runner.timeout == 0 {
		runner.timeout = DefaultTimeout
	}
	versionBinary := runner.binary
	if versionBinary == "" {
		if resolved, err := runner.resolve(); err == nil {
			versionBinary = resolved.Path
		} else {
			versionBinary = runner.terminalBinary
		}
	}
	runner.version = toolversion.New(versionBinary, "--version")
	return runner, nil
}

func (r *Runner) resolve() (toolpath.Resolved, error) {
	if r.binary != "" {
		return toolpath.Resolve("explicit", []toolpath.Candidate{{Source: "explicit", Binary: r.binary}})
	}
	return toolpath.Resolve(r.source, r.candidates())
}

func (r *Runner) candidates() []toolpath.Candidate {
	return []toolpath.Candidate{
		{Source: "terminal", Binary: r.terminalBinary},
		{Source: "app", Binary: r.appBinary},
	}
}

// Surface reports the selected headless executable. Claude.app's GUI process
// is intentionally never selected by the default configuration.
func (r *Runner) Surface() string {
	if r.binary != "" {
		return "explicit:" + r.binary
	}
	resolved, err := r.resolve()
	if err != nil {
		return r.source + ":unavailable"
	}
	return resolved.Source + ":" + resolved.Path
}

// ID names the runner on the status screen.
func (r *Runner) ID() string { return "claude-code" }

// Serves reports whether this adapter answers for that implementation.
func (r *Runner) Serves(implementationID string) bool {
	return slices.Contains(r.implementations, implementationID)
}

// Capabilities lists what this adapter's Run can actually dispatch, so a
// settings file naming an implementation it has no case for is refused at
// load rather than at the call.
func (r *Runner) Capabilities() []string { return []string{CodeSearch} }

// Implementations lists what this adapter answers for, sorted.
func (r *Runner) Implementations() []string {
	out := slices.Clone(r.implementations)
	slices.Sort(out)
	return out
}

// Run executes one step by handing it to Claude Code and reading the answer
// back.
//
// The version travels back on every path, including the failing ones: which
// build produced a number is half the number's meaning, and an upgrade that
// started failing is exactly the case with no outcome to carry it.
func (r *Runner) Run(ctx context.Context, req contract.RunRequest) (out contract.Outcome, err error) {
	defer func() { out.ToolVersion = r.version.Version(ctx) }()
	if err := req.Validate(); err != nil {
		return contract.Outcome{}, err
	}
	if !r.Serves(req.Implementation.ID) {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"claude-code adapter does not serve implementation %s", req.Implementation.ID)
	}
	if req.Capability.ID != CodeSearch {
		return contract.Outcome{}, contract.Fail(contract.FailureNotFound,
			"claude-code adapter has no implementation of %s", req.Capability.ID)
	}
	// The commission's grant is what this call may draw, and it arrives cut to
	// this step's share. Nothing left is a refusal, not a free ride: a far side
	// that charges must stop rather than run up a bill nobody granted. It is
	// the permission bin because that is what ran out -- the tool is fine, and
	// saying otherwise here would take it out of the funnel for every later
	// step, including the ones that were never going to cost anything.
	if !req.Permission.Funded() {
		return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied,
			"claude code costs money and the commission has none left to spend")
	}

	started := time.Now()
	ask, err := readSearch(req.Payload)
	if err != nil {
		return contract.Outcome{}, err
	}
	root, err := filepath.Abs(req.Repository.Path)
	if err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %q: %v", req.Repository.ID, req.Repository.Path, err)
	}

	answer, peak, err := r.invoke(ctx, root, req, ask)
	// The weight is read before the verdict, and read the same way whichever
	// the verdict turns out to be. A turn that ran for a minute and then died
	// at its spending ceiling occupied the machine for that minute and charged
	// for it; reporting a zero would leave the time off the baseline's worst
	// case, the money off the receipt, and -- because the core spends its
	// purse down by what comes back -- would let one commission charge past
	// its grant without anything noticing.
	weighed := contract.Outcome{
		Spent: contract.Sample{
			Duration: time.Since(started),
			Tokens:   answer.Usage.total(),
			PeakRSS:  peak,
		},
		// Money travels as a number of its own, never folded into the token
		// count: the baseline ranks on tokens, and a dollar rounded into them
		// would be a price list quietly poisoning a measurement.
		SpentUSD: answer.TotalCostUSD,
	}
	if err != nil {
		return weighed, err
	}

	result, outOfScope, err := r.readAnswer(answer, req, ask)
	if err != nil {
		return weighed, err
	}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return weighed, contract.Fail(contract.FailureUnavailable,
			"claude code answered a shape this capability does not accept: %v", err).
			WithRaw(answer.Result)
	}

	outcome := weighed
	outcome.Result = result
	outcome.Verdict = contract.VerdictOK
	if answer.TotalCostUSD > 0 {
		// Reported against the ceiling rather than on its own, because the
		// number that means something is the fraction of the grant it used. A
		// call that spent nine tenths of what it was allowed answered this
		// time and will not answer a slightly bigger question, and that is
		// worth knowing before it fails rather than after.
		outcome.Discoveries = append(outcome.Discoveries, contract.Discovery{
			Level: contract.ContextRepository,
			Note: fmt.Sprintf("claude code answered %s for $%.4f of its $%.2f ceiling over %d turn(s)",
				req.Repository.ID, answer.TotalCostUSD, req.Permission.BudgetUSD, answer.NumTurns),
		})
	}
	if notices := completenessDoubt(answer, req.Permission.BudgetUSD); len(notices) > 0 {
		outcome.Notices = append(outcome.Notices, notices...)
	}
	// The count travels as a number and the sentence is built from it, so the
	// caller who reads the receipt and the funnel that ranks the provider are
	// looking at the same fact rather than at prose and a guess.
	outcome.OutOfScope = outOfScope
	if outOfScope > 0 {
		outcome.Notices = append(outcome.Notices, fmt.Sprintf(
			"%d match(es) fell outside the requested scope and were dropped", outOfScope))
	}
	return outcome, nil
}

// nearCeilingFraction is how much of its grant a turn may spend before a
// clean answer is flagged rather than trusted outright. Chosen, not measured:
// every recorded case this close to a ceiling died outright (see
// ceiling_test.go, weight_test.go), so there is no successful call yet to
// calibrate against. 80% leaves room for an answer that is simply thorough
// and expensive, while still catching one that came home a grep short.
const nearCeilingFraction = 0.8

// completenessDoubt names concrete reasons a successful answer might still
// be short of the "every match" the contract promises. It is deliberately
// narrow -- most calls trip neither check -- because a notice on every
// answer trains the reader to skip the line that matters.
func completenessDoubt(answer envelope, budgetUSD float64) []string {
	var out []string
	if answer.NumTurns <= 1 {
		// A tool call always costs a turn of its own to read the result
		// back: the completion that calls Grep cannot also be the one that
		// reports what it found, because the tool has not run yet when that
		// completion is written. A one-turn answer therefore never saw a
		// tool result, despite the prompt saying "use your Grep tool" and
		// "do not invent" a match -- it answered from whatever the model
		// already believed, not from a search that ran.
		out = append(out, "answered in a single turn, with no tool result read back -- "+
			"the prompt requires a grep before an answer, so this one may not have run")
	}
	if budgetUSD > 0 && answer.TotalCostUSD/budgetUSD >= nearCeilingFraction {
		// Measured: the runs that died outright spent past their ceiling
		// while still mid-search, 8-9 turns in, cut off by the money rather
		// than by finishing. A successful call that spent most of the same
		// grant without dying is the same shape one step earlier -- close
		// enough to the edge that stopping deliberately, one grep short, is
		// at least as likely an explanation as finishing cleanly.
		out = append(out, fmt.Sprintf(
			"spent $%.4f of its $%.2f ceiling (%.0f%%) -- close enough to the edge "+
				"that it may have stopped searching rather than finished",
			answer.TotalCostUSD, budgetUSD, 100*answer.TotalCostUSD/budgetUSD))
	}
	return out
}

// search is one payload read once.
type search struct {
	query        string
	scope        []string
	fileTypes    []string
	matchCase    bool
	regex        bool
	wholeWord    bool
	contextLines int
}

// readSearch reads the payload after it has been checked once against the
// declared schema, so the shape is known and only the meaning is left.
func readSearch(payload map[string]any) (search, error) {
	text, _ := payload["query"].(string)
	if strings.TrimSpace(text) == "" {
		return search{}, contract.Fail(contract.FailureInvalidInput, "query is empty")
	}
	out := search{
		query:        text,
		contextLines: defaultContextLines,
		scope:        stringsAt(payload, "scope"),
		fileTypes:    stringsAt(payload, "file_types"),
		matchCase:    boolAt(payload, "match_case"),
		regex:        boolAt(payload, "regex"),
		wholeWord:    boolAt(payload, "whole_word"),
	}
	if lines, ok := intAt(payload, "context_lines"); ok {
		if lines < 0 {
			return search{}, contract.Fail(contract.FailureInvalidInput,
				"context_lines %d is negative", lines)
		}
		out.contextLines = lines
	}
	return out, nil
}

// tools translates the commission's effects into the tool list Claude Code is
// allowed to have.
//
// This is the whole of the permission translation, and it is enforcement
// rather than etiquette: --tools decides which tools EXIST for the turn, so a
// commission that only covers reading is handed a Claude Code that has no way
// to write. --allowedTools then auto-approves what is left, because a headless
// turn has nobody to answer a permission prompt.
func tools(permission contract.Permission) []string {
	out := []string{"Grep", "Glob", "Read"}
	if permission.Allows(contract.EffectWrite) {
		out = append(out, "Edit", "Write")
	}
	if permission.Allows(contract.EffectExternal) {
		out = append(out, "WebFetch", "WebSearch")
	}
	return out
}

// args builds the command line.
func (r *Runner) args(req contract.RunRequest, ask search) ([]string, error) {
	schema, err := outputSchema(req.Capability)
	if err != nil {
		return nil, err
	}
	allowed := tools(req.Permission)
	return []string{
		"--print", prompt(req, ask, r.sensitive),
		"--output-format", "json",
		"--json-schema", schema,
		// The catalog, the rules and the permissions live in Atenea. A
		// CLAUDE.md in the repository under search must not be able to change
		// what a capability means, so every customization the client would
		// otherwise load is off. Auth and the built-in tools still work, which
		// is what keeps the OAuth session -- and the design's no-API-keys
		// rule -- intact.
		"--safe-mode",
		// Measured: reusing a --session-id fails with a plain-text
		// "already in use" on the second run. A fresh unsaved session per
		// commission is what keeps two chats from ever sharing a far side,
		// and it is the only shape of this that can be verified without a
		// live login.
		"--no-session-persistence",
		"--tools", strings.Join(allowed, ","),
		"--allowedTools", strings.Join(allowed, ","),
		// The share the core cut for this step, not a number this adapter
		// keeps. Enforcing it here as well as in the core is deliberate: the
		// core's arithmetic bounds what CAN be handed out, and this bounds what
		// the far side is allowed to draw against what it was handed. Without
		// the second the figure on the receipt would be a hope.
		"--max-budget-usd", strconv.FormatFloat(req.Permission.BudgetUSD, 'f', -1, 64),
	}, nil
}

// invoke runs one turn and hands back the envelope and what the child weighed.
//
// The weight comes back even when the turn failed. A model call that ran for
// two minutes and then errored still occupied the machine for two minutes, and
// a baseline that only counted the successes would rank it as cheap.
func (r *Runner) invoke(ctx context.Context, root string, req contract.RunRequest, ask search) (envelope, int64, error) {
	resolved, err := r.resolve()
	if err != nil {
		return envelope{}, 0, contract.Fail(contract.FailureUnavailable,
			"claude code executable is unavailable for source %q", r.source)
	}
	argv, err := r.args(req, ask)
	if err != nil {
		return envelope{}, 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, resolved.Path, argv...)
	cmd.Dir = root
	// A model turn spawns a tree -- tool subprocesses, and whatever the client
	// starts for itself. Without this, canceling leaves them running and
	// blocks here until the longest-lived one exits, which is how a ctrl-c at
	// two seconds turned into a twenty-seven second wait and a twenty-seven
	// second row in the base.
	procgroup.Contain(cmd)
	stdout, runErr := cmd.Output()
	peak := procstat.PeakRSS(cmd.ProcessState)

	var stderr string
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		stderr = strings.TrimSpace(string(exit.Stderr))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return envelope{}, peak, contract.Stopped(ctxErr, "claude code", r.timeout).WithRaw(stderr)
	}

	out, parseErr := parse(stdout)
	if parseErr != nil {
		// A turn that failed before it could print an envelope says so in
		// plain text on stderr -- a rejected session id, a bad flag. There is
		// no structure to sort, so the raw line travels for whoever debugs it.
		if runErr != nil {
			return envelope{}, peak, failureFor(stderr, runErr)
		}
		return envelope{}, peak, parseErr
	}
	if out.IsError {
		// The envelope travels with the error on purpose. A turn that ran for
		// a minute and then died at its ceiling occupied the machine for that
		// minute and charged for it, and its PRICE is right here: returning an
		// empty one puts that money on nobody's bill.
		//
		// Its TOKENS are not here, and on this path they are not anywhere.
		// Measured 2026-08-16 on the live CLI (2.1.232): a turn killed at
		// --max-budget-usd prints `total_cost_usd: 0.41228` beside usage that
		// is zero in every lane, because the price is a cost ledger charged as
		// the work happens while usage is summed only at `message_stop`, which
		// a killed message never reaches. This adapter asks for
		// `--output-format json` -- one envelope, no event stream -- so unlike
		// internal/agent/model's held-open turn there is no second reading to
		// recover them from. See that package's conversation.charge.
		//
		// So a ceiling death here records a real dollar figure against zero
		// tokens. That pairing is the codebase's existing way of saying "not
		// recorded" -- see cmd/atenea/floor.go, which prints exactly that for
		// a row carrying a price and no token count -- and it is a limit of
		// this envelope, not a measurement of a turn that read nothing.
		return out, peak, failureFor(out.reason(), runErr)
	}
	return out, peak, nil
}

// envelope is the JSON one headless turn prints.
type envelope struct {
	// IsError is the only field that tells the truth about failure. Measured:
	// a turn that failed to authenticate still reported subtype "success", so
	// reading the subtype instead would call an error a result.
	IsError bool `json:"is_error"`
	// Subtype names how the turn ended. Measured: an authentication failure
	// reports "success" here, so it can never be the first field consulted.
	Subtype string `json:"subtype"`
	// Result is the text answer, and on a failure the message -- when there is
	// one at all.
	Result string `json:"result"`
	// TerminalReason and Errors are where the reason lives when the turn died
	// without producing a result.
	//
	// Measured on a real turn stopped at its spending ceiling: no `result`
	// field is printed whatsoever, while `errors` says "Reached maximum budget
	// ($0.25)", `terminal_reason` says "budget_exhausted" and `subtype` says
	// "error_max_budget_usd". Reading only `result` left this adapter holding
	// the child's exit status, which names nothing, so a ceiling of ours that
	// was too small was filed as the provider being unreachable -- the one bin
	// that marks a provider down and takes it out of the funnel.
	TerminalReason string   `json:"terminal_reason"`
	Errors         []string `json:"errors"`
	// StructuredOutput is where --json-schema puts the answer.
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            usage           `json:"usage"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	NumTurns         int             `json:"num_turns"`
	// PermissionDenials lists what the turn was refused. A non-empty list is
	// the permission bin regardless of how the turn ended.
	PermissionDenials []json.RawMessage `json:"permission_denials"`
}

// reason is the best sentence the far side gave for a failure.
//
// The order is measured rather than preferred. `result` carries the message
// when the turn produced one; `errors` and `terminal_reason` carry it when the
// turn died before producing anything; `subtype` comes last because it reports
// "success" on turns that failed, which is the trap the IsError field exists
// to sidestep. Empty is a real answer here -- it means the far side said
// nothing about its own failure, and the caller falls back to the exit status.
func (e envelope) reason() string {
	for _, candidate := range []string{
		e.Result,
		strings.Join(e.Errors, "; "),
		e.TerminalReason,
		e.Subtype,
	} {
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

// total is what the run actually cost in tokens. Cache reads are counted
// because they were paid for, even if cheaply: a baseline that ignored them
// would make a warm repository look free.
func (u usage) total() int {
	return u.InputTokens + u.OutputTokens + u.CacheRead + u.CacheWrite
}

// parse reads the envelope out of stdout.
func parse(stdout []byte) (envelope, error) {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return envelope{}, contract.Fail(contract.FailureUnavailable,
			"claude code printed nothing")
	}
	var out envelope
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return envelope{}, contract.Fail(contract.FailureUnavailable,
			"claude code printed something this adapter cannot read").WithRaw(trimmed)
	}
	return out, nil
}

// failureFor sorts one Claude Code failure into the shared bins.
//
// The bins are the whole point of an adapter: whatever wording the client
// invents, the core only ever sees one of six, with the untranslated text
// traveling beside it for whoever debugs later.
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
		// A client nobody is logged into is a provider unreachable from here,
		// which is the bin that drives fallback to somebody who is.
		return contract.Fail(contract.FailureUnavailable,
			"claude code is not logged in on this machine").WithRaw(text)
	case strings.Contains(lower, "budget"):
		// Money is a permission, so running out of it is a refusal, not
		// slowness. The ceiling is one Atenea set and passed down itself,
		// which is exactly what this bin means: an action refused on this
		// machine. Calling it a timeout said the provider was slow, sent the
		// reader to look at latency, and hid the one fact that would have
		// fixed it -- the grant was too small.
		//
		// Nothing about the fallback changes: no bin but `unavailable` marks
		// a provider down, and a ceiling reached says nothing about health.
		// It is checked before the generic refusal below so the reason names
		// the ceiling rather than saying "refused the work".
		return contract.Fail(contract.FailurePermissionDenied,
			"claude code stopped at its spending ceiling").WithRaw(text)
	case strings.Contains(lower, "permission"), strings.Contains(lower, "denied"):
		return contract.Fail(contract.FailurePermissionDenied,
			"claude code refused the work").WithRaw(text)
	case strings.Contains(lower, "max turns"), strings.Contains(lower, "max_turns"):
		// The turn ceiling stays a timeout, and the difference is who set it:
		// Atenea never grants turns, so this is the far side giving up on its
		// own rather than a grant of ours running out.
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

// ---------------------------------------------------------------------------
// Translating out
// ---------------------------------------------------------------------------

// outputSchema is the capability's declared answer shape as a JSON string,
// which is the only form the CLI's --json-schema flag takes.
//
// The shape itself belongs to the contract, next to the validator that judges
// the answer coming back: this adapter asks a far side to fill in a form and
// then checks the form, and those two had better be the same form. All that is
// left here is the encoding.
func outputSchema(capability contract.Capability) (string, error) {
	root, err := capability.OutputSchema()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return "", contract.Fail(contract.FailureInvalidInput,
			"output schema cannot be expressed as JSON: %v", err)
	}
	return string(encoded), nil
}

// prompt is what the far side is actually told.
//
// It is deliberately an instruction and not a conversation: the capability
// already declares what it means, so the prompt transcribes that and adds the
// two things the declaration cannot carry -- where to look and what never to
// open. Everything about how to find it is left to Claude Code, which is the
// entire reason a thinking far side is worth its cost.
func prompt(req contract.RunRequest, ask search, sensitive []string) string {
	var b strings.Builder
	b.WriteString("Search this repository and report every match.\n\n")

	b.WriteString("What is being asked of you, in Atenea's own words:\n")
	b.WriteString("  capability: " + req.Capability.ID + "\n")
	b.WriteString("  summary:    " + req.Capability.Summary + "\n")
	if req.Capability.Semantics != "" {
		b.WriteString("  semantics:  " + oneLine(req.Capability.Semantics) + "\n")
	}

	b.WriteString("\nThe query, verbatim between the markers:\n<<<\n")
	b.WriteString(ask.query)
	b.WriteString("\n>>>\n")

	b.WriteString("\nHow to read the query:\n")
	b.WriteString("  - " + literalOrRegex(ask.regex) + "\n")
	b.WriteString("  - " + caseRule(ask.matchCase) + "\n")
	if ask.wholeWord {
		b.WriteString("  - Match whole words only.\n")
	}
	if len(ask.fileTypes) > 0 {
		b.WriteString("  - Only these file extensions: " + strings.Join(ask.fileTypes, ", ") + "\n")
	}
	if len(ask.scope) > 0 {
		b.WriteString("  - Only under these paths: " + strings.Join(ask.scope, ", ") + "\n")
	} else {
		b.WriteString("  - The whole repository, from the working directory down.\n")
	}
	if len(sensitive) > 0 {
		b.WriteString("  - Never open or report a file matching: " +
			strings.Join(sensitive, ", ") + "\n")
	}

	b.WriteString("\nHow to answer:\n")
	b.WriteString("  - Use your Grep tool. Do not guess a match and do not invent one.\n")
	b.WriteString("  - Report every match you find, not a summary of them.\n")
	b.WriteString("  - path: relative to the working directory, using forward slashes.\n")
	b.WriteString("  - line: 1-based line number.\n")
	b.WriteString("  - column: 1-based column where the match starts on that line.\n")
	fmt.Fprintf(&b,
		"  - snippet: the matching line, with up to %d line(s) of context above and below.\n",
		ask.contextLines)
	b.WriteString("  - No matches is a valid answer: return an empty list.\n")
	return b.String()
}

func literalOrRegex(regex bool) string {
	if regex {
		return "Treat the query as a regular expression."
	}
	return "Treat the query as literal text, not a pattern."
}

func caseRule(matchCase bool) string {
	if matchCase {
		return "Case matters: match the query exactly as written."
	}
	return "Ignore case."
}

func oneLine(value string) string { return strings.Join(strings.Fields(value), " ") }

// ---------------------------------------------------------------------------
// Translating back
// ---------------------------------------------------------------------------

// readAnswer turns the structured output into the capability's own shape.
//
// A thinking far side can be wrong in ways a tool cannot: it can report a file
// it was told not to open, or a path outside the repository it was pointed at.
// Trusting the instruction alone would make the security design advisory, so
// what comes back is checked again here, where the answer can still be refused.
func (r *Runner) readAnswer(out envelope, req contract.RunRequest, ask search) (map[string]any, int, error) {
	if len(out.PermissionDenials) > 0 {
		return nil, 0, contract.Fail(contract.FailurePermissionDenied,
			"claude code was refused %d action(s) it needed", len(out.PermissionDenials)).
			WithRaw(out.Result)
	}
	if len(out.StructuredOutput) == 0 {
		// The turn ended without the shape it was asked for. That is not a
		// search with no matches -- it is a search that did not happen.
		return nil, 0, contract.Fail(contract.FailureUnavailable,
			"claude code answered without the structure it was asked for").
			WithRaw(out.Result)
	}
	var answer map[string]any
	if err := json.Unmarshal(out.StructuredOutput, &answer); err != nil {
		return nil, 0, contract.Fail(contract.FailureUnavailable,
			"claude code's structured answer is not an object").
			WithRaw(string(out.StructuredOutput))
	}

	raw, _ := answer["matches"].([]any)
	matches := make([]any, 0, len(raw))
	droppedOutOfScope := 0
	for _, item := range raw {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hit, keep, outOfScope := r.cleanHit(record, req.Repository.ID, ask)
		if outOfScope {
			droppedOutOfScope++
			continue
		}
		if !keep {
			continue
		}
		matches = append(matches, hit)
	}
	return map[string]any{"matches": matches}, droppedOutOfScope, nil
}

// cleanHit checks one reported match and normalises it, or drops it.
//
// Repository containment, sensitivity and file type drop silently, the same
// way omp drops them: a search that reported "1 match in .env" would leak
// the very thing the list exists to protect, and one that stopped to ask
// would break the flow over a file the user never wanted looked at. Scope is
// different in kind, not degree -- it is a request-shaping constraint, not a
// secret, so a match that fell outside it is worth telling the caller about
// rather than hiding. The caller finds out through the outOfScope return and
// an aggregate Notice on the Outcome, never a Notice per hit.
func (r *Runner) cleanHit(record map[string]any, repositoryID string, ask search) (hit map[string]any, keep bool, outOfScope bool) {
	name, _ := record["path"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false, false
	}
	relative, inside := insideRepository(name)
	if !inside {
		return nil, false, false
	}
	if !inScope(relative, ask.scope) {
		return nil, false, true
	}
	if r.isSensitive(relative) {
		return nil, false, false
	}
	if len(ask.fileTypes) > 0 && !wantedType(relative, ask.fileTypes) {
		return nil, false, false
	}
	line, ok := positive(record["line"])
	if !ok {
		return nil, false, false
	}
	column, ok := positive(record["column"])
	if !ok {
		// A far side that found the line but not the column has still found
		// the line. The capability requires a column, and the first one is the
		// only honest answer available: it points at the match's line, not at
		// a position nobody measured.
		column = 1
	}
	out := map[string]any{"path": relative, "line": line, "column": column}
	if snippet, ok := record["snippet"].(string); ok {
		out["snippet"] = snippet
	}
	return out, true, false
}

// insideRepository reports the repository-relative path of a reported hit, or
// false when it does not sit under the working directory the turn was given.
func insideRepository(name string) (string, bool) {
	clean := filepath.ToSlash(filepath.Clean(name))
	if filepath.IsAbs(name) {
		// The far side was run with the repository as its working directory,
		// so an absolute path is only usable if it still points inside it.
		// There is nothing to resolve it against here, so it is refused: a
		// path the caller cannot open is not an answer.
		return "", false
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return strings.TrimPrefix(clean, "./"), true
}

// isSensitive matches the patterns against both the bare file name and the
// repository-relative path, so `.env` catches a root file and `config/*.pem`
// catches a nested one.
func (r *Runner) isSensitive(relative string) bool {
	for _, pattern := range r.sensitive {
		if ok, _ := path.Match(pattern, relative); ok {
			return true
		}
		if ok, _ := path.Match(pattern, path.Base(relative)); ok {
			return true
		}
	}
	return false
}

// wantedType reports whether a path carries one of the requested extensions.
func wantedType(relative string, types []string) bool {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(relative)), ".")
	for _, want := range types {
		if strings.EqualFold(ext, strings.TrimPrefix(want, ".")) {
			return true
		}
	}
	return false
}

// inScope reports whether relative sits under one of the requested scope
// paths. An empty scope means the whole repository was in play, the same
// reading omp gives an empty scope when it builds its own search targets;
// "." means the same thing spelled out. A scope entry only matches a proper
// path prefix -- "internal/adapter" must never also match
// "internal/adapter2" -- the same boundary targets() draws with filepath.Rel.
func inScope(relative string, scope []string) bool {
	if len(scope) == 0 {
		return true
	}
	for _, entry := range scope {
		clean := strings.TrimPrefix(path.Clean(filepath.ToSlash(entry)), "./")
		if clean == "." || relative == clean || strings.HasPrefix(relative, clean+"/") {
			return true
		}
	}
	return false
}

// positive reads a 1-based coordinate. JSON numbers arrive as float64, so a
// whole number is what is being checked for, not an int.
func positive(value any) (int, bool) {
	switch n := value.(type) {
	case float64:
		if n < 1 || n != float64(int64(n)) {
			return 0, false
		}
		return int(n), true
	case int:
		if n < 1 {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// ---------------------------------------------------------------------------
// Payload readers
// ---------------------------------------------------------------------------

func stringsAt(payload map[string]any, key string) []string {
	raw, ok := payload[key].([]any)
	if !ok {
		if direct, ok := payload[key].([]string); ok {
			return slices.Clone(direct)
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func boolAt(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

func intAt(payload map[string]any, key string) (int, bool) {
	switch n := payload[key].(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}
