package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Tutitoos/atenea/internal/sshcontrol/handshake"
	"github.com/Tutitoos/atenea/internal/sshcontrol/localipc"
	"github.com/Tutitoos/atenea/internal/sshcontrol/wire"
)

const childRootEnv = "ATENEA_SSH_CONTROLLER_TEST_ROOT"
const idRootEnv = "ATENEA_SSH_INSTALLATION_ID_TEST_ROOT"
const idOutputEnv = "ATENEA_SSH_INSTALLATION_ID_TEST_OUTPUT"

func TestInstallationIDAcrossProcesses(t *testing.T) {
	root := filepath.Join(t.TempDir(), "controller")
	const callers = 8
	var commands [callers]*exec.Cmd
	var outputs [callers]bytes.Buffer
	var paths [callers]string
	for i := range commands {
		paths[i] = filepath.Join(t.TempDir(), "result")
		cmd := exec.Command(os.Args[0], "-test.run=^TestInstallationIDSubprocess$", "-test.timeout=20s")
		cmd.Env = append(os.Environ(), idRootEnv+"="+root, idOutputEnv+"="+paths[i])
		cmd.Stdout, cmd.Stderr = &outputs[i], &outputs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands[i] = cmd
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	}
	want := ""
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("installation ID child %d: %v: %s", i, err, outputs[i].String())
		}
		data, err := os.ReadFile(paths[i])
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			want = string(data)
		} else if string(data) != want {
			t.Fatalf("separate processes created different installation IDs")
		}
	}
	if current, err := InstallationID(root); err != nil || current != want {
		t.Fatalf("published installation ID differs from child processes: %v", err)
	}
}

func TestInstallationIDSubprocess(t *testing.T) {
	root, output := os.Getenv(idRootEnv), os.Getenv(idOutputEnv)
	if root == "" || output == "" {
		t.Skip("child-only")
	}
	id, err := InstallationID(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte(id), 0o600); err != nil {
		t.Fatal(err)
	}
}

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
	otherID := uuid.NewString()
	if otherID == id {
		t.Fatal("fixture installation IDs collided")
	}
	if _, err := Call(context.Background(), root, otherID, "stop"); err == nil {
		t.Fatalf("different installation could stop controller: %v", err)
	}
	conn, err := localipc.Dial(root, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	incompatible, err := handshake.New(handshake.Desktop, id)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	incompatible.ProtocolMajor++
	payload, err := json.Marshal(incompatible)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := wire.WriteFrame(conn, payload); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if _, err := wire.ReadFrame(conn); err == nil {
		_ = conn.Close()
		t.Fatal("incompatible protocol received a response")
	}
	_ = conn.Close()
	if status, err := Call(context.Background(), root, id, "status"); err != nil || status.State != "running" {
		t.Fatalf("rejected peers disrupted controller: %+v, %v", status, err)
	}
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
