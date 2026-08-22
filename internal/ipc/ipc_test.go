package ipc_test

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/ipc"
)

func TestEndpointAndDialHelpersUseTheBoundService(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "run", "core.sock")
	listener, err := ipc.Listen(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	if got := ipc.Endpoint(root); got != path {
		t.Fatalf("endpoint = %q, want %q", got, path)
	}
	conn, err := ipc.Dial(path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		peer, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- peer
		}
	}()
	select {
	case peer := <-accepted:
		peer.Close()
	case <-time.After(time.Second):
		t.Fatal("listener did not accept the helper dial")
	}
}

// The ordinary case, and the one the peer check must not break: this user
// reaching a socket this user owns.
//
// The other half -- a different uid being turned away -- has no test here, and
// cannot have one. SO_PEERCRED reports what the kernel knows about the process
// on the far end, so nothing in-process can stand in for a second user: making
// the check pass for everyone (`|| true`) leaves this suite green. Covering it
// needs a run that can become somebody else, which this one cannot.
//
// So it was verified by hand instead, on 2026-08-07, with a real second
// account against a running service: refused by the directory at the shipped
// modes, and refused by the uid check with the directory deliberately opened,
// while the owner was served through the same opening. The capture is in
// docs/content/architecture.md under "The door only opens for you". Anyone
// changing sameUser is changing something no test defends -- re-run it.
//
// What is defended here is the consequence of getting it wrong in the other
// direction -- an owner locked out of a socket they own, which is the failure
// a live machine would hit -- plus the two modes below, which are the guard
// that does not depend on uid.
func TestThisUserGetsIn(t *testing.T) {
	listener := listen(t, socketPath(t))
	go echo(listener)

	conn, err := net.Dial("unix", listener.Path())
	if err != nil {
		t.Fatalf("dialing our own socket: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("knock\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 16)
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("the socket never answered: %v", err)
	}
	if got := strings.TrimSpace(string(buf[:n])); got != "knock" {
		t.Errorf("answer = %q, want the knock back", got)
	}
	if refused := listener.Refused(); refused != 0 {
		t.Errorf("refused = %d, want 0: our own connection was turned away", refused)
	}
}

// A socket is reachable only through its directory, and the file mode is what
// stops a second user who can already traverse it. Both are set before anything
// is served, because a window here is a window in the only authentication this
// door has.
//
// The directory is made wide open first, and that is the whole point of the
// test. MkdirAll does nothing to a directory that already exists, so a version
// that only passes a mode to MkdirAll passes every test that lets it create the
// directory and still ships 0755 to anyone whose state root was already there.
// That is exactly what happened: caught by running the real binary, not here.
func TestTheDoorIsClosedToOtherUsers(t *testing.T) {
	path := socketPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	listener := listen(t, path)

	socket, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := socket.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %o, want 600", perm)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("directory mode = %o, want 700", perm)
	}
	_ = listener
}

// A killed owner leaves the name behind: the socket file is not the socket, and
// nothing removes it when the process holding it dies outright. If that
// leftover blocked the next start, one SIGKILL would lock the machine out until
// somebody deleted a file by hand.
func TestAKilledOwnerDoesNotBlockTheNextStart(t *testing.T) {
	path := socketPath(t)
	holder := hold(t, path)

	if err := holder.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	_ = holder.Wait()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the killed owner left no socket to step over, so this proves nothing: %v", err)
	}

	listener, err := ipc.Listen(path)
	if err != nil {
		t.Fatalf("a dead owner's leftover blocked the next start: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
}

// The other half of the same decision. A leftover is removed because nothing
// answers on it; a socket that does answer belongs to somebody, and taking its
// name would leave two processes believing they are reachable while every
// client reaches only one.
func TestALiveOwnerIsNotEvicted(t *testing.T) {
	path := socketPath(t)
	first := listen(t, path)
	go echo(first)

	_, err := ipc.Listen(path)
	if !errors.Is(err, ipc.ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse: the live owner was evicted", err)
	}

	// The refusal must not have taken the socket with it.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("the first owner lost its socket to a failed second bind: %v", err)
	}
	_ = conn.Close()
}

// Whatever is at the path, it is not something to delete on sight: a regular
// file there is somebody's mistake or somebody's data, and removing it to make
// room would be the tool destroying what it was pointed at.
func TestSomethingThatIsNotASocketIsLeftAlone(t *testing.T) {
	path := socketPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := ipc.Listen(path); err == nil {
		t.Fatal("a regular file was treated as a leftover socket")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the file was removed: %v", err)
	}
}

// Closing takes the name with it, so the next start finds nothing to probe.
func TestClosingRemovesTheName(t *testing.T) {
	path := socketPath(t)
	listener := listen(t, path)

	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket file survived a clean close: %v", err)
	}
	// A second close is what a deferred cleanup does after an explicit one.
	if err := listener.Close(); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Errorf("closing twice reported %v", err)
	}
}

func socketPath(t *testing.T) string {
	t.Helper()
	// Short: a unix socket path is capped near 100 bytes by the kernel, and a
	// t.TempDir() under a long test name can be most of that on its own.
	dir, err := os.MkdirTemp("", "at")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s", "core.sock")
}

func listen(t *testing.T, path string) *ipc.Listener {
	t.Helper()
	listener, err := ipc.Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func echo(listener *ipc.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			buf := make([]byte, 64)
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			_, _ = conn.Write(buf[:n])
		}()
	}
}

// hold starts a real second process holding the socket, because the behavior
// under test is what one process leaves behind for another. A goroutine cannot
// be SIGKILLed and cannot leave a socket file orphaned the way a dead process
// does.
func hold(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHoldTheSocket$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "ATENEA_HOLD_SOCKET="+path)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the holder: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); err == nil {
			return cmd
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the holder never bound the socket")
	return nil
}

// TestHoldTheSocket is the child half of hold: it binds and then waits to be
// killed. It is a no-op in an ordinary run.
func TestHoldTheSocket(t *testing.T) {
	path := os.Getenv("ATENEA_HOLD_SOCKET")
	if path == "" {
		t.Skip("child-only")
	}
	listener, err := ipc.Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go echo(listener)
	time.Sleep(55 * time.Second)
}
