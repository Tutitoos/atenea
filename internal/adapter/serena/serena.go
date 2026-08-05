// Package serena is the third adapter, and the first whose far side is not a
// command line.
//
// Serena is an MCP server behind a local proxy: it speaks JSON-RPC over HTTP
// instead of argv and stdout. Nothing above this file changes because of that.
// A capability does not care whether its provider is a binary or a server, and
// the fact that this package satisfies exactly the same contract.Runner as the
// omp and Claude Code adapters is the evidence that the seam is real rather
// than decorative.
//
// Two translation problems are peculiar to this far side, and both are the
// adapter's to absorb.
//
// The first is coordinates. Atenea names a symbol by POSITION -- file, line,
// column -- because a workspace of forty repositories holds the same name many
// times over. Serena names a symbol by NAME PATH and does not accept a
// position at all. So the adapter converts, and it does so by asking Serena
// for the symbol map of that one file and finding which symbol covers the
// position. It never parses code itself: an adapter that started reading
// syntax would be a second brain, and there is only supposed to be one.
//
// The second is that each Serena process holds ONE active project at a time.
// Atenea dispatches waves of up to four steps across several repositories, so
// two symbol steps against the same endpoint would silently retarget each
// other. Every call therefore runs under one lock per endpoint and
// re-activates its repository first. A repository may name its own Serena
// endpoint so two projects stay warm on two processes instead of thrashing
// one; empty falls back to the adapter default and pays the retarget. That
// makes symbol work serial per endpoint, which is honest: it is what a single
// shared language server can actually deliver, and the declared cost says so.
package serena

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// The capabilities this adapter answers, and the implementation ids the
// shipped catalog gives them. Ids are separate from capability ids because a
// capability is the "what" and an implementation is the "who": another
// provider may answer the same three capabilities tomorrow.
const (
	CapabilityDefinition      = "symbol.definition"
	CapabilityReferences      = "symbol.references"
	CapabilityImplementations = "symbol.implementations"

	ImplDefinition      = "serena.definition"
	ImplReferences      = "serena.references"
	ImplImplementations = "serena.implementations"
)

// DefaultEndpoint is where the ToolHive proxy puts Serena when the port is
// pinned, which it must be: an unpinned proxy picks a random port and the URL
// written here would work exactly until the next restart.
const DefaultEndpoint = "http://127.0.0.1:40010/mcp"

// DefaultTimeout caps one call. A language server indexing a cold repository
// is slow long before it is stuck, so this sits well above omp's ceiling; past
// it the call is stuck rather than working, and the timeout bin is what lets
// the core fall back to somebody else.
const DefaultTimeout = 90 * time.Second

// defaultSnippetLines is the "small by default" the capability promises when a
// caller asks for a fragment without saying how much of one.
const defaultSnippetLines = 5

// protocolVersion is the MCP revision this adapter speaks. It is stated rather
// than negotiated down: a server that cannot answer it should say so at the
// handshake, not halfway through a commission.
const protocolVersion = "2025-06-18"

// DefaultImplementations is what the adapter answers for. It is a function and
// not a package-level slice because a caller that appended to a shared one
// would quietly change what every other Atenea in this process serves.
func DefaultImplementations() []string {
	return []string{ImplDefinition, ImplReferences, ImplImplementations}
}

// Options configure the adapter.
type Options struct {
	// Endpoint is the default MCP server URL. Used for every repository that
	// does not name its own SerenaEndpoint.
	Endpoint string
	// Implementations the adapter answers for.
	Implementations []string
	// Sensitive holds the path patterns that carry secrets. This adapter opens
	// a file to read the word under the cursor, so the list is not advisory
	// here: it is the difference between reading a name and reading a key.
	Sensitive []string
	// Timeout caps one call.
	Timeout time.Duration
	// HTTP is the client used to reach every endpoint. Zero uses a default one.
	// Tests point this at a stub server; nothing else should need it.
	HTTP *http.Client
}

// conn is one live MCP session against one Serena URL. Session state cannot
// be shared across URLs: a handshake on :40010 is meaningless to :9121, and
// an active project on one is invisible to the other.
type conn struct {
	endpoint string

	// mu serializes every exchange on this endpoint. See the package comment:
	// one active project at a time means one caller at a time per URL.
	mu sync.Mutex
	// session is the MCP session id, established lazily on the first call and
	// reused. It is guarded by mu like everything else here.
	session string
	// active is the project path this Serena is currently pointed at, so a run
	// of steps against one repository does not re-activate it every time.
	active string
	// nextID numbers the JSON-RPC requests on this session.
	nextID int
	// version is what the server called itself when the session opened.
	version string
}

// Runner is the Serena far side of contract.Runner.
type Runner struct {
	defaultEndpoint string
	implementations []string
	sensitive       []string
	timeout         time.Duration
	http            *http.Client

	// conns holds one session per distinct endpoint URL. Guarded by connsMu
	// only for map insert/lookup; each conn.mu guards that session's wire
	// state, so two repositories on two endpoints run in parallel.
	connsMu sync.Mutex
	conns   map[string]*conn
}

// New validates the options and returns the adapter.
func New(opts Options) (*Runner, error) {
	endpoint := strings.TrimSpace(opts.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"serena adapter: endpoint %q must be an http or https URL", endpoint)
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"serena adapter: timeout must not be negative, got %s", timeout)
	}
	impls := slices.Clone(opts.Implementations)
	if impls == nil {
		impls = DefaultImplementations()
	}
	slices.Sort(impls)
	client := opts.HTTP
	if client == nil {
		// The per-call deadline is carried on the context, so the client keeps
		// no timeout of its own: two ceilings on the same call would race, and
		// the one that fired first would be the one nobody configured.
		client = &http.Client{}
	}
	return &Runner{
		defaultEndpoint: endpoint,
		implementations: impls,
		sensitive:       slices.Clone(opts.Sensitive),
		timeout:         timeout,
		http:            client,
		conns:           make(map[string]*conn),
	}, nil
}

// connFor returns the session state for the endpoint this repository should
// hit. A repository that names its own SerenaEndpoint gets that URL; everyone
// else shares the adapter default. Distinct URLs never share a conn.
func (r *Runner) connFor(repo contract.Repository) *conn {
	endpoint := strings.TrimSpace(repo.SerenaEndpoint)
	if endpoint == "" {
		endpoint = r.defaultEndpoint
	}
	r.connsMu.Lock()
	defer r.connsMu.Unlock()
	if c, ok := r.conns[endpoint]; ok {
		return c
	}
	c := &conn{endpoint: endpoint}
	r.conns[endpoint] = c
	return c
}

// ID names the runner on the status screen.
func (r *Runner) ID() string { return "serena" }

// Implementations lists every implementation this runner declares itself the
// far side of.
func (r *Runner) Implementations() []string { return slices.Clone(r.implementations) }

// Serves answers the same question one id at a time.
func (r *Runner) Serves(implementationID string) bool {
	return slices.Contains(r.implementations, implementationID)
}

// serverVersion is what the default endpoint called itself, if a session has
// opened there. Prefer the per-call ToolVersion taken from the conn that
// actually answered; this is only a fallback for callers that have not run.
func (r *Runner) serverVersion() string {
	r.connsMu.Lock()
	c := r.conns[r.defaultEndpoint]
	r.connsMu.Unlock()
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// Run executes one step.
//
// The version travels back on every path, including the failing ones. Here it
// is whatever the handshake already learned on the endpoint this repository
// uses: a session that never opened has nothing to report, which is a fact
// rather than a gap.
func (r *Runner) Run(ctx context.Context, req contract.RunRequest) (out contract.Outcome, err error) {
	var toolVersion string
	defer func() { out.ToolVersion = toolVersion }()
	if err := req.Validate(); err != nil {
		return contract.Outcome{}, err
	}
	if missing, ok := req.Allowed(); !ok {
		return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied,
			"%s causes %s, which the commission does not cover", req.Capability.ID, missing)
	}
	if !r.Serves(req.Implementation.ID) {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"serena adapter does not serve implementation %s", req.Implementation.ID)
	}
	kind, err := kindOf(req.Capability.ID)
	if err != nil {
		return contract.Outcome{}, err
	}
	ask, err := readAsk(req.Payload, req.Capability.ID)
	if err != nil {
		return contract.Outcome{}, err
	}

	started := time.Now()
	call, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	root, err := filepath.Abs(req.Repository.Path)
	if err != nil {
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %q: %v", req.Repository.ID, req.Repository.Path, err)
	}

	c := r.connFor(req.Repository)
	// One lock around the whole exchange on this endpoint, not around each
	// round trip: the project activation and the calls that depend on it have
	// to be one indivisible unit, or a second caller on the same URL would
	// retarget Serena between them. Distinct endpoints take distinct locks.
	c.mu.Lock()
	records, notes, runErr := r.resolve(call, c, root, kind, ask)
	toolVersion = c.version
	c.mu.Unlock()

	if runErr != nil {
		return contract.Outcome{}, r.failureFor(runErr, call)
	}

	result, err := shape(kind, records, req.Capability)
	if err != nil {
		return contract.Outcome{}, err
	}
	outcome := contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		// No memory figure: Serena is a server in somebody else's process,
		// so there is no child process here to weigh. Leaving it at zero is
		// what tells the store to record the gap instead of a number.
		Spent: contract.Sample{Duration: time.Since(started)},
	}
	// Discoveries carry the facts a human needs after the fact: which name the
	// position became, whether this call paid a project retarget (the slow
	// multi-repo tax), and how many locations came back.
	for _, note := range notes {
		if note == "" {
			continue
		}
		outcome.Discoveries = append(outcome.Discoveries,
			contract.Discovery{Level: contract.ContextRepository, Note: note})
	}
	outcome.Discoveries = append(outcome.Discoveries, contract.Discovery{
		Level: contract.ContextRepository,
		Note: fmt.Sprintf("serena answered %s for %s with %d location(s)",
			req.Capability.ID, req.Repository.ID, len(records)),
	})
	return outcome, nil
}

// kind is which of the three questions is being asked. Keeping it as one value
// rather than three code paths is what makes references and implementations
// visibly the same shape, which the design says they are.
type kind uint8

const (
	kindDefinition kind = iota
	kindReferences
	kindImplementations
)

func kindOf(capabilityID string) (kind, error) {
	switch capabilityID {
	case CapabilityDefinition:
		return kindDefinition, nil
	case CapabilityReferences:
		return kindReferences, nil
	case CapabilityImplementations:
		return kindImplementations, nil
	}
	return 0, contract.Fail(contract.FailureNotFound,
		"serena adapter has no translation for %s", capabilityID)
}

// ask is the payload after it has been checked once against the declared
// schema, so the rest of this file knows the shape and only has to care about
// the meaning.
type ask struct {
	file    string
	line    int
	column  int
	name    string
	scope   []string
	snippet bool
	lines   int
	// identifier is the word actually sitting at the position, filled in once
	// the file has been read. The payload's name is a hint the caller may have
	// got wrong or left out; this one is what is really there.
	identifier string
}

func readAsk(payload map[string]any, capabilityID string) (ask, error) {
	out := ask{lines: defaultSnippetLines}
	file, _ := payload["file"].(string)
	out.file = strings.TrimSpace(file)
	if out.file == "" {
		return ask{}, contract.Fail(contract.FailureInvalidInput,
			"%s: file is empty", capabilityID)
	}
	if filepath.IsAbs(out.file) {
		// The capability says "relative to the repository root" and means it.
		// An absolute path here would work by accident on this machine and
		// break the moment the same commission ran anywhere else.
		return ask{}, contract.Fail(contract.FailureInvalidInput,
			"%s: file %q must be relative to the repository root", capabilityID, out.file)
	}
	var err error
	if out.line, err = positive(payload, "line", capabilityID); err != nil {
		return ask{}, err
	}
	if out.column, err = positive(payload, "column", capabilityID); err != nil {
		return ask{}, err
	}
	if hint, ok := payload["name"].(string); ok {
		out.name = strings.TrimSpace(hint)
	}
	for _, raw := range list(payload["scope"]) {
		if path := strings.TrimSpace(raw); path != "" {
			out.scope = append(out.scope, path)
		}
	}
	out.snippet, _ = payload["include_snippet"].(bool)
	if n, ok := whole(payload["snippet_lines"]); ok {
		if n <= 0 {
			return ask{}, contract.Fail(contract.FailureInvalidInput,
				"%s: snippet_lines must be above 0, got %d", capabilityID, n)
		}
		out.lines = n
	}
	return out, nil
}

func positive(payload map[string]any, field, capabilityID string) (int, error) {
	n, ok := whole(payload[field])
	if !ok {
		return 0, contract.Fail(contract.FailureInvalidInput,
			"%s: %s is required and must be a whole number", capabilityID, field)
	}
	if n < 1 {
		// Both line and column are 1-based in the contract. A zero here is
		// almost always an off-by-one in the caller, and answering it as if it
		// were line 1 would hide the bug behind a plausible answer.
		return 0, contract.Fail(contract.FailureInvalidInput,
			"%s: %s starts at 1, got %d", capabilityID, field, n)
	}
	return n, nil
}

// whole accepts the float64 a JSON decoder produces for whole numbers, so an
// adapter speaking JSON is not forced to pre-convert.
func whole(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int64(n)) {
			return int(n), true
		}
	}
	return 0, false
}

func list(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// location is one answer, in Atenea's words rather than Serena's.
type location struct {
	Path    string
	Line    int
	Snippet string
}

// resolve runs the exchange. It assumes c.mu is held.
func (r *Runner) resolve(ctx context.Context, c *conn, root string, k kind, a ask) ([]location, []string, error) {
	// The word under the cursor comes from the file, before Serena is asked
	// anything: a position that names nothing is a bad request, and finding
	// that out costs one line read rather than a project activation.
	identifier, err := identifierAt(r, root, a)
	if err != nil {
		return nil, nil, err
	}
	a.identifier = identifier
	retargeted, previous, err := r.activate(ctx, c, root)
	if err != nil {
		return nil, nil, err
	}
	var notes []string
	if retargeted {
		// A real project switch on this endpoint: the previous language servers
		// were torn down and the new ones started. Naming it here is what keeps
		// a slow multi-repo run from looking silently slow in the trace.
		notes = append(notes, fmt.Sprintf(
			"serena retargeted %s from %s to %s", c.endpoint, previous, root))
	}
	found, note, err := r.symbolAt(ctx, c, a)
	if err != nil {
		return nil, notes, err
	}
	notes = append(notes, note)
	switch k {
	case kindDefinition:
		return locationsFrom([]symbol{found}, a), notes, nil
	case kindReferences:
		out, err := r.referencing(ctx, c, a, found.NamePath)
		return out, notes, err
	default:
		out, err := r.findImplementations(ctx, c, a, found.NamePath)
		return out, notes, err
	}
}

// activate points this endpoint's Serena at the repository. Serena holds one
// project at a time per process, so this is state and not a parameter, and
// re-pointing it is only skipped when it is already where it needs to be.
// retargeted is true only when a different project was active before — the
// case that tears language servers down. A first activation (previous empty)
// is not a retarget.
func (r *Runner) activate(ctx context.Context, c *conn, root string) (retargeted bool, previous string, err error) {
	if c.active == root {
		return false, "", nil
	}
	previous = c.active
	if _, err := r.call(ctx, c, "activate_project", map[string]any{"project": root}); err != nil {
		c.active = ""
		return false, previous, err
	}
	c.active = root
	return previous != "", previous, nil
}

// symbolAt turns the position the capability speaks into the symbol Serena
// speaks about.
//
// One call does it: Serena's symbol search takes a bare leaf name and answers
// with every name path that ends in it, each with its range. The position then
// picks among them, which is exactly the job the design gave it. The overview
// tool cannot do this -- it reports names with no line numbers -- and the
// wildcard search times out on a real repository. Both measured, not assumed.
//
// The returned note is not decoration: the caller named a position and gets an
// answer about a name, so the trace has to say which name that was, or the
// answer cannot be checked against the question.
func (r *Runner) symbolAt(ctx context.Context, c *conn, a ask) (symbol, string, error) {
	raw, err := r.call(ctx, c, "find_symbol", map[string]any{
		"name_path_pattern": a.identifier,
		"relative_path":     a.file,
		"include_body":      a.snippet,
	})
	if err != nil {
		return symbol{}, "", err
	}
	candidates, err := parseSymbols(raw)
	if err != nil {
		return symbol{}, "", err
	}
	found, err := pick(candidates, a)
	if err != nil {
		return symbol{}, "", err
	}
	note := fmt.Sprintf("position %s:%d:%d names %q, which is symbol %s",
		a.file, a.line, a.column, a.identifier, found.NamePath)
	// The payload's name is declared a hint, so a mismatch is reported and not
	// refused: the position is the authority. An implementation that let the
	// hint override it would answer a different question than the one asked.
	if a.name != "" && a.name != a.identifier {
		note += fmt.Sprintf(" (the hint said %q; the position wins)", a.name)
	}
	return found, note, nil
}

// referencing asks who uses the symbol.
//
// The answer comes back in a different shape from a symbol search -- path,
// then kind, then entries -- which is why it has a parser of its own rather
// than a flag on the first one.
func (r *Runner) referencing(ctx context.Context, c *conn, a ask, namePath string) ([]location, error) {
	raw, err := r.call(ctx, c, "find_referencing_symbols", map[string]any{
		"name_path":     namePath,
		"relative_path": a.file,
	})
	if err != nil {
		return nil, err
	}
	found, err := parseReferences(raw)
	if err != nil {
		return nil, err
	}
	return withinScope(found, a.scope), nil
}

// findImplementations asks who fulfills the symbol.
//
// Not every language server implements this request, and the ones that do not
// say so plainly. That is a provider that cannot answer here, not a broken
// commission, so the bin is unavailable and the funnel falls back.
func (r *Runner) findImplementations(ctx context.Context, c *conn, a ask, namePath string) ([]location, error) {
	raw, err := r.call(ctx, c, "find_implementations", map[string]any{
		"name_path":     namePath,
		"relative_path": a.file,
	})
	if err != nil {
		return nil, err
	}
	// Serena answers this one in the reference shape when it has hits, and
	// with "{}" or "[]" when it has none (measured: find_implementations
	// uses the array). Asking something concrete for its implementations is
	// a legitimate empty answer, not a failure.
	found, err := parseReferences(raw)
	if err != nil {
		return nil, err
	}
	return withinScope(found, a.scope), nil
}

// withinScope enforces the scope the caller declared.
//
// Serena has no scope parameter for these two tools, so the narrowing happens
// here. That is the adapter absorbing a difference rather than leaking it: a
// caller that asked for one directory and got the whole repository back would
// have been answered a question it did not ask.
func withinScope(found []location, scope []string) []location {
	if len(scope) == 0 {
		return found
	}
	kept := make([]location, 0, len(found))
	for _, loc := range found {
		for _, prefix := range scope {
			clean := filepath.Clean(prefix)
			if loc.Path == clean || strings.HasPrefix(loc.Path, clean+string(filepath.Separator)) {
				kept = append(kept, loc)
				break
			}
		}
	}
	return kept
}

// shape turns the locations into the output the capability declared, and then
// checks them against it. The check is not paranoia about our own code: the
// records are built from whatever the far side sent, and a capability whose
// declared shape is not enforced is a comment rather than a contract.
func shape(k kind, found []location, capability contract.Capability) (map[string]any, error) {
	records := make([]any, 0, len(found))
	for _, loc := range found {
		record := map[string]any{"path": loc.Path, "line": loc.Line}
		if loc.Snippet != "" {
			record["snippet"] = loc.Snippet
		}
		records = append(records, record)
	}
	var result map[string]any
	if k == kindDefinition {
		if len(records) == 0 {
			return nil, contract.Fail(contract.FailureNotFound, "no definition found")
		}
		result = map[string]any{"location": records[0]}
	} else {
		result = map[string]any{"locations": records}
	}
	if err := capability.ValidateOutput(result); err != nil {
		return nil, err
	}
	return result, nil
}

// failureFor sorts what went wrong into the shared bins.
//
// The bins are the whole point of an adapter: whatever wording the far side
// invents, the core only ever sees one of six, with the untranslated text
// traveling beside it for whoever debugs later.
func (r *Runner) failureFor(err error, ctx context.Context) *contract.Failure {
	// A failure the adapter already binned travels as it is: re-reading its
	// text would be the adapter guessing about its own error.
	var known *contract.Failure
	if errors.As(err, &known) {
		return known
	}
	text := strings.TrimSpace(err.Error())
	lower := strings.ToLower(text)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return contract.Stopped(ctxErr, "serena", r.timeout).WithRaw(text)
	}
	switch {
	case strings.Contains(lower, "language server"),
		strings.Contains(lower, "solidlspexception"),
		strings.Contains(lower, "unhandled method"),
		strings.Contains(lower, "not initialized"),
		strings.Contains(lower, "is not installed"):
		// Serena is up and answering; it just cannot do this here. Either it
		// has no language server for this repository, or the one it has does
		// not implement the request -- measured: a Python server refuses
		// textDocument/implementation outright. Both are a provider that
		// cannot work here, which is what unavailable means and what makes
		// the funnel fall back to somebody who can.
		return contract.Fail(contract.FailureUnavailable,
			"serena has no working language server for this request").WithRaw(text)
	case strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "no such host"),
		strings.Contains(lower, "econnrefused"):
		return contract.Fail(contract.FailureUnavailable,
			"serena is not reachable at this endpoint").WithRaw(text)
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timeout"):
		return contract.Fail(contract.FailureTimeout,
			"serena took longer than allowed").WithRaw(text)
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "forbidden"):
		return contract.Fail(contract.FailurePermissionDenied,
			"serena refused access").WithRaw(text)
	case strings.Contains(lower, "no such file"), strings.Contains(lower, "not found"):
		return contract.Fail(contract.FailureNotFound,
			"serena could not find what it was pointed at").WithRaw(text)
	}
	return contract.Fail(contract.FailureUnavailable,
		"serena did not answer").WithRaw(text)
}
