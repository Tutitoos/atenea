//go:build darwin || linux

package localipc

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/internal/sshcontrol/handshake"
)

func TestNativeSocketAndSingleOwner(t *testing.T) {
	root := shortRoot(t)
	listener, err := Listen(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		var data [1]byte
		if _, err = io.ReadFull(conn, data[:]); err == nil && data[0] != 42 {
			err = ErrPeer
		}
		if err == nil {
			_, err = conn.Write([]byte{43})
		}
		done <- err
	}()
	conn, err := Dial(root, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{42}); err != nil {
		t.Fatal(err)
	}
	var data [1]byte
	if _, err := io.ReadFull(conn, data[:]); err != nil || data[0] != 43 {
		t.Fatalf("reply: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("accept did not finish")
	}
	if listener.Refused() != 0 {
		t.Fatal("same user was refused")
	}
	if _, err := Listen(root); !errors.Is(err, pidlock.ErrHeld) {
		t.Fatalf("second owner: %v", err)
	}
}

func TestPrivateRootAndSocketMode(t *testing.T) {
	t.Run("relative", func(t *testing.T) {
		if _, err := Listen("relative"); !errors.Is(err, ErrPrivateRoot) {
			t.Fatal(err)
		}
	})
	t.Run("world-readable", func(t *testing.T) {
		root := shortRoot(t)
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatal(err)
		}
		listener, err := Listen(root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		info, err := os.Lstat(root)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("root was not tightened: %v", err)
		}
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Dial(root, time.Second); !errors.Is(err, ErrPrivateRoot) {
			t.Fatalf("widened root accepted: %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := shortRoot(t)
		link := filepath.Join(t.TempDir(), "linked")
		if err := os.Symlink(root, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Listen(link); !errors.Is(err, ErrPrivateRoot) {
			t.Fatal(err)
		}
	})
	t.Run("socket widened", func(t *testing.T) {
		root := shortRoot(t)
		listener, err := Listen(root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		path := Endpoint(root)
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(path, 0o600) }()
		if _, err := Dial(root, time.Second); !errors.Is(err, ErrPeer) {
			t.Fatal(err)
		}
	})
}

func TestExistingFileAndRestart(t *testing.T) {
	root := shortRoot(t)
	run := filepath.Join(root, "run")
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	path := Endpoint(root)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(root); err == nil {
		t.Fatal("overwrote existing file")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "keep" {
		t.Fatalf("foreign file changed: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	listener, err := Listen(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err = Listen(root)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func shortRoot(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "as-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func TestNativeConnectionAndProtocolHandshake(t *testing.T) {
	root := shortRoot(t)
	listener, err := Listen(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	controllerHello, err := handshake.New(handshake.Controller, "c29b3c96-5b91-4a31-a6a4-c8df574acabb")
	if err != nil {
		t.Fatal(err)
	}
	desktopHello, err := handshake.New(handshake.Desktop, controllerHello.InstallationID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		result, err := handshake.Exchange(ctx, conn, controllerHello)
		if err == nil && result.Peer != desktopHello {
			err = ErrPeer
		}
		done <- err
	}()
	conn, err := Dial(root, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	result, err := handshake.Exchange(ctx, conn, desktopHello)
	if err != nil || result.Peer != controllerHello {
		t.Fatalf("native handshake: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
