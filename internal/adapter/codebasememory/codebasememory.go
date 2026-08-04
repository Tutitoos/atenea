// Package codebasememory is the fourth adapter: the far side of
// contract.Runner, backed by the codebase-memory-mcp CLI installed on this
// machine.
//
// Unlike omp, which drives a tool shaped for a human and has to parse printed
// text, and unlike Serena, which drives an MCP server over HTTP, this adapter
// drives a second CLI that already speaks JSON on both sides: a request goes
// in on stdin, an answer comes back on stdout, one process per call, nothing
// kept running between them. That is also why this is the first candidate to
// run under Atenea's own supervisor and the reason it does not today: the CLI
// needs no long-lived process, so there is nothing here for a supervisor to
// keep alive.
//
// It answers two capabilities neither omp nor Serena can: symbol.calls walks
// the call graph codebase-memory already built from the repository; code.
// impact asks that same graph what a git diff reaches. Both need a call
// graph, which is the one thing neither a grep nor a language server keeps.
// code.search already has three cheaper or equally-capable providers, so this
// adapter does not claim it -- a fourth identical answer would only give the
// funnel one more thing to rank.
//
// codebase-memory-mcp answers in JSON on stdout when a call succeeds, and in
// JSON on stderr -- one object with an "error" field -- when it does not.
// Absorbing exactly that shape, and nothing about the graph itself, is the
// whole of what this package knows: there is no policy here, and there is no
// second brain.
package codebasememory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/internal/procstat"
	"github.com/Tutitoos/atenea/internal/toolversion"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The capabilities this adapter answers, and the implementation ids the
// shipped catalog gives them. Ids are separate from capability ids because a
// capability is the "what" and an implementation is the "who": another
// provider may answer either capability tomorrow.
const (
	CapabilitySymbolCalls = "symbol.calls"
	CapabilityCodeImpact  = "code.impact"

	ImplCalls  = "codebase-memory.calls"
	ImplImpact = "codebase-memory.impact"
)

// DefaultBinary is the command looked up on PATH when the settings name none.
const DefaultBinary = "codebase-memory-mcp"

// DefaultTimeout caps one codebase-memory-mcp invocation. It sits above
// omp's: this opens a graph database file and walks edges in it, which a
// plain grep never has to do. It sits at Serena's own ceiling for the same
// reason theirs does -- a cold cache is slow long before it is stuck.
const DefaultTimeout = 90 * time.Second

// defaultDepth bounds a walk neither the capability nor the caller sized.
// Small on purpose, for the reason both capabilities' own semantics give: a
// workspace of many repositories turns an unbounded walk into hundreds of
// hits before anyone asked for that many.
const defaultDepth = 2

// defaultSnippetLines matches the "small by default" every sibling capability
// in this family promises, and the exact number Serena already uses for it.
const defaultSnippetLines = 5

// maxImpactSeeds caps how many directly changed symbols code.impact will walk
// callers from. Each seed is its own trace_path process; a change that
// touches this many symbols already answers "reaches nearly everything" on
// its own, and walking every last one of them would only spend more calls to
// say so louder.
const maxImpactSeeds = 50

// DefaultImplementations is what the adapter answers for. It is a function
// and not a package-level slice because a caller that appended to a shared
// one would quietly change what every other Atenea in this process serves.
func DefaultImplementations() []string {
	return []string{ImplCalls, ImplImpact}
}

// Options configure the adapter. Everything here is declared in the settings
// file, so retuning it never means touching Go.
type Options struct {
	// Binary is the codebase-memory-mcp executable. A bare name is looked up
	// on PATH; a path is used as given.
	Binary string
	// Implementations this adapter answers for, by implementation id.
	Implementations []string
	// Sensitive holds the path patterns that carry secrets. This adapter
	// opens a file to resolve the identifier at a position and, when asked,
	// to read a snippet -- the list is not advisory here, it is the
	// difference between reading a name and reading a key.
	Sensitive []string
	// Timeout caps one codebase-memory-mcp or git invocation. Zero takes
	// DefaultTimeout.
	Timeout time.Duration
}

// Runner answers capabilities by driving the codebase-memory-mcp CLI.
type Runner struct {
	binary          string
	implementations []string
	sensitive       []string
	timeout         time.Duration
	// version asks the binary who it is, once, so measurements are filed
	// under the build that actually produced them rather than the one
	// somebody wrote in the settings file months ago.
	version *toolversion.Probe
}

// meter accumulates what the child processes of one request cost. A single
// call to Run can shell out several times -- codebase-memory-mcp more than
// once, git once for code.impact -- and each is its own process. The memory
// figure that means anything for the request as a whole is the largest of
// them: two 40 MB processes one after the other did not need 80 MB, they
// needed 40 twice.
type meter struct{ peak int64 }

func (m *meter) saw(state *os.ProcessState) {
	if peak := procstat.PeakRSS(state); peak > m.peak {
		m.peak = peak
	}
}

// New validates the options and returns the adapter.
//
// A missing binary is deliberately not an error here, the same reasoning as
// every other adapter: a client that is not installed is a provider that is
// unreachable, which is what the unavailable bin and the fallback it drives
// are for; refusing to build would take the whole core down over one absent
// CLI.
func New(opts Options) (*Runner, error) {
	for _, pattern := range opts.Sensitive {
		if _, err := path.Match(pattern, "probe"); err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"codebase-memory adapter: sensitive pattern %q: %v", pattern, err)
		}
	}
	if opts.Timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"codebase-memory adapter: timeout must not be negative, got %s", opts.Timeout)
	}
	runner := &Runner{
		binary:          strings.TrimSpace(opts.Binary),
		implementations: slices.Clone(opts.Implementations),
		sensitive:       slices.Clone(opts.Sensitive),
		timeout:         opts.Timeout,
	}
	if runner.binary == "" {
		runner.binary = DefaultBinary
	}
	if runner.timeout == 0 {
		runner.timeout = DefaultTimeout
	}
	runner.version = toolversion.New(runner.binary, "--version")
	return runner, nil
}

// ID names the runner on the status screen, so it says who is actually
// behind the catalog.
func (r *Runner) ID() string { return "codebase-memory" }

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

// Sensitive lists the configured secret-carrying patterns, sorted.
func (r *Runner) Sensitive() []string {
	out := slices.Clone(r.sensitive)
	slices.Sort(out)
	return out
}

// Run executes one step by handing it to codebase-memory-mcp (and, for
// code.impact, to git) and reading the answer back.
//
// The version travels back on every path, including the failing ones. Which
// build of a tool produced a number is half the number's meaning, and the
// case worth catching -- an upgrade that started failing -- is exactly the
// one where the call did not return an outcome to carry it.
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
			"codebase-memory adapter does not serve implementation %s", req.Implementation.ID)
	}
	root, err := filepath.Abs(req.Repository.Path)
	if err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %q: %v", req.Repository.ID, req.Repository.Path, err)
	}
	switch req.Capability.ID {
	case CapabilitySymbolCalls:
		return r.runSymbolCalls(ctx, req, root)
	case CapabilityCodeImpact:
		return r.runCodeImpact(ctx, req, root)
	default:
		return contract.Outcome{}, contract.Fail(contract.FailureNotFound,
			"codebase-memory adapter has no implementation of %s", req.Capability.ID)
	}
}

// invoke runs one codebase-memory-mcp CLI tool and hands back its raw JSON
// answer, sorting every way the process itself can fail into a bin.
//
// The request travels on stdin as JSON, not as a positional argument: the CLI
// itself deprecates the latter and warns about it on stderr, which is the
// same stream failureFor reads the real error from. Piping it keeps that
// stream clean for parsing.
func (r *Runner) invoke(ctx context.Context, tool string, args map[string]any, weight *meter) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	payload, err := json.Marshal(args)
	if err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"codebase-memory: could not encode arguments for %s: %v", tool, err)
	}
	cmd := exec.CommandContext(ctx, r.binary, "cli", tool)
	cmd.Stdin = bytes.NewReader(payload)
	// codebase-memory-mcp shells out to nothing of its own, but the same rule
	// applies here as anywhere a process is spawned under a context: kill the
	// tree on cancellation and do not sit on the pipes waiting for it.
	procgroup.Contain(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	weight.saw(cmd.ProcessState)
	switch {
	case err == nil:
		return stdout.Bytes(), nil
	case ctx.Err() != nil:
		return nil, contract.Stopped(ctx.Err(), "codebase-memory", r.timeout).WithRaw(stderr.String())
	case errors.Is(err, exec.ErrNotFound):
		return nil, contract.Fail(contract.FailureUnavailable,
			"codebase-memory-mcp is not installed: %q is not on PATH", r.binary).WithRaw(err.Error())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, failureFor(tool, stderr.Bytes())
	}
	return nil, contract.Fail(contract.FailureUnavailable,
		"codebase-memory-mcp could not be started: %v", err).WithRaw(stderr.String())
}

// git runs one git command scoped to root, sorting the way it can fail into
// the same bins as codebase-memory-mcp's own failures.
func (r *Runner) git(ctx context.Context, root string, args []string, weight *meter) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	procgroup.Contain(cmd)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	weight.saw(cmd.ProcessState)
	switch {
	case err == nil:
		return stdout.String(), nil
	case ctx.Err() != nil:
		return "", contract.Stopped(ctx.Err(), "git", r.timeout).WithRaw(stderr.String())
	case errors.Is(err, exec.ErrNotFound):
		return "", contract.Fail(contract.FailureUnavailable, "git is not installed").WithRaw(err.Error())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "", gitFailureFor(stderr.String())
	}
	return "", contract.Fail(contract.FailureUnavailable,
		"git could not be started: %v", err).WithRaw(stderr.String())
}

// cliError is the one shape codebase-memory-mcp's own errors take: a JSON
// object on stderr, behind whatever log lines its own startup already wrote
// there.
type cliError struct {
	Error string `json:"error"`
	Hint  string `json:"hint"`
}

// failureFor sorts one of codebase-memory-mcp's own errors into a shared
// bin.
//
// This function is the whole of what Atenea knows about how codebase-
// memory-mcp fails, and the core knows none of it. There is no attempt to
// cover message by message: every one of them lands in one of the few bins,
// and the untranslated text rides along for whoever debugs later.
func failureFor(tool string, stderr []byte) error {
	raw := strings.TrimSpace(string(stderr))
	message := raw
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var parsed cliError
		if err := json.Unmarshal([]byte(line), &parsed); err == nil && parsed.Error != "" {
			message = parsed.Error
			if parsed.Hint != "" {
				message += ": " + parsed.Hint
			}
		}
		break
	}
	if message == "" {
		message = "codebase-memory-mcp failed without saying why"
	}
	lower := strings.ToLower(message)
	kind := contract.FailureUnavailable
	switch {
	case strings.Contains(lower, "not found") && strings.Contains(lower, "project"):
		// The repository itself has no ready index yet -- a precondition
		// unmet, not a bad request, and exactly the bin that drives
		// fallback to a different implementation.
		kind = contract.FailureUnavailable
	case strings.Contains(lower, "not found"):
		kind = contract.FailureNotFound
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "eacces"):
		kind = contract.FailurePermissionDenied
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"):
		kind = contract.FailureTimeout
	case strings.Contains(lower, "invalid"), strings.Contains(lower, "required"), strings.Contains(lower, "must be"):
		kind = contract.FailureInvalidInput
	}
	return contract.Fail(kind, "codebase-memory %s: %s", tool, message).WithRaw(raw)
}

// gitFailureFor sorts one of git's own stderr messages into a shared bin. Git
// prefixes every fatal error with "fatal:", so unlike codebase-memory-mcp
// there is no JSON to find first -- the raw line is all there is.
func gitFailureFor(stderr string) error {
	message := strings.TrimSpace(stderr)
	if message == "" {
		message = "git failed without saying why"
	}
	lower := strings.ToLower(message)
	kind := contract.FailureUnavailable
	switch {
	case strings.Contains(lower, "unknown revision"),
		strings.Contains(lower, "bad revision"),
		strings.Contains(lower, "ambiguous argument"),
		strings.Contains(lower, "not a git repository"):
		kind = contract.FailureInvalidInput
	}
	return contract.Fail(kind, "git: %s", message).WithRaw(stderr)
}

// isSensitive matches the pattern list against both the bare file name and
// the repository-relative path, so ".env" catches a root file and
// "config/*.pem" catches a nested one.
func (r *Runner) isSensitive(relative string) bool {
	name := path.Base(filepath.ToSlash(relative))
	slash := filepath.ToSlash(relative)
	for _, pattern := range r.sensitive {
		if ok, _ := path.Match(pattern, name); ok {
			return true
		}
		if ok, _ := path.Match(pattern, slash); ok {
			return true
		}
	}
	return false
}

// --- payload readers -------------------------------------------------------
//
// A capability's payload arrives as map[string]any, decoded from whatever the
// caller sent; a JSON number always lands as float64 and a JSON array always
// lands as []any, so every reader has to accept the decoded shape rather than
// the Go type the field will end up as.

func stringAt(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func boolAt(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

func stringsAt(payload map[string]any, key string) []string {
	raw, ok := payload[key].([]any)
	if !ok {
		if direct, isSlice := payload[key].([]string); isSlice {
			return slices.Clone(direct)
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, isText := item.(string); isText {
			out = append(out, text)
		}
	}
	return out
}

func intAt(payload map[string]any, key string) (int, bool) {
	switch value := payload[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

// --- graph plumbing ----------------------------------------------------
//
// query_graph is the one primitive shared by symbol.calls and code.impact:
// both eventually need to turn a set of qualified names into the file and
// line codebase-memory recorded for them, and both do it in one round trip
// for the whole set rather than one per name.

type queryGraphResponse struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Total   int      `json:"total"`
}

// graphLocation is where the graph says a symbol lives.
type graphLocation struct {
	FilePath  string
	StartLine int
}

// locate resolves a batch of qualified names to their file and starting
// line in one round trip. Names the graph does not recognize -- a call into
// another repository, or into code nothing indexed -- are simply absent from
// the result rather than an error: a partial graph is not a broken one.
func (r *Runner) locate(ctx context.Context, root string, qualifiedNames []string, weight *meter) (map[string]graphLocation, error) {
	if len(qualifiedNames) == 0 {
		return map[string]graphLocation{}, nil
	}
	quoted := make([]string, len(qualifiedNames))
	for i, qn := range qualifiedNames {
		quoted[i] = cypherString(qn)
	}
	query := fmt.Sprintf(
		"MATCH (n) WHERE n.qualified_name IN [%s] AND n.start_line IS NOT NULL "+
			"RETURN n.qualified_name AS qn, n.file_path AS file_path, n.start_line AS start_line",
		strings.Join(quoted, ", "))
	raw, err := r.invoke(ctx, "query_graph", map[string]any{"project": root, "query": query}, weight)
	if err != nil {
		return nil, err
	}
	var resp queryGraphResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"codebase-memory: could not read query_graph's answer: %v", err).WithRaw(string(raw))
	}
	out := make(map[string]graphLocation, len(resp.Rows))
	for _, row := range resp.Rows {
		if len(row) < 3 {
			continue
		}
		qn, _ := row[0].(string)
		file, _ := row[1].(string)
		line := lineNumber(row[2])
		if qn == "" || file == "" || line <= 0 {
			continue
		}
		out[qn] = graphLocation{FilePath: file, StartLine: line}
	}
	return out, nil
}

// cypherString quotes a value for inline use in a Cypher query. The values
// this adapter inlines come from codebase-memory-mcp's own prior answers or
// from a git diff's own file list, never from the request payload directly,
// but every one of them is escaped anyway: a name is not a query, whatever
// produced it.
func cypherString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
}

// lineNumber reads a line number back out of a query_graph row. The CLI
// encodes every column as JSON, and a number that started life as an integer
// property comes back as a quoted string rather than a bare one -- measured,
// not assumed -- so both shapes are accepted.
func lineNumber(v any) int {
	switch value := v.(type) {
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0
		}
		return n
	case float64:
		return int(value)
	default:
		return 0
	}
}

// inScope reports whether a repository-relative path falls inside any of the
// declared scope paths. Empty scope means everywhere, per every capability in
// this family.
func inScope(relPath string, scope []string) bool {
	if len(scope) == 0 {
		return true
	}
	relPath = filepath.ToSlash(relPath)
	for _, s := range scope {
		s = strings.TrimSuffix(filepath.ToSlash(s), "/")
		if s == "" || relPath == s || strings.HasPrefix(relPath, s+"/") {
			return true
		}
	}
	return false
}
