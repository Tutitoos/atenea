package kivgraph

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// --- fake kivgraph child ---------------------------------------------------
//
// mcpstdio.Session is a concrete struct, not an interface: the only way to
// hand the adapter a live one without spawning the real `kivgraph serve`
// binary is a peer that speaks the same newline-delimited JSON-RPC over an
// io.Pipe pair. It auto-answers initialize; every other call is dispatched
// by tool name to a test-registered handler, and every call is recorded so
// a test can assert not just the answer but what was actually asked.
type fakeKivgraph struct {
	mu       sync.Mutex
	calls    []fakeCall
	handlers map[string]func(args map[string]any) (result string, isError bool)
}

type fakeCall struct {
	tool string
	args map[string]any
}

func newFakeKivgraph(t *testing.T) (*fakeKivgraph, *mcpstdio.Session) {
	t.Helper()
	stdinR, stdinW := io.Pipe()   // the Session writes stdinW; the fake reads stdinR
	stdoutR, stdoutW := io.Pipe() // the fake writes stdoutW; the Session reads stdoutR
	f := &fakeKivgraph{handlers: map[string]func(map[string]any) (string, bool){}}
	go f.serve(stdinR, stdoutW)
	sess := mcpstdio.New(stdinW, stdoutR, mcpstdio.Options{})
	t.Cleanup(func() {
		_ = sess.Close()
		_ = stdoutW.Close()
	})
	return f, sess
}

// on registers the answer a tool call gets. A tool with no registered
// handler fails loudly (isError=true) rather than hanging Call forever, so
// a test that forgot to configure a tool it did not mean to reach finds out
// from an assertion, not a timeout.
func (f *fakeKivgraph) on(tool string, result string, isError bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[tool] = func(map[string]any) (string, bool) { return result, isError }
}

func (f *fakeKivgraph) callsTo(tool string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.tool == tool {
			out = append(out, c.args)
		}
	}
	return out
}

func (f *fakeKivgraph) serve(in io.Reader, out io.WriteCloser) {
	reader := bufio.NewReaderSize(in, 1<<20)
	for {
		line, err := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			f.handle([]byte(trimmed), out)
		}
		if err != nil {
			return
		}
	}
}

func (f *fakeKivgraph) handle(line []byte, out io.Writer) {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(line, &msg) != nil {
		return
	}
	if len(msg.ID) == 0 || string(msg.ID) == "null" {
		return // a notification, e.g. notifications/initialized: nothing waits on an answer
	}
	switch msg.Method {
	case "initialize":
		f.reply(out, msg.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"serverInfo":      map[string]any{"name": "kivgraph", "version": "0.5.1"},
			"capabilities":    map[string]any{},
		})
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		f.mu.Lock()
		f.calls = append(f.calls, fakeCall{tool: params.Name, args: params.Arguments})
		handler := f.handlers[params.Name]
		f.mu.Unlock()
		if handler == nil {
			f.reply(out, msg.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("no handler configured for %s", params.Name)}},
				"isError": true,
			})
			return
		}
		text, isErr := handler(params.Arguments)
		f.reply(out, msg.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		})
	default:
		f.reply(out, msg.ID, map[string]any{})
	}
}

func (f *fakeKivgraph) reply(out io.Writer, id json.RawMessage, result any) {
	body, err := json.Marshal(struct {
		Version string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{Version: "2.0", ID: id, Result: result})
	if err != nil {
		return
	}
	_, _ = out.Write(append(body, '\n'))
}

// --- fixtures ----------------------------------------------------------

// readyStatus is a healthy graph_status answer registering one repository,
// named repoName on kivgraph's own side, rooted at repoPath.
func readyStatus(repoName, repoPath string) string {
	body, err := json.Marshal(map[string]any{
		"results": map[string]any{
			"status": "ready", "snapshot_id": 1, "snapshot_built_at": "2026-08-14T17:23:13Z",
			"symbols": 3074, "edges": 11460, "files": 103, "repositories": 1, "unresolved": 208,
			"repository_freshness": []map[string]any{{"name": repoName, "path": repoPath}},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// emptyStatus is kivgraph's own measured empty-config failure mode: a
// fresh `kivgraph serve` with no config publishes this, successfully,
// isError:false, for any question at all.
const emptyStatus = `{"results":{"status":"ready","snapshot_id":1,"snapshot_built_at":"2026-08-14T17:23:13Z",` +
	`"symbols":0,"edges":0,"files":0,"repositories":0,"unresolved":0,"repository_freshness":[]}}`

// notReadyStatus is a graph mid-build: not "ready" at all, the other half
// of the empty-graph guard's own status check.
const notReadyStatus = `{"results":{"status":"indexing","snapshot_id":0,"snapshot_built_at":"",` +
	`"symbols":0,"edges":0,"files":0,"repositories":0,"unresolved":0,"repository_freshness":[]}}`

func consumersCapability() contract.Capability {
	return contract.Capability{
		ID:      CapabilityConsumers,
		Version: contract.Version{Major: 1},
		Summary: "test double for symbol.consumers",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "file", Type: contract.TypeString, Required: true},
			{Name: "line", Type: contract.TypeInt, Required: true},
			{Name: "column", Type: contract.TypeInt, Required: true},
			{Name: "name", Type: contract.TypeString, Required: false},
			{Name: "resolution", Type: contract.TypeString, Required: false},
		},
		// Mirrors the shipped capability's own output shape
		// (internal/config/default.toml): without it ValidateOutput refuses
		// every successfully shaped answer, and no test could reach past
		// the first failing tool call.
		Outputs: []contract.Field{
			{
				Name: "consumers", Type: contract.TypeRecordList, Required: true,
				Fields: []contract.Field{
					{Name: "repository", Type: contract.TypeString, Required: true},
					{Name: "path", Type: contract.TypeString, Required: false},
					{
						Name: "resolution", Type: contract.TypeString, Required: true,
						Enum: consumerResolutions,
					},
				},
			},
		},
	}
}

func getCapability() contract.Capability {
	return contract.Capability{
		ID:      CapabilityGet,
		Version: contract.Version{Major: 1},
		Summary: "test double for symbol.get",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "stable_key", Type: contract.TypeString, Required: true},
		},
	}
}

func unresolvedCapability() contract.Capability {
	return contract.Capability{
		ID:      CapabilityUnresolved,
		Version: contract.Version{Major: 1},
		Summary: "test double for symbol.unresolved",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "reason", Type: contract.TypeString, Required: false},
			{Name: "requested_package", Type: contract.TypeString, Required: false},
			{Name: "limit", Type: contract.TypeInt, Required: false},
		},
	}
}

func statusCapability() contract.Capability {
	return contract.Capability{
		ID:      CapabilityGraphStatus,
		Version: contract.Version{Major: 1},
		Summary: "test double for graph.status",
		Effects: []contract.Effect{contract.EffectRead},
	}
}

func capabilityFor(id string) contract.Capability {
	switch id {
	case CapabilityConsumers:
		return consumersCapability()
	case CapabilityGet:
		return getCapability()
	case CapabilityUnresolved:
		return unresolvedCapability()
	case CapabilityGraphStatus:
		return statusCapability()
	default:
		panic("capabilityFor: unknown capability " + id)
	}
}

func implFor(capabilityID string) string {
	switch capabilityID {
	case CapabilityConsumers:
		return ImplConsumers
	case CapabilityGet:
		return ImplGet
	case CapabilityUnresolved:
		return ImplUnresolved
	case CapabilityGraphStatus:
		return ImplStatus
	default:
		panic("implFor: unknown capability " + capabilityID)
	}
}

// request builds a valid RunRequest against repo for one capability, the
// same shape codebasememory_test.go's own request helper builds.
func request(t *testing.T, repo contract.Repository, capabilityID string, payload map[string]any) contract.RunRequest {
	t.Helper()
	return contract.RunRequest{
		Capability:     capabilityFor(capabilityID),
		Implementation: contract.Implementation{ID: implFor(capabilityID), Provider: "kivgraph", Capability: capabilityID},
		Repository:     repo,
		Payload:        payload,
		Permission:     contract.Permission{Task: "probe", Effects: []contract.Effect{contract.EffectRead}},
	}
}

func testRepo(t *testing.T) contract.Repository {
	t.Helper()
	root := t.TempDir()
	return contract.NewRepository("current", root, nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
}

func absPath(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", p, err)
	}
	return abs
}

// newTestRunner builds a Runner backed by sess, the same session for every
// call this Runner ever makes -- fine for a unit test, which never restarts
// the child mid-test the way the supervisor's own on_demand lifecycle would.
func newTestRunner(t *testing.T, sess *mcpstdio.Session) *Runner {
	t.Helper()
	runner, err := New(Options{
		Session: func(context.Context) (*mcpstdio.Session, error) { return sess, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner
}

func slicesContain(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// --- New ---------------------------------------------------------------

func TestNewRejectsANegativeTimeout(t *testing.T) {
	dummy := func(context.Context) (*mcpstdio.Session, error) { return nil, fmt.Errorf("never called") }
	_, err := New(Options{Session: dummy, Timeout: -time.Second})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureInvalidInput, err)
	}
}

func TestNewRejectsANilSession(t *testing.T) {
	_, err := New(Options{})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureInvalidInput, err)
	}
}

func TestNewFillsInDefaults(t *testing.T) {
	dummy := func(context.Context) (*mcpstdio.Session, error) { return nil, fmt.Errorf("never called") }
	runner, err := New(Options{Session: dummy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if runner.timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want %v", runner.timeout, DefaultTimeout)
	}
	for _, id := range DefaultImplementations() {
		if !runner.Serves(id) {
			t.Errorf("Serves(%q) = false, want true", id)
		}
	}
}

// --- identity ------------------------------------------------------------

func TestRunnerAnnouncesWhoItIsAndWhatItServes(t *testing.T) {
	dummy := func(context.Context) (*mcpstdio.Session, error) { return nil, fmt.Errorf("never called") }
	runner, err := New(Options{Session: dummy, Implementations: []string{ImplGet, ImplStatus}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := runner.ID(); got != "kivgraph" {
		t.Errorf("ID() = %q, want %q", got, "kivgraph")
	}
	if !runner.Serves(ImplGet) || !runner.Serves(ImplStatus) {
		t.Errorf("Serves() = false for a declared implementation")
	}
	if runner.Serves(ImplConsumers) {
		t.Errorf("Serves(%q) = true, want false: not in the declared set", ImplConsumers)
	}
	caps := runner.Capabilities()
	for _, want := range []string{CapabilityConsumers, CapabilityGet, CapabilityUnresolved, CapabilityGraphStatus} {
		if !slicesContain(caps, want) {
			t.Errorf("Capabilities() = %v, missing %q", caps, want)
		}
	}
}

// --- Run: guard rails ------------------------------------------------------

func TestRunRejectsAnImplementationItDoesNotServe(t *testing.T) {
	dummy := func(context.Context) (*mcpstdio.Session, error) { return nil, fmt.Errorf("never called") }
	runner, err := New(Options{Session: dummy, Implementations: []string{ImplGet}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	repo := testRepo(t)
	req := request(t, repo, CapabilityConsumers, map[string]any{"file": "a.go", "line": 1, "column": 1})
	_, err = runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureUnavailable, err)
	}
}

func TestRunRejectsACapabilityItHasNoCodeFor(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	runner := newTestRunner(t, sess)

	// A capability Capabilities() does not name, but Serves() still answers
	// yes for the implementation it is stamped against: the two lists are
	// checked separately upstream (see core.checkDispatch), so Run's own
	// switch has to refuse it too rather than trust the funnel already did.
	unknown := contract.Capability{
		ID:      "symbol.unknown",
		Version: contract.Version{Major: 1},
		Summary: "not one of the four",
		Effects: []contract.Effect{contract.EffectRead},
	}
	req := contract.RunRequest{
		Capability:     unknown,
		Implementation: contract.Implementation{ID: ImplGet, Provider: "kivgraph", Capability: unknown.ID},
		Repository:     repo,
		Payload:        map[string]any{},
		Permission:     contract.Permission{Task: "probe", Effects: []contract.Effect{contract.EffectRead}},
	}
	_, err := runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureNotFound, err)
	}
}

// --- the empty-graph guard: the headline behavior ---------------------------

func TestRunRefusesEveryCapabilityWhenTheGraphIsEmpty(t *testing.T) {
	capabilities := []string{CapabilityConsumers, CapabilityGet, CapabilityUnresolved, CapabilityGraphStatus}
	for _, capabilityID := range capabilities {
		t.Run(capabilityID, func(t *testing.T) {
			repo := testRepo(t)
			fake, sess := newFakeKivgraph(t)
			// isError:false, every count zero -- kivgraph's own empty-config
			// failure mode. Nothing else is registered: reaching
			// find_cross_repo_consumers, get_symbol or
			// get_unresolved_references at all would itself be the bug this
			// test exists to catch, since the fake fails loudly on any tool
			// it was not told to answer.
			fake.on(toolStatus, emptyStatus, false)
			runner := newTestRunner(t, sess)

			var payload map[string]any
			switch capabilityID {
			case CapabilityConsumers:
				payload = map[string]any{"file": "a.go", "line": 1, "column": 1}
			case CapabilityGet:
				payload = map[string]any{"stable_key": "go:pkg#Foo"}
			default:
				payload = map[string]any{}
			}
			req := request(t, repo, capabilityID, payload)
			_, err := runner.Run(context.Background(), req)
			if got := contract.KindOf(err); got != contract.FailureUnavailable {
				t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureUnavailable, err)
			}
			for _, tool := range []string{toolConsumers, toolGet, toolUnresolved, toolOutline} {
				if calls := fake.callsTo(tool); len(calls) > 0 {
					t.Errorf("%s was called %d time(s) despite the empty-graph guard", tool, len(calls))
				}
			}
		})
	}
}

func TestRunRefusesWhenTheGraphIsNotReady(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, notReadyStatus, false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityGraphStatus, map[string]any{})
	_, err := runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureUnavailable, err)
	}
}

func TestRunRefusesWhenTheRepositoryIsNotRegistered(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	// A real, non-empty graph -- just one that never indexed this
	// repository: registered under a path that is deliberately not repo's.
	fake.on(toolStatus, readyStatus("other", "/somewhere/else"), false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityGet, map[string]any{"stable_key": "go:pkg#Foo"})
	_, err := runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureUnavailable, err)
	}
	if calls := fake.callsTo(toolGet); len(calls) > 0 {
		t.Errorf("%s was called despite the repository being unregistered", toolGet)
	}
}

// --- ProbeIndex --------------------------------------------------------

func TestProbeIndexReportsReadyForARegisteredRepository(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	runner := newTestRunner(t, sess)

	ready, hint, err := runner.ProbeIndex(context.Background(), repo.Path)
	if err != nil {
		t.Fatalf("ProbeIndex: %v", err)
	}
	if !ready {
		t.Errorf("ready = false, want true (hint=%q)", hint)
	}
	if hint != "" {
		t.Errorf("hint = %q, want empty on a ready verdict", hint)
	}
}

func TestProbeIndexReportsNotReadyForAnUnregisteredRepository(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("other", "/somewhere/else"), false)
	runner := newTestRunner(t, sess)

	ready, hint, err := runner.ProbeIndex(context.Background(), repo.Path)
	if err != nil {
		t.Fatalf("ProbeIndex: %v", err)
	}
	if ready {
		t.Errorf("ready = true, want false: repo was never registered")
	}
	if hint == "" {
		t.Errorf("hint = empty, want an explanation on a false verdict")
	}
}

func TestProbeIndexReturnsTheErrorForAnythingElse(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	// A malformed answer to graph_status: unreadable, so fetchStatus itself
	// fails rather than deciding the graph is absent -- the caller must not
	// correct indexed_by on a probe that could not reach a verdict at all.
	fake.on(toolStatus, `not json`, false)
	runner := newTestRunner(t, sess)

	ready, hint, err := runner.ProbeIndex(context.Background(), repo.Path)
	if err == nil {
		t.Fatalf("err = nil, want non-nil: the probe itself could not reach a verdict")
	}
	if ready {
		t.Errorf("ready = true, want false on an errored probe")
	}
	if hint != "" {
		t.Errorf("hint = %q, want empty on a non-nil err", hint)
	}
}

// --- symbol.consumers position resolution -----------------------------

func TestRunConsumersResolvesAPositionBeforeCallingFindCrossRepoConsumers(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	// kivgraph's own name for this repository ("backend") need not match
	// atenea's repository id ("current") -- runConsumers has to read that
	// translation off the very entry the shared guard already matched, not
	// assume the two ids agree.
	fake.on(toolStatus, readyStatus("backend", absPath(t, repo.Path)), false)
	// get_file_outline's real envelope: "results" is one object about the
	// file and the declarations hang off its "symbols" -- not the row list
	// every neighboring tool returns. Measured against v0.5.1; the fixture
	// said list here first and the package passed while the live call could
	// not decode a single outline.
	fake.on(toolOutline, `{"results":{"path":"handler.go","repository":"backend","symbols":[
		{"name":"Handler","qualified_name":"Handler","kind":"func","stable_key":"go:pkg#Handler","start_line":10,"end_line":20}
	]}}`, false)
	// The consumers call itself is left to fail on purpose: this test's job
	// is to prove resolvePosition handed the RIGHT stable_key to
	// find_cross_repo_consumers, not to exercise a full success shape.
	fake.on(toolConsumers, "SYMBOL_NOT_FOUND: stable key rotated out from under this call", true)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityConsumers, map[string]any{"file": "handler.go", "line": 15, "column": 3})
	_, err := runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureNotFound, err)
	}

	outlineCalls := fake.callsTo(toolOutline)
	if len(outlineCalls) != 1 {
		t.Fatalf("get_file_outline called %d time(s), want 1", len(outlineCalls))
	}
	if got := outlineCalls[0]["repository"]; got != "backend" {
		t.Errorf("get_file_outline repository = %v, want %q -- kivgraph's own registered name, not atenea's repository id", got, "backend")
	}
	if got := outlineCalls[0]["path"]; got != "handler.go" {
		t.Errorf("get_file_outline path = %v, want %q", got, "handler.go")
	}

	consumersCalls := fake.callsTo(toolConsumers)
	if len(consumersCalls) != 1 {
		t.Fatalf("find_cross_repo_consumers called %d time(s), want 1", len(consumersCalls))
	}
	if got := consumersCalls[0]["stable_key"]; got != "go:pkg#Handler" {
		t.Errorf("find_cross_repo_consumers stable_key = %v, want %q -- resolvePosition's own answer must be what is forwarded", got, "go:pkg#Handler")
	}
}

func TestRunConsumersNeverPaysTheSecondHopWhenTheOutlineCarriesTheKey(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("backend", absPath(t, repo.Path)), false)
	fake.on(toolOutline, `{"results":{"path":"handler.go","repository":"backend","symbols":[
		{"name":"Handler","qualified_name":"Handler","kind":"func","stable_key":"go:pkg#Handler","start_line":10,"end_line":20}
	]}}`, false)
	fake.on(toolConsumers, `{"results":[],"total":0,"returned":0}`, false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityConsumers, map[string]any{"file": "handler.go", "line": 15, "column": 3})
	if _, err := runner.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The fast path is one outline call and nothing else: a row that
	// already names its key must never pay for the get_symbol hop.
	if calls := fake.callsTo(toolGet); len(calls) > 0 {
		t.Errorf("get_symbol called %d time(s) although the outline already carried a stable_key", len(calls))
	}
}

func TestRunConsumersResolvesTheStableKeyThroughGetSymbolWhenTheOutlineOmitsIt(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("api-db-go", absPath(t, repo.Path)), false)
	// kivgraph 0.2.1's own measured outline: declarations grouped per file
	// under "files", each row carrying name, kind, signature, exported and
	// the span -- no stable_key at all, and no qualified_name either for a
	// plain function.
	fake.on(toolOutline, `{"results":{"repository":"api-db-go","path":"internal/infrastructure/redis/client.go","files":[
		{"path":"internal/infrastructure/redis/client.go","symbols":[
			{"name":"NewClient","kind":"func","signature":"func NewClient(ctx context.Context, cfg config.Redis) (*Client, error)","exported":true,"start_line":21,"end_line":41},
			{"name":"Client","kind":"type","signature":"Client","exported":true,"start_line":14,"end_line":16}
		]}
	]}}`, false)
	// get_symbol on that same server: one "results" object, and the
	// stable_key the outline never carried.
	fake.on(toolGet, `{"results":{"stable_key":"SE2NYIHQKFDA6NK7KX2PVB2SW5XFEUXWH4O6OCO4XD3ZF2B4N2GQ","repository":"api-db-go","file_path":"internal/infrastructure/redis/client.go","name":"NewClient","qualified_name":"NewClient","kind":"func","start_line":21,"end_line":41}}`, false)
	fake.on(toolConsumers, `{"results":{"subject":{"qualified_name":"NewClient"},"consumers":null},"total":0,"returned":0}`, false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityConsumers, map[string]any{
		"file": "internal/infrastructure/redis/client.go", "line": 21, "column": 6,
	})
	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v -- an outline without stable_key is a version difference, not a missing declaration", err)
	}
	if out.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v, want %v", out.Verdict, contract.VerdictOK)
	}

	getCalls := fake.callsTo(toolGet)
	if len(getCalls) != 1 {
		t.Fatalf("get_symbol called %d time(s), want exactly 1 -- the second hop is paid once, only where the key is missing", len(getCalls))
	}
	if got := getCalls[0]["repository"]; got != "api-db-go" {
		t.Errorf("get_symbol repository = %v, want %q", got, "api-db-go")
	}
	if got := getCalls[0]["path"]; got != "internal/infrastructure/redis/client.go" {
		t.Errorf("get_symbol path = %v, want the file the position named", got)
	}
	// A server that only sets qualified_name for methods leaves the bare
	// name as the row's only handle, and that is what get_symbol is asked by.
	if got := getCalls[0]["qualified_name"]; got != "NewClient" {
		t.Errorf("get_symbol qualified_name = %v, want %q", got, "NewClient")
	}

	consumersCalls := fake.callsTo(toolConsumers)
	if len(consumersCalls) != 1 {
		t.Fatalf("find_cross_repo_consumers called %d time(s), want 1", len(consumersCalls))
	}
	if got := consumersCalls[0]["stable_key"]; got != "SE2NYIHQKFDA6NK7KX2PVB2SW5XFEUXWH4O6OCO4XD3ZF2B4N2GQ" {
		t.Errorf("find_cross_repo_consumers stable_key = %v, want the key get_symbol minted", got)
	}
}

func TestRunConsumersAsksGetSymbolByQualifiedNameForAMethod(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("api-db-go", absPath(t, repo.Path)), false)
	// The one row shape kivgraph 0.2.1 does qualify: a method. Its bare
	// name names several declarations across the corpus, so the qualified
	// one is what must travel.
	fake.on(toolOutline, `{"results":{"repository":"api-db-go","path":"client.go","files":[
		{"path":"client.go","symbols":[
			{"name":"Close","qualified_name":"Client.Close","kind":"method","start_line":48,"end_line":53}
		]}
	]}}`, false)
	fake.on(toolGet, `{"results":{"stable_key":"KEYCLOSE","file_path":"client.go","name":"Close","kind":"method","start_line":48}}`, false)
	fake.on(toolConsumers, `{"results":{"consumers":[]}}`, false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityConsumers, map[string]any{"file": "client.go", "line": 50, "column": 2})
	if _, err := runner.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	getCalls := fake.callsTo(toolGet)
	if len(getCalls) != 1 {
		t.Fatalf("get_symbol called %d time(s), want 1", len(getCalls))
	}
	if got := getCalls[0]["qualified_name"]; got != "Client.Close" {
		t.Errorf("get_symbol qualified_name = %v, want %q -- the row's qualified_name wins over its bare name", got, "Client.Close")
	}
}

func TestRunConsumersRefusesWhenNeitherHopCarriesAStableKey(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("api-db-go", absPath(t, repo.Path)), false)
	fake.on(toolOutline, `{"results":{"repository":"api-db-go","path":"client.go","files":[
		{"path":"client.go","symbols":[{"name":"NewClient","kind":"func","start_line":21,"end_line":41}]}
	]}}`, false)
	// An answer without the one field the second hop exists to fetch.
	fake.on(toolGet, `{"results":{"file_path":"client.go","name":"NewClient","kind":"func","start_line":21}}`, false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityConsumers, map[string]any{"file": "client.go", "line": 30, "column": 1})
	_, err := runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureNotFound, err)
	}
	// The declaration WAS found: a refusal claiming otherwise sends the
	// caller hunting for a line that was never the problem.
	if strings.Contains(err.Error(), "names no declaration") {
		t.Errorf("message = %q, want it to report the missing key, not a missing declaration", err.Error())
	}
	if !strings.Contains(err.Error(), "NewClient") || !strings.Contains(err.Error(), "stable key") {
		t.Errorf("message = %q, want the resolved declaration and the missing stable key named", err.Error())
	}
	if calls := fake.callsTo(toolConsumers); len(calls) > 0 {
		t.Errorf("find_cross_repo_consumers was called with no stable key to query by")
	}
}

func TestRunConsumersKeepsTheSecondHopsNotFoundOutOfTheUnavailableBin(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("api-db-go", absPath(t, repo.Path)), false)
	fake.on(toolOutline, `{"results":{"repository":"api-db-go","path":"client.go","files":[
		{"path":"client.go","symbols":[{"name":"NewClient","kind":"func","start_line":21,"end_line":41}]}
	]}}`, false)
	// The far side's own "nothing by that name". Binning it as unavailable
	// would drive provider health down over one symbol the graph does not
	// hold, and pull kivgraph out of the funnel for every later question.
	fake.on(toolGet, "SYMBOL_NOT_FOUND: no symbol NewClient in client.go", true)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityConsumers, map[string]any{"file": "client.go", "line": 30, "column": 1})
	_, err := runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureNotFound, err)
	}
	if !strings.Contains(err.Error(), "no symbol NewClient in client.go") {
		t.Errorf("message = %q, want the far side's own words kept", err.Error())
	}
}

func TestRunConsumersReadsAGroupedConsumersAnswer(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("library-logger", absPath(t, repo.Path)), false)
	fake.on(toolOutline, `{"results":{"repository":"library-logger","path":"src/index.ts","files":[
		{"path":"src/index.ts","symbols":[{"name":"createLogger","kind":"function","start_line":41,"end_line":45}]}
	]}}`, false)
	fake.on(toolGet, `{"results":{"stable_key":"KEYCREATELOGGER","file_path":"src/index.ts","name":"createLogger","kind":"function","start_line":41}}`, false)
	// kivgraph 0.2.1's own consumers envelope: "results" is an object
	// holding the subject and the rows, and a row names its repository
	// bare ("repository") with the consuming file under "file_path" --
	// where v0.5.1 says consumer_repository_key and consumer_file_path.
	fake.on(toolConsumers, `{"results":{"subject":{"qualified_name":"createLogger"},"consumers":[
		{"category":"package","repository":"admin.kena.lan","package_name":"admin.kena.lan","confidence":"EXACT_TYPECHECKED"},
		{"category":"unresolved","repository":"api-cdn","file_path":"src/shared/logger.ts","confidence":"UNRESOLVED"}
	]},"total":2,"returned":2}`, false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityConsumers, map[string]any{"file": "src/index.ts", "line": 41, "column": 17})
	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	consumers, ok := out.Result["consumers"].([]any)
	if !ok || len(consumers) != 2 {
		t.Fatalf("consumers = %#v, want the 2 rows the grouped answer carried", out.Result["consumers"])
	}

	pkg, _ := consumers[0].(map[string]any)
	if pkg["repository"] != "admin.kena.lan" {
		t.Errorf("package row repository = %v, want the bare name the row carried", pkg["repository"])
	}
	// A package-level row proves a dependency on the PACKAGE, never a use
	// of the symbol: it carries no file, and none may be invented for it.
	if _, has := pkg["path"]; has {
		t.Errorf("package row carries path = %v, want none", pkg["path"])
	}

	unresolved, _ := consumers[1].(map[string]any)
	if unresolved["repository"] != "api-cdn" || unresolved["path"] != "src/shared/logger.ts" {
		t.Errorf("unresolved row = %#v, want repository api-cdn at src/shared/logger.ts", unresolved)
	}
}

func TestRunConsumersRefusesAPositionInsideNoDeclaration(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolOutline, `{"results":{"path":"handler.go","repository":"current","symbols":[
		{"name":"Handler","qualified_name":"Handler","kind":"func","stable_key":"go:pkg#Handler","start_line":10,"end_line":20}
	]}}`, false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityConsumers, map[string]any{"file": "handler.go", "line": 200, "column": 1})
	_, err := runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureNotFound, err)
	}
	if calls := fake.callsTo(toolConsumers); len(calls) > 0 {
		t.Errorf("find_cross_repo_consumers was called despite no declaration matching the position")
	}
}

func TestRunConsumersRefusesAnUnknownResolutionValue(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityConsumers, map[string]any{
		"file": "a.go", "line": 1, "column": 1, "resolution": "sort-of",
	})
	_, err := runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureInvalidInput, err)
	}
	if calls := fake.callsTo(toolOutline); len(calls) > 0 {
		t.Errorf("get_file_outline was called despite an invalid resolution value")
	}
}
