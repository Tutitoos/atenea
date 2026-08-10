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
// times over. Serena mostly names a symbol by NAME PATH instead, and the one
// tool that does take a position (find_declaration) takes it wrapped in a
// regex, not as a line and column. So the adapter converts either way: its
// first choice asks Serena to resolve the position directly, in one call,
// wherever the declaration turns out to live; failing that, it falls back to
// asking for the symbol map of the query's own file and finding which symbol
// covers the position. It never parses code itself: an adapter that started
// reading syntax would be a second brain, and there is only supposed to be
// one.
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
	CapabilityOverview        = "symbol.overview"

	ImplDefinition      = "serena.definition"
	ImplReferences      = "serena.references"
	ImplImplementations = "serena.implementations"
	ImplOverview        = "serena.overview"
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
	return []string{ImplDefinition, ImplReferences, ImplImplementations, ImplOverview}
}

// Options configure the adapter.
type Options struct {
	// Endpoint is the default MCP server URL, used for every repository that
	// EndpointFor does not answer for.
	Endpoint string
	// EndpointFor resolves the URL for one repository, and is how a
	// per-repository policy reaches this adapter without the adapter knowing
	// such a policy exists. Nil, or an empty answer, means Endpoint.
	//
	// A function rather than a map because the far side is started on
	// demand: the URL for a repository is known before the process is up,
	// but the caller supplying this is also the one that has to ensure it
	// comes up, and giving it the call rather than a snapshot keeps those
	// two facts in one place.
	EndpointFor func(repo contract.Repository) (string, error)
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

	// mu serializes each commission's exchange on this endpoint: one active
	// project at a time means one caller at a time per URL, so two
	// commissions never interleave activation or the session lifecycle.
	mu sync.Mutex
	// wireMu additionally guards session, active, nextID, and version below.
	// A commission holds mu for its whole exchange, but symbol.overview's
	// locateAll dispatches many find_symbol calls concurrently inside that
	// one hold: mu excludes other commissions from each other, not these
	// siblings, so the fields every rpc() call touches need their own,
	// finer lock that each concurrent call actually takes.
	wireMu sync.Mutex
	// session is the MCP session id, established lazily on the first call
	// and reused. Guarded by wireMu.
	session string
	// active is the project path this Serena is currently pointed at, so a
	// run of steps against one repository does not re-activate it every
	// time. Guarded by wireMu.
	active string
	// nextID numbers the JSON-RPC requests on this session. Guarded by
	// wireMu.
	nextID int
	// version is what the server called itself when the session opened.
	// Guarded by wireMu.
	version string
}

// Runner is the Serena far side of contract.Runner.
type Runner struct {
	defaultEndpoint string
	endpointFor     func(repo contract.Repository) (string, error)
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

// ValidateEndpoint reports whether an endpoint is one this adapter could
// dial. Empty is allowed and means DefaultEndpoint.
//
// It is exported because the settings layer has to check an endpoint it will
// then throw away: declaring a managed process takes the address over, and a
// file whose validity depended on whether that table happened to sit beside
// it would mean two different things on two days.
func ValidateEndpoint(endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return contract.Fail(contract.FailureInvalidInput,
			"serena adapter: endpoint %q must be an http or https URL", endpoint)
	}
	return nil
}

// New validates the options and returns the adapter.
func New(opts Options) (*Runner, error) {
	endpoint := strings.TrimSpace(opts.Endpoint)
	if err := ValidateEndpoint(endpoint); err != nil {
		return nil, err
	}
	if endpoint == "" {
		endpoint = DefaultEndpoint
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
		endpointFor:     opts.EndpointFor,
		implementations: impls,
		sensitive:       slices.Clone(opts.Sensitive),
		timeout:         timeout,
		http:            client,
		conns:           make(map[string]*conn),
	}, nil
}

// connFor returns the session state for the endpoint this repository should
// hit.
//
// Distinct URLs never share a conn, and that is the whole reason this exists:
// Serena's active project is process-wide, so two repositories on one URL take
// turns and pay a language server restart each time they swap. Two
// repositories on two URLs do not. Which of those a machine gets is decided by
// the resolver, above this adapter -- it knows about instance policies and
// repositories, and this only knows that a URL is a session.
func (r *Runner) connFor(repo contract.Repository) (*conn, error) {
	endpoint := r.defaultEndpoint
	if r.endpointFor != nil {
		resolved, err := r.endpointFor(repo)
		if err != nil {
			return nil, err
		}
		if resolved = strings.TrimSpace(resolved); resolved != "" {
			endpoint = resolved
		}
	}
	r.connsMu.Lock()
	defer r.connsMu.Unlock()
	if c, ok := r.conns[endpoint]; ok {
		return c, nil
	}
	c := &conn{endpoint: endpoint}
	r.conns[endpoint] = c
	return c, nil
}

// ID names the runner on the status screen.
func (r *Runner) ID() string { return "serena" }

// This runner does not implement contract.IndexProber, on purpose rather
// than by omission: activate_project succeeds silently on a path with
// nothing indexed yet (it opens an empty project rather than erring), so it
// cannot serve as a readiness check, and there is no cheaper call that
// answers "is this repository's index actually warm" the way index_status
// answers it for codebase-memory. Detection skips a runner with nothing to
// probe rather than guess; indexed_by for serena stays exactly what the
// settings file declares until a real probe exists to correct it.

// Implementations lists every implementation this runner declares itself the
// far side of.
func (r *Runner) Implementations() []string { return slices.Clone(r.implementations) }

// Serves answers the same question one id at a time.
func (r *Runner) Serves(implementationID string) bool {
	return slices.Contains(r.implementations, implementationID)
}

// Capabilities lists what this adapter's Run can actually dispatch, so a
// settings file naming an implementation it has no case for is refused at
// load rather than at the call.
func (r *Runner) Capabilities() []string {
	return []string{CapabilityDefinition, CapabilityReferences, CapabilityImplementations, CapabilityOverview}
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
	if !r.Serves(req.Implementation.ID) {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"serena adapter does not serve implementation %s", req.Implementation.ID)
	}
	if req.Capability.ID == CapabilityOverview {
		// Not a fourth kind alongside the switch below: kindOf, ask, resolve
		// and shape all exist to turn a POSITION into a symbol, and this
		// capability has no position -- it is the thing that hands one back.
		// Forcing it through that pipeline would mean bending four functions
		// built for one shape to also fit a second, unrelated one. What it
		// does share -- validation already done above, the connection, the
		// lock, activation, failure translation -- it shares by calling the
		// same methods this pipeline calls, not by joining the switch.
		outcome, version, runErr := r.runOverview(ctx, req)
		toolVersion = version
		return outcome, runErr
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

	c, err := r.connFor(req.Repository)
	if err != nil {
		return contract.Outcome{}, err
	}
	// One lock around the whole exchange on this endpoint, not around each
	// round trip: the project activation and the calls that depend on it have
	// to be one indivisible unit, or a second caller on the same URL would
	// retarget Serena between them. Distinct endpoints take distinct locks.
	c.mu.Lock()
	records, notes, runErr := r.resolve(call, c, root, kind, ask)
	c.wireMu.Lock()
	toolVersion = c.version
	c.wireMu.Unlock()
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

// runOverview is symbol.overview's own Run: the same shared steps -- timing,
// a bounded context, the repository root, one lock per endpoint -- around a
// different exchange. Where resolve asks Serena about a position, overview
// asks for the whole file at once and then locates, concurrently, every name
// that answer reported.
func (r *Runner) runOverview(ctx context.Context, req contract.RunRequest) (contract.Outcome, string, error) {
	a, err := readOverviewAsk(req.Payload)
	if err != nil {
		return contract.Outcome{}, "", err
	}

	started := time.Now()
	call, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	root, err := filepath.Abs(req.Repository.Path)
	if err != nil {
		return contract.Outcome{}, "", contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %q: %v", req.Repository.ID, req.Repository.Path, err)
	}

	c, err := r.connFor(req.Repository)
	if err != nil {
		return contract.Outcome{}, "", err
	}
	// Same indivisible unit as Run: activation and everything that depends on
	// it, held under one lock for the whole exchange.
	c.mu.Lock()
	entries, notes, runErr := r.overview(call, c, root, a)
	c.wireMu.Lock()
	toolVersion := c.version
	c.wireMu.Unlock()
	c.mu.Unlock()

	if runErr != nil {
		return contract.Outcome{}, toolVersion, r.failureFor(runErr, call)
	}

	result, err := shapeOverview(entries, req.Capability)
	if err != nil {
		return contract.Outcome{}, toolVersion, err
	}
	outcome := contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		// No memory figure, same reasoning as Run: Serena runs in somebody
		// else's process, so there is no child process here to weigh.
		Spent: contract.Sample{Duration: time.Since(started)},
	}
	for _, note := range notes {
		if note == "" {
			continue
		}
		outcome.Discoveries = append(outcome.Discoveries,
			contract.Discovery{Level: contract.ContextRepository, Note: note})
	}
	outcome.Discoveries = append(outcome.Discoveries, contract.Discovery{
		Level: contract.ContextRepository,
		Note: fmt.Sprintf("serena answered %s for %s with %d symbol(s)",
			req.Capability.ID, req.Repository.ID, len(entries)),
	})
	return outcome, toolVersion, nil
}

// overview runs the exchange. It assumes c.mu is held, same as resolve.
//
// The sensitivity and containment checks below are not free elsewhere in
// this file: identifierAt gets them for the other three capabilities by
// reading the file locally, through readLineAt, before ever calling Serena.
// This capability has no position to read first -- the file itself is the
// whole ask -- so without an explicit check here, a sensitive or
// repository-escaping path would reach a second process, and whatever its
// own parser can see in the file, before anything in this adapter had a
// chance to refuse it.
func (r *Runner) overview(ctx context.Context, c *conn, root string, a overviewAsk) ([]overviewEntry, []string, error) {
	if r.isSensitive(a.file) {
		return nil, nil, contract.Fail(contract.FailurePermissionDenied,
			"%s carries secrets and is not read", a.file)
	}
	if _, err := within(root, a.file); err != nil {
		return nil, nil, err
	}
	retargeted, previous, err := r.activate(ctx, c, root)
	if err != nil {
		return nil, nil, err
	}
	var notes []string
	if retargeted {
		notes = append(notes, fmt.Sprintf(
			"serena retargeted %s from %s to %s", c.endpoint, previous, root))
	}
	raw, err := r.call(ctx, c, "get_symbols_overview", map[string]any{
		"relative_path": a.file,
		"depth":         a.depth,
	})
	if err != nil {
		return nil, notes, err
	}
	names, err := parseOverviewNames(raw)
	if err != nil {
		return nil, notes, err
	}
	entries, unplaceable, err := r.locateAll(ctx, c, root, a, names)
	if err != nil {
		return nil, notes, err
	}
	if unplaceable > 0 {
		// Said out loud rather than swallowed: the answer is short by a known
		// amount, and a caller comparing two providers' symbol counts deserves
		// to know which one declined to guess.
		notes = append(notes, fmt.Sprintf(
			"%d of %d symbol(s) skipped: serena named them but their own name is not written anywhere in the range it reported",
			unplaceable, len(names)))
	}
	// get_symbols_overview groups by kind, which the caller never asked
	// about; top to bottom, as the file actually reads, is the useful order,
	// and it only becomes available now that every entry has a real line.
	slices.SortFunc(entries, func(x, y overviewEntry) int {
		if x.line != y.line {
			return x.line - y.line
		}
		if x.column != y.column {
			return x.column - y.column
		}
		return strings.Compare(x.name, y.name)
	})
	return entries, notes, nil
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

// overviewAsk is symbol.overview's own payload, once checked: a file and how
// deep to descend into it. It does not extend ask -- nothing else ask
// carries applies here, there is no position, and no function in this file
// needs to branch on which capability it is serving.
type overviewAsk struct {
	file  string
	depth int
}

func readOverviewAsk(payload map[string]any) (overviewAsk, error) {
	file, _ := payload["file"].(string)
	out := overviewAsk{file: strings.TrimSpace(file)}
	if out.file == "" {
		return overviewAsk{}, contract.Fail(contract.FailureInvalidInput,
			"%s: file is empty", CapabilityOverview)
	}
	if filepath.IsAbs(out.file) {
		// Same rule as readAsk, for the same reason: relative to the
		// repository root is what the capability promises, and an absolute
		// path would only work by accident on this machine.
		return overviewAsk{}, contract.Fail(contract.FailureInvalidInput,
			"%s: file %q must be relative to the repository root", CapabilityOverview, out.file)
	}
	if n, ok := whole(payload["depth"]); ok {
		if n < 0 {
			return overviewAsk{}, contract.Fail(contract.FailureInvalidInput,
				"%s: depth must not be negative, got %d", CapabilityOverview, n)
		}
		out.depth = n
	}
	return out, nil
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
	found, note, err := r.symbolAt(ctx, c, root, a)
	if err != nil {
		return nil, notes, err
	}
	notes = append(notes, note)
	switch k {
	case kindDefinition:
		return r.locationsFrom(root, []symbol{found}, a), notes, nil
	case kindReferences:
		out, err := r.referencing(ctx, c, a, found)
		return out, notes, err
	default:
		out, err := r.findImplementations(ctx, c, root, a, found)
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
	c.wireMu.Lock()
	already := c.active == root
	previous = c.active
	c.wireMu.Unlock()
	if already {
		return false, "", nil
	}
	if _, err := r.call(ctx, c, "activate_project", map[string]any{"project": root}); err != nil {
		c.wireMu.Lock()
		c.active = ""
		c.wireMu.Unlock()
		return false, previous, err
	}
	c.wireMu.Lock()
	c.active = root
	c.wireMu.Unlock()
	return previous != "", previous, nil
}

// overviewEntry is one answer to symbol.overview: what get_symbols_overview
// establishes (name, nesting) plus what only find_symbol and a local read of
// the source line can recover -- a real line, and the column neither Serena
// tool ever reports.
type overviewEntry struct {
	name    string
	kind    string
	parent  string
	line    int
	endLine int
	column  int
}

// maxConcurrentSymbolLookups bounds how many find_symbol calls run at once
// for one symbol.overview commission. Unbounded, measured against this
// repository's own largest file (62 top-level symbols): 232ms, no worse than
// firing 20 at once -- Serena, over gopls, absorbed that width on one file
// without degrading. This bound exists for the file this repository does not
// have: hundreds of symbols, where an unbounded fan-out would open hundreds
// of requests against one endpoint at once instead of racing a fixed pool
// through them. 16 is arbitrary within "comfortably above what this
// repository ever asks for, comfortably below whatever would actually stress
// a shared endpoint" -- not a number this repository's own files can settle
// either side of.
const maxConcurrentSymbolLookups = 16

// errNameNotLocated marks one symbol whose own name is written nowhere in the
// range its language server reported for it.
//
// It is deliberately not a contract failure. Serena answered, and every other
// symbol in the same file answered with it: one name that cannot be placed is
// one entry missing, not a provider outage. Binning it as `unavailable` is what
// took a 21-symbol Rust file offline over a single unplaceable name -- and
// demoted Serena's health for the whole repository on the way past.
var errNameNotLocated = errors.New("symbol name not found inside its reported range")

// locateAll turns the bare names get_symbols_overview reported into entries
// with a real line and column, by asking find_symbol once per name -- the
// only call in this exchange that returns a location at all -- capped at
// maxConcurrentSymbolLookups in flight together.
//
// It assumes c.mu is held, same as every exchange with this endpoint: these
// are read-only calls against the project overview already activated, and
// nothing else may retarget Serena while they are outstanding.
func (r *Runner) locateAll(ctx context.Context, c *conn, root string, a overviewAsk, names []overviewName) ([]overviewEntry, int, error) {
	entries := make([]overviewEntry, len(names))
	errs := make([]error, len(names))

	sem := make(chan struct{}, maxConcurrentSymbolLookups)
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, n overviewName) {
			defer wg.Done()
			defer func() { <-sem }()
			entries[i], errs[i] = r.locateOne(ctx, c, root, a, n)
		}(i, n)
	}
	wg.Wait()

	out := make([]overviewEntry, 0, len(names))
	unplaceable := 0
	for i := range names {
		switch {
		case errors.Is(errs[i], errNameNotLocated):
			unplaceable++
		case errs[i] != nil:
			return nil, 0, errs[i]
		default:
			out = append(out, entries[i])
		}
	}
	return out, unplaceable, nil
}

// locateOne finds where one overview name actually sits: find_symbol gives
// its line and its own kind, a local read of that exact line recovers the
// column find_symbol never reports.
//
// max_matches asks for more than the one match expected, on purpose.
// Serena's own truncation at exactly the requested count would turn a
// genuine ambiguity into a parse failure instead of a named one -- the
// difference between "this ran out of room" and "this file has more than
// one name where get_symbols_overview already said there was one," which are
// not the same fact, and only the second is this adapter's to report.
func (r *Runner) locateOne(ctx context.Context, c *conn, root string, a overviewAsk, n overviewName) (overviewEntry, error) {
	raw, err := r.call(ctx, c, "find_symbol", map[string]any{
		"name_path_pattern": n.queryPath,
		"relative_path":     a.file,
		"max_matches":       5,
	})
	if err != nil {
		return overviewEntry{}, err
	}
	found, err := parseSymbols(raw)
	if err != nil {
		return overviewEntry{}, err
	}
	// find_symbol's own matcher is not anchored to the depth this overview
	// asked about: an unqualified pattern like "kind" matches every symbol
	// in the file named kind, at any nesting -- not only the one
	// get_symbols_overview actually reported at this depth. Measured live:
	// this file has both a top-level type named kind and an unrelated
	// field overviewEntry/kind, and find_symbol returns both for the
	// pattern "kind" unfiltered.
	//
	// A candidate whose own name_path exactly echoes the path just asked
	// for is the more faithful read, so narrow to those when at least one
	// exists. But narrowing can't be unconditional: get_symbols_overview
	// reports Go methods flat, with no receiver, while find_symbol's real
	// name_path for the same method is always receiver-qualified -- so a
	// method query never has an exact echo to narrow to. Measured live:
	// three unrelated types in one file each define String(), overview
	// reports all three as the bare name "String", and no candidate's
	// name_path is ever the bare string "String". That is a genuine
	// ambiguity this adapter cannot resolve from what overview told it,
	// not a missing symbol -- so an unnarrowed multi-match result falls
	// through to the ambiguous case below rather than being reported as
	// not found.
	if exact := slices.DeleteFunc(slices.Clone(found), func(s symbol) bool { return s.NamePath != n.queryPath }); len(exact) > 0 {
		found = exact
	}
	if len(found) == 0 {
		return overviewEntry{}, errNameNotLocated
	}
	if len(found) > 1 {
		return overviewEntry{}, contract.Fail(contract.FailureInvalidInput,
			"%s: %q matches %d symbols; serena's overview cannot be trusted to mean one of them",
			a.file, n.queryPath, len(found))
	}
	match := found[0]
	line := toContractLine(match.Location.StartLine)
	endLine := toContractLine(match.Location.EndLine)

	// The name is looked for across the reported range, not on its first line.
	// Which line actually carries the declaration is the language server's
	// choice, not Atenea's: gopls starts at the `func`, rust-analyzer starts at
	// the doc comment above it.
	span := endLine - line + 1
	if span < 1 {
		span = 1
	}
	if span > nameScanLines {
		span = nameScanLines
	}
	window, err := readLinesFrom(r, root, a.file, line, span)
	if err != nil {
		return overviewEntry{}, err
	}
	nameLine, column, ok := nameSiteIn(window, line, n.name)
	if !ok {
		return overviewEntry{}, errNameNotLocated
	}

	entry := overviewEntry{name: n.name, kind: match.Kind, parent: n.parent, line: nameLine, column: column}
	if endLine != nameLine {
		// A single-line symbol repeating its own start line here would be
		// noise, not information -- the caller already has it.
		entry.endLine = endLine
	}
	return entry, nil
}

// symbolAt turns the position the capability speaks into the symbol Serena
// speaks about.
//
// declarationAt tries first: one language-server request, position in and
// declaring symbol out, wherever that declaration actually lives. Measured
// against this repository: 0.04-0.19s whether the position sits on the
// declaration itself or on a call site in a different file, against 1.05s
// for a wildcard find_symbol search on the same query -- and the wildcard
// answer would still only be a name, not the guarantee that the position
// actually names it. That guarantee, and the cross-file reach, is why this
// goes first rather than staying the fallback.
//
// Serena's symbol search is the fallback, unchanged from before: it takes a
// bare leaf name and answers with every name path that ends in it, each with
// its range, and the position picks among them. It only ever finds a
// declaration that shares the query's own file, but it is what runs when
// declarationAt cannot answer -- an ambiguous regex, a position naming
// nothing resolvable, or a language server that does not implement the
// request. The overview tool was never an option for either path -- it
// reports names with no line numbers -- and that, like the wildcard cost
// above, was measured, not assumed.
//
// The returned note is not decoration: the caller named a position and gets an
// answer about a name, so the trace has to say which name that was, or the
// answer cannot be checked against the question.
func (r *Runner) symbolAt(ctx context.Context, c *conn, root string, a ask) (symbol, string, error) {
	if found, ok := r.declarationAt(ctx, c, root, a); ok {
		return found, definitionNote(a, found), nil
	}
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
	return found, definitionNote(a, found), nil
}

// declarationAt asks Serena's own "go to definition" directly: one request,
// answered from the language server's index rather than a name search, so it
// costs the same whether the declaration sits in the query's file or a
// different one.
//
// find_declaration's own interface has no line-and-column parameter, only a
// regex that must match the query's file exactly once -- declarationRegex
// anchors it to the caller's own source line to make that hold. The second
// return is false for anything that stops this from answering: a regex that
// is not unique, a position the language server cannot resolve, or a
// language server that does not implement the request at all. None of those
// are reported to the caller as a failure of their own -- symbolAt's
// same-file fallback answers instead, exactly as it always did.
func (r *Runner) declarationAt(ctx context.Context, c *conn, root string, a ask) (symbol, bool) {
	regex, err := declarationRegex(r, root, a)
	if err != nil {
		return symbol{}, false
	}
	raw, err := r.call(ctx, c, "find_declaration", map[string]any{
		"relative_path": a.file,
		"regex":         regex,
	})
	if err != nil {
		return symbol{}, false
	}
	found, err := parseSymbol(raw)
	if err != nil {
		return symbol{}, false
	}
	return found, true
}

// definitionNote records which name the position actually resolved to, the
// same way regardless of which of symbolAt's two lookups found it.
func definitionNote(a ask, found symbol) string {
	note := fmt.Sprintf("position %s:%d:%d names %q, which is symbol %s",
		a.file, a.line, a.column, a.identifier, found.NamePath)
	// The payload's name is declared a hint, so a mismatch is reported and not
	// refused: the position is the authority. An implementation that let the
	// hint override it would answer a different question than the one asked.
	if a.name != "" && a.name != a.identifier {
		note += fmt.Sprintf(" (the hint said %q; the position wins)", a.name)
	}
	return note
}

// referencing asks who uses the symbol.
//
// The answer comes back in a different shape from a symbol search -- path,
// then kind, then entries -- which is why it has a parser of its own rather
// than a flag on the first one.
//
// relative_path names the file where target is DECLARED, which symbolAt may
// now have resolved to a file other than the one the caller pointed at. The
// search itself is not restricted to that file -- Serena uses it only to
// locate target before asking the language server for every reference to it,
// project-wide -- but it does have to be the right file, or that first,
// cheap lookup finds nothing to search from. Measured: given the correct
// file, one call found references in two different files at once.
func (r *Runner) referencing(ctx context.Context, c *conn, a ask, target symbol) ([]location, error) {
	raw, err := r.call(ctx, c, "find_referencing_symbols", map[string]any{
		"name_path":     target.NamePath,
		"relative_path": target.Path,
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
func (r *Runner) findImplementations(ctx context.Context, c *conn, root string, a ask, target symbol) ([]location, error) {
	raw, err := r.call(ctx, c, "find_implementations", map[string]any{
		"name_path":     target.NamePath,
		"relative_path": target.Path,
	})
	if err != nil {
		return nil, err
	}
	// find_implementations answers in find_symbol's shape, not
	// find_referencing_symbols's: a flat array of symbols, one per
	// implementation, not entries nested path -> kind. "[]" for zero hits
	// unmarshals through parseSymbols untouched, so the empty answer needs
	// no special case here -- asking something concrete for its
	// implementations is a legitimate empty answer, not a failure.
	found, err := parseSymbols(raw)
	if err != nil {
		return nil, err
	}
	return withinScope(r.locationsFrom(root, found, a), a.scope), nil
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

// shapeOverview turns overview entries into the output symbol.overview
// declares, and checks them against it -- same discipline as shape, for the
// same reason: a capability whose declared shape is not enforced is a
// comment rather than a contract.
func shapeOverview(entries []overviewEntry, capability contract.Capability) (map[string]any, error) {
	records := make([]any, 0, len(entries))
	for _, e := range entries {
		record := map[string]any{
			"name":   e.name,
			"kind":   e.kind,
			"line":   e.line,
			"column": e.column,
		}
		if e.endLine != 0 {
			record["end_line"] = e.endLine
		}
		if e.parent != "" {
			record["parent"] = e.parent
		}
		records = append(records, record)
	}
	result := map[string]any{"symbols": records}
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
