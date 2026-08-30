package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// decodeProbe reads the two shapes a streamable-HTTP MCP server may answer
// with, the same ones mcp.go's own decode handles. It has no process to
// spawn, so these are plain string-in, string-out cases.

func TestDecodeProbeAcceptsPlainJSON(t *testing.T) {
	_, err := decodeProbe(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	if err != nil {
		t.Fatalf("decodeProbe rejected a plain JSON result: %v", err)
	}
}

func TestDecodeProbeAcceptsSSEFraming(t *testing.T) {
	_, err := decodeProbe("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
	if err != nil {
		t.Fatalf("decodeProbe rejected an SSE-framed result: %v", err)
	}
}

func TestDecodeProbeRejoinsMultilineSSEData(t *testing.T) {
	framed := "data: {\"jsonrpc\":\"2.0\"\ndata: ,\"id\":1,\"result\":{}}\n\n"
	if _, err := decodeProbe(framed); err != nil {
		t.Fatalf("decodeProbe did not rejoin split data: lines: %v", err)
	}
}

func TestDecodeProbeSurfacesAJSONRPCError(t *testing.T) {
	_, err := decodeProbe(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`)
	if err == nil {
		t.Fatal("decodeProbe returned no error for a JSON-RPC error response")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Fatalf("error %q does not mention the server's own message", err)
	}
}

func TestDecodeProbeRejectsEmptyAnswer(t *testing.T) {
	if _, err := decodeProbe("   "); err == nil {
		t.Fatal("decodeProbe accepted a blank answer")
	}
}

func TestDecodeProbeRejectsUnreadableJSON(t *testing.T) {
	if _, err := decodeProbe("<html>not json</html>"); err == nil {
		t.Fatal("decodeProbe accepted a non-JSON answer")
	}
}

func TestDecodeProbeRejectsAnEventWithNoData(t *testing.T) {
	if _, err := decodeProbe("event: ping\n\n"); err == nil {
		t.Fatal("decodeProbe accepted an SSE event carrying no data: line")
	}
}

// probeReady itself needs only a real HTTP endpoint, not a real MCP server
// process: what it sends and how it reads the answer is the whole thing
// under test here, so httptest is the right weight -- the process-level
// tests in process_test.go are what need a real subprocess.

func TestProbeReadySendsAnInitializeRequest(t *testing.T) {
	var gotMethod, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req probeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotMethod = req.Method
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	if err := probeReady(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("probeReady failed against a well-behaved server: %v", err)
	}
	if gotMethod != "initialize" {
		t.Fatalf("probe method = %q, want %q", gotMethod, "initialize")
	}
	if !strings.Contains(gotAccept, "application/json") || !strings.Contains(gotAccept, "text/event-stream") {
		t.Fatalf("Accept header = %q, want both mcp response moods advertised", gotAccept)
	}
}

func TestProbeReadyReturnsFirstSSEFrameWithoutWaitingForStreamClose(t *testing.T) {
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(closed)
	}))

	started := time.Now()
	if err := probeReady(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("probeReady failed against an open SSE response: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("probeReady waited %s for the SSE stream to close", elapsed)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("probeReady did not close the SSE response")
	}
	srv.Close()
}

func TestProbeReadyFailsOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if err := probeReady(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("probeReady succeeded against a server answering 503")
	}
}

func TestProbeReadyFailsWhenNothingIsListening(t *testing.T) {
	// A closed listener's address: connection refused, the shape a server
	// that has not started yet answers with.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	if err := probeReady(context.Background(), http.DefaultClient, addr); err == nil {
		t.Fatal("probeReady succeeded against an address nothing is listening on")
	}
}

func TestProbeHTTPAcceptsAWebPageWithoutMCPHandshake(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>viewer</html>"))
	}))
	defer srv.Close()

	if err := probeHTTP(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("probeHTTP: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
}
