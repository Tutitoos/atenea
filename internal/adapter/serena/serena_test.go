package serena

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// This adapter has two seams and the tests follow them. One is the wire: what
// Serena is asked and what its answer becomes, exercised against a stub that
// speaks the shapes measured off the live server. The other is the translation
// that has nothing to do with the wire at all -- Atenea names a position,
// Serena names a symbol, and one of them has to give.
//
// The stub is not a convenience. Serena needs a language server per language
// and a warm index, so a unit test against the real one would be testing this
// machine rather than this code.

// stub is a fake MCP server. It records what it was asked and answers with
// canned text, which is the only thing the adapter is entitled to look at.
type stub struct {
	mu sync.Mutex
	// calls records every tools/call in order: tool name and arguments.
	calls []stubCall
	// answers maps a tool name to the text it returns. A tool with no entry
	// answers with an empty JSON array, which is a legitimate "nothing".
	answers map[string]string
	// errors maps a tool name to an MCP error text, which is what Serena
	// sends when the tool itself failed rather than the transport.
	errors map[string]string
	// noSession drops the session header, which is how a broken proxy looks.
	noSession bool
}

type stubCall struct {
	Tool string
	Args map[string]any
}

func newStub(t *testing.T) (*stub, string) {
	t.Helper()
	s := &stub{answers: map[string]string{}, errors: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	return s, server.URL + "/mcp"
}

func (s *stub) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     int             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch req.Method {
	case "initialize":
		if !s.noSession {
			w.Header().Set("Mcp-Session-Id", "test-session")
		}
		// SSE framing, because that is what the live server sends and the
		// adapter has to survive it.
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n",
			fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05"}}`, req.ID))
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/call":
		var params struct {
			Name string         `json:"name"`
			Args map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &params)
		s.mu.Lock()
		s.calls = append(s.calls, stubCall{Tool: params.Name, Args: params.Args})
		text, isError := s.errors[params.Name], true
		if text == "" {
			text, isError = s.answers[params.Name], false
			if text == "" {
				text = "[]"
			}
		}
		s.mu.Unlock()
		result, _ := json.Marshal(map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isError,
		})
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(result)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	default:
		http.Error(w, "unexpected method "+req.Method, http.StatusBadRequest)
	}
}

func (s *stub) called(tool string) (stubCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, call := range s.calls {
		if call.Tool == tool {
			return call, true
		}
	}
	return stubCall{}, false
}

func (s *stub) toolNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	for i, call := range s.calls {
		out[i] = call.Tool
	}
	return out
}

// The three capabilities, mirroring what default.toml ships. The adapter has
// to produce something that passes the declared schema, not something that
// merely looks right, so the shapes are copied rather than simplified.
func definitionCapability() contract.Capability {
	return contract.Capability{
		ID: CapabilityDefinition, Version: contract.Version{Major: 1},
		Summary: "Find where a symbol is born.",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "file", Type: contract.TypeString, Required: true},
			{Name: "line", Type: contract.TypeInt, Required: true},
			{Name: "column", Type: contract.TypeInt, Required: true},
			{Name: "name", Type: contract.TypeString},
			{Name: "snippet", Type: contract.TypeBool},
		},
		Outputs: []contract.Field{{
			Name: "location", Type: contract.TypeRecord, Required: true,
			Fields: []contract.Field{
				{Name: "path", Type: contract.TypeString, Required: true},
				{Name: "line", Type: contract.TypeInt, Required: true},
				{Name: "snippet", Type: contract.TypeString},
			},
		}},
	}
}

func listCapability(id string) contract.Capability {
	return contract.Capability{
		ID: id, Version: contract.Version{Major: 1},
		Summary: "Find every use of a symbol.",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "file", Type: contract.TypeString, Required: true},
			{Name: "line", Type: contract.TypeInt, Required: true},
			{Name: "column", Type: contract.TypeInt, Required: true},
			{Name: "name", Type: contract.TypeString},
			{Name: "scope", Type: contract.TypeStringList},
			{Name: "snippet", Type: contract.TypeBool},
		},
		Outputs: []contract.Field{{
			Name: "locations", Type: contract.TypeRecordList, Required: true,
			Fields: []contract.Field{
				{Name: "path", Type: contract.TypeString, Required: true},
				{Name: "line", Type: contract.TypeInt, Required: true},
				{Name: "snippet", Type: contract.TypeString},
			},
		}},
	}
}

func capabilityFor(id string) contract.Capability {
	if id == CapabilityDefinition {
		return definitionCapability()
	}
	return listCapability(id)
}

func implFor(capabilityID string) contract.Implementation {
	return contract.Implementation{
		ID:         strings.Replace(capabilityID, "symbol.", "serena.", 1),
		Provider:   "serena",
		Capability: capabilityID,
	}
}

// repo writes a source tree and returns it as a repository. The adapter reads
// the file to find the word under the cursor, so a real file has to exist.
func repo(t *testing.T, files map[string]string) contract.Repository {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	return contract.NewRepository("current", root, []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
}

func newRunner(t *testing.T, endpoint string) *Runner {
	t.Helper()
	runner, err := New(Options{
		Endpoint:        endpoint,
		Implementations: DefaultImplementations(),
		Sensitive:       []string{".env", "*.pem", "credentials.json"},
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner
}

func run(t *testing.T, runner *Runner, capabilityID string, r contract.Repository, payload map[string]any) (contract.Outcome, error) {
	t.Helper()
	return runner.Run(t.Context(), contract.RunRequest{
		Capability:     capabilityFor(capabilityID),
		Implementation: implFor(capabilityID),
		Repository:     r,
		Payload:        payload,
		Permission: contract.Permission{
			Task:    "resolve a symbol",
			Effects: []contract.Effect{contract.EffectRead},
		},
	})
}

// The measured answer shapes. find_symbol returns a list; the reference tools
// return path -> kind -> entries with a rendered context block. Both were read
// off the live server before any of this was written.
const symbolAnswer = `[{"name_path":"Shape/area","kind":"Method","relative_path":"pkg/shapes.go","body_location":{"start_line":1,"end_line":2}}]`

const referenceAnswer = `{"main.go":{"Function":[{"name_path":"twice","body_location":{"start_line":6,"end_line":8},` +
	`"content_around_reference":"...   7:func twice() int {\n  >   8:\treturn area() * 2\n...   9:}"}]}}`

// ---------------------------------------------------------------------------
// The translation: position in, name out
// ---------------------------------------------------------------------------

// The whole reason this adapter is more than a passthrough. Atenea's contract
// names a position because that is what an editor has; Serena's API names a
// symbol. Something has to read the word under the cursor, and it is here.
func TestThePositionBecomesTheNameSerenaIsAsked(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	runner := newRunner(t, endpoint)

	_, err := run(t, runner, CapabilityDefinition, repo(t, map[string]string{
		"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n",
	}), map[string]any{"file": "pkg/shapes.go", "line": 3, "column": 6})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	call, ok := s.called("find_symbol")
	if !ok {
		t.Fatalf("find_symbol was never called; tools = %v", s.toolNames())
	}
	if got := call.Args["name_path_pattern"]; got != "area" {
		t.Errorf("name_path_pattern = %v, want the word under the cursor", got)
	}
	if got := call.Args["relative_path"]; got != "pkg/shapes.go" {
		t.Errorf("relative_path = %v", got)
	}
}

// The payload's name is documented as a hint. Believing it over the file would
// let a caller with a stale editor buffer ask about a symbol that is not there
// and get a confident answer about the wrong one.
func TestThePositionBeatsTheHint(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	runner := newRunner(t, endpoint)

	outcome, err := run(t, runner, CapabilityDefinition, repo(t, map[string]string{
		"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n",
	}), map[string]any{
		"file": "pkg/shapes.go", "line": 3, "column": 6, "name": "perimeter",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	call, _ := s.called("find_symbol")
	if got := call.Args["name_path_pattern"]; got != "area" {
		t.Fatalf("name_path_pattern = %v, want the file to win over the hint", got)
	}
	// And the disagreement is said out loud, because a caller who believes
	// their own hint needs to know it was overruled.
	var notes []string
	for _, d := range outcome.Discoveries {
		notes = append(notes, d.Note)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "perimeter") || !strings.Contains(joined, "position wins") {
		t.Errorf("the overruled hint is not in the trace: %s", joined)
	}
}

// A position inside whitespace names nothing. Sending an empty pattern would
// make Serena answer about every symbol in the file.
func TestAPositionOnNothingIsABadRequest(t *testing.T) {
	s, endpoint := newStub(t)
	runner := newRunner(t, endpoint)

	_, err := run(t, runner, CapabilityDefinition, repo(t, map[string]string{
		"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n",
	}), map[string]any{"file": "pkg/shapes.go", "line": 3, "column": 5})
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input; err = %v", contract.KindOf(err), err)
	}
	if len(s.toolNames()) != 0 {
		t.Errorf("serena was asked anyway: %v", s.toolNames())
	}
}

// Serena reports 0-based lines and the capability declares 1-based ones. The
// conversion has exactly one place to be wrong, so it gets its own test.
func TestSerenaLinesArriveOneBased(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	runner := newRunner(t, endpoint)

	outcome, err := run(t, runner, CapabilityDefinition, repo(t, map[string]string{
		"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n",
	}), map[string]any{"file": "pkg/shapes.go", "line": 3, "column": 6})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	location, ok := outcome.Result["location"].(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want a location record", outcome.Result)
	}
	// start_line 1 in the answer above.
	if location["line"] != 2 {
		t.Errorf("line = %v, want 2: serena's 1 is the contract's 2", location["line"])
	}
}

// ---------------------------------------------------------------------------
// The answers coming back
// ---------------------------------------------------------------------------

// A reference is a line that MENTIONS the symbol. Serena's entry also carries
// the enclosing function's body_location, and returning that would point the
// caller at a definition nobody asked about.
func TestAReferenceIsTheReferringLineNotItsFunction(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	s.answers["find_referencing_symbols"] = referenceAnswer
	runner := newRunner(t, endpoint)

	outcome, err := run(t, runner, CapabilityReferences, repo(t, map[string]string{
		"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n",
	}), map[string]any{"file": "pkg/shapes.go", "line": 3, "column": 6})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	locations, _ := outcome.Result["locations"].([]any)
	if len(locations) != 1 {
		t.Fatalf("locations = %#v, want one", outcome.Result["locations"])
	}
	got := locations[0].(map[string]any)
	// The marked line is 8, 0-based, so 9. The enclosing function starts at 6
	// and would arrive as 7 -- that is the wrong answer this test exists for.
	if got["line"] != 9 {
		t.Errorf("line = %v, want 9: the marked line, not the function around it", got["line"])
	}
	if !strings.Contains(got["snippet"].(string), "area() * 2") {
		t.Errorf("snippet = %v, want the referring line", got["snippet"])
	}
}

// Serena has no scope parameter for the reference tools, so a caller that asks
// about one directory and is handed the whole repository was answered a
// different question. The adapter narrows rather than leaking the gap.
func TestScopeIsEnforcedHereBecauseSerenaCannot(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	s.answers["find_referencing_symbols"] = `{"main.go":{"Function":[{"name_path":"a","body_location":{"start_line":0}}]},` +
		`"pkg/other.go":{"Function":[{"name_path":"b","body_location":{"start_line":4}}]}}`
	runner := newRunner(t, endpoint)

	outcome, err := run(t, runner, CapabilityReferences, repo(t, map[string]string{
		"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n",
	}), map[string]any{
		"file": "pkg/shapes.go", "line": 3, "column": 6, "scope": []any{"pkg"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	locations, _ := outcome.Result["locations"].([]any)
	if len(locations) != 1 {
		t.Fatalf("locations = %#v, want only the one inside pkg/", outcome.Result["locations"])
	}
	if got := locations[0].(map[string]any)["path"]; got != "pkg/other.go" {
		t.Errorf("path = %v, want the in-scope hit", got)
	}
}

// Two runs of the same commission have to answer the same thing in the same
// order. Serena keys references by path and a Go map has none.
func TestTheSameQuestionAnswersInTheSameOrder(t *testing.T) {
	answer := `{"z.go":{"Function":[{"name_path":"a","body_location":{"start_line":0}}]},` +
		`"a.go":{"Function":[{"name_path":"b","body_location":{"start_line":4}}]},` +
		`"m.go":{"Function":[{"name_path":"c","body_location":{"start_line":2}}]}}`
	want := []string{"a.go", "m.go", "z.go"}
	for i := range 8 {
		got, err := parseReferences(answer)
		if err != nil {
			t.Fatalf("parseReferences: %v", err)
		}
		paths := make([]string, len(got))
		for j, loc := range got {
			paths[j] = loc.Path
		}
		for j := range want {
			if paths[j] != want[j] {
				t.Fatalf("run %d: paths = %v, want %v", i, paths, want)
			}
		}
	}
}

// A definition is one place by declaration. Serena can match a pattern more
// than once, and handing the extras back would fail the schema at the door.
func TestADefinitionIsOnePlace(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = `[{"name_path":"area","relative_path":"a.go","body_location":{"start_line":0,"end_line":1}},` +
		`{"name_path":"area","relative_path":"b.go","body_location":{"start_line":0,"end_line":1}}]`
	runner := newRunner(t, endpoint)

	outcome, err := run(t, runner, CapabilityDefinition, repo(t, map[string]string{
		"a.go": "package a\nfunc area() int { return 1 }\n",
	}), map[string]any{"file": "a.go", "line": 2, "column": 6})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := outcome.Result["location"].(map[string]any); !ok {
		t.Fatalf("result = %#v, want exactly one location record", outcome.Result)
	}
}

// Nobody referring to a symbol is an answer, not a failure. Turning it into
// one would make the funnel retry a provider that already answered correctly.
func TestNoReferencesIsAnAnswer(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	s.answers["find_referencing_symbols"] = "{}"
	runner := newRunner(t, endpoint)

	outcome, err := run(t, runner, CapabilityReferences, repo(t, map[string]string{
		"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n",
	}), map[string]any{"file": "pkg/shapes.go", "line": 3, "column": 6})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Verdict != contract.VerdictOK {
		t.Errorf("verdict = %v, want ok", outcome.Verdict)
	}
	if locations, _ := outcome.Result["locations"].([]any); len(locations) != 0 {
		t.Errorf("locations = %#v, want empty", locations)
	}
}

// Measured against a live server: find_implementations answers zero hits
// with "[]", not the "{}" find_referencing_symbols uses. Both are Serena
// saying nothing matched, and both have to read the same way -- a symbol
// with no implementations is exactly as much an answer as one with no
// references.
func TestNoImplementationsIsAnAnswer(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	s.answers["find_implementations"] = "[]"
	runner := newRunner(t, endpoint)

	outcome, err := run(t, runner, CapabilityImplementations, repo(t, map[string]string{
		"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n",
	}), map[string]any{"file": "pkg/shapes.go", "line": 3, "column": 6})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Verdict != contract.VerdictOK {
		t.Errorf("verdict = %v, want ok", outcome.Verdict)
	}
	if locations, _ := outcome.Result["locations"].([]any); len(locations) != 0 {
		t.Errorf("locations = %#v, want empty", locations)
	}
}

// ---------------------------------------------------------------------------
// Per-repo endpoints and the retarget note
// ---------------------------------------------------------------------------

// A repository that names its own Serena URL must hit that URL, not the
// adapter default. Otherwise the dedicated warm unit is dead weight and the
// default endpoint still pays the retarget tax.
func TestARepositoryEndpointOverridesTheDefault(t *testing.T) {
	defaultStub, defaultURL := newStub(t)
	repoStub, repoURL := newStub(t)
	repoStub.answers["find_symbol"] = symbolAnswer
	defaultStub.answers["find_symbol"] = symbolAnswer

	runner := newRunner(t, defaultURL)
	r := repo(t, map[string]string{
		"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n",
	})
	r.SerenaEndpoint = repoURL

	if _, err := run(t, runner, CapabilityDefinition, r, map[string]any{
		"file": "pkg/shapes.go", "line": 3, "column": 6,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(defaultStub.toolNames()) != 0 {
		t.Errorf("default endpoint was contacted: %v", defaultStub.toolNames())
	}
	if _, ok := repoStub.called("activate_project"); !ok {
		t.Fatal("repo endpoint was never asked to activate")
	}
	if _, ok := repoStub.called("find_symbol"); !ok {
		t.Fatal("repo endpoint was never asked for the symbol")
	}
}

// Two repositories on two endpoints must not share a session: a handshake on
// one is meaningless to the other, and locking them together would serialize
// work that two warm processes can do in parallel.
func TestDistinctEndpointsKeepDistinctSessions(t *testing.T) {
	a, aURL := newStub(t)
	b, bURL := newStub(t)
	a.answers["find_symbol"] = symbolAnswer
	b.answers["find_symbol"] = symbolAnswer

	runner := newRunner(t, aURL)
	ra := repo(t, map[string]string{"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n"})
	// leave ra on the default (aURL)
	rb := repo(t, map[string]string{"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n"})
	rb.SerenaEndpoint = bURL

	if _, err := run(t, runner, CapabilityDefinition, ra, map[string]any{
		"file": "pkg/shapes.go", "line": 3, "column": 6,
	}); err != nil {
		t.Fatalf("ra: %v", err)
	}
	if _, err := run(t, runner, CapabilityDefinition, rb, map[string]any{
		"file": "pkg/shapes.go", "line": 3, "column": 6,
	}); err != nil {
		t.Fatalf("rb: %v", err)
	}
	if len(a.toolNames()) == 0 || len(b.toolNames()) == 0 {
		t.Fatalf("both endpoints must have been used; a=%v b=%v", a.toolNames(), b.toolNames())
	}
	// Two conns in the map.
	runner.connsMu.Lock()
	n := len(runner.conns)
	runner.connsMu.Unlock()
	if n != 2 {
		t.Fatalf("conns = %d, want 2", n)
	}
}

// A real project switch on a shared endpoint is the slow multi-repo tax. The
// discovery list has to say so, or a commission that alternates repos looks
// silently slow in the trace.
func TestARetargetLeavesADiscoveryNote(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	runner := newRunner(t, endpoint)

	r1 := repo(t, map[string]string{"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n"})
	r2 := repo(t, map[string]string{"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n"})
	// Distinct absolute paths so activate sees a switch.
	if r1.Path == r2.Path {
		t.Fatal("test setup produced identical roots")
	}

	if _, err := run(t, runner, CapabilityDefinition, r1, map[string]any{
		"file": "pkg/shapes.go", "line": 3, "column": 6,
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	out, err := run(t, runner, CapabilityDefinition, r2, map[string]any{
		"file": "pkg/shapes.go", "line": 3, "column": 6,
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	var found bool
	for _, d := range out.Discoveries {
		if strings.Contains(d.Note, "serena retargeted") &&
			strings.Contains(d.Note, r1.Path) &&
			strings.Contains(d.Note, r2.Path) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("discoveries = %#v, want a retarget note naming both roots", out.Discoveries)
	}
}

// The first activation on an endpoint is not a retarget: there was nothing to
// tear down. Naming it as one would cry wolf on every cold start.
func TestFirstActivationIsNotARetargetNote(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	runner := newRunner(t, endpoint)

	out, err := run(t, runner, CapabilityDefinition, repo(t, map[string]string{
		"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n",
	}), map[string]any{"file": "pkg/shapes.go", "line": 3, "column": 6})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, d := range out.Discoveries {
		if strings.Contains(d.Note, "serena retargeted") {
			t.Fatalf("first call carried a retarget note: %q", d.Note)
		}
	}
}

// Staying on the same repository must not claim a retarget either: the skip
// path is the whole point of caching active on the conn.
func TestSameRepositoryDoesNotRetarget(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	runner := newRunner(t, endpoint)
	r := repo(t, map[string]string{"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n"})

	if _, err := run(t, runner, CapabilityDefinition, r, map[string]any{
		"file": "pkg/shapes.go", "line": 3, "column": 6,
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	out, err := run(t, runner, CapabilityDefinition, r, map[string]any{
		"file": "pkg/shapes.go", "line": 3, "column": 6,
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	// Second call should not have re-issued activate_project.
	var activates int
	for _, name := range s.toolNames() {
		if name == "activate_project" {
			activates++
		}
	}
	if activates != 1 {
		t.Errorf("activate_project called %d times, want 1", activates)
	}
	for _, d := range out.Discoveries {
		if strings.Contains(d.Note, "serena retargeted") {
			t.Fatalf("same-repo call carried a retarget note: %q", d.Note)
		}
	}
}

// ---------------------------------------------------------------------------
// Security and the bins
// ---------------------------------------------------------------------------

// This is the only adapter that opens a file to do its job, so the sensitive
// list is not advisory here. Exploring skips these in silence because a missed
// hit costs nothing; a caller pointing at one exact position is refused out
// loud, because "nothing here" would be a lie.
func TestASecretFileIsRefusedRatherThanRead(t *testing.T) {
	s, endpoint := newStub(t)
	runner := newRunner(t, endpoint)

	_, err := run(t, runner, CapabilityDefinition, repo(t, map[string]string{
		".env": "TOKEN=hunter2\n",
	}), map[string]any{"file": ".env", "line": 1, "column": 1})
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied; err = %v", contract.KindOf(err), err)
	}
	if len(s.toolNames()) != 0 {
		t.Errorf("serena was asked about a secret file: %v", s.toolNames())
	}
}

// A step carries permission for the unit of work it was commissioned against.
// A path that climbs out of it is reading something nobody authorized.
func TestAPathThatLeavesTheRepositoryIsRefused(t *testing.T) {
	_, endpoint := newStub(t)
	runner := newRunner(t, endpoint)

	for _, file := range []string{"../escape.go", "a/../../escape.go"} {
		t.Run(file, func(t *testing.T) {
			_, err := run(t, runner, CapabilityDefinition, repo(t, map[string]string{
				"a/keep.go": "package a\n",
			}), map[string]any{"file": file, "line": 1, "column": 1})
			if contract.KindOf(err) != contract.FailurePermissionDenied {
				t.Fatalf("kind = %v, want permission_denied; err = %v", contract.KindOf(err), err)
			}
		})
	}
}

// A language server that does not implement the request is a provider that
// cannot work here, which is what unavailable means: the funnel falls back to
// somebody who can. Measured -- a Python server refuses textDocument/
// implementation outright.
func TestALanguageServerThatCannotAnswerIsUnavailable(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	s.errors["find_implementations"] = "Error executing tool find_implementations: SolidLSPException: " +
		"Unhandled method textDocument/implementation (-32601)"
	runner := newRunner(t, endpoint)

	_, err := run(t, runner, CapabilityImplementations, repo(t, map[string]string{
		"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n",
	}), map[string]any{"file": "pkg/shapes.go", "line": 3, "column": 6})
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable; err = %v", contract.KindOf(err), err)
	}
	// The untranslated text travels beside the bin for whoever debugs later.
	var failure *contract.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("err is not a contract failure: %v", err)
	}
	if !strings.Contains(failure.Raw, "textDocument/implementation") {
		t.Errorf("Raw = %q, want the far side's own words", failure.Raw)
	}
}

// A proxy that answers without a session is broken, not slow. Retrying against
// it would hang every commission behind a server that will never work.
func TestAHandshakeWithoutASessionIsUnavailable(t *testing.T) {
	s, endpoint := newStub(t)
	s.noSession = true
	runner := newRunner(t, endpoint)

	_, err := run(t, runner, CapabilityDefinition, repo(t, map[string]string{
		"a.go": "package a\nfunc area() int { return 1 }\n",
	}), map[string]any{"file": "a.go", "line": 2, "column": 6})
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable; err = %v", contract.KindOf(err), err)
	}
}

// The adapter answers for the implementations it was configured with and no
// others. A runner that took work it does not serve would silently become the
// far side of a provider nobody wired up.
func TestAnImplementationItDoesNotServeIsRefused(t *testing.T) {
	_, endpoint := newStub(t)
	runner := newRunner(t, endpoint)

	_, err := runner.Run(t.Context(), contract.RunRequest{
		Capability:     definitionCapability(),
		Implementation: contract.Implementation{ID: "other.definition", Provider: "other", Capability: CapabilityDefinition},
		Repository:     repo(t, map[string]string{"a.go": "package a\n"}),
		Payload:        map[string]any{"file": "a.go", "line": 1, "column": 1},
		Permission:     contract.Permission{Task: "x", Effects: []contract.Effect{contract.EffectRead}},
	})
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable; err = %v", contract.KindOf(err), err)
	}
}

// ---------------------------------------------------------------------------
// The session
// ---------------------------------------------------------------------------

// One activation and one session serve every call. Re-activating per request
// would cost a project walk on a repository Serena is already pointed at, and
// the walk is what hangs on a monorepo.
func TestTheProjectIsActivatedOnceNotPerCall(t *testing.T) {
	s, endpoint := newStub(t)
	s.answers["find_symbol"] = symbolAnswer
	s.answers["find_referencing_symbols"] = referenceAnswer
	runner := newRunner(t, endpoint)
	r := repo(t, map[string]string{"pkg/shapes.go": "package pkg\n\nfunc area() int { return 1 }\n"})
	payload := map[string]any{"file": "pkg/shapes.go", "line": 3, "column": 6}

	for _, capability := range []string{CapabilityDefinition, CapabilityReferences, CapabilityDefinition} {
		if _, err := run(t, runner, capability, r, payload); err != nil {
			t.Fatalf("%s: %v", capability, err)
		}
	}
	activations := 0
	for _, tool := range s.toolNames() {
		if tool == "activate_project" {
			activations++
		}
	}
	if activations != 1 {
		t.Errorf("activate_project ran %d time(s) across three calls, want 1", activations)
	}
}
