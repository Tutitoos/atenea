package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestStatusAttributesRedAndKeepsObservationStatesDistinct(t *testing.T) {
	atenea := build(t, catalog)
	unknown := atenea.Status()
	if unknown.Light != core.LightAmber || len(unknown.RedCauses) != 0 {
		t.Fatalf("unprobed status = %s causes=%+v, want amber without red causes", unknown.Light, unknown.RedCauses)
	}
	var graph core.ImplementationStatus
	for _, impl := range unknown.Capabilities[0].Implementations {
		if impl.ID == "graph.search" {
			graph = impl
		}
	}
	if graph.Health.State != contract.HealthUnknown || graph.State != "unknown" ||
		graph.LastChecked != nil || !graph.Health.ObservedAt.IsZero() || graph.HealthExpired {
		t.Fatalf("never checked implementation = %+v", graph)
	}

	fresh := time.Now().UTC().Add(-time.Minute)
	for _, id := range []string{"ripgrep", "fixture.search", "graph.search"} {
		if err := atenea.Registry().SetHealth("api", id, contract.Health{
			State: contract.HealthDown, Reason: "api_key=private-value provider unavailable", Raw: "Bearer private-value", ObservedAt: fresh,
		}); err != nil {
			t.Fatal(err)
		}
	}
	down := atenea.Status()
	if down.Light != core.LightRed || len(down.RedCauses) != 3 {
		t.Fatalf("down status = %s causes=%+v", down.Light, down.RedCauses)
	}
	for _, cause := range down.RedCauses {
		if cause.Capability != "code.search" || cause.Repository != "api" || cause.State != "down" ||
			cause.ObservedAt == nil || !cause.ObservedAt.Equal(fresh) {
			t.Errorf("current down cause = %+v", cause)
		}
		if strings.Contains(cause.Reason, "private-value") || strings.Contains(cause.Reason, "api_key") ||
			cause.Reason != "A runtime observation reports this implementation unavailable." {
			t.Errorf("cause reason is not controlled: %q", cause.Reason)
		}
	}
	for _, impl := range down.Capabilities[0].Implementations {
		if impl.State != "down" || impl.LastChecked == nil || !impl.LastChecked.Equal(fresh) {
			t.Errorf("current implementation freshness = %+v", impl)
		}
	}
	encoded, err := json.Marshal(down)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-value") {
		t.Fatalf("status JSON exposed provider diagnostic credential: %s", encoded)
	}

	old := time.Now().UTC().Add(-48 * time.Hour)
	if err := atenea.Registry().SetHealth("api", "graph.search", contract.Health{
		State: contract.HealthDown, Reason: "old failure", ObservedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	expired := atenea.Status()
	if expired.Light == core.LightRed || len(expired.RedCauses) != 0 {
		t.Fatalf("expired observation still raised red: %s %+v", expired.Light, expired.RedCauses)
	}
	for _, impl := range expired.Capabilities[0].Implementations {
		if impl.ID == "graph.search" && (!impl.HealthExpired || impl.State != "unknown" || impl.LastChecked == nil || !impl.LastChecked.Equal(old)) {
			t.Errorf("expired implementation = %+v", impl)
		}
	}
}

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
	atenea := build(t, onDisk(t, managedCatalog))

	before := atenea.Status().Processes
	if len(before) != 1 {
		t.Fatalf("processes = %+v, want exactly one entry", before)
	}
	if before[0].ID != "kivgraph" || before[0].State != "stopped" || before[0].Light != core.LightGreen {
		t.Errorf("idle process = %+v, want kivgraph/stopped/green", before[0])
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
	atenea := buildService(t, body)

	if got := atenea.Status().Light; got != core.LightGreen {
		t.Fatalf("light before warm-up = %v, want green", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- atenea.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// Run's own failure is checked on every turn, not only at the end.
		// It was read once, after the loop, and that ordering hid the error
		// that actually happens: Run refusing to start -- a socket path over
		// the sun_path limit, an upkeep claim already held -- spent the full
		// three seconds polling a service that was never there and then
		// failed with a sentence about process lights, while the real reason
		// sat unread in a channel until the deferred cancel threw it away.
		select {
		case err := <-done:
			t.Fatalf("the service stopped instead of warming up: %v", err)
		default:
		}
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
