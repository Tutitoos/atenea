package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A symbol capability, end to end: the shipped catalog, the real funnel, the
// real Serena adapter, and the CLI verb a client would actually type. Only the
// far side is a stand-in, and it has to be: Serena needs a language server per
// language and a warm index, so a suite that required a live one would be
// testing this machine rather than this code.
//
// The stand-in is not a stub of the adapter. It is an MCP server speaking the
// shapes measured off the real one, so everything between the command line and
// the wire is the production path.

// serenaStub answers the three MCP calls the adapter makes. It records the
// tool names so a test can prove which question actually left the machine.
type serenaStub struct {
	endpoint string
	tools    []string
	answers  map[string]string
}

func newSerenaStub(t *testing.T, answers map[string]string) *serenaStub {
	t.Helper()
	s := &serenaStub{answers: answers}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	s.endpoint = server.URL + "/mcp"
	return s
}

func (s *serenaStub) serve(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Mcp-Session-Id", "end-to-end")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, req.ID)
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/call":
		var params struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &params)
		s.tools = append(s.tools, params.Name)
		text := s.answers[params.Name]
		if text == "" {
			text = "[]"
		}
		result, _ := json.Marshal(map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
		})
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(result),
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	default:
		http.Error(w, "unexpected "+req.Method, http.StatusBadRequest)
	}
}

// symbolInstall boots the shipped defaults, swaps the runner list for Serena
// alone and points it at the stand-in. Everything else -- the capabilities,
// the implementations, their constraints -- is what actually ships, so a
// catalog that drifted would break this test rather than pass it quietly.
func symbolInstall(t *testing.T, answers map[string]string) (*serenaStub, string) {
	t.Helper()
	stub := newSerenaStub(t, answers)

	root := t.TempDir()
	source := "package shapes\n\n" +
		"type Shape interface{ Area() int }\n\n" +
		"func Area() int {\n\treturn 1\n}\n"
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "shapes.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The shipped file, with two knobs turned: who is attached, and where it
	// lives. A hand-written catalog here would stop testing what ships.
	defaults, err := os.ReadFile(filepath.Join("..", "..", "internal", "config", "default.toml"))
	if err != nil {
		t.Fatalf("read defaults: %v", err)
	}
	body := strings.Replace(string(defaults), `runners = ["omp"]`, `runners = ["serena"]`, 1)
	if !strings.Contains(body, `runners = ["serena"]`) {
		t.Fatal("the shipped runner list changed shape; this fixture needs updating")
	}
	// The repository has to declare the index, because the shipped
	// implementations all require one and the funnel checks.
	body = strings.Replace(body, "indexed_by = []", `indexed_by = ["serena"]`, 1)
	shipped := `endpoint = "http://127.0.0.1:40010/mcp"`
	if !strings.Contains(body, shipped) {
		t.Fatal("the shipped serena endpoint changed shape; this fixture needs updating")
	}
	body = strings.Replace(body, shipped, `endpoint = "`+stub.endpoint+`"`, 1)

	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	t.Chdir(root)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ATENEA_CONFIG", "")
	return stub, path
}

const stubSymbol = `[{"name_path":"Area","kind":"Function","relative_path":"pkg/shapes.go",` +
	`"body_location":{"start_line":4,"end_line":6}}]`

// The claim of this brick, stated where nothing can satisfy it by accident:
// a symbol capability goes in at the command line and comes back out as a
// location, having gone through the funnel and a real adapter.
func TestASymbolDefinitionGoesAllTheWayThrough(t *testing.T) {
	stub, settingsPath := symbolInstall(t, map[string]string{"find_symbol": stubSymbol})

	out, err := exec(t, "--config", settingsPath, "ask", "symbol.definition",
		"--repo", "current", "--set", "file=pkg/shapes.go", "--set", "line=5", "--set", "column=8",
		"--trace")
	if err != nil {
		t.Fatalf("ask: %v\n%s", err, out)
	}
	for _, want := range []string{
		"verdict   ok",
		// The funnel chose, and it chose the only provider that can answer.
		"serena.definition",
		// The answer itself reaches the screen, 1-based as the capability
		// declares: Serena's start_line 4 is line 5.
		"line     5",
		"path     pkg/shapes.go",
		"review   child=ok parent=ok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	// And the position really became a name on the way out. Without this the
	// test would pass against an adapter that forwarded the payload verbatim.
	if len(stub.tools) == 0 || stub.tools[0] != "activate_project" {
		t.Errorf("tools = %v, want the project activated first", stub.tools)
	}
	if !containsTool(stub.tools, "find_symbol") {
		t.Errorf("tools = %v, want a symbol lookup", stub.tools)
	}
}

// References go through the same path and come back as a list. The two share
// every stage but the tool they end at, which is exactly the claim.
func TestSymbolReferencesComeBackAsAList(t *testing.T) {
	references := `{"pkg/use.go":{"Function":[{"name_path":"twice",` +
		`"body_location":{"start_line":2,"end_line":4},` +
		`"content_around_reference":"...   3:func twice() int {\n  >   4:\treturn Area() * 2\n"}]}}`
	stub, settingsPath := symbolInstall(t, map[string]string{
		"find_symbol":              stubSymbol,
		"find_referencing_symbols": references,
	})

	out, err := exec(t, "--config", settingsPath, "ask", "symbol.references",
		"--repo", "current", "--set", "file=pkg/shapes.go", "--set", "line=5", "--set", "column=8")
	if err != nil {
		t.Fatalf("ask: %v\n%s", err, out)
	}
	for _, want := range []string{"verdict   ok", "locations (1)", "line     5", "path     pkg/use.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	if !containsTool(stub.tools, "find_referencing_symbols") {
		t.Errorf("tools = %v, want the reference lookup", stub.tools)
	}
}

// The payload is typed by the capability's own declaration, so a line number
// that is not a number is refused at the door rather than sent as a string for
// the far side to trip over.
func TestTheAskPayloadIsTypedByTheCapability(t *testing.T) {
	_, settingsPath := symbolInstall(t, nil)

	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"not a number", []string{"--set", "file=a.go", "--set", "line=soon", "--set", "column=1"}, 2},
		{"unknown field", []string{"--set", "file=a.go", "--set", "color=blue"}, 2},
		{"not name=value", []string{"--set", "file"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--config", settingsPath, "ask", "symbol.definition", "--repo", "current"}, tc.args...)
			out, err := exec(t, args...)
			if err == nil {
				t.Fatalf("accepted a bad payload:\n%s", out)
			}
			if got := exitCode(err); got != tc.want {
				t.Errorf("exit code = %d, want %d (err %v)", got, tc.want, err)
			}
		})
	}
}

// A capability nobody declared is a not_found, not a crash and not a silent
// empty answer.
func TestAskingForACapabilityNobodyDeclaredIsNotFound(t *testing.T) {
	_, settingsPath := symbolInstall(t, nil)

	_, err := exec(t, "--config", settingsPath, "ask", "symbol.invented", "--repo", "current",
		"--set", "file=a.go")
	if err == nil {
		t.Fatal("an unknown capability was accepted")
	}
	if got := exitCode(err); got != 3 {
		t.Errorf("exit code = %d, want 3 (err %v)", got, err)
	}
}

func containsTool(tools []string, want string) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

// A tally is a property of a split-up commission. An ask has an answer, and a
// zero nobody counted reads as "found nothing" rather than "did not count".
func TestAnAskReportsAnAnswerNotATally(t *testing.T) {
	_, settingsPath := symbolInstall(t, map[string]string{"find_symbol": stubSymbol})

	out, err := exec(t, "--config", settingsPath, "ask", "symbol.definition",
		"--repo", "current", "--set", "file=pkg/shapes.go", "--set", "line=5", "--set", "column=8")
	if err != nil {
		t.Fatalf("ask: %v\n%s", err, out)
	}
	if strings.Contains(out, "matches") {
		t.Errorf("the report claims a tally it never counted:\n%s", out)
	}
	if !strings.Contains(out, "answer") {
		t.Errorf("the answer is missing:\n%s", out)
	}
}

// Serena being down is a provider that cannot work here, and with nobody else
// serving the capability the step has to fail out loud. The bin travels to the
// shell as an exit code so a script can tell it from a bad invocation.
func TestASerenaThatIsDownFailsTheAsk(t *testing.T) {
	_, settingsPath := symbolInstall(t, nil)
	// Point the adapter at a port nothing is listening on. Everything else is
	// the shipped catalog, so this is the real unreachable-provider path.
	body, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	broken := regexp.MustCompile(`endpoint = "[^"]+"`).
		ReplaceAllString(string(body), `endpoint = "http://127.0.0.1:1/mcp"`)
	if err := os.WriteFile(settingsPath, []byte(broken), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := exec(t, "--config", settingsPath, "ask", "symbol.definition",
		"--repo", "current", "--set", "file=pkg/shapes.go", "--set", "line=5", "--set", "column=8",
		"--trace")
	if err == nil {
		t.Fatalf("a dead provider answered:\n%s", out)
	}
	if got := exitCode(err); got != 6 {
		t.Errorf("exit code = %d, want 6 (err %v)", got, err)
	}
	if !strings.Contains(out, "verdict   failed") {
		t.Errorf("the report was swallowed:\n%s", out)
	}
	// The reason has to be readable, and it has to be the right bin: a
	// provider that is not reachable is unavailable, not a bad request.
	if !strings.Contains(out, "unavailable") {
		t.Errorf("the failure bin is not on the screen:\n%s", out)
	}
}
