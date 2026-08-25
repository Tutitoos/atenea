//go:build unix

package procgroup

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestContainStopsAContextChildAndSetsAWaitDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sleep", "30")
	Contain(cmd)
	if cmd.WaitDelay != Grace {
		t.Fatalf("WaitDelay = %s, want %s", cmd.WaitDelay, Grace)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("Contain did not isolate the process group")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("canceled child exited successfully")
	}
}

// Isolate's caller drives the whole lifecycle itself, so nothing cancels this
// Cmd -- but Wait still hangs on the output pipes, and a child that leaves a
// helper behind keeps the write end open after it is gone. Here the shell
// exits immediately and the sleep it started inherits stderr, which is a
// bytes.Buffer rather than a file, so os/exec copies it through a pipe and
// Wait cannot see EOF for as long as the helper lives. Without WaitDelay that
// is five seconds for this test and the lifetime of a leaked language server
// in internal/supervisor, where it blocked the whole shutdown.
func TestIsolateStopsWaitingOnPipesAHelperLeftOpen(t *testing.T) {
	var output bytes.Buffer
	cmd := exec.Command("sh", "-c", "sleep 5 & exit 0")
	cmd.Stderr = &output
	Isolate(cmd)
	if cmd.WaitDelay != Grace {
		t.Fatalf("WaitDelay = %s, want %s", cmd.WaitDelay, Grace)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = cmd.Wait()
	if took := time.Since(start); took > Grace+2*time.Second {
		t.Fatalf("Wait took %s: it is still waiting on a pipe the helper holds", took)
	}
}

func TestTerminateAndKillReachAnIsolatedChildGroup(t *testing.T) {
	for _, action := range []struct {
		name string
		stop func(*exec.Cmd) error
	}{
		{name: "terminate", stop: Terminate},
		{name: "kill", stop: Kill},
	} {
		t.Run(action.name, func(t *testing.T) {
			cmd := exec.Command("sleep", "30")
			Isolate(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if err := action.stop(cmd); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("stopped child exited successfully")
			}
		})
	}
	if err := Terminate(&exec.Cmd{}); err != nil {
		t.Fatalf("Terminate on an unstarted command: %v", err)
	}
	if err := Kill(&exec.Cmd{}); err != nil {
		t.Fatalf("Kill on an unstarted command: %v", err)
	}
}
