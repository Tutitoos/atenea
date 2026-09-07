package mcphttp

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
)

type modernHTTPFixture struct {
	mu       sync.Mutex
	methods  []string
	headers  []http.Header
	bodies   []map[string]any
	fallback bool
	invalid  bool
	unauth   bool
	future   bool
	blocked  chan struct{}
}

func (f *modernHTTPFixture) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	method, _ := body["method"].(string)
	f.mu.Lock()
	f.methods = append(f.methods, method)
	f.headers = append(f.headers, r.Header.Clone())
	f.bodies = append(f.bodies, body)
	f.mu.Unlock()
	if f.unauth {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if f.blocked != nil {
		close(f.blocked)
		<-r.Context().Done()
		return
	}
	if method == "server/discover" && f.fallback {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not supported"}}`)
		return
	}
	if method == "server/discover" && f.invalid {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid modern metadata"}}`)
		return
	}
	if method == "initialize" {
		w.Header().Set("Mcp-Session-Id", "legacy-session")
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": map[string]any{
			"protocolVersion": mcpcompat.Legacy.String(), "serverInfo": map[string]any{"name": "legacy", "version": "1"},
		}})
		return
	}
	if method == "notifications/initialized" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if method == "tools/call" && f.future {
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": map[string]any{
			"resultType": "future_result", "requestState": "opaque-state", "payload": map[string]any{"keep": true},
		}})
		return
	}
	result := modernResult(false)
	if method == "server/discover" {
		result = discoveryResult()
	}
	writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": result})
}

func discoveryResult() map[string]any {
	return map[string]any{
		"resultType": "complete", "supportedVersions": []string{mcpcompat.Modern.String(), mcpcompat.Legacy.String()},
		"capabilities": map[string]any{"tools": map[string]any{}}, "ttlMs": int64(0), "cacheScope": "private",
		"_meta": map[string]any{mcpcompat.ServerInfoKey: map[string]any{"name": "modern", "version": "1"}},
	}
}

func modernResult(inputRequired bool) map[string]any {
	resultType := "complete"
	if inputRequired {
		resultType = "input_required"
	}
	result := map[string]any{
		"resultType": resultType,
		"content":    []map[string]any{{"type": "text", "text": "ok"}},
		"isError":    false,
		"_meta": map[string]any{
			mcpcompat.ServerInfoKey: map[string]any{"name": "modern", "version": "1"},
		},
	}
	if inputRequired {
		result["inputRequests"] = map[string]any{"confirm": map[string]any{"method": "elicitation/create", "params": map[string]any{"mode": "form", "message": "continue?", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}}}}}
	}
	return result
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestModernPinUsesReservedMetadataAndStatelessHeaders(t *testing.T) {
	fixture := &modernHTTPFixture{}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL, ProtocolMode: ProtocolModernPin, Client: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"one", "two"} {
		if _, err := client.Call(t.Context(), tool, map[string]any{"x": true}); err != nil {
			t.Fatalf("Call(%s): %v", tool, err)
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.methods) != 3 || fixture.methods[0] != "server/discover" {
		t.Fatalf("methods = %v", fixture.methods)
	}
	for i, headers := range fixture.headers[1:] {
		if headers.Get("MCP-Protocol-Version") != mcpcompat.Modern.String() || headers.Get("Mcp-Method") != "tools/call" || headers.Get("Mcp-Name") != fixture.bodies[i+1]["params"].(map[string]any)["name"] {
			t.Fatalf("modern headers[%d] = %v", i, headers)
		}
		if headers.Get("Mcp-Session-Id") != "" {
			t.Fatalf("modern sent session header: %v", headers)
		}
		params := fixture.bodies[i+1]["params"].(map[string]any)
		meta := params["_meta"].(map[string]any)
		if meta[mcpcompat.ProtocolVersionKey] != mcpcompat.Modern.String() || meta[mcpcompat.ClientInfoKey] == nil || meta[mcpcompat.ClientCapabilitiesKey] == nil {
			t.Fatalf("reserved metadata = %v", meta)
		}
		if _, flat := meta["protocolVersion"]; flat {
			t.Fatal("flat protocol metadata was sent")
		}
	}
}

func TestAutoDoesNotDowngradeOnInvalidModernDiscovery(t *testing.T) {
	fixture := &modernHTTPFixture{invalid: true}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL, ProtocolMode: ProtocolAuto})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(t.Context(), "blocked", nil); err == nil {
		t.Fatal("auto accepted malformed modern discovery")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if strings.Join(fixture.methods, ",") != "server/discover" {
		t.Fatalf("invalid discovery caused an unauthorized downgrade: %v", fixture.methods)
	}
}

func TestAutoModernDiscoveryAndCompatibleFallback(t *testing.T) {
	t.Run("modern", func(t *testing.T) {
		fixture := &modernHTTPFixture{}
		server := httptest.NewServer(http.HandlerFunc(fixture.serve))
		defer server.Close()
		client, err := New(Options{Endpoint: server.URL, ProtocolMode: ProtocolAuto})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Call(t.Context(), "demo", nil); err != nil {
			t.Fatal(err)
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		if len(fixture.methods) != 2 || fixture.methods[0] != "server/discover" || fixture.methods[1] != "tools/call" {
			t.Fatalf("methods = %v", fixture.methods)
		}
		if fixture.headers[0].Get("MCP-Protocol-Version") != mcpcompat.Modern.String() || fixture.headers[0].Get("Mcp-Method") != "server/discover" || fixture.headers[0].Get("Mcp-Name") != "" {
			t.Fatalf("discover headers = %v", fixture.headers[0])
		}
	})
	t.Run("fallback", func(t *testing.T) {
		fixture := &modernHTTPFixture{fallback: true}
		server := httptest.NewServer(http.HandlerFunc(fixture.serve))
		defer server.Close()
		client, err := New(Options{Endpoint: server.URL, ProtocolMode: ProtocolAuto})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Call(t.Context(), "demo", nil); err != nil {
			t.Fatal(err)
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		want := []string{"server/discover", "initialize", "notifications/initialized", "tools/call"}
		if fmt.Sprint(fixture.methods) != fmt.Sprint(want) {
			t.Fatalf("methods = %v, want %v", fixture.methods, want)
		}
		if fixture.headers[1].Get("MCP-Protocol-Version") != "" || fixture.headers[3].Get("Mcp-Session-Id") != "legacy-session" {
			t.Fatalf("legacy fallback headers = %v", fixture.headers)
		}
	})
}

func TestModernAnonymousOrMalformedServerInfoStillCompletes(t *testing.T) {
	for name, result := range map[string]map[string]any{
		"anonymous":       {"resultType": "complete", "content": []map[string]any{{"type": "text", "text": "ok"}}, "isError": false},
		"malformed":       {"resultType": "complete", "content": []map[string]any{{"type": "text", "text": "ok"}}, "isError": false, "_meta": map[string]any{mcpcompat.ServerInfoKey: []any{"bad"}}},
		"flat-is-ignored": {"resultType": "complete", "content": []map[string]any{{"type": "text", "text": "ok"}}, "isError": false, "serverInfo": map[string]any{"name": "flat"}},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["method"] == "server/discover" {
					writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": discoveryResult()})
					return
				}
				writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": result})
			}))
			defer server.Close()
			client, err := New(Options{Endpoint: server.URL, ProtocolMode: ProtocolModernPin})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Call(t.Context(), "demo", nil); err != nil {
				t.Fatalf("anonymous modern response failed: %v", err)
			}
			if got := client.ObservedProtocolVersion(); got != mcpcompat.Modern.String() {
				t.Fatalf("observed protocol = %q", got)
			}
			if got := client.Version(); got != "" {
				t.Fatalf("malformed/absent server version = %q", got)
			}
		})
	}
}

func TestModernMessageNameOnlyUsesIdentifyingParameters(t *testing.T) {
	if got := modernMessageName(map[string]any{"name": "tool"}); got != "tool" {
		t.Fatalf("name = %q", got)
	}
	if got := modernMessageName(map[string]any{"uri": "file:///tmp/a"}); got != "file:///tmp/a" {
		t.Fatalf("uri = %q", got)
	}
	if got := modernMessageName(map[string]any{"taskId": "task-1"}); got != "task-1" {
		t.Fatalf("taskId = %q", got)
	}
	if got := modernMessageName(map[string]any{"_meta": map[string]any{}}); got != "" {
		t.Fatalf("unexpected name for list/discover params = %q", got)
	}
}

func TestAutoDoesNotFallbackAuthenticationFailure(t *testing.T) {
	fixture := &modernHTTPFixture{unauth: true}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL, ProtocolMode: ProtocolAuto})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(t.Context(), "demo", nil); err == nil {
		t.Fatal("authentication failure was hidden by legacy fallback")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.methods) != 1 || fixture.methods[0] != "server/discover" {
		t.Fatalf("methods after auth failure = %v", fixture.methods)
	}
}

func TestModernInputRequiredIsTypedAndCancellationSendsNoNotification(t *testing.T) {
	t.Run("input-required", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			result := modernResult(true)
			if body["method"] == "server/discover" {
				result = discoveryResult()
			}
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": result})
		}))
		defer server.Close()
		client, _ := New(Options{Endpoint: server.URL, ProtocolMode: ProtocolModernPin})
		_, err := client.Call(t.Context(), "demo", nil)
		var typed *InputRequiredError
		if !errors.As(err, &typed) || !errors.Is(err, ErrInputRequired) {
			t.Fatalf("input_required error = %T %v", err, err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		started := make(chan struct{})
		fixture := &modernHTTPFixture{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["method"] == "server/discover" {
				writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": discoveryResult()})
				return
			}
			fixture.mu.Lock()
			fixture.methods = append(fixture.methods, body["method"].(string))
			fixture.mu.Unlock()
			close(started)
			<-r.Context().Done()
		}))
		defer server.Close()
		client, _ := New(Options{Endpoint: server.URL, ProtocolMode: ProtocolModernPin})
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		_, err := client.Call(ctx, "demo", nil)
		if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Fatalf("cancel error = %v", err)
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		if len(fixture.methods) != 1 || fixture.methods[0] != "tools/call" {
			t.Fatalf("cancel emitted extra protocol messages: %v", fixture.methods)
		}
	})
}

func TestModernFutureResultIsTypedAndPreservesState(t *testing.T) {
	fixture := &modernHTTPFixture{future: true}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	defer server.Close()
	client, err := New(Options{Endpoint: server.URL, ProtocolMode: ProtocolModernPin})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Call(t.Context(), "demo", nil)
	var future *mcpcompat.FutureResultError
	if !errors.As(err, &future) || !errors.Is(err, mcpcompat.ErrFutureResult) {
		t.Fatalf("future result error = %T %v", err, err)
	}
	if future.Response.Type != mcpcompat.ResultType("future_result") || string(future.Response.RequestState) != `"opaque-state"` {
		t.Fatalf("future response = %#v", future.Response)
	}
}
