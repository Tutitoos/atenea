//go:build unix

package procgroup

import (
	"context"
	"os/exec"
	"testing"
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
