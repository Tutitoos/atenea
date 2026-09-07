package mcpstdio_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpcompat"
	"github.com/Tutitoos/atenea/internal/mcpstdio"
)

// stdioWire is a deterministic child process fixture. The handler runs in
// its own goroutine so a child can observe notifications/canceled while a
// request handler is intentionally waiting for the caller's cancellation.
type stdioWire struct {
	toChild   *io.PipeReader
	fromChild *io.PipeWriter
	session   *mcpstdio.Session

	writeMu sync.Mutex
}

func newStdioWire(t *testing.T, opts mcpstdio.Options) *stdioWire {
	t.Helper()
	toChild, toSession := io.Pipe()
	fromSession, fromChild := io.Pipe()
	w := &stdioWire{
		toChild:   toChild,
		fromChild: fromChild,
		session:   mcpstdio.New(toSession, fromSession, opts),
	}
	t.Cleanup(func() {
		_ = w.session.Close()
		_ = w.toChild.Close()
		_ = w.fromChild.Close()
	})
	return w
}

func (w *stdioWire) send(value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	_, err = w.fromChild.Write(append(body, '\n'))
	return err
}

func (w *stdioWire) run(handler func(map[string]any)) {
	scanner := bufio.NewScanner(w.toChild)
	for scanner.Scan() {
		var message map[string]any
		if json.Unmarshal(scanner.Bytes(), &message) == nil {
			go handler(message)
		}
	}
}

func messageMethod(message map[string]any) string {
	method, _ := message["method"].(string)
	return method
}

func messageID(message map[string]any) any { return message["id"] }

func completeResult(text string) map[string]any {
	return map[string]any{
		"resultType": "complete",
		"content":    []map[string]any{{"type": "text", "text": text}},
		"isError":    false,
	}
}

func discoveryResult() map[string]any {
	return map[string]any{
		"resultType": "complete", "supportedVersions": []string{mcpcompat.Modern.String(), mcpcompat.Legacy.String()},
		"capabilities": map[string]any{"tools": map[string]any{}}, "ttlMs": int64(0), "cacheScope": "private",
	}
}

func response(id any, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func TestModernPinIsStatelessAndSupportsConcurrentCalls(t *testing.T) {
	w := newStdioWire(t, mcpstdio.Options{
		ProtocolMode: mcpstdio.ProtocolModernPin,
		ClientName:   "test-client",
	})
	var mu sync.Mutex
	var methods []string
	var badMeta string
	go w.run(func(message map[string]any) {
		method := messageMethod(message)
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		if method == "server/discover" || method == "tools/call" {
			params, _ := message["params"].(map[string]any)
			meta, _ := params["_meta"].(map[string]any)
			if meta[mcpcompat.ProtocolVersionKey] != mcpcompat.Modern.String() || meta[mcpcompat.ClientInfoKey] == nil || meta[mcpcompat.ClientCapabilitiesKey] == nil {
				mu.Lock()
				badMeta = fmt.Sprintf("%s: %#v", method, meta)
				mu.Unlock()
			}
		}
		switch method {
		case "server/discover":
			_ = w.send(response(messageID(message), discoveryResult()))
		case "tools/call":
			params, _ := message["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			marker, _ := args["marker"].(string)
			_ = w.send(response(messageID(message), completeResult(marker)))
		}
	})

	const calls = 8
	results := make([]string, calls)
	errs := make([]error, calls)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = w.session.Call(t.Context(), "demo", map[string]any{"marker": fmt.Sprintf("m%d", i)})
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Errorf("call %d: %v", i, errs[i])
		}
		if results[i] != fmt.Sprintf("m%d", i) {
			t.Errorf("call %d result = %q", i, results[i])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if badMeta != "" {
		t.Fatal(badMeta)
	}
	if len(methods) != calls+1 || methods[0] != "server/discover" {
		t.Fatalf("methods = %v, want one discover and %d calls", methods, calls)
	}
	for _, method := range methods[1:] {
		if method != "tools/call" {
			t.Fatalf("modern session emitted %q after discover", method)
		}
	}
	if got := w.session.RequestedProtocolVersion(); got != mcpcompat.Modern.String() {
		t.Fatalf("requested protocol = %q", got)
	}
	if got := w.session.ObservedProtocolVersion(); got != mcpcompat.Modern.String() {
		t.Fatalf("observed protocol = %q", got)
	}
}

func TestModernFutureResultIsTypedAndPreservesState(t *testing.T) {
	w := newStdioWire(t, mcpstdio.Options{ProtocolMode: mcpstdio.ProtocolModernPin})
	go w.run(func(message map[string]any) {
		switch messageMethod(message) {
		case "server/discover":
			_ = w.send(response(messageID(message), discoveryResult()))
		case "tools/call":
			_ = w.send(response(messageID(message), map[string]any{
				"resultType": "future_result", "requestState": "opaque-state", "payload": map[string]any{"keep": true},
			}))
		}
	})
	_, err := w.session.Call(t.Context(), "demo", nil)
	var future *mcpcompat.FutureResultError
	if !errors.As(err, &future) || !errors.Is(err, mcpcompat.ErrFutureResult) {
		t.Fatalf("future result error = %T %v", err, err)
	}
	if future.Response.Type != mcpcompat.ResultType("future_result") || string(future.Response.RequestState) != `"opaque-state"` {
		t.Fatalf("future response = %#v", future.Response)
	}
}

func TestAutoFallsBackOnlyOnExplicitMethodNotFound(t *testing.T) {
	w := newStdioWire(t, mcpstdio.Options{ProtocolMode: mcpstdio.ProtocolAuto})
	var mu sync.Mutex
	var methods []string
	go w.run(func(message map[string]any) {
		method := messageMethod(message)
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		switch method {
		case "server/discover":
			_ = w.send(map[string]any{"jsonrpc": "2.0", "id": messageID(message), "error": map[string]any{"code": -32601, "message": "unsupported"}})
		case "initialize":
			_ = w.send(response(messageID(message), map[string]any{"protocolVersion": mcpcompat.Legacy.String(), "serverInfo": map[string]any{"version": "legacy-1"}}))
		case "tools/call":
			_ = w.send(response(messageID(message), map[string]any{"content": []map[string]any{{"type": "text", "text": "legacy"}}}))
		}
	})

	got, err := w.session.Call(t.Context(), "demo", nil)
	if err != nil || got != "legacy" {
		t.Fatalf("fallback call = %q, %v", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"server/discover", "initialize", "notifications/initialized", "tools/call"}
	if fmt.Sprint(methods) != fmt.Sprint(want) {
		t.Fatalf("methods = %v, want %v", methods, want)
	}
	if got := w.session.RequestedProtocolVersion(); got != mcpcompat.Modern.String() {
		t.Errorf("requested protocol = %q", got)
	}
	if got := w.session.ObservedProtocolVersion(); got != mcpcompat.Legacy.String() {
		t.Errorf("observed protocol = %q", got)
	}
}

func TestAutoDoesNotFallbackAfterDiscoveryTimeout(t *testing.T) {
	w := newStdioWire(t, mcpstdio.Options{ProtocolMode: mcpstdio.ProtocolAuto})
	discovered := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var methods []string
	go w.run(func(message map[string]any) {
		method := messageMethod(message)
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		if method == "server/discover" {
			once.Do(func() { close(discovered) })
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	callDone := make(chan error, 1)
	go func() {
		_, err := w.session.Call(ctx, "demo", nil)
		callDone <- err
	}()
	select {
	case <-discovered:
	case <-time.After(time.Second):
		t.Fatal("server/discover was not sent")
	}
	if err := <-callDone; err == nil {
		t.Fatal("timeout unexpectedly succeeded")
	}
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(methods) != "[server/discover]" {
		t.Fatalf("timeout triggered unsafe fallback: %v", methods)
	}
}

func TestCancellationSendsCancelledForBothProtocolEras(t *testing.T) {
	for _, mode := range []mcpstdio.ProtocolMode{mcpstdio.ProtocolLegacy, mcpstdio.ProtocolModernPin} {
		t.Run(string(mode), func(t *testing.T) {
			w := newStdioWire(t, mcpstdio.Options{ProtocolMode: mode})
			canceled := make(chan map[string]any, 1)
			started := make(chan struct{})
			var once sync.Once
			go w.run(func(message map[string]any) {
				switch messageMethod(message) {
				case "server/discover":
					_ = w.send(response(messageID(message), discoveryResult()))
				case "initialize":
					_ = w.send(response(messageID(message), map[string]any{"protocolVersion": mcpcompat.Legacy.String()}))
				case "tools/call":
					once.Do(func() { close(started) })
				case "notifications/canceled":
					canceled <- message
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			callDone := make(chan error, 1)
			go func() {
				_, err := w.session.Call(ctx, "demo", nil)
				callDone <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("tools/call was not sent")
			}
			if err := <-callDone; err == nil {
				t.Fatal("canceled call succeeded")
			}
			select {
			case message := <-canceled:
				params, _ := message["params"].(map[string]any)
				if _, ok := params["requestId"]; !ok {
					t.Fatalf("cancel params = %#v", params)
				}
				if mode == mcpstdio.ProtocolModernPin {
					meta, _ := params["_meta"].(map[string]any)
					if meta[mcpcompat.ProtocolVersionKey] != mcpcompat.Modern.String() {
						t.Fatalf("modern cancellation metadata = %#v", meta)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("notifications/canceled was not sent")
			}
		})
	}
}
