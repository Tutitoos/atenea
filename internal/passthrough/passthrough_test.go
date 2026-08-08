package passthrough_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	// forget makes the server behave like one that restarted: the next call
	// carrying a session id is answered 404 once, which is the shape a real
	// expiry takes.
	forget bool
	// sse answers with an event stream instead of plain JSON.
	sse bool
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
		result = `{"tools":[{"name":"semgrep_scan","description":"scan","inputSchema":{"type":"object"}},{"name":"","description":"nameless"}]}`
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

func dial(t *testing.T, b *backend) *passthrough.Backend {
	t.Helper()
	server := httptest.NewServer(b)
	t.Cleanup(server.Close)
	return passthrough.New("semgrep", server.URL, 5*time.Second)
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

	if _, err := b.Tools(t.Context()); err != nil {
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
	dead := passthrough.New("semgrep", "http://127.0.0.1:1/mcp", 2*time.Second)
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
	_, err = passthrough.New("semgrep", refusing.URL, 2*time.Second).Call(t.Context(), "nope", nil)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Errorf("refused call kind = %v, want invalid_input", got)
	}
}
