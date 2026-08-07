// Package mcpprobe asks an MCP server one question: are you there, and do you
// speak MCP? It is the smallest client that can tell a live server from a dead
// one, and it exists because "declared" and "working" are different facts.
//
// A settings file naming an endpoint is a claim. Nothing checks a claim like
// that until a tool call needs it, and by then the client has already started,
// already told a model what it can do, and already lost the one moment where
// saying "this one is not there" would have been useful. Measured on the
// machine this was written on: five MCP servers declared in one client's
// config, two of them dead for long enough that nobody could say when they
// died, and the client reported both as a warning nobody reads.
//
// Deliberately not a general MCP client. It opens a connection, sends
// `initialize`, reads one answer and leaves; it never calls a tool, never
// reuses a session, and never retries. internal/adapter/serena has the real
// client, and it is not shared with this one on purpose: that one owns a
// long-lived session whose lifecycle is most of its code, and a probe that
// borrowed it would inherit a session it has no use for and would have to
// remember not to keep.
package mcpprobe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Tutitoos/atenea/internal/procgroup"
)

// protocolVersion is the MCP revision the probe announces. A server that
// speaks an older one still answers `initialize` -- version negotiation is
// what the handshake is for -- so this being ahead of a server is not a
// failure and must not be read as one.
const protocolVersion = "2025-06-18"

// maxNoise caps how many lines a stdio server may print before its answer.
// Servers log to stdout despite the spec saying not to, and a probe that
// gave up on the first unparseable line would call them all dead. A server
// that has not answered within this many lines is not framing JSON-RPC.
const maxNoise = 64

// prober is the probe's own client rather than http.DefaultClient. One
// request is made per server and the connection is never wanted again, so
// keep-alives would only hold sockets open to endpoints that just proved
// they are not worth talking to. The deadline is the context's; nothing is
// set here that would quietly override a caller's Timeout.
var prober = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

// Server is one endpoint to check. Exactly one of URL or Command is set;
// which one is set is what picks the transport.
type Server struct {
	ID      string
	URL     string
	Command []string
	Env     map[string]string
	Timeout time.Duration
}

// Result is what came back. Err is the whole diagnosis when OK is false: it
// carries the server's own words wherever the server gave any, because the
// reason a server is down is the one thing the operator cannot guess.
type Result struct {
	ID      string
	OK      bool
	Name    string
	Version string
	Took    time.Duration
	Err     error
}

// Transport names how this server was reached, for a report that has to say
// where it looked.
func (s Server) Transport() string {
	if s.URL != "" {
		return "http"
	}
	return "stdio"
}

// Where is the address a reader would check by hand.
func (s Server) Where() string {
	if s.URL != "" {
		return s.URL
	}
	if len(s.Command) == 0 {
		return ""
	}
	return strings.Join(s.Command, " ")
}

type rpcRequest struct {
	Version string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

// hello is the part of an initialize result worth keeping: who answered.
type hello struct {
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

// Probe checks one server and always returns a Result -- a failure is an
// answer, not an error to propagate. Nothing here writes to disk, spawns
// anything it does not kill, or leaves a session behind.
func Probe(ctx context.Context, s Server) Result {
	started := time.Now()
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var raw json.RawMessage
	var err error
	switch {
	case s.URL != "":
		raw, err = probeHTTP(ctx, s)
	case len(s.Command) > 0:
		raw, err = probeStdio(ctx, s)
	default:
		err = errors.New("no url and no command: nothing to reach")
	}

	out := Result{ID: s.ID, Took: time.Since(started)}
	if err != nil {
		// A deadline that passed reads as a bare "context deadline exceeded",
		// which names the mechanism and not the fact. The fact is that
		// something is listening or spawning and never got to an answer,
		// which is a different repair from a refused connection.
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("no answer within %s", timeout)
		}
		out.Err = err
		return out
	}
	var who hello
	if json.Unmarshal(raw, &who) == nil {
		out.Name, out.Version = who.ServerInfo.Name, who.ServerInfo.Version
	}
	out.OK = true
	return out
}

func initializeBody() ([]byte, error) {
	return json.Marshal(rpcRequest{
		Version: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "atenea-probe", "version": "1"},
		},
	})
}

func probeHTTP(ctx context.Context, s Server) (json.RawMessage, error) {
	body, err := initializeBody()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// A streamable-HTTP server picks its own framing per response, so both
	// are advertised: refusing one would make the probe depend on the mood
	// the server happens to be in rather than on whether it is up.
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := prober.Do(req)
	if err != nil {
		// Unwrapped, because the reader already knows the address: every
		// caller of this package prints it beside the reason. Go's
		// *url.Error would restate the method and the whole URL in front
		// of the one clause that says what happened, which pushes the
		// clause off the end of a line already carrying it once.
		var wrapped *url.Error
		if errors.As(err, &wrapped) && wrapped.Err != nil {
			return nil, wrapped.Err
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	text, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("answered %s: %s", resp.Status, clip(string(text)))
	}
	return decode(string(text))
}

func probeStdio(ctx context.Context, s Server) (json.RawMessage, error) {
	cmd := exec.CommandContext(ctx, s.Command[0], s.Command[1:]...)
	// An MCP server routinely spawns helpers of its own -- language servers,
	// indexers -- and killing only the process Atenea started leaves those
	// holding the stderr pipe they inherited. Wait would then block until the
	// longest-lived orphan exited, which for a probe means hanging on exactly
	// the misbehaving server it was written to catch. Measured while writing
	// this: a probe of a server whose child slept for five minutes took five
	// minutes to report a success it already had.
	procgroup.Contain(cmd)
	if len(s.Env) > 0 {
		cmd.Env = environ(s.Env)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// The copier is ours rather than the one cmd.Stderr would start, because
	// this needs to be joinable twice over: os/exec's contract says Wait must
	// not run until reads from the pipe are done, and the probe's whole value
	// on a stdio server is the stderr it reports -- a message the child
	// already wrote, lost to a race with our own reporting, is the failure
	// this package exists to prevent, one level down.
	var stderr said
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(&stderr, stderrPipe)
	}()
	// settle waits for that copier. The bound is a backstop, not a timeout:
	// a grandchild holding the inherited write end can keep the pipe open
	// after the child itself is gone, and at this point the caller is owed
	// an answer more than it is owed the last line.
	settle := func() {
		select {
		case <-drained:
		case <-time.After(procgroup.Grace):
		}
	}
	// The tree is killed on every path out of here, including the happy one:
	// a probe that left a server running would be paying the cost it exists
	// to measure.
	defer func() {
		_ = stdin.Close()
		_ = procgroup.Kill(cmd)
		settle()
		_ = cmd.Wait()
	}()

	body, err := initializeBody()
	if err != nil {
		return nil, err
	}
	if _, err := stdin.Write(append(body, '\n')); err != nil {
		// Either way the child's stderr is complete and worth the wait: this
		// is the path where the reason lives there and nowhere else.
		settle()
		if childIsGone(err) {
			return nil, withStderr(errExited, &stderr)
		}
		return nil, withStderr(fmt.Errorf("could not be asked: %w", err), &stderr)
	}

	reader := bufio.NewReaderSize(stdout, 1<<20)
	for range maxNoise {
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			if errors.Is(err, io.EOF) {
				// The shape of a server that starts and dies. Saying so is
				// the whole point: "connection closed" sends a reader to the
				// network, and there is no network here.
				settle()
				return nil, withStderr(errExited, &stderr)
			}
			return nil, withStderr(err, &stderr)
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
			continue // a log line; servers print them despite the spec
		}
		var out rpcResponse
		if json.Unmarshal([]byte(trimmed), &out) != nil {
			continue
		}
		if out.ID == nil {
			continue // a notification racing our answer
		}
		if out.Error != nil {
			return nil, out.Error
		}
		return out.Result, nil
	}
	return nil, withStderr(fmt.Errorf("printed %d lines and never framed a reply", maxNoise), &stderr)
}

// errExited is the one sentence for a stdio server that died before it
// answered. Both places that can notice it return exactly this, so the two
// cannot drift into two wordings for one fact again.
var errExited = errors.New("exited without answering")

// childIsGone reports whether a write failed because there is no longer a
// process on the other end of the pipe.
//
// A write that fails for that reason is not a diagnosis of its own: the child
// is dead, which is exactly what the read loop below reports as EOF. Which of
// the two notices first is a race -- whether the child got far enough for the
// write to see EPIPE, or whether the request fit in the pipe buffer and the
// death surfaced one ReadString later. Measured: the same server that exits on
// startup reported `exited without answering` on the machine this was written
// on and `closed before it could be asked: write |1: broken pipe` on a CI
// runner, from the same commit.
//
// One fact must have one sentence, or every caller has to learn both and the
// OS-level one wins the reader's attention while saying the least. A write
// error that is *not* this keeps its own wording, because then the write really
// is the news.
func childIsGone(err error) bool {
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe)
}

// said is the child's stderr, collected while the child is still running.
//
// The lock is not incidental. The probe reports what a server said at the
// moment it gave up on it -- on a timeout, on a malformed reply, on a server
// that exits mid-handshake -- and every one of those moments is before
// cmd.Wait has returned, which means os/exec's copier is still writing here.
// A plain bytes.Buffer read at that point is a data race, and the reason it
// has to be read at that point is the whole feature: a stdio server's stderr
// is usually the only place the actual cause exists.
type said struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *said) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *said) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// withStderr attaches whatever the server said on its way down.
func withStderr(err error, buf *said) error {
	text := clip(strings.TrimSpace(buf.String()))
	if text == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, text)
}

func environ(extra map[string]string) []string {
	// The child inherits this process's environment and adds to it, because
	// an MCP server launched with a bare environment loses PATH and HOME and
	// fails for a reason that has nothing to do with whether it works.
	out := append([]string{}, os.Environ()...)
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// decode reads one JSON-RPC response out of either HTTP framing.
func decode(text string) (json.RawMessage, error) {
	payload := strings.TrimSpace(text)
	if payload == "" {
		return nil, errors.New("answered with an empty body")
	}
	if strings.HasPrefix(payload, "event:") || strings.HasPrefix(payload, "data:") {
		payload = sseData(payload)
		if payload == "" {
			return nil, fmt.Errorf("sent an event with no data: %s", clip(text))
		}
	}
	var out rpcResponse
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return nil, fmt.Errorf("sent unreadable JSON: %s", clip(text))
	}
	if out.Error != nil {
		return nil, out.Error
	}
	return out.Result, nil
}

// sseData pulls the payload out of SSE framing. One logical message may be
// split across several data: lines, which the spec says to rejoin.
func sseData(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			b.WriteString(strings.TrimSpace(after))
		}
	}
	return b.String()
}

// clip keeps an error readable when a server answers with a page instead of
// a sentence.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "..."
}
