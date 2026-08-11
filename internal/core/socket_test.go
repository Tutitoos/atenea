package core_test

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The socket is the service's, and a command must not open one. A door is a
// claim that somebody is behind it, and a command is gone a second later --
// a client that found one would connect to a process already exiting.
func TestACommandOpensNoDoor(t *testing.T) {
	atenea := build(t, socketSettings)
	defer func() { _ = atenea.Shutdown() }()

	// Bounded, and not with the test's own context, because of what breaks when
	// the guard does. A refusal returns instantly; serving returns only when the
	// context ends. Given t.Context() a broken guard makes this test hang until
	// the whole package times out ten minutes later, which reads as CI being
	// slow rather than as this line being wrong. With a deadline the same break
	// fails in two seconds and says which assertion caught it.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	err := atenea.Run(ctx)
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", contract.KindOf(err))
	}
	if _, err := os.Lstat(core.SocketPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a command left a socket behind: %v", err)
	}
}

// The ordinary case: a service is up, and a command on the same machine can ask
// it what it thinks rather than working it out from disk.
func TestAServiceAnswersForItself(t *testing.T) {
	atenea := buildService(t, socketSettings)
	stop := serve(t, atenea)
	defer stop()

	status, ok := core.Asked()
	if !ok {
		t.Fatal("nothing answered on the socket")
	}
	if status.Role != "service" {
		t.Errorf("role = %q, want service: the answer did not come from the service", status.Role)
	}
	if status.Version == "" {
		t.Error("the answer carried no version, so it is not a status")
	}
}

// The payoff, and the reason asking beats reading the disk. A chat lives in the
// service's memory and nowhere else: every command that ever printed this table
// printed an empty one, not because there were no chats but because it was
// asking the wrong process.
func TestAskingSeesAChatThatNoFileRecords(t *testing.T) {
	atenea := buildService(t, socketSettings)
	stop := serve(t, atenea)
	defer stop()

	if _, err := atenea.Open(core.SessionOptions{ID: "visible", Client: "omp"}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	status, ok := core.Asked()
	if !ok {
		t.Fatal("nothing answered on the socket")
	}
	if len(status.Chats) != 1 {
		t.Fatalf("chats = %d, want 1: the service's own table did not travel", len(status.Chats))
	}
	if status.Chats[0].ID != "visible" {
		t.Errorf("chat = %q, want visible", status.Chats[0].ID)
	}
}

// Nothing running is the normal state of a machine where somebody types one
// command, so it has to be an ordinary answer and not an error to handle.
func TestAskingNobodyIsAPlainNo(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, ok := core.Asked(); ok {
		t.Error("something answered on a socket nobody opened")
	}
}

// A stop takes the door with it. A socket file outliving its service is a
// client's dial failing obscurely, when the honest answer is that
// nobody is home.
func TestStoppingTakesTheDoorWithIt(t *testing.T) {
	atenea := buildService(t, socketSettings)
	stop := serve(t, atenea)

	path := core.SocketPath()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the service never opened its socket: %v", err)
	}
	stop()

	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket outlived the service: %v", err)
	}
	if _, ok := core.Asked(); ok {
		t.Error("a stopped service still answered")
	}
}

// The envelope is JSON-RPC because that is what MCP is, and this is where the
// MCP server will answer. A method it does not know is refused by name rather
// than by silence, because the caller that sent it is one this service is
// meant to be talking to.
func TestAnUnknownMethodIsRefusedByName(t *testing.T) {
	atenea := buildService(t, socketSettings)
	stop := serve(t, atenea)
	defer stop()

	answer := ask(t, `{"jsonrpc":"2.0","id":7,"method":"atenea/nonsense"}`)
	if !strings.Contains(answer, "-32601") {
		t.Errorf("answer = %s, want a method-not-found code", answer)
	}
	if !strings.Contains(answer, "atenea/nonsense") {
		t.Errorf("answer = %s, want the method named back", answer)
	}
}

// atenea/detect takes an optional filter, and an empty body means every
// repository -- the same default the command's --repo flag has. A method that
// required params would make the common call the awkward one.
func TestDetectOverTheSocketTakesNoParams(t *testing.T) {
	atenea := buildService(t, socketSettings)
	stop := serve(t, atenea)
	defer stop()

	answer := ask(t, `{"jsonrpc":"2.0","id":3,"method":"atenea/detect"}`)
	if strings.Contains(answer, "error") {
		t.Fatalf("answer = %s, want a result", answer)
	}
	// The service signs its answer: a caller that reached a socket cannot
	// otherwise tell which process earned the verdicts it is reading.
	if !strings.Contains(answer, "\"PID\":"+strconv.Itoa(os.Getpid())) {
		t.Errorf("answer = %s, want the answering process's own pid", answer)
	}
}

// A body that is not the shape must be refused rather than read as "every
// repository". A caller that sent something meant something, and quietly
// sweeping everything is how a filtered question gets an unfiltered answer.
func TestDetectOverTheSocketRefusesAMalformedBody(t *testing.T) {
	atenea := buildService(t, socketSettings)
	stop := serve(t, atenea)
	defer stop()

	answer := ask(t, `{"jsonrpc":"2.0","id":4,"method":"atenea/detect","params":"api"}`)
	if !strings.Contains(answer, "-32600") {
		t.Errorf("answer = %s, want an invalid-request code", answer)
	}
}

// A caller speaking some other protocol at this socket gets told so, rather
// than having its bytes guessed at.
func TestSomethingThatIsNotTheProtocolIsRefused(t *testing.T) {
	atenea := buildService(t, socketSettings)
	stop := serve(t, atenea)
	defer stop()

	if answer := ask(t, "GET / HTTP/1.1"); !strings.Contains(answer, "-32700") {
		t.Errorf("answer = %s, want a parse error", answer)
	}
	if answer := ask(t, `{"id":1,"method":"atenea/status"}`); !strings.Contains(answer, "-32600") {
		t.Errorf("answer = %s, want a rejection of the missing envelope", answer)
	}
}

// serve runs the service until the returned stop is called, and returns only
// once the door is actually open -- a test that raced the bind would fail as
// "nothing answered", which reads as a broken socket rather than a slow one.
func serve(t *testing.T, atenea *core.Core) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- atenea.Run(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(core.SocketPath()); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the service stopped before it opened: %v", err)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}

	var once bool
	return func() {
		if once {
			return
		}
		once = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the service did not stop")
		}
	}
}

// ask sends one raw line and returns one raw line, so a test can say what went
// on the wire instead of going through the client that builds it.
func ask(t *testing.T, line string) string {
	t.Helper()
	conn, err := net.Dial("unix", core.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 8192)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return strings.TrimSpace(string(buf[:n]))
}

const socketSettings = `
contract = "3.0.0"

[core]
shutdown_grace = "2s"

[orchestrator]
runners = ["local"]

  [orchestrator.local]
  implementations = ["ripgrep"]

[[capability]]
id = "code.search"
version = "1.0.0"
summary = "Find literal text in a repository."
effects = ["read"]

  [[capability.input]]
  name = "query"
  type = "string"
  required = true

  [[capability.output]]
  name = "matches"
  type = "record_list"
  required = true

    [[capability.output.field]]
    name = "path"
    type = "string"
    required = true

    [[capability.output.field]]
    name = "line"
    type = "int"
    required = true

    [[capability.output.field]]
    name = "column"
    type = "int"
    required = true

    [[capability.output.field]]
    name = "snippet"
    type = "string"

[[implementation]]
id = "ripgrep"
provider = "ripgrep"
capability = "code.search"

  [implementation.health]
  state = "alive"

[[repository]]
id = "work"
path = "/tmp"
languages = ["go"]
scale = "small"
`
