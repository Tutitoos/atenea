package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
)

// The screen a command prints is now sometimes another process's. When a
// service is up, `atenea status` asks it instead of working the answer out from
// disk, and the `process` line is where that shows: a command that reached the
// service prints `service`, and a command that found nobody prints `command`.
//
// Both halves are here because either alone passes for the wrong reason. A test
// that only checks the reachable case passes on a build that always says
// `service` from a local core it built itself; a test that only checks the
// unreachable case passes on the build this phase replaced, which never asked
// anyone. The pair is what pins the fallback to the socket actually answering.
func TestStatusAsksTheServiceWhenOneIsListening(t *testing.T) {
	settingsPath, _ := isolated(t)

	out, err := cli(t, "--config", settingsPath, "status")
	if err != nil {
		t.Fatalf("status with no service: %v\n%s", err, out)
	}
	if !strings.Contains(out, "process   command") {
		t.Fatalf("with nobody listening the screen is not the command's own:\n%s", out)
	}

	stop := serveFrom(t, settingsPath)
	defer stop()

	out, err = cli(t, "--config", settingsPath, "status")
	if err != nil {
		t.Fatalf("status with a service up: %v\n%s", err, out)
	}
	if !strings.Contains(out, "process   service") {
		t.Errorf("a listening service was not asked:\n%s", out)
	}
}

// A door that is answered by something other than a service must not silently
// become the screen. The command has no way to check what is on the far end
// beyond the protocol itself, so a reply it cannot read has to fall back to
// what it can work out alone rather than print a blank or fail the command.
func TestStatusFallsBackWhenTheDoorAnswersNonsense(t *testing.T) {
	settingsPath, _ := isolated(t)

	stop := serveGibberish(t)
	defer stop()

	out, err := cli(t, "--config", settingsPath, "status")
	if err != nil {
		t.Fatalf("status against a broken door: %v\n%s", err, out)
	}
	if !strings.Contains(out, "process   command") {
		t.Errorf("nonsense on the socket was taken for a service:\n%s", out)
	}
}

// A door that is opened and never answered is the case the fallback exists
// for. Connect succeeds against any listener, so a wedged service -- or
// anything else holding the name -- leaves the command blocked on a read that
// never comes. An `atenea status` that hangs is worse than one that prints the
// poorer screen, so the ask is bounded and the fallback still runs.
func TestStatusDoesNotHangOnADoorThatNeverAnswers(t *testing.T) {
	settingsPath, _ := isolated(t)

	stop := serveSilence(t)
	defer stop()

	done := make(chan string, 1)
	go func() {
		out, err := cli(t, "--config", settingsPath, "status")
		if err != nil {
			done <- "status failed: " + err.Error()
			return
		}
		done <- out
	}()

	select {
	case out := <-done:
		if !strings.Contains(out, "process   command") {
			t.Errorf("a silent door was taken for a service:\n%s", out)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("status hung on a door that never answered")
	}
}

// Naming a file is a different question from asking after Atenea, and a
// service running some other file is not the answer to it. The screen falls
// back to what the named file gives rather than reporting numbers that belong
// to a configuration the caller did not ask about.
func TestStatusIgnoresAServiceRunningADifferentFile(t *testing.T) {
	settingsPath, _ := isolated(t)
	stop := serveFrom(t, settingsPath)
	defer stop()

	other := settingsFile(t)
	out, err := cli(t, "--config", other, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "process   command") {
		t.Errorf("a service on another file answered for this one:\n%s", out)
	}
	if !strings.Contains(out, other) {
		t.Errorf("the screen is not about the file that was named:\n%s", out)
	}
}

// serveFrom starts a real service on the socket the CLI will look for, from the
// same settings file the CLI is pointed at, and waits until it is answering.
func serveFrom(t *testing.T, settingsPath string) func() {
	t.Helper()
	cfg, err := config.Load(settingsPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	atenea, err := core.New(cfg, core.Service)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = atenea.Run(ctx)
	}()
	waitForSocket(t)
	return func() {
		cancel()
		<-done
	}
}

// waitForSocket blocks until the service is answering, rather than until the
// file exists: the name appears at bind and the greeting works a moment later,
// and a test that raced that gap would fail as "the service was not asked".
func waitForSocket(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := core.Asked(); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the service never answered on its socket")
}

// serveSilence binds the socket and accepts callers without ever replying.
// Holding the connection open is the point: closing it would give the client
// an EOF to react to, which is not the failure under test.
func serveSilence(t *testing.T) func() {
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
	var held []net.Conn
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				for _, c := range held {
					_ = c.Close()
				}
				return
			}
			held = append(held, conn)
		}
	}()
	return func() {
		_ = listener.Close()
		<-done
	}
}

// serveGibberish binds the real socket path and answers every caller with
// something that is not the protocol.
func serveGibberish(t *testing.T) func() {
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
			_, _ = conn.Write([]byte("this is not JSON\n"))
			_ = conn.Close()
		}
	}()
	return func() {
		_ = listener.Close()
		<-done
	}
}
