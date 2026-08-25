// Package passthrough holds one declared backend open and re-offers that
// server's own tools verbatim, under a name that says it is not a capability.
//
// This is the weaker of the two things Atenea does and it is deliberately
// kept apart from the other one. A capability is a promise with a schema, a
// funnel and competitors behind it; a passthrough tool is somebody else's
// tool with somebody else's name on it, reached because a client would
// otherwise spawn its own copy of the same server. Nothing here consults the
// catalog, ranks anything, or records a measurement: there is no competitor
// for a raw tool, so a number about it would be evidence in a decision that
// never happens.
//
// The seam is the name. Everything offered from here is `raw.<server>.<tool>`
// and the contract refuses `raw` as the first segment of any capability or
// implementation id, so the two namespaces cannot collide and a reader can
// tell which of the two they are holding without looking anything up.
package passthrough

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// protocolVersion is the MCP revision Atenea announces to a backend. It is
// the same revision Atenea serves to its own clients: speaking two would mean
// translating between them, and nothing here translates -- a tool's schema
// and its result are forwarded as they were given.
const protocolVersion = "2025-06-18"

// maxBody caps what a backend may return in one answer. A passthrough result
// is forwarded into a chat, so an unbounded read would let a backend decide
// how much of a client's context to spend.
const maxBody = 8 << 20

// Tool is one of a backend's own tools, as the backend described it.
//
// The schema is carried as raw JSON rather than a decoded map because nothing
// here needs to read it: it is handed to a client exactly as it arrived. A
// decode-and-re-encode would be an opportunity to change it, and the whole
// point of raw is that nobody did.
type Tool struct {
	// Name is the backend's own name for the tool, without the prefix.
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Backend is one server Atenea keeps a session with, shared by every chat.
//
// Shared is the only instance policy here and it is the one that makes the
// whole feature worth having: six clients each spawning a private copy is the
// waste this replaces. It also means the backend outlives any single chat, so
// nothing behind this interface may hold a chat's identity -- the session
// belongs to the process, not to whoever happened to open it.
//
// There are two implementations and the difference between them is only how
// bytes reach the server. Everything above this line -- the budget, the
// effects gate, the naming, the receipts -- is written once against this
// interface, because a rule that had to be re-implemented per transport is a
// rule that would eventually differ per transport.
type Backend interface {
	// ID is the declared name of the server, which is the middle segment of
	// every tool this backend offers.
	ID() string
	// Where is the address or the command, for a report that has to say
	// where it looked.
	Where() string
	// Tools is what the server offers right now, already filtered to the
	// budget.
	Tools(ctx context.Context) ([]Tool, error)
	// Call runs one tool and hands back what the server said, whole.
	Call(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error)
	// Allows reports whether a tool name is inside the declared budget.
	Allows(tool string) bool
	// Allowed is the budget as declared, for a report that has to show it.
	Allowed() []string
	// Close releases whatever this backend is holding. What that means is
	// the one thing the two modes genuinely disagree about: an HTTP backend
	// forgets a session it did not create, a stdio backend stops a process
	// it did.
	Close()
}

// Spec is a declared backend, in the words the settings file used.
//
// One struct for both modes rather than two constructors, because which mode
// a block means is not the operator's choice to make twice: settings already
// refuses a block that names both a url and a command, so exactly one of the
// two fields is set here and the choice is a consequence of that, not a
// second decision that could disagree with the first.
type Spec struct {
	ID      string
	URL     string
	Command []string
	Env     map[string]string
	Timeout time.Duration
	Allowed []string
}

// New prepares a backend. Nothing is dialed and nothing is spawned here: a
// server that is down at startup must not stop Atenea from starting, and one
// that comes up later must start working without a restart, so the first call
// that needs the server is the one that reaches for it.
func New(spec Spec) Backend {
	if len(spec.Command) > 0 {
		return newStdio(spec)
	}
	return newHTTP(spec)
}

type httpBackend struct {
	id      string
	url     string
	timeout time.Duration
	client  *http.Client
	// allowed is the operator's budget: the only tool names this backend
	// may offer or run, whatever it advertises. It is held here rather than
	// applied by the caller because there is no reading of an unlisted tool
	// that any caller should be able to choose -- a filter one layer up is
	// a filter the next caller can forget.
	allowed []string

	// mu guards the handshake and the id it produces. Two chats calling a
	// cold backend at the same moment must produce one session, not two:
	// the second would be a private copy created by the very code written
	// to stop clients making private copies.
	mu      sync.Mutex
	session string
	open    bool
	// seq numbers requests. It is atomic rather than under mu because the
	// handshake numbers its own messages while holding mu.
	seq atomic.Int64
}

func newHTTP(spec Spec) *httpBackend {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &httpBackend{
		id:      spec.ID,
		url:     spec.URL,
		timeout: timeout,
		allowed: slices.Clone(spec.Allowed),
		// Keep-alives are wanted here, unlike the probe's client: this is a
		// session that will be used again, and re-dialing per call would add
		// a handshake to every tool a chat runs.
		client: &http.Client{},
	}
}

// Allows reports whether a tool name is inside the declared budget.
func (b *httpBackend) Allows(tool string) bool { return slices.Contains(b.allowed, tool) }

// Allowed is the budget as declared, for a report that has to show it.
func (b *httpBackend) Allowed() []string { return slices.Clone(b.allowed) }

// ID is the declared name of the server, which is the middle segment of every
// tool this backend offers.
func (b *httpBackend) ID() string { return b.id }

// Where is the address, for a report that has to say where it looked.
func (b *httpBackend) Where() string { return b.url }

// Name is the one place a passthrough tool's public name is built.
//
// It exists so that the prefix is never written by hand at a call site: a
// second spelling of it would be a namespace with a hole in it.
func Name(server, tool string) string {
	return contract.ReservedNamespace + "." + server + "." + tool
}

// Split takes a public name back apart, and reports whether it was one of
// ours at all.
//
// The server id may not contain a dot -- settings refuses one -- so the split
// is unambiguous: first segment is the marker, second is the server, and
// everything after it belongs to the tool, whose own name may contain
// anything the backend likes.
func Split(name string) (server, tool string, ok bool) {
	rest, found := strings.CutPrefix(name, contract.ReservedNamespace+".")
	if !found {
		return "", "", false
	}
	server, tool, found = strings.Cut(rest, ".")
	if server == "" || tool == "" || !found {
		return "", "", false
	}
	return server, tool, true
}

// Tools asks the backend what it offers.
//
// Asked on every list rather than cached at startup, for the reason the
// design already gives: a backend's tool list is not a constant. One of the
// servers on the machine this was written for advertises eight tools cold and
// fourteen warm, so a snapshot taken at declaration time would be wrong by
// lunchtime and wrong in the direction that hides tools rather than the one
// that fails loudly.
func (b *httpBackend) Tools(ctx context.Context) ([]Tool, error) {
	raw, err := b.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return toolsFrom(raw, b.allowed, b.fail)
}

// toolsFrom reads a tools/list answer and keeps only what the budget allows.
//
// Shared by both transports on purpose: this is where the allow list stops
// being a declaration and starts being the list a chat sees, and a second
// copy of it is a second place a tool could leak through.
func toolsFrom(raw json.RawMessage, allowed []string, fail failer) ([]Tool, error) {
	var body struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fail(contract.FailureUnavailable, "listing tools: %v", err)
	}
	out := make([]Tool, 0, min(len(body.Tools), len(allowed)))
	for _, t := range body.Tools {
		// Filtered against the declaration, not against what the server
		// wishes it offered. A backend that grows a tool overnight grows
		// nothing here: the list an operator wrote is the list a chat sees,
		// and a new tool arrives when somebody decides it may.
		if !slices.Contains(allowed, strings.TrimSpace(t.Name)) {
			continue
		}
		out = append(out, Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out, nil
}

// failer is a backend's own error constructor, passed to the shared helpers so
// that an error raised in common code still names the server that caused it.
type failer func(kind contract.FailureKind, format string, args ...any) error

// rpcError is the error member of a JSON-RPC answer.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// errBackendGone marks the one failure the stdio mode retries: the process
// that was answering has stopped. It is the stdio counterpart of a stale
// session -- same shape of problem, same single retry, and the same refusal
// to loop.
var errBackendGone = errors.New("passthrough: backend stopped")

// resultOf takes the result out of a JSON-RPC answer, or turns its error
// member into the right bin.
func resultOf(raw json.RawMessage, method string, fail failer) (json.RawMessage, error) {
	var answer struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fail(contract.FailureUnavailable, "%s: unreadable answer: %v", method, err)
	}
	if answer.Error != nil {
		// A backend refusing a call is not Atenea being unavailable, and the
		// bin has to say which: the caller can fix a bad argument and cannot
		// fix a dead server.
		return nil, fail(contract.FailureInvalidInput, "%s: %s", method, answer.Error.Message)
	}
	return answer.Result, nil
}

// Call runs one of the backend's tools and hands back what it said, whole.
//
// The result is not inspected. An MCP result carries `isError` for a tool
// that ran and went badly, and that belongs to the caller who asked for the
// tool: reading it here would mean this package forming an opinion about a
// tool it knows nothing about, and the opinion would end up in a health
// record for a provider with no competitor to be judged against.
func (b *httpBackend) Call(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	// Checked again here, and deliberately not only where the list is built.
	// A name that never appeared on a list is still a name a client can
	// send, and a budget enforced only by omission is a budget enforced only
	// against clients that are asking politely.
	if !b.Allows(tool) {
		return nil, b.fail(contract.FailurePermissionDenied,
			"tool %q is not in this backend's tools", tool)
	}
	if args == nil {
		args = map[string]any{}
	}
	return b.request(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
}

// Close forgets the session. The backend is somebody else's process and is
// not stopped: Atenea did not start it, and a shared server that dies when
// one of its users goes away is not shared.
func (b *httpBackend) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.session, b.open = "", false
}

// request sends one call, opening the session first when there is not one.
//
// A session that the backend has forgotten -- it restarted, or it expired the
// id -- is retried exactly once with a fresh handshake. Once, because a
// second failure is the backend saying something a retry cannot fix, and a
// loop here would turn one dead server into a stall in every chat attached to
// it.
func (b *httpBackend) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := b.attempt(ctx, method, params)
	if err == nil {
		return raw, nil
	}
	if !errors.Is(err, errStaleSession) {
		return nil, err
	}
	b.Close()
	raw, err = b.attempt(ctx, method, params)
	if errors.Is(err, errStaleSession) {
		return nil, b.fail(contract.FailureUnavailable,
			"%s: the session was refused twice; the backend is not keeping one", method)
	}
	return raw, err
}

// errStaleSession marks the one failure worth retrying: the backend does not
// know the session id we are using.
var errStaleSession = errors.New("passthrough: session not found")

func (b *httpBackend) attempt(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := b.ensure(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	session := b.session
	b.mu.Unlock()
	raw, opened, err := b.send(ctx, method, params, session)
	// send does not write b.session, and this is why. It runs on both sides of
	// the lock -- ensure calls it holding mu, this path calls it without --
	// so a Lock inside it would deadlock the first chat to touch a cold
	// backend and leaving it unlocked was a write racing every other chat's
	// read two lines above. Adopting the observed id here keeps the field's
	// only writers in the two places that can take the lock honestly.
	if session == "" && opened != "" {
		b.mu.Lock()
		if b.session == "" {
			b.session = opened
		}
		b.mu.Unlock()
	}
	return raw, err
}

// ensure performs the handshake once, under the lock, for whoever gets there
// first. Everyone else waits and then finds it done.
func (b *httpBackend) ensure(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open {
		return nil
	}
	// The handshake's own answer is not kept: what it says about the server
	// is already on `atenea wrap`'s report, and a tool list taken here would
	// be the snapshot Tools deliberately refuses to cache.
	_, opened, err := b.send(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "atenea", "version": "1"},
	}, "")
	if err != nil {
		return err
	}
	// Written under the lock this function already holds, which is what makes
	// the field safe to read anywhere else that takes it.
	b.session = opened
	// The notification carries the session id the initialize answer set, and
	// a server that never issued one is talking sessionless -- which is
	// allowed, and which the empty string already expresses.
	if err := b.notify(ctx, "notifications/initialized", b.session); err != nil {
		return err
	}
	b.open = true
	return nil
}

// send performs one JSON-RPC round trip and returns the result member, plus
// the session id the server issued for a call that carried none.
//
// The session travels back rather than being stored here because this method
// has two callers with two different relationships to the lock -- ensure holds
// mu, attempt does not -- and there is no way to write the field from inside
// that is correct for both. Handing the observation up leaves each caller to
// record it the way its own lock allows.
func (b *httpBackend) send(ctx context.Context, method string, params any, session string) (json.RawMessage, string, error) {
	// Atomic rather than guarded by mu: send is called from ensure, which
	// already holds mu, and taking it again there would deadlock the first
	// chat to touch a cold backend. The counter needs atomicity, not the
	// session lock's ordering.
	id := b.seq.Add(1)

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		return nil, "", b.fail(contract.FailureInvalidInput, "%s: %v", method, err)
	}
	answered, err := b.post(ctx, body, session)
	if err != nil {
		return nil, "", err
	}
	if answered.code == http.StatusNotFound && session != "" {
		return nil, "", errStaleSession
	}
	if answered.code >= 400 {
		return nil, "", b.fail(contract.FailureUnavailable, "%s: answered %s: %s",
			method, answered.status, clip(answered.text))
	}
	// The id is reported from the response headers on the handshake only: a
	// server that rotates it mid-session would be inventing a protocol, and a
	// call that already carried one is not being told anything new.
	opened := ""
	if session == "" {
		opened = answered.session
	}
	payload, err := decode(answered.text)
	if err != nil {
		return nil, opened, b.fail(contract.FailureUnavailable, "%s: %v", method, err)
	}
	raw, err := resultOf(payload, method, b.fail)
	return raw, opened, err
}

// notify sends a message that is owed no answer.
func (b *httpBackend) notify(ctx context.Context, method, session string) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return b.fail(contract.FailureInvalidInput, "%s: %v", method, err)
	}
	answered, err := b.post(ctx, body, session)
	if err != nil {
		return err
	}
	if answered.code >= 400 {
		return b.fail(contract.FailureUnavailable, "%s: answered %s: %s", method, answered.status, clip(answered.text))
	}
	return nil
}

// reply is what survives a round trip: the body is read and closed inside
// post, so handing back an *http.Response would hand back a spent one and
// invite a caller to read a closed stream.
type reply struct {
	code    int
	status  string
	session string
	text    string
}

func (b *httpBackend) post(ctx context.Context, body []byte, session string) (reply, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url, bytes.NewReader(body))
	if err != nil {
		return reply{}, b.fail(contract.FailureInvalidInput, "%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Both framings are advertised for the same reason the probe advertises
	// both: a streamable-HTTP server picks per response, and refusing one
	// would make this depend on the mood the server is in.
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		var wrapped *url.Error
		if errors.As(err, &wrapped) && wrapped.Err != nil {
			err = wrapped.Err
		}
		return reply{}, b.fail(contract.FailureUnavailable, "%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	text, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return reply{}, b.fail(contract.FailureUnavailable, "reading the answer: %v", err)
	}
	return reply{
		code:   resp.StatusCode,
		status: resp.Status,
		// Read here rather than by the caller: the header is part of the
		// answer, and the answer is what this returns.
		session: resp.Header.Get("Mcp-Session-Id"),
		text:    string(text),
	}, nil
}

// fail names the server in every error this package produces. A chat sees
// these words with no idea which of several backends produced them.
func (b *httpBackend) fail(kind contract.FailureKind, format string, args ...any) error {
	return contract.Fail(kind, "%s: %s", b.id, fmt.Sprintf(format, args...))
}

// decode reads a body that may be plain JSON or a single SSE event, which is
// the same two shapes the probe already handles.
func decode(text string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, errors.New("answered with an empty body")
	}
	if strings.HasPrefix(trimmed, "{") {
		return json.RawMessage(trimmed), nil
	}
	for line := range strings.SplitSeq(trimmed, "\n") {
		if payload, found := strings.CutPrefix(strings.TrimSpace(line), "data:"); found {
			return json.RawMessage(strings.TrimSpace(payload)), nil
		}
	}
	return nil, fmt.Errorf("answered with neither json nor an event: %s", clip(trimmed))
}

func clip(text string) string {
	const limit = 200
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
