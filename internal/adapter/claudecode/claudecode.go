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

	"github.com/Tutitoos/atenea/internal/procstat"
	"github.com/Tutitoos/atenea/internal/toolversion"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// CodeSearch is the one capability this adapter is wired to.
const CodeSearch = "code.search"

// DefaultBinary is the command looked up on PATH when the settings name none.
const DefaultBinary = "claude"

// DefaultTimeout caps one invocation. A model turn is slower than a tool call
// by nature, so this is far above omp's ceiling; past it the turn is stuck
// rather than thinking, and the timeout bin is what lets the core fall back.
const DefaultTimeout = 5 * time.Minute

// DefaultBudgetUSD caps what one commission may spend.
//
// A model turn with no ceiling is a runaway, and unlike a tool call it costs
// real money. The number is deliberately small: this adapter answers a flat
// text search, and a search that needs more than this has gone wrong.
const DefaultBudgetUSD = 0.25

// defaultContextLines matches the capability's declared semantics.
const defaultContextLines = 2

// Options configure the adapter. Everything here is declared in the settings
// file, so retuning it never means touching Go.
type Options struct {
	// Binary is the claude executable. A bare name is looked up on PATH.
	Binary string
	// Implementations this adapter answers for, by implementation id.
	Implementations []string
	// Sensitive holds the path patterns that carry secrets. Unlike a tool
	// runner this adapter cannot filter the far side's reading after the fact,
	// so the patterns are handed to Claude Code as an instruction and the
	// answer is filtered again on the way back. Belt and braces, because the
	// far side is the one thing here that can decide for itself.
	Sensitive []string
	// BudgetUSD caps what one invocation may spend. Zero takes
	// DefaultBudgetUSD.
	BudgetUSD float64
	// Timeout caps one invocation. Zero takes DefaultTimeout.
	Timeout time.Duration
}

// Runner answers capabilities by driving the Claude Code CLI.
type Runner struct {
	binary          string
	implementations []string
	sensitive       []string
	budget          float64
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
	if opts.BudgetUSD < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"claude-code adapter: budget must not be negative, got %v", opts.BudgetUSD)
	}
	if opts.Timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"claude-code adapter: timeout must not be negative, got %s", opts.Timeout)
	}
	runner := &Runner{
		binary:          strings.TrimSpace(opts.Binary),
		implementations: slices.Clone(opts.Implementations),
		sensitive:       slices.Clone(opts.Sensitive),
		budget:          opts.BudgetUSD,
		timeout:         opts.Timeout,
	}
	if runner.binary == "" {
		runner.binary = DefaultBinary
	}
	if runner.budget == 0 {
		runner.budget = DefaultBudgetUSD
	}
	if runner.timeout == 0 {
		runner.timeout = DefaultTimeout
	}
	runner.version = toolversion.New(runner.binary, "--version")
	return runner, nil
}

// ID names the runner on the status screen.
func (r *Runner) ID() string { return "claude-code" }

// Serves reports whether this adapter answers for that implementation.
func (r *Runner) Serves(implementationID string) bool {
	return slices.Contains(r.implementations, implementationID)
}

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
	if missing, ok := req.Allowed(); !ok {
		return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied,
			"%s causes %s, which the commission does not cover", req.Capability.ID, missing)
	}
	if !r.Serves(req.Implementation.ID) {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"claude-code adapter does not serve implementation %s", req.Implementation.ID)
	}
	if req.Capability.ID != CodeSearch {
		return contract.Outcome{}, contract.Fail(contract.FailureNotFound,
			"claude-code adapter has no implementation of %s", req.Capability.ID)
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
	if err != nil {
		return contract.Outcome{}, err
	}

	result, err := r.readAnswer(answer, req, ask)
	if err != nil {
		return contract.Outcome{}, err
	}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"claude code answered a shape this capability does not accept: %v", err).
			WithRaw(answer.Result)
	}

	spent := contract.Sample{
		Duration: time.Since(started),
		Tokens:   answer.Usage.total(),
		PeakRSS:  peak,
	}
	outcome := contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		Spent:   spent,
	}
	if answer.TotalCostUSD > 0 {
		// The measurement base has three axes -- time, tokens, memory -- and
		// money is not one of them. Rounding dollars into the token count
		// would poison the baseline the selector learns from, so the figure
		// is reported as what it is: a fact about this run, in the one
		// channel that carries facts.
		outcome.Discoveries = append(outcome.Discoveries, contract.Discovery{
			Level: contract.ContextRepository,
			Note: fmt.Sprintf("claude code answered %s for $%.4f over %d turn(s)",
				req.Repository.ID, answer.TotalCostUSD, answer.NumTurns),
		})
	}
	return outcome, nil
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
	schema, err := jsonSchema(req.Capability.Outputs)
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
		// what a capability means, so every customisation the client would
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
		"--max-budget-usd", strconv.FormatFloat(r.budget, 'f', -1, 64),
	}, nil
}

// invoke runs one turn and hands back the envelope and what the child weighed.
//
// The weight comes back even when the turn failed. A model call that ran for
// two minutes and then errored still occupied the machine for two minutes, and
// a baseline that only counted the successes would rank it as cheap.
func (r *Runner) invoke(ctx context.Context, root string, req contract.RunRequest, ask search) (envelope, int64, error) {
	binary, err := exec.LookPath(r.binary)
	if err != nil {
		return envelope{}, 0, contract.Fail(contract.FailureUnavailable,
			"claude code is not installed: %q is not on PATH", r.binary)
	}
	argv, err := r.args(req, ask)
	if err != nil {
		return envelope{}, 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Dir = root
	stdout, runErr := cmd.Output()
	peak := procstat.PeakRSS(cmd.ProcessState)

	var stderr string
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		stderr = strings.TrimSpace(string(exit.Stderr))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return envelope{}, peak, contract.Fail(contract.FailureTimeout,
			"claude code took longer than %s", r.timeout).WithRaw(stderr)
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
		return envelope{}, peak, failureFor(out.Result, runErr)
	}
	return out, peak, nil
}

// envelope is the JSON one headless turn prints.
type envelope struct {
	// IsError is the only field that tells the truth about failure. Measured:
	// a turn that failed to authenticate still reported subtype "success", so
	// reading the subtype instead would call an error a result.
	IsError bool `json:"is_error"`
	// Subtype names how the turn ended when it ended cleanly.
	Subtype string `json:"subtype"`
	// Result is the text answer, and on a failure the message.
	Result string `json:"result"`
	// StructuredOutput is where --json-schema puts the answer.
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            usage           `json:"usage"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	NumTurns         int             `json:"num_turns"`
	// PermissionDenials lists what the turn was refused. A non-empty list is
	// the permission bin regardless of how the turn ended.
	PermissionDenials []json.RawMessage `json:"permission_denials"`
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
	case strings.Contains(lower, "permission"), strings.Contains(lower, "denied"):
		return contract.Fail(contract.FailurePermissionDenied,
			"claude code refused the work").WithRaw(text)
	case strings.Contains(lower, "budget"):
		// A ceiling reached is not a crash: the turn stopped early and the
		// answer is not there. Timeout is the bin for "gave up before
		// finishing", and it is the one that lets the core try somebody else.
		return contract.Fail(contract.FailureTimeout,
			"claude code stopped at its spending ceiling").WithRaw(text)
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

// ---------------------------------------------------------------------------
// Translating out
// ---------------------------------------------------------------------------

// jsonSchema turns a capability's declared output shape into the JSON Schema
// Claude Code validates its structured answer against.
//
// This is the load-bearing half of talking to a far side that thinks. A tool
// answers in whatever format it answers in and the adapter parses it; a model
// answers in whatever it was asked for, so asking precisely is the difference
// between an answer and an essay. The capability already declares the shape,
// so nothing is invented here -- it is transcribed.
func jsonSchema(fields []contract.Field) (string, error) {
	root, err := objectSchema(fields)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return "", contract.Fail(contract.FailureInvalidInput,
			"output schema cannot be expressed as JSON Schema: %v", err)
	}
	return string(encoded), nil
}

func objectSchema(fields []contract.Field) (map[string]any, error) {
	properties := make(map[string]any, len(fields))
	required := make([]string, 0, len(fields))
	for _, field := range fields {
		entry, err := fieldSchema(field)
		if err != nil {
			return nil, err
		}
		properties[field.Name] = entry
		if field.Required {
			required = append(required, field.Name)
		}
	}
	out := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		out["required"] = required
	}
	return out, nil
}

func fieldSchema(field contract.Field) (map[string]any, error) {
	var out map[string]any
	switch field.Type {
	case contract.TypeString:
		out = map[string]any{"type": "string"}
	case contract.TypeInt:
		out = map[string]any{"type": "integer"}
	case contract.TypeBool:
		out = map[string]any{"type": "boolean"}
	case contract.TypeStringList:
		out = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	case contract.TypeRecord:
		nested, err := objectSchema(field.Fields)
		if err != nil {
			return nil, err
		}
		out = nested
	case contract.TypeRecordList:
		nested, err := objectSchema(field.Fields)
		if err != nil {
			return nil, err
		}
		out = map[string]any{"type": "array", "items": nested}
	default:
		return nil, contract.Fail(contract.FailureInvalidInput,
			"field %s has a type this adapter cannot describe: %s", field.Name, field.Type)
	}
	if field.Summary != "" {
		// The summary is the only place the capability says what a field
		// MEANS. A far side that reads it fills the field correctly; one that
		// does not was going to guess anyway.
		out["description"] = field.Summary
	}
	return out, nil
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
func (r *Runner) readAnswer(out envelope, req contract.RunRequest, ask search) (map[string]any, error) {
	if len(out.PermissionDenials) > 0 {
		return nil, contract.Fail(contract.FailurePermissionDenied,
			"claude code was refused %d action(s) it needed", len(out.PermissionDenials)).
			WithRaw(out.Result)
	}
	if len(out.StructuredOutput) == 0 {
		// The turn ended without the shape it was asked for. That is not a
		// search with no matches -- it is a search that did not happen.
		return nil, contract.Fail(contract.FailureUnavailable,
			"claude code answered without the structure it was asked for").
			WithRaw(out.Result)
	}
	var answer map[string]any
	if err := json.Unmarshal(out.StructuredOutput, &answer); err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"claude code's structured answer is not an object").
			WithRaw(string(out.StructuredOutput))
	}

	raw, _ := answer["matches"].([]any)
	matches := make([]any, 0, len(raw))
	for _, item := range raw {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hit, keep := r.cleanHit(record, req.Repository.ID, ask)
		if !keep {
			continue
		}
		matches = append(matches, hit)
	}
	return map[string]any{"matches": matches}, nil
}

// cleanHit checks one reported match and normalises it, or drops it.
//
// Dropping is silent on purpose, exactly as it is for a sensitive file in the
// other adapter: a search that reported "1 match in .env" would leak the very
// thing the list exists to protect, and one that stopped to ask would break
// the flow over a file the user never wanted looked at.
func (r *Runner) cleanHit(record map[string]any, repositoryID string, ask search) (map[string]any, bool) {
	name, _ := record["path"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	relative, inside := insideRepository(name)
	if !inside {
		return nil, false
	}
	if r.isSensitive(relative) {
		return nil, false
	}
	if len(ask.fileTypes) > 0 && !wantedType(relative, ask.fileTypes) {
		return nil, false
	}
	line, ok := positive(record["line"])
	if !ok {
		return nil, false
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
	return out, true
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
