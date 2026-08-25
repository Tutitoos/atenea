// Package kivgraph is the fifth adapter, and the first that stopped caring
// which transport reaches its far side.
//
// Every earlier client speaks over an address: omp and Claude Code as a
// binary invoked once per call, Serena as an MCP server behind a fixed URL,
// another graph CLI as a fresh process per call. kivgraph itself answers two
// ways, measured on the same 0.7.0 binary: `kivgraph serve` over stdio, a
// server with no address at all -- nothing to dial, only two pipes, reachable
// only by whatever spawned it and kept them open -- and `kivgraph daemon`,
// which publishes the identical tool surface at a fixed streamable-HTTP
// address instead, supervised once by systemd rather than once per Atenea
// process. Both answer the same tools this package already decodes, and this
// package only ever called one method on either far side, so it asks only
// for a Session (see that type's own doc comment) rather than the concrete
// *mcpstdio.Session it used to require.
//
// The graph itself is one global corpus, not one per repository. kivgraph
// indexes a whole workspace (`kivgraph index --full`) and publishes a single snapshot every
// reader shares by atomic generation; there is no per-repository index to
// warm or retarget the way Serena's active project has to be, which is why
// the declared instance policy is "shared" and only "shared" -- see
// internal/core's wiring for the refusal when a settings file asks for
// anything else.
//
// # Two versions, one far side
//
// This package is named for the binary that answers on this machine,
// `kivgraph` 0.2.1. Several shapes here were first measured against the
// predecessor-compatible Kivgraph v0.5.1: where a comment names
// one version or the other, it is saying which server that fact was measured
// on, not which one is required. Three of those facts differ between the
// two, and every one of them is decoded both ways rather than picked by
// version number -- a server is asked, never by number:
//   - get_file_outline groups declarations under "files":[{"symbols":[...]}]
//     on 0.2.1 and directly under "symbols" on v0.5.1 (see declarations()).
//   - find_cross_repo_consumers answers {"subject":..,"consumers":[..]} on
//     0.2.1 and a bare list on v0.5.1 (see consumerRows).
//   - an outline row carries stable_key on v0.5.1 and none on 0.2.1, where
//     the address costs one extra get_symbol call (see stableKeyOf).
//
// # The empty-graph trap
//
// A fresh `kivgraph serve` with no config scaffolds an empty config and
// publishes an empty graph, and answers every query with nothing --
// successfully, isError:false, zero counts. Measured against Kivgraph v0.5.1: this is
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
// The capability declarations were written from the design; kivgraph's own
// wire was measured separately, and the two do not use the same words for
// the same fact. consumer_repository_key is a KEY ("repository:backend"),
// not a bare name -- there is no consumer_repository_name field -- so the
// declared "repository" output strips the prefix (repositoryNameFromKey).
// get_unresolved_references carries no line number, only start_offset, a
// byte offset, so symbol.unresolved's declared output is "offset", not
// "line": synthesizing a line by reading the file back would be a second,
// unverified guess about a position kivgraph itself never reported.
// find_cross_repo_consumers classifies each row with "category", whose real
// values (exact_symbol, package, candidate, unresolved) are kept verbatim as
// the declared "resolution" rather than normalized to the coverage
// envelope's own, differently-spelled buckets (exact, package_level,
// candidate, unresolved_related) -- and a package-level row proves the
// consumer depends on the provider PACKAGE, never the symbol, so it is never
// folded into exact_symbol by anything in this file, and its file path is
// left out of the record entirely rather than invented, because kivgraph
// itself never sets one for that row.
//
// # Position resolution inside symbol.consumers
//
// Atenea names a symbol by position -- file, line, column -- and
// symbol.consumers keeps that shape: file and line stay required, and there
// is no stable_key input, on purpose. A capability whose required input
// only one provider could ever produce is not a capability, it is that
// provider's tool passthrough wearing a funnel costume: no language server
// and no future provider could ever mint a kivgraph stable_key, so
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
// candidate's qualified_name exactly (kivgraph disambiguates repeated
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
// # Indexing and impact
//
// The read capabilities use the published snapshot only. repository.index is
// the explicit mutation boundary: it runs Kivgraph's official full-index
// command, then reads graph_status from the supervised server. code.impact
// resolves the current declarations touched by a Git baseline diff and uses
// get_blast_radius for each one. Both paths keep the provider's limitations
// visible in their discoveries rather than pretending a global graph is a
// per-repository one.
package kivgraph

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// The capabilities this adapter answers, and the implementation ids the
// shipped catalog gives them. Ids are separate from capability ids because a
// capability is the "what" and an implementation is the "who": another
// provider may answer the same four capabilities tomorrow.
const (
	CapabilityDefinition  = "symbol.definition"
	CapabilityReferences  = "symbol.references"
	CapabilityOverview    = "symbol.overview"
	CapabilityConsumers   = "symbol.consumers"
	CapabilityGet         = "symbol.get"
	CapabilityUnresolved  = "symbol.unresolved"
	CapabilityGraphStatus = "graph.status"
	CapabilityImpact      = "code.impact"
	CapabilityIndex       = "repository.index"

	ImplDefinition = "kivgraph.definition"
	ImplReferences = "kivgraph.references"
	ImplOverview   = "kivgraph.overview"
	ImplConsumers  = "kivgraph.cross_repo_consumers"
	ImplGet        = "kivgraph.get"
	ImplUnresolved = "kivgraph.unresolved_references"
	ImplStatus     = "kivgraph.status"
	ImplImpact     = "kivgraph.impact"
	ImplIndex      = "kivgraph.index"
)

// The MCP tool names behind each capability, on kivgraph's own far side.
// get_file_outline answers no capability of its own -- symbol.consumers
// calls it internally, through resolvePosition, to turn a position into
// the stable_key find_cross_repo_consumers actually requires.
const (
	toolConsumers  = "find_cross_repo_consumers"
	toolReferences = "find_references"
	toolGet        = "get_symbol"
	toolUnresolved = "get_unresolved_references"
	toolStatus     = "graph_status"
	toolOutline    = "get_file_outline"
	toolBlast      = "get_blast_radius"
)

// maxSnippetBytes caps a file read for a snippet or a column. A minified
// bundle or a checked-in blob is not something to pull into memory whole to
// answer where a name starts on one line.
const maxSnippetBytes = 1 << 20

// defaultSnippetLines is the window read when a caller asks for a snippet
// without saying how much. Small on purpose, as the declaration says.
const defaultSnippetLines = 3

// repositoryKeyPrefix is how kivgraph addresses a repository inside a key
// such as consumer_repository_key. There is no bare-name field beside it, so
// stripping this is the only way to answer the declared "repository" output
// in atenea's own vocabulary rather than kivgraph's addressing scheme.
const repositoryKeyPrefix = "repository:"

// DefaultTimeout caps one call. kivgraph opens a published graph snapshot
// and walks it, which sits at the same class of cost Serena and
// graph providers already pay for a cold cache -- slow long before it is
// stuck -- so this matches their own ceiling rather than inventing a new
// one.
const DefaultTimeout = 90 * time.Second

// DefaultEndpoint is where kivgraph's own daemon listens when nothing else
// is configured: `kivgraph daemon` serves streamable-HTTP MCP at a fixed
// local port, the same "assume it's already running" shape Serena's own
// DefaultEndpoint assumes for its proxy.
const DefaultEndpoint = "http://127.0.0.1:7788/mcp"

// DefaultImplementations is what the adapter answers for. It is a function
// and not a package-level slice because a caller that appended to a shared
// one would quietly change what every other Atenea in this process serves.
func DefaultImplementations() []string {
	return []string{ImplDefinition, ImplReferences, ImplOverview, ImplConsumers, ImplGet, ImplUnresolved, ImplStatus, ImplImpact, ImplIndex}
}

// IndexReport is the authoritative result of Kivgraph's full index command.
// The persistent MCP server can observe the newly published generation a
// little later, so repository.index returns these process-boundary counters
// and uses graph_status only to verify readiness.
type IndexReport struct {
	Generation string
	Nodes      int
	Edges      int
}

// Indexer is the explicit process boundary for repository.index. Keeping it
// injectable makes the mutation path testable without rebuilding a real
// workspace in every adapter test.
type Indexer func(context.Context, string, string) (IndexReport, error)

// Session is the far side of one MCP server, whatever transport reached it.
//
// kivgraph 0.7.0 answers the identical tool vocabulary from `kivgraph serve`
// over stdio and from `kivgraph daemon` over a streamable-HTTP address, and
// this package never asked either one for anything but Call: every
// capability below is a sequence of named tools and decoded JSON, never a
// handshake, a session id or a wire frame. Naming the concrete stdio session
// here would have coupled every function in this file to a transport this
// package has no opinion about, for a method it was never going to use.
type Session interface {
	Call(ctx context.Context, tool string, args map[string]any) (string, error)
}

// Options configure the adapter.
type Options struct {
	// Implementations the adapter answers for.
	Implementations []string
	// Sensitive holds the path patterns that carry secrets. kivgraph's own
	// wire never carries file contents, so this only guards the one thing
	// this adapter reads off the local disk: the line a snippet or a column
	// comes from (see snippetAt and columnOf).
	Sensitive []string
	// Timeout caps one call.
	Timeout time.Duration
	// Session returns the live MCP session for the supervised kivgraph
	// child, over whichever transport reaches it. It is a function, not a
	// stored value, because the process or connection behind it may not
	// exist yet when New runs (on_demand lifecycle) and may be replaced by
	// a restart later: every call asks again rather than trusting a
	// session it cached itself.
	Session func(ctx context.Context) (Session, error)
	// Index runs the provider's official full-index command. It is nil for an
	// unmanaged/test adapter and becomes the configured Kivgraph binary in the
	// core wiring.
	Index Indexer
}

// Runner is the kivgraph far side of contract.Runner.
type Runner struct {
	implementations []string
	sensitive       []string
	timeout         time.Duration
	session         func(ctx context.Context) (Session, error)
	index           Indexer
}

// New validates the options and returns the adapter.
func New(opts Options) (*Runner, error) {
	if opts.Session == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"kivgraph adapter: session is required -- there is no far side to call tools on without one")
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"kivgraph adapter: timeout must not be negative, got %s", timeout)
	}
	// A pattern that cannot compile is refused here rather than silently
	// matching nothing at the one moment it was supposed to protect a file.
	for _, pattern := range opts.Sensitive {
		if _, err := path.Match(pattern, "probe"); err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"kivgraph adapter: sensitive pattern %q: %v", pattern, err)
		}
	}
	impls := slices.Clone(opts.Implementations)
	if impls == nil {
		impls = DefaultImplementations()
	}
	slices.Sort(impls)
	return &Runner{
		implementations: impls,
		sensitive:       slices.Clone(opts.Sensitive),
		timeout:         timeout,
		session:         opts.Session,
		index:           opts.Index,
	}, nil
}

// ID names the runner on the status screen.
func (r *Runner) ID() string { return "kivgraph" }

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
	return []string{
		CapabilityDefinition, CapabilityReferences, CapabilityOverview,
		CapabilityConsumers, CapabilityGet, CapabilityUnresolved, CapabilityGraphStatus,
		CapabilityImpact, CapabilityIndex,
	}
}

// Run executes one step.
func (r *Runner) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	if err := req.Validate(); err != nil {
		return contract.Outcome{}, err
	}
	if !r.Serves(req.Implementation.ID) {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"kivgraph adapter does not serve implementation %s", req.Implementation.ID)
	}

	started := time.Now()
	call, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	sess, err := r.session(call)
	if err != nil {
		return contract.Outcome{}, r.failureFor(err, call)
	}
	// Session declares only Call: an MCP handshake, if one is even needed
	// on this transport, is whatever a Call implementation does before its
	// first request answers -- mcpstdio.Session.Call performs it lazily and
	// idempotently today (see internal/mcpstdio) -- not a second method this
	// package would otherwise have to call defensively on every dispatch.

	// Indexing is the one capability that is allowed to repair an absent or
	// stale graph. It must run before the normal graph-ready gate; otherwise a
	// missing snapshot would make the operation that creates it unreachable.
	if req.Capability.ID == CapabilityIndex {
		result, notes, err := r.runIndex(call, sess, req)
		if err != nil {
			return contract.Outcome{}, r.failureFor(err, call)
		}
		return r.outcome(started, result, notes), nil
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
	case CapabilityDefinition:
		result, notes, err = r.runDefinition(call, sess, status, req)
	case CapabilityReferences:
		result, notes, err = r.runReferences(call, sess, status, req)
	case CapabilityOverview:
		result, notes, err = r.runOverview(call, sess, status, req)
	case CapabilityConsumers:
		result, notes, err = r.runConsumers(call, sess, status, req)
	case CapabilityGet:
		result, notes, err = r.runGet(call, sess, req)
	case CapabilityUnresolved:
		result, notes, err = r.runUnresolved(call, sess, req)
	case CapabilityGraphStatus:
		result, notes, err = r.runGraphStatus(status, req)
	case CapabilityImpact:
		result, notes, err = r.runImpact(call, sess, status, req)
	default:
		return contract.Outcome{}, contract.Fail(contract.FailureNotFound,
			"kivgraph adapter has no implementation of %s", req.Capability.ID)
	}
	if err != nil {
		return contract.Outcome{}, r.failureFor(err, call)
	}

	return r.outcome(started, result, notes), nil
}

func (r *Runner) outcome(started time.Time, result map[string]any, notes []string) contract.Outcome {
	outcome := contract.Outcome{
		Result:  result,
		Verdict: contract.VerdictOK,
		// No memory figure: like Serena, kivgraph runs in a process the
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
	return outcome
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
// against Kivgraph v0.5.1: unlike the other three tools this is one object, not a
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
func (r *Runner) fetchStatus(ctx context.Context, sess Session) (*statusResult, error) {
	text, err := sess.Call(ctx, toolStatus, map[string]any{})
	if err != nil {
		return nil, err
	}
	var answer statusAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, fmt.Errorf("kivgraph %s: unreadable answer: %w", toolStatus, err)
	}
	return answer.Results, nil
}

// checkGraphReady is the guard every capability shares: before any tool's
// answer is trusted, the graph itself has to be real. kivgraph's own
// failure mode, measured against Kivgraph v0.5.1, is not an error -- it is
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
			"kivgraph has no published graph to answer from (status %q)", statusLabel(status))
	}
	if status.Symbols == 0 && status.Edges == 0 && status.Files == 0 {
		return contract.Fail(contract.FailureUnavailable, "kivgraph's published graph is empty")
	}
	if repository == "" {
		return nil
	}
	if _, ok := findRepositoryFreshness(status.RepositoryFreshness, repository); !ok {
		return contract.Fail(contract.FailureUnavailable,
			"kivgraph's published graph does not include repository %s", repository)
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
// in the upstream Kivgraph source, v0.5.1), narrowed to the fields this adapter
// maps into the declared output. category and confidence are always set;
// the consuming file in particular is absent for a package-level row --
// see runConsumers.
//
// The repository and the file arrive under two different names on the two
// servers this adapter was measured against, so both are decoded and
// repositoryName/consumerPath pick whichever was filled: Kivgraph v0.5.1 sets
// consumer_repository_key ("repository:backend") and consumer_file_path,
// kivgraph 0.2.1 sets a bare repository ("admin.kena.lan") and file_path.
// Reading only one pair answers with an empty repository on the other
// server -- a row shaped right and saying nothing.
type consumerRecord struct {
	Category              string `json:"category"`
	ConsumerRepositoryKey string `json:"consumer_repository_key"`
	ConsumerFilePath      string `json:"consumer_file_path"`
	Repository            string `json:"repository"`
	FilePath              string `json:"file_path"`
}

// repositoryName answers the declared "repository" output. A key is
// stripped to its bare name (repositoryNameFromKey); a name that arrived
// bare is already the answer.
func (row consumerRecord) repositoryName() string {
	if row.ConsumerRepositoryKey != "" {
		return repositoryNameFromKey(row.ConsumerRepositoryKey)
	}
	return row.Repository
}

// consumerPath is the file holding the consuming reference, and empty for
// a package-level row on either server -- never invented, see runConsumers.
func (row consumerRecord) consumerPath() string {
	if row.ConsumerFilePath != "" {
		return row.ConsumerFilePath
	}
	return row.FilePath
}

// consumerAnswer is find_cross_repo_consumers' envelope. truncated and
// next_cursor sit beside results on both servers; results itself does not
// -- see consumerRows.
type consumerAnswer struct {
	Results    consumerRows `json:"results"`
	Truncated  bool         `json:"truncated"`
	NextCursor string       `json:"next_cursor"`
}

// consumerRows is find_cross_repo_consumers' "results" in either measured
// shape: Kivgraph v0.5.1 answers with the bare list of rows every neighboring tool
// returns, kivgraph 0.2.1 wraps it as {"subject": {...}, "consumers":
// [...]} -- the subject being the symbol the stable_key named, which this
// adapter already knows because it is the one that resolved it, and so
// never decodes.
//
// Dispatching on the first byte rather than trying one shape and falling
// back to the other on error keeps a genuinely malformed payload an error:
// a list that fails to decode must not be retried as an object and
// reported as an object that happened to hold no consumers.
type consumerRows struct {
	rows []consumerRecord
}

func (c *consumerRows) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '[' {
		return json.Unmarshal(trimmed, &c.rows)
	}
	var grouped struct {
		Consumers []consumerRecord `json:"consumers"`
	}
	if err := json.Unmarshal(trimmed, &grouped); err != nil {
		return err
	}
	c.rows = grouped.Consumers
	return nil
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
// symbol.references; kivgraph names a symbol by stable_key instead, so
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
func (r *Runner) runConsumers(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (map[string]any, []string, error) {
	file, ok := stringAt(req.Payload, "file")
	if !ok || file == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.consumers: file is required")
	}
	line, ok := intAt(req.Payload, "line")
	if !ok {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.consumers: line is required")
	}
	// column is required by the declared capability -- already enforced by
	// req.Validate() before Run ever dispatched here -- but get_file_outline's
	// own declarations carry no column, only [start_line, end_line], so this
	// adapter's span-based resolution never reads it back out.
	name, _ := stringAt(req.Payload, "name")
	resolution, _ := stringAt(req.Payload, "resolution")
	if resolution != "" && !slices.Contains(consumerResolutions, resolution) {
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"kivgraph symbol.consumers: resolution %q is not one of %s", resolution, strings.Join(consumerResolutions, ", "))
	}

	kivgraphRepo, _, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}

	stableKey, notes, err := r.resolvePosition(ctx, sess, kivgraphRepo, file, line, name)
	if err != nil {
		return nil, nil, err
	}

	text, err := sess.Call(ctx, toolConsumers, map[string]any{"stable_key": stableKey})
	if err != nil {
		return nil, nil, err
	}
	var answer consumerAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, fmt.Errorf("kivgraph %s: unreadable answer: %w", toolConsumers, err)
	}

	records := make([]any, 0, len(answer.Results.rows))
	for _, row := range answer.Results.rows {
		if resolution != "" && row.Category != resolution {
			continue
		}
		record := map[string]any{
			"repository": row.repositoryName(),
			"resolution": row.Category,
		}
		// A package-level row proves the consumer depends on the provider
		// PACKAGE, never the symbol -- neither server sets a consuming file
		// for one, and this emits the row without inventing a path for it
		// rather than dropping it.
		if consumerPath := row.consumerPath(); consumerPath != "" {
			record["path"] = consumerPath
		}
		records = append(records, record)
	}
	result := map[string]any{"consumers": records}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}

	notes = append(notes, fmt.Sprintf("kivgraph answered symbol.consumers for %s with %d consumer(s)",
		req.Repository.ID, len(records)))
	if answer.Truncated {
		notes = append(notes, fmt.Sprintf(
			"kivgraph truncated this answer; more consumers exist past cursor %q", answer.NextCursor))
	}
	return result, notes, nil
}

// repositoryNaming answers a repository in kivgraph's own vocabulary and in
// the filesystem's, which are two different names for one repository and both
// needed on every position-first call: the tools are addressed by kivgraph's
// name, and a snippet is read from the absolute root.
//
// The shared guard already matched that root against repository_freshness to
// reach any dispatch, so the matching entry's own Name is reused rather than
// assuming atenea's repository id and kivgraph's happen to agree.
func (r *Runner) repositoryNaming(status *statusResult, req contract.RunRequest) (name, root string, err error) {
	root, err = filepath.Abs(req.Repository.Path)
	if err != nil {
		return "", "", contract.Fail(contract.FailureInvalidInput,
			"repository %s: path %q: %v", req.Repository.ID, req.Repository.Path, err)
	}
	name = req.Repository.ID
	if fresh, ok := findRepositoryFreshness(status.RepositoryFreshness, root); ok && fresh.Name != "" {
		name = fresh.Name
	}
	return name, root, nil
}

// runDefinition answers symbol.definition.
//
// kivgraph needs no "go to definition" tool and has none: its graph is a
// graph of DECLARATIONS, so the declaration a position falls inside of
// already IS the definition. This is the same span walk symbol.consumers
// pays (resolveDeclaration), stopping one hop earlier because no stable_key
// is wanted here -- and deliberately not find_symbol, which searches by
// name, while on this capability a name is a hint and never the subject.
func (r *Runner) runDefinition(ctx context.Context, sess Session, status *statusResult,
	req contract.RunRequest) (map[string]any, []string, error) {

	file, ok := stringAt(req.Payload, "file")
	if !ok || file == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.definition: file is required")
	}
	line, ok := intAt(req.Payload, "line")
	if !ok {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.definition: line is required")
	}
	// column arrives required -- req.Validate() enforced it before Run
	// dispatched -- and stays unread for the same reason as in
	// symbol.consumers: an outline declaration carries [start_line,
	// end_line] and no column at all.
	name, _ := stringAt(req.Payload, "name")

	kivgraphRepo, root, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	decl, notes, err := r.resolveDeclaration(ctx, sess, CapabilityDefinition, kivgraphRepo, file, line, name)
	if err != nil {
		return nil, nil, err
	}

	// The definition is in the file that was asked about: the outline this
	// resolved against is that one file's own, so the declared path is the
	// caller's path and never a second guess at one.
	location := map[string]any{"path": file, "line": decl.StartLine}
	if boolAt(req.Payload, "include_snippet") {
		snippet, err := r.snippetAt(root, file, decl.StartLine, snippetWindow(req.Payload))
		if err != nil {
			return nil, nil, err
		}
		if snippet != "" {
			location["snippet"] = snippet
		}
	}
	result := map[string]any{"location": location}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}

	notes = append(notes, fmt.Sprintf("kivgraph resolved %s:%d to %s (%s) at line %d in %s",
		file, line, decl.Name, decl.Kind, decl.StartLine, kivgraphRepo))
	return result, notes, nil
}

// referenceRecord is one row of find_references' own "references", measured
// live against kivgraph 0.2.1: name, qualified_name, kind, repository,
// file_path, start_line, end_line, language, edge_kind, confidence,
// provenance. Only the three fields the capability can honestly project are
// decoded, plus the repository each row carries -- which is load-bearing, see
// runReferences.
type referenceRecord struct {
	Name       string `json:"name"`
	Repository string `json:"repository"`
	FilePath   string `json:"file_path"`
	StartLine  int    `json:"start_line"`
}

// referencesAnswer is find_references' envelope. Unlike its neighbors the
// rows do not hang off "results" directly: results is an object carrying the
// subject it resolved, the direction it walked and the rows themselves --
// measured, not assumed, and decoding it as a bare list fails on the wire.
type referencesAnswer struct {
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor"`
	Results    struct {
		References []referenceRecord `json:"references"`
	} `json:"results"`
}

// runReferences answers symbol.references.
//
// find_references is addressable by repository, path and qualified_name --
// measured -- so unlike symbol.consumers this pays no stable_key hop: the
// declaration resolved from the position carries the qualified name the tool
// wants, and a package-level declaration whose qualified_name kivgraph 0.2.1
// leaves empty is addressed by its bare name, which that same tool accepts
// (measured: NewClient in api-db-go).
//
// Two narrowings happen client-side, and both are honesty rather than taste:
//
//   - Rows from OTHER repositories are dropped. The declared output is a
//     path and a line and has no repository field, so emitting a foreign
//     row would report a path relative to a root the caller never named --
//     a location pointing at the wrong file, in the one field callers act
//     on. How many were dropped travels back as a discovery, and the
//     capability for that question is symbol.consumers.
//   - Identical path/line rows are collapsed. kivgraph reports one row per
//     graph EDGE, so two calls to the same symbol inside one function come
//     back as two rows naming that function's own declaration line: true on
//     the wire, indistinguishable in an output whose fields are path and
//     line. The collapsed count is reported rather than silently lost.
func (r *Runner) runReferences(ctx context.Context, sess Session, status *statusResult,
	req contract.RunRequest) (map[string]any, []string, error) {

	file, ok := stringAt(req.Payload, "file")
	if !ok || file == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.references: file is required")
	}
	line, ok := intAt(req.Payload, "line")
	if !ok {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.references: line is required")
	}
	name, _ := stringAt(req.Payload, "name")
	scope := scopeEntries(req.Payload)
	wantSnippet := boolAt(req.Payload, "include_snippet")

	kivgraphRepo, root, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	decl, notes, err := r.resolveDeclaration(ctx, sess, CapabilityReferences, kivgraphRepo, file, line, name)
	if err != nil {
		return nil, nil, err
	}
	subject := decl.QualifiedName
	if subject == "" {
		subject = decl.Name
	}

	text, err := sess.Call(ctx, toolReferences, map[string]any{
		"repository":     kivgraphRepo,
		"path":           file,
		"qualified_name": subject,
	})
	if err != nil {
		return nil, nil, err
	}
	var answer referencesAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, fmt.Errorf("kivgraph %s: unreadable answer: %w", toolReferences, err)
	}

	type site struct {
		path string
		line int
	}
	seen := map[site]struct{}{}
	records := []any{}
	foreign, collapsed, outOfScope := 0, 0, 0
	for _, row := range answer.Results.References {
		if row.Repository != "" && row.Repository != kivgraphRepo {
			foreign++
			continue
		}
		relative := filepath.ToSlash(path.Clean(row.FilePath))
		if relative == "" || relative == "." || row.StartLine <= 0 {
			continue
		}
		if !inScope(relative, scope) {
			outOfScope++
			continue
		}
		key := site{path: relative, line: row.StartLine}
		if _, already := seen[key]; already {
			collapsed++
			continue
		}
		seen[key] = struct{}{}
		record := map[string]any{"path": relative, "line": row.StartLine}
		if wantSnippet {
			snippet, err := r.snippetAt(root, relative, row.StartLine, snippetWindow(req.Payload))
			if err != nil {
				return nil, nil, err
			}
			if snippet != "" {
				record["snippet"] = snippet
			}
		}
		records = append(records, record)
	}

	result := map[string]any{"locations": records}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}

	notes = append(notes, fmt.Sprintf("kivgraph answered symbol.references for %s in %s with %d site(s)",
		subject, kivgraphRepo, len(records)))
	if foreign > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d reference(s) live in other repositories and are not reported here: this output's paths are relative to %s, ask symbol.consumers for those",
			foreign, req.Repository.ID))
	}
	if collapsed > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d further graph edge(s) landed on a path and line already listed -- several uses inside one declaration",
			collapsed))
	}
	if outOfScope > 0 {
		notes = append(notes, fmt.Sprintf("%d reference(s) fell outside the declared scope", outOfScope))
	}
	if answer.Truncated {
		notes = append(notes, fmt.Sprintf(
			"kivgraph truncated this answer; more references exist past cursor %q", answer.NextCursor))
	}
	return result, notes, nil
}

// runOverview answers symbol.overview straight from get_file_outline, which
// is the one tool here whose whole job already is "what does this file
// declare".
//
// Two facts about that wire shape the answer, both measured:
//
//   - There is no column on an outline row, so it is recovered from the file
//     exactly the way the capability's own declaration says it is: by finding
//     the symbol's name as a whole word on the line the provider reported
//     (columnOf), with column 1 the honest fallback.
//   - kivgraph reports a method at the file's own top level and names it
//     "Client.Get" in qualified_name, so parent is read off that name rather
//     than guessed from overlapping spans, and depth 0 does not drop methods:
//     they are what this provider considers the file's top level. depth above
//     0 asks include_members, which is where a struct's fields come from.
func (r *Runner) runOverview(ctx context.Context, sess Session, status *statusResult,
	req contract.RunRequest) (map[string]any, []string, error) {

	file, ok := stringAt(req.Payload, "file")
	if !ok || file == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.overview: file is required")
	}
	relative := filepath.ToSlash(path.Clean(file))
	// Refused outright, not answered with column 1: this capability reads the
	// file on every call to recover a required output field, so a sensitive
	// file is a read this adapter declines out loud -- the same refusal the
	// other providers of symbol.overview make.
	if r.isSensitive(relative) {
		return nil, nil, contract.Fail(contract.FailurePermissionDenied,
			"kivgraph symbol.overview: %s carries secrets: this adapter never reads it", relative)
	}
	depth, _ := intAt(req.Payload, "depth")
	if depth < 0 {
		return nil, nil, contract.Fail(contract.FailureInvalidInput,
			"kivgraph symbol.overview: depth must not be negative, got %d", depth)
	}

	kivgraphRepo, root, err := r.repositoryNaming(status, req)
	if err != nil {
		return nil, nil, err
	}
	// Checked before the provider is asked anything, because a path that
	// leaves the repository is not a question worth paying a round trip for.
	// This is the read the rest of the package routes through snippetAt; this
	// one joined the path onto the root itself and skipped the check
	// entirely -- path.Clean does not strip a leading "..", and an absolute
	// path was not refused either.
	resolved, err := within(root, relative)
	if err != nil {
		return nil, nil, err
	}
	args := map[string]any{"repository": kivgraphRepo, "path": relative}
	if depth > 0 {
		args["include_members"] = true
	}
	text, err := sess.Call(ctx, toolOutline, args)
	if err != nil {
		return nil, nil, err
	}
	var answer outlineAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, fmt.Errorf("kivgraph %s: unreadable answer: %w", toolOutline, err)
	}

	// Read once for every row's column, not once per row.
	lines := readLines(resolved)
	symbols := []any{}
	for _, decl := range answer.declarations() {
		if decl.Name == "" || decl.StartLine <= 0 {
			continue
		}
		record := map[string]any{
			"name":   decl.Name,
			"kind":   decl.Kind,
			"line":   decl.StartLine,
			"column": columnOf(lines, decl.StartLine, decl.Name),
		}
		// end_line only when it says something line does not.
		if decl.EndLine > decl.StartLine {
			record["end_line"] = decl.EndLine
		}
		if parent, ok := parentOf(decl); ok {
			record["parent"] = parent
		}
		symbols = append(symbols, record)
	}

	result := map[string]any{"symbols": symbols}
	if err := req.Capability.ValidateOutput(result); err != nil {
		return nil, nil, err
	}

	notes := []string{fmt.Sprintf("kivgraph: %s declares %d symbol(s) in %s",
		relative, len(symbols), kivgraphRepo)}
	if lines == nil {
		notes = append(notes, fmt.Sprintf(
			"%s could not be read from disk, so every column falls back to 1; the lines are the graph's own", relative))
	}
	if answer.Truncated {
		notes = append(notes, fmt.Sprintf(
			"kivgraph truncated this outline; more declarations exist past cursor %q", answer.NextCursor))
	}
	return result, notes, nil
}

// parentOf reads the enclosing declaration's name off a qualified name.
// kivgraph writes a method as "Client.Get", so the prefix before the row's
// own name is its parent -- and a qualified name that is merely the bare name
// repeated (what 0.2.1 writes for a package-level declaration) names no
// parent at all.
func parentOf(decl outlineDeclaration) (string, bool) {
	if decl.QualifiedName == "" || decl.QualifiedName == decl.Name {
		return "", false
	}
	suffix := "." + decl.Name
	if !strings.HasSuffix(decl.QualifiedName, suffix) {
		return "", false
	}
	parent := strings.TrimSuffix(decl.QualifiedName, suffix)
	if parent == "" {
		return "", false
	}
	// Only the innermost enclosing name, never the whole path to it: the
	// declared field is the enclosing symbol's name.
	if at := strings.LastIndex(parent, "."); at >= 0 {
		parent = parent[at+1:]
	}
	return parent, parent != ""
}

// outlineDeclaration is one row of get_file_outline: a declaration's span
// and the stable_key that names it, measured live against Kivgraph v0.5.1.
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
// it. Decoding it as a list of rows, the shape the neighbors use, costs
// nothing at compile time and fails on the wire with "cannot unmarshal
// object into ... []outlineDeclaration": measured against Kivgraph v0.5.1, and only
// caught by asking the real server.
//
// Where the declarations hang off it differs between the two servers this
// adapter was measured against, which is why both fields are decoded and
// declarations() joins them: Kivgraph v0.5.1 puts them directly under "symbols",
// kivgraph 0.2.1 groups them per file under "files":[{"path":...,
// "symbols":[...]}] -- one file per answer in practice, since this adapter
// only ever asks about one. Reading only "symbols" against 0.2.1 decodes
// cleanly into nothing, and an empty outline reports every position as
// naming no declaration: a wrong answer that looks like a resolved one.
type outlineAnswer struct {
	// truncated and next_cursor sit beside results, not inside it, on the
	// same envelope every other tool here uses. symbol.overview reports
	// them: an outline cut short would otherwise read as a file that simply
	// declares less than it does.
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor"`
	Results    struct {
		// Kind is hoisted out of the rows when every declaration on the page
		// shares one, which is how the compact form pays for itself. It is
		// the only kind those rows carry, so a decoder that ignores it
		// reports every declaration as kindless.
		Kind    string               `json:"kind"`
		Symbols []outlineDeclaration `json:"symbols"`
		Files   []outlineFile        `json:"files"`
		Groups  []outlineGroup       `json:"groups"`
	} `json:"results"`
}

// outlineFile is one file's declarations. Which field carries them depends
// on the server: "symbols" on kivgraph 0.2.1, and "at" on 0.7.0, whose
// get_file_outline weighs the grouped encoding against the flat one and
// ships whichever is smaller. Grouping only wins when a (kind, visibility)
// pair repeats enough to pay for its own header, so the flat shape below is
// not an edge case -- it is what a file of mixed declarations produces.
// Measured against 0.7.0: lib/main.dart came back flat with 19 declarations
// and read as empty, while a file whose kinds repeated came back grouped and
// read fine, which is why this looked intermittent rather than broken.
type outlineFile struct {
	File    string               `json:"file"`
	Symbols []outlineDeclaration `json:"symbols"`
	At      []json.RawMessage    `json:"at"`
}

// outlineGroup is the compact shape emitted by the installed kivgraph
// release. Its get_file_outline response groups symbols by kind and encodes
// each range as "Name@line" or "Name@start-end" instead of returning one
// object per declaration. Keep this decoder beside the two older shapes
// above: accepting a new wire shape here is cheaper and safer than letting a
// valid graph look empty to every position-first capability.
type outlineGroup struct {
	Kind  string        `json:"kind"`
	Files []outlineFile `json:"files"`
}

// declarations is every declaration the outline carried, whichever of the
// two shapes it arrived in. Both are read rather than one-or-the-other: a
// server that ever filled both would otherwise have half its answer
// silently dropped, and a duplicate row is harmless here because the
// narrowest span wins and identical rows resolve to the same key.
func (a outlineAnswer) declarations() []outlineDeclaration {
	out := a.Results.Symbols
	for _, file := range a.Results.Files {
		out = append(out, file.Symbols...)
		// The page-level kind is the only one a flat row carries; grouped
		// rows get theirs from the group instead.
		out = append(out, compactDeclarations(a.Results.Kind, file.At)...)
	}
	for _, group := range a.Results.Groups {
		for _, file := range group.Files {
			out = append(out, compactDeclarations(group.Kind, file.At)...)
		}
	}
	return out
}

// compactDeclarations reads one file's compact rows. Each is either the bare
// "Name@start-end" string, or a tuple whose first element is that same
// encoding and whose rest names what the page could not hoist. Both are
// accepted because which one arrives is decided per page by the server, not
// by the caller: a decoder that reads only one shape reports the other as a
// file that declares nothing, and an empty outline is indistinguishable from
// a file with no declarations -- a wrong answer wearing a resolved one's
// clothes.
func compactDeclarations(kind string, rows []json.RawMessage) []outlineDeclaration {
	var out []outlineDeclaration
	for _, row := range rows {
		if decl, ok := compactOutlineEntry(kind, row); ok {
			out = append(out, decl)
		}
	}
	return out
}

// compactOutlineEntry decodes one compact row. The tuple form carries the
// qualified name in its encoding and the simple name beside it, and both are
// kept: columnOf scans the source line for the simple name and would never
// find a qualified one, while parentOf needs the qualified name to name the
// declaration that encloses this one.
func compactOutlineEntry(kind string, row json.RawMessage) (outlineDeclaration, bool) {
	var encoded string
	if err := json.Unmarshal(row, &encoded); err == nil {
		return compactOutlineDeclaration(kind, encoded)
	}
	var tuple []string
	if err := json.Unmarshal(row, &tuple); err != nil || len(tuple) == 0 {
		return outlineDeclaration{}, false
	}
	decl, ok := compactOutlineDeclaration(kind, tuple[0])
	if !ok {
		return outlineDeclaration{}, false
	}
	decl.StableKey = tuple[0]
	if len(tuple) > 1 && tuple[1] != "" {
		decl.Name = tuple[1]
	}
	if len(tuple) > 2 && tuple[2] != "" {
		decl.Kind = tuple[2]
	}
	return decl, true
}

func compactOutlineDeclaration(kind, encoded string) (outlineDeclaration, bool) {
	at := strings.LastIndex(encoded, "@")
	if at <= 0 || at == len(encoded)-1 {
		return outlineDeclaration{}, false
	}
	name := encoded[:at]
	span := strings.Split(encoded[at+1:], "-")
	start, err := strconv.Atoi(span[0])
	if err != nil || start <= 0 {
		return outlineDeclaration{}, false
	}
	end := start
	if len(span) == 2 {
		end, err = strconv.Atoi(span[1])
		if err != nil || end < start {
			return outlineDeclaration{}, false
		}
	}
	return outlineDeclaration{
		Name:          name,
		QualifiedName: name,
		Kind:          kind,
		StartLine:     start,
		EndLine:       end,
	}, true
}

// resolveDeclaration turns a position into the declaration containing it, by
// walking one file's outline. It is the shared half of what symbol.consumers
// used to do alone: symbol.definition answers from the declaration itself,
// symbol.references addresses find_references by its qualified name, and
// symbol.consumers goes one hop further for a stable_key (resolvePosition).
//
// Never cached, for any caller: an outline is only true of the generation
// that produced it, and answering confidently from a resolution the graph
// has since rotated past is exactly the failure this whole provider was
// measured against, so every call pays this again.
//
// capability names the caller in every refusal. It is passed rather than
// inferred because the same wrong answer -- "this position names no
// declaration" -- has to be readable as the question that asked it.
func (r *Runner) resolveDeclaration(ctx context.Context, sess Session,
	capability, repository, file string, line int, name string) (outlineDeclaration, []string, error) {

	text, err := sess.Call(ctx, toolOutline, map[string]any{"repository": repository, "path": file})
	if err != nil {
		// The extra call is not free and must not be invisible: a failure
		// reaching the outline at all belongs in the empty-graph guard's own
		// bin. Nothing has been searched yet, so nothing can honestly be
		// reported missing -- this is deliberately pre-classified rather than
		// left for failureFor's NOT_FOUND sniffing, which would misread
		// kivgraph refusing to produce an outline at all as a resolved
		// "not found".
		return outlineDeclaration{}, nil, contract.Fail(contract.FailureUnavailable,
			"kivgraph %s: could not read %s's outline to resolve %s:%d: %v", capability, repository, file, line, err)
	}
	var answer outlineAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return outlineDeclaration{}, nil, contract.Fail(contract.FailureUnavailable,
			"kivgraph %s: unreadable outline for %s: %v", capability, file, err)
	}

	var candidates []outlineDeclaration
	for _, decl := range answer.declarations() {
		if line >= decl.StartLine && line <= decl.EndLine {
			candidates = append(candidates, decl)
		}
	}
	if len(candidates) == 0 {
		// A position that names no declaration is a question with no
		// subject, not a symbol with no answer: an empty list would claim
		// this position was searched and answered, when nothing in the
		// outline ever matched it.
		return outlineDeclaration{}, nil, contract.Fail(contract.FailureNotFound,
			"kivgraph %s: %s:%d names no declaration in %s's outline", capability, file, line, repository)
	}

	// name, when it disambiguates outright, wins over span alone: kivgraph
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
				return decl, nil, nil
			}
		}
		if len(byName) == 1 {
			return byName[0], nil, nil
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
			// Compared by position and name, never by stable_key: kivgraph
			// 0.2.1 leaves that field empty on every row, so keying identity
			// on it would make each candidate look like the chosen one and
			// report an ambiguity nobody was told about as unanimity.
			if decl.StartLine == innermost.StartLine && decl.EndLine == innermost.EndLine &&
				decl.QualifiedName == innermost.QualifiedName && decl.Name == innermost.Name {
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
	return innermost, notes, nil
}

// resolvePosition is the detour symbol.consumers pays that no other
// capability here does -- see the package doc comment for why the
// capability's own shape stays position-first rather than exposing
// stable_key as input.
//
// It is resolveDeclaration plus the one hop only this capability needs:
// find_cross_repo_consumers is keyed by stable_key alone, while
// find_references and the outline itself both answer to a qualified name.
func (r *Runner) resolvePosition(ctx context.Context, sess Session, repository, file string, line int, name string) (string, []string, error) {
	decl, notes, err := r.resolveDeclaration(ctx, sess, CapabilityConsumers, repository, file, line, name)
	if err != nil {
		return "", nil, err
	}
	key, err := r.stableKeyOf(ctx, sess, CapabilityConsumers, repository, file, decl)
	if err != nil {
		return "", nil, err
	}
	return key, notes, nil
}

// stableKeyOf is the second hop Kivgraph 0.2.1 forces and Kivgraph v0.5.1 does not.
//
// Kivgraph v0.5.1 carries a stable_key on every outline row, so the position
// resolved above is already an address and this returns it untouched. 0.2.1
// carries none -- measured: its outline rows hold name, kind, signature,
// exported, start_line, end_line, and a qualified_name only for methods --
// so the declaration has to be looked up once more, by the one addressing
// scheme that server does answer to: get_symbol keyed by repository, path
// and qualified name, whose payload does carry stable_key.
//
// The name used is qualified_name when the row has one and the bare name
// otherwise: for a package-level declaration 0.2.1 sets no qualified_name
// and get_symbol accepts the bare name in its place (measured: NewClient in
// api-db-go resolves that way). One extra call per resolution on that
// server, never a wrong answer, and nothing at all on a server that fills
// the field.
// capabilityID is threaded through for the error messages alone. They used to
// name symbol.consumers unconditionally, and runImpact calls this once per
// declaration the diff touched, so a code.impact failure explained itself as a
// symbol.consumers one and sent whoever read it looking at the wrong
// capability.
func (r *Runner) stableKeyOf(ctx context.Context, sess Session,
	capabilityID, repository, file string, decl outlineDeclaration) (string, error) {

	if decl.StableKey != "" {
		return decl.StableKey, nil
	}
	qualified := decl.QualifiedName
	if qualified == "" {
		qualified = decl.Name
	}
	if qualified == "" {
		return "", contract.Fail(contract.FailureNotFound,
			"kivgraph %s: the declaration at %s:%d has neither a stable key nor a name to look one up with",
			capabilityID, file, decl.StartLine)
	}
	text, err := sess.Call(ctx, toolGet, map[string]any{
		"repository":     repository,
		"path":           file,
		"qualified_name": qualified,
	})
	if err != nil {
		// A dead pipe, a timeout or a refusal that is not "nothing by that
		// name" keeps whatever bin failureFor -- the one function here paid
		// to know this provider's error vocabulary -- puts it in. Its own
		// SYMBOL_NOT_FOUND is not a provider going down, and calling it
		// unavailable would pull kivgraph out of the funnel over one
		// symbol the graph simply does not hold.
		failure := r.failureFor(err, ctx)
		if failure.Kind != contract.FailureNotFound {
			return "", failure
		}
		return "", contract.Fail(contract.FailureNotFound,
			"kivgraph %s: %s carries no stable key in %s's outline and %s could not supply one: %s",
			capabilityID, qualified, repository, toolGet, failure.Message)
	}
	var answer getSymbolAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return "", contract.Fail(contract.FailureUnavailable,
			"kivgraph %s: unreadable %s answer while resolving %s: %v", capabilityID, toolGet, qualified, err)
	}
	if answer.Results == nil || answer.Results.StableKey == "" {
		return "", contract.Fail(contract.FailureNotFound,
			"kivgraph %s: neither %s nor %s gives a stable key for %s at %s:%d",
			capabilityID, toolOutline, toolGet, qualified, file, decl.StartLine)
	}
	return answer.Results.StableKey, nil
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
// symbol.get declares -- path, line, name and kind all come back required --
// plus the stable_key symbol.get itself never needs, because it was the
// input. That fifth field is here for the other caller: stableKeyOf, which
// asks this same tool for the address kivgraph 0.2.1's outline does not
// carry.
type symbolRecord struct {
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	StableKey string `json:"stable_key"`
}

// getSymbolAnswer is get_symbol's envelope. Unlike every other tool here its
// "results" is a single object, not a list -- confirmed live against Kivgraph v0.5.1
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
func (r *Runner) runGet(ctx context.Context, sess Session, req contract.RunRequest) (map[string]any, []string, error) {
	stableKey, ok := stringAt(req.Payload, "stable_key")
	if !ok || stableKey == "" {
		return nil, nil, contract.Fail(contract.FailureInvalidInput, "kivgraph symbol.get: stable_key is required")
	}

	text, err := sess.Call(ctx, toolGet, map[string]any{"stable_key": stableKey})
	if err != nil {
		return nil, nil, err
	}
	var answer getSymbolAnswer
	if err := json.Unmarshal([]byte(text), &answer); err != nil {
		return nil, nil, fmt.Errorf("kivgraph %s: unreadable answer: %w", toolGet, err)
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

	notes := []string{fmt.Sprintf("kivgraph answered symbol.get for %s with %d row", req.Repository.ID, len(records))}
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
// would be inventing a position kivgraph itself never reported.
//
// reason, requested_package and limit are the only declared inputs -- repo,
// package, requested_symbol, language and cursor are real arguments on the
// tool's own schema but never made it into the capability's, so there is no
// payload key to read them from: checkPayload refuses any field the
// capability does not declare before Run is ever reached.
func (r *Runner) runUnresolved(ctx context.Context, sess Session, req contract.RunRequest) (map[string]any, []string, error) {
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
		return nil, nil, fmt.Errorf("kivgraph %s: unreadable answer: %w", toolUnresolved, err)
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

	notes := []string{fmt.Sprintf("kivgraph answered symbol.unresolved for %s with %d row(s)",
		req.Repository.ID, len(records))}
	if answer.Truncated {
		notes = append(notes, fmt.Sprintf(
			"kivgraph truncated this answer; more unresolved references exist past cursor %q", answer.NextCursor))
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

	notes := []string{fmt.Sprintf("kivgraph answered graph.status: %d symbol(s), %d edge(s) across %d repositories",
		status.Symbols, status.Edges, status.Repositories)}
	return result, notes, nil
}

// ProbeIndex asks kivgraph whether root already has a place in the
// published graph, without asking it to build one.
//
// It mirrors the graph provider's three-way contract exactly: a working
// probe with a definite verdict returns (ready, hint, nil); only the probe
// itself failing to reach a verdict returns a non-nil err, because a caller
// must never correct indexed_by on a guess.
func (r *Runner) ProbeIndex(ctx context.Context, root string) (bool, string, error) {
	sess, err := r.session(ctx)
	if err != nil {
		return false, "", r.failureFor(err, ctx)
	}
	// See Run's own comment on this same point: Session declares only Call,
	// and whatever handshake a transport needs happens inside it.
	status, err := r.fetchStatus(ctx, sess)
	if err != nil {
		return false, "", r.failureFor(err, ctx)
	}
	repoAbs, err := filepath.Abs(root)
	if err != nil {
		return false, "", contract.Fail(contract.FailureInvalidInput, "kivgraph probe: path %q: %v", root, err)
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

// repositoryNameFromKey strips kivgraph's own "repository:" key prefix.
// consumer_repository_key is a KEY ("repository:backend"), not a bare name
// -- there is no consumer_repository_name field on CrossRepoConsumerSummary
// -- and the declared "repository" output is atenea's existing vocabulary
// (see symbol.references), so the prefix belongs to kivgraph's own
// addressing scheme, not to the answer this adapter hands back.
func repositoryNameFromKey(key string) string {
	return strings.TrimPrefix(key, repositoryKeyPrefix)
}

// failureFor sorts what went wrong into the shared bins.
//
// An already-binned failure -- ValidateOutput, resolvePosition's own
// not-found and unavailable refusals, a required-field refusal -- travels
// unchanged: re-reading its own text would be the adapter guessing about
// itself. kivgraph's own
// isError text is the exception worth a rule of its own: every refusal
// measured against Kivgraph v0.5.1 takes the form "UPPERCASE_CODE: message"
// (SYMBOL_NOT_FOUND, ...), and a caller passing a stale or mistyped
// stable_key must land in contract.FailureNotFound, never
// contract.FailureUnavailable -- that bin drives provider health to down and
// pulls kivgraph out of the funnel over one bad key, not one dead provider.
// This classification is still measured against mcpstdio.Session's own
// error shaping (see internal/mcpstdio and providerCode below): dispatch
// above no longer names a transport, but the failures it has to sort still
// arrive shaped by whichever one produced them, and this function's whole
// job is knowing that shape -- it is not something Session's one-method
// interface could ever have carried for it.
func (r *Runner) failureFor(err error, ctx context.Context) *contract.Failure {
	var known *contract.Failure
	if errors.As(err, &known) {
		// mcpstdio bins every tool that ran and refused as
		// FailureInvalidInput. That is the right default for a transport,
		// which must not learn one provider's error vocabulary -- and it is
		// wrong for this one: kivgraph's SYMBOL_NOT_FOUND says the key
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
			return contract.Fail(contract.FailureNotFound, "kivgraph: %s", message).WithRaw(raw)
		}
		return known
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return contract.Stopped(ctxErr, "kivgraph", r.timeout).WithRaw(err.Error())
	}
	text := strings.TrimSpace(err.Error())
	if code, message, ok := providerCode(text); ok && strings.Contains(code, "NOT_FOUND") {
		return contract.Fail(contract.FailureNotFound, "kivgraph: %s", message).WithRaw(text)
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, "no such host"),
		strings.Contains(lower, "econnrefused"), strings.Contains(lower, "closed pipe"),
		strings.Contains(lower, "file already closed"), strings.Contains(lower, "broken pipe"):
		return contract.Fail(contract.FailureUnavailable, "kivgraph is not reachable").WithRaw(text)
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"):
		return contract.Fail(contract.FailureTimeout, "kivgraph took longer than allowed").WithRaw(text)
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "forbidden"):
		return contract.Fail(contract.FailurePermissionDenied, "kivgraph refused access").WithRaw(text)
	}
	return contract.Fail(contract.FailureUnavailable, "kivgraph did not answer").WithRaw(text)
}

// isUpperCode reports whether s looks like kivgraph's own error-code
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

// providerCode finds kivgraph's own error code in a message that may have
// been prefixed on the way here. mcpstdio.Session.Call labels a refusal with
// the tool that refused ("find_cross_repo_consumers: SYMBOL_NOT_FOUND:
// ..."), a shape this function still has to know even though the dispatch
// above it no longer names mcpstdio anywhere, so the code is not reliably
// the first segment and cutting once would miss it -- which is exactly how
// the classification below became unreachable the first time. Scans
// segments left to right and returns the first that reads as a code, with
// everything after it.
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

// --- local reads -------------------------------------------------------
//
// kivgraph's wire carries positions and names, never file contents, so
// everything below reads the working copy on disk. Two output fields need
// it and no more: symbol.overview's required column, which no provider
// behind that capability reports, and the optional snippet the three
// position-first capabilities offer.

// isSensitive matches the configured patterns against both the bare file
// name and the repository-relative path, so ".env" catches a root file and
// "config/*.pem" catches a nested one.
func (r *Runner) isSensitive(relative string) bool {
	slash := filepath.ToSlash(relative)
	for _, pattern := range r.sensitive {
		if ok, _ := path.Match(pattern, slash); ok {
			return true
		}
		if ok, _ := path.Match(pattern, path.Base(slash)); ok {
			return true
		}
	}
	return false
}

// within resolves a repository-relative path against root, refusing anything
// that climbs out of it: an absolute path, a "..", or a symlink that lands
// outside once resolved. A step carries permission for the repository it was
// commissioned against, and a path that escapes it is reading something
// nobody authorized.
func within(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(filepath.FromSlash(name)) {
		return "", contract.Fail(contract.FailureInvalidInput, "%q must be a relative path", name)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", contract.Fail(contract.FailureUnavailable, "cannot resolve repository root: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolved
	}
	joined := filepath.Join(rootAbs, filepath.FromSlash(name))
	if err := contained(rootAbs, joined, name); err != nil {
		return "", err
	}
	// A symlink can point outside the repository even when the lexical path
	// does not climb out of it. EvalSymlinks fails on a path that does not
	// exist, which is fine here: the caller's own read reports that case,
	// and a snippet is optional anyway.
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if os.IsNotExist(err) {
			return joined, nil
		}
		return "", contract.Fail(contract.FailureUnavailable, "cannot resolve %q: %v", name, err)
	}
	if err := contained(rootAbs, resolved, name); err != nil {
		return "", err
	}
	return joined, nil
}

// contained reports whether path sits inside root, once both are absolute.
func contained(root, target, name string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return contract.Fail(contract.FailureInvalidInput, "%q escapes the repository", name)
	}
	return nil
}

// readLines reads a whole file for column recovery. A file that cannot be
// read is not an error: columnOf has an honest fallback, and failing an
// answer whose lines came from the graph over a column would be the tail
// wagging the dog. The caller reports the fallback rather than hiding it.
func readLines(name string) []string {
	info, err := os.Stat(name)
	if err != nil || info.IsDir() || info.Size() > maxSnippetBytes {
		return nil
	}
	file, err := os.Open(name)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	var out []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSnippetBytes)
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
// declaration whose name sits on a line other than the one reported.
func columnOf(lines []string, line int, name string) int {
	if line <= 0 || line > len(lines) || name == "" {
		return 1
	}
	word, err := regexp.Compile(`\b` + regexp.QuoteMeta(name) + `\b`)
	if err != nil {
		return 1
	}
	if at := word.FindStringIndex(lines[line-1]); at != nil {
		return at[0] + 1
	}
	return 1
}

// snippetAt reads a window of lines starting at line, the convention this
// capability family already uses: "how much of it to return" is a forward
// look from the anchor, not a window centered on it.
//
// A sensitive file is refused out loud rather than answered without the
// snippet that was explicitly asked for. A file that merely cannot be read
// yields an empty snippet, because the location around it is still true.
func (r *Runner) snippetAt(root, relative string, line, window int) (string, error) {
	if r.isSensitive(relative) {
		return "", contract.Fail(contract.FailurePermissionDenied,
			"%s carries secrets: this adapter never reads it", relative)
	}
	resolved, err := within(root, relative)
	if err != nil {
		return "", err
	}
	lines := readLines(resolved)
	if line <= 0 || line > len(lines) {
		return "", nil
	}
	to := min(line+window-1, len(lines))
	return strings.Join(lines[line-1:to], "\n"), nil
}

// snippetWindow is how many lines a snippet covers: what the caller asked
// for, or a deliberately small default.
func snippetWindow(payload map[string]any) int {
	if lines, ok := intAt(payload, "snippet_lines"); ok && lines > 0 {
		return lines
	}
	return defaultSnippetLines
}

// scopeEntries reads the declared scope as repository-relative prefixes. An
// empty scope means the whole repository, deliberately, so the default is
// honest rather than quietly narrow.
func scopeEntries(payload map[string]any) []string {
	raw, ok := payload["scope"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text, isText := item.(string)
		if !isText || strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, filepath.ToSlash(path.Clean(text)))
	}
	return out
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

// boolAt reads an optional boolean field. An absent one is false, which is
// what every intent flag here means when nobody set it.
func boolAt(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}
