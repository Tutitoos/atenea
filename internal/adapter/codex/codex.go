// Package codex is an adapter for the Codex CLI's non-interactive exec mode.
//
// Codex is a model-backed provider, not a search parser. Atenea gives it one
// read-only turn, asks for the capability's output schema, and validates and
// confines the answer before it crosses the runner boundary. The adapter has
// its own command and event parser because Codex's JSONL event stream is not
// Claude Code's JSON envelope.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/internal/procstat"
	"github.com/Tutitoos/atenea/internal/toolpath"
	"github.com/Tutitoos/atenea/internal/toolversion"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const CodeSearch = "code.search"

// DefaultBinary is retained for callers that configured one explicit binary.
const DefaultBinary = "codex"

const DefaultTerminalBinary = "codex"

const DefaultAppBinary = "/Applications/ChatGPT.app/Contents/Resources/codex"

const DefaultTimeout = 90 * time.Second

const defaultContextLines = 2

type Options struct {
	Binary          string
	Source          string
	TerminalBinary  string
	AppBinary       string
	Implementations []string
	Sensitive       []string
	Timeout         time.Duration
}

type Runner struct {
	binary          string
	source          string
	terminalBinary  string
	appBinary       string
	implementations []string
	sensitive       []string
	timeout         time.Duration
	version         *toolversion.Probe
}

func New(opts Options) (*Runner, error) {
	for _, pattern := range opts.Sensitive {
		if _, err := path.Match(pattern, "probe"); err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"codex adapter: sensitive pattern %q: %v", pattern, err)
		}
	}
	if opts.Timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"codex adapter: timeout must not be negative, got %s", opts.Timeout)
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
		runner.terminalBinary = DefaultTerminalBinary
	}
	if runner.appBinary == "" {
		runner.appBinary = DefaultAppBinary
	}
	if runner.binary == "" {
		if err := toolpath.ValidateSource(runner.source, runner.candidates()); err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput, "codex adapter: %v", err)
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

// Surface reports the selected executable without exposing authentication or
// environment details. It is used by `atenea status` to make terminal/app
// selection visible.
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

func (r *Runner) ID() string { return "codex" }

func (r *Runner) Serves(implementationID string) bool {
	return slices.Contains(r.implementations, implementationID)
}

func (r *Runner) Capabilities() []string { return []string{CodeSearch} }

func (r *Runner) Implementations() []string {
	out := slices.Clone(r.implementations)
	slices.Sort(out)
	return out
}

func (r *Runner) Run(ctx context.Context, req contract.RunRequest) (out contract.Outcome, err error) {
	defer func() { out.ToolVersion = r.version.Version(ctx) }()
	if err := req.Validate(); err != nil {
		return contract.Outcome{}, err
	}
	if !r.Serves(req.Implementation.ID) {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"codex adapter does not serve implementation %s", req.Implementation.ID)
	}
	if req.Capability.ID != CodeSearch {
		return contract.Outcome{}, contract.Fail(contract.FailureNotFound,
			"codex adapter has no implementation of %s", req.Capability.ID)
	}
	if !req.Permission.Funded() {
		return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied,
			"codex costs money and the commission has none left to spend")
	}

	started := time.Now()
	ask, err := readSearch(req.Payload)
	if err != nil {
		return contract.Outcome{}, err
	}
	root, err := filepath.Abs(req.Repository.Path)
	if err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"repository %s: path cannot be resolved", req.Repository.ID)
	}
	if err := validateScope(root, ask.scope, req.Repository.ID); err != nil {
		return contract.Outcome{}, err
	}

	answer, peak, err := r.invoke(ctx, root, req, ask)
	outcome := contract.Outcome{Spent: contract.Sample{
		Duration: time.Since(started),
		Tokens:   answer.Usage.total(),
		PeakRSS:  peak,
	}, SpentUSD: answer.CostUSD}
	if err != nil {
		return outcome, err
	}
	if answer.CostUSD > req.Permission.BudgetUSD {
		return outcome, contract.Fail(contract.FailurePermissionDenied,
			"codex reported a cost above the commission budget")
	}

	result, dropped, err := r.readAnswer(answer, root, ask)
	if err != nil {
		return outcome, err
	}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return outcome, contract.Fail(contract.FailureUnavailable,
			"codex answered a shape this capability does not accept")
	}
	outcome.Result = result
	outcome.Verdict = contract.VerdictOK
	outcome.OutOfScope = dropped
	if dropped > 0 {
		outcome.Notices = append(outcome.Notices, fmt.Sprintf(
			"%d match(es) fell outside the requested scope and were dropped", dropped))
	}
	if answer.CostUSD == 0 {
		outcome.Notices = append(outcome.Notices,
			"Codex CLI did not report monetary usage; the Atenea budget gated dispatch but could not be metered")
	}
	return outcome, nil
}

type search struct {
	query        string
	scope        []string
	fileTypes    []string
	matchCase    bool
	regex        bool
	wholeWord    bool
	contextLines int
}

func readSearch(payload map[string]any) (search, error) {
	query, _ := payload["query"].(string)
	if strings.TrimSpace(query) == "" {
		return search{}, contract.Fail(contract.FailureInvalidInput, "query is empty")
	}
	out := search{
		query:        query,
		scope:        stringsAt(payload, "scope"),
		fileTypes:    stringsAt(payload, "file_types"),
		matchCase:    boolAt(payload, "match_case"),
		regex:        boolAt(payload, "regex"),
		wholeWord:    boolAt(payload, "whole_word"),
		contextLines: defaultContextLines,
	}
	if lines, ok := intAt(payload, "context_lines"); ok {
		if lines < 0 {
			return search{}, contract.Fail(contract.FailureInvalidInput,
				"context_lines must not be negative, got %d", lines)
		}
		out.contextLines = lines
	}
	return out, nil
}

func (r *Runner) invoke(ctx context.Context, root string, req contract.RunRequest, ask search) (answer response, peak int64, err error) {
	resolved, err := r.resolve()
	if err != nil {
		return response{}, 0, contract.Fail(contract.FailureUnavailable,
			"codex executable is unavailable for source %q", r.source)
	}
	schema, err := strictOutputSchema(req.Capability)
	if err != nil {
		return response{}, 0, err
	}
	schemaFile, err := os.CreateTemp("", "atenea-codex-schema-*.json")
	if err != nil {
		return response{}, 0, contract.Fail(contract.FailureUnavailable,
			"codex output schema could not be prepared")
	}
	schemaName := schemaFile.Name()
	defer os.Remove(schemaName)
	if _, err := schemaFile.Write(schema); err != nil {
		_ = schemaFile.Close()
		return response{}, 0, contract.Fail(contract.FailureUnavailable,
			"codex output schema could not be written")
	}
	if err := schemaFile.Close(); err != nil {
		return response{}, 0, contract.Fail(contract.FailureUnavailable,
			"codex output schema could not be closed")
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	args := []string{
		"exec",
		"--ephemeral",
		"--sandbox", "read-only",
		"--ignore-user-config",
		"--ignore-rules",
		"--cd", root,
		"--output-schema", schemaName,
		"--json",
		"--color", "never",
	}
	cmd := exec.CommandContext(ctx, resolved.Path, args...)
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(prompt(req, ask, r.sensitive))
	procgroup.Contain(cmd)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	peak = procstat.PeakRSS(cmd.ProcessState)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return response{}, peak, contract.Stopped(ctxErr, "codex", r.timeout)
	}
	parsed, parseErr := parseEvents(stdout.String())
	if parseErr != nil {
		if runErr != nil {
			return response{}, peak, classifyFailure(parsed.ErrorText+" "+stderr.String(), runErr)
		}
		return response{}, peak, parseErr
	}
	if runErr != nil {
		return response{}, peak, classifyFailure(parsed.ErrorText+" "+stderr.String(), runErr)
	}
	if parsed.Message == "" {
		return response{}, peak, contract.Fail(contract.FailureUnavailable,
			"codex completed without a final JSON answer")
	}
	var out response
	if err := json.Unmarshal([]byte(parsed.Message), &out.Structured); err != nil {
		return response{}, peak, contract.Fail(contract.FailureUnavailable,
			"codex returned invalid JSON")
	}
	out.Usage = parsed.Usage
	out.CostUSD = parsed.CostUSD
	return out, peak, nil
}

type response struct {
	Structured map[string]any
	Usage      usage
	CostUSD    float64
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	Cached       int `json:"cached_input_tokens"`
	CacheRead    int `json:"cache_read_input_tokens"`
	CacheWrite   int `json:"cache_write_input_tokens"`
}

func (u usage) total() int {
	return u.InputTokens + u.OutputTokens + u.Cached + u.CacheRead + u.CacheWrite
}

type eventStream struct {
	Message   string
	ErrorText string
	Usage     usage
	CostUSD   float64
}

func parseEvents(stdout string) (eventStream, error) {
	var out eventStream
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return out, contract.Fail(contract.FailureUnavailable,
				"codex emitted invalid JSONL")
		}
		var kind string
		_ = json.Unmarshal(raw["type"], &kind)
		switch kind {
		case "item.completed":
			var item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw["item"], &item); err == nil && item.Type == "agent_message" {
				out.Message = item.Text
			}
		case "turn.completed":
			var done struct {
				Usage   usage   `json:"usage"`
				CostUSD float64 `json:"total_cost_usd"`
			}
			if err := json.Unmarshal(raw["usage"], &done.Usage); err != nil {
				_ = json.Unmarshal(raw["usage"], &done)
			}
			_ = json.Unmarshal(raw["usage"], &out.Usage)
			_ = json.Unmarshal(raw["total_cost_usd"], &out.CostUSD)
		case "error", "turn.failed":
			out.ErrorText += " " + eventMessage(raw)
		}
	}
	if err := scanner.Err(); err != nil {
		return out, contract.Fail(contract.FailureUnavailable,
			"codex output could not be read")
	}
	if strings.TrimSpace(stdout) == "" {
		return out, contract.Fail(contract.FailureUnavailable,
			"codex printed no JSONL events")
	}
	return out, nil
}

func eventMessage(raw map[string]json.RawMessage) string {
	var message string
	_ = json.Unmarshal(raw["message"], &message)
	if message != "" {
		return message
	}
	var nested struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw["error"], &nested)
	return nested.Message
}

func classifyFailure(text string, runErr error) *contract.Failure {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "auth"), strings.Contains(lower, "login"), strings.Contains(lower, "oauth"), strings.Contains(lower, "credential"):
		return contract.Fail(contract.FailureUnavailable, "codex is not authenticated")
	case strings.Contains(lower, "permission"), strings.Contains(lower, "approval"), strings.Contains(lower, "sandbox"):
		return contract.Fail(contract.FailurePermissionDenied, "codex refused the read-only operation")
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"):
		return contract.Fail(contract.FailureTimeout, "codex timed out")
	case errors.Is(runErr, exec.ErrNotFound):
		return contract.Fail(contract.FailureUnavailable, "codex is not installed")
	default:
		return contract.Fail(contract.FailureUnavailable, "codex process failed")
	}
}

func strictOutputSchema(capability contract.Capability) ([]byte, error) {
	schema, err := capability.OutputSchema()
	if err != nil {
		return nil, err
	}
	makeStrict(schema)
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"codex output schema cannot be encoded")
	}
	return encoded, nil
}

// Codex uses strict structured outputs: every object property must be listed
// as required, including optional capability fields. The adapter still
// accepts the capability's looser contract on the way back.
func makeStrict(node map[string]any) {
	if properties, ok := node["properties"].(map[string]any); ok {
		names := make([]string, 0, len(properties))
		for name, child := range properties {
			names = append(names, name)
			if childMap, ok := child.(map[string]any); ok {
				makeStrict(childMap)
			}
		}
		slices.Sort(names)
		node["required"] = names
	}
	if items, ok := node["items"].(map[string]any); ok {
		makeStrict(items)
	}
}

func (r *Runner) readAnswer(answer response, root string, ask search) (map[string]any, int, error) {
	raw, ok := answer.Structured["matches"].([]any)
	if !ok {
		return nil, 0, contract.Fail(contract.FailureUnavailable,
			"codex structured output has no matches list")
	}
	matches := make([]any, 0, len(raw))
	dropped := 0
	for _, item := range raw {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hit, keep, outside := r.cleanHit(record, root, ask)
		if outside {
			dropped++
			continue
		}
		if keep {
			matches = append(matches, hit)
		}
	}
	return map[string]any{"matches": matches}, dropped, nil
}

func (r *Runner) cleanHit(record map[string]any, root string, ask search) (map[string]any, bool, bool) {
	name, _ := record["path"].(string)
	name = strings.TrimSpace(name)
	if name == "" || filepath.IsAbs(name) {
		return nil, false, false
	}
	joined := filepath.Clean(filepath.Join(root, filepath.FromSlash(name)))
	relative, err := filepath.Rel(root, joined)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, false, false
	}
	relative = filepath.ToSlash(relative)
	if !inScope(relative, ask.scope) {
		return nil, false, true
	}
	if r.isSensitive(relative) || (len(ask.fileTypes) > 0 && !wantedType(relative, ask.fileTypes)) {
		return nil, false, false
	}
	line, ok := positive(record["line"])
	if !ok {
		return nil, false, false
	}
	column, ok := positive(record["column"])
	if !ok {
		column = 1
	}
	out := map[string]any{"path": relative, "line": line, "column": column}
	return out, true, false
}

func validateScope(root string, scope []string, repositoryID string) error {
	for _, entry := range scope {
		if filepath.IsAbs(entry) {
			return contract.Fail(contract.FailurePermissionDenied,
				"scope leaves repository %s", repositoryID)
		}
		joined := filepath.Clean(filepath.Join(root, filepath.FromSlash(entry)))
		relative, err := filepath.Rel(root, joined)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return contract.Fail(contract.FailurePermissionDenied,
				"scope leaves repository %s", repositoryID)
		}
	}
	return nil
}

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

func wantedType(relative string, types []string) bool {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(relative)), ".")
	for _, want := range types {
		if strings.EqualFold(ext, strings.TrimPrefix(want, ".")) {
			return true
		}
	}
	return false
}

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

func prompt(req contract.RunRequest, ask search, sensitive []string) string {
	var b strings.Builder
	b.WriteString("Search this repository and report every match.\n\n")
	fmt.Fprintf(&b, "Capability: %s\nSummary: %s\n", req.Capability.ID, req.Capability.Summary)
	if req.Capability.Semantics != "" {
		fmt.Fprintf(&b, "Semantics: %s\n", oneLine(req.Capability.Semantics))
	}
	fmt.Fprintf(&b, "Query: %s\n", ask.query)
	fmt.Fprintf(&b, "Literal: %t\nCase sensitive: %t\nWhole word: %t\nRegex: %t\n",
		!ask.regex, ask.matchCase, ask.wholeWord, ask.regex)
	if len(ask.fileTypes) > 0 {
		fmt.Fprintf(&b, "File extensions: %s\n", strings.Join(ask.fileTypes, ", "))
	}
	if len(ask.scope) > 0 {
		fmt.Fprintf(&b, "Search only under: %s\n", strings.Join(ask.scope, ", "))
	}
	if len(sensitive) > 0 {
		b.WriteString("Never open or report files matching: ")
		b.WriteString(strings.Join(sensitive, ", "))
		b.WriteByte('\n')
	}
	b.WriteString("The repository is read-only. Do not edit files, use network commands, or inspect files one by one. First run one deterministic repository-wide `rg` search (use `-F` for literal queries, `-i` unless case-sensitive, `-w` for whole words, and `-g` for requested extensions/scope); pass the query after `--`. Then transform that command's results into JSON immediately. Do not answer from memory or skip the search. Return every match as JSON matching the provided schema. Use paths relative to the repository, 1-based line and column, and set snippet to an empty string: Atenea deliberately does not return file content from this provider. No matches is {\"matches\":[]}.\n")
	return b.String()
}

func oneLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func stringsAt(payload map[string]any, key string) []string {
	if direct, ok := payload[key].([]string); ok {
		return slices.Clone(direct)
	}
	raw, _ := payload[key].([]any)
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

var _ contract.Runner = (*Runner)(nil)
