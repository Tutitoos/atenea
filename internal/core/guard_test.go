package core_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
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

// The endpoint key and a process table cannot both decide where the adapter
// dials: one is an address a user picked, the other does not exist until
// something spawns. Declaring a process settles it, and settles it harder
// than "ignored" suggests -- the value is never read, so it is never
// validated either. An endpoint that is not a URL at all is refused outright
// on its own and passes without comment beside a process table, which is
// worth pinning in both directions: it means deleting a process block can
// turn a file that always loaded into one that suddenly does not.
func TestAManagedProcessTakesOverTheWrittenEndpoint(t *testing.T) {
	// Not a URL. The adapter refuses this shape, so a core that builds
	// against it is a core that never handed it to the adapter.
	managed := strings.Replace(managedCatalog,
		`endpoint = "http://127.0.0.1:1/mcp"`, `endpoint = "localhost:9121"`, 1)

	atenea := build(t, managed)
	status := atenea.Status()
	if len(status.Processes) != 1 {
		t.Fatalf("processes = %d, want the one the file declared", len(status.Processes))
	}
	// The supervisor owns the port. The host and the MCP path are still what
	// the adapter has to be able to dial.
	got := status.Processes[0].Endpoint
	if !strings.HasPrefix(got, "http://127.0.0.1:") || !strings.HasSuffix(got, "/mcp") {
		t.Errorf("endpoint = %q, want the supervisor's own URL", got)
	}

	const processTable = `  [orchestrator.serena.process]
  command = "/nonexistent/atenea-test-serena-binary"
  lifecycle = "on_demand"
  restart_limit = 0
`
	unmanaged := strings.Replace(managed, processTable, "", 1)
	if unmanaged == managed {
		t.Fatal("fixture drifted: the process table is not where this test expects it")
	}
	// Nothing to take the endpoint over now, so it has to be read -- and
	// reading it is the step that fails.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg, err := config.Load(writeTemp(t, unmanaged))
	if err == nil {
		_, err = core.New(cfg)
	}
	if err == nil {
		t.Fatal("an endpoint that is not a URL was accepted with nothing to take it over")
	}
	if kind := contract.KindOf(err); kind != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want invalid_input; err = %v", kind, err)
	}
}
