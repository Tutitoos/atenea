package core_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/orchestrator"
)

// An unmanaged catalog has nothing for the status screen to say about
// processes. The section is the newest one on this screen, and it has to
// stay out of the way on the far more common setup that never opted into
// supervision -- the same restraint every other optional section here
// already keeps for a fresh or ordinary install.
func TestStatusReportsNoProcessesWhenNothingIsManaged(t *testing.T) {
	atenea := build(t, catalog)
	if got := atenea.Status().Processes; len(got) != 0 {
		t.Errorf("processes = %+v, want none", got)
	}
}

// A managed process that cannot spawn has to reach StateDown and say so on
// the status screen -- amber, a restart count, and the reason -- once
// something has actually asked for it. Before that it must not invent a
// problem: on_demand idle is green, the same restraint BackupStatus.stale
// applies to a fresh install with no copy yet.
func TestStatusReportsAnOnDemandProcessBeforeAndAfterItGoesDown(t *testing.T) {
	atenea := build(t, managedCatalog)

	before := atenea.Status().Processes
	if len(before) != 1 {
		t.Fatalf("processes = %+v, want exactly one entry", before)
	}
	if before[0].ID != "serena" || before[0].State != "stopped" || before[0].Light != core.LightGreen {
		t.Errorf("idle process = %+v, want serena/stopped/green", before[0])
	}

	// One dispatch is enough to force the guard to try, fail, and mark the
	// process down for good (restart_limit = 0 in the fixture).
	if _, err := atenea.Ask(context.Background(), orchestrator.Question{
		Capability: "symbol.definition",
		Repository: "api",
		Payload:    map[string]any{"file": "main.go"},
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	after := atenea.Status().Processes
	if len(after) != 1 {
		t.Fatalf("processes = %+v, want exactly one entry", after)
	}
	p := after[0]
	if p.State != "down" || p.Light != core.LightAmber {
		t.Errorf("down process = %+v, want down/amber", p)
	}
	if p.Restarts != 1 {
		t.Errorf("restarts = %d, want 1 (the one attempt that failed)", p.Restarts)
	}
	if p.LastReason == "" {
		t.Error("LastReason is empty for a process that failed to start")
	}
}

// A persistent process warmed up by Run reaches the same StateDown on its
// own, with nothing ever dispatched -- and unlike the on_demand case above,
// nothing here touches capability health, so the overall light can only have
// moved for one reason: the process light actually reaches it, which is the
// point of wiring Processes into Status at all rather than leaving it a
// footnote nobody rolls up.
func TestStatusOverallLightFollowsAWarmedUpProcessGoingDown(t *testing.T) {
	body := strings.Replace(managedCatalog, `lifecycle = "on_demand"`, `lifecycle = "persistent"`, 1)
	atenea := build(t, body)

	if got := atenea.Status().Light; got != core.LightGreen {
		t.Fatalf("light before warm-up = %v, want green", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- atenea.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if procs := atenea.Status().Processes; len(procs) == 1 && procs[0].State == "down" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	status := atenea.Status()
	if len(status.Processes) != 1 || status.Processes[0].State != "down" {
		t.Fatalf("processes = %+v, want the one entry down within the deadline", status.Processes)
	}
	if status.Light != core.LightAmber {
		t.Errorf("overall light = %v, want amber once the warmed-up process is down", status.Light)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was canceled")
	}
}
