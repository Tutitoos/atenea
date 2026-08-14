// Package ladygraph is the fifth adapter, and the first whose far side is a
// process Atenea itself launches and holds the pipes of.
//
// Every earlier client speaks over an address: omp and Claude Code as a
// binary invoked once per call, Serena as an MCP server behind a fixed URL,
// codebase-memory-mcp as a fresh process per call. A stdio MCP server has no
// address at all -- there is nothing to dial, only two pipes -- so it can
// only ever be reached by something that spawned it and kept them open. That
// something is internal/supervisor, extended with a stdio transport for
// exactly this adapter; this package only ever asks it for the live
// *mcpstdio.Session already attached to a process it is not this package's
// job to start, watch or restart.
//
// The graph itself is one global corpus, not one per repository. ladygraph
// indexes a whole workspace by hand (`ladygraph index --full`, ahead of
// time, never from inside Atenea) and publishes a single snapshot every
// reader shares by atomic generation; there is no per-repository index to
// warm or retarget the way Serena's active project has to be, which is why
// the declared instance policy is "shared" and only "shared" -- see
// internal/core's wiring for the refusal when a settings file asks for
// anything else.
//
// # The empty-graph trap
//
// A fresh `ladygraph serve` with no config scaffolds an empty config and
// publishes an empty graph, and answers every query with nothing --
// successfully, isError:false, zero counts. Measured against v0.5.1: this is
// the actual failure mode, not a hypothetical one, and it looks nothing like
// an error. Every one of the four capabilities below pays for one
// graph_status call before trusting any other answer (checkGraphReady), and
// an absent, not-"ready", zero-count, or (when a repository is in play)
// unregistered graph becomes contract.FailureUnavailable -- never a
// contract.VerdictOK built on top of nothing.
//
// This is deliberately kept apart from a tool answering a real, legitimate
// empty result: measured on the same corpus, find_cross_repo_consumers
// correctly returns zero rows for a workspace whose repositories are
// genuinely decoupled, and that is VerdictOK with an empty list. The guard
// below asks one question -- does a real graph exist -- and never asks
// whether any one query happened to match nothing; conflating the two is
// exactly the mistake this package exists to not make, so they are kept in
// separate functions (checkGraphReady never sees a query's own results, and
// no run* function ever reinterprets an empty results array as anything but
// success) rather than one that could drift into conflating them.
//
// # The vocabulary gap
//
// The capability declarations were written from the design; ladygraph's own
// wire was measured separately, and the two do not use the same words for
// the same fact. consumer_repository_key is a KEY ("repository:backend"),
// not a bare name -- there is no consumer_repository_name field -- so the
// declared "repository" output strips the prefix (repositoryNameFromKey).
// get_unresolved_references carries no line number, only start_offset, a
// byte offset, so symbol.unresolved's declared output is "offset", not
// "line": synthesizing a line by reading the file back would be a second,
// unverified guess about a position ladygraph itself never reported.
// find_cross_repo_consumers classifies each row with "category", whose real
// values (exact_symbol, package, candidate, unresolved) are kept verbatim as
// the declared "resolution" rather than normalized to the coverage
// envelope's own, differently-spelled buckets (exact, package_level,
// candidate, unresolved_related) -- and a package-level row proves the
// consumer depends on the provider PACKAGE, never the symbol, so it is never
// folded into exact_symbol by anything in this file, and its file path is
// left out of the record entirely rather than invented, because ladygraph
// itself never sets one for that row.
//
// # Position resolution inside symbol.consumers
//
// Atenea names a symbol by position -- file, line, column -- and
// symbol.consumers keeps that shape: file and line stay required, and there
// is no stable_key input, on purpose. A capability whose required input
// only one provider could ever produce is not a capability, it is that
// provider's tool passthrough wearing a funnel costume: no language server
// and no future provider could ever mint a ladygraph stable_key, so
// accepting one as input would make this capability permanently
// unimplementable by anyone else, and unreachable for any caller holding
// only a file and a line, which is what callers actually hold.
//
// find_cross_repo_consumers is keyed by stable_key alone, so this adapter
// pays the resolution cost the capability's own shape refuses to expose.
// get_file_outline lists every declaration in one file with its
// [start_line, end_line] span and its stable_key, and runConsumers (through
// resolvePosition) walks that list for the declaration the requested line
// actually falls inside of. Containment can nest -- a method inside a
// class -- so the innermost (smallest) span wins; the declared optional
// "name" input, when it names exactly one candidate or matches a
// candidate's qualified_name exactly (ladygraph disambiguates repeated
// names with suffixes like "reservas#2"), settles it outright instead.
//
// Three honesty requirements follow from resolving rather than refusing:
//   - a position inside no declaration's span is contract.FailureNotFound,
//     not an empty consumers list. A question with no subject is not a
//     symbol with no consumers, and answering the latter would claim a
//     search happened that never did.
//   - several declarations contain the position and "name" does not
//     resolve it: the innermost still wins, but the candidates passed over
//     travel back as a contract.Discovery rather than disappearing.
//   - get_file_outline itself failing to answer -- a dead call, an
//     unreadable payload -- is contract.FailureUnavailable, never
//     FailureNotFound: nothing has been searched yet, so nothing can
//     honestly be reported missing.
//
// The resolved stable_key is never cached across calls. It is only stable
// within the generation that minted it, and answering confidently from a
// key resolved against a generation the graph has since rotated past is
// exactly the failure the empty-graph trap above was measured against, so
// every symbol.consumers call pays the resolution again rather than trust
// one it resolved a moment ago.
//
// # What this package does not do
//
// It does not build the graph: no `ladygraph index`, no requires_index probe
// that tries to fix what it finds missing. It only reads. And it never
// reports Outcome.ToolVersion: mcpstdio.Session exposes no serverInfo getter
// by design, and none of the four tools' own payloads carries a version
// string either, so an upgrade on disk is invisible to this adapter the same
// honest way Serena is when its own server declines to introduce itself.
package ladygraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The capabilities this adapter answers, and the implementation ids the
// shipped catalog gives them. Ids are separate from capability ids because a
// capability is the "what" and an implementation is the "who": another
// provider may answer the same four capabilities tomorrow.
const (
	CapabilityConsumers   = "symbol.consumers"
	CapabilityGet         = "symbol.get"
	CapabilityUnresolved  = "symbol.unresolved"
	CapabilityGraphStatus = "graph.status"

	ImplConsumers  = "ladygraph.cross_repo_consumers"
	ImplGet        = "ladygraph.get"
	ImplUnresolved = "ladygraph.unresolved_references"
	ImplStatus     = "ladygraph.status"
)

// The MCP tool names behind each capability, on ladygraph's own far side.
// get_file_outline answers no capability of its own -- symbol.consumers
// calls it internally, through resolvePosition, to turn a position into
// the stable_key find_cross_repo_consumers actually requires.
const (
	toolConsumers  = "find_cross_repo_consumers"
	toolGet        = "get_symbol"
	toolUnresolved = "get_unresolved_references"
	toolStatus     = "graph_status"
	toolOutline    = "get_file_outline"
)

// repositoryKeyPrefix is how ladygraph addresses a repository inside a key
// such as consumer_repository_key. There is no bare-name field beside it, so
// stripping this is the only way to answer the declared "repository" output
// in atenea's own vocabulary rather than ladygraph's addressing scheme.
const repositoryKeyPrefix = "repository:"

// DefaultTimeout caps one call. ladygraph opens a published graph snapshot
// and walks it, which sits at the same class of cost Serena and
// codebase-memory already pay for a cold cache -- slow long before it is
// stuck -- so this matches their own ceiling rather than inventing a new
// one.
const DefaultTimeout = 90 * time.Second

// DefaultImplementations is what the adapter answers for. It is a function
// and not a package-level slice because a caller that appended to a shared
// one would quietly change what every other Atenea in this process serves.
func DefaultImplementations() []string {
	return []string{ImplConsumers, ImplGet, ImplUnresolved, ImplStatus}
}

// Options configure the adapter.
type Options struct {
	// Implementations the adapter answers for.
	Implementations []string
	// Timeout caps one call.
	Timeout time.Duration
	// Session returns the live MCP session for the supervised ladygraph
	// child. It is a function, not a stored value, because the process
	// behind it may not exist yet when New runs (on_demand lifecycle) and
	// may be replaced by a restart later: every call asks again rather than
	// trusting a session it cached itself.
	Session func(ctx context.Context) (*mcpstdio.Session, error)
}

// Runner is the ladygraph far side of contract.Runner.
type Runner struct {
	implementations []string
	timeout         time.Duration
	session         func(ctx context.Context) (*mcpstdio.Session, error)
}

// New validates the options and returns the adapter.
func New(opts Options) (*Runner, error) {
	if opts.Session == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"ladygraph adapter: session is required -- a stdio server has no address to dial without one")
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"ladygraph adapter: timeout must not be negative, got %s", timeout)
	}
	impls := slices.Clone(opts.Implementations)
	if impls == nil {
		impls = DefaultImplementations()
	}
	slices.Sort(impls)
	return &Runner{
		implementations: impls,
		timeout:         timeout,
		session:         opts.Session,
	}, nil
}

// ID names the runner on the status screen.
func (r *Runner) ID() string { return "ladygraph" }

// Serves reports whether this adapter answers for that implementation.
func (r *Runner) Serves(implementationID string) bool {
	return slices.Contains(r.implementations, implementationID)
}

// Implementations lists every implementation this runner declares itself
// the far side of.
func (r *Runner) Implementations() []string { return slices.Clone(r.implementations) }

// Capabilities lists what this adapter's Run can actually dispatch, so a
// settings file naming an implementation it has no case for is refused at
// load rather than at the call.
func (r *Runner) Capabilities() []string {
	return []string{CapabilityConsumers, CapabilityGet, CapabilityUnresolved, CapabilityGraphStatus}
}

// Run executes one step.
func (r *Runner) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	if err := req.Validate(); err != nil {
		return contract.Outcome{}, err
	}
	if !r.Serves(req.Implementation.ID) {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"ladygraph adapter does not serve implementation %s", req.Implementation.ID)
	}

	started := time.Now()
	call, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	sess, err := r.session(call)
	if err != nil {
		return contract.Outcome{}, r.failureFor(err, call)
	}
	if err := sess.Initialize(call); err != nil {
		return contract.Outcome{}, r.failureFor(err, call)
	}

	// Every capability pays for one graph_status call before anything else
	// is trusted: see the package doc comment and checkGraphReady for why.
	status, err := r.fetchStatus(call, sess)
	if err != nil {
		return contract.Outcome{}, r.failureFor(err, call)
	}
	repository, err := r.repositoryInPlay(req)
	if err != nil {
		return contract.Outcome{}, err
	}
	if err := checkGraphReady(status, repository); err != nil {
		return contract.Outcome{}, err
	}

	var result map[string]any
	var notes []string
	switch req.Capability.ID {
	case CapabilityConsumers:
		result, notes, err = r.runConsumers(call, sess, status, req)
	case CapabilityGet:
		result, notes, err = r.runGet(call, sess, req)
	case CapabilityUnresolved:
		result, notes, err = r.runUnresolved(call, sess, req)
	case CapabilityGraphStatus:
		result, notes, err = r.runGraphStatus(status, req)
	default:
		return contract.Outcome{}, contract.Fail(contract.FailureNotFound,
			"ladygraph adapter has no implementation of %s", req.Capability.ID)
	}
	if err != nil {
		return contract.Outcome{}, r.failureFor(err, call)
	}

	outcome := contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		// No memory figure: like Serena, ladygraph runs in a process the
		// supervisor owns, not one this call spawned, so there is nothing
		// here to weigh.
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

// repositoryInPlay resolves which repository the shared guard checks
// against, and it is not the same field for every capability.
//
// The three symbol capabilities are always scoped to req.Repository -- the
// funnel's own binding, present on every RunRequest by construction -- so an
// unregistered one is a provider that cannot answer FOR that repository.
// graph.status declares no input at all and stays fully repository-agnostic
// -- it has no structural repository to check and nothing in its payload to
// read, so an unscoped call is satisfied by the graph existing at all.
func (r *Runner) repositoryInPlay(req contract.RunRequest) (string, error) {
	if req.Capability.ID == CapabilityGraphStatus {
		return "", nil
	}
	root, err := filepath.Abs(req.Repository.Path)
	if err != nil {
		return "", contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %q: %v", req.Repository.ID, req.Repository.Path, err)
	}
	return root, nil
}

// statusResult is graph_status's single-object "results", measured live
// against v0.5.1: unlike the other three tools this is one object, not a
// list. storage and worker are deliberately not decoded here: both read
// "not_applicable" on a healthy server that only ever reads its own
// published snapshot rather than opening the database itself, so keying
// readiness on them would be a permanent false negative, not a real signal.
type statusResult struct {
	Status              string                `json:"status"`
	SnapshotID          int                   `json:"snapshot_id"`
	SnapshotBuiltAt     string                `json:"snapshot_built_at"`
	Symbols             int                   `json:"symbols"`
	Edges               int                   `json:"edges"`
	Files               int                   `json:"files"`
	Repositories        int                   `json:"repositories"`
	Unresolved          int                   `json:"unresolved"`
	RepositoryFreshness []repositoryFreshness `json:"repository_freshness"`
}

// repositoryFreshness is one row of graph_status's own repository_freshness
// list. The wire carries more per row -- languages, moved, indexed/current
// branch and commit, the strongest staleness signals this provider
// produces -- but this adapter only ever matches a repository by its path
// (see findRepositoryFreshness) and no capability here surfaces the rest:
// graph.status stays the repository-agnostic scalars its declaration
// promises, nothing bolted on, and a real question about drift is a
// capability of its own the day one is declared against it, decoding what
// it needs fresh rather than inheriting unused fields from this one.
type repositoryFreshness struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// statusAnswer is graph_status's envelope, narrowed to the one field this
// adapter reads out of it.
type statusAnswer struct {
	Results *statusResult `json:"results"`
}

// fetchStatus calls graph_status and decodes its answer. It is the one call
// every capability pays before trusting any other tool -- see
// checkGraphReady for what "trust" means here -- and graph.status itself
// reuses this very same decoded value as its own answer (runGraphStatus)
// rather than calling the tool a second time.
func (r *Runner) fetchStatus(ctx context.Context, sess *mcpstdio.Session) (*statusResult, error) {
	text, err := sess.Call(ctx, toolStatus, map[string]any{})
	if err != nil {
		return nil, err
	}
	var answer statusAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, fmt.Errorf("ladygraph %s: unreadable answer: %w", toolStatus, err)
	}
	return answer.Results, nil
}

// checkGraphReady is the guard every capability shares: before any tool's
// answer is trusted, the graph itself has to be real. ladygraph's own
// failure mode, measured against v0.5.1, is not an error -- it is
// isError:false with every count at zero, an empty config's empty answer to
// any question at all -- so this inspects the decoded payload rather than
// sniffing text, and refuses before a caller ever sees a VerdictOK built on
// top of nothing.
//
// repository is empty exactly when the capability has none to check (see
// repositoryInPlay): a call against an unregistered repository is a
// provider that cannot answer for it, not a legitimate empty result, and an
// unscoped graph.status call is satisfied by the graph existing at all.
func checkGraphReady(status *statusResult, repository string) error {
	if status == nil || status.Status != "ready" {
		return contract.Fail(contract.FailureUnavailable,
			"ladygraph has no published graph to answer from (status %q)", statusLabel(status))
	}
	if status.Symbols == 0 && status.Edges == 0 && status.Files == 0 {
		return contract.Fail(contract.FailureUnavailable, "ladygraph's published graph is empty")
	}
	if repository == "" {
		return nil
	}
	if _, ok := findRepositoryFreshness(status.RepositoryFreshness, repository); !ok {
		return contract.Fail(contract.FailureUnavailable,
			"ladygraph's published graph does not include repository %s", repository)
	}
	return nil
}

// statusLabel names status for a refusal message without a nil check at
// every call site.
func statusLabel(status *statusResult) string {
	if status == nil {
		return "absent"
	}
	if status.Status == "" {
		return "unknown"
	}
	return status.Status
}

// findRepositoryFreshness looks a repository up by path. Every caller left
// in this package resolves req.Repository.Path with filepath.Abs before
// reaching here -- graph.status's own optional "repository" payload field
// that once could have carried a bare name instead is gone -- so path is
// the only shape this ever has to match.
func findRepositoryFreshness(entries []repositoryFreshness, repositoryPath string) (repositoryFreshness, bool) {
	for _, fresh := range entries {
		if sameRepoPath(fresh.Path, repositoryPath) {
			return fresh, true
		}
	}
	return repositoryFreshness{}, false
}

// sameRepoPath compares two filesystem paths the way this adapter must: a
// snapshot's repository_freshness path and a caller's repository.Path are
// each somebody else's absolute path, and either may pass through a symlink
// the other resolved differently.
func sameRepoPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return normalizeRepoPath(a) == normalizeRepoPath(b)
}

// normalizeRepoPath resolves p as far as the filesystem will confirm.
// EvalSymlinks is tried and kept only when it succeeds -- a path that no
// longer exists (a stale snapshot entry, a repository moved since indexing)
// still deserves a plain Abs/Clean comparison rather than being silently
// disqualified because the filesystem could not confirm it.
func normalizeRepoPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return filepath.Clean(abs)
}

// consumerRecord is one row of find_cross_repo_consumers' own
// CrossRepoConsumerSummary (internal/mcp/tools/find_cross_repo_consumers.go
// in the ladygraph source, v0.5.1), narrowed to the fields this adapter
// maps into the declared output. category, confidence and
// consumer_repository_key are the only fields the far side always sets;
// consumer_file_path in particular is absent for a package-level row -- see
// runConsumers.
type consumerRecord struct {
	Category              string `json:"category"`
	ConsumerRepositoryKey string `json:"consumer_repository_key"`
	ConsumerFilePath      string `json:"consumer_file_path"`
}

// consumerAnswer is find_cross_repo_consumers' envelope.
type consumerAnswer struct {
	Results    []consumerRecord `json:"results"`
	Truncated  bool             `json:"truncated"`
	NextCursor string           `json:"next_cursor"`
}

// consumerResolutions is the closed set symbol.consumers' own declared
// "resolution" input and find_cross_repo_consumers' own "category" field
// share -- see the package doc comment's vocabulary gap for why the wire
// name and the declared name differ while the four values themselves do
// not.
var consumerResolutions = []string{"exact_symbol", "package", "candidate", "unresolved"}

// runConsumers answers symbol.consumers.
//
// The declared input is position-first (file/line/column), matching
// symbol.references; ladygraph names a symbol by stable_key instead, so
// this resolves the position to one via resolvePosition before ever calling
// find_cross_repo_consumers -- see the package doc comment for why the
// capability itself takes no stable_key input, and what the resolution
// costs.
//
// stable_key is the only argument ever sent to the tool: repo, language,
// limit and cursor are real arguments on find_cross_repo_consumers' own
// schema, but the capability never declared them as inputs, so there is no
// payload key to read them from (checkPayload refuses any the capability
// does not declare before Run is ever reached). resolution is declared, but
// the tool has no matching filter argument to forward it to -- this is the
// one place that input can honestly change the output, so it narrows the
// decoded results client-side instead.
func (r *Runner) runConsumers(ctx context.Context, sess *mcpstdio.Session, status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	file, ok := stringAt(req.Payload, "file")
	if !ok || file == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "ladygraph symbol.consumers: file is required")
	}
	line, ok := intAt(req.Payload, "line")
	if !ok {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "ladygraph symbol.consumers: line is required")
	}
	// column is required by the declared capability -- already enforced by
	// req.Validate() before Run ever dispatched here -- but get_file_outline's
	// own declarations carry no column, only [start_line, end_line], so this
	// adapter's span-based resolution never reads it back out.
	name, _ := stringAt(req.Payload, "name")
	resolution, _ := stringAt(req.Payload, "resolution")
	if resolution != "" && !slices.Contains(consumerResolutions, resolution) {
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"ladygraph symbol.consumers: resolution %q is not one of %s", resolution, strings.Join(consumerResolutions, ", "))
	}

	root, err := filepath.Abs(req.Repository.Path)
	if err != nil {
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %q: %v", req.Repository.ID, req.Repository.Path, err)
	}
	// get_file_outline is addressed in ladygraph's own vocabulary, not
	// atenea's repository id: the shared guard already matched root against
	// repository_freshness to reach this dispatch, so that entry's own Name
	// is reused here rather than assuming the two ids happen to agree.
	ladygraphRepo := req.Repository.ID
	if fresh, ok := findRepositoryFreshness(status.RepositoryFreshness, root); ok && fresh.Name != "" {
		ladygraphRepo = fresh.Name
	}

	stableKey, notes, err := r.resolvePosition(ctx, sess, ladygraphRepo, file, line, name)
	if err != nil {
		return nil, nil, err
	}

	text, err := sess.Call(ctx, toolConsumers, map[string]any{"stable_key": stableKey})
	if err != nil {
		return nil, nil, err
	}
	var answer consumerAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, fmt.Errorf("ladygraph %s: unreadable answer: %w", toolConsumers, err)
	}

	records := make([]any, 0, len(answer.Results))
	for _, row := range answer.Results {
		if resolution != "" && row.Category != resolution {
			continue
		}
		record := map[string]any{
			"repository": repositoryNameFromKey(row.ConsumerRepositoryKey),
			"resolution": row.Category,
		}
		// A package-level row proves the consumer depends on the provider
		// PACKAGE, never the symbol -- ladygraph does not set
		// consumer_file_path for one, and this emits the row without
		// inventing a path for it rather than dropping it.
		if row.ConsumerFilePath != "" {
			record["path"] = row.ConsumerFilePath
		}
		records = append(records, record)
	}
	result := map[string]any{"consumers": records}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}

	notes = append(notes, fmt.Sprintf("ladygraph answered symbol.consumers for %s with %d consumer(s)",
		req.Repository.ID, len(records)))
	if answer.Truncated {
		notes = append(notes, fmt.Sprintf(
			"ladygraph truncated this answer; more consumers exist past cursor %q", answer.NextCursor))
	}
	return result, notes, nil
}

// outlineDeclaration is one row of get_file_outline: a declaration's span
// and the stable_key that names it, measured live against v0.5.1.
type outlineDeclaration struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Kind          string `json:"kind"`
	StableKey     string `json:"stable_key"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
}

// outlineAnswer is get_file_outline's envelope. Unlike every other tool
// here, its "results" is a single object describing the file -- path,
// repository, languages, packages, counts -- and the declarations hang off
// its "symbols" field. Decoding it as a list of rows, the shape the
// neighbors use, costs nothing at compile time and fails on the wire with
// "cannot unmarshal object into ... []outlineDeclaration": measured against
// v0.5.1, and only caught by asking the real server.
type outlineAnswer struct {
	Results struct {
		Symbols []outlineDeclaration `json:"symbols"`
	} `json:"results"`
}

// resolvePosition is the detour symbol.consumers pays that no other
// capability here does -- see the package doc comment for why the
// capability's own shape stays position-first rather than exposing
// stable_key as input.
//
// Never cached: a stable_key is only stable within the generation that
// minted it, and answering confidently from a key resolved against a
// generation the graph has since rotated past is exactly the failure this
// whole provider was measured against, so every call pays this again.
func (r *Runner) resolvePosition(ctx context.Context, sess *mcpstdio.Session, repository, file string, line int, name string) (string, []string, error) {
	text, err := sess.Call(ctx, toolOutline, map[string]any{"repository": repository, "path": file})
	if err != nil {
		// The extra call is not free and must not be invisible: a failure
		// reaching the outline at all belongs in the empty-graph guard's own
		// bin. Nothing has been searched yet, so nothing can honestly be
		// reported missing -- this is deliberately pre-classified rather than
		// left for failureFor's NOT_FOUND sniffing, which would misread
		// ladygraph refusing to produce an outline at all as a resolved
		// "not found".
		return "", nil, contract.Fail(contract.FailureUnavailable,
			"ladygraph symbol.consumers: could not read %s's outline to resolve %s:%d: %v", repository, file, line, err)
	}
	var answer outlineAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return "", nil, contract.Fail(contract.FailureUnavailable,
			"ladygraph symbol.consumers: unreadable outline for %s: %v", file, err)
	}

	var candidates []outlineDeclaration
	for _, decl := range answer.Results.Symbols {
		if line >= decl.StartLine && line <= decl.EndLine {
			candidates = append(candidates, decl)
		}
	}
	if len(candidates) == 0 {
		// A position that names no declaration is a question with no
		// subject, not a symbol with no consumers: an empty consumers list
		// would claim this position was searched and answered, when nothing
		// in the outline ever matched it.
		return "", nil, contract.Fail(contract.FailureNotFound,
			"ladygraph symbol.consumers: %s:%d names no declaration in %s's outline", file, line, repository)
	}

	// name, when it disambiguates outright, wins over span alone: ladygraph
	// disambiguates repeated declaration names with qualified_name suffixes
	// like "reservas#2", so an exact qualified_name hit is preferred to a
	// bare name match among several same-named candidates.
	if name != "" {
		var byName []outlineDeclaration
		for _, decl := range candidates {
			if decl.Name == name {
				byName = append(byName, decl)
			}
		}
		for _, decl := range byName {
			if decl.QualifiedName == name {
				return decl.StableKey, nil, nil
			}
		}
		if len(byName) == 1 {
			return byName[0].StableKey, nil, nil
		}
		if len(byName) > 0 {
			candidates = byName
		}
	}

	innermost := narrowestSpan(candidates)
	var notes []string
	if len(candidates) > 1 {
		var passedOver []string
		for _, decl := range candidates {
			if decl.StableKey == innermost.StableKey {
				continue
			}
			passedOver = append(passedOver,
				fmt.Sprintf("%s (%s, lines %d-%d)", decl.Name, decl.Kind, decl.StartLine, decl.EndLine))
		}
		if len(passedOver) > 0 {
			// Several declarations contained the position and name did not
			// settle it: the innermost still wins, but the ones passed over
			// travel back as a discovery rather than disappearing -- never a
			// silent pick among genuinely ambiguous candidates.
			notes = append(notes, fmt.Sprintf(
				"%s:%d fell inside %d declarations; resolved to the innermost, %s, over %s",
				file, line, len(candidates), innermost.Name, strings.Join(passedOver, ", ")))
		}
	}
	return innermost.StableKey, notes, nil
}

// narrowestSpan returns the declaration with the smallest [start_line,
// end_line] span -- the innermost one containing a position several
// declarations nest around, a method inside a class rather than the class
// itself.
func narrowestSpan(candidates []outlineDeclaration) outlineDeclaration {
	best := candidates[0]
	bestSpan := best.EndLine - best.StartLine
	for _, decl := range candidates[1:] {
		if span := decl.EndLine - decl.StartLine; span < bestSpan {
			best = decl
			bestSpan = span
		}
	}
	return best
}

// symbolRecord is get_symbol's answer, narrowed to the four fields
// symbol.get declares: path, line, name and kind all come back required.
type symbolRecord struct {
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
}

// getSymbolAnswer is get_symbol's envelope. Unlike every other tool here its
// "results" is a single object, not a list -- confirmed live against v0.5.1
// -- and nil exactly when the key names nothing the far side could resolve.
type getSymbolAnswer struct {
	Results *symbolRecord `json:"results"`
}

// runGet answers symbol.get: the identity retrieval a position-first
// capability cannot do.
//
// stable_key is the only declared input -- there is no response_format
// here, unlike an earlier draft of this adapter assumed; the shipped
// capability never grew one, and forwarding an undeclared field would be
// dead code, since checkPayload refuses any payload key the capability does
// not declare before Run is ever reached.
//
// An unknown stable_key does not come back as an empty "results" -- it
// comes back isError:true, SYMBOL_NOT_FOUND, which failureFor bins as
// contract.FailureNotFound before Run ever reaches shaping. The nil-Results
// branch below is kept anyway as the honest reading of what the tool's own
// schema allows, not as the path this adapter expects a caller to hit.
func (r *Runner) runGet(ctx context.Context, sess *mcpstdio.Session, req contract.RunRequest) (map[string]any, []string, error) {
	stableKey, ok := stringAt(req.Payload, "stable_key")
	if !ok || stableKey == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "ladygraph symbol.get: stable_key is required")
	}

	text, err := sess.Call(ctx, toolGet, map[string]any{"stable_key": stableKey})
	if err != nil {
		return nil, nil, err
	}
	var answer getSymbolAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, fmt.Errorf("ladygraph %s: unreadable answer: %w", toolGet, err)
	}

	records := []any{}
	if answer.Results != nil {
		records = append(records, map[string]any{
			"path": answer.Results.FilePath,
			"line": answer.Results.StartLine,
			"name": answer.Results.Name,
			"kind": answer.Results.Kind,
		})
	}
	result := map[string]any{"symbol": records}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}

	notes := []string{fmt.Sprintf("ladygraph answered symbol.get for %s with %d row", req.Repository.ID, len(records))}
	return result, notes, nil
}

// unresolvedRecord is one row of get_unresolved_references. There is no
// line number here -- only start_offset, a byte offset -- and
// requested_package is absent for a reason that names no package at all.
type unresolvedRecord struct {
	FilePath         string `json:"file_path"`
	StartOffset      int    `json:"start_offset"`
	Reason           string `json:"reason"`
	RequestedPackage string `json:"requested_package"`
}

// unresolvedAnswer is get_unresolved_references' envelope.
type unresolvedAnswer struct {
	Results    []unresolvedRecord `json:"results"`
	Truncated  bool               `json:"truncated"`
	NextCursor string             `json:"next_cursor"`
}

// runUnresolved answers symbol.unresolved. get_unresolved_references
// carries no line number, only start_offset -- so the declared output field
// is "offset", not "line": synthesizing a line by reading the file back
// would be inventing a position ladygraph itself never reported.
//
// reason, requested_package and limit are the only declared inputs -- repo,
// package, requested_symbol, language and cursor are real arguments on the
// tool's own schema but never made it into the capability's, so there is no
// payload key to read them from: checkPayload refuses any field the
// capability does not declare before Run is ever reached.
func (r *Runner) runUnresolved(ctx context.Context, sess *mcpstdio.Session, req contract.RunRequest) (map[string]any, []string, error) {
	args := map[string]any{}
	if reason, ok := stringAt(req.Payload, "reason"); ok {
		args["reason"] = reason
	}
	if requestedPackage, ok := stringAt(req.Payload, "requested_package"); ok {
		args["requested_package"] = requestedPackage
	}
	if limit, ok := intAt(req.Payload, "limit"); ok {
		args["limit"] = limit
	}

	text, err := sess.Call(ctx, toolUnresolved, args)
	if err != nil {
		return nil, nil, err
	}
	var answer unresolvedAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, fmt.Errorf("ladygraph %s: unreadable answer: %w", toolUnresolved, err)
	}

	records := make([]any, 0, len(answer.Results))
	for _, row := range answer.Results {
		record := map[string]any{
			"path":   row.FilePath,
			"offset": row.StartOffset,
			"reason": row.Reason,
		}
		if row.RequestedPackage != "" {
			record["requested_package"] = row.RequestedPackage
		}
		records = append(records, record)
	}
	result := map[string]any{"unresolved": records}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}

	notes := []string{fmt.Sprintf("ladygraph answered symbol.unresolved for %s with %d row(s)",
		req.Repository.ID, len(records))}
	if answer.Truncated {
		notes = append(notes, fmt.Sprintf(
			"ladygraph truncated this answer; more unresolved references exist past cursor %q", answer.NextCursor))
	}
	return result, notes, nil
}

// runGraphStatus shapes graph_status's already-fetched answer into
// graph.status's declared output. It never calls the tool a second time:
// Run already paid for one graph_status call to pass the shared guard, and
// that decoded payload IS this capability's answer, not a fact merely used
// to gate it.
//
// The capability declares zero inputs, deliberately: an input that cannot
// change the declared output is not a filter, it is a schema that promises
// narrowing and delivers none. repository_freshness is real and
// load-bearing -- ProbeIndex reads it for every repository's index
// detection -- but that is where it stays; this answer never projects it.
func (r *Runner) runGraphStatus(status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	record := map[string]any{
		"status":            status.Status,
		"snapshot_id":       status.SnapshotID,
		"snapshot_built_at": status.SnapshotBuiltAt,
		"symbols":           status.Symbols,
		"edges":             status.Edges,
		"files":             status.Files,
		"repositories":      status.Repositories,
		"unresolved":        status.Unresolved,
	}
	result := map[string]any{"snapshot": []any{record}}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}

	notes := []string{fmt.Sprintf("ladygraph answered graph.status: %d symbol(s), %d edge(s) across %d repositories",
		status.Symbols, status.Edges, status.Repositories)}
	return result, notes, nil
}

// ProbeIndex asks ladygraph whether root already has a place in the
// published graph, without asking it to build one.
//
// It mirrors codebasememory's own three-way contract exactly: a working
// probe with a definite verdict returns (ready, hint, nil); only the probe
// itself failing to reach a verdict returns a non-nil err, because a caller
// must never correct indexed_by on a guess.
func (r *Runner) ProbeIndex(ctx context.Context, root string) (bool, string, error) {
	sess, err := r.session(ctx)
	if err != nil {
		return false, "", r.failureFor(err, ctx)
	}
	if err := sess.Initialize(ctx); err != nil {
		return false, "", r.failureFor(err, ctx)
	}
	status, err := r.fetchStatus(ctx, sess)
	if err != nil {
		return false, "", r.failureFor(err, ctx)
	}
	repoAbs, err := filepath.Abs(root)
	if err != nil {
		return false, "", contract.Fail(contract.FailureInvalidInput, "ladygraph probe: path %q: %v", root, err)
	}
	switch probeErr := checkGraphReady(status, repoAbs); {
	case probeErr == nil:
		return true, "", nil
	case contract.KindOf(probeErr) == contract.FailureUnavailable:
		return false, probeErr.Error(), nil
	default:
		return false, "", probeErr
	}
}

// repositoryNameFromKey strips ladygraph's own "repository:" key prefix.
// consumer_repository_key is a KEY ("repository:backend"), not a bare name
// -- there is no consumer_repository_name field on CrossRepoConsumerSummary
// -- and the declared "repository" output is atenea's existing vocabulary
// (see symbol.references), so the prefix belongs to ladygraph's own
// addressing scheme, not to the answer this adapter hands back.
func repositoryNameFromKey(key string) string {
	return strings.TrimPrefix(key, repositoryKeyPrefix)
}

// failureFor sorts what went wrong into the shared bins.
//
// An already-binned failure -- ValidateOutput, resolvePosition's own
// not-found and unavailable refusals, a required-field refusal -- travels
// unchanged: re-reading its own text would be the adapter guessing about
// itself. ladygraph's own
// isError text is the exception worth a rule of its own: every refusal
// measured against v0.5.1 takes the form "UPPERCASE_CODE: message"
// (SYMBOL_NOT_FOUND, ...), and a caller passing a stale or mistyped
// stable_key must land in contract.FailureNotFound, never
// contract.FailureUnavailable -- that bin drives provider health to down and
// pulls ladygraph out of the funnel over one bad key, not one dead provider.
func (r *Runner) failureFor(err error, ctx context.Context) *contract.Failure {
	var known *contract.Failure
	if errors.As(err, &known) {
		// mcpstdio bins every tool that ran and refused as
		// FailureInvalidInput. That is the right default for a transport,
		// which must not learn one provider's error vocabulary -- and it is
		// wrong for this one: ladygraph's SYMBOL_NOT_FOUND says the key
		// names nothing, not that the argument was malformed, and a caller
		// told "invalid input" would go on correcting a stable_key that was
		// never the problem. Correcting the bin here is the whole job of an
		// adapter: this is the one package paid to know that vocabulary.
		if known.Kind != contract.FailureInvalidInput {
			return known
		}
		if code, message, ok := providerCode(known.Message); ok && strings.Contains(code, "NOT_FOUND") {
			raw := known.Raw
			if raw == "" {
				raw = known.Message
			}
			return contract.Fail(contract.FailureNotFound, "ladygraph: %s", message).WithRaw(raw)
		}
		return known
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return contract.Stopped(ctxErr, "ladygraph", r.timeout).WithRaw(err.Error())
	}
	text := strings.TrimSpace(err.Error())
	if code, message, ok := providerCode(text); ok && strings.Contains(code, "NOT_FOUND") {
		return contract.Fail(contract.FailureNotFound, "ladygraph: %s", message).WithRaw(text)
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, "no such host"),
		strings.Contains(lower, "econnrefused"), strings.Contains(lower, "closed pipe"),
		strings.Contains(lower, "file already closed"), strings.Contains(lower, "broken pipe"):
		return contract.Fail(contract.FailureUnavailable, "ladygraph is not reachable").WithRaw(text)
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"):
		return contract.Fail(contract.FailureTimeout, "ladygraph took longer than allowed").WithRaw(text)
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "forbidden"):
		return contract.Fail(contract.FailurePermissionDenied, "ladygraph refused access").WithRaw(text)
	}
	return contract.Fail(contract.FailureUnavailable, "ladygraph did not answer").WithRaw(text)
}

// isUpperCode reports whether s looks like ladygraph's own error-code
// prefix: SCREAMING_SNAKE_CASE, nothing else. A message that merely
// contains a colon ("path: /repo/x") must not be misread as one.
func isUpperCode(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == '_' || (r >= 'A' && r <= 'Z') {
			continue
		}
		return false
	}
	return true
}

// providerCode finds ladygraph's own error code in a message that may have
// been prefixed on the way here. mcpstdio labels a refusal with the tool
// that refused ("find_cross_repo_consumers: SYMBOL_NOT_FOUND: ..."), so the
// code is not reliably the first segment and cutting once would miss it --
// which is exactly how the classification below became unreachable the first
// time. Scans segments left to right and returns the first that reads as a
// code, with everything after it.
func providerCode(text string) (code, message string, found bool) {
	rest := text
	for {
		head, tail, cut := strings.Cut(rest, ":")
		if !cut {
			return "", "", false
		}
		if head = strings.TrimSpace(head); isUpperCode(head) {
			return head, strings.TrimSpace(tail), true
		}
		rest = tail
	}
}

// --- payload readers ---------------------------------------------------
//
// A capability's payload arrives as map[string]any, decoded from whatever
// the caller sent; a JSON number always lands as float64 and a JSON array
// always lands as []any, so every reader has to accept the decoded shape
// rather than the Go type the field will end up as.

// hasField reports whether key is present and not explicitly null --
// consistent with contract.Capability's own reading of "present" -- so a
// payload holding {"file": null} is read the same as one omitting file
// entirely.
func hasField(payload map[string]any, key string) bool {
	value, ok := payload[key]
	return ok && value != nil
}

// stringAt reads an optional string field. The bool distinguishes "absent"
// from "present but empty": an optional tool argument is omitted entirely
// when the caller did not send it, never forwarded as an explicit "".
func stringAt(payload map[string]any, key string) (string, bool) {
	if !hasField(payload, key) {
		return "", false
	}
	text, ok := payload[key].(string)
	return text, ok
}

// intAt reads an optional integer field, accepting the float64 a JSON
// decoder produces for a whole number.
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
