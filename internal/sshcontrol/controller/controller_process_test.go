package controller

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/sshcontrol/localipc"
)

const childRootEnv = "ATENEA_SSH_CONTROLLER_TEST_ROOT"

func TestRestartAfterKilledController(t *testing.T) {
	base := t.TempDir()
	if runtime.GOOS != "windows" {
		shortTemp := "/tmp"
		if runtime.GOOS == "darwin" {
			shortTemp = "/private/tmp"
		}
		var err error
		base, err = os.MkdirTemp(shortTemp, "a151-restart-")
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

	start := func() (*exec.Cmd, *bytes.Buffer) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestControllerSubprocess$", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), childRootEnv+"="+root)
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd, &output
	}
	awaitRunning := func(cmd *exec.Cmd, output *bytes.Buffer) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			status, err := Call(context.Background(), root, id, "status")
			if err == nil && status.State == "running" {
				return
			}
			if time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("controller did not become ready: %v, output: %s", err, output.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	first, firstOutput := start()
	awaitRunning(first, firstOutput)
	contenderCtx, cancelContender := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelContender()
	contender := exec.CommandContext(contenderCtx, os.Args[0], "-test.run=^TestControllerSubprocess$", "-test.timeout=10s")
	contender.Env = append(os.Environ(), childRootEnv+"="+root)
	if output, err := contender.CombinedOutput(); err == nil || contenderCtx.Err() != nil {
		t.Fatalf("second controller acquired a live endpoint or hung: %v, output: %s", err, output)
	}
	if status, err := Call(context.Background(), root, id, "status"); err != nil || status.State != "running" {
		t.Fatalf("first controller lost its endpoint: %+v, %v", status, err)
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err == nil {
		t.Fatal("killed controller exited successfully")
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Lstat(localipc.Endpoint(root)); err != nil {
			t.Fatalf("killed controller left no socket to recover: %v", err)
		}
	}

	second, secondOutput := start()
	awaitRunning(second, secondOutput)
	if _, err := Call(context.Background(), root, id, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("restarted controller did not stop cleanly: %v, output: %s", err, secondOutput.String())
	}
}

// TestControllerSubprocess is only entered by the parent test above.
func TestControllerSubprocess(t *testing.T) {
	root := os.Getenv(childRootEnv)
	if root == "" {
		t.Skip("child-only")
	}
	id, err := InstallationID(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Serve(context.Background(), root, id); err != nil {
		t.Fatal(err)
	}
}
