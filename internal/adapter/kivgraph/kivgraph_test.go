package kivgraph

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
		// The shipped output shape, for the reason consumersCapability gives:
		// a double that declares none makes ValidateOutput vacuous, so a
		// runner emitting the wrong keys would pass every assertion in here.
		Outputs: []contract.Field{{
			Name: "symbol", Type: contract.TypeRecordList, Required: true,
			Fields: []contract.Field{
				{Name: "path", Type: contract.TypeString, Required: true},
				{Name: "line", Type: contract.TypeInt, Required: true},
				{Name: "name", Type: contract.TypeString, Required: true},
				{Name: "kind", Type: contract.TypeString, Required: true},
			},
		}},
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
		Outputs: []contract.Field{{
			Name: "unresolved", Type: contract.TypeRecordList, Required: true,
			Fields: []contract.Field{
				{Name: "path", Type: contract.TypeString, Required: true},
				// offset, not line: kivgraph records a byte offset here and
				// the capability declares what it has.
				{Name: "offset", Type: contract.TypeInt, Required: true},
				{Name: "reason", Type: contract.TypeString, Required: true},
				{Name: "requested_package", Type: contract.TypeString, Required: false},
			},
		}},
	}
}

// positionInputs is the input family symbol.definition, symbol.references
// and symbol.consumers all share: a position, a name hint, and the snippet
// intent. Declared once here for the same reason the shipped catalog
// declares it three times -- these are one shape.
func positionInputs(withScope bool) []contract.Field {
	fields := []contract.Field{
		{Name: "file", Type: contract.TypeString, Required: true},
		{Name: "line", Type: contract.TypeInt, Required: true},
		{Name: "column", Type: contract.TypeInt, Required: true},
		{Name: "name", Type: contract.TypeString, Required: false},
	}
	if withScope {
		fields = append(fields, contract.Field{Name: "scope", Type: contract.TypeStringList, Required: false})
	}
	return append(fields,
		contract.Field{Name: "include_snippet", Type: contract.TypeBool, Required: false},
		contract.Field{Name: "snippet_lines", Type: contract.TypeInt, Required: false},
	)
}

func definitionCapability() contract.Capability {
	return contract.Capability{
		ID:      CapabilityDefinition,
		Version: contract.Version{Major: 1},
		Summary: "test double for symbol.definition",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs:  positionInputs(false),
		Outputs: []contract.Field{
			{
				Name: "location", Type: contract.TypeRecord, Required: true,
				Fields: []contract.Field{
					{Name: "path", Type: contract.TypeString, Required: true},
					{Name: "line", Type: contract.TypeInt, Required: true},
					{Name: "snippet", Type: contract.TypeString, Required: false},
				},
			},
		},
	}
}

func referencesCapability() contract.Capability {
	return contract.Capability{
		ID:      CapabilityReferences,
		Version: contract.Version{Major: 1},
		Summary: "test double for symbol.references",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs:  positionInputs(true),
		Outputs: []contract.Field{
			{
				Name: "locations", Type: contract.TypeRecordList, Required: true,
				Fields: []contract.Field{
					{Name: "path", Type: contract.TypeString, Required: true},
					{Name: "line", Type: contract.TypeInt, Required: true},
					{Name: "snippet", Type: contract.TypeString, Required: false},
				},
			},
		},
	}
}

func overviewCapability() contract.Capability {
	return contract.Capability{
		ID:      CapabilityOverview,
		Version: contract.Version{Major: 1},
		Summary: "test double for symbol.overview",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "file", Type: contract.TypeString, Required: true},
			{Name: "depth", Type: contract.TypeInt, Required: false},
		},
		Outputs: []contract.Field{
			{
				Name: "symbols", Type: contract.TypeRecordList, Required: true,
				Fields: []contract.Field{
					{Name: "name", Type: contract.TypeString, Required: true},
					{Name: "kind", Type: contract.TypeString, Required: true},
					{Name: "line", Type: contract.TypeInt, Required: true},
					{Name: "column", Type: contract.TypeInt, Required: true},
					{Name: "end_line", Type: contract.TypeInt, Required: false},
					{Name: "parent", Type: contract.TypeString, Required: false},
				},
			},
		},
	}
}

func statusCapability() contract.Capability {
	return contract.Capability{
		ID:      CapabilityGraphStatus,
		Version: contract.Version{Major: 1},
		Summary: "test double for graph.status",
		Effects: []contract.Effect{contract.EffectRead},
		// No inputs, deliberately: the provider's graph_status tool takes
		// none, so the shipped capability declares none either.
		Outputs: []contract.Field{{
			Name: "snapshot", Type: contract.TypeRecordList, Required: true,
			Fields: []contract.Field{
				{Name: "status", Type: contract.TypeString, Required: true},
				{Name: "snapshot_id", Type: contract.TypeInt, Required: true},
				{Name: "snapshot_built_at", Type: contract.TypeString, Required: true},
				{Name: "symbols", Type: contract.TypeInt, Required: true},
				{Name: "edges", Type: contract.TypeInt, Required: true},
				{Name: "files", Type: contract.TypeInt, Required: true},
				{Name: "repositories", Type: contract.TypeInt, Required: true},
				{Name: "unresolved", Type: contract.TypeInt, Required: true},
			},
		}},
	}
}

func impactCapability() contract.Capability {
	return contract.Capability{
		ID: CapabilityImpact, Version: contract.Version{Major: 1}, Summary: "test double for code.impact",
		Effects: []contract.Effect{contract.EffectRead, contract.EffectProcess},
		Inputs: []contract.Field{
			{Name: "baseline", Type: contract.TypeString, Required: true},
			{Name: "scope", Type: contract.TypeStringList},
			{Name: "depth", Type: contract.TypeInt},
			{Name: "include_snippet", Type: contract.TypeBool},
			{Name: "snippet_lines", Type: contract.TypeInt},
		},
		Outputs: []contract.Field{
			{Name: "changed_files", Type: contract.TypeStringList, Required: true},
			{Name: "affected_symbols", Type: contract.TypeRecordList, Required: true, Fields: []contract.Field{
				{Name: "path", Type: contract.TypeString, Required: true},
				{Name: "line", Type: contract.TypeInt, Required: true},
				{Name: "name", Type: contract.TypeString, Required: true},
				{Name: "kind", Type: contract.TypeString},
				{Name: "depth", Type: contract.TypeInt, Required: true},
				{Name: "snippet", Type: contract.TypeString},
			}},
		},
	}
}

func indexCapability() contract.Capability {
	return contract.Capability{
		ID: CapabilityIndex, Version: contract.Version{Major: 1}, Summary: "test double for repository.index",
		Effects: []contract.Effect{contract.EffectWrite, contract.EffectProcess},
		Inputs:  []contract.Field{{Name: "mode", Type: contract.TypeString, Enum: []string{"full"}}},
		Outputs: []contract.Field{
			{Name: "status", Type: contract.TypeString, Required: true},
			{Name: "nodes", Type: contract.TypeInt, Required: true},
			{Name: "edges", Type: contract.TypeInt, Required: true},
		},
	}
}

func capabilityFor(id string) contract.Capability {
	switch id {
	case CapabilityDefinition:
		return definitionCapability()
	case CapabilityReferences:
		return referencesCapability()
	case CapabilityOverview:
		return overviewCapability()
	case CapabilityConsumers:
		return consumersCapability()
	case CapabilityGet:
		return getCapability()
	case CapabilityUnresolved:
		return unresolvedCapability()
	case CapabilityGraphStatus:
		return statusCapability()
	case CapabilityImpact:
		return impactCapability()
	case CapabilityIndex:
		return indexCapability()
	default:
		panic("capabilityFor: unknown capability " + id)
	}
}

func implFor(capabilityID string) string {
	switch capabilityID {
	case CapabilityDefinition:
		return ImplDefinition
	case CapabilityReferences:
		return ImplReferences
	case CapabilityOverview:
		return ImplOverview
	case CapabilityConsumers:
		return ImplConsumers
	case CapabilityGet:
		return ImplGet
	case CapabilityUnresolved:
		return ImplUnresolved
	case CapabilityGraphStatus:
		return ImplStatus
	case CapabilityImpact:
		return ImplImpact
	case CapabilityIndex:
		return ImplIndex
	default:
		panic("implFor: unknown capability " + capabilityID)
	}
}

// request builds a valid RunRequest against repo for one capability, the
// same shape as the provider request helper.
func request(t *testing.T, repo contract.Repository, capabilityID string, payload map[string]any) contract.RunRequest {
	t.Helper()
	return contract.RunRequest{
		Capability:     capabilityFor(capabilityID),
		Implementation: contract.Implementation{ID: implFor(capabilityID), Provider: "kivgraph", Capability: capabilityID},
		Repository:     repo,
		Payload:        payload,
		Permission:     contract.Permission{Task: "probe", Effects: effectsFor(capabilityID)},
	}
}

func effectsFor(capabilityID string) []contract.Effect {
	if capabilityID == CapabilityIndex {
		return []contract.Effect{contract.EffectWrite, contract.EffectProcess}
	}
	if capabilityID == CapabilityImpact {
		return []contract.Effect{contract.EffectRead, contract.EffectProcess}
	}
	return []contract.Effect{contract.EffectRead}
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
	for _, want := range []string{
		CapabilityDefinition, CapabilityReferences, CapabilityOverview,
		CapabilityConsumers, CapabilityGet, CapabilityUnresolved, CapabilityGraphStatus,
		CapabilityImpact, CapabilityIndex,
	} {
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
	capabilities := []string{
		CapabilityDefinition, CapabilityReferences, CapabilityOverview,
		CapabilityConsumers, CapabilityGet, CapabilityUnresolved, CapabilityGraphStatus,
	}
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
			case CapabilityDefinition, CapabilityReferences, CapabilityConsumers:
				payload = map[string]any{"file": "a.go", "line": 1, "column": 1}
			case CapabilityOverview:
				payload = map[string]any{"file": "a.go"}
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
			for _, tool := range []string{toolConsumers, toolReferences, toolGet, toolUnresolved, toolOutline} {
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

// --- symbol.definition -------------------------------------------------

// outlineWith builds a get_file_outline answer in kivgraph 0.2.1's own
// measured shape: results.files[].symbols[], no stable_key, no column.
func outlineWith(repo, file string, rows ...map[string]any) string {
	body, err := json.Marshal(map[string]any{
		"truncated": false,
		"results": map[string]any{
			"repository": repo,
			"path":       file,
			"files":      []map[string]any{{"path": file, "symbols": rows}},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func TestOutlineDeclarationsAcceptsCompactGroupedRanges(t *testing.T) {
	var answer outlineAnswer
	input := `{"results":{"groups":[{"kind":"method","files":[{"file":"client.go","at":["Runner.Run@304-380","Runner.ID@282"]}]}]}}`
	if err := json.Unmarshal([]byte(input), &answer); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	declarations := answer.declarations()
	if len(declarations) != 2 {
		t.Fatalf("declarations = %#v, want two compact rows", declarations)
	}
	if got := declarations[0]; got.Name != "Runner.Run" || got.QualifiedName != "Runner.Run" || got.Kind != "method" || got.StartLine != 304 || got.EndLine != 380 {
		t.Fatalf("first declaration = %#v", got)
	}
	if got := declarations[1]; got.Name != "Runner.ID" || got.StartLine != 282 || got.EndLine != 282 {
		t.Fatalf("second declaration = %#v", got)
	}
}

func TestRunDefinitionAnswersFromTheDeclarationContainingThePosition(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	// The position asked about is line 15, inside Client.Get's 12-20 span:
	// the definition is the declaration's own start line, not the position.
	fake.on(toolOutline, outlineWith("current", "client.go",
		map[string]any{"name": "Client", "kind": "struct", "start_line": 5, "end_line": 9},
		map[string]any{"name": "Get", "kind": "method", "qualified_name": "Client.Get", "start_line": 12, "end_line": 20},
	), false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityDefinition, map[string]any{"file": "client.go", "line": 15, "column": 3})
	outcome, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	location, ok := outcome.Result["location"].(map[string]any)
	if !ok {
		t.Fatalf("location = %#v, want a record", outcome.Result["location"])
	}
	if got := location["path"]; got != "client.go" {
		t.Errorf("path = %v, want client.go", got)
	}
	if got := location["line"]; got != 12 {
		t.Errorf("line = %v, want 12 (the declaration's start, not the position asked about)", got)
	}
	if _, present := location["snippet"]; present {
		t.Errorf("snippet present without include_snippet: %v", location["snippet"])
	}
	// No stable_key hop: a declaration is already the definition.
	if calls := fake.callsTo(toolGet); len(calls) > 0 {
		t.Errorf("%s was called %d time(s); symbol.definition needs no stable key", toolGet, len(calls))
	}
}

func TestRunDefinitionReadsTheSnippetOffDiskWhenAskedFor(t *testing.T) {
	repo := testRepo(t)
	source := "package main\n\nfunc Answer() int {\n\treturn 42\n}\n"
	if err := os.WriteFile(filepath.Join(repo.Path, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolOutline, outlineWith("current", "main.go",
		map[string]any{"name": "Answer", "kind": "func", "start_line": 3, "end_line": 5},
	), false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityDefinition, map[string]any{
		"file": "main.go", "line": 3, "column": 6,
		"include_snippet": true, "snippet_lines": 2,
	})
	outcome, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	location := outcome.Result["location"].(map[string]any)
	want := "func Answer() int {\n\treturn 42"
	if got := location["snippet"]; got != want {
		t.Errorf("snippet = %q, want %q", got, want)
	}
}

func TestRunDefinitionRefusesAPositionInsideNoDeclaration(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolOutline, outlineWith("current", "client.go",
		map[string]any{"name": "Get", "kind": "method", "qualified_name": "Client.Get", "start_line": 12, "end_line": 20},
	), false)
	runner := newTestRunner(t, sess)

	// Line 3 is inside nothing: a question with no subject, never a
	// definition at line 0 nor an empty answer.
	req := request(t, repo, CapabilityDefinition, map[string]any{"file": "client.go", "line": 3, "column": 1})
	_, err := runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailureNotFound, err)
	}
}

// --- symbol.references -------------------------------------------------

// referencesAnswerJSON builds find_references' measured envelope:
// results.references[], each row carrying its own repository and file_path.
func referencesAnswerJSON(truncated bool, rows ...map[string]any) string {
	body, err := json.Marshal(map[string]any{
		"truncated":   truncated,
		"next_cursor": "",
		"results": map[string]any{
			"direction":  "incoming",
			"references": rows,
		},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func TestRunReferencesAddressesFindReferencesByQualifiedNameWithoutAStableKey(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolOutline, outlineWith("current", "client.go",
		map[string]any{"name": "Get", "kind": "method", "qualified_name": "Client.Get", "start_line": 12, "end_line": 20},
	), false)
	fake.on(toolReferences, referencesAnswerJSON(false,
		map[string]any{"name": "Handler", "repository": "current", "file_path": "http/handler.go", "start_line": 44},
	), false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityReferences, map[string]any{"file": "client.go", "line": 15, "column": 3})
	outcome, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls := fake.callsTo(toolReferences)
	if len(calls) != 1 {
		t.Fatalf("%s called %d time(s), want 1", toolReferences, len(calls))
	}
	if got := calls[0]["qualified_name"]; got != "Client.Get" {
		t.Errorf("qualified_name = %v, want Client.Get", got)
	}
	if got := calls[0]["repository"]; got != "current" {
		t.Errorf("repository = %v, want current", got)
	}
	if _, sent := calls[0]["stable_key"]; sent {
		t.Errorf("stable_key was sent: this tool is addressable by qualified name, and the extra hop is not paid")
	}
	if calls := fake.callsTo(toolGet); len(calls) > 0 {
		t.Errorf("%s was called %d time(s); no stable key is needed here", toolGet, len(calls))
	}
	locations, ok := outcome.Result["locations"].([]any)
	if !ok || len(locations) != 1 {
		t.Fatalf("locations = %#v, want one row", outcome.Result["locations"])
	}
	row := locations[0].(map[string]any)
	if row["path"] != "http/handler.go" || row["line"] != 44 {
		t.Errorf("row = %#v, want http/handler.go:44", row)
	}
}

func TestRunReferencesDropsForeignRepositoriesAndCollapsesRepeatedSites(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolOutline, outlineWith("current", "client.go",
		map[string]any{"name": "NewClient", "kind": "func", "start_line": 21, "end_line": 30},
	), false)
	// Measured against the real server: two calls inside one function come
	// back as two rows naming that function's own declaration line, and a
	// consumer in another repository comes back with its own repository set.
	fake.on(toolReferences, referencesAnswerJSON(false,
		map[string]any{"name": "NewAdapter", "repository": "current", "file_path": "redis/adapter.go", "start_line": 37},
		map[string]any{"name": "NewAdapter", "repository": "current", "file_path": "redis/adapter.go", "start_line": 37},
		map[string]any{"name": "Boot", "repository": "other-repo", "file_path": "cmd/main.go", "start_line": 9},
	), false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityReferences, map[string]any{"file": "client.go", "line": 21, "column": 6})
	outcome, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	locations := outcome.Result["locations"].([]any)
	if len(locations) != 1 {
		t.Fatalf("locations = %#v, want exactly one: the duplicate collapsed and the foreign row dropped", locations)
	}
	if row := locations[0].(map[string]any); row["path"] != "redis/adapter.go" {
		t.Errorf("row = %#v, want the local one", row)
	}
	// Neither narrowing is silent: both travel back as discoveries.
	var foreign, collapsed bool
	for _, d := range outcome.Discoveries {
		if strings.Contains(d.Note, "other repositories") {
			foreign = true
		}
		if strings.Contains(d.Note, "already listed") {
			collapsed = true
		}
	}
	if !foreign {
		t.Errorf("dropped a cross-repository reference without saying so: %+v", outcome.Discoveries)
	}
	if !collapsed {
		t.Errorf("collapsed a repeated site without saying so: %+v", outcome.Discoveries)
	}
}

func TestRunReferencesNarrowsToTheDeclaredScope(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolOutline, outlineWith("current", "client.go",
		map[string]any{"name": "NewClient", "kind": "func", "start_line": 21, "end_line": 30},
	), false)
	fake.on(toolReferences, referencesAnswerJSON(false,
		map[string]any{"repository": "current", "file_path": "redis/adapter.go", "start_line": 37},
		map[string]any{"repository": "current", "file_path": "cmd/tool/main.go", "start_line": 12},
	), false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityReferences, map[string]any{
		"file": "client.go", "line": 21, "column": 6,
		"scope": []any{"redis"},
	})
	outcome, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	locations := outcome.Result["locations"].([]any)
	if len(locations) != 1 {
		t.Fatalf("locations = %#v, want only the row under redis/", locations)
	}
	if row := locations[0].(map[string]any); row["path"] != "redis/adapter.go" {
		t.Errorf("row = %#v, want redis/adapter.go", row)
	}
}

// --- symbol.overview ---------------------------------------------------

func TestRunOverviewRecoversTheColumnFromDiskAndTheParentFromTheQualifiedName(t *testing.T) {
	repo := testRepo(t)
	// Line 3 puts Get at column 18, which no kivgraph row ever reports.
	source := "package redis\n\n" + "func (c *Client) Get(k string) error {\n\treturn nil\n}\n"
	if err := os.WriteFile(filepath.Join(repo.Path, "client.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolOutline, outlineWith("current", "client.go",
		map[string]any{"name": "Get", "kind": "method", "qualified_name": "Client.Get", "start_line": 3, "end_line": 5},
	), false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityOverview, map[string]any{"file": "client.go"})
	outcome, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	symbols := outcome.Result["symbols"].([]any)
	if len(symbols) != 1 {
		t.Fatalf("symbols = %#v, want one row", symbols)
	}
	row := symbols[0].(map[string]any)
	if got := row["column"]; got != 18 {
		t.Errorf("column = %v, want 18 (where the name starts on line 3)", got)
	}
	if got := row["parent"]; got != "Client" {
		t.Errorf("parent = %v, want Client (read off qualified_name, not guessed from spans)", got)
	}
	if got := row["end_line"]; got != 5 {
		t.Errorf("end_line = %v, want 5", got)
	}
}

func TestRunOverviewFallsBackToColumnOneAndSaysSoWhenTheFileIsMissing(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolOutline, outlineWith("current", "gone.go",
		map[string]any{"name": "Answer", "kind": "func", "start_line": 3, "end_line": 3},
	), false)
	runner := newTestRunner(t, sess)

	req := request(t, repo, CapabilityOverview, map[string]any{"file": "gone.go"})
	outcome, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	row := outcome.Result["symbols"].([]any)[0].(map[string]any)
	if got := row["column"]; got != 1 {
		t.Errorf("column = %v, want the fallback 1", got)
	}
	// A one-line declaration repeats nothing: end_line stays absent.
	if _, present := row["end_line"]; present {
		t.Errorf("end_line present for a single-line declaration: %v", row["end_line"])
	}
	var said bool
	for _, d := range outcome.Discoveries {
		if strings.Contains(d.Note, "falls back to 1") {
			said = true
		}
	}
	if !said {
		t.Errorf("fell back to column 1 without saying so: %+v", outcome.Discoveries)
	}
}

func TestRunOverviewAsksForMembersOnlyWhenDepthIsAboveZero(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]any
		want    bool
	}{
		{name: "depth absent", payload: map[string]any{"file": "client.go"}, want: false},
		{name: "depth zero", payload: map[string]any{"file": "client.go", "depth": 0}, want: false},
		{name: "depth one", payload: map[string]any{"file": "client.go", "depth": 1}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			fake, sess := newFakeKivgraph(t)
			fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
			fake.on(toolOutline, outlineWith("current", "client.go",
				map[string]any{"name": "Client", "kind": "struct", "start_line": 5, "end_line": 9},
			), false)
			runner := newTestRunner(t, sess)

			if _, err := runner.Run(context.Background(), request(t, repo, CapabilityOverview, tc.payload)); err != nil {
				t.Fatalf("Run: %v", err)
			}
			calls := fake.callsTo(toolOutline)
			if len(calls) != 1 {
				t.Fatalf("%s called %d time(s), want 1", toolOutline, len(calls))
			}
			_, sent := calls[0]["include_members"]
			if sent != tc.want {
				t.Errorf("include_members sent = %v, want %v", sent, tc.want)
			}
		})
	}
}

func TestRunOverviewRefusesASensitiveFileOutLoud(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	runner, err := New(Options{
		Sensitive: []string{".env", "*.pem"},
		Session:   func(context.Context) (*mcpstdio.Session, error) { return sess, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := request(t, repo, CapabilityOverview, map[string]any{"file": "config/prod.pem"})
	_, err = runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want %v (err=%v)", got, contract.FailurePermissionDenied, err)
	}
	// Refused before the graph was ever asked: nothing to leak, nothing spent.
	if calls := fake.callsTo(toolOutline); len(calls) > 0 {
		t.Errorf("%s was called for a sensitive file", toolOutline)
	}
}

func TestRunDefinitionRefusesASnippetFromASensitiveFile(t *testing.T) {
	repo := testRepo(t)
	if err := os.WriteFile(filepath.Join(repo.Path, ".env"), []byte("TOKEN=shh\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolOutline, outlineWith("current", ".env",
		map[string]any{"name": "TOKEN", "kind": "const", "start_line": 1, "end_line": 1},
	), false)
	runner, err := New(Options{
		Sensitive: []string{".env"},
		Session:   func(context.Context) (*mcpstdio.Session, error) { return sess, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := request(t, repo, CapabilityDefinition, map[string]any{
		"file": ".env", "line": 1, "column": 1, "include_snippet": true,
	})
	_, err = runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want %v (err=%v): a requested snippet is refused, never silently dropped", got, contract.FailurePermissionDenied, err)
	}
}

func TestWithinRefusesAPathThatEscapesTheRepository(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../outside.go", "/etc/passwd", "sub/../../outside.go"} {
		if _, err := within(root, name); contract.KindOf(err) != contract.FailureInvalidInput {
			t.Errorf("within(%q) kind = %v, want %v", name, contract.KindOf(err), contract.FailureInvalidInput)
		}
	}
}

// --- the three capabilities nothing exercised ------------------------------
//
// Runner.Capabilities() declares nine. symbol.get, symbol.unresolved and
// graph.status had no test at all: nobody checked the mapping between what
// kivgraph sends and what the capability declares, and each of the three
// renames or reshapes something on the way through.

// stable_key is symbol.get's only input and its whole point: the capability
// has no file and no line to fall back on, so an empty key is a question with
// no subject and must not reach the wire.
func TestRunGetRefusesAnEmptyStableKeyBeforeAskingTheProvider(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)

	_, err := newTestRunner(t, sess).Run(context.Background(),
		request(t, repo, CapabilityGet, map[string]any{"stable_key": ""}))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err=%v)", got, err)
	}
	if calls := fake.callsTo(toolGet); len(calls) != 0 {
		t.Fatalf("%s was called %#v for a key that names nothing", toolGet, calls)
	}
}

// get_symbol answers with a single object under "results" -- not the list
// every neighbouring tool uses -- and names its fields file_path and
// start_line where the capability says path and line. symbol.get is declared
// as a record LIST all the same, so the one row travels as a single-row list
// and an unknown key travels as an empty one.
func TestRunGetRenamesTheOneRowAndAnswersAnUnknownKeyWithNoRows(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolGet, `{"results":{"stable_key":"KEY1","file_path":"src/index.ts",`+
		`"name":"createLogger","kind":"function","start_line":41,"end_line":45}}`, false)
	runner := newTestRunner(t, sess)

	out, err := runner.Run(context.Background(),
		request(t, repo, CapabilityGet, map[string]any{"stable_key": "KEY1"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if asked := fake.callsTo(toolGet); len(asked) != 1 || asked[0]["stable_key"] != "KEY1" {
		t.Fatalf("%s was asked %#v, want the key alone", toolGet, asked)
	}
	rows, ok := out.Result["symbol"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("symbol = %#v, want the one row as a single-element list", out.Result["symbol"])
	}
	row := rows[0].(map[string]any)
	if row["path"] != "src/index.ts" || row["line"] != 41 ||
		row["name"] != "createLogger" || row["kind"] != "function" {
		t.Fatalf("row = %#v, want file_path/start_line renamed to path/line", row)
	}

	// The tool's own schema allows a null "results", and an empty list is the
	// honest shape for it: symbol.get declares the row list required, so
	// answering with no rows is different from failing to answer.
	empty, sessEmpty := newFakeKivgraph(t)
	empty.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	empty.on(toolGet, `{"results":null}`, false)
	out, err = newTestRunner(t, sessEmpty).Run(context.Background(),
		request(t, repo, CapabilityGet, map[string]any{"stable_key": "GONE"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rows, ok := out.Result["symbol"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("symbol = %#v, want an empty list for a key the graph does not hold", out.Result["symbol"])
	}
}

// symbol.unresolved declares offset and not line, because kivgraph records a
// byte offset and synthesizing a line from it would invent a position the
// provider never reported. requested_package is written only when the row
// carries one, and a truncated answer says so rather than reading as complete.
func TestRunUnresolvedKeepsTheByteOffsetAndReportsTruncation(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	fake.on(toolUnresolved, `{"results":[
		{"file_path":"src/a.ts","start_offset":1487,"reason":"PACKAGE_PROVIDER_NOT_FOUND","requested_package":"@kena/logger"},
		{"file_path":"src/b.ts","start_offset":92,"reason":"AMBIGUOUS"}
	],"truncated":true,"next_cursor":"c-42"}`, false)
	runner := newTestRunner(t, sess)

	out, err := runner.Run(context.Background(), request(t, repo, CapabilityUnresolved,
		map[string]any{"reason": "PACKAGE_PROVIDER_NOT_FOUND", "limit": 2}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	asked := fake.callsTo(toolUnresolved)
	if len(asked) != 1 || asked[0]["reason"] != "PACKAGE_PROVIDER_NOT_FOUND" || asked[0]["limit"] != float64(2) {
		t.Fatalf("%s was asked %#v, want the declared filters forwarded", toolUnresolved, asked)
	}
	if _, forwarded := asked[0]["requested_package"]; forwarded {
		t.Fatalf("%s was asked %#v, want no key for a filter nobody sent", toolUnresolved, asked)
	}
	rows, ok := out.Result["unresolved"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("unresolved = %#v, want both rows", out.Result["unresolved"])
	}
	first := rows[0].(map[string]any)
	if first["offset"] != 1487 || first["requested_package"] != "@kena/logger" {
		t.Fatalf("first row = %#v, want start_offset carried through as offset", first)
	}
	if _, hasLine := first["line"]; hasLine {
		t.Fatalf("first row = %#v, want no line: kivgraph reports none here", first)
	}
	second := rows[1].(map[string]any)
	if _, has := second["requested_package"]; has {
		t.Fatalf("second row = %#v, want no requested_package for a reason that names none", second)
	}
	if !hasNote(out, "past cursor \"c-42\"") {
		t.Fatalf("discoveries = %#v, want one saying the answer was truncated", out.Discoveries)
	}
}

// graph.status answers from the graph_status call Run already paid for to pass
// the empty-graph guard, wraps it in the declared "snapshot" list, and asks
// the tool nothing a second time.
func TestRunGraphStatusWrapsTheGuardsOwnAnswerWithoutAskingAgain(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)

	out, err := newTestRunner(t, sess).Run(context.Background(),
		request(t, repo, CapabilityGraphStatus, map[string]any{}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls := fake.callsTo(toolStatus); len(calls) != 1 {
		t.Fatalf("%s was called %d time(s), want the guard's one", toolStatus, len(calls))
	}
	rows, ok := out.Result["snapshot"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("snapshot = %#v, want the one record as a single-element list", out.Result["snapshot"])
	}
	row := rows[0].(map[string]any)
	if row["status"] != "ready" || row["symbols"] != 3074 || row["edges"] != 11460 ||
		row["files"] != 103 || row["repositories"] != 1 || row["unresolved"] != 208 {
		t.Fatalf("snapshot row = %#v, want the counts the guard already read", row)
	}
	// repository_freshness is real and load-bearing for ProbeIndex, and it is
	// not part of this answer: the capability declares eight fields and
	// projecting a ninth would be a shape nobody agreed to.
	if _, leaked := row["repository_freshness"]; leaked {
		t.Fatalf("snapshot row = %#v, want no repository_freshness", row)
	}
}

// hasNote keeps the discovery loop out of every assertion that only wants to
// know whether something was reported at all.
func hasNote(out contract.Outcome, substring string) bool {
	for _, discovery := range out.Discoveries {
		if strings.Contains(discovery.Note, substring) {
			return true
		}
	}
	return false
}
