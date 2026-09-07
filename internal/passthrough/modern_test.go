package passthrough_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpcompat"
	"github.com/Tutitoos/atenea/internal/passthrough"
)

type modernPassthroughServer struct {
	mu       sync.Mutex
	methods  []string
	fail     bool
	fallback bool
	future   bool
}

func (s *modernPassthroughServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var message map[string]any
	_ = json.NewDecoder(r.Body).Decode(&message)
	method, _ := message["method"].(string)
	s.mu.Lock()
	s.methods = append(s.methods, method)
	fail, fallback, future := s.fail, s.fallback, s.future
	s.mu.Unlock()
	if fail {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if method == "server/discover" {
		params, _ := message["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		_ = meta[mcpcompat.ProtocolVersionKey]
		if fallback {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"error":{"code":-32601,"message":"legacy"}}`, message["id"])
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"resultType":"complete","supportedVersions":["2026-07-28","2025-06-18"],"capabilities":{"tools":{}},"ttlMs":0,"cacheScope":"private","_meta":{"%s":{"name":"modern","version":"2"}}}}`, message["id"], mcpcompat.ServerInfoKey)
		return
	}
	if method == "initialize" {
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2025-06-18","serverInfo":{"version":"1"}}}`, message["id"])
		return
	}
	var result string
	switch method {
	case "tools/list":
		result = `{"resultType":"complete","ttlMs":0,"cacheScope":"private","tools":[{"name":"allowed","description":"modern","inputSchema":{"type":"object"}}]}`
	case "tools/call":
		if future {
			result = `{"resultType":"future_result","requestState":"opaque-state","payload":{"keep":true}}`
		} else {
			result = `{"resultType":"complete","content":[{"type":"text","text":"called"}],"isError":false}`
		}
	default:
		result = `{"resultType":"complete"}`
	}
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":%s}`, message["id"], result)
}

func TestModernHTTPPassthroughListsCallsAndKeepsAllowList(t *testing.T) {
	server := &modernPassthroughServer{}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	b := passthrough.New(passthrough.Spec{
		ID: "modern", URL: httpServer.URL, Allowed: []string{"allowed"},
		ProtocolMode: passthrough.ProtocolModernPin,
	})
	defer b.Close()
	tools, err := b.Tools(t.Context())
	if err != nil || len(tools) != 1 || tools[0].Name != "allowed" {
		t.Fatalf("Tools = %#v, %v", tools, err)
	}
	if _, err := b.Call(t.Context(), "allowed", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if b.Allows("forbidden") {
		t.Fatal("modern protocol changed the allow-list")
	}
	if got := b.(interface{ ObservedProtocolVersion() string }).ObservedProtocolVersion(); got != mcpcompat.Modern.String() {
		t.Fatalf("observed protocol = %q", got)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if strings.Join(server.methods, ",") != "server/discover,tools/list,tools/call" {
		t.Fatalf("methods = %v", server.methods)
	}
}

func TestModernHTTPPassthroughPreservesFutureResultError(t *testing.T) {
	server := &modernPassthroughServer{future: true}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	b := passthrough.New(passthrough.Spec{ID: "future", URL: httpServer.URL, Allowed: []string{"allowed"}, ProtocolMode: passthrough.ProtocolModernPin})
	defer b.Close()
	_, err := b.Call(t.Context(), "allowed", nil)
	var future *mcpcompat.FutureResultError
	if !errors.As(err, &future) || !errors.Is(err, mcpcompat.ErrFutureResult) {
		t.Fatalf("future result error = %T %v", err, err)
	}
	if future.Response.Type != mcpcompat.ResultType("future_result") || string(future.Response.RequestState) != `"opaque-state"` {
		t.Fatalf("future response = %#v", future.Response)
	}
}

func TestAutoHTTPPassthroughFallsBackAndDoesNotDowngradeOnFailure(t *testing.T) {
	t.Run("explicit-fallback", func(t *testing.T) {
		server := &modernPassthroughServer{fallback: true}
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		b := passthrough.New(passthrough.Spec{ID: "auto", URL: httpServer.URL, Allowed: []string{"allowed"}, ProtocolMode: passthrough.ProtocolAuto})
		defer b.Close()
		if _, err := b.Tools(t.Context()); err != nil {
			t.Fatalf("fallback Tools: %v", err)
		}
		if got := b.(interface{ ObservedProtocolVersion() string }).ObservedProtocolVersion(); got != mcpcompat.Legacy.String() {
			t.Fatalf("observed fallback protocol = %q", got)
		}
		server.mu.Lock()
		defer server.mu.Unlock()
		if strings.Join(server.methods, ",") != "server/discover,initialize,notifications/initialized,tools/list" {
			t.Fatalf("fallback methods = %v", server.methods)
		}
	})
	t.Run("server-failure", func(t *testing.T) {
		server := &modernPassthroughServer{fail: true}
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		b := passthrough.New(passthrough.Spec{ID: "auto", URL: httpServer.URL, Allowed: []string{"allowed"}, ProtocolMode: passthrough.ProtocolAuto})
		_, err := b.Tools(context.Background())
		if err == nil {
			t.Fatal("server failure was accepted")
		}
		server.mu.Lock()
		defer server.mu.Unlock()
		if strings.Join(server.methods, ",") != "server/discover" {
			t.Fatalf("failure downgraded methods = %v", server.methods)
		}
	})
}

func TestModernHTTPReconnectReprobesAndInvalidatesCatalog(t *testing.T) {
	server := &modernPassthroughServer{}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	b := passthrough.New(passthrough.Spec{ID: "modern", URL: httpServer.URL, Allowed: []string{"allowed"}, ProtocolMode: passthrough.ProtocolModernPin})
	defer b.Close()
	if _, err := b.Tools(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.Close()
	if _, err := b.Tools(t.Context()); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if strings.Join(server.methods, ",") != "server/discover,tools/list,server/discover,tools/list" {
		t.Fatalf("reconnect methods = %v", server.methods)
	}
}

func TestModernHTTPCancellationClosesRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message map[string]any
		_ = json.NewDecoder(r.Body).Decode(&message)
		method, _ := message["method"].(string)
		if method == "server/discover" {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"resultType":"complete","supportedVersions":["2026-07-28","2025-06-18"],"capabilities":{"tools":{}},"ttlMs":0,"cacheScope":"private"}}`, message["id"])
			return
		}
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })
	b := passthrough.New(passthrough.Spec{ID: "modern", URL: server.URL, Allowed: []string{"allowed"}, ProtocolMode: passthrough.ProtocolModernPin, Timeout: time.Second})
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	callDone := make(chan error, 1)
	go func() { _, err := b.Call(ctx, "allowed", nil); callDone <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tools/call was not sent")
	}
	if err := <-callDone; err == nil {
		t.Fatal("canceled call succeeded")
	}
}
