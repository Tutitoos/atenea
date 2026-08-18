package tokensave

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// --- fake tokensave child ---------------------------------------------------
//
// Same arrangement as the kivgraph adapter's own fake, and for the same
// reason: mcpstdio.Session is a concrete struct, so the only way to hand the
// adapter a live one without spawning `tokensave serve` is a peer speaking the
// same newline-delimited JSON-RPC over an io.Pipe pair. Every call is recorded,
// so a test asserts what was asked and not only what came back.
type fakeTokensave struct {
	mu       sync.Mutex
	calls    []fakeCall
	handlers map[string]func(args map[string]any) (result string, isError bool)
}

type fakeCall struct {
	tool string
	args map[string]any
}

func newFakeTokensave(t *testing.T) (*fakeTokensave, *mcpstdio.Session) {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	f := &fakeTokensave{handlers: map[string]func(map[string]any) (string, bool){}}
	go f.serve(stdinR, stdoutW)
	sess := mcpstdio.New(stdinW, stdoutR, mcpstdio.Options{})
	t.Cleanup(func() {
		_ = sess.Close()
		_ = stdoutW.Close()
	})
	return f, sess
}

func (f *fakeTokensave) on(tool string, result string, isError bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[tool] = func(map[string]any) (string, bool) { return result, isError }
}

func (f *fakeTokensave) callsTo(tool string) []map[string]any {
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

func (f *fakeTokensave) serve(in io.Reader, out io.WriteCloser) {
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

func (f *fakeTokensave) handle(line []byte, out io.Writer) {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(line, &msg) != nil {
		return
	}
	if len(msg.ID) == 0 || string(msg.ID) == "null" {
		return
	}
	switch msg.Method {
	case "initialize":
		f.reply(out, msg.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]any{"name": "tokensave", "version": "7.9.0"},
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

func (f *fakeTokensave) reply(out io.Writer, id json.RawMessage, result any) {
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

// --- fixtures ---------------------------------------------------------------

// readyStatus is tokensave_status on a real index, narrowed to the three
// counts the guard reads.
const readyStatus = `{"node_count":116746,"edge_count":240721,"file_count":7492}`

// emptyStatus is the measured failure mode: a server pointed at a directory it
// has never indexed answers every question successfully, with nothing.
const emptyStatus = `{"node_count":0,"edge_count":0,"file_count":0}`

func overviewCapability() contract.Capability {
	return contract.Capability{
		ID:        CapabilityOverview,
		Version:   contract.Version{Major: 1},
		Summary:   "test double for symbol.overview",
		Semantics: "what a file declares",
		Effects:   []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "file", Type: contract.TypeString, Required: true, Summary: "file to list"},
			{Name: "depth", Type: contract.TypeInt, Summary: "levels of nesting"},
		},
		Outputs: []contract.Field{{
			Name: "symbols", Type: contract.TypeRecordList, Required: true, Summary: "declarations",
			Fields: []contract.Field{
				{Name: "name", Type: contract.TypeString, Required: true, Summary: "name"},
				{Name: "kind", Type: contract.TypeString, Required: true, Summary: "kind"},
				{Name: "line", Type: contract.TypeInt, Required: true, Summary: "line"},
				{Name: "column", Type: contract.TypeInt, Required: true, Summary: "column"},
				{Name: "end_line", Type: contract.TypeInt, Summary: "end line"},
				{Name: "parent", Type: contract.TypeString, Summary: "enclosing symbol"},
			},
		}},
	}
}

func callsCapability() contract.Capability {
	return contract.Capability{
		ID:        CapabilityCalls,
		Version:   contract.Version{Major: 1},
		Summary:   "test double for symbol.calls",
		Semantics: "walk the call graph",
		Effects:   []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "file", Type: contract.TypeString, Required: true, Summary: "file"},
			{Name: "line", Type: contract.TypeInt, Required: true, Summary: "line"},
			{Name: "column", Type: contract.TypeInt, Required: true, Summary: "column"},
			{Name: "name", Type: contract.TypeString, Summary: "name hint"},
			{Name: "direction", Type: contract.TypeString, Required: true, Summary: "which way"},
			{Name: "scope", Type: contract.TypeStringList, Summary: "paths"},
			{Name: "depth", Type: contract.TypeInt, Summary: "hops"},
			{Name: "include_snippet", Type: contract.TypeBool, Summary: "return the line"},
			{Name: "snippet_lines", Type: contract.TypeInt, Summary: "window"},
		},
		Outputs: []contract.Field{{
			Name: "calls", Type: contract.TypeRecordList, Required: true, Summary: "calls",
			Fields: []contract.Field{
				{Name: "path", Type: contract.TypeString, Required: true, Summary: "path"},
				{Name: "line", Type: contract.TypeInt, Required: true, Summary: "line"},
				{Name: "name", Type: contract.TypeString, Required: true, Summary: "name"},
				{Name: "direction", Type: contract.TypeString, Required: true, Summary: "direction"},
				{Name: "depth", Type: contract.TypeInt, Required: true, Summary: "depth"},
				{Name: "snippet", Type: contract.TypeString, Summary: "the line"},
			},
		}},
	}
}

func capabilityFor(id string) contract.Capability {
	switch id {
	case CapabilityOverview:
		return overviewCapability()
	case CapabilityCalls:
		return callsCapability()
	default:
		panic("capabilityFor: unknown capability " + id)
	}
}

func implFor(capabilityID string) string {
	switch capabilityID {
	case CapabilityOverview:
		return ImplOverview
	case CapabilityCalls:
		return ImplCalls
	default:
		panic("implFor: unknown capability " + capabilityID)
	}
}

func request(t *testing.T, repo contract.Repository, capabilityID string, payload map[string]any) contract.RunRequest {
	t.Helper()
	return contract.RunRequest{
		Capability:     capabilityFor(capabilityID),
		Implementation: contract.Implementation{ID: implFor(capabilityID), Provider: "tokensave", Capability: capabilityID},
		Repository:     repo,
		Payload:        payload,
		Permission:     contract.Permission{Task: "probe", Effects: []contract.Effect{contract.EffectRead}},
	}
}

// workspace lays out a served root with one repository inside it, which is the
// arrangement this adapter exists for: the root is the project tokensave
// serves, the repository is a directory under it.
func workspace(t *testing.T) (root string, repo contract.Repository) {
	t.Helper()
	root = t.TempDir()
	path := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(filepath.Join(path, "internal"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root, contract.NewRepository("api", path, []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
}

func writeFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func newTestRunner(t *testing.T, root string, sess *mcpstdio.Session) *Runner {
	t.Helper()
	runner, err := New(Options{
		Root:      root,
		Sensitive: []string{".env"},
		Session:   func(context.Context) (*mcpstdio.Session, error) { return sess, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner
}

func rows(t *testing.T, out contract.Outcome, key string) []map[string]any {
	t.Helper()
	raw, ok := out.Result[key].([]any)
	if !ok {
		t.Fatalf("result[%q] = %#v, want a list", key, out.Result[key])
	}
	list := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		record, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("row %#v is not a record", item)
		}
		list = append(list, record)
	}
	return list
}

// --- tests -----------------------------------------------------------------

// The root-relative translation is the whole reason this adapter has a Root:
// the capability speaks "internal/client.go" and the far side speaks
// "services/api/internal/client.go", in both directions.
func TestRunOverviewTranslatesPathsAcrossTheRootPrefix(t *testing.T) {
	root, repo := workspace(t)
	writeFile(t, filepath.Join(repo.Path, "internal", "client.go"), strings.Join([]string{
		"package redis",
		"",
		"type Client struct {",
		"\tcfg Config",
		"}",
		"",
		"func NewClient(cfg Config) *Client {",
		"\treturn &Client{cfg: cfg}",
		"}",
	}, "\n"))
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, readyStatus, false)
	fake.on(toolEntities, `{"file":"services/api/internal/client.go","symbol_count":6,"symbols":[
		{"kind":"file","name":"services/api/internal/client.go","line":1,"end_line":9},
		{"kind":"go_package","name":"redis","line":1,"end_line":1},
		{"kind":"use","name":"context","line":1,"end_line":1},
		{"kind":"struct","name":"Client","line":3,"end_line":5},
		{"kind":"field","name":"cfg","line":4,"end_line":4},
		{"kind":"function","name":"NewClient","line":7,"end_line":9}]}`, false)

	out, err := newTestRunner(t, root, sess).Run(context.Background(),
		request(t, repo, CapabilityOverview, map[string]any{"file": "internal/client.go"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	asked := fake.callsTo(toolEntities)
	if len(asked) != 1 || asked[0]["file"] != "services/api/internal/client.go" {
		t.Fatalf("entities was asked %#v, want one call for the root-relative path", asked)
	}
	got := rows(t, out, "symbols")
	if len(got) != 2 {
		t.Fatalf("symbols = %#v, want the struct and the function only", got)
	}
	// The pseudo rows -- the file node, the package clause, the import -- are
	// not declarations of this file's own symbols; the field is nested and
	// belongs to depth > 0.
	for _, row := range got {
		if row["kind"] == "file" || row["kind"] == "use" || row["kind"] == "go_package" || row["kind"] == "field" {
			t.Fatalf("row %#v should not be in a depth-0 answer", row)
		}
	}
	// The column is recovered from the file, because no provider reports one:
	// "type Client struct {" puts the name at column 6.
	if got[0]["name"] != "Client" || got[0]["column"] != 6 {
		t.Fatalf("first row = %#v, want Client at column 6", got[0])
	}
	if got[1]["name"] != "NewClient" || got[1]["column"] != 6 {
		t.Fatalf("second row = %#v, want NewClient at column 6", got[1])
	}
}

// depth > 0 is the only way a nested declaration comes back, and it comes back
// naming what encloses it.
func TestRunOverviewDescendsAndNamesTheParent(t *testing.T) {
	root, repo := workspace(t)
	writeFile(t, filepath.Join(repo.Path, "internal", "client.go"),
		"package redis\n\ntype Client struct {\n\tcfg Config\n}\n")
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, readyStatus, false)
	fake.on(toolEntities, `{"symbols":[
		{"kind":"struct","name":"Client","line":3,"end_line":5},
		{"kind":"field","name":"cfg","line":4,"end_line":4}]}`, false)

	out, err := newTestRunner(t, root, sess).Run(context.Background(),
		request(t, repo, CapabilityOverview, map[string]any{"file": "internal/client.go", "depth": 1}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rows(t, out, "symbols")
	if len(got) != 2 {
		t.Fatalf("symbols = %#v, want the struct and its field", got)
	}
	if got[1]["name"] != "cfg" || got[1]["parent"] != "Client" {
		t.Fatalf("nested row = %#v, want cfg inside Client", got[1])
	}
}

// A file matching a sensitive pattern is never read, and unlike a search hit
// it cannot be dropped in silence: the caller named this one file, so an empty
// answer would claim it was listed and holds nothing.
func TestRunOverviewRefusesASensitiveFile(t *testing.T) {
	root, repo := workspace(t)
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, readyStatus, false)

	_, err := newTestRunner(t, root, sess).Run(context.Background(),
		request(t, repo, CapabilityOverview, map[string]any{"file": ".env"}))
	if err == nil {
		t.Fatal("Run answered for a sensitive file, want permission_denied")
	}
	var failure *contract.Failure
	if !asFailure(err, &failure) || failure.Kind != contract.FailurePermissionDenied {
		t.Fatalf("Run failed with %v, want permission_denied", err)
	}
	if calls := fake.callsTo(toolEntities); len(calls) != 0 {
		t.Fatalf("entities was called %#v for a sensitive file", calls)
	}
}

// symbol.calls resolves a position through the outline, looks the id up by
// name, and walks one direction per way asked for.
func TestRunCallsResolvesThePositionAndWalksBothDirections(t *testing.T) {
	root, repo := workspace(t)
	writeFile(t, filepath.Join(repo.Path, "internal", "client.go"),
		"package redis\n\nfunc NewClient() {}\n")
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, readyStatus, false)
	fake.on(toolEntities, `{"symbols":[
		{"kind":"file","name":"services/api/internal/client.go","line":1,"end_line":3},
		{"kind":"struct","name":"Client","line":1,"end_line":9},
		{"kind":"function","name":"NewClient","line":3,"end_line":3}]}`, false)
	fake.on(toolExact, `{"name":"NewClient","count":2,"matches":[
		{"id":"function:other","name":"NewClient","kind":"function","file":"services/other/client.go","line":3},
		{"id":"function:mine","name":"NewClient","kind":"function","file":"services/api/internal/client.go","line":3}]}`, false)
	fake.on(toolCallers, `[
		{"node_id":"function:a","name":"NewAdapter","kind":"function","file":"services/api/internal/adapter.go","line":44,"depth":1},
		{"node_id":"function:b","name":"main","kind":"function","file":"services/web/main.go","line":10,"depth":1}]`, false)
	fake.on(toolCallees, `[
		{"node_id":"function:c","name":"Dial","kind":"function","file":"services/api/internal/dial.go","line":7,"depth":1}]`, false)

	out, err := newTestRunner(t, root, sess).Run(context.Background(),
		request(t, repo, CapabilityCalls, map[string]any{
			"file": "internal/client.go", "line": 3, "column": 6, "direction": "both",
		}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The innermost declaration containing line 3 is the function, not the
	// struct whose span also covers it.
	if asked := fake.callsTo(toolExact); len(asked) != 1 || asked[0]["name"] != "NewClient" {
		t.Fatalf("find_exact_symbol was asked %#v, want the innermost declaration's name", asked)
	}
	// The id is picked by file and line, never by name alone: the same name
	// exists in another repository under the same root.
	for _, tool := range []string{toolCallers, toolCallees} {
		asked := fake.callsTo(tool)
		if len(asked) != 1 || asked[0]["node_id"] != "function:mine" {
			t.Fatalf("%s was asked %#v, want the id matching this file and line", tool, asked)
		}
		if asked[0]["max_depth"] != float64(defaultDepth) {
			t.Fatalf("%s max_depth = %#v, want the default %d", tool, asked[0]["max_depth"], defaultDepth)
		}
	}

	got := rows(t, out, "calls")
	if len(got) != 2 {
		t.Fatalf("calls = %#v, want the two hops inside this repository", got)
	}
	if got[0]["path"] != "internal/adapter.go" || got[0]["direction"] != directionIncoming {
		t.Fatalf("incoming row = %#v, want a repository-relative incoming hop", got[0])
	}
	if got[1]["path"] != "internal/dial.go" || got[1]["direction"] != directionOutgoing {
		t.Fatalf("outgoing row = %#v, want a repository-relative outgoing hop", got[1])
	}
	// The hop in services/web is real and has nowhere to go in a
	// repository-relative answer, so it is reported rather than dropped in
	// silence.
	if !hasNote(out, "outside") {
		t.Fatalf("discoveries = %#v, want one naming the calls outside the repository", out.Discoveries)
	}
}

// A position inside no declaration is a question with no subject, not a symbol
// with no calls.
func TestRunCallsRefusesAPositionInsideNoDeclaration(t *testing.T) {
	root, repo := workspace(t)
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, readyStatus, false)
	fake.on(toolEntities, `{"symbols":[{"kind":"function","name":"NewClient","line":3,"end_line":3}]}`, false)

	_, err := newTestRunner(t, root, sess).Run(context.Background(),
		request(t, repo, CapabilityCalls, map[string]any{
			"file": "internal/client.go", "line": 99, "column": 1, "direction": "incoming",
		}))
	var failure *contract.Failure
	if err == nil || !asFailure(err, &failure) || failure.Kind != contract.FailureNotFound {
		t.Fatalf("Run failed with %v, want not_found", err)
	}
	if calls := fake.callsTo(toolExact); len(calls) != 0 {
		t.Fatalf("find_exact_symbol was called %#v with nothing resolved", calls)
	}
}

// The empty-graph guard: counts at zero is a provider with nothing to answer
// from, not a file that declares nothing.
func TestRunRefusesAnEmptyGraph(t *testing.T) {
	root, repo := workspace(t)
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, emptyStatus, false)
	fake.on(toolEntities, `{"symbols":[{"kind":"function","name":"NewClient","line":3,"end_line":3}]}`, false)

	_, err := newTestRunner(t, root, sess).Run(context.Background(),
		request(t, repo, CapabilityOverview, map[string]any{"file": "internal/client.go"}))
	var failure *contract.Failure
	if err == nil || !asFailure(err, &failure) || failure.Kind != contract.FailureUnavailable {
		t.Fatalf("Run failed with %v, want unavailable", err)
	}
	if calls := fake.callsTo(toolEntities); len(calls) != 0 {
		t.Fatalf("entities was called %#v on a graph that holds nothing", calls)
	}
}

// A repository outside the served root is refused before any tool is called:
// there is no path on that wire that could name it.
func TestRunRefusesARepositoryOutsideTheRoot(t *testing.T) {
	root, _ := workspace(t)
	outside := contract.NewRepository("elsewhere", t.TempDir(), nil,
		contract.ScaleSmall, contract.VCSUnspecified, nil)
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, readyStatus, false)

	_, err := newTestRunner(t, root, sess).Run(context.Background(),
		request(t, outside, CapabilityOverview, map[string]any{"file": "internal/client.go"}))
	var failure *contract.Failure
	if err == nil || !asFailure(err, &failure) || failure.Kind != contract.FailureUnavailable {
		t.Fatalf("Run failed with %v, want unavailable", err)
	}
	if calls := fake.callsTo(toolStatus); len(calls) != 0 {
		t.Fatalf("status was called %#v for a repository this server cannot address", calls)
	}
}

// A path climbing out of the repository is refused, whatever it says.
func TestToRootRefusesAPathLeavingTheRepository(t *testing.T) {
	if _, err := toRoot("services/api", "../other/secret.go", "api"); err == nil {
		t.Fatal("toRoot accepted a path leaving the repository")
	}
	got, err := toRoot("services/api", "internal/client.go", "api")
	if err != nil || got != "services/api/internal/client.go" {
		t.Fatalf("toRoot = %q, %v; want the root-relative path", got, err)
	}
	if got, ok := toRepository("services/api", "services/web/main.go"); ok {
		t.Fatalf("toRepository accepted a foreign path as %q", got)
	}
}

func hasNote(out contract.Outcome, substring string) bool {
	for _, discovery := range out.Discoveries {
		if strings.Contains(discovery.Note, substring) {
			return true
		}
	}
	return false
}

// asFailure keeps the errors.As dance out of every assertion above.
func asFailure(err error, target **contract.Failure) bool {
	var failure *contract.Failure
	if !errors.As(err, &failure) {
		return false
	}
	*target = failure
	return true
}

// The real server appends its own accounting line after the JSON, outside it.
// Decoding the whole text fails on that line, and the failure looks like a
// provider that is down rather than a client that cannot read.
func TestPayloadOfCutsTheTrailingMetricsLine(t *testing.T) {
	const body = `{"node_count":1,"edge_count":2,"file_count":3}`
	text := body + "\n\n\ntokensave_metrics: before=812 after=660 saved=152"
	var status statusAnswer
	if err := json.Unmarshal(payloadOf(text), &status); err != nil {
		t.Fatalf("decoding a real tool result: %v", err)
	}
	if status.Nodes != 1 || status.Edges != 2 || status.Files != 3 {
		t.Fatalf("status = %#v, want the three counts", status)
	}
	if got := string(payloadOf(body)); got != body {
		t.Fatalf("payloadOf on a plain answer = %q, want it untouched", got)
	}
}

// The guard has to survive that same line, or every capability comes back
// unavailable against the real server while every fake-backed test passes.
func TestRunSurvivesTheMetricsLineOnEveryCall(t *testing.T) {
	root, repo := workspace(t)
	writeFile(t, filepath.Join(repo.Path, "internal", "client.go"), "package redis\n\nfunc NewClient() {}\n")
	const metrics = "\n\ntokensave_metrics: before=812 after=660 saved=152"
	fake, sess := newFakeTokensave(t)
	fake.on(toolStatus, readyStatus+metrics, false)
	fake.on(toolEntities, `{"symbols":[{"kind":"function","name":"NewClient","line":3,"end_line":3}]}`+metrics, false)

	out, err := newTestRunner(t, root, sess).Run(context.Background(),
		request(t, repo, CapabilityOverview, map[string]any{"file": "internal/client.go"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rows(t, out, "symbols"); len(got) != 1 || got[0]["name"] != "NewClient" {
		t.Fatalf("symbols = %#v, want the one declaration", got)
	}
}
