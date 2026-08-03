// Package omp is the first real client adapter: the far side of
// contract.Runner, backed by the omp CLI already installed on this machine.
//
// It is deliberately thin. Every decision -- which implementation answers a
// capability, whether the commission covers an effect, what to do when a
// provider is down -- was taken in the core before a request ever arrives
// here. This package speaks two languages and does nothing but translate
// between them: Atenea's capability payload on one side, omp's command line on
// the other. There is no policy in this file, and there is no second brain.
//
// Translating back is the harder half, because omp's search prints for a
// human. It has no machine format, it never reports a column, and a pattern it
// cannot compile comes back as a clean zero rather than as an error. Absorbing
// exactly that is why adapters exist: those quirks are sorted into the shared
// failure bins here and nowhere else, so the core never sees a line of omp's
// output.
package omp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/internal/procstat"
	"github.com/Tutitoos/atenea/internal/toolversion"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// CodeSearch is the one capability this adapter is wired to.
const CodeSearch = "code.search"

// DefaultBinary is the command looked up on PATH when the settings name none.
const DefaultBinary = "omp"

// defaultContextLines matches the capability's declared semantics: two lines
// above and two below unless the caller asks for more.
const defaultContextLines = 2

// DefaultMatchLimit is how many matches one search asks omp for.
//
// omp always caps, and its zero does not mean "no limit": passing -l 0 falls
// back to a small internal default and then reports the short answer as if it
// were complete. So the adapter always states a number out loud, and says so
// when that number is reached.
const DefaultMatchLimit = 10000

// DefaultTimeout caps one omp invocation. A search running longer than this is
// not slow, it is stuck, and the timeout bin is what lets the core fall back
// instead of waiting.
const DefaultTimeout = 30 * time.Second

// Options configure the adapter. Everything here is declared in the settings
// file, so retuning it never means touching Go.
type Options struct {
	// Binary is the omp executable. A bare name is looked up on PATH; a path
	// is used as given, which is what lets an unusual install work without a
	// rebuild.
	Binary string
	// Implementations this adapter answers for, by implementation id. An
	// implementation outside the list is reported unavailable, which is the
	// bin that drives fallback.
	Implementations []string
	// Sensitive holds the path patterns that carry secrets. A match inside one
	// never leaves this package: as while exploring, they are dropped in
	// silence, because stopping to ask would break the flow and noting the
	// skip would cost more than ignoring it is worth.
	Sensitive []string
	// MatchLimit caps how many matches one search asks for. Zero takes
	// DefaultMatchLimit.
	MatchLimit int
	// Timeout caps one invocation. Zero takes DefaultTimeout.
	Timeout time.Duration
}

// Runner answers capabilities by driving the omp CLI.
type Runner struct {
	binary          string
	implementations []string
	sensitive       []string
	matchLimit      int
	timeout         time.Duration
	// version asks the binary who it is, once, so measurements are filed
	// under the omp that actually produced them rather than the one somebody
	// wrote in the settings file months ago.
	version *toolversion.Probe
}

// meter accumulates what the child processes of one request cost.
//
// A single search can walk several roots, and each root is its own process. The
// memory figure that means anything for the request as a whole is the largest
// of them: two 40 MB processes one after the other did not need 80 MB, they
// needed 40 twice.
type meter struct{ peak int64 }

func (m *meter) saw(state *os.ProcessState) {
	if peak := procstat.PeakRSS(state); peak > m.peak {
		m.peak = peak
	}
}

// New validates the options and returns the adapter.
//
// A missing omp binary is deliberately not an error here. A client that is not
// installed is a provider that is unreachable, which is what the unavailable
// bin and the fallback it drives are for; refusing to build would take the
// whole core down over one absent CLI.
func New(opts Options) (*Runner, error) {
	for _, pattern := range opts.Sensitive {
		if _, err := path.Match(pattern, "probe"); err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"omp adapter: sensitive pattern %q: %v", pattern, err)
		}
	}
	if opts.MatchLimit < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"omp adapter: match limit must not be negative, got %d", opts.MatchLimit)
	}
	if opts.Timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"omp adapter: timeout must not be negative, got %s", opts.Timeout)
	}
	runner := &Runner{
		binary:          strings.TrimSpace(opts.Binary),
		implementations: slices.Clone(opts.Implementations),
		sensitive:       slices.Clone(opts.Sensitive),
		matchLimit:      opts.MatchLimit,
		timeout:         opts.Timeout,
	}
	if runner.binary == "" {
		runner.binary = DefaultBinary
	}
	if runner.matchLimit == 0 {
		runner.matchLimit = DefaultMatchLimit
	}
	if runner.timeout == 0 {
		runner.timeout = DefaultTimeout
	}
	runner.version = toolversion.New(runner.binary, "--version")
	return runner, nil
}

// ID names the runner on the status screen, so it says who is actually behind
// the catalog.
func (r *Runner) ID() string { return "omp" }

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

// Run executes one step by handing it to omp and reading the answer back.
//
// The version travels back on every path, including the failing ones. Which
// build of a tool produced a number is half the number's meaning, and the case
// worth catching -- an upgrade that started failing -- is exactly the one where
// the call did not return an outcome to carry it.
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
			"omp adapter does not serve implementation %s", req.Implementation.ID)
	}
	if req.Capability.ID != CodeSearch {
		return contract.Outcome{}, contract.Fail(contract.FailureNotFound,
			"omp adapter has no implementation of %s", req.Capability.ID)
	}

	started := time.Now()
	search, err := readSearch(req.Payload, r.matchLimit)
	if err != nil {
		return contract.Outcome{}, err
	}
	root, err := filepath.Abs(req.Repository.Path)
	if err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %q: %v", req.Repository.ID, req.Repository.Path, err)
	}
	roots, err := targets(root, search.scope, req.Repository.ID)
	if err != nil {
		return contract.Outcome{}, err
	}

	var matches []any
	truncated := false
	weight := &meter{}
	for _, target := range roots {
		found, cut, runErr := r.searchOne(ctx, root, target, search, weight)
		if runErr != nil {
			return contract.Outcome{}, runErr
		}
		matches = append(matches, found...)
		truncated = truncated || cut
	}
	if matches == nil {
		matches = []any{}
	}

	result := map[string]any{"matches": matches}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return contract.Outcome{}, err
	}
	outcome := contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		// Duration and memory are real measurements. Tokens are honestly
		// zero: omp's search is a tool call, not a model turn, so inventing a
		// figure would poison the very baseline the selector is meant to
		// learn from.
		Spent: contract.Sample{
			Duration: time.Since(started),
			PeakRSS:  weight.peak,
		},
	}
	if truncated {
		// A partial answer that does not say so is a wrong answer. The core
		// has no field for truncation, but a discovery is exactly the channel
		// for something learned mid-run that the next task should not have to
		// pay to learn again.
		outcome.Discoveries = append(outcome.Discoveries, contract.Discovery{
			Level: contract.ContextRepository,
			Note: fmt.Sprintf("search of %s stopped at the %d match ceiling: the answer is partial",
				req.Repository.ID, search.limit),
		})
	}
	return outcome, nil
}

// search is one payload read once: what omp is about to be told, and what the
// adapter has to keep in order to read the answer back.
type search struct {
	// pattern is the query with every declared intent folded into it. omp's
	// search takes a regex and nothing else -- no case flag, no word flag, no
	// literal mode -- so intent the far side cannot be told any other way has
	// to be written into the only field it does read.
	pattern string
	// matcher is that same expression compiled here, for two jobs. It refuses
	// a broken query before omp can answer one with a clean zero, and it finds
	// the offset again inside the line omp returns, because omp reports which
	// line matched but never where in the line.
	matcher      *regexp.Regexp
	scope        []string
	glob         string
	contextLines int
	limit        int
}

// readSearch translates one capability payload into the search omp will run.
func readSearch(payload map[string]any, limit int) (search, error) {
	text, _ := payload["query"].(string)
	if strings.TrimSpace(text) == "" {
		return search{}, contract.Fail(contract.FailureInvalidInput, "query is empty")
	}

	pattern := text
	if !boolAt(payload, "regex") {
		pattern = regexp.QuoteMeta(text)
	}
	if boolAt(payload, "whole_word") {
		pattern = `\b(?:` + pattern + `)\b`
	}
	// match_case is declared as the INTENT to distinguish upper from lower
	// case, so its absence means the caller does not care and the search is
	// case-insensitive.
	if !boolAt(payload, "match_case") {
		pattern = `(?i)` + pattern
	}
	// Compiling here is not a duplicate of what omp does: it is the only place
	// the query is ever checked. omp answers a pattern it cannot parse with
	// "0 matches" and a zero exit, which is indistinguishable from a search
	// that honestly found nothing, so a typo would come back as a fact.
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return search{}, contract.Fail(contract.FailureInvalidInput, "query %q: %v", text, err)
	}

	found := search{
		pattern:      pattern,
		matcher:      matcher,
		scope:        stringsAt(payload, "scope"),
		contextLines: defaultContextLines,
		limit:        limit,
	}
	types := make([]string, 0, 4)
	for _, kind := range stringsAt(payload, "file_types") {
		kind = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(kind)), ".")
		if kind != "" {
			types = append(types, kind)
		}
	}
	found.glob = globFor(types)
	if lines, ok := intAt(payload, "context_lines"); ok {
		if lines < 0 {
			return search{}, contract.Fail(contract.FailureInvalidInput,
				"context_lines must not be negative, got %d", lines)
		}
		found.contextLines = lines
	}
	return found, nil
}

// globFor turns the declared file types into the single glob omp understands.
//
// Repeating -g does not widen the filter, it narrows it to nothing: two -g
// flags come back with zero matches. A brace is the only shape that means "or"
// to that flag.
func globFor(types []string) string {
	switch len(types) {
	case 0:
		return ""
	case 1:
		return "*." + types[0]
	default:
		return "*.{" + strings.Join(types, ",") + "}"
	}
}

// args is the command line for one target. Flags come first and the two
// positionals last, behind the -- that stops a query beginning with a dash
// from being read as a flag.
func (s search) args(target string) []string {
	args := []string{
		"grep",
		"-C", strconv.Itoa(s.contextLines),
		"-l", strconv.Itoa(s.limit),
	}
	if s.glob != "" {
		args = append(args, "-g", s.glob)
	}
	return append(args, "--", s.pattern, target)
}

// targets turns the declared scope into the paths omp is pointed at, refusing
// anything that climbs out of the repository. Reading outside the unit of work
// is outside the commission, whatever the path says.
//
// Whether a surviving path exists is not checked here: omp answers that
// itself, and a second opinion would only be one more thing to keep in step.
func targets(root string, scope []string, repositoryID string) ([]string, error) {
	if len(scope) == 0 {
		return []string{root}, nil
	}
	out := make([]string, 0, len(scope))
	for _, entry := range scope {
		joined := filepath.Clean(filepath.Join(root, entry))
		if filepath.IsAbs(entry) {
			joined = filepath.Clean(entry)
		}
		relative, err := filepath.Rel(root, joined)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, contract.Fail(contract.FailurePermissionDenied,
				"scope %q leaves repository %s", entry, repositoryID)
		}
		out = append(out, joined)
	}
	return out, nil
}

// searchOne runs omp once and turns what came back into records shaped like
// the capability's declared output.
func (r *Runner) searchOne(ctx context.Context, root, target string, s search, weight *meter) ([]any, bool, error) {
	stdout, err := r.invoke(ctx, root, s.args(target), weight)
	if err != nil {
		return nil, false, err
	}
	report, err := parse(stdout)
	if err != nil {
		return nil, false, err
	}
	out := make([]any, 0, len(report.hits))
	for i, hit := range report.hits {
		if !hit.match {
			continue
		}
		relative, ok := relativeTo(root, target, hit.path)
		if !ok {
			// A path that does not sit under the repository cannot be part of
			// this commission's answer, whatever produced it.
			continue
		}
		if r.isSensitive(relative, filepath.Base(relative)) {
			continue
		}
		out = append(out, map[string]any{
			"path":    relative,
			"line":    hit.line,
			"column":  s.column(hit.text),
			"snippet": report.hits.snippet(i, s.contextLines),
		})
	}
	return out, report.truncated, nil
}

// invoke runs one omp command and hands back its stdout, sorting every way the
// process itself can fail into a bin.
//
// The child is weighed whichever way it ends. A search that timed out or blew
// up still spent the memory it spent, and a baseline built only from the calls
// that worked would flatter every tool that fails expensively.
func (r *Runner) invoke(ctx context.Context, dir string, args []string, weight *meter) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Dir = dir
	// omp shells out in turn, so the same rule applies as anywhere else: kill
	// the tree, and do not sit on the pipes waiting for a survivor to close
	// the copy of stdout it inherited.
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
		return "", contract.Stopped(ctx.Err(), "omp", r.timeout).WithRaw(stderr.String())
	case errors.Is(err, exec.ErrNotFound):
		return "", contract.Fail(contract.FailureUnavailable,
			"omp is not installed: %q is not on PATH", r.binary).WithRaw(err.Error())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "", failureFor(stderr.String())
	}
	return "", contract.Fail(contract.FailureUnavailable,
		"omp could not be started: %v", err).WithRaw(stderr.String())
}

// errorPrefix is how omp announces a failure on stderr.
const errorPrefix = "Error:"

// failureFor sorts one of omp's own errors into a shared bin.
//
// This function is the whole of what Atenea knows about how omp fails, and the
// core knows none of it. There is no attempt to cover message by message:
// every one of them lands in one of the few bins, and the untranslated text
// rides along for whoever debugs later.
func failureFor(stderr string) error {
	raw := strings.TrimSpace(stderr)
	message := raw
	for _, line := range strings.Split(raw, "\n") {
		if after, found := strings.CutPrefix(strings.TrimSpace(line), errorPrefix); found {
			message = strings.TrimSpace(after)
			break
		}
	}
	if message == "" {
		message = "omp failed without saying why"
	}
	lower := strings.ToLower(message)
	kind := contract.FailureUnavailable
	switch {
	case strings.Contains(lower, "not found"), strings.Contains(lower, "no such file"):
		kind = contract.FailureNotFound
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "eacces"):
		kind = contract.FailurePermissionDenied
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"):
		kind = contract.FailureTimeout
	}
	return contract.Fail(kind, "omp: %s", message).WithRaw(raw)
}

// hit is one line omp printed: either a match or a line of context around one.
type hit struct {
	path  string
	line  int
	text  string
	match bool
}

// body is every line of one omp run, in the order it printed them.
type body []hit

// matched returns just the hits that are matches rather than context.
func (b body) matched() []hit {
	out := make([]hit, 0, len(b))
	for _, h := range b {
		if h.match {
			out = append(out, h)
		}
	}
	return out
}

// snippet rebuilds the fragment around one match from the context omp already
// printed, which is cheaper and more faithful than reading the file again.
//
// Neighbors are taken by line distance rather than by block, because omp
// merges two nearby matches into one run of lines: the window belongs to the
// match, not to the block it was printed in.
func (b body) snippet(at, window int) string {
	from := at
	for from > 0 && adjacent(b[from-1], b[at], window) {
		from--
	}
	to := at
	for to+1 < len(b) && adjacent(b[to+1], b[at], window) {
		to++
	}
	lines := make([]string, 0, to-from+1)
	for _, h := range b[from : to+1] {
		lines = append(lines, h.text)
	}
	return strings.Join(lines, "\n")
}

// adjacent reports whether one printed line belongs in another's window.
func adjacent(other, at hit, window int) bool {
	if other.path != at.path {
		return false
	}
	distance := other.line - at.line
	if distance < 0 {
		distance = -distance
	}
	return distance <= window
}

// report is one omp run read back.
type report struct {
	hits       body
	matches    int
	truncated  bool
	sawSummary bool
}

// bodyLine reads one rendered line. omp prints "path:12: text" for a match and
// "path-12- text" for context, which turns ambiguous the moment a path holds a
// colon or a dash followed by digits. The leftmost separator pair that agrees
// with itself and is followed by a space wins, so a path would have to contain
// a literal ":12: " to fool it.
var bodyLine = regexp.MustCompile(`^(.*?)([-:])(\d+)([-:]) (.*)$`)

const (
	totalPrefix   = "Total matches:"
	limitPrefix   = "Limit reached:"
	limitExceeded = "true"
)

// parse reads one omp run's stdout.
func parse(stdout string) (report, error) {
	var out report
	for _, line := range strings.Split(stdout, "\n") {
		switch {
		case strings.HasPrefix(line, errorPrefix):
			// omp normally reports failure on stderr with a non-zero exit, but
			// a zero exit carrying an error line is still a failure and must
			// never be read as an empty answer.
			return report{}, failureFor(line)
		case strings.HasPrefix(line, totalPrefix):
			count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, totalPrefix)))
			if err != nil {
				return report{}, contract.Fail(contract.FailureUnavailable,
					"omp reported an unreadable match count %q", line).WithRaw(stdout)
			}
			out.matches = count
			out.sawSummary = true
		case strings.HasPrefix(line, limitPrefix):
			out.truncated = strings.TrimSpace(strings.TrimPrefix(line, limitPrefix)) == limitExceeded
		default:
			if h, ok := readLine(line); ok {
				out.hits = append(out.hits, h)
			}
		}
	}
	// The summary is a free checksum on everything above, but only while the
	// answer is whole: once omp stops at the ceiling it keeps counting past
	// what it prints, so the two legitimately disagree. When they disagree for
	// any other reason this adapter is reading a format that is no longer the
	// one omp writes, and the only honest answer is that omp is unusable from
	// here. Falling back beats reporting a partial search as a complete one.
	if !out.truncated && out.sawSummary && out.matches != len(out.hits.matched()) {
		return report{}, contract.Fail(contract.FailureUnavailable,
			"omp reported %d matches and printed %d: its output is not the shape this adapter reads",
			out.matches, len(out.hits.matched())).WithRaw(stdout)
	}
	return out, nil
}

// readLine turns one printed line back into a hit.
func readLine(line string) (hit, bool) {
	fields := bodyLine.FindStringSubmatch(line)
	if fields == nil || fields[2] != fields[4] || fields[1] == "" {
		return hit{}, false
	}
	number, err := strconv.Atoi(fields[3])
	if err != nil || number <= 0 {
		return hit{}, false
	}
	return hit{path: fields[1], line: number, text: fields[5], match: fields[2] == ":"}, true
}

// column finds where in the line the match actually starts.
//
// omp reports the line and not the offset, and the capability requires one, so
// the adapter looks again with the very expression it sent. A line omp matched
// that this expression cannot find again means the two engines read the same
// pattern differently; the start of the line is the honest answer then,
// because the line itself is right either way.
func (s search) column(text string) int {
	if at := s.matcher.FindStringIndex(text); at != nil {
		return at[0] + 1
	}
	return 1
}

// relativeTo reports the repository-relative, slash-separated path of a hit.
//
// omp prints a path relative to whatever it was pointed at, so a scope below
// the root comes back shortened and has to be rejoined: the caller was told
// about a repository and has to be able to open what it is handed. Pointed at
// a single file it prints an absolute path instead, which is why both shapes
// are handled rather than assumed.
func relativeTo(root, target, name string) (string, bool) {
	if !filepath.IsAbs(name) {
		name = filepath.Join(target, name)
	}
	relative, err := filepath.Rel(root, filepath.Clean(name))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

// isSensitive matches the patterns against both the bare file name and the
// repository-relative path, so `.env` catches a root file and `config/*.pem`
// catches a nested one.
func (r *Runner) isSensitive(relative, name string) bool {
	for _, pattern := range r.sensitive {
		if ok, _ := path.Match(pattern, relative); ok {
			return true
		}
		if ok, _ := path.Match(pattern, name); ok {
			return true
		}
	}
	return false
}

// Sensitive lists the configured secret-carrying patterns, sorted.
func (r *Runner) Sensitive() []string {
	out := slices.Clone(r.sensitive)
	slices.Sort(out)
	return out
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
