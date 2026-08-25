package mcphttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// This package used to be part of the Serena adapter, written and tested
// against one far side. These tests are the wire behaviour that adapter
// measured, kept here because the wire format is what moved: framing, the
// handshake, session handling. What is not here any more is Serena's own
// meaning -- which tool means what, how its answer becomes a location --
// because none of that ever belonged to the wire in the first place.
//
// The stub is not a convenience. A unit test against a real MCP server would
// be testing that server's process rather than this client.

// stub is a fake streamable-HTTP MCP server. It records what it was asked
// and answers with canned text, which is the only thing a client is
// entitled to look at.
type stub struct {
	mu sync.Mutex
	// calls records every tools/call in order: tool name and arguments.
	calls []stubCall
	// answers maps a tool name to the text it returns. A tool with no entry
	// answers with an empty JSON array, which is a legitimate "nothing".
	answers map[string]string
	// noSession drops the session header, which is how a broken proxy looks.
	noSession bool
	// initializes counts the handshakes. One Client may only ever open one
	// session, and it takes concurrency to find out whether that holds.
	initializes int
	// slowHandshake delays the initialize reply, which is what gives a
	// fan-out of concurrent calls the chance to each start one of their own.
	slowHandshake time.Duration
	// extraEvent, when set, is written as its own SSE event before the reply
	// to the handshake -- a progress notification, which a streamable HTTP
	// server may legitimately interleave into the same response body.
	extraEvent string
	// failTools names tools whose call fails at the TRANSPORT, which is a
	// different thing from a tool that ran and refused: only this one takes
	// the session down with it.
	failTools map[string]bool
	// initializeHeaders and toolCallHeaders capture the request headers seen
	// on the first initialize and the first tools/call, so a test can prove
	// a header configured on the Client reached both -- not just whichever
	// request happened to be built first.
	initializeHeaders http.Header
	toolCallHeaders   http.Header
}

type stubCall struct {
	Tool string
	Args map[string]any
}

func newStub(t *testing.T) (*stub, string) {
	t.Helper()
	s := &stub{answers: map[string]string{}}
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
		s.mu.Lock()
		s.initializes++
		if s.initializeHeaders == nil {
			s.initializeHeaders = r.Header.Clone()
		}
		delay, extra := s.slowHandshake, s.extraEvent
		s.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		if !s.noSession {
			w.Header().Set("Mcp-Session-Id", "test-session")
		}
		// SSE framing, because that is what a real streamable-HTTP server
		// sends and the client has to survive it.
		w.Header().Set("Content-Type", "text/event-stream")
		if extra != "" {
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", extra)
		}
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
		if s.toolCallHeaders == nil {
			s.toolCallHeaders = r.Header.Clone()
		}
		if s.failTools[params.Name] {
			s.mu.Unlock()
			http.Error(w, "the transport broke", http.StatusInternalServerError)
			return
		}
		text := s.answers[params.Name]
		if text == "" {
			text = "[]"
		}
		s.mu.Unlock()
		result, _ := json.Marshal(map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": false,
		})
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(result)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	default:
		http.Error(w, "unexpected method "+req.Method, http.StatusBadRequest)
	}
}

// New rejects anything that is not an absolute http(s) URL: a relative path,
// a bare host with no scheme, or a scheme this client cannot dial all leave
// New unable to build a request against opts.Endpoint later, so the failure
// belongs at construction rather than surfacing as a confusing dial error on
// the first Call.
func TestNewRejectsAnEndpointThatIsNotAnAbsoluteHTTPURL(t *testing.T) {
	for _, endpoint := range []string{"", "not a url", "/mcp", "ftp://host/mcp", "host.example/mcp"} {
		if _, err := New(Options{Endpoint: endpoint}); err == nil {
			t.Errorf("New(%q) succeeded, want an error", endpoint)
		}
	}
	if _, err := New(Options{Endpoint: "http://127.0.0.1:7788/mcp"}); err != nil {
		t.Errorf("New with a valid endpoint: %v", err)
	}
}

// One connection opens one session, however many callers arrive at once.
//
// A caller may fan concurrent Calls out against one Client -- Atenea's own
// Serena adapter does exactly this for symbol.overview, up to sixteen
// find_symbol calls inside one held lock -- and every one of them begins by
// asking whether a session exists. Written as "read the field, release the
// lock, then initialize", that check decides nothing: several callers each
// conclude there is no session and each run their own initialize, every one
// but the last stamping a session the server then abandons.
func TestOneConnectionOpensExactlyOneSessionUnderConcurrentCalls(t *testing.T) {
	const concurrency = 16
	s, endpoint := newStub(t)
	// Slow enough that a second goroutine reaching the check while the first
	// is still in flight is the ordinary outcome rather than a lucky one.
	s.slowHandshake = 40 * time.Millisecond
	c, err := New(Options{Endpoint: endpoint, Client: "mcphttp-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.Call(t.Context(), "noop", nil)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Call %d: %v", i, err)
		}
	}

	s.mu.Lock()
	got := s.initializes
	s.mu.Unlock()
	if got != 1 {
		t.Fatalf("initialize ran %d time(s), want 1: the handshake is not serialized", got)
	}
}

// An SSE body is a stream of events, not one payload: a server may interleave
// a progress notification, or a reply to another request, into the same
// response. The handshake is the exchange the stub frames as SSE, so a body
// with a notification in front of the reply has to survive it or every
// Call on this Client fails from the first one on.
func TestAnExtraSSEEventDuringTheHandshakeDoesNotBreakTheSession(t *testing.T) {
	s, endpoint := newStub(t)
	s.extraEvent = `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`
	s.answers["find_declaration"] = `{"name_path":"Shape/area"}`
	c, err := New(Options{Endpoint: endpoint})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	text, err := c.Call(t.Context(), "find_declaration", map[string]any{"relative_path": "pkg/shapes.go"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(text, "Shape/area") {
		t.Fatalf("text = %q, want the answer the stub sent", text)
	}
}

// decode's own rules, stated once rather than inferred from a Call.
func TestDecodePicksTheEventAnsweringTheRequestItWasGiven(t *testing.T) {
	const wanted = `{"jsonrpc":"2.0","id":7,"result":{"answer":"mine"}}`
	body := "event: message\ndata: " + `{"jsonrpc":"2.0","method":"notifications/progress"}` + "\n\n" +
		"event: message\ndata: " + `{"jsonrpc":"2.0","id":6,"result":{"answer":"someone else's"}}` + "\n\n" +
		"event: message\ndata: " + wanted + "\n\n"

	result, err := decode(answer{contentType: "text/event-stream", body: body}, 7)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(string(result), "mine") {
		t.Fatalf("result = %s, want the reply to request 7", result)
	}

	// A stream that answers a different request is not this request's
	// answer, and reading it as one would hand the caller somebody else's
	// result.
	if _, err := decode(answer{contentType: "text/event-stream", body: body}, 9); err == nil {
		t.Fatal("decode accepted a stream carrying no reply to the request it was given")
	}
}

// The framing is decided by the header that exists to declare it. Sniffing
// the first bytes of the body missed every SSE stream that opens with
// anything but "event:" or "data:" -- an id: line and a comment are both
// ordinary -- and sent it into the JSON decoder, where it came back as
// "unreadable JSON".
func TestDecodeTrustsTheContentTypeOverTheShapeOfTheFirstLine(t *testing.T) {
	body := ": keep-alive\nid: 42\nevent: message\ndata: " +
		`{"jsonrpc":"2.0","id":3,"result":{"answer":"here"}}` + "\n\n"

	result, err := decode(answer{contentType: "text/event-stream; charset=utf-8", body: body}, 3)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(string(result), "here") {
		t.Fatalf("result = %s, want the reply the stream carried", result)
	}

	// And a plain JSON document is still a plain JSON document.
	plain, err := decode(answer{
		contentType: "application/json",
		body:        `{"jsonrpc":"2.0","id":3,"result":{"answer":"plain"}}`,
	}, 3)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(string(plain), "plain") {
		t.Fatalf("result = %s, want the JSON body read as JSON", plain)
	}
}

// A dead session must not be reused. rpc drops the session on any POST
// error, and the only way to observe that from outside the package -- session
// is unexported -- is that the next Call has to open a new one: the stub's
// initialize count goes from one to two.
func TestAFailedCallDropsTheSessionSoTheNextCallReestablishesIt(t *testing.T) {
	s, endpoint := newStub(t)
	s.failTools = map[string]bool{"find_symbol": true}
	c, err := New(Options{Endpoint: endpoint})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := c.Call(t.Context(), "find_symbol", map[string]any{"name_path_pattern": "a"}); err == nil {
		t.Fatal("Call succeeded against a transport that refused it")
	}
	if _, err := c.Call(t.Context(), "find_declaration", map[string]any{"relative_path": "x"}); err != nil {
		t.Fatalf("second Call: %v", err)
	}

	s.mu.Lock()
	got := s.initializes
	s.mu.Unlock()
	if got != 2 {
		t.Fatalf("initialize ran %d time(s), want 2: a dead session must not be reused", got)
	}
}

// Options.Headers is the whole point of this package existing apart from
// Serena's old private client: it is how a caller reaches a server that
// requires authentication, such as kivgraph's daemon answering 401 without
// "Authorization: Bearer <token>". A header set only on tools/call and
// missing from initialize would still get the handshake rejected before any
// tool ever ran, so both requests have to carry it.
func TestHeadersReachBothTheHandshakeAndASubsequentToolCall(t *testing.T) {
	s, endpoint := newStub(t)
	c, err := New(Options{
		Endpoint: endpoint,
		Headers:  map[string]string{"Authorization": "Bearer secret-token"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := c.Call(t.Context(), "find_symbol", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}

	s.mu.Lock()
	initHeader := s.initializeHeaders.Get("Authorization")
	callHeader := s.toolCallHeaders.Get("Authorization")
	s.mu.Unlock()
	if initHeader != "Bearer secret-token" {
		t.Errorf("initialize Authorization = %q, want the configured header", initHeader)
	}
	if callHeader != "Bearer secret-token" {
		t.Errorf("tools/call Authorization = %q, want the configured header", callHeader)
	}
}
