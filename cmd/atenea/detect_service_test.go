package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
)

// The environment a verdict was earned in is part of the verdict.
//
// Measured on this machine while building the servers section: a service
// started with a minimal PATH reported context7 as FAILED with "env: 'node':
// No such file or directory" on its status screen, while `atenea detect` run
// from a shell seconds later called the same server reachable. Both were right
// about their own environment, and the operator was the only one who could not
// tell which one had answered.
//
// The two halves cannot live in one process: cli() calls run() in-process and
// serveFrom() runs the service in a goroutine beside it, so they share one
// environment by construction. This spawns the service as a real child with a
// PATH the parent does not have, which is the only arrangement where "whose
// environment answered" has an observable answer.

// serviceHelperEnv turns this test binary into the service when set. The
// pattern is the one internal/core/stdio_test.go already uses for a backend.
const (
	serviceHelperEnv   = "ATENEA_TEST_SERVICE_HELPER"
	serviceSettingsEnv = "ATENEA_TEST_SERVICE_SETTINGS"
)

// TestServiceHelper is not a test. It is the service, running in its own
// process so that its environment can differ from its caller's.
func TestServiceHelper(t *testing.T) {
	if os.Getenv(serviceHelperEnv) != "1" {
		t.Skip("helper process; runs only when the parent asks for it")
	}
	cfg, err := config.Load(os.Getenv(serviceSettingsEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: config.Load: %v\n", err)
		os.Exit(1)
	}
	atenea, err := core.New(cfg, core.Service)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: core.New: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := atenea.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "helper: Run: %v\n", err)
		os.Exit(1)
	}
}

// fakeMCP writes a server that answers one initialize and nothing else, into a
// directory of its own. A shell script rather than a compiled binary: the
// shebang is an absolute path, so it runs under a PATH that contains only this
// directory -- which is the whole trick this test turns on.
func fakeMCP(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nread -r _\n" +
		`printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18",` +
		`"serverInfo":{"name":"fake-mcp","version":"9.9.9"},"capabilities":{}}}'` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "fake-mcp"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake server: %v", err)
	}
	return dir
}

// serviceWithPATH starts the helper as a service whose PATH is exactly dir, and
// waits until it answers on the socket. The returned func stops it.
func serviceWithPATH(t *testing.T, settingsPath, dir string) (pid int, stop func()) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	cmd := exec.Command(self, "-test.run=TestServiceHelper", "-test.timeout=120s")
	// A minimal environment on purpose. PATH holds the fake server and
	// nothing else; HOME and the state root have to travel or the child
	// derives a different socket path and the test would pass by never
	// meeting the service at all.
	cmd.Env = []string{
		serviceHelperEnv + "=1",
		serviceSettingsEnv + "=" + settingsPath,
		"PATH=" + dir,
		"HOME=" + os.Getenv("HOME"),
		"XDG_STATE_HOME=" + os.Getenv("XDG_STATE_HOME"),
		"XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"),
	}
	// Held, not forwarded to os.Stderr.
	//
	// The child is this same test binary, so when it ends the testing
	// framework prints its own "PASS" and its own "coverage: 0.0% of
	// statements" line. Sent to the parent's stderr, both land in the stream
	// `go test` is parsing for the parent package, and the second one wins:
	// `go test -cover ./cmd/atenea` reported 0.0% for a package covered 72.9%,
	// which is a number a coverage gate acts on. Kept in a buffer instead, and
	// printed through t.Logf only when the test that started it failed, which
	// is the only time anybody wants to read it.
	var childOutput lockedBuffer
	cmd.Stdout, cmd.Stderr = &childOutput, &childOutput
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the service child: %v", err)
	}
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
	}
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			t.Logf("service child output:\n%s", childOutput.String())
		}
	})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := core.Asked(); ok {
			return cmd.Process.Pid, stop
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	t.Fatal("the service child never answered on its socket")
	return 0, stop
}

// lockedBuffer collects the child's output without racing the test that reads
// it: exec's copying goroutines write here while the parent test is still
// running, and the parent reads the whole thing from a cleanup.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// detectSettings declares the fake server by BARE NAME and pins no PATH, which
// is the shape three servers on this machine have and the reason they died.
func detectSettings(t *testing.T) string {
	t.Helper()
	base := settingsFile(t)
	body, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	block := "\n[[mcp_server]]\nid = \"fake\"\ncommand = [\"fake-mcp\"]\n" +
		"expose = \"raw\"\ntools = [\"noop\"]\neffects = [\"read\"]\n"
	if err := os.WriteFile(base, append(body, block...), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return base
}

// The pair. Either half alone passes for the wrong reason: only the reachable
// case passes on a build that always claims the service answered, and only the
// unreachable case passes on today's build, which never asks anyone.
func TestDetectAsksTheService(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh to write a fake server with")
	}
	_, _ = isolated(t)
	settingsPath := detectSettings(t)
	dir := fakeMCP(t)

	// The parent must not be able to reach it: the verdict below can then
	// only have come from an environment that is not this one.
	if _, err := exec.LookPath("fake-mcp"); err == nil {
		t.Skip("fake-mcp is on this machine's PATH, so the premise is gone")
	}

	pid, stop := serviceWithPATH(t, settingsPath, dir)

	out, err := cli(t, "--config", settingsPath, "detect")
	if err != nil {
		t.Fatalf("detect with a service up: %v\n%s", err, out)
	}
	line := rowFor(t, out, "fake")
	if !strings.Contains(line, "reachable") {
		t.Errorf("row = %q, want reachable: the service can spawn it and the caller cannot", line)
	}
	if !strings.Contains(line, "fake-mcp 9.9.9") {
		t.Errorf("row = %q, want the server's own name and version from the handshake", line)
	}
	// And it has to say who answered, with something the reader can check.
	if !strings.Contains(out, "service") || !strings.Contains(out, strconv.Itoa(pid)) {
		t.Errorf("output does not name the service that answered (pid %d):\n%s", pid, out)
	}

	// The other half: with nobody listening, the same command against the
	// same file must fall back, fail, and say the fallback happened.
	stop()
	waitForNoService(t)

	out, err = cli(t, "--config", settingsPath, "detect")
	if err != nil {
		t.Fatalf("detect with no service: %v\n%s", err, out)
	}
	line = rowFor(t, out, "fake")
	if strings.Contains(line, "reachable") {
		t.Errorf("row = %q, want unreachable: this process cannot spawn fake-mcp", line)
	}
	if !strings.Contains(out, "command") {
		t.Errorf("a local probe did not say it was local:\n%s", out)
	}
	if strings.Contains(out, strconv.Itoa(pid)) {
		t.Errorf("the dead service is still credited with the answer:\n%s", out)
	}
}

// The third branch, and the one that can put the original fault back without
// anybody noticing: a service is up, it is running a different settings file,
// so it must not answer for this one -- and the fallback has to say that a
// service exists rather than letting the reader conclude none does.
//
// The guard is the same one `atenea status` has had since the door existed
// (TestStatusIgnoresAServiceRunningADifferentFile). What is new is the second
// half: status falls back silently, and for a verdict about an environment,
// silence is the bug.
func TestDetectRefusesAServiceOnAnotherFile(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh to write a fake server with")
	}
	_, _ = isolated(t)
	served := detectSettings(t)
	dir := fakeMCP(t)
	if _, err := exec.LookPath("fake-mcp"); err == nil {
		t.Skip("fake-mcp is on this machine's PATH, so the premise is gone")
	}
	pid, _ := serviceWithPATH(t, served, dir)

	// A second file, declaring the same server, that the service is not
	// running. The question "what does THIS file give me" is not one the
	// service can answer.
	asked := detectSettings(t)
	out, err := cli(t, "--config", asked, "detect")
	if err != nil {
		t.Fatalf("detect: %v\n%s", err, out)
	}
	if line := rowFor(t, out, "fake"); strings.Contains(line, "reachable") {
		t.Errorf("row = %q, want unreachable: a service on another file answered", line)
	}
	if strings.Contains(out, strconv.Itoa(pid)) {
		t.Errorf("a service running another file was credited with the answer:\n%s", out)
	}
	// And the reader has to learn the fallback was not for want of a service.
	if !strings.Contains(out, served) {
		t.Errorf("the output does not say which file the live service is running:\n%s", out)
	}
}

// The upgrade window, which is a real state of this machine and not a
// hypothetical: the service is running an older build that does not know
// atenea/detect, so the ask comes back unanswered while the service is plainly
// there. Reporting that as "no service answered" would send an operator looking
// for something that is running in front of them.
func TestDetectSaysWhenAServiceRefusesTheMethod(t *testing.T) {
	settingsPath, _ := isolated(t)
	stop := serveOldService(t, settingsPath)
	defer stop()

	out, err := cli(t, "--config", settingsPath, "detect")
	if err != nil {
		t.Fatalf("detect: %v\n%s", err, out)
	}
	if !strings.Contains(out, "did not answer atenea/detect") {
		t.Errorf("a service that refused the method was not reported as such:\n%s", out)
	}
	if strings.Contains(out, "no service answered") {
		t.Errorf("a running service was reported as absent:\n%s", out)
	}
}

// serveOldService answers atenea/status like a service and refuses everything
// else by name, which is exactly what a build from before this method looks
// like from the far end of the socket.
func serveOldService(t *testing.T, settingsPath string) func() {
	t.Helper()
	path := core.SocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				lines := bufio.NewScanner(conn)
				for lines.Scan() {
					var req struct {
						ID     any    `json:"id"`
						Method string `json:"method"`
					}
					if json.Unmarshal(lines.Bytes(), &req) != nil {
						return
					}
					var answer string
					if req.Method == core.MethodStatus {
						answer = fmt.Sprintf(
							`{"jsonrpc":"2.0","id":%v,"result":{"Settings":%q,"Role":"service"}}`,
							req.ID, settingsPath)
					} else {
						answer = fmt.Sprintf(
							`{"jsonrpc":"2.0","id":%v,"error":{"code":-32601,"message":"unknown method %s"}}`,
							req.ID, req.Method)
					}
					if _, err := conn.Write([]byte(answer + "\n")); err != nil {
						return
					}
				}
			}()
		}
	}()
	return func() {
		_ = listener.Close()
		<-done
	}
}

// rowFor returns the detect line for a server id. A missing row fails the
// premise rather than an assertion: the section lists every declaration.
func rowFor(t *testing.T, out, id string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// The id is its own field, so "fake" never matches "fake-mcp" in a
		// where= clause on some other server's row.
		for i, f := range fields {
			if f == id && i > 0 {
				return line
			}
		}
	}
	t.Fatalf("no row for %q in:\n%s", id, out)
	return ""
}

// waitForNoService blocks until the socket stops answering. Killing a process
// returns before its socket is gone, and a detect that raced that would be
// asking the corpse.
func waitForNoService(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := core.Asked(); !ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the socket kept answering after the service was stopped")
}
