package mcpprobe_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpprobe"
)

const okResult = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18",` +
	`"serverInfo":{"name":"fake","version":"9.9.9"}}}`

func serve(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

// The happy path also has to carry back who answered: a report that can name
// the server and its version is the difference between "something is there"
// and "the thing you meant is there".
func TestALiveServerAnswersWithItsName(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, okResult)
	})
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "fake", URL: url})
	if !got.OK {
		t.Fatalf("probe failed: %v", got.Err)
	}
	if got.Name != "fake" || got.Version != "9.9.9" {
		t.Errorf("name/version = %q/%q, want fake/9.9.9", got.Name, got.Version)
	}
}

// Streamable HTTP lets the server choose its framing per response. Both are
// the same answer, and a probe that only understood one would report half the
// live servers on this machine as dead.
func TestSSEFramingIsTheSameAnswerAsPlainJSON(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", okResult)
	})
	if got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "fake", URL: url}); !got.OK {
		t.Fatalf("SSE framing read as a failure: %v", got.Err)
	}
}

// This is the live shape of chrome-devtools on the machine this was written
// on: the port answers, but not with MCP. "Declared" and "working" part
// company here, which is the whole reason the probe exists.
func TestAPortThatAnswersWithoutSpeakingMCPIsNotAlive(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "<html>not an mcp server</html>")
	})
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "x", URL: url})
	if got.OK {
		t.Fatal("a page of HTML was accepted as an MCP handshake")
	}
	if !strings.Contains(got.Err.Error(), "unreadable JSON") {
		t.Errorf("err = %v, want it to name what came back instead", got.Err)
	}
}

func TestAnHTTPErrorCarriesTheStatusAndTheBody(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no session for you", http.StatusForbidden)
	})
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "x", URL: url})
	if got.OK {
		t.Fatal("403 was accepted")
	}
	if !strings.Contains(got.Err.Error(), "403") || !strings.Contains(got.Err.Error(), "no session") {
		t.Errorf("err = %v, want the status and what the server said", got.Err)
	}
}

// A JSON-RPC error is a server that is up and refusing, which is a different
// repair from one that is down, so it must not be flattened into "failed to
// connect".
func TestARefusalCarriesTheServersOwnCode(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"bad protocol version"}}`)
	})
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "x", URL: url})
	if got.OK {
		t.Fatal("an rpc error was accepted as a handshake")
	}
	if !strings.Contains(got.Err.Error(), "-32602") || !strings.Contains(got.Err.Error(), "bad protocol") {
		t.Errorf("err = %v, want the server's own code and words", got.Err)
	}
}

// A reason is printed beside the address it belongs to, so Go's *url.Error
// wrapper restates the method and the whole URL in front of the one clause
// that says what happened. On a real refusal that is most of the line, and
// the clause is the part that gets pushed off the end.
func TestAConnectionFailureIsNotWrappedInItsOwnRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "gone", URL: url})
	if got.OK {
		t.Fatal("a closed server was accepted")
	}
	if strings.Contains(got.Err.Error(), url) {
		t.Errorf("err = %v, want the cause alone: the address is already on the line", got.Err)
	}
	if !strings.Contains(got.Err.Error(), "connection refused") {
		t.Errorf("err = %v, want the cause kept", got.Err)
	}
}

// A server that hangs is neither up nor down until somebody puts a clock on
// it. The message has to name the fact, not the Go mechanism that noticed.
func TestAServerThatNeverAnswersIsBoundedByItsTimeout(t *testing.T) {
	release := make(chan struct{})
	url := serve(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	// Registered after serve, so it runs before the server's own Close:
	// cleanups are LIFO, and Close blocks until every handler has returned.
	t.Cleanup(func() { close(release) })
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "x", URL: url, Timeout: 150 * time.Millisecond})
	if got.OK {
		t.Fatal("a hang was accepted")
	}
	if !strings.Contains(got.Err.Error(), "no answer within") {
		t.Errorf("err = %v, want the fact rather than 'context deadline exceeded'", got.Err)
	}
}

func TestNothingToReachIsSaidPlainly(t *testing.T) {
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{ID: "x"})
	if got.OK || !strings.Contains(got.Err.Error(), "nothing to reach") {
		t.Errorf("ok=%v err=%v, want a refusal naming the empty declaration", got.OK, got.Err)
	}
}

// --- stdio ---------------------------------------------------------------

func TestAStdioServerAnswersOverThePipe(t *testing.T) {
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{
		ID:      "fake",
		Command: []string{"sh", "-c", "read -r line; printf '%s\\n' '" + okResult + "'"},
	})
	if !got.OK {
		t.Fatalf("probe failed: %v", got.Err)
	}
	if got.Name != "fake" {
		t.Errorf("name = %q, want fake", got.Name)
	}
}

// This is the live shape of claude-mem: it starts, dies, and the client
// reports "MCP error -32000: Connection closed" -- which sends a reader to
// the network for a process that never got off the ground. The probe has to
// say what actually happened and hand over what the server said on its way
// down, because that text is the only diagnosis that exists.
func TestAServerThatStartsAndDiesSaysSoAndCarriesItsStderr(t *testing.T) {
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{
		ID:      "dies",
		Command: []string{"sh", "-c", "echo 'cannot find module foo' >&2; exit 1"},
	})
	if got.OK {
		t.Fatal("a server that exited immediately was accepted")
	}
	if !strings.Contains(got.Err.Error(), "exited without answering") {
		t.Errorf("err = %v, want the process fact, not a connection metaphor", got.Err)
	}
	if !strings.Contains(got.Err.Error(), "cannot find module foo") {
		t.Errorf("err = %v, want the server's own stderr carried through", got.Err)
	}
}

// Servers log to stdout despite the spec saying not to. Giving up on the
// first unparseable line would report a healthy server as dead.
func TestChatterBeforeTheAnswerIsSkipped(t *testing.T) {
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{
		ID: "chatty",
		Command: []string{"sh", "-c",
			"echo 'starting up'; echo 'loading index'; read -r line; printf '%s\\n' '" + okResult + "'"},
	})
	if !got.OK {
		t.Fatalf("chatter was read as a failure: %v", got.Err)
	}
}

func TestAMissingBinaryIsRefusedByName(t *testing.T) {
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{
		ID:      "absent",
		Command: []string{"atenea-no-such-binary-anywhere"},
	})
	if got.OK {
		t.Fatal("a command that does not exist was accepted")
	}
	if !strings.Contains(got.Err.Error(), "atenea-no-such-binary-anywhere") {
		t.Errorf("err = %v, want the command named", got.Err)
	}
}

// The probe must not leave behind the very process it exists to avoid
// spawning: a probe that warmed up every server it checked would be paying
// the cost it was written to measure. The child records its own pid and then
// tries to outlive the probe.
func TestALiveStdioServerIsNotLeftRunning(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	got := mcpprobe.Probe(t.Context(), mcpprobe.Server{
		ID: "sleeper",
		Command: []string{"sh", "-c",
			"echo $$ > " + pidFile + "; read -r line; printf '%s\\n' '" + okResult + "'; sleep 300"},
	})
	if !got.OK {
		t.Fatalf("probe failed: %v", got.Err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the child never recorded its pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("pid %q: %v", raw, err)
	}
	// Probe waits on the child before returning, so by here it is reaped and
	// signal 0 can no longer find it. A live pid means the sleep survived.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		t.Errorf("pid %d is still alive: the probe left a server running", pid)
	}
}
