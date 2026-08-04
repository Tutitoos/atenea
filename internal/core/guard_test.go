package core_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// managedCatalog is the fixture for the guard/supervisor wiring tests: one
// symbol capability answered by a Serena Atenea is told to launch and watch
// itself, pointed at a command that cannot exist on any machine.
// restart_limit = 0 means the first failed spawn goes straight to down, with
// no retry delay to wait out -- fast and deterministic, the same shape a
// real crash-looping install would produce without the real wait to prove it.
const managedCatalog = `
contract = "1.0.0"

[core]
shutdown_grace = "2s"

[orchestrator]
runners = ["serena"]

  [orchestrator.serena]
  endpoint = "http://127.0.0.1:1/mcp"
  implementations = ["serena.definition"]
  timeout = "5s"

  [orchestrator.serena.process]
  command = "/nonexistent/atenea-test-serena-binary"
  lifecycle = "on_demand"
  restart_limit = 0

[[capability]]
id = "symbol.definition"
version = "1.0.0"
summary = "Resolve a definition."
effects = ["read"]

  [[capability.input]]
  name = "file"
  type = "string"
  required = true

  [[capability.output]]
  name = "locations"
  type = "record_list"
  required = true

    [[capability.output.field]]
    name = "path"
    type = "string"
    required = true

[[implementation]]
id = "serena.definition"
provider = "serena"
capability = "symbol.definition"

  [implementation.health]
  state = "alive"
  score = 1.0

[[repository]]
id = "api"
path = "/srv/api"
languages = ["go"]
scale = "small"
`

// A managed Serena that can never spawn must fail the call at the guard, not
// at the adapter -- EnsureReady is the gate, and the adapter's own dial is
// never supposed to be reached behind a process Atenea itself could not
// start. Ask does not return the step's error as its own: a runner failure
// is recorded on the step and reviewed, the same as any other capability
// outcome, so the assertion belongs on result.Steps, not on err.
func TestManagedSerenaGuardsDispatchUntilReady(t *testing.T) {
	atenea := build(t, managedCatalog)

	result, err := atenea.Ask(context.Background(), orchestrator.Question{
		Capability: "symbol.definition",
		Repository: "api",
		Payload:    map[string]any{"file": "main.go"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(result.Steps))
	}
	step := result.Steps[0]
	if step.FailureKind != contract.FailureUnavailable {
		t.Errorf("kind = %v, want unavailable; failure = %q", step.FailureKind, step.Failure)
	}
	// "did not come up" is the guard's own wording (guardFailure), distinct
	// from anything the serena adapter itself would say about a connection
	// it never got to attempt.
	if !strings.Contains(step.Failure, "did not come up") {
		t.Errorf("failure = %q, want the guard's wording, not the adapter's", step.Failure)
	}
}

// Run has to warm and stop a real (if doomed) child process without hanging
// or panicking: WarmUp spawns persistent servers without waiting on them,
// and Shutdown has to join whatever that spawn attempt left behind before it
// can call the batch settled. A managed process that never comes up is the
// case most likely to deadlock that handoff, so it is the one worth proving
// against here rather than trusting the nil-Supervisor path already covered
// by every other Run test in this package.
func TestRunWarmsAndStopsAManagedProcessCleanly(t *testing.T) {
	body := strings.Replace(managedCatalog, `lifecycle = "on_demand"`, `lifecycle = "persistent"`, 1)
	atenea := build(t, body)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- atenea.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was canceled; " +
			"WarmUp or Shutdown likely deadlocked on the managed process")
	}
	// A second Shutdown must still be the no-op it is for an unmanaged core.
	if err := atenea.Shutdown(); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}
