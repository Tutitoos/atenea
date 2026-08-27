package core

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/ipc"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// SocketPath is derived from the state root and lives below a private
// Unix-socket directory.
//
// Under the state root, and not in XDG_RUNTIME_DIR where a socket of this kind
// ordinarily goes, for the reason the upkeep claim already documents: that
// variable is set for a systemd --user service and for a login shell, unset
// under cron, so two processes derive two different paths. For a lock that
// means both sweep; for a socket it means a client cannot find the service that
// is running. The state root comes from HOME, so everyone agrees. The one thing
// the runtime directory would give us -- a tmpfs wiped on reboot -- does not
// solve the failure that actually happens, which is a service killed on a
// machine that keeps running, and Listen already handles both.
//
// In a directory of its own because that directory has to be 0700, and the
// state root is not this package's to narrow. It holds the receipts, the crash
// notebook and the measurement base; whatever mode the operator has it at is
// their business, and a socket appearing inside it is no reason to change that
// underneath them.
func SocketPath() string {
	return ipc.Endpoint(platform.StateDir())
}

// askTimeout bounds a client's whole exchange with the service. Generous
// against the work being done -- a status is built from memory -- and short
// against a person waiting for a screen.
const askTimeout = 2 * time.Second

// The wire is one JSON object per line.
//
// JSON-RPC 2.0 because that is what MCP is, and this socket is where the MCP
// server will answer. Choosing the envelope now costs four struct fields and
// framing beside the first.
const (
	rpcVersion = "2.0"
	// MethodStatus asks the service for its own view of itself.
	MethodStatus = "atenea/status"
	// MethodDetect asks the service to probe, now, in its own environment.
	//
	// Separate from MethodStatus rather than a flag on it, because the two
	// cost different things: a status is built from memory and is served to
	// every `atenea status` and every client bridge that starts, while this
	// one spawns a process per declared stdio server. Folding them together
	// would put six spawns behind the most frequently called method on this
	// socket.
	MethodDetect = "atenea/detect"

	codeParse         = -32700
	codeInvalid       = -32600
	codeMethodUnknown = -32601
)

// probeAskTimeout is the backstop for a detect over the socket when the caller
// brought no deadline of its own. Generous against six servers spawning in
// parallel -- measured at about four seconds on this machine -- and still short
// enough that a wedged service does not hold a person there.
const probeAskTimeout = 60 * time.Second

// detectParams is what a detect asks about: one repository, or every one when
// empty. The same filter `atenea detect --repo` already took.
type detectParams struct {
	Repository string `json:"repository"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
	// Params is left raw: every method decodes its own shape, and a shared
	// map here would mean each of them re-checking types the decoder could
	// have checked once.
	Params json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// listen opens the door, and only the service may.
//
// A command must not bind it: the socket says "there is an Atenea here to talk
// to", and a command is gone a second later. The upkeep claim already makes
// this true -- a command never holds it -- but stating it here means the rule
// is where the socket is, not two files away.
func (c *Core) listen() (*ipc.Listener, error) {
	if c.role != Service {
		return nil, contract.Fail(contract.FailurePermissionDenied,
			"only the service opens the socket; this process is a %s", c.role)
	}
	listener, err := ipc.Listen(SocketPath())
	if err != nil {
		if errors.Is(err, ipc.ErrInUse) {
			return nil, contract.Fail(contract.FailureUnavailable,
				"another atenea is already listening at %s", SocketPath())
		}
		return nil, contract.Fail(contract.FailurePermissionDenied, "opening the socket: %v", err)
	}
	c.mu.Lock()
	c.socket = listener
	c.mu.Unlock()
	return listener, nil
}

// accept answers callers until the socket closes.
//
// The listener is handed in rather than read from the core on every turn: a
// stop closes it from another goroutine, and a field that a stop can change
// under a loop that is dereferencing it is a nil away from a panic. It was,
// once -- the tests found it before anything else did.
func (c *Core) accept(ctx context.Context, listener *ipc.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			// A closed listener is the clean stop and the only error this
			// loop may treat as the end. Everything else is the door failing
			// while the service is still meant to be behind it: Go retries
			// EINTR and ECONNABORTED itself, but descriptor exhaustion --
			// EMFILE, ENFILE -- comes back here, and returning on it
			// abandoned the socket permanently and silently. A machine that
			// briefly ran out of descriptors would leave a running Atenea
			// that nothing could ever connect to again, with the socket file
			// still on disk claiming otherwise.
			if errors.Is(err, net.ErrClosed) {
				return
			}
			_ = c.notebook.Record(notebook.Incident{
				Op:      "socket.accept",
				Detail:  err.Error(),
				Version: buildinfo.Full(),
			})
			// A short pause, because the errors that reach here are the
			// resource kind: retrying at full speed would spin a core while
			// the condition that caused it is exactly the one that needs the
			// machine's attention elsewhere. Interruptible, so a stop during
			// the pause is still prompt.
			select {
			case <-time.After(acceptBackoff):
			case <-ctx.Done():
				return
			}
			continue
		}
		// Registered under the same lock that flips the stopping flag, and
		// refused once it is set. A WaitGroup requires that an Add taking the
		// counter off zero happens before Wait, and this Add came after the
		// Accept that produced the connection: a shutdown that closed the
		// listener a moment later could see the counter at zero, return from
		// Wait, and only then race this Add -- the misuse sync.WaitGroup
		// documents. Taking c.mu makes the two orderings agree, and a
		// connection that arrives after the flag is set is hung up on rather
		// than served, which is what shutting down means.
		if !c.register() {
			_ = conn.Close()
			continue
		}
		go func() {
			defer c.conns.Done()
			c.answer(ctx, conn)
		}()
	}
}

// acceptBackoff is how long the accept loop pauses after an error it intends
// to retry. Long enough that a descriptor shortage is not made worse by a
// tight loop, short enough that a caller arriving after the shortage clears
// is answered rather than kept waiting.
const acceptBackoff = 50 * time.Millisecond

// register admits one accepted connection, or refuses it because a stop has
// begun. The counter and the flag are read and written under the same lock
// for the reason accept's own comment gives.
func (c *Core) register() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopping {
		return false
	}
	c.conns.Add(1)
	return true
}

// answer serves one connection: a line in, a line out, until it hangs up.
func (c *Core) answer(ctx context.Context, conn net.Conn) {
	// A stop has to reach a caller sitting on an open connection, and closing
	// the listener does not close what it already accepted.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer func() { _ = conn.Close() }()

	lines := bufio.NewScanner(conn)
	lines.Buffer(make([]byte, 0, 64*1024), maxRequestLine)
	writer := json.NewEncoder(conn)
	// One conversation per connection, and it dies with it: the chat a client
	// opens is closed by hanging up, which is the only signal a client that
	// crashed will ever send.
	// The look-then-act permission is read once, here, and held for the life of
	// the connection. Reading it per call would let a settings edit change what
	// a chat may do halfway through the loop it is already running.
	talk := &conversation{core: c, screen: taint{permitted: c.settings.Desktop.LookThenAct}}
	defer talk.close()
	for lines.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(lines.Bytes(), &req); err != nil {
			_ = writer.Encode(rpcResponse{JSONRPC: rpcVersion,
				Error: &rpcError{Code: codeParse, Message: "not JSON"}})
			return
		}
		if answer := talk.dispatch(ctx, req); answer != nil {
			_ = writer.Encode(answer)
		}
	}
	// Scan returning false is two different facts and only one of them is a
	// client hanging up. The other is a request over the scanner's one-mebibyte
	// token limit, and until this was written the two were indistinguishable
	// from the client's side: the connection closed, the conversation closed,
	// and no JSON-RPC answer was ever written -- so a caller that sent a large
	// tools/call saw a dropped socket and had nothing to tell it that the size
	// was the reason. Saying so costs one line and turns an unexplained
	// disconnect into a fixable message.
	if err := lines.Err(); err != nil {
		// Unless we are the ones who closed it. The watcher above closes this
		// connection when the context is done, which makes Scan return
		// net.ErrClosed -- a stop, not a client that sent something
		// unreadable. Archiving that as an incident put one false
		// "socket.request" in the notebook per connected client on every
		// clean stop, and described a failure that had not happened; the
		// Encode alongside it wrote to a socket already closed. It is also
		// the write that outlived Run: the notebook file lands in the state
		// root, so a caller removing that root after the stop raced a report
		// about the stop itself.
		if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
			return
		}
		message := "the request was not readable: " + err.Error()
		if errors.Is(err, bufio.ErrTooLong) {
			message = "the request is over the " + limitWords + " limit for one line"
		}
		_ = writer.Encode(rpcResponse{JSONRPC: rpcVersion,
			Error: &rpcError{Code: codeInvalid, Message: message}})
		_ = c.notebook.Record(notebook.Incident{
			Op: "socket.request", Detail: message, Version: buildinfo.Full()})
	}
}

// maxRequestLine is the largest single request this door accepts, and
// limitWords is the same number in the sentence a refused caller is given. A
// literal in the message would be a second place to change it and the two
// would eventually disagree about what the limit is.
const (
	maxRequestLine = 1 << 20
	limitWords     = "1 MiB"
)

// closeSocket shuts the door. It does not wait for whoever is still inside:
// that wait belongs to Shutdown, under the same bounded margin as the rest of
// the in-flight work, because c.conns.Wait() has no bound of its own and a
// handler that never returns would hang the stop before the grace timer was
// even started.
//
// It is worth being exact about what closing the listener does and does not
// reach, because the sentence this comment used to carry -- that a caller
// mid-question is owed its answer -- was not true of the code. accept and
// answer are given Run's context, which is the signal's, so the watchdog
// goroutine in answer closes every open connection the moment SIGTERM
// arrives, before Shutdown runs at all. Closing the listener stops new
// callers; the ones already inside were already cut off. The margin that
// follows is therefore for work that outlives its connection -- a dispatch
// still running, a batch still to be written -- and not for a client waiting
// on a reply that is no longer coming.
//
// The field is left pointing at the closed listener rather than nilled. A
// closed one is inert -- Close is idempotent and Accept only reports that it
// is over -- and clearing it would be a write racing every reader for no gain.
func (c *Core) closeSocket() {
	c.mu.Lock()
	listener := c.socket
	c.mu.Unlock()
	if listener == nil {
		return
	}
	_ = listener.Close()
}

// Asked is a client's side of the same conversation: dial the running service
// and ask it for its own view.
//
// The second return says whether anybody answered, and a false is ordinary --
// no service is running is the normal state of a machine where somebody types
// one command. Only a live, well-formed answer counts as yes.
//
// Bounded, because the caller's whole reason for asking is that it has a worse
// answer ready. A socket with a listener behind it that never replies -- a
// wedged service, or something else holding the name -- makes connect succeed
// and the read block forever, and an `atenea status` that hangs is worse than
// one that quietly falls back. The deadline covers the write and the read: the
// service builds this from memory and answers in microseconds, so anything
// near a second means it is not going to.
func Asked() (Status, bool) {
	conn, err := ipc.DialTimeout(SocketPath(), askTimeout)
	if err != nil {
		return Status{}, false
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(askTimeout)); err != nil {
		return Status{}, false
	}

	if err := json.NewEncoder(conn).Encode(rpcRequest{
		JSONRPC: rpcVersion, ID: 1, Method: MethodStatus,
	}); err != nil {
		return Status{}, false
	}
	var out struct {
		Result Status    `json:"result"`
		Error  *rpcError `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&out); err != nil || out.Error != nil {
		return Status{}, false
	}
	return out.Result, true
}

// AskedDetect asks the running service to probe on the caller's behalf.
//
// The deadline comes from the caller's context rather than askTimeout, and that
// is the difference between this and Asked: a status is memory and answers in
// microseconds, while this spawns a process per stdio server. Measured on this
// machine, six servers take about four seconds and semgrep alone takes two, so
// the two-second bound that makes Asked safe would make this fail every time.
//
// Connecting is still bounded tightly. A door with nobody behind it is the
// ordinary state of a machine, and the caller has a local sweep ready.
func AskedDetect(ctx context.Context, repository string) (Detection, bool) {
	conn, err := ipc.DialTimeout(SocketPath(), askTimeout)
	if err != nil {
		return Detection{}, false
	}
	defer func() { _ = conn.Close() }()
	// A context with no deadline leaves the read unbounded, which is the one
	// thing this must not do: the reason to ask is that a local answer is
	// already available.
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(probeAskTimeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return Detection{}, false
	}

	params, err := json.Marshal(detectParams{Repository: repository})
	if err != nil {
		return Detection{}, false
	}
	if err := json.NewEncoder(conn).Encode(rpcRequest{
		JSONRPC: rpcVersion, ID: 1, Method: MethodDetect, Params: params,
	}); err != nil {
		return Detection{}, false
	}
	var out struct {
		Result Detection `json:"result"`
		Error  *rpcError `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&out); err != nil || out.Error != nil {
		return Detection{}, false
	}
	return out.Result, true
}
