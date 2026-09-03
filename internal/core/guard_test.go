package core_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// managedCatalog is the fixture for the guard/supervisor wiring tests: one
// symbol capability answered by a Kivgraph Atenea is told to launch and watch
// itself, pointed at a command that cannot exist on any machine.
// restart_limit = 0 means the first failed spawn goes straight to down, with
// no retry delay to wait out -- fast and deterministic, the same shape a
// real crash-looping install would produce without the real wait to prove it.
const managedCatalog = `
contract = "4.0.0"

[core]
shutdown_grace = "2s"

[orchestrator]
runners = ["kivgraph"]

  [orchestrator.kivgraph]
  implementations = ["kivgraph.definition"]
  timeout = "5s"

  [orchestrator.kivgraph.process]
  command = "/nonexistent/atenea-test-kivgraph-binary"
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
id = "kivgraph.definition"
provider = "kivgraph"
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

// onDisk points the fixture's repository at a directory that is really there.
//
// The seam refuses a step whose repository path is missing before any process
// is warmed for it (grounded), which is the right order in production and the
// wrong one for these tests: they are about what happens *behind* that gate --
// a managed process that cannot spawn -- so the path has to be real for the
// call to reach the guard at all.
func onDisk(t *testing.T, body string) string {
	t.Helper()
	return strings.Replace(body, `path = "/srv/api"`, `path = "`+t.TempDir()+`"`, 1)
}

// A managed Kivgraph that can never spawn must fail the call at the guard, not
// at the adapter -- EnsureReady is the gate, and the adapter's own dial is
// never supposed to be reached behind a process Atenea itself could not
// start. Ask does not return the step's error as its own: a runner failure
// is recorded on the step and reviewed, the same as any other capability
// outcome, so the assertion belongs on result.Steps, not on err.
func TestManagedKivgraphGuardsDispatchUntilReady(t *testing.T) {
	atenea := build(t, onDisk(t, managedCatalog))

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
	// from anything the kivgraph adapter itself would say about a connection
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
	atenea := buildService(t, body)
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

// The shared policy is what a file that says nothing gets, and it must keep
// producing exactly one server for every repository on the machine. This is
// shared-provider policy remains independent of the number of repositories.
func TestASharedDeclarationStaysOneProcess(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	body := onDisk(t, managedCatalog)
	body += "\n[[repository]]\nid = \"web\"\npath = \"" + t.TempDir() + "\"\nlanguages = [\"go\"]\nscale = \"small\"\n"

	atenea := build(t, body)
	var ids []string
	for _, p := range atenea.Status().Processes {
		ids = append(ids, p.ID)
	}
	if want := []string{"kivgraph"}; !slices.Equal(ids, want) {
		t.Fatalf("processes = %v, want %v", ids, want)
	}
}
