package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFixtureControllerLifecycle(t *testing.T) {
	var base string
	if runtime.GOOS == "windows" {
		base = t.TempDir()
	} else {
		shortTemp := "/tmp"
		if runtime.GOOS == "darwin" {
			shortTemp = "/private/tmp"
		}
		var err error
		base, err = os.MkdirTemp(shortTemp, "a151-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(base) })
	}
	root := filepath.Join(base, "controller")
	id, err := InstallationID(root)
	if err != nil {
		t.Fatal(err)
	}
	again, err := InstallationID(root)
	if err != nil || again != id {
		t.Fatalf("unstable id: %q %v", again, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, root, id) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("controller exited before ready: %v", err)
		default:
		}
		status, callErr := Call(context.Background(), root, id, "status")
		if callErr == nil {
			if status.State != "running" {
				t.Fatalf("status: %+v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(callErr)
		}
		runtime.Gosched()
	}
	if _, err := Call(context.Background(), root, id, "exec"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("unexpected operation: %v", err)
	}
	if _, err := Call(context.Background(), root, id, "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not terminate")
	}
}

func TestRequestIsExactAndLimited(t *testing.T) {
	for _, raw := range []string{
		`{"Operation":"stop"}`, `{"operation":"exec"}`, `{"operation":"stop","extra":true}`, `{"operation":null}`,
	} {
		if _, err := decodeRequest([]byte(raw)); !errors.Is(err, ErrProtocol) {
			t.Fatalf("accepted %s: %v", raw, err)
		}
	}
	if req, err := decodeRequest([]byte(`{"operation":"status"}`)); err != nil || req.Operation != "status" {
		t.Fatalf("valid status: %+v %v", req, err)
	}
}
