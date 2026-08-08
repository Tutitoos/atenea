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
	id      string
	command []string
	env     map[string]string
	timeout time.Duration
	allowed []string

	// mu guards the spawn and everything the spawn produces. It is never held
	// across a round trip: the reader goroutine has to be able to deliver the
	// handshake's own answer while the spawn is still in progress, so ensure
	// releases it before waiting.
	mu      sync.Mutex
	proc    *process
	seq     atomic.Int64
	pending pending
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
	// gone closes when the reader sees the process stop talking. Every caller
	// waiting on an answer selects on it, so a server that dies mid-call fails
	// its callers instead of hanging them until their timeouts expire one by
	// one.
	gone chan struct{}
	// why is the reason it went, read only after gone is closed.
	why error
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
	raw, err := b.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return toolsFrom(raw, b.allowed, b.fail)
}

func (b *stdioBackend) Call(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	if !b.Allows(tool) {
		return nil, b.fail(contract.FailurePermissionDenied,
			"tool %q is not in this backend's tools", tool)
	}
	if args == nil {
		args = map[string]any{}
	}
	return b.request(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
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
			// rather than hand back a corpse.
			b.proc = nil
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
	if err := b.handshake(ctx, proc); err != nil {
		proc.stop()
		return nil, err
	}
	b.proc = proc
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
	proc := &process{cmd: cmd, stdin: stdin, stderr: &tail{}, gone: make(chan struct{})}
	go func() { _, _ = io.Copy(proc.stderr, stderrPipe) }()
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
	reader := bufio.NewReaderSize(stdout, 1<<20)
	defer func() {
		// Whoever is still waiting is waiting for a process that has stopped
		// talking. Telling them now turns one dead server into one error per
		// caller instead of one timeout per caller.
		b.pending.clear()
		close(proc.gone)
	}()
	for {
		line, err := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			b.route(trimmed)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				proc.why = err
			}
			return
		}
	}
}

func (b *stdioBackend) route(line string) {
	if !strings.HasPrefix(line, "{") {
		return // a log line
	}
	var answer struct {
		ID     *int64          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if json.Unmarshal([]byte(line), &answer) != nil {
		return
	}
	// A notification carries no id and answers nobody. Matching one to a
	// waiting caller would hand a chat somebody else's news as its result.
	if answer.ID == nil {
		return
	}
	body, err := json.Marshal(answer)
	if err != nil {
		return
	}
	b.pending.deliver(*answer.ID, body)
}

// send writes one request and waits for its answer, its timeout, or the death
// of the process.
func (b *stdioBackend) send(ctx context.Context, proc *process, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
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
	ch := b.pending.add(id)
	defer b.pending.drop(id)

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
	_, err := b.send(ctx, proc, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "atenea", "version": "1"},
	})
	if err != nil {
		return err
	}
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
