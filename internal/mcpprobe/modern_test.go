package mcpprobe_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpcompat"
	"github.com/Tutitoos/atenea/internal/mcpprobe"
)

const modernProbeResult = `{"resultType":"complete","supportedVersions":["2026-07-28","2025-06-18"],"capabilities":{"tools":{}},"ttlMs":0,"cacheScope":"private","_meta":{"io.modelcontextprotocol/serverInfo":{"name":"modern","version":"2"}}}`

func jsonRPCResult(id any, result string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":%s}`, id, result)
}

func TestModernHTTPProbeUsesDiscoverAndRecordsBothVersions(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		method, _ := body["method"].(string)
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		if method != "server/discover" {
			t.Errorf("method = %q, want server/discover", method)
		}
		if got := r.Header.Get("MCP-Protocol-Version"); got != mcpcompat.Modern.String() {
			t.Errorf("protocol header = %q", got)
		}
		params, _ := body["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		if meta[mcpcompat.ProtocolVersionKey] != mcpcompat.Modern.String() || meta[mcpcompat.ClientInfoKey] == nil || meta[mcpcompat.ClientCapabilitiesKey] == nil {
			t.Errorf("reserved metadata = %#v", meta)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, jsonRPCResult(body["id"], modernProbeResult))
	}))
	defer server.Close()

	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "modern", URL: server.URL, ProtocolMode: mcpprobe.ProtocolModernPin})
	if !got.OK {
		t.Fatalf("modern probe failed: %v", got.Err)
	}
	if got.Name != "modern" || got.Version != "2" {
		t.Fatalf("name/version = %q/%q", got.Name, got.Version)
	}
	if got.RequestedProtocolVersion != mcpcompat.Modern.String() || got.ObservedProtocolVersion != mcpcompat.Modern.String() {
		t.Fatalf("protocols = %q/%q", got.RequestedProtocolVersion, got.ObservedProtocolVersion)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(methods) != "[server/discover]" {
		t.Fatalf("methods = %v", methods)
	}
}

func TestModernStdioProbeRequiresDiscoveryEnvelope(t *testing.T) {
	result := `{"resultType":"complete","capabilities":{},"ttlMs":0,"cacheScope":"private"}`
	command := []string{"sh", "-c", fmt.Sprintf(`read -r line; printf '%%s\n' '%s'`, jsonRPCResult(1, result))}
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "incomplete", Command: command, ProtocolMode: mcpprobe.ProtocolModernPin})
	if got.OK {
		t.Fatal("accepted a resultType-only discovery response")
	}
}

func TestAutoHTTPFallsBackOnlyOnExplicitCompatibility(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		method, _ := body["method"].(string)
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if method == "server/discover" {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"error":{"code":-32601,"message":"legacy only"}}`, body["id"])
			return
		}
		fmt.Fprint(w, jsonRPCResult(body["id"], `{"protocolVersion":"2025-06-18","serverInfo":{"name":"legacy","version":"1"}}`))
	}))
	defer server.Close()

	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "auto", URL: server.URL, ProtocolMode: mcpprobe.ProtocolAuto})
	if !got.OK || got.Name != "legacy" {
		t.Fatalf("fallback result = %#v", got)
	}
	if got.RequestedProtocolVersion != mcpcompat.Modern.String() || got.ObservedProtocolVersion != mcpcompat.Legacy.String() {
		t.Fatalf("protocols = %q/%q", got.RequestedProtocolVersion, got.ObservedProtocolVersion)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(methods) != "[server/discover initialize]" {
		t.Fatalf("methods = %v", methods)
	}
}

func TestAutoHTTPDoesNotFallbackAuthenticationOrTimeout(t *testing.T) {
	t.Run("authentication", func(t *testing.T) {
		var methods atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			methods.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()
		got := mcpprobe.Probe(t.Context(), mcpprobe.Server{URL: server.URL, ProtocolMode: mcpprobe.ProtocolAuto})
		if got.OK || methods.Load() != 1 {
			t.Fatalf("auth result ok=%v methods=%d err=%v", got.OK, methods.Load(), got.Err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		t.Cleanup(func() {
			close(release)
			server.Close()
		})
		got := mcpprobe.Probe(t.Context(), mcpprobe.Server{URL: server.URL, Timeout: 40 * time.Millisecond, ProtocolMode: mcpprobe.ProtocolAuto})
		if got.OK {
			t.Fatal("timeout was accepted")
		}
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("modern discovery was not sent")
		}
	})
}

func TestAutoStdioRetriesWithFreshChildAfterExplicitModernRejection(t *testing.T) {
	command := []string{"sh", "-c", `read -r line; case "$line" in *server/discover*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"legacy"}}' ;; *initialize*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"legacy","version":"1"}}}' ;; esac`}
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "stdio", Command: command, ProtocolMode: mcpprobe.ProtocolAuto})
	if !got.OK || got.Name != "legacy" || got.ObservedProtocolVersion != mcpcompat.Legacy.String() {
		t.Fatalf("stdio fallback = %#v", got)
	}
}

func TestAutoStdioDoesNotDowngradeAfterProbeTimeoutOrCallerCancellation(t *testing.T) {
	t.Run("probe-timeout", func(t *testing.T) {
		command := []string{"sh", "-c", `read -r line; case "$line" in *server/discover*) sleep 1 ;; *initialize*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"legacy","version":"1"}}}' ;; esac`}
		got := mcpprobe.Probe(t.Context(), mcpprobe.Server{Command: command, Timeout: 40 * time.Millisecond, ProtocolMode: mcpprobe.ProtocolAuto})
		if got.OK || got.ObservedProtocolVersion != mcpcompat.Unknown.String() {
			t.Fatalf("timeout downgrade = %#v", got)
		}
	})
	t.Run("caller-cancel", func(t *testing.T) {
		count := filepath.Join(t.TempDir(), "starts")
		command := []string{"sh", "-c", `echo start >> "$COUNT"; read -r line; sleep 1`}
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			time.Sleep(35 * time.Millisecond)
			cancel()
		}()
		got := mcpprobe.Probe(ctx, mcpprobe.Server{Command: command, Env: map[string]string{"COUNT": count}, Timeout: time.Second, ProtocolMode: mcpprobe.ProtocolAuto})
		if got.OK {
			t.Fatal("caller cancellation was accepted")
		}
		raw, err := os.ReadFile(count)
		if err != nil {
			t.Fatalf("start marker: %v", err)
		}
		if lines := strings.Count(strings.TrimSpace(string(raw)), "start"); lines != 1 {
			t.Fatalf("caller cancellation started %d children, want one", lines)
		}
	})
}
