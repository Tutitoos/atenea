package mcphttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuditHandshakePublishedBeforeInitialized(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	tool := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Method string
			ID     int
		}
		_ = json.NewDecoder(r.Body).Decode(&v)
		switch v.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, v.ID)
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/call":
			tool <- struct{}{}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[]}}`, v.ID)
		}
	}))
	defer srv.Close()
	c, _ := New(Options{Endpoint: srv.URL})
	c.http.Transport = auditTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := r.GetBody()
		data, _ := io.ReadAll(b)
		_ = b.Close()
		if bytes.Contains(data, []byte("notifications/initialized")) {
			close(entered)
			<-release
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	done := make(chan error, 2)
	go func() { _, e := c.Call(context.Background(), "one", nil); done <- e }()
	<-entered
	go func() { _, e := c.Call(context.Background(), "two", nil); done <- e }()
	select {
	case <-tool:
		close(release)
		t.Fatal("tool ran before initialized")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-done
	<-done
}
func TestAuditHandshakeWaitIgnoresDeadline(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Method string
			ID     int
		}
		_ = json.NewDecoder(r.Body).Decode(&v)
		if v.Method == "initialize" {
			close(entered)
			<-release
			w.Header().Set("Mcp-Session-Id", "s")
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[]}}`, v.ID)
	}))
	defer srv.Close()
	c, _ := New(Options{Endpoint: srv.URL})
	one := make(chan struct{})
	go func() { _, _ = c.Call(context.Background(), "one", nil); close(one) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	two := make(chan error, 1)
	go func() { _, e := c.Call(ctx, "two", nil); two <- e }()
	<-ctx.Done()
	select {
	case <-two:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("deadline ignored")
	}
	close(release)
	<-one
}
func TestAuditSSEWaitsForEOF(t *testing.T) {
	sent := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Method string
			ID     int
		}
		_ = json.NewDecoder(r.Body).Decode(&v)
		if v.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "s")
		}
		if v.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		if v.Method == "tools/call" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}\n\n", v.ID)
			w.(http.Flusher).Flush()
			close(sent)
			<-release
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, v.ID)
	}))
	defer srv.Close()
	c, _ := New(Options{Endpoint: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := c.Call(ctx, "tool", nil); done <- e }()
	select {
	case <-sent:
	case e := <-done:
		close(release)
		t.Fatalf("call failed before SSE fixture: %v", e)
	case <-time.After(6 * time.Second):
		close(release)
		t.Fatal("fixture never reached")
	}
	e := <-done
	close(release)
	if e != nil {
		t.Fatal(e)
	}
}
func TestAuditStatelessServerRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct{ ID int }
		_ = json.NewDecoder(r.Body).Decode(&v)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"stateless","version":"1"}}}`, v.ID)
	}))
	defer srv.Close()
	c, _ := New(Options{Endpoint: srv.URL})
	_, e := c.Call(context.Background(), "tool", nil)
	if e != nil {
		t.Fatal(e)
	}
}
func TestAuditProtocolVersionHeaderMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Method string
			ID     int
		}
		_ = json.NewDecoder(r.Body).Decode(&v)
		if v.Method != "initialize" && r.Header.Get("MCP-Protocol-Version") == "" {
			http.Error(w, "missing MCP-Protocol-Version", 400)
			return
		}
		w.Header().Set("Mcp-Session-Id", "s")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, v.ID)
	}))
	defer srv.Close()
	c, _ := New(Options{Endpoint: srv.URL})
	_, e := c.Call(context.Background(), "tool", nil)
	if e != nil {
		t.Fatal(e)
	}
}

type auditTransport func(*http.Request) (*http.Response, error)

func (a auditTransport) RoundTrip(r *http.Request) (*http.Response, error) { return a(r) }

func TestOversizedMCPResponseIsRejected(t *testing.T) {
	for _, media := range []string{"application/json", "text/event-stream"} {
		t.Run(media, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", media)
				_, _ = io.WriteString(w, strings.Repeat("x", (8<<20)+1))
			}))
			defer srv.Close()
			c, err := New(Options{Endpoint: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if _, err := c.Call(ctx, "tool", nil); err == nil {
				t.Fatal("accepted oversized reply")
			}
		})
	}
}
