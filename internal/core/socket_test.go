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

// A request the scanner cannot fit is refused in words, not by hanging up.
//
// The loop reads with a bufio.Scanner capped at one mebibyte per line, and it
// used to leave the loop on Scan returning false without ever asking why. A
// client that sent a larger request then saw exactly what a clean hang-up looks
// like -- the connection closed, nothing written back -- so the one fact that
// would let it fix the call, that the request was too big, was the fact it
// never got. The size is the client's to control; being told is the service's
// job.
func TestARequestOverTheLineLimitIsRefusedInWords(t *testing.T) {
	atenea := buildService(t, socketSettings)
	stop := serve(t, atenea)
	defer stop()

	// Valid JSON all the way through, so nothing but the size can be the
	// reason it is refused.
	huge := `{"jsonrpc":"2.0","id":9,"method":"atenea/status","params":{"pad":"` +
		strings.Repeat("p", 2<<20) + `"}}`
	answer := askLong(t, huge)
	if answer == "" {
		t.Fatal("an oversized request was answered with silence: a dropped socket is not a reason")
	}
	if !strings.Contains(answer, "-32600") {
		t.Errorf("answer = %s, want an invalid-request code", answer)
	}
	if !strings.Contains(answer, "1 MiB") {
		t.Errorf("answer = %s, want it to name the limit the request went over", answer)
	}
}

// askLong is ask for a request the service may refuse before it has finished
// arriving. The read runs alongside the write on purpose: the service stops
// reading at the ceiling and closes the connection right after answering, so a
// client that wrote the whole request before looking would lose the answer to
// its own broken pipe.
func askLong(t *testing.T, line string) string {
	t.Helper()
	conn, err := net.Dial("unix", core.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	answered := make(chan string, 1)
	go func() {
		buf := make([]byte, 8192)
		n, _ := conn.Read(buf)
		answered <- strings.TrimSpace(string(buf[:n]))
	}()
	// The write is allowed to fail: the service hanging up mid-request is one
	// of the shapes this is measuring, and the answer is what the test wants.
	_, _ = conn.Write([]byte(line + "\n"))
	select {
	case got := <-answered:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("nothing came back")
		return ""
	}
}

// The shutdown margin has to cover the connection handlers too.
//
// Shutdown promises a bounded margin and explicitly refuses to wait forever,
// but the wait for connection handlers used to sit inside closeSocket, ahead of
// the grace timer and with no limit of its own. A client that connects and then
// says nothing leaves its handler parked in Scan, which is neither a bug nor
// rare -- an editor holding a session open does exactly this -- and the whole
// stop hung on it, before the timer that was supposed to bound it had started.
// What is being pinned here is not that the stop is fast; it is that it ends.
func TestAConnectionThatNeverEndsDoesNotHangTheStop(t *testing.T) {
	atenea := buildService(t, socketSettings)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- atenea.Run(ctx) }()
	waitForDoor(t, ran)

	conn, err := net.Dial("unix", core.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	// One round trip, so the handler is certainly registered and certainly
	// back in Scan by the time the stop begins.
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"atenea/status"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := conn.Read(make([]byte, 8192)); err != nil {
		t.Fatalf("read: %v", err)
	}
	// And now it says nothing more, holding the connection open.

	stopped := make(chan error, 1)
	go func() { stopped <- atenea.Shutdown() }()
	select {
	case err := <-stopped:
		// The grace is two seconds in these settings and the handler is still
		// parked, so the honest answer is that the margin ran out.
		if contract.KindOf(err) != contract.FailureTimeout {
			t.Errorf("Shutdown = %v (%v), want a timeout naming the margin", err, contract.KindOf(err))
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Shutdown never returned: the wait for connection handlers is outside the margin")
	}
	cancel()
	<-ran
}

// waitForDoor blocks until the service has opened its socket, failing with
// Run's own error if it stopped before it got there -- which is the failure
// the polling loop would otherwise hide behind a timeout about something else.
func waitForDoor(t *testing.T, ran <-chan error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-ran:
			t.Fatalf("the service stopped before it opened its door: %v", err)
		default:
		}
		if _, err := os.Lstat(core.SocketPath()); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the service never opened its door")
}
