package dbaccess

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestLeaseProcessHelper holds a shared lease until its parent closes stdin.
func TestLeaseProcessHelper(t *testing.T) {
	path := os.Getenv("ATENEA_LEASE_FIXTURE")
	if path == "" {
		return
	}
	release, err := Acquire(t.Context(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
	if err = release(); err != nil {
		t.Fatal(err)
	}
}

// TestExclusiveLeaseWaitsAcrossProcesses verifies kernel exclusion and cancellation.
func TestExclusiveLeaseWaitsAcrossProcesses(t *testing.T) {
	if !lockingSupported {
		t.Skip("descriptor locking is unsupported on this platform")
	}
	path := filepath.Join(t.TempDir(), "db")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLeaseProcessHelper$")
	cmd.Env = append(os.Environ(), "ATENEA_LEASE_FIXTURE="+path)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("helper: %q %v", line, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if release, err := Acquire(ctx, path, true); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			_ = release()
		}
		t.Fatalf("exclusive access bypassed live writer: %v", err)
	}
	_ = in.Close()
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	release, err := Acquire(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = release()
}

// TestCanonicalAliasesShareExclusion covers goroutines opening the same file by different paths.
func TestCanonicalAliasesShareExclusion(t *testing.T) {
	if !lockingSupported {
		t.Skip("descriptor locking is unsupported on this platform")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	release, err := Acquire(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if other, err := Acquire(ctx, alias, false); !errors.Is(err, context.DeadlineExceeded) {
		if other != nil {
			_ = other()
		}
		t.Fatalf("alias bypassed lock: %v", err)
	}
}

// TestConnectionsQueueWithinProcess prevents concurrent DuckDB handles for one file.
func TestConnectionsQueueWithinProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	first, err := AcquireConnection(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if second, err := AcquireConnection(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		if second != nil {
			_ = second()
		}
		t.Fatalf("concurrent in-process lease bypassed queue: %v", err)
	}

	if err = first(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireConnection(t.Context(), path)
	if err != nil {
		t.Fatalf("lease remained blocked after release: %v", err)
	}
	_ = second()
}
