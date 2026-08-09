package core_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// fakeBackend is a declared server: it answers a handshake, lists one tool
// with a schema of its own, and echoes the arguments it was called with.
type fakeBackend struct {
	mu    sync.Mutex
	calls []map[string]any
}

func (f *fakeBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var msg struct {
		ID     *int `json:"id"`
		Method string
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&msg)
	if msg.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Mcp-Session-Id", "session-1")
	var result string
	switch msg.Method {
	case "tools/list":
		// Three tools, on purpose: one the operator allows, one allowed but
		// costlier, and one that is exactly the reason an allow list exists.
		result = `{"tools":[` +
			`{"name":"semgrep_scan","description":"scan code",` +
			`"inputSchema":{"type":"object","properties":{"code_files":{"type":"array"}},"required":["code_files"]}},` +
			`{"name":"semgrep_fix","description":"rewrite findings","inputSchema":{"type":"object"}},` +
			`{"name":"execute_shell_command","description":"run anything","inputSchema":{"type":"object"}}` +
			`]}`
	case "tools/call":
		f.mu.Lock()
		f.calls = append(f.calls, msg.Params.Arguments)
		f.mu.Unlock()
		if msg.Params.Name == "execute_shell_command" {
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"owned"}]}}`, *msg.ID)
			return
		}
		result = `{"content":[{"type":"text","text":"0 findings"}],"isError":false}`
	default:
		result = `{}`
	}
	_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, *msg.ID, result)
}

// rawSettings is the mcp settings with one declared backend exposed raw.
func rawSettings(t *testing.T, endpoint string) string {
	t.Helper()
	repo := t.TempDir()
	body := "package main\n\n// TODO: the thing this test looks for\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	settings := strings.Replace(socketSettings, `path = "/tmp"`, fmt.Sprintf("path = %q", repo), 1)
	// A second candidate for the same capability, which no attached runner can
	// execute. It exists so the funnel has somebody to drop: a trace whose
	// stages never discard anyone proves the field is written, not that it
	// records what it claims to.
	settings += "\n[[implementation]]\nid = \"serena.search\"\nprovider = \"serena\"\ncapability = \"code.search\"\n"
	// The budget the backend is held to: two of the three tools it offers,
	// and one of those two costs more than reading.
	return settings + fmt.Sprintf("\n[[mcp_server]]\nid = \"semgrep\"\nurl = %q\nexpose = \"raw\"\n"+
		"tools = [\"semgrep_scan\", \"semgrep_fix\"]\neffects = [\"read\"]\n"+
		"\n  [[mcp_server.tool]]\n  name = \"semgrep_fix\"\n  effects = [\"read\", \"write\"]\n", endpoint)
}

// answerText is the one text block a tool result carries, which is where both
// an answer and a refusal are written.
func answerText(got map[string]any) string {
	content, _ := got["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

// onlyRun is the single raw receipt a test's one call produced.
func onlyRun(t *testing.T, atenea *core.Core) checkpoint.Run {
	t.Helper()
	store := atenea.Checkpoints()
	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found checkpoint.Run
	for _, id := range ids {
		run, err := store.Load(id)
		if err != nil {
			t.Fatalf("Load %s: %v", id, err)
		}
		if run.Kind == checkpoint.KindRaw {
			found = run
		}
	}
	if found.ID == "" {
		t.Fatalf("no raw receipt among %v", ids)
	}
	return found
}

// The two namespaces share one list and must stay legible in it: a capability
// keeps its dotted id, a forwarded tool wears the reserved prefix and the
// server it came from.
func TestABackendsToolsAreOfferedUnderTheReservedPrefix(t *testing.T) {
	backend := httptest.NewServer(&fakeBackend{})
	defer backend.Close()
	atenea := buildService(t, rawSettings(t, backend.URL))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/list", nil), "tools/list")

	tools, _ := got["tools"].([]any)
	var raw, capability map[string]any
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		switch tool["name"] {
		case "raw.semgrep.semgrep_scan":
			raw = tool
		case "code.search":
			capability = tool
		}
	}
	if capability == nil {
		t.Error("the catalog disappeared when a backend was declared")
	}
	if raw == nil {
		t.Fatalf("the backend's tool is not on the list: %v", tools)
	}
	if raw["description"] != "scan code" {
		t.Errorf("description = %v, want the backend's own", raw["description"])
	}
	// The schema is the backend's, unedited: no repository argument is added,
	// because a raw tool has no idea what a repository is.
	schema, _ := raw["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["code_files"]; !ok {
		t.Errorf("the backend's own schema was not forwarded: %v", schema)
	}
	if _, ok := props["repository"]; ok {
		t.Errorf("Atenea's repository argument was added to a raw tool: %v", schema)
	}
	if _, ok := raw["outputSchema"]; ok {
		t.Errorf("a raw tool was given an output schema it never declared: %v", raw)
	}
}

// The allow list is the budget, and a tool outside it must not appear on any
// list a client reads. A backend that advertises a shell is the whole reason
// the list exists: what it offers is its own business, what Atenea re-offers
// is the operator's.
func TestAToolOutsideTheAllowListIsNeverOffered(t *testing.T) {
	backend := httptest.NewServer(&fakeBackend{})
	defer backend.Close()
	atenea := buildService(t, rawSettings(t, backend.URL))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/list", nil), "tools/list")

	tools, _ := got["tools"].([]any)
	offered := make([]string, 0, len(tools))
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		if name, _ := tool["name"].(string); strings.HasPrefix(name, "raw.") {
			offered = append(offered, name)
		}
	}
	want := []string{"raw.semgrep.semgrep_fix", "raw.semgrep.semgrep_scan"}
	slices.Sort(offered)
	if !slices.Equal(offered, want) {
		t.Errorf("offered %v, want exactly the declared two %v", offered, want)
	}
}

// Filtering the list is not enforcement: a name that never appeared is still
// a name a client can send. The call has to be refused on its own, and it has
// to be refused before the backend hears about it.
func TestAToolOutsideTheAllowListIsRefusedWhenCalledAnyway(t *testing.T) {
	fake := &fakeBackend{}
	backend := httptest.NewServer(fake)
	defer backend.Close()
	atenea := buildService(t, rawSettings(t, backend.URL))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/call", map[string]any{
		"name": "raw.semgrep.execute_shell_command", "arguments": map[string]any{},
	}), "tools/call")

	if got["isError"] != true {
		t.Fatalf("an undeclared tool was not refused: %v", got)
	}
	if text := answerText(got); !strings.Contains(text, "not in this backend's tools") {
		t.Errorf("refusal = %q, want it to name the budget", text)
	}
	// The point of refusing here rather than only omitting it from a list.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 0 {
		t.Errorf("the backend was reached %d times by a tool nobody allowed", len(fake.calls))
	}
}

// An allowed tool still has to be paid for. `semgrep_fix` is declared to
// write, and a chat holding no grant beyond reading may not authorize that --
// the same rule, and the same refusal, a capability meets.
func TestARawToolCostingMoreThanTheChatHoldsIsRefused(t *testing.T) {
	fake := &fakeBackend{}
	backend := httptest.NewServer(fake)
	defer backend.Close()
	atenea := buildService(t, rawSettings(t, backend.URL))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/call", map[string]any{
		"name": "raw.semgrep.semgrep_fix", "arguments": map[string]any{},
	}), "tools/call")

	if got["isError"] != true {
		t.Fatalf("a write tool was run by a chat that may only read: %v", got)
	}
	if text := answerText(got); !strings.Contains(text, "may not authorize write") {
		t.Errorf("refusal = %q, want it to name the effect", text)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 0 {
		t.Errorf("the backend ran a tool the chat could not authorize")
	}
}

// The other side of the same rule, and the reason `client_effects` exists.
// An operator who grants clients write has said this machine's chats may run
// a write tool -- and until the chat could actually be handed that grant, the
// key was a ceiling with no way to reach it: every raw tool declaring more
// than `read` was refused on every machine, whatever the settings file said.
func TestARawToolTheOperatorGrantedClientsIsRun(t *testing.T) {
	fake := &fakeBackend{}
	backend := httptest.NewServer(fake)
	defer backend.Close()
	settings := strings.Replace(rawSettings(t, backend.URL), "[orchestrator]\n",
		"[orchestrator]\nclient_effects = [\"read\", \"write\"]\n", 1)
	atenea := buildService(t, settings)
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/call", map[string]any{
		"name": "raw.semgrep.semgrep_fix", "arguments": map[string]any{},
	}), "tools/call")

	if got["isError"] == true {
		t.Fatalf("the operator granted clients write and the chat was still refused: %v", answerText(got))
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 1 {
		t.Errorf("backend calls = %d, want the one the chat was entitled to make", len(fake.calls))
	}
}

// A refused call is the kind an operator most wants to find later, so it
// leaves the same receipt a successful one does -- carrying what it would
// have been authorized to cause, which is the only durable statement of that.
func TestARefusedRawCallIsStillOnTheRecord(t *testing.T) {
	backend := httptest.NewServer(&fakeBackend{})
	defer backend.Close()
	atenea := buildService(t, rawSettings(t, backend.URL))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	result(t, c.call("tools/call", map[string]any{
		"name": "raw.semgrep.semgrep_fix", "arguments": map[string]any{},
	}), "tools/call")

	run := onlyRun(t, atenea)
	if run.Verdict != contract.VerdictFailed.String() {
		t.Errorf("verdict = %q, want a failure on the record", run.Verdict)
	}
	if !slices.Contains(run.Effects, contract.EffectWrite) {
		t.Errorf("effects = %v, want the write it was refused for", run.Effects)
	}
	if len(run.Steps) != 1 || run.Steps[0].Funnel.State != checkpoint.FunnelNone {
		t.Errorf("a refused raw call did not read as a passthrough: %+v", run.Steps)
	}
}

// The call itself: arguments reach the backend untouched and its answer comes
// back whole rather than re-wrapped by a layer that knows nothing about it.
func TestARawCallReachesTheBackendAndReturnsItsAnswer(t *testing.T) {
	fake := &fakeBackend{}
	backend := httptest.NewServer(fake)
	defer backend.Close()
	atenea := buildService(t, rawSettings(t, backend.URL))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/call", map[string]any{
		"name":      "raw.semgrep.semgrep_scan",
		"arguments": map[string]any{"code_files": []any{"/tmp/x.py"}},
	}), "tools/call")

	if got["isError"] == true {
		t.Fatalf("the call failed: %v", got)
	}
	content, _ := got["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v, want the backend's own block", got)
	}
	first, _ := content[0].(map[string]any)
	if first["text"] != "0 findings" {
		t.Errorf("text = %v, want the backend's answer", first["text"])
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 1 {
		t.Fatalf("backend saw %d calls, want 1", len(fake.calls))
	}
	files, _ := fake.calls[0]["code_files"].([]any)
	if len(files) != 1 || files[0] != "/tmp/x.py" {
		t.Errorf("arguments arrived as %v, want them untouched", fake.calls[0])
	}
}

// The receipt is the half of this phase that outlives the call, and the field
// that matters is the one that says a funnel never happened -- as opposed to
// happening and going unrecorded.
func TestARawCallLeavesAReceiptSayingThereWasNoFunnel(t *testing.T) {
	backend := httptest.NewServer(&fakeBackend{})
	defer backend.Close()
	atenea := buildService(t, rawSettings(t, backend.URL))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	result(t, c.call("tools/call", map[string]any{
		"name":      "raw.semgrep.semgrep_scan",
		"arguments": map[string]any{"code_files": []any{"/tmp/x.py"}},
	}), "tools/call")

	store := atenea.Checkpoints()
	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found checkpoint.Run
	for _, id := range ids {
		run, err := store.Load(id)
		if err != nil {
			t.Fatalf("Load %s: %v", id, err)
		}
		if run.Kind == checkpoint.KindRaw {
			found = run
		}
	}
	if found.ID == "" {
		t.Fatalf("no raw receipt among %v", ids)
	}
	if found.Task != "raw.semgrep.semgrep_scan" {
		t.Errorf("task = %q, want the tool's public name", found.Task)
	}
	if !found.Closed {
		t.Error("a raw receipt is written closed: there is nothing to resume")
	}
	if len(found.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(found.Steps))
	}
	step := found.Steps[0]
	if step.Funnel.State != checkpoint.FunnelNone {
		t.Errorf("funnel state = %q, want %q", step.Funnel.State, checkpoint.FunnelNone)
	}
	if len(step.Funnel.Stages) != 0 {
		t.Errorf("a step with no funnel carries a trace: %v", step.Funnel.Stages)
	}
	if step.Capability != "" {
		t.Errorf("capability = %q, want empty: a raw tool answers none", step.Capability)
	}
}

// The pair the three states exist for: in one file, a capability step and a
// passthrough step must not read the same. Without this the reader cannot tell
// a missing record from a decision that never happened.
func TestAPassthroughStepIsDistinguishableFromAKeptFunnel(t *testing.T) {
	backend := httptest.NewServer(&fakeBackend{})
	defer backend.Close()
	atenea := buildService(t, rawSettings(t, backend.URL))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	result(t, c.call("tools/call", map[string]any{
		"name":      "raw.semgrep.semgrep_scan",
		"arguments": map[string]any{"code_files": []any{"/tmp/x.py"}},
	}), "tools/call")
	result(t, c.call("tools/call", map[string]any{
		"name":      "code.search",
		"arguments": map[string]any{"query": "TODO"},
	}), "tools/call")

	store := atenea.Checkpoints()
	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	states := map[string]bool{}
	var kept checkpoint.Funnel
	for _, id := range ids {
		run, err := store.Load(id)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		for _, step := range run.Steps {
			states[step.Funnel.State] = true
			if step.Funnel.State == checkpoint.FunnelKept {
				kept = step.Funnel
			}
		}
	}
	if !states[checkpoint.FunnelNone] {
		t.Errorf("no step says it had no funnel: %v", states)
	}
	if !states[checkpoint.FunnelKept] {
		t.Fatalf("no step kept its funnel: %v", states)
	}
	// Kept means the trace is actually there: the candidates dropped at each
	// stage, which is the thing a past decision could not be audited without.
	if len(kept.Stages) == 0 {
		t.Fatal("a kept funnel carries no stages")
	}
	named := false
	for _, stage := range kept.Stages {
		if stage.Name == "" {
			t.Errorf("a stage has no name: %+v", stage)
		}
		if stage.In < stage.Out {
			t.Errorf("stage %s let more out than came in: %+v", stage.Name, stage)
		}
		for _, drop := range stage.Dropped {
			if drop.Implementation == "" || drop.Reason == "" {
				t.Errorf("a drop names nobody: %+v", drop)
			}
			named = true
		}
	}
	if !named {
		t.Error("no candidate was named as dropped; the trace records nothing to audit")
	}
}

// A name in the reserved namespace whose backend is not declared is refused as
// a bad request, not answered with the catalog's "did you mean" -- the near
// miss would send a model looking for a capability that was never the point.
func TestARawNameWithNoBackendIsRefusedPlainly(t *testing.T) {
	backend := httptest.NewServer(&fakeBackend{})
	defer backend.Close()
	atenea := buildService(t, rawSettings(t, backend.URL))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	answer := c.call("tools/call", map[string]any{"name": "raw.nobody.scan", "arguments": map[string]any{}})

	failure, _ := answer["error"].(map[string]any)
	if failure == nil {
		t.Fatalf("an undeclared backend was accepted: %v", answer)
	}
	if msg, _ := failure["message"].(string); !strings.Contains(msg, "nobody") {
		t.Errorf("message = %q, want the undeclared server named", msg)
	}
}

// A backend that is down leaves the capabilities alone. Telling a model about
// a tool that cannot run is worse than not mentioning it.
func TestADeadBackendIsLeftOutRatherThanListedBroken(t *testing.T) {
	atenea := buildService(t, rawSettings(t, "http://127.0.0.1:1/mcp"))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("omp")
	got := result(t, c.call("tools/list", nil), "tools/list")

	tools, _ := got["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("a dead backend emptied the tool list")
	}
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		if name, _ := tool["name"].(string); strings.HasPrefix(name, "raw.") {
			t.Errorf("a dead backend's tool was offered: %v", name)
		}
	}
}
