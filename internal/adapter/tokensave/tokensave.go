// Package tokensave is the sixth adapter, and the second whose far side is a
// stdio MCP server Atenea itself spawns and holds the pipes of.
//
// It is the kivgraph arrangement with one difference that changes everything
// downstream: kivgraph publishes ONE global corpus addressed by repository
// name, while tokensave serves ONE project rooted at a directory on disk and
// speaks paths relative to that root. This workspace is 51 independent git
// repositories plus cli/ under one umbrella root, and Atenea also declares
// that root itself for workspace-wide code.context calls. The mapping is not
// optional: an individual repository is a path PREFIX inside tokensave's
// root, and every path crossing this package is translated in both directions
// (toRoot/toRepository). A capability's declared output is
// repository-relative; a tokensave answer never is.
//
// The consequence worth stating out loud: a caller who asks about a symbol in
// one repository can get calls that live in another, because the graph does
// not stop at a repository boundary. Those rows cannot be expressed in the
// declared output at all -- there is no field for "another repository" on
// symbol.calls -- so they are dropped and the drop is reported as a
// discovery. Silently renaming a foreign path into a repository-relative one
// would be a lie the caller cannot detect.
//
// # The empty-graph trap, again
//
// tokensave syncs its SQLite index incrementally on every call, so a server
// pointed at a directory it has not indexed answers successfully with nothing
// -- the same shape as kivgraph's empty snapshot. Measured against v7.9.0:
// tokensave_status carries node_count, edge_count and file_count, so every
// capability pays for one status call before any other answer is trusted
// (checkGraphReady), and a zero-count graph becomes
// contract.FailureUnavailable rather than a VerdictOK built on top of
// nothing. A tool that legitimately matches nothing is a different thing and
// stays VerdictOK with an empty list.
//
// # What this package does not do
//
// It does not index: tokensave keeps its own index in step by itself, so
// there is no repository indexing implementation here and nothing in this file
// ever writes. It answers the two capabilities its far side can answer
// honestly -- what a file declares, and where a symbol sits on the call graph
// -- and deliberately not symbol.definition: resolving the symbol under a
// position back to its declaration needs a type checker, tokensave resolves
// by NAME, and a name lookup answering a position question would be a
// different answer wearing the same name.
package tokensave

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The capabilities this adapter answers, and the implementation ids the
// catalog gives them.
const (
	CapabilityContext  = "code.context"
	CapabilityOverview = "symbol.overview"
	CapabilityCalls    = "symbol.calls"

	ImplContext  = "tokensave.context"
	ImplOverview = "tokensave.overview"
	ImplCalls    = "tokensave.calls"
)

// The MCP tool names on tokensave's own far side, measured against v7.9.0.
// find_exact_symbol answers no capability of its own: symbol.calls calls it
// to turn a resolved declaration into the node id callers/callees require.
const (
	toolStatus   = "tokensave_status"
	toolContext  = "tokensave_context"
	toolEntities = "tokensave_entities"
	toolExact    = "tokensave_find_exact_symbol"
	toolCallers  = "tokensave_callers"
	toolCallees  = "tokensave_callees"
)

// The directions symbol.calls declares. Both is not a third query: it is the
// other two, run and merged, which is why the direction travels back on every
// row rather than being remembered by the caller.
const (
	directionIncoming = "incoming"
	directionOutgoing = "outgoing"
	directionBoth     = "both"
)

// DefaultTimeout caps one call. tokensave re-syncs its index before
// answering, which is the same class of cost as opening a cold graph, so this
// sits at kivgraph's own ceiling rather than inventing a new number.
const DefaultTimeout = 90 * time.Second

// defaultDepth is how far symbol.calls walks when the caller does not say.
// One hop: the capability declares depth "small by default, on purpose", and
// on a graph spanning the whole workspace the second hop is where a readable
// answer turns into hundreds of rows nobody asked for.
const defaultDepth = 1

// defaultSnippetLines is the window read back around a call when a snippet is
// asked for. tokensave reports a line and never the text at it, so the line
// is read from disk here; one line means exactly the line of the call.
const defaultSnippetLines = 1

// maxSnippetBytes caps how much of a file this package will read to recover a
// snippet or a column. A generated bundle is not worth paging into memory to
// find one column, and a file this size has no readable snippet in it anyway.
const maxSnippetBytes = 4 << 20

// pseudoKinds are the rows tokensave_entities returns that are not
// declarations of the file's own symbols: the file node itself, its package
// clause and its imports. Measured against v7.9.0 on Go, TypeScript and
// Rust files. They are dropped rather than reported, because symbol.overview
// promises "every symbol the file declares" and an import is a symbol the
// file USES.
var pseudoKinds = map[string]bool{
	"file":       true,
	"use":        true,
	"go_package": true,
	"package":    true,
	"export":     true,
}

// nestedKinds are the rows that only exist inside another declaration, so
// they belong to symbol.overview's depth > 0 answer and not to its default
// one.
var nestedKinds = map[string]bool{
	"field":         true,
	"struct_tag":    true,
	"enum_variant":  true,
	"generic_param": true,
}

// entityKindFilters are the kinds accepted by the official tokensave_entities
// tool. A large unfiltered answer can be clipped by the MCP client before it
// reaches json.Unmarshal; asking one kind at a time keeps every response
// below that transport ceiling. The less common kinds are included because
// tokensave can serve polyglot projects, even though most Go files only use
// function, struct and const.
var entityKindFilters = []string{
	"function",
	"struct",
	"enum",
	"trait",
	"impl",
	"class",
	"method",
	"const",
	"type",
	"interface",
	"variable",
	"var",
	"field",
	"struct_tag",
	"enum_variant",
	"generic_param",
}

// DefaultImplementations is what the adapter answers for. It is a function
// and not a package-level slice because a caller that appended to a shared
// one would quietly change what every other Atenea in this process serves.
func DefaultImplementations() []string {
	return []string{ImplCalls, ImplContext, ImplOverview}
}

// Options configure the adapter.
type Options struct {
	// Root is the directory tokensave serves, absolute. Every repository
	// this adapter can answer for lives under it, and every path on the wire
	// is relative to it.
	Root string
	// Implementations the adapter answers for.
	Implementations []string
	// Sensitive holds the path patterns that carry secrets. A match never
	// leaves this package.
	Sensitive []string
	// Timeout caps one call.
	Timeout time.Duration
	// Session returns the live MCP session for the supervised tokensave
	// child. It is a function, not a stored value, because the process may
	// not exist yet when New runs (on_demand lifecycle) and may be replaced
	// by a restart later.
	Session func(ctx context.Context) (*mcpstdio.Session, error)
}

// Runner is the tokensave far side of contract.Runner.
type Runner struct {
	root            string
	implementations []string
	sensitive       []string
	timeout         time.Duration
	session         func(ctx context.Context) (*mcpstdio.Session, error)
}

// New validates the options and returns the adapter.
func New(opts Options) (*Runner, error) {
	if opts.Session == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"tokensave adapter: session is required -- a stdio server has no address to dial without one")
	}
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"tokensave adapter: root is required -- every path on this wire is relative to it")
	}
	if !filepath.IsAbs(root) {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"tokensave adapter: root %q must be absolute", root)
	}
	for _, pattern := range opts.Sensitive {
		if _, err := path.Match(pattern, "probe"); err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"tokensave adapter: sensitive pattern %q: %v", pattern, err)
		}
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"tokensave adapter: timeout must not be negative, got %s", timeout)
	}
	impls := slices.Clone(opts.Implementations)
	if impls == nil {
		impls = DefaultImplementations()
	}
	slices.Sort(impls)
	return &Runner{
		root:            filepath.Clean(root),
		implementations: impls,
		sensitive:       slices.Clone(opts.Sensitive),
		timeout:         timeout,
		session:         opts.Session,
	}, nil
}

// ID names the runner on the status screen.
func (r *Runner) ID() string { return "tokensave" }

// Serves reports whether this adapter answers for that implementation.
func (r *Runner) Serves(implementationID string) bool {
	return slices.Contains(r.implementations, implementationID)
}

// Implementations lists every implementation this runner declares itself the
// far side of.
func (r *Runner) Implementations() []string { return slices.Clone(r.implementations) }

// Capabilities lists what this adapter's Run can actually dispatch, so a
// settings file naming an implementation it has no case for is refused at
// load rather than at the call.
func (r *Runner) Capabilities() []string {
	return []string{CapabilityContext, CapabilityOverview, CapabilityCalls}
}

// Sensitive lists the configured secret-carrying patterns, sorted.
func (r *Runner) Sensitive() []string {
	out := slices.Clone(r.sensitive)
	slices.Sort(out)
	return out
}

// Run executes one step.
func (r *Runner) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	if err := req.Validate(); err != nil {
		return contract.Outcome{}, err
	}
	if !r.Serves(req.Implementation.ID) {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"tokensave adapter does not serve implementation %s", req.Implementation.ID)
	}

	started := time.Now()
	call, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// The repository has to sit inside the served root before a single tool
	// is called: a path outside it has no representation on this wire, and
	// discovering that after the graph answered would report another
	// project's symbols under this repository's name.
	prefix, err := r.prefixFor(req.Repository)
	if err != nil {
		return contract.Outcome{}, err
	}

	sess, err := r.session(call)
	if err != nil {
		return contract.Outcome{}, r.failureFor(err, call)
	}
	if err := sess.Initialize(call); err != nil {
		return contract.Outcome{}, r.failureFor(err, call)
	}
	if err := r.checkGraphReady(call, sess); err != nil {
		return contract.Outcome{}, err
	}

	var (
		result map[string]any
		notes  []string
	)
	switch req.Capability.ID {
	case CapabilityContext:
		result, notes, err = r.runContext(call, sess, prefix, req)
	case CapabilityOverview:
		result, notes, err = r.runOverview(call, sess, prefix, req)
	case CapabilityCalls:
		result, notes, err = r.runCalls(call, sess, prefix, req)
	default:
		return contract.Outcome{}, contract.Fail(contract.FailureNotFound,
			"tokensave adapter has no implementation of %s", req.Capability.ID)
	}
	if err != nil {
		return contract.Outcome{}, r.failureFor(err, call)
	}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return contract.Outcome{}, err
	}

	outcome := contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		// No memory figure and no token figure: the far side is a process the
		// supervisor owns rather than one this call spawned, and a graph
		// query is not a model turn. Inventing either would poison the
		// baseline the selector ranks on.
		Spent: contract.Sample{Duration: time.Since(started)},
	}
	for _, note := range notes {
		if note == "" {
			continue
		}
		outcome.Discoveries = append(outcome.Discoveries,
			contract.Discovery{Level: contract.ContextRepository, Note: note})
	}
	return outcome, nil
}

// prefixFor is the repository's path relative to the served root: the one
// value every translation in this package is built on. An empty prefix is
// legitimate and means the repository IS the root.
func (r *Runner) prefixFor(repo contract.Repository) (string, error) {
	abs, err := filepath.Abs(repo.Path)
	if err != nil {
		return "", contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %q: %v", repo.ID, repo.Path, err)
	}
	relative, err := filepath.Rel(r.root, filepath.Clean(abs))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", contract.Fail(contract.FailureUnavailable,
			"repository %s is outside tokensave's root %s: this server can answer nothing about it",
			repo.ID, r.root)
	}
	if relative == "." {
		return "", nil
	}
	return filepath.ToSlash(relative), nil
}

// toRoot turns a repository-relative path into the root-relative one
// tokensave reads, refusing anything that climbs out of the repository:
// reading outside the unit of work is outside the commission, whatever the
// path says.
func toRoot(prefix, file, repositoryID string) (string, error) {
	// Cleaned as given, never anchored at "/" first: anchoring would turn
	// "../other/secret.go" into "other/secret.go" and answer about a file
	// nobody asked for instead of refusing the question. The escape has to
	// survive Clean to be caught, so Clean runs on the relative path.
	clean := path.Clean(filepath.ToSlash(strings.TrimSpace(file)))
	if clean == "" || clean == "." {
		return "", contract.Fail(contract.FailureInvalidInput, "file is empty")
	}
	if path.IsAbs(clean) {
		return "", contract.Fail(contract.FailureInvalidInput,
			"file %q must be relative to repository %s", file, repositoryID)
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", contract.Fail(contract.FailurePermissionDenied,
			"file %q leaves repository %s", file, repositoryID)
	}
	if prefix == "" {
		return clean, nil
	}
	return prefix + "/" + clean, nil
}

// toRepository turns a root-relative path back into a repository-relative
// one. The second return is false for a path outside the repository, which is
// a real answer on a graph that does not stop at a repository boundary and
// has no field to travel back in.
func toRepository(prefix, file string) (string, bool) {
	clean := filepath.ToSlash(path.Clean(file))
	// An umbrella root has no prefix, and until this check existed that case
	// waved every path through: "../../etc/passwd" came back from the far side
	// as a legitimate repository-relative path, and runCalls joined it onto
	// the root and read it. Every other prefix rejects an escape by not
	// matching; the empty one has to reject it by saying so.
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", false
	}
	if prefix == "" {
		return clean, true
	}
	if clean == prefix {
		return "", false
	}
	if !strings.HasPrefix(clean, prefix+"/") {
		return "", false
	}
	return strings.TrimPrefix(clean, prefix+"/"), true
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

// statusAnswer is tokensave_status narrowed to the three counts that say
// whether there is a graph here at all.
type statusAnswer struct {
	Nodes int `json:"node_count"`
	Edges int `json:"edge_count"`
	Files int `json:"file_count"`
}

// metricsPrefix is the line tokensave appends to its own tool results, after
// the JSON and outside it: "tokensave_metrics: before=812 after=660 saved=152".
// Measured against v7.9.0 on every tool this adapter calls.
const metricsPrefix = "tokensave_metrics:"

// payloadOf is the JSON of one tool result, with that trailing report cut off.
//
// It is not decoration that can be ignored: json.Unmarshal on the whole text
// fails with "invalid character 't' after top-level value" and every capability
// comes back unavailable -- which is exactly how this was found, against the
// real server rather than against the fake. The saving it reports is a fact
// about the client's context window, not about the answer, so nothing here
// forwards it.
func payloadOf(text string) []byte {
	if at := strings.Index(text, metricsPrefix); at >= 0 {
		text = text[:at]
	}
	return []byte(strings.TrimSpace(text))
}

// checkGraphReady is the guard every capability shares. tokensave answers a
// directory it has never indexed successfully and with nothing, so the counts
// are what separates "no graph" from "no match".
func (r *Runner) checkGraphReady(ctx context.Context, sess *mcpstdio.Session) error {
	text, err := sess.Call(ctx, toolStatus, map[string]any{})
	if err != nil {
		return r.failureFor(err, ctx)
	}
	var status statusAnswer
	if err := json.Unmarshal(payloadOf(text), &status); err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"tokensave %s: unreadable answer", toolStatus).WithRaw(text)
	}
	if status.Nodes == 0 && status.Edges == 0 && status.Files == 0 {
		return contract.Fail(contract.FailureUnavailable,
			"tokensave has no graph for %s: it answers every query with nothing", r.root)
	}
	return nil
}

// entity is one row of tokensave_entities: a declaration with the span it
// covers. There is no column on this wire and no id either, which is why
// symbol.overview recovers the column from the file (see columnOf) and
// symbol.calls pays a second call to find_exact_symbol for an id.
type entity struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
}

// entitiesAnswer is tokensave_entities' envelope.
type entitiesAnswer struct {
	File    string   `json:"file"`
	Symbols []entity `json:"symbols"`
}

// fetchEntities lists what one file declares, in tokensave's own vocabulary.
func (r *Runner) fetchEntities(ctx context.Context, sess *mcpstdio.Session, file string) ([]entity, error) {
	text, err := sess.Call(ctx, toolEntities, map[string]any{"file": file})
	if err != nil {
		return nil, err
	}
	var answer entitiesAnswer
	if decodeErr := json.Unmarshal(payloadOf(text), &answer); decodeErr == nil {
		return answer.Symbols, nil
	}

	// The server's unfiltered response is useful for small files, but the
	// stdio client deliberately protects itself from an oversized frame. The
	// official kinds filter gives us a bounded, lossless fallback without
	// changing the server's global context settings.
	var (
		all       []entity
		lastErr   error
		succeeded bool
	)
	for _, kind := range entityKindFilters {
		part, callErr := sess.Call(ctx, toolEntities, map[string]any{
			"file":  file,
			"kinds": []string{kind},
		})
		if callErr != nil {
			lastErr = callErr
			continue
		}
		var filtered entitiesAnswer
		if decodeErr := json.Unmarshal(payloadOf(part), &filtered); decodeErr != nil {
			lastErr = decodeErr
			continue
		}
		succeeded = true
		all = append(all, filtered.Symbols...)
	}
	if !succeeded {
		if lastErr == nil {
			lastErr = errors.New("no filtered entity query succeeded")
		}
		return nil, fmt.Errorf("tokensave %s: unreadable unfiltered answer and kind fallback: %w", toolEntities, lastErr)
	}

	seen := make(map[string]struct{}, len(all))
	merged := all[:0]
	for _, item := range all {
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%d", item.Kind, item.Name, item.Line, item.EndLine)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, item)
	}
	slices.SortFunc(merged, func(a, b entity) int {
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		if a.EndLine != b.EndLine {
			return a.EndLine - b.EndLine
		}
		if n := strings.Compare(a.Kind, b.Kind); n != 0 {
			return n
		}
		return strings.Compare(a.Name, b.Name)
	})
	return merged, nil
}

// runOverview answers symbol.overview from tokensave_entities.
func (r *Runner) runOverview(ctx context.Context, sess *mcpstdio.Session, prefix string,
	req contract.RunRequest) (map[string]any, []string, error) {

	file, ok := req.Payload["file"].(string)
	if !ok || strings.TrimSpace(file) == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "file is empty")
	}
	relative := filepath.ToSlash(path.Clean(file))
	if r.isSensitive(relative) {
		return nil, nil, contract.Fail(contract.FailurePermissionDenied,
			"%s carries secrets: this adapter never reads it", relative)
	}
	target, err := toRoot(prefix, file, req.Repository.ID)
	if err != nil {
		return nil, nil, err
	}
	depth, err := intAt(req.Payload, "depth", 0)
	if err != nil {
		return nil, nil, err
	}
	if depth < 0 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"depth must not be negative, got %d", depth)
	}

	found, err := r.fetchEntities(ctx, sess, target)
	if err != nil {
		return nil, nil, err
	}
	// The column is recovered from the file, once, because no provider behind
	// this capability reports one and the declaration says so out loud. A
	// file that cannot be read still answers: the line is right either way,
	// and column 1 is the honest fallback. A file that leaves the root is a
	// different thing and refuses the answer -- reading it is outside the
	// commission whether or not a column comes back.
	resolved, err := within(r.root, target)
	if err != nil {
		return nil, nil, err
	}
	lines := readLines(resolved)

	tops := topLevel(found)
	symbols := make([]any, 0, len(found))
	for _, item := range found {
		if pseudoKinds[item.Kind] {
			continue
		}
		nested := nestedKinds[item.Kind]
		if nested && depth == 0 {
			continue
		}
		record := map[string]any{
			"name":   item.Name,
			"kind":   item.Kind,
			"line":   item.Line,
			"column": columnOf(lines, item.Line, item.Name),
		}
		// end_line only when it says something line does not.
		if item.EndLine > item.Line {
			record["end_line"] = item.EndLine
		}
		if nested {
			if parent, ok := enclosing(tops, item); ok {
				record["parent"] = parent
			}
		}
		symbols = append(symbols, record)
	}
	notes := []string{fmt.Sprintf("tokensave: %s declares %d symbol(s)", relative, len(symbols))}
	return map[string]any{"symbols": symbols}, notes, nil
}

// topLevel is every declaration that is not nested, which is what a nested
// one's parent can be. Sorted by span so the innermost enclosing declaration
// wins over an outer one that also contains the line.
func topLevel(found []entity) []entity {
	out := make([]entity, 0, len(found))
	for _, item := range found {
		if pseudoKinds[item.Kind] || nestedKinds[item.Kind] {
			continue
		}
		out = append(out, item)
	}
	slices.SortFunc(out, func(a, b entity) int { return span(a) - span(b) })
	return out
}

// enclosing names the declaration a nested row sits inside.
func enclosing(tops []entity, item entity) (string, bool) {
	for _, candidate := range tops {
		if candidate.Line <= item.Line && item.Line <= max(candidate.EndLine, candidate.Line) {
			return candidate.Name, true
		}
	}
	return "", false
}

func span(e entity) int {
	if e.EndLine <= e.Line {
		return 0
	}
	return e.EndLine - e.Line
}

// match is one row of tokensave_find_exact_symbol: the id every graph walk
// needs, keyed by a name and disambiguated here by file and line.
type match struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// matchesAnswer is find_exact_symbol's envelope.
type matchesAnswer struct {
	Matches []match `json:"matches"`
}

// call is one row of tokensave_callers / tokensave_callees.
type call struct {
	NodeID  string `json:"node_id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	DefLine int    `json:"def_line"`
	Depth   int    `json:"depth"`
}

// runCalls answers symbol.calls: a position resolved to a node, then one walk
// per direction asked for.
func (r *Runner) runCalls(ctx context.Context, sess *mcpstdio.Session, prefix string,
	req contract.RunRequest) (map[string]any, []string, error) {

	file, ok := req.Payload["file"].(string)
	if !ok || strings.TrimSpace(file) == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "file is empty")
	}
	line, err := intAt(req.Payload, "line", 0)
	if err != nil {
		return nil, nil, err
	}
	if line <= 0 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"line must be 1 or more, got %d", line)
	}
	direction, err := readDirection(req.Payload)
	if err != nil {
		return nil, nil, err
	}
	depth, err := intAt(req.Payload, "depth", defaultDepth)
	if err != nil {
		return nil, nil, err
	}
	if depth <= 0 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"depth must be 1 or more, got %d", depth)
	}
	snippetLines, err := intAt(req.Payload, "snippet_lines", defaultSnippetLines)
	if err != nil {
		return nil, nil, err
	}
	wantSnippet, _ := req.Payload["include_snippet"].(bool)
	hint, _ := req.Payload["name"].(string)

	target, err := toRoot(prefix, file, req.Repository.ID)
	if err != nil {
		return nil, nil, err
	}
	node, err := r.resolve(ctx, sess, target, line, hint)
	if err != nil {
		return nil, nil, err
	}

	scope, err := scopePrefixes(req.Payload, prefix, req.Repository.ID)
	if err != nil {
		return nil, nil, err
	}
	var (
		rows    []any
		outside int
	)
	for _, way := range directions(direction) {
		tool := toolCallers
		if way == directionOutgoing {
			tool = toolCallees
		}
		found, err := r.walk(ctx, sess, tool, node.ID, depth)
		if err != nil {
			return nil, nil, err
		}
		for _, hop := range found {
			relative, inside := toRepository(prefix, hop.File)
			if !inside {
				// A hop in another repository is a true fact with nowhere to
				// go: the declared output is repository-relative.
				outside++
				continue
			}
			if r.isSensitive(relative) {
				// Dropped in silence, the same policy the omp adapter applies
				// to a search hit inside a secret: a call graph walk did not
				// ask about this file by name, so stopping to report it would
				// cost more than ignoring it is worth.
				continue
			}
			if !inScope(relative, scope) {
				continue
			}
			record := map[string]any{
				"path":      relative,
				"line":      hop.Line,
				"name":      hop.Name,
				"direction": way,
				"depth":     hop.Depth,
			}
			if wantSnippet {
				// A hop whose file leaves the root loses its snippet and
				// keeps its row: the row was built from the graph and is
				// still true, while the snippet is the only part that would
				// have needed the read. snippetAt already answers "" for a
				// file it cannot open, so an escape lands in the same place.
				if resolved, err := within(r.root, hop.File); err == nil {
					if text := snippetAt(resolved, hop.Line, snippetLines); text != "" {
						record["snippet"] = text
					}
				}
			}
			rows = append(rows, record)
		}
	}
	if rows == nil {
		rows = []any{}
	}

	notes := []string{fmt.Sprintf("tokensave: %s at %s:%d has %d call(s) within %s",
		node.Name, filepath.ToSlash(path.Clean(file)), line, len(rows), req.Repository.ID)}
	if outside > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d call(s) of %s live outside %s and are not in this answer: symbol.calls reports repository-relative paths only",
			outside, node.Name, req.Repository.ID))
	}
	return map[string]any{"calls": rows}, notes, nil
}

// resolve turns a position into the one node tokensave can walk from.
//
// Two calls, and neither is avoidable: tokensave_entities is the only tool
// that reports what a FILE declares with the span it covers, and it reports
// no id; find_exact_symbol is the only tool that reports an id, and it is
// keyed by name. So the position picks a declaration by span, and the name of
// that declaration is what the id is looked up with -- disambiguated by file
// and line, because a name is not unique across 53 repositories.
func (r *Runner) resolve(ctx context.Context, sess *mcpstdio.Session, file string, line int,
	hint string) (match, error) {

	found, err := r.fetchEntities(ctx, sess, file)
	if err != nil {
		return match{}, err
	}
	declaration, ok := declarationAt(found, line, hint)
	if !ok {
		return match{}, contract.Fail(contract.FailureNotFound,
			"tokensave: %s:%d names no declaration in the graph's outline of that file", file, line)
	}
	text, err := sess.Call(ctx, toolExact, map[string]any{"name": declaration.Name})
	if err != nil {
		return match{}, err
	}
	var answer matchesAnswer
	if err := json.Unmarshal(payloadOf(text), &answer); err != nil {
		return match{}, fmt.Errorf("tokensave %s: unreadable answer: %w", toolExact, err)
	}
	for _, candidate := range answer.Matches {
		if filepath.ToSlash(path.Clean(candidate.File)) == file && candidate.Line == declaration.Line {
			return candidate, nil
		}
	}
	return match{}, contract.Fail(contract.FailureNotFound,
		"tokensave: %s is declared at %s:%d in the outline but carries no node id the call graph can be walked from",
		declaration.Name, file, declaration.Line)
}

// declarationAt picks the declaration a position falls inside: the smallest
// span that contains the line, so a method wins over the type it hangs off.
// The name hint only ever breaks a tie -- a caller holding a cursor knows the
// word under it, and the capability says an implementation may ignore it.
func declarationAt(found []entity, line int, hint string) (entity, bool) {
	var best entity
	ok := false
	for _, item := range found {
		if pseudoKinds[item.Kind] || nestedKinds[item.Kind] {
			continue
		}
		end := max(item.EndLine, item.Line)
		if item.Line > line || line > end {
			continue
		}
		switch {
		case !ok:
			best, ok = item, true
		case hint != "" && item.Name == hint && best.Name != hint:
			best = item
		case span(item) < span(best) && (hint == "" || best.Name != hint):
			best = item
		}
	}
	return best, ok
}

// walk runs one direction of the call graph.
func (r *Runner) walk(ctx context.Context, sess *mcpstdio.Session, tool, nodeID string,
	depth int) ([]call, error) {

	text, err := sess.Call(ctx, tool, map[string]any{"node_id": nodeID, "max_depth": depth})
	if err != nil {
		return nil, err
	}
	var rows []call
	if err := json.Unmarshal(payloadOf(text), &rows); err != nil {
		return nil, fmt.Errorf("tokensave %s: unreadable answer: %w", tool, err)
	}
	slices.SortStableFunc(rows, func(a, b call) int {
		if a.Depth != b.Depth {
			return a.Depth - b.Depth
		}
		if a.File != b.File {
			return strings.Compare(a.File, b.File)
		}
		return a.Line - b.Line
	})
	return rows, nil
}

// directions is which walks one declared direction means.
func directions(declared string) []string {
	if declared == directionBoth {
		return []string{directionIncoming, directionOutgoing}
	}
	return []string{declared}
}

// readDirection reads the one required option symbol.calls will not guess.
func readDirection(payload map[string]any) (string, error) {
	value, _ := payload["direction"].(string)
	switch strings.ToLower(strings.TrimSpace(value)) {
	case directionIncoming:
		return directionIncoming, nil
	case directionOutgoing:
		return directionOutgoing, nil
	case directionBoth:
		return directionBoth, nil
	}
	return "", contract.Fail(contract.FailureInvalidInput,
		"direction %q: want %q, %q or %q", value, directionIncoming, directionOutgoing, directionBoth)
}

// scopePrefixes turns the declared scope into repository-relative prefixes.
// An empty scope means the whole repository, which is every path.
//
// The entries stay repository-relative, unlike code.context's, which cross the
// wire and are rooted first: inScope matches them against paths this package
// has already translated back. They go through toRoot anyway and only its
// verdict is kept, because the two parameters were being discarded and an
// entry like "../other" was accepted as a filter that can never match. That
// answers "this symbol has no calls" to a question that was never askable,
// which is precisely what runContext refuses by the same rule.
func scopePrefixes(payload map[string]any, prefix, repositoryID string) ([]string, error) {
	raw, ok := payload["scope"].([]any)
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text, isText := item.(string)
		if !isText || strings.TrimSpace(text) == "" {
			continue
		}
		if _, err := toRoot(prefix, text, repositoryID); err != nil {
			return nil, err
		}
		out = append(out, filepath.ToSlash(path.Clean(strings.TrimSpace(text))))
	}
	return out, nil
}

// inScope reports whether one repository-relative path is under any declared
// scope entry, matching a directory prefix or the file itself.
func inScope(relative string, scope []string) bool {
	if len(scope) == 0 {
		return true
	}
	for _, entry := range scope {
		if relative == entry || strings.HasPrefix(relative, entry+"/") {
			return true
		}
	}
	return false
}

// within turns a root-relative path into the absolute file to open, and is
// the last gate before this package touches the disk.
//
// Both reads here are of paths the FAR SIDE chose: runOverview reads the file
// tokensave says it indexed, and runCalls reads the file a call-graph hop
// points at. The lexical checks in toRoot and toRepository run before that,
// but they cannot see a symlink: `services/api/vendor` pointing at /etc is a
// path that never climbs out of the root and still reads outside it. So the
// containment is checked twice, once lexically and once on the resolved path,
// exactly as the kivgraph adapter does for the same reason. A path that does
// not exist resolves to no error: the caller's own read reports that case, and
// a missing file is not an escape.
func within(root, relative string) (string, error) {
	name := filepath.FromSlash(relative)
	if name == "" || filepath.IsAbs(name) {
		return "", contract.Fail(contract.FailureInvalidInput, "%q must be relative to the served root", relative)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", contract.Fail(contract.FailureUnavailable, "cannot resolve tokensave's root: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolved
	}
	joined := filepath.Join(rootAbs, name)
	if err := contained(rootAbs, joined, relative); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if os.IsNotExist(err) {
			return joined, nil
		}
		return "", contract.Fail(contract.FailureUnavailable, "cannot resolve %q: %v", relative, err)
	}
	if err := contained(rootAbs, resolved, relative); err != nil {
		return "", err
	}
	return joined, nil
}

// contained reports whether target sits inside root, once both are absolute.
func contained(root, target, name string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return contract.Fail(contract.FailurePermissionDenied, "%q leaves tokensave's root", name)
	}
	return nil
}

// readLines reads a file for column and snippet recovery. A file that cannot
// be read is not an error here: both callers have an honest fallback, and
// failing the whole answer over a snippet would be the tail wagging the dog.
func readLines(name string) []string {
	info, err := os.Stat(name)
	if err != nil || info.Size() > maxSnippetBytes || info.IsDir() {
		return nil
	}
	file, err := os.Open(name)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	var out []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		out = append(out, scanner.Text())
	}
	if scanner.Err() != nil {
		return nil
	}
	return out
}

// columnOf finds where a symbol's own name starts on its line, as a whole
// word. Column 1 is the answer when the file is unreadable or the name does
// not appear there -- which happens for a generated name, and for a
// declaration whose name sits on the line above the one reported.
func columnOf(lines []string, line int, name string) int {
	if line <= 0 || line > len(lines) || name == "" {
		return 1
	}
	text := lines[line-1]
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	if at := word.FindStringIndex(text); at != nil {
		return at[0] + 1
	}
	return 1
}

// snippetAt reads the window around one line.
func snippetAt(name string, line, window int) string {
	if window <= 0 {
		window = defaultSnippetLines
	}
	lines := readLines(name)
	if line <= 0 || line > len(lines) {
		return ""
	}
	from := max(line-window/2, 1)
	to := min(from+window-1, len(lines))
	return strings.Join(lines[from-1:to], "\n")
}

// intAt reads an optional integer, refusing a value of the wrong type rather
// than silently taking the default: a caller who sent depth = "2" asked for
// something, and answering with the default would answer a question nobody
// put.
func intAt(payload map[string]any, key string, fallback int) (int, error) {
	switch value := payload[key].(type) {
	case nil:
		return fallback, nil
	case int:
		return value, nil
	case int64:
		return int(value), nil
	case float64:
		return int(value), nil
	}
	return 0, contract.Fail(contract.FailureInvalidInput,
		"%s must be a number, got %T", key, payload[key])
}

// failureFor sorts a far-side error into a shared bin. This is the whole of
// what Atenea knows about how tokensave fails, and the core knows none of it.
func (r *Runner) failureFor(err error, ctx context.Context) error {
	var known *contract.Failure
	if errors.As(err, &known) {
		return known
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return contract.Stopped(ctxErr, "tokensave", r.timeout).WithRaw(err.Error())
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "not found"), strings.Contains(message, "no such file"):
		return contract.Fail(contract.FailureNotFound, "tokensave: %v", err)
	case strings.Contains(message, "permission denied"):
		return contract.Fail(contract.FailurePermissionDenied, "tokensave: %v", err)
	}
	return contract.Fail(contract.FailureUnavailable, "tokensave: %v", err).WithRaw(err.Error())
}
