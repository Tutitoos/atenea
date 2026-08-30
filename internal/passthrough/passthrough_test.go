package passthrough_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/passthrough"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// backend is a server that behaves the way the specification allows a real
// one to: it issues a session id at the handshake and refuses any later call
// that does not carry it.
type backend struct {
	mu       sync.Mutex
	session  string
	calls    []string
	sessions int
	listings int
	// forget makes the server behave like one that restarted: the next call
	// carrying a session id is answered 404 once, which is the shape a real
	// expiry takes.
	forget bool
	// sse answers with an event stream instead of plain JSON.
	sse          bool
	outputSchema bool
	extraTool    bool
}

func (b *backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var msg struct {
		ID     *int   `json:"id"`
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&msg)

	b.mu.Lock()
	b.calls = append(b.calls, msg.Method)
	carried := r.Header.Get("Mcp-Session-Id")
	switch {
	case msg.Method == "initialize":
		b.sessions++
		b.session = fmt.Sprintf("s%d", b.sessions)
		w.Header().Set("Mcp-Session-Id", b.session)
	case b.forget && carried != "":
		b.forget = false
		b.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		return
	case carried != b.session:
		b.mu.Unlock()
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	sse := b.sse
	b.mu.Unlock()

	if msg.ID == nil { // a notification is owed no body
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result string
	switch msg.Method {
	case "tools/list":
		b.mu.Lock()
		b.listings++
		b.mu.Unlock()
		if b.outputSchema {
			result = `{"tools":[{"name":"semgrep_scan","description":"scan","inputSchema":{"type":"object"},"outputSchema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}]}`
		} else {
			result = `{"tools":[{"name":"semgrep_scan","description":"scan","inputSchema":{"type":"object"}},{"name":"","description":"nameless"}]}`
		}
		if b.extraTool {
			result = strings.Replace(result, "]}", ",{\"name\":\"new_tool\",\"description\":\"new\",\"inputSchema\":{\"type\":\"object\"}}]}", 1)
		}
	case "tools/call":
		result = fmt.Sprintf(`{"content":[{"type":"text","text":%q}],"isError":false}`, msg.Params.Name)
	default:
		result = `{}`
	}
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, *msg.ID, result)
	if sse {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestOutputSchemaIsPreserved(t *testing.T) {
	tools, err := dial(t, &backend{outputSchema: true}).Tools(t.Context())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if got := string(tools[0].OutputSchema); got != `{"type":"object","properties":{"ok":{"type":"boolean"}}}` {
		t.Fatalf("output schema = %s", got)
	}
}

func TestCatalogDriftReportsAddedAndMissingNames(t *testing.T) {
	b := dial(t, &backend{extraTool: true})
	if _, err := b.Tools(t.Context()); err != nil {
		t.Fatal(err)
	}
	reporter, ok := b.(interface {
		CatalogDrift() passthrough.CatalogDrift
	})
	if !ok {
		t.Fatal("backend does not expose catalog drift")
	}
	drift := reporter.CatalogDrift()
	if len(drift.Added) != 1 || drift.Added[0] != "new_tool" {
		t.Fatalf("added drift = %#v", drift.Added)
	}
	if len(drift.Missing) != 0 {
		t.Fatalf("missing drift = %#v", drift.Missing)
	}
}

func dial(t *testing.T, b *backend) passthrough.Backend {
	t.Helper()
	server := httptest.NewServer(b)
	t.Cleanup(server.Close)
	return passthrough.New(passthrough.Spec{
		ID: "semgrep", URL: server.URL, Timeout: 5 * time.Second,
		Allowed: []string{"semgrep_scan"},
	})
}

// The name is the seam between the two namespaces, so it is worth pinning
// from both directions rather than trusting one spelling of the prefix.
func TestNameAndSplitAreInverses(t *testing.T) {
	name := passthrough.Name("semgrep", "semgrep_scan")
	if want := "raw.semgrep.semgrep_scan"; name != want {
		t.Fatalf("Name = %q, want %q", name, want)
	}
	server, tool, ok := passthrough.Split(name)
	if !ok || server != "semgrep" || tool != "semgrep_scan" {
		t.Fatalf("Split(%q) = %q, %q, %v", name, server, tool, ok)
	}
	// A tool whose own name carries dots still belongs entirely to the tool:
	// only the server id is forbidden a dot, and the split has to honor that
	// asymmetry or a backend with dotted tool names becomes unreachable.
	if _, tool, ok := passthrough.Split("raw.semgrep.a.b.c"); !ok || tool != "a.b.c" {
		t.Errorf("dotted tool name = %q, %v", tool, ok)
	}
	for _, name := range []string{"code.search", "raw", "raw.", "raw.semgrep", "raw..scan", "rawsemgrep.x"} {
		if _, _, ok := passthrough.Split(name); ok {
			t.Errorf("Split(%q) claimed a capability name", name)
		}
	}
}

func TestToolsAreListedAsTheBackendGaveThem(t *testing.T) {
	tools, err := dial(t, &backend{}).Tools(t.Context())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	// The nameless entry is dropped rather than offered: a tool with no name
	// cannot be called, and offering it would put an uncallable name in a
	// client's list.
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want the one that has a name", len(tools))
	}
	if tools[0].Name != "semgrep_scan" || tools[0].Description != "scan" {
		t.Errorf("tool = %+v", tools[0])
	}
	if got := strings.TrimSpace(string(tools[0].InputSchema)); got != `{"type":"object"}` {
		t.Errorf("schema = %s, want the backend's own, unedited", got)
	}
}

// One session, shared: the whole reason this exists is that six clients each
// spawning a private copy is waste, so a second caller must not open a second
// one.
func TestTheSessionIsOpenedOnceAndShared(t *testing.T) {
	server := &backend{}
	b := dial(t, server)
	for range 3 {
		if _, err := b.Tools(t.Context()); err != nil {
			t.Fatalf("Tools: %v", err)
		}
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.sessions != 1 {
		t.Errorf("handshakes = %d, want 1", server.sessions)
	}
	if server.listings != 1 {
		t.Errorf("tools/list calls = %d, want one cached catalog", server.listings)
	}
}

// Two chats hitting a cold backend at the same moment is the case the lock
// exists for: without it each would handshake, and the fix for private copies
// would be making private copies.
func TestConcurrentCallersOpenOneSession(t *testing.T) {
	server := &backend{}
	b := dial(t, server)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = b.Tools(context.Background())
		}()
	}
	wg.Wait()
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.sessions != 1 {
		t.Errorf("handshakes = %d, want 1 for eight concurrent callers", server.sessions)
	}
	if server.listings != 1 {
		t.Errorf("tools/list calls = %d, want one singleflight request", server.listings)
	}
}

// A backend that restarted answers 404 to the id we are holding. That is the
// one failure a retry can fix, and it must be fixed silently: the chat that
// happened to call first did nothing wrong.
func TestAForgottenSessionIsReopenedOnce(t *testing.T) {
	server := &backend{}
	b := dial(t, server)
	if _, err := b.Tools(t.Context()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	server.mu.Lock()
	server.forget = true
	server.mu.Unlock()

	if _, err := b.Call(t.Context(), "semgrep_scan", nil); err != nil {
		t.Fatalf("after the backend forgot the session: %v", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.sessions != 2 {
		t.Errorf("handshakes = %d, want a second one after the refusal", server.sessions)
	}
}

func TestCallForwardsTheToolNameAndReturnsTheResultWhole(t *testing.T) {
	raw, err := dial(t, &backend{}).Call(t.Context(), "semgrep_scan", map[string]any{"code_files": []string{"x"}})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "semgrep_scan" {
		t.Errorf("result = %s, want the backend's own answer forwarded", raw)
	}
}

// Streamable HTTP servers pick their framing per response, so a backend that
// answers with an event stream is not a broken one.
func TestAnEventStreamAnswerIsRead(t *testing.T) {
	tools, err := dial(t, &backend{sse: true}).Tools(t.Context())
	if err != nil {
		t.Fatalf("Tools over sse: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d", len(tools))
	}
}

// A dead backend is unavailable, and a tool that refused is invalid input.
// The bins are the difference between "try again later" and "you asked
// wrongly", and a chat can only act on the second.
func TestFailuresLandInTheRightBins(t *testing.T) {
	dead := passthrough.New(passthrough.Spec{
		ID: "semgrep", URL: "http://127.0.0.1:1/mcp", Timeout: 2 * time.Second,
		Allowed: []string{"semgrep_scan"},
	})
	_, err := dead.Tools(t.Context())
	if err == nil {
		t.Fatal("a dead backend answered")
	}
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Errorf("dead backend kind = %v, want unavailable", got)
	}
	if !strings.Contains(err.Error(), "semgrep") {
		t.Errorf("err = %v, want the server named", err)
	}

	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID *int `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32602,"message":"no such tool"}}`, *msg.ID)
	}))
	t.Cleanup(refusing.Close)
	_, err = passthrough.New(passthrough.Spec{
		ID: "semgrep", URL: refusing.URL, Timeout: 2 * time.Second, Allowed: []string{"nope"},
	}).
		Call(t.Context(), "nope", nil)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Errorf("refused call kind = %v, want invalid_input", got)
	}
}

// The budget belongs to the backend rather than to whoever is listing: a
// filter one layer up is a filter the next caller can forget, and the next
// caller here would be holding a live connection to somebody else's shell.
func TestTheAllowListBoundsBothListingAndCalling(t *testing.T) {
	server := &backend{}
	http := httptest.NewServer(server)
	t.Cleanup(http.Close)
	// The server offers semgrep_scan; this declaration allows something else
	// entirely, so the tool it really has must not survive the filter.
	narrow := passthrough.New(passthrough.Spec{
		ID: "semgrep", URL: http.URL, Timeout: 5 * time.Second,
		Allowed: []string{"something_else"},
	})

	tools, err := narrow.Tools(t.Context())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("tools = %v, want none: the one it offers is not allowed", tools)
	}
	_, err = narrow.Call(t.Context(), "semgrep_scan", nil)
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Errorf("kind = %v, want permission_denied", got)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if slices.Contains(server.calls, "tools/call") {
		t.Errorf("a tool outside the budget reached the wire: %v", server.calls)
	}
	if !narrow.Allows("something_else") || narrow.Allows("semgrep_scan") {
		t.Errorf("Allows disagrees with the declaration: %v", narrow.Allowed())
	}
}

// lateSession is a server that starts sessionless and issues an id on the
// first call rather than at the handshake.
//
// Unusual, and allowed: the specification lets a server begin assigning
// session ids at any point, and the reason to model it here is that it is the
// only shape that puts two chats in send() at once with an empty session in
// hand. A server that issues the id at the handshake never does, because the
// handshake runs alone under the lock.
type lateSession struct {
	mu       sync.Mutex
	issued   int
	assigned string
}

func (l *lateSession) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var msg struct {
		ID     *int   `json:"id"`
		Method string `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&msg)
	if msg.Method != "initialize" && msg.Method != "notifications/initialized" {
		l.mu.Lock()
		if l.assigned == "" {
			l.assigned = "late-1"
		}
		l.issued++
		session := l.assigned
		l.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", session)
	}
	if msg.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"ok"}]}}`, *msg.ID)
}

// The session id has one owner, and every write to it is under the lock that
// every read of it takes.
//
// send runs on both sides of that lock -- ensure calls it holding mu, attempt
// calls it without -- and it used to write b.session itself, unguarded, on any
// answer that carried a header for a call that carried no session. Two chats
// arriving at a backend that assigns its id late are then writing that string
// while a third is reading it under the lock, which is a data race on a field
// whose whole job is to be the same for everybody. Run under -race this fails
// on the write; run without it, it is the concurrency that has to be here for
// the detector to have anything to see.
func TestTheSessionIDIsOnlyWrittenUnderTheLock(t *testing.T) {
	server := httptest.NewServer(&lateSession{})
	t.Cleanup(server.Close)
	b := passthrough.New(passthrough.Spec{
		ID: "late", URL: server.URL, Timeout: 5 * time.Second,
		Allowed: []string{"semgrep_scan"},
	})
	t.Cleanup(b.Close)

	const chats = 12
	var wg sync.WaitGroup
	for i := range chats {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.Call(t.Context(), "semgrep_scan", map[string]any{"n": i}); err != nil {
				t.Errorf("chat %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
}
