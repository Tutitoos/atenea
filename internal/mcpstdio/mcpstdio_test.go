package mcpstdio_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// inbound is one line a fake child received, decoded just enough for a test
// to react to it: which method it named, what a tools/call asked for, or --
// for a message with no method at all -- the answer to something the fake
// itself asked the session.
type inbound struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"params"`
}

// fake stands in for a child MCP server without spawning one: two io.Pipe
// pairs carry a Session's own stdin and stdout, and run drives the read
// side, handing every line that is not the generic handshake to a
// test-supplied handler. Every message is dispatched on its own goroutine,
// deliberately: a handler that blocks on its own reply -- or, as in the
// route() incident this package defends against, sends a request of its own
// first and waits for the answer -- must never stall run's own reading, or
// the two sides of the pipe pair could each end up waiting on a write the
// other is not there to read.
type fake struct {
	toR   *io.PipeReader
	fromW *io.PipeWriter
	wmu   sync.Mutex
	inits atomic.Int32
}

func newFake(t *testing.T) (*fake, *mcpstdio.Session) {
	t.Helper()
	toR, toW := io.Pipe()
	fromR, fromW := io.Pipe()
	sess := mcpstdio.New(toW, fromR, mcpstdio.Options{})
	t.Cleanup(func() {
		_ = sess.Close()
		_ = fromW.Close()
	})
	return &fake{toR: toR, fromW: fromW}, sess
}

func (f *fake) initCount() int32 { return f.inits.Load() }

func (f *fake) send(v any) {
	body, err := json.Marshal(v)
	if err != nil {
		return
	}
	f.wmu.Lock()
	defer f.wmu.Unlock()
	_, _ = f.fromW.Write(append(body, '\n'))
}

func (f *fake) reply(id json.RawMessage, result any) {
	f.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (f *fake) request(id, method string, params any) {
	f.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func okResult(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

// run reads lines until the session's stdin closes. It always answers
// initialize and always ignores notifications/initialized -- the two
// messages every scenario below needs regardless of what it is testing --
// and hands everything else to handle, each on its own goroutine.
func (f *fake) run(handle func(msg inbound)) {
	scanner := bufio.NewScanner(f.toR)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		var msg inbound
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			f.inits.Add(1)
			go f.reply(msg.ID, map[string]any{"protocolVersion": "2025-06-18"})
		case "notifications/initialized":
			// a notification: no id, no reply owed
		default:
			go handle(msg)
		}
	}
}

func TestSessionInitializeThenCallSucceeds(t *testing.T) {
	f, sess := newFake(t)
	go f.run(func(msg inbound) {
		if msg.Method != "tools/call" {
			return
		}
		f.reply(msg.ID, okResult("tool="+msg.Params.Name))
	})

	if err := sess.Initialize(t.Context()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	got, err := sess.Call(t.Context(), "get_symbol", map[string]any{"name": "Foo"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if want := "tool=get_symbol"; got != want {
		t.Errorf("Call result = %q, want %q", got, want)
	}
}

// Initialize is idempotent, and Call performs it defensively too: across two
// explicit calls plus one Call, the child must see the handshake exactly
// once.
func TestSessionInitializeIsIdempotent(t *testing.T) {
	f, sess := newFake(t)
	go f.run(func(msg inbound) {
		if msg.Method == "tools/call" {
			f.reply(msg.ID, okResult("ok"))
		}
	})

	if err := sess.Initialize(t.Context()); err != nil {
		t.Fatalf("first Initialize: %v", err)
	}
	if err := sess.Initialize(t.Context()); err != nil {
		t.Fatalf("second Initialize: %v", err)
	}
	if _, err := sess.Call(t.Context(), "get_symbol", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := f.initCount(); got != 1 {
		t.Errorf("the child saw %d initialize calls, want exactly 1 across two Initialize calls and one Call", got)
	}
}

func TestSessionCallSurfacesIsErrorText(t *testing.T) {
	f, sess := newFake(t)
	go f.run(func(msg inbound) {
		if msg.Method != "tools/call" {
			return
		}
		f.reply(msg.ID, map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": "unknown symbol: " + msg.Params.Name}},
		})
	})

	_, err := sess.Call(t.Context(), "get_symbol", nil)
	if err == nil {
		t.Fatal("Call succeeded against an isError:true result")
	}
	if !strings.Contains(err.Error(), "unknown symbol: get_symbol") {
		t.Errorf("error = %q, want it to carry the tool's own message", err)
	}
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want %v -- the tool refused a specific request, which the caller can act on", got, contract.FailureInvalidInput)
	}
}

// isError with no content at all is a server that flagged a failure and
// said nothing about it: there is no message to call the caller's fault, so
// this is unavailable rather than invalid input.
func TestSessionCallFailsUnavailableWhenIsErrorHasNoMessage(t *testing.T) {
	f, sess := newFake(t)
	go f.run(func(msg inbound) {
		if msg.Method != "tools/call" {
			return
		}
		f.reply(msg.ID, map[string]any{"isError": true, "content": []map[string]any{}})
	})

	_, err := sess.Call(t.Context(), "get_symbol", nil)
	if err == nil {
		t.Fatal("Call succeeded against an isError:true result with no message")
	}
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Errorf("kind = %v, want %v", got, contract.FailureUnavailable)
	}
}

func TestSessionCallFailsWhenTheChildAnswersWithAJSONRPCError(t *testing.T) {
	f, sess := newFake(t)
	go f.run(func(msg inbound) {
		if msg.Method != "tools/call" {
			return
		}
		f.send(map[string]any{
			"jsonrpc": "2.0", "id": msg.ID,
			"error": map[string]any{"code": -32602, "message": "unknown tool"},
		})
	})

	_, err := sess.Call(t.Context(), "not_a_real_tool", nil)
	if err == nil {
		t.Fatal("Call succeeded against a JSON-RPC error response")
	}
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want %v -- the child refused the request itself, not this session being unavailable", got, contract.FailureInvalidInput)
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error = %q, want it to carry the child's own message", err)
	}
}

// The protocol runs in both directions, and a child is allowed to ask its
// client something before it answers -- semgrep asks roots/list ahead of
// its first tools/call. Until 2026-08-09,
// internal/passthrough/stdio.go's route read that request as a response
// meant for whichever caller was waiting on id 0, which is nobody, and every
// call to that backend died on its own timeout with the child healthy and
// idle. This is that incident, reproduced against mcpstdio directly: a
// concurrent Call must survive it, and the request itself must get a
// refusal naming the method, not silence.
func TestSessionServerToClientRequestDoesNotHangAConcurrentCall(t *testing.T) {
	f, sess := newFake(t)
	refused := make(chan inbound, 1)
	go f.run(func(msg inbound) {
		switch {
		case msg.Method == "tools/call":
			f.request("srv-1", "roots/list", map[string]any{})
			f.reply(msg.ID, okResult("ok"))
		case msg.Method == "" && string(msg.ID) == `"srv-1"`:
			refused <- msg
		}
	})

	got, err := sess.Call(t.Context(), "get_symbol", nil)
	if err != nil {
		t.Fatalf("Call did not survive a server-to-client request: %v", err)
	}
	if got != "ok" {
		t.Errorf("Call result = %q, want %q", got, "ok")
	}

	select {
	case msg := <-refused:
		if msg.Error == nil || msg.Error.Code != -32601 {
			t.Fatalf("refusal = %+v, want a -32601 method-not-found error", msg.Error)
		}
		if !strings.Contains(msg.Error.Message, "roots/list") {
			t.Errorf("refusal message = %q, want it to name roots/list", msg.Error.Message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the session never answered the child's own roots/list request")
	}
}

func TestSessionCallFailsUnavailableWhenChildDies(t *testing.T) {
	f, sess := newFake(t)
	go f.run(func(msg inbound) {
		if msg.Method != "tools/call" {
			return
		}
		// The child dies instead of answering: its stdout just stops.
		_ = f.fromW.Close()
	})

	_, err := sess.Call(t.Context(), "get_symbol", nil)
	if err == nil {
		t.Fatal("Call succeeded against a child that died mid-call")
	}
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Errorf("kind = %v, want %v", got, contract.FailureUnavailable)
	}
	if sess.Err() == nil {
		t.Error("Err() is nil after the child died")
	}
}

// Every caller in flight together must get its own answer back, proven by
// staggering the delay so responses do not arrive in the order the requests
// were sent: a session that paired answers by arrival order rather than by
// JSON-RPC id would fail here and only here.
func TestSessionConcurrentCallsGetTheirOwnAnswers(t *testing.T) {
	f, sess := newFake(t)
	go f.run(func(msg inbound) {
		if msg.Method != "tools/call" {
			return
		}
		marker := fmt.Sprint(msg.Params.Arguments["marker"])
		delay, _ := time.ParseDuration(fmt.Sprint(msg.Params.Arguments["sleep"]))
		time.Sleep(delay)
		f.reply(msg.ID, okResult("marker="+marker))
	})

	const callers = 12
	var wg sync.WaitGroup
	errs := make([]error, callers)
	got := make([]string, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = sess.Call(t.Context(), "get_symbol", map[string]any{
				"marker": i, "sleep": fmt.Sprintf("%dms", (callers-i)*5),
			})
		}(i)
	}
	wg.Wait()

	for i := range callers {
		if errs[i] != nil {
			t.Errorf("caller %d: %v", i, errs[i])
			continue
		}
		if want := fmt.Sprintf("marker=%d", i); got[i] != want {
			t.Errorf("caller %d got %q, want %q", i, got[i], want)
		}
	}
}

func TestSessionCallFailsTimeoutWhenContextDeadlineExceeded(t *testing.T) {
	f, sess := newFake(t)
	// The child sees the call and simply never answers it -- what a
	// wedged tool looks like from this side.
	go f.run(func(inbound) {})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := sess.Call(ctx, "get_symbol", nil)
	if err == nil {
		t.Fatal("Call succeeded against a child that never answers")
	}
	if got := contract.KindOf(err); got != contract.FailureTimeout {
		t.Errorf("kind = %v, want %v", got, contract.FailureTimeout)
	}
	// The quoted ceiling has to come from ctx's own deadline, the same way
	// serena.go's failureFor quotes its Runner's r.timeout: a Session has
	// no fixed timeout of its own, so silence here would mean nothing ever
	// proved the deadline -- not some unrelated elapsed-time number -- is
	// what ends up in the message.
	if !strings.Contains(err.Error(), "took longer than") {
		t.Errorf("error = %q, want it to quote how long ctx allotted this call", err)
	}
}

func TestSessionCallFailsCanceledWhenContextIsCanceled(t *testing.T) {
	f, sess := newFake(t)
	go f.run(func(inbound) {})

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := sess.Call(ctx, "get_symbol", nil)
	if err == nil {
		t.Fatal("Call succeeded against a canceled context")
	}
	if got := contract.KindOf(err); got != contract.FailureCanceled {
		t.Errorf("kind = %v, want %v", got, contract.FailureCanceled)
	}
}

// A child that writes to stdout and never frames a line has to be cut off at
// a ceiling, and the session it was talking on has to say so.
//
// bufio.Reader.ReadString, which read() used, grows a slice until it finds
// the delimiter, so the reader's size only sets how much is read per syscall
// and nothing puts a roof on one message. The children behind this package
// are the ones internal/supervisor launches -- kivgraph and tokensave -- and
// the supervisor already caps their stderr with a ring for exactly this
// reason, which left the pipe carrying the protocol as the only unbounded
// one. Without the ceiling the flood is simply swallowed and the caller waits
// on its own deadline instead; with it, the session dies naming the fault.
func TestAChildThatNeverFramesALineKillsTheSession(t *testing.T) {
	f, sess := newFake(t)
	go f.run(func(msg inbound) {
		if msg.Method != "tools/call" {
			return
		}
		// Written straight onto the pipe rather than through send(), because
		// the whole point is that no newline ever follows it.
		f.wmu.Lock()
		defer f.wmu.Unlock()
		chunk := strings.Repeat("x", 64<<10)
		for written := 0; written < 9<<20; written += len(chunk) {
			if _, err := f.fromW.Write([]byte(chunk)); err != nil {
				return
			}
		}
	})
	if err := sess.Initialize(t.Context()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err := sess.Call(ctx, "get_symbol", nil)
	if err == nil {
		t.Fatal("a child that never framed a line answered a call")
	}
	if !strings.Contains(err.Error(), "not framing JSON-RPC") {
		t.Errorf("error = %v, want it to name the framing as the fault", err)
	}
	// And the session is dead rather than merely confused: every later caller
	// is told immediately instead of waiting out its own deadline.
	if sess.Err() == nil {
		t.Error("the session stayed live after its child stopped framing JSON-RPC")
	}
}
