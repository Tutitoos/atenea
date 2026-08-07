// Package local is the stand-in far side, for a machine where no client is
// installed.
//
// It sits exactly where the omp adapter sits: outside the core, behind
// contract.Runner, chosen by the settings file. The core still decides and
// delegates; this package is one of the things it can delegate to. It was
// written to let the skeleton beat before the first adapter existed, and it
// stays for the case that outlives that: contract.Runner has several possible
// far sides, and a machine with no omp on it still needs one.
//
// Because it stands in for a tool rather than for a brain, it holds no policy:
// it answers exactly one capability, refuses everything else into the common
// bins, and skips the files the security list marks as sensitive.
package local

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// CodeSearch is the one capability this stand-in knows how to run.
const CodeSearch = "code.search"

// defaultContextLines matches the capability's declared semantics: two lines
// above and two below unless the caller asks for more.
const defaultContextLines = 2

// maxFileSize keeps one pathological file from stalling a whole search. A
// source file above this is not source.
const maxFileSize = 1 << 20

// binarySniff is how much of a file is inspected for a NUL byte before
// deciding it is not text. Searching for text inside a binary finds noise.
const binarySniff = 8 << 10

// Options configure the stand-in. Everything here is declared in the settings
// file, so retuning it never means touching Go.
type Options struct {
	// Implementations this runner answers for, by implementation id. An
	// implementation outside the list is reported unavailable, which is the
	// bin that drives fallback.
	Implementations []string
	// Sensitive holds the path patterns that carry secrets. While exploring,
	// these are skipped in silence: stopping to ask would break the flow, and
	// noting the skip would cost more than ignoring it is worth.
	Sensitive []string
	// SkipDirs are directory names never descended into. Unlike a real search
	// tool this stand-in does not read .gitignore, so without this it would
	// spend most of its time inside .git.
	SkipDirs []string
}

// Runner searches a repository on the local filesystem.
type Runner struct {
	implementations []string
	sensitive       []string
	skipDirs        []string
}

// New validates the options and returns the stand-in.
func New(opts Options) (*Runner, error) {
	for _, pattern := range opts.Sensitive {
		if _, err := path.Match(pattern, "probe"); err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"local runner: sensitive pattern %q: %v", pattern, err)
		}
	}
	return &Runner{
		implementations: slices.Clone(opts.Implementations),
		sensitive:       slices.Clone(opts.Sensitive),
		skipDirs:        slices.Clone(opts.SkipDirs),
	}, nil
}

// ID names the runner on the status screen.
func (r *Runner) ID() string { return "local" }

// Serves reports whether the stand-in answers for that implementation.
func (r *Runner) Serves(implementationID string) bool {
	return slices.Contains(r.implementations, implementationID)
}

// Capabilities lists what this stand-in's Run can actually dispatch, so a
// settings file naming an implementation it has no case for is refused at
// load rather than at the call.
func (r *Runner) Capabilities() []string { return []string{CodeSearch} }

// Implementations lists what this runner answers for, sorted.
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

// Run executes one step.
//
// The version travels back on every path, including the failing ones: the
// stand-in is Atenea itself, so the answer is known before the walk starts and
// there is no reason a failed search should file its numbers anonymously.
func (r *Runner) Run(ctx context.Context, req contract.RunRequest) (out contract.Outcome, err error) {
	defer func() { out.ToolVersion = buildinfo.Version }()
	if err := req.Validate(); err != nil {
		return contract.Outcome{}, err
	}
	if !r.Serves(req.Implementation.ID) {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"local runner does not serve implementation %s", req.Implementation.ID)
	}
	if req.Capability.ID != CodeSearch {
		return contract.Outcome{}, contract.Fail(contract.FailureNotFound,
			"local runner has no implementation of %s", req.Capability.ID)
	}

	started := time.Now()
	matches, err := r.search(ctx, req)
	if err != nil {
		return contract.Outcome{}, err
	}

	result := map[string]any{"matches": matches}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return contract.Outcome{}, err
	}
	return contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		// Duration is a real measurement. Tokens are honestly zero: nothing
		// here talks to a model, so inventing a figure would poison the very
		// baseline the selector is meant to learn from. Memory is left
		// unmeasured for a different reason -- there is no child process,
		// this walk happens inside Atenea, and weighing the whole core would
		// answer a question nobody asked.
		Spent: contract.Sample{Duration: time.Since(started)},
	}, nil
}

// query is the payload after it has been read once, so the walk does not have
// to keep reaching back into a map[string]any.
type query struct {
	pattern      *regexp.Regexp
	scope        []string
	fileTypes    []string
	contextLines int
}

func (r *Runner) search(ctx context.Context, req contract.RunRequest) ([]any, error) {
	q, err := readQuery(req.Payload)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(req.Repository.Path)
	if err != nil {
		return nil, contract.Fail(contract.FailureNotFound,
			"repository %s: %v", req.Repository.ID, err)
	}
	roots, err := scopeRoots(root, q.scope, req.Repository.ID)
	if err != nil {
		return nil, err
	}

	matches := make([]any, 0, 16)
	for _, start := range roots {
		if err := ctx.Err(); err != nil {
			return nil, contract.Fail(contract.FailureTimeout,
				"search of %s stopped: %v", req.Repository.ID, err)
		}
		found, err := r.walk(ctx, root, start, q)
		if err != nil {
			return nil, err
		}
		matches = append(matches, found...)
	}
	return matches, nil
}

func (r *Runner) walk(ctx context.Context, root, start string, q query) ([]any, error) {
	// The root is checked before the walk begins, and only the root. WalkDir
	// hands a missing start to the callback below like any other unreadable
	// entry, where it would be skipped and the search would answer "nothing
	// found" for a repository it never opened. A zero that means "not
	// searched" is worse than an error: it reads as a fast, cheap, successful
	// call, and that is precisely what the measurement base would learn from.
	if _, err := os.Stat(start); err != nil {
		return nil, contract.Fail(contract.FailureNotFound, "cannot search %s: %v", start, err)
	}
	var matches []any
	err := filepath.WalkDir(start, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A path that cannot even be listed is skipped, never fatal: one
			// unreadable directory must not sink a search across a real
			// machine.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil //nolint:nilerr // an unreadable entry is skipped, not reported
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, inside := relativeTo(root, name)
		if !inside {
			return nil
		}

		if entry.IsDir() {
			if slices.Contains(r.skipDirs, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		// Sensitive files are skipped in silence, with nothing written down.
		if r.isSensitive(relative, entry.Name()) {
			return nil
		}
		if !wantedType(entry.Name(), q.fileTypes) {
			return nil
		}
		if tooBig(entry) {
			return nil
		}
		matches = append(matches, scanFile(name, relative, q)...)
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, contract.Fail(contract.FailureTimeout, "search stopped: %v", ctxErr)
		}
		return nil, contract.Fail(contract.FailureNotFound, "walking %s: %v", start, err)
	}
	return matches, nil
}

// isSensitive matches the patterns against both the bare file name and the
// repository-relative path, so `.env` catches a root file and `config/*.pem`
// catches a nested one.
func (r *Runner) isSensitive(relative, name string) bool {
	for _, pattern := range r.sensitive {
		if ok, _ := path.Match(pattern, name); ok {
			return true
		}
		if ok, _ := path.Match(pattern, relative); ok {
			return true
		}
	}
	return false
}

func wantedType(name string, types []string) bool {
	if len(types) == 0 {
		return true
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	return slices.Contains(types, ext)
}

// tooBig reports whether an entry should be passed over on size. An entry
// whose info cannot be read is passed over too: a file that vanished mid-walk
// has nothing left to contribute.
func tooBig(entry fs.DirEntry) bool {
	info, err := entry.Info()
	return err != nil || info.Size() > maxFileSize
}

// relativeTo reports the repository-relative, slash-separated path of an
// entry, or false when it does not sit under the root at all -- which only
// happens if the tree moved while the walk was in flight.
func relativeTo(root, name string) (string, bool) {
	relative, err := filepath.Rel(root, name)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

// scanFile returns every match in one file. A file that cannot be read is not
// an error a search can act on: from the caller's side it is indistinguishable
// from a file with no matches, and reporting it would abort a whole commission
// over one unreadable path.
func scanFile(name, relative string, q query) []any {
	raw, err := os.ReadFile(name)
	if err != nil {
		return nil
	}
	sniff := raw
	if len(sniff) > binarySniff {
		sniff = sniff[:binarySniff]
	}
	if bytes.IndexByte(sniff, 0) >= 0 {
		return nil
	}

	lines := strings.Split(string(raw), "\n")
	var matches []any
	for i, line := range lines {
		for _, at := range q.pattern.FindAllStringIndex(line, -1) {
			matches = append(matches, map[string]any{
				"path":    relative,
				"line":    i + 1,
				"column":  at[0] + 1,
				"snippet": snippet(lines, i, q.contextLines),
			})
		}
	}
	return matches
}

func snippet(lines []string, at, around int) string {
	from := max(at-around, 0)
	to := min(at+around+1, len(lines))
	return strings.Join(lines[from:to], "\n")
}

// scopeRoots turns the declared scope into absolute starting points, refusing
// anything that climbs out of the repository. Reading outside the unit of work
// is outside the commission, whatever the path says.
func scopeRoots(root string, scope []string, repositoryID string) ([]string, error) {
	if len(scope) == 0 {
		return []string{root}, nil
	}
	roots := make([]string, 0, len(scope))
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
		if _, err := os.Stat(joined); err != nil {
			return nil, contract.Fail(contract.FailureNotFound,
				"scope %q: %v", entry, err)
		}
		roots = append(roots, joined)
	}
	return roots, nil
}

func readQuery(payload map[string]any) (query, error) {
	text, _ := payload["query"].(string)
	if strings.TrimSpace(text) == "" {
		return query{}, contract.Fail(contract.FailureInvalidInput, "query is empty")
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
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return query{}, contract.Fail(contract.FailureInvalidInput, "query %q: %v", text, err)
	}

	q := query{pattern: compiled, contextLines: defaultContextLines}
	q.scope = stringsAt(payload, "scope")
	for _, kind := range stringsAt(payload, "file_types") {
		q.fileTypes = append(q.fileTypes, strings.TrimPrefix(strings.ToLower(kind), "."))
	}
	if lines, ok := intAt(payload, "context_lines"); ok {
		if lines < 0 {
			return query{}, contract.Fail(contract.FailureInvalidInput,
				"context_lines must not be negative, got %d", lines)
		}
		q.contextLines = lines
	}
	return q, nil
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
