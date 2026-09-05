package passthrough

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// stdioBackend is one child process Atenea starts and every chat shares.
//
// This is the mode the whole feature was written for. Six copies of the same
// indexer were running on the machine this was measured on, one per client
// session, each holding its own index of the same repositories. The reason
// there were six is that stdio has no address: a client that wants a stdio
// server has no choice but to spawn one, because there is nothing to point at.
// Atenea spawning one and lending it out is the only way that number becomes
// one.
//
// Three things make it harder than the HTTP mode, and each has cost somebody a
// day already:
//
//   - One pipe, many askers. HTTP gives every call its own request and its own
//     answer. Here every chat writes into the same stdin and reads out of the
//     same stdout, so answers have to be routed back by JSON-RPC id: a reader
//     that handed the wrong reply to the wrong chat would be worse than no
//     passthrough at all.
//   - stdin must never close. A stdio server treats EOF on stdin as its client
//     leaving and exits. The hand-rolled shim this replaces held the write end
//     of a FIFO open with `sleep infinity` for exactly this reason. Atenea
//     holds the pipe for the life of the process, so the same guarantee costs
//     nothing here -- but closing stdin is still how the process is asked to
//     go away.
//   - The handshake belongs to the process, not the chat. A fresh MCP server
//     answers nothing until it has been initialized once. That shim replayed a
//     canned `initialize` from a file to make the daemon believe a client had
//     attached; the same replay happens here, once per spawn, before any chat
//     is told the backend is up.
//
// What the shim could not do is the half that matters: `cat init.json fifo |
// server` is one-way. Nothing ever read the server's stdout, so no answer
// could come back and no tool could be called. It kept a process alive for its
// web UI and that was all it could ever do.
type stdioBackend struct {
	version atomic.Value
	id      string
	command []string
	env     map[string]string
	timeout time.Duration
	allowed []string

	// mu guards the spawn and everything the spawn produces, and it is held
	// for the whole of it -- the fork, the handshake's round trip and the
	// publication of the result -- on purpose: two chats arriving at a cold
	// backend together must produce one process, and a lock released before
	// the handshake would let the second one spawn a private copy while the
	// first was still initializing. The cost is stated rather than hidden: a
	// second arrival waits up to b.timeout, the handshake's own bound.
	//
	// What makes that safe is that the reader goroutine never takes mu. It
	// delivers the handshake's own answer through pending, which carries its
	// own lock, so the round trip inside the critical section can complete
	// without the deliverer ever needing the lock its waiter is holding.
	mu         sync.Mutex
	proc       *process
	seq        atomic.Int64
	generation atomic.Uint64
	catalog    catalogCache
	driftMu    sync.RWMutex
	drift      CatalogDrift
}

// process is one live child. It is replaced wholesale on a respawn rather than
// reset, so a late answer from a dead one can never be delivered to a caller
// waiting on its successor.
type process struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	// writeMu serializes writes onto the one shared pipe.
	writeMu sync.Mutex
	stderr  *tail
	// stderrDone closes when the copier has consumed the child's final
	// diagnostics. stdout and stderr are separate pipes, so observing EOF on
	// stdout does not by itself mean the last stderr line is already in tail.
	stderrDone chan struct{}
	// gone closes when the reader sees the process stop talking. Every caller
	// waiting on an answer selects on it, so a server that dies mid-call fails
	// its callers instead of hanging them until their timeouts expire one by
	// one.
	gone chan struct{}
	// why is the reason it went, read only after gone is closed.
	why error
	// pending belongs to the process and not to the backend, because the
	// routing table is a property of the pipe: an id means something only to
	// the process that was asked with it. It lived on the backend until this
	// was written, and the bug that shape allows is a reader clearing the
	// successor's waiters. Close breaks the ordering that hid it -- it drops
	// b.proc and only then calls stop(), so the next chat can spawn a
	// replacement and register calls on it while the previous reader is still
	// on its way to its own defer -- and that clear() would abandon callers
	// waiting on a process that is perfectly alive.
	pending pending
}

// pending is the routing table from request id to the caller waiting for it.
//
// It has its own lock rather than sharing the spawn's. A reader goroutine
// delivering an answer must never wait on a spawn, or a slow-starting server
// would block the answers of the one it replaced.
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

// deliver hands one answer to whoever asked for it. An id nobody is waiting on
// is dropped: it is a late reply to a call that already timed out, and there is
// no one left to tell.
func (p *pending) deliver(id int64, body json.RawMessage) {
	p.mu.Lock()
	ch, ok := p.wait[id]
	delete(p.wait, id)
	p.mu.Unlock()
	if ok {
		ch <- body
	}
}

// clear abandons every waiting caller at once, which is what a dead process
// means for all of them.
func (p *pending) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.wait)
}

func newStdio(spec Spec) *stdioBackend {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &stdioBackend{
		id:      spec.ID,
		command: slices.Clone(spec.Command),
		env:     spec.Env,
		timeout: timeout,
		allowed: slices.Clone(spec.Allowed),
	}
}

func (b *stdioBackend) ID() string    { return b.id }
func (b *stdioBackend) Where() string { return strings.Join(b.command, " ") }

func (b *stdioBackend) Allows(tool string) bool { return slices.Contains(b.allowed, tool) }
func (b *stdioBackend) Allowed() []string       { return slices.Clone(b.allowed) }

func (b *stdioBackend) Tools(ctx context.Context) ([]Tool, error) {
	if _, err := b.ensure(ctx); err != nil {
		b.catalog.invalidate()
		return nil, err
	}
	generation := b.generation.Load()
	return b.catalog.get(ctx, generation, func() ([]Tool, error) {
		raw, err := b.request(ctx, "tools/list", map[string]any{})
		if err != nil {
			b.catalog.invalidate()
			return nil, err
		}
		tools, drift, err := toolsFromReport(raw, b.allowed, b.fail)
		b.setCatalogDrift(drift)
		return tools, err
	})
}

func (b *stdioBackend) setCatalogDrift(drift CatalogDrift) {
	b.driftMu.Lock()
	b.drift = cloneCatalogDrift(drift)
	b.driftMu.Unlock()
}

// CatalogDrift returns the last observed catalog difference.
func (b *stdioBackend) CatalogDrift() CatalogDrift {
	b.driftMu.RLock()
	defer b.driftMu.RUnlock()
	return cloneCatalogDrift(b.drift)
}

func (b *stdioBackend) Call(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	if !b.Allows(tool) {
		return nil, b.fail(contract.FailurePermissionDenied,
			"tool %q is not in this backend's tools", tool)
	}
	if args == nil {
		args = map[string]any{}
	}
	raw, err := b.request(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if errors.Is(err, errBackendGone) {
		b.catalog.invalidate()
	}
	return raw, err
}

// Close stops the child.
//
// The opposite of the HTTP backend's Close, and the asymmetry is the point:
// there, the server belongs to somebody else and stopping it would be rude.
// Here Atenea started the process, so leaving it running would recreate the
// exact waste this mode exists to remove -- one abandoned indexer per Atenea
// restart, which is how the machine got to six in the first place.
func (b *stdioBackend) Close() {
	b.mu.Lock()
	proc := b.proc
	b.proc = nil
	b.mu.Unlock()
	b.generation.Add(1)
	b.catalog.invalidate()
	b.setCatalogDrift(CatalogDrift{})
	if proc != nil {
		proc.stop()
	}
}

// stop asks, then insists. Closing stdin is the polite request every stdio
// server understands; the kill is for one that ignores it, and it goes to the
// whole group because an MCP server routinely spawns helpers that would
// otherwise inherit the pipes and outlive it.
func (p *process) stop() {
	_ = p.stdin.Close()
	select {
	case <-p.gone:
	case <-time.After(procgroup.Grace):
	}
	_ = procgroup.Kill(p.cmd)
	// The stderr copier has to be joined before Wait, not after. os/exec
	// documents that Wait closes the pipes it created, so calling it with
	// io.Copy still reading StderrPipe hands that copy an ErrFileClosing
	// mid-read and drops whatever the child said on its way down -- which is
	// exactly the text gone() reports as the reason a backend died. The probe
	// already joins its own copier before Wait for this reason; this path did
	// not, and it is the path Close takes, so a stop during shutdown was the
	// one that lost the last words. The bound is a backstop rather than a
	// timeout: a grandchild that inherited the write end can hold the pipe
	// open after the child itself is gone, and the caller is owed a return
	// more than it is owed the final line.
	select {
	case <-p.stderrDone:
	case <-time.After(procgroup.Grace):
	}
	_ = p.cmd.Wait()
}

// request sends one call, and retries a listing -- and only a listing -- if
// the process died underneath it.
//
// The asymmetry is deliberate and it is about effects. A `tools/list` that
// died can be asked again of a fresh process: nothing happened, and a chat
// that has been open for a day should not see a tool vanish because a backend
// restarted at lunchtime. A `tools/call` cannot. The server may have run the
// tool and died on the way back with the answer, and this package cannot tell
// that apart from dying before it started -- so a silent retry would be
// Atenea running somebody's write twice because a pipe broke, which is
// exactly the kind of harm the effects declaration exists to make visible.
// The caller is told instead, and the process is already replaced for
// whoever asks next.
func (b *stdioBackend) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := b.attempt(ctx, method, params)
	if err == nil || method != "tools/list" || !errors.Is(err, errBackendGone) {
		return raw, err
	}
	return b.attempt(ctx, method, params)
}

func (b *stdioBackend) attempt(ctx context.Context, method string, params any) (json.RawMessage, error) {
	proc, err := b.ensure(ctx)
	if err != nil {
		return nil, err
	}
	return b.send(ctx, proc, method, params)
}

// ensure returns a live process, starting one if there is none.
//
// The lock is held for the spawn and the handshake because two chats arriving
// at a cold backend together must produce one process, not two -- the second
// would be a private copy created by the very code written to stop clients
// making private copies. It is not held for anything else.
func (b *stdioBackend) ensure(ctx context.Context) (*process, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.proc != nil {
		select {
		case <-b.proc.gone:
			// It died while nobody was looking. Drop it and start again
			// rather than hand back a corpse -- but bury it first. Nothing
			// else ever will: stop() runs from Close and from a failed
			// handshake, and neither of those is this path, so a server that
			// falls over on its own used to leave a process nobody had
			// waited on. Without Wait the child stays a zombie in the
			// process table for as long as Atenea runs, and the three
			// os.File ends of its pipes stay open with it, so a backend that
			// crashes and respawns in a loop leaks a descriptor triple and a
			// slot per crash. stop() is bounded and its waits are already
			// satisfied here -- gone is closed, so it takes the kill and the
			// reap and returns.
			dead := b.proc
			b.proc = nil
			dead.stop()
		default:
			return b.proc, nil
		}
	}
	proc, err := b.spawn()
	if err != nil {
		return nil, err
	}
	// The handshake is the process's, not the chat's: it happens once per
	// spawn, and every chat that arrives afterwards finds it already done.
	// Its context is the process's too. spawn() deliberately refuses
	// exec.CommandContext so the child does not die with the call that
	// happened to need it first, and handing the caller's context to the
	// handshake would undo that one line later: the first chat pressing
	// ctrl-c during initialize would take down the process every other chat
	// is about to share. WithoutCancel keeps the caller's values -- tracing,
	// deadlines that other code reads off the context -- while cutting the
	// cancellation, and b.timeout supplies the bound that the caller's
	// deadline was providing.
	handshakeCtx, done := context.WithTimeout(context.WithoutCancel(ctx), b.timeout)
	defer done()
	if err := b.handshake(handshakeCtx, proc); err != nil {
		proc.stop()
		return nil, err
	}
	b.proc = proc
	b.generation.Add(1)
	return proc, nil
}

func (b *stdioBackend) spawn() (*process, error) {
	// Deliberately not exec.CommandContext: the context here belongs to one
	// call, and the process outlives every call that uses it. Binding the
	// child's life to the first chat's context is how a shared server becomes
	// a per-chat one again, invisibly.
	cmd := exec.Command(b.command[0], b.command[1:]...)
	// Isolate rather than Contain for the same reason, and the two do not
	// mix: Contain wires a Cancel that only a CommandContext may carry, and
	// Start refuses a Cmd that has one otherwise. This backend drives the
	// child's whole life itself -- spawn on first need, stop on Close --
	// which is the case Isolate exists for.
	procgroup.Isolate(cmd)
	if len(b.env) > 0 {
		cmd.Env = environ(b.env)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, b.fail(contract.FailureUnavailable, "%v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, b.fail(contract.FailureUnavailable, "%v", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, b.fail(contract.FailureUnavailable, "%v", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, b.fail(contract.FailureUnavailable, "%v", err)
	}
	proc := &process{
		cmd:        cmd,
		stdin:      stdin,
		stderr:     &tail{},
		stderrDone: make(chan struct{}),
		gone:       make(chan struct{}),
	}
	go func() {
		defer close(proc.stderrDone)
		_, _ = io.Copy(proc.stderr, stderrPipe)
	}()
	go b.read(proc, stdout)
	return proc, nil
}

// read routes every line the server prints back to whoever asked for it, until
// the server stops printing.
//
// Servers log to stdout despite the specification saying not to -- measured on
// this machine, by the probe, before this existed -- so a line that is not
// JSON-RPC is skipped rather than treated as a protocol violation.
func (b *stdioBackend) read(proc *process, stdout io.Reader) {
	// A Scanner with a ceiling rather than ReadString, and the difference is
	// the whole point. bufio.Reader.ReadString keeps appending into a slice it
	// grows until it finds the delimiter, so NewReaderSize only sets how much
	// is read at a time and puts no roof on one message: a server that writes
	// to stdout and never emits a newline -- a hung binary dumping a core, a
	// tool result with a runaway loop behind it -- grows that slice until
	// Atenea is killed for it. The HTTP half of this package has capped its
	// reads at maxBody since it was written, for exactly the reason spelled
	// out at that constant, and it is the more defensible of the two: that
	// backend is somebody else's server, while this one is a child Atenea
	// started itself and shares between every chat, so its runaway takes all
	// of them down at once. The two now share the one ceiling.
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64<<10), maxBody)
	defer func() {
		// Whoever is still waiting is waiting for a process that has stopped
		// talking. Telling them now turns one dead server into one error per
		// caller instead of one timeout per caller.
		proc.pending.clear()
		close(proc.gone)
	}()
	for lines.Scan() {
		if trimmed := strings.TrimSpace(lines.Text()); trimmed != "" {
			b.route(proc, trimmed)
		}
	}
	// A frame over the ceiling is not a read error to shrug at: the server is
	// not framing JSON-RPC at all, and every further byte on that pipe is
	// unparseable by construction. Saying so names the fault in the caller's
	// error instead of leaving it as a bare "the server stopped".
	if err := lines.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			err = fmt.Errorf("printed more than %d bytes without a newline: it is not framing JSON-RPC", maxBody)
		}
		proc.why = err
	}
}

// route decides what one line from the backend is, and there are three
// answers, not two.
//
// A response carries an id and no method. A notification carries a method and
// no id. The third is the one this missed until 2026-08-09: a *request*, which
// carries both, because the protocol runs in both directions. Semgrep asks
// `roots/list` before it will answer its first tools/call, and a request was
// being read here as a response and delivered to a caller waiting on id 0 --
// which is nobody, since ids here start at 1. The line vanished, the server
// waited for an answer that was never coming, and every call to that backend
// died on its timeout with the backend healthy and idle. Measured: 106 bytes
// in, 47 bytes back, one deadline exceeded.
func (b *stdioBackend) route(proc *process, line string) {
	if !strings.HasPrefix(line, "{") {
		return // a log line
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
	// waiting caller would hand a chat somebody else's news as its result.
	if !hasID {
		return
	}
	if msg.Method != "" {
		b.refuse(proc, msg.ID, msg.Method)
		return
	}
	var id int64
	if json.Unmarshal(msg.ID, &id) != nil {
		return // not an id this backend can be waiting on
	}
	body, err := json.Marshal(struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}{ID: id, Result: msg.Result, Error: msg.Error})
	if err != nil {
		return
	}
	proc.pending.deliver(id, body)
}

// refuse answers a request Atenea does not serve, so the backend stops waiting.
//
// Refusing rather than implementing is the honest answer for every one of
// them today: the handshake declares no capabilities, so a server asking for
// roots, sampling or elicitation is asking for something Atenea never offered.
// What matters is that it gets *an* answer -- silence is the one reply that
// hangs a shared process for every chat attached to it, not just the one that
// provoked it.
func (b *stdioBackend) refuse(proc *process, id json.RawMessage, method string) {
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
	// Best effort on purpose. The write can only fail on a process that is
	// already gone, and the read loop is about to discover that itself.
	_ = proc.write(body)
}

// send writes one request and waits for its answer, its timeout, or the death
// of the process.
func (b *stdioBackend) send(ctx context.Context, proc *process, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutFor(ctx, b.timeout))
	defer cancel()

	id := b.seq.Add(1)
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		return nil, b.fail(contract.FailureInvalidInput, "%s: %v", method, err)
	}
	// Registered before the write, never after: a fast server can answer
	// before the writing goroutine is scheduled again, and an answer with
	// nobody waiting for it is dropped.
	ch := proc.pending.add(id)
	defer proc.pending.drop(id)

	if err := proc.write(body); err != nil {
		return nil, err
	}
	select {
	case raw := <-ch:
		return resultOf(raw, method, b.fail)
	case <-proc.gone:
		return nil, b.gone(proc, method)
	case <-ctx.Done():
		return nil, b.fail(contract.FailureTimeout, "%s: %v", method, ctx.Err())
	}
}

// write serializes one message onto the shared pipe. Two chats writing at once
// would interleave their bytes and the server would read one corrupt line.
func (p *process) write(body []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := p.stdin.Write(append(body, '\n'))
	return err
}

// gone names a dead server in the words its own stderr used, because for a
// stdio server that is usually where the reason is -- and wraps the sentinel
// the retry looks for.
func (b *stdioBackend) gone(proc *process, method string) error {
	// The stdout reader can observe the process going away a few scheduler
	// ticks before the stderr copier sees EOF. Give that copier a short chance
	// to publish the server's own reason, while retaining a bound for malformed
	// children that inherit the descriptor without exiting cleanly.
	select {
	case <-proc.stderrDone:
	case <-time.After(250 * time.Millisecond):
	}
	reason := "the server stopped"
	if proc.why != nil {
		reason = proc.why.Error()
	}
	if said := proc.stderr.String(); said != "" {
		reason += ": " + clip(said)
	}
	return fmt.Errorf("%w: %s: %s: %s", errBackendGone, b.id, method, reason)
}

// handshake is the replay the shim did from a file. It runs on the process, so
// the notification that completes it is sent before any chat can ask anything.
func (b *stdioBackend) handshake(ctx context.Context, proc *process) error {
	hello, err := b.send(ctx, proc, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "atenea", "version": "1"},
	})
	if err != nil {
		return err
	}
	b.version.Store(serverVersion(hello))
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{},
	})
	if err != nil {
		return b.fail(contract.FailureInvalidInput, "%v", err)
	}
	if err := proc.write(body); err != nil {
		return b.fail(contract.FailureUnavailable, "completing the handshake: %v", err)
	}
	return nil
}

func (b *stdioBackend) fail(kind contract.FailureKind, format string, args ...any) error {
	return contract.Fail(kind, "%s: %s", b.id, fmt.Sprintf(format, args...))
}

// environ renders a declared environment the way exec wants it, on top of the
// one Atenea is running in: a server declared with one variable still needs a
// PATH and a HOME to start at all.
func environ(extra map[string]string) []string {
	out := os.Environ()
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// tail keeps the last of what a server said rather than the first.
//
// The probe keeps the first few kilobytes, which is right for a process that
// lives for two seconds. This one may live for days: its first kilobytes are
// startup banners, and the lines worth having when it finally dies are the
// ones just before it did. Bounded for the same reason -- an unbounded buffer
// on a long-running child is a leak with a schedule.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailBytes = 4 << 10

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if extra := len(t.buf) - tailBytes; extra > 0 {
		t.buf = append(t.buf[:0], t.buf[extra:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// Version returns the observed handshake version without starting a process.
func (b *stdioBackend) Version() string { v, _ := b.version.Load().(string); return v }

// SchemaHash reads discovery evidence without starting a process.
func (b *stdioBackend) SchemaHash() string { return b.catalog.fingerprint() }
