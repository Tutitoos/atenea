// Package mcpstdio speaks JSON-RPC over a child process's own stdin and
// stdout -- the transport internal/passthrough/stdio.go already speaks for a
// server a chat borrows, factored out here so internal/supervisor can hand
// the same kind of session to an adapter instead of forwarding a backend's
// tools raw.
//
// A Session owns none of the process it talks to. New takes the pipes, not a
// command: the child's whole lifecycle -- spawn, restart budget, kill --
// belongs to whoever called New, because that is already
// internal/supervisor's job and a second process owner here would just be
// two things disagreeing about whether the child is still alive.
//
// Three things about this transport are easy to get wrong, and each one
// already cost somebody a day in internal/passthrough/stdio.go, which is why
// its shape is copied here rather than reinvented:
//
//   - One pipe, every caller. Call may be invoked from as many goroutines as
//     a caller likes; answers are routed back by JSON-RPC id, not by which
//     goroutine happens to be reading next.
//   - A response is not the only thing that arrives with an id. MCP runs in
//     both directions, and a server may ask something of its client --
//     `roots/list` before its first tools/call, observed in the wild -- before
//     it will go on. Mistaking that request for a response to whichever call
//     is waiting on the lowest id hangs that caller forever on an answer that
//     is never coming, and hangs the server on a reply this package never
//     sends. route classifies a line into one of three shapes, not two,
//     before it ever touches the routing table.
//   - The handshake is per-session, not per-call. initialize has to happen
//     once before anything else is asked, and Initialize is idempotent so
//     every caller -- Call included -- can invoke it defensively without a
//     second one ever reaching the wire.
package mcpstdio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// maxFrame caps one JSON-RPC message read off the child's stdout.
//
// The same number internal/passthrough's maxBody caps an HTTP answer at, and
// deliberately so: both are one MCP message arriving from a server, and two
// ceilings would mean the same oversized result was refused on one transport
// and accepted on the other. It is repeated rather than imported because that
// constant is unexported and this package sits below the one that owns it;
// the number is small enough to state twice, and the reason it exists is the
// same in both places.
const maxFrame = 8 << 20

// Options configures the handshake Initialize performs. Every field
// defaults when left zero, so Options{} is a legitimate call.
type Options struct {
	// ClientName and ClientVersion identify Atenea to the far side, the
	// way every MCP client introduces itself. Default "atenea" / "0", the
	// same identity internal/adapter/serena/mcp.go's own handshake uses.
	ClientName    string
	ClientVersion string
	// ProtocolVersion is the MCP revision this session declares. Default
	// matches internal/supervisor/probe.go's own constant: a server that
	// cannot answer this revision should say so at the handshake, the
	// earliest point that failure can be caught.
	ProtocolVersion string
}

func (o Options) withDefaults() Options {
	if o.ClientName == "" {
		o.ClientName = "atenea"
	}
	if o.ClientVersion == "" {
		o.ClientVersion = "0"
	}
	if o.ProtocolVersion == "" {
		o.ProtocolVersion = "2025-06-18"
	}
	return o
}

// Session is one JSON-RPC conversation with a child reachable over the
// pipes New was given. It is safe for concurrent use: Call may be invoked
// from as many goroutines as the caller likes, each getting its own answer
// back regardless of the order any of them arrive in.
type Session struct {
	stdin io.WriteCloser
	opts  Options

	writeMu sync.Mutex
	seq     atomic.Int64
	pending pending

	initMu      sync.Mutex
	initialized bool

	// closeOnce guards dead and why together: the reader goroutine reaching
	// EOF and a caller's own Close must not race to close dead twice, and
	// whichever of them gets there first is the one whose reason sticks --
	// a caller that closed on purpose does not want a read error racing in
	// afterward to overwrite that with something that looks like a crash.
	closeOnce sync.Once
	// dead closes once the session is no longer live: the child exited,
	// the pipe broke, or the caller closed it. Every caller waiting on an
	// answer selects on it, so a child that dies mid-call fails its
	// callers instead of leaving them to their own ctx deadlines.
	dead chan struct{}
	// why is the reason dead closed, set only inside closeOnce -- so every
	// read of why after dead is observed closed is reading a value that
	// will never change again.
	why error
}

// New starts routing answers from stdout and returns a Session ready for
// Initialize. It does not write anything itself: the handshake is a
// deliberate call, not a side effect of construction, so a caller that only
// wants to probe liveness some other way never pays for one.
func New(stdin io.WriteCloser, stdout io.Reader, opts Options) *Session {
	s := &Session{
		stdin: stdin,
		opts:  opts.withDefaults(),
		dead:  make(chan struct{}),
	}
	go s.read(stdout)
	return s
}

// Initialize performs the MCP handshake once: an initialize call, then the
// notifications/initialized notification the spec requires before any tool
// may be asked for. Calling it again on an already-initialized session is a
// no-op, so a caller that wants to be sure before its own first Call never
// pays for a second round trip over the wire, and Call itself calls this
// defensively for the same reason.
func (s *Session) Initialize(ctx context.Context) error {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	if s.initialized {
		return nil
	}
	_, err := s.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": s.opts.ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    s.opts.ClientName,
			"version": s.opts.ClientVersion,
		},
	})
	if err != nil {
		return err
	}
	if err := s.notify("notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	s.initialized = true
	return nil
}

// Call runs one tool and returns the concatenated text of every text-typed
// content entry, mirroring internal/adapter/serena/mcp.go's own call. A
// result with isError=true becomes an error carrying that text, or the raw
// frame clipped to 300 chars when the far side flagged a failure and then
// said nothing about it.
func (s *Session) Call(ctx context.Context, tool string, args map[string]any) (string, error) {
	if err := s.Initialize(ctx); err != nil {
		return "", err
	}
	if args == nil {
		args = map[string]any{}
	}
	raw, err := s.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return "", err
	}
	var out toolResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", contract.Fail(contract.FailureUnavailable, "%s: unreadable answer: %v", tool, err)
	}
	var text strings.Builder
	for _, part := range out.Content {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	body := text.String()
	if out.IsError {
		if body == "" {
			// isError with nothing in content is a server that flagged a
			// failure and then said nothing about it. An error built from
			// that would trim down to "", which is worse than no
			// evidence: it looks like the call answered instead of the
			// call failing silently. The raw frame is the only text left
			// to show.
			return "", contract.Fail(contract.FailureUnavailable,
				"%s reported an error with no message: %s", tool, clip(string(raw)))
		}
		// The tool ran and refused: the caller can fix a bad argument and
		// cannot fix a dead server, the same split
		// internal/passthrough/passthrough.go's resultOf makes for a
		// JSON-RPC-level refusal.
		return "", contract.Fail(contract.FailureInvalidInput, "%s: %s", tool, strings.TrimSpace(body))
	}
	return body, nil
}

// Err reports why this session is dead. Nil while it is still live.
func (s *Session) Err() error {
	select {
	case <-s.dead:
		return s.deadReason()
	default:
		return nil
	}
}

// Close releases the write end this session was given. It does not touch
// the process itself -- New was handed pipes, not a command -- so a caller
// that also owns the child still has to stop it; this only ends this
// package's half, and unblocks anyone waiting on an answer that is not
// coming now.
func (s *Session) Close() error {
	err := s.stdin.Close()
	s.markDead(nil)
	return err
}

func (s *Session) deadReason() error {
	if s.why != nil {
		return contract.Fail(contract.FailureUnavailable, "the process stopped talking: %v", s.why)
	}
	return contract.Fail(contract.FailureUnavailable, "the session is closed")
}

func (s *Session) markDead(why error) {
	s.closeOnce.Do(func() {
		s.why = why
		s.pending.clear()
		close(s.dead)
	})
}

// read routes every line the child prints on stdout back to whoever asked
// for it, until the child stops printing.
//
// The ceiling is not decoration. bufio.Reader.ReadString, which this used
// until it was written, grows a slice until it finds the delimiter, so a
// NewReaderSize only sets how much is read per syscall and puts no roof on a
// single message: a child that writes to stdout and never emits a newline
// grows that slice until the kernel kills Atenea for it. The children behind
// this package are the ones internal/supervisor launches -- kivgraph and
// tokensave, indexers whose stdout is a protocol and whose stderr the
// supervisor already caps with a ring for the same reason -- so the one pipe
// that was unbounded was the one carrying the most data.
func (s *Session) read(stdout io.Reader) {
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64<<10), maxFrame)
	for lines.Scan() {
		if trimmed := strings.TrimSpace(lines.Text()); trimmed != "" {
			s.route(trimmed)
		}
	}
	// A clean EOF leaves Err nil, and a session that ended because the child
	// exited is dead for no reason worth quoting. A frame over the ceiling is
	// the opposite: the child is not framing JSON-RPC at all, and every byte
	// after it on that pipe is unparseable by construction, so the caller is
	// owed that sentence rather than a bare "the session is closed".
	err := lines.Err()
	if errors.Is(err, bufio.ErrTooLong) {
		err = fmt.Errorf("printed more than %d bytes without a newline: it is not framing JSON-RPC", maxFrame)
	}
	s.markDead(err)
}

// route decides what one line from the child is, and there are three
// answers, not two.
//
// A response carries an id and no method. A notification carries a method
// and no id. The third is the one internal/passthrough/stdio.go's own route
// missed until 2026-08-09: a *request*, which carries both, because the
// protocol runs in both directions. A server asking `roots/list` before it
// will answer anything else, read here as a response, would be delivered to
// whichever caller is waiting on the id the server happened to reuse and
// leave that server waiting on an answer this package never sends -- one
// unanswered question wedging every call on the session, not only the one
// that provoked it.
func (s *Session) route(line string) {
	if !strings.HasPrefix(line, "{") {
		return // a log line the child printed to stdout, not JSON-RPC
	}
	var msg struct {
		// Raw, not int64: a request's id is echoed back verbatim and the
		// protocol allows a string there. Parsing it as a number would
		// answer a server with an id it never used.
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if json.Unmarshal([]byte(line), &msg) != nil {
		return
	}
	hasID := len(msg.ID) > 0 && string(msg.ID) != "null"
	// A notification carries no id and answers nobody. Matching one to a
	// waiting caller would hand a caller somebody else's news as its
	// result.
	if !hasID {
		return
	}
	if msg.Method != "" {
		s.refuse(msg.ID, msg.Method)
		return
	}
	var id int64
	if json.Unmarshal(msg.ID, &id) != nil {
		return // not an id this session could be waiting on
	}
	body, err := json.Marshal(rpcResponse{Result: msg.Result, Error: msg.Error})
	if err != nil {
		return
	}
	s.pending.deliver(id, body)
}

// refuse answers a request Atenea's side does not serve, so the far side
// stops waiting on it. Silence is the one answer that would hang a child
// that -- reasonably, since the protocol runs both ways -- expects a reply
// to something it asked.
func (s *Session) refuse(id json.RawMessage, method string) {
	body, err := json.Marshal(struct {
		Version string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   rpcError        `json:"error"`
	}{
		Version: "2.0",
		ID:      id,
		Error: rpcError{
			Code:    -32601,
			Message: fmt.Sprintf("atenea does not serve %s", method),
		},
	})
	if err != nil {
		return
	}
	// Best effort on purpose. The write can only fail on a session that is
	// already dead, and the reader is about to discover that itself.
	_ = s.write(body)
}

// rpc sends one request and waits for its answer, this session's death, or
// ctx -- whichever comes first.
func (s *Session) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := s.seq.Add(1)
	body, err := json.Marshal(rpcRequest{Version: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput, "%s: %v", method, err)
	}
	// Registered before the write, never after: a fast child can answer
	// before this goroutine is scheduled again, and an answer nobody is
	// waiting for yet is dropped.
	ch := s.pending.add(id)
	defer s.pending.drop(id)

	start := time.Now()
	if err := s.write(body); err != nil {
		return nil, err
	}
	select {
	case raw := <-ch:
		return resultOf(raw, method)
	case <-s.dead:
		return nil, s.deadReason()
	case <-ctx.Done():
		// The bin has to say which of the two this was -- a deadline this
		// call ran into, or somebody upstream changing their mind -- the
		// same distinction internal/adapter/serena/serena.go's failureFor
		// makes with the same helper.
		return nil, contract.Stopped(ctx.Err(), "mcpstdio", ceiling(ctx, start)).WithRaw(method)
	}
}

// ceiling reports how long ctx allotted this call, the number
// contract.Stopped quotes back to whoever reads the failure later. Unlike
// serena.Runner's own r.timeout, a Session carries no fixed timeout of its
// own -- the ceiling is whatever ctx each caller happens to bring to this
// one call -- so it has to be read off ctx itself: the time still on its
// deadline when this attempt started. A ctx with no deadline at all, only a
// plain cancellation, has no ceiling to quote, so the best that is left to
// report is how long this attempt actually ran before giving up.
func ceiling(ctx context.Context, start time.Time) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline.Sub(start)
	}
	return time.Since(start)
}

// notify sends a JSON-RPC notification: no id, so the far side owes no
// answer and none is waited for.
func (s *Session) notify(method string, params any) error {
	body, err := json.Marshal(rpcRequest{Version: "2.0", Method: method, Params: params})
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%s: %v", method, err)
	}
	return s.write(body)
}

// write serializes one message onto the shared pipe. Two callers writing at
// once would interleave their bytes and the child would read one corrupt
// line.
func (s *Session) write(body []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(append(body, '\n')); err != nil {
		return contract.Fail(contract.FailureUnavailable, "writing to the child: %v", err)
	}
	return nil
}

// resultOf takes the result out of a JSON-RPC answer, or turns its error
// member into the right bin -- the same split
// internal/passthrough/passthrough.go's own resultOf makes: a child
// refusing a call is not this session being unavailable, and the bin has to
// say which.
func resultOf(raw json.RawMessage, method string) (json.RawMessage, error) {
	var answer rpcResponse
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, contract.Fail(contract.FailureUnavailable, "%s: unreadable answer: %v", method, err)
	}
	if answer.Error != nil {
		return nil, contract.Fail(contract.FailureInvalidInput, "%s: %s", method, answer.Error.Message)
	}
	return answer.Result, nil
}

// pending is the routing table from request id to whoever is waiting for
// it, the same role internal/passthrough/stdio.go's own pending plays and
// for the same reason: the reader goroutine delivering an answer must never
// block on anything else, or a slow caller would hold up every other
// answer.
type pending struct {
	mu   sync.Mutex
	wait map[int64]chan json.RawMessage
}

func (p *pending) add(id int64) chan json.RawMessage {
	ch := make(chan json.RawMessage, 1)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.wait == nil {
		p.wait = make(map[int64]chan json.RawMessage)
	}
	p.wait[id] = ch
	return ch
}

func (p *pending) drop(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.wait, id)
}

// deliver hands one answer to whoever asked for it. An id nobody is waiting
// on is dropped: it is a late reply to a call that already timed out, and
// there is no one left to tell.
func (p *pending) deliver(id int64, body json.RawMessage) {
	p.mu.Lock()
	ch, ok := p.wait[id]
	delete(p.wait, id)
	p.mu.Unlock()
	if ok {
		ch <- body
	}
}

// clear abandons every waiting caller at once, which is what a dead session
// means for all of them.
func (p *pending) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.wait)
}

// rpcRequest is one JSON-RPC call. ID is omitted for notifications, which is
// what distinguishes them on the wire -- the same shape
// internal/adapter/serena/mcp.go's own rpcRequest uses.
type rpcRequest struct {
	Version string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcpstdio rpc %d: %s", e.Code, e.Message) }

// toolResult is the shape of an MCP tools/call answer.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	// IsError marks a tool that ran and refused, as opposed to a call that
	// never reached the tool. Both are failures; only this one carries a
	// message the far side wrote on purpose.
	IsError bool `json:"isError"`
}

// clip keeps an error message readable when a child answers with something
// far longer than a sentence.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
