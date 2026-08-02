package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// catalog is the end-to-end fixture: one capability, three providers with
// genuinely different constraints, and two repositories that pull the funnel in
// opposite directions.
const catalog = `
contract = "1.0.0"

[core]
shutdown_grace = "2s"

# The stand-in reaches every provider in this catalog, so the overall light
# answers the question this fixture is about -- health -- and not a separate
# one about who is plugged in.
[orchestrator]

  [orchestrator.local]
  implementations = ["ripgrep", "serena.search", "graph.search"]

[[capability]]
id = "code.search"
version = "1.0.0"
summary = "Find literal text in a repository."
effects = ["read"]

  [[capability.input]]
  name = "query"
  type = "string"
  required = true

[[implementation]]
id = "ripgrep"
provider = "ripgrep"
capability = "code.search"

  [implementation.cost]
  estimated_duration = "80ms"
  estimated_tokens = 400

  [implementation.health]
  state = "alive"
  score = 0.9

[[implementation]]
id = "serena.search"
provider = "serena"
capability = "code.search"

  [implementation.constraints]
  languages = ["go", "typescript"]
  requires_index = true

  [implementation.health]
  state = "alive"
  score = 1.0

[[implementation]]
id = "graph.search"
provider = "codebase-memory"
capability = "code.search"

  [implementation.constraints]
  requires_index = true
  min_scale = "large"

[[repository]]
id = "api"
path = "/srv/api"
languages = ["go"]
scale = "small"
indexed_by = ["serena"]

[[repository]]
id = "scripts"
path = "/srv/scripts"
languages = ["bash"]
scale = "small"
`

func build(t *testing.T, body string) *core.Core {
	t.Helper()
	path := writeTemp(t, body)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	atenea, err := core.New(cfg)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	return atenea
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// The whole point of the first brick: register a capability, and have the
// funnel pick a provider from constraints and health alone.
func TestEndToEndSelectionFollowsTheFunnel(t *testing.T) {
	atenea := build(t, catalog)

	// On the Go repository with a warm Serena index, both Serena and ripgrep
	// fit, and Serena wins on health score.
	decision, err := atenea.Select("code.search", "api")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "serena.search" {
		t.Fatalf("chosen = %s, want serena.search", decision.Chosen.ID)
	}

	// On the Bash repository Serena does not speak the language and the graph
	// has neither the index nor the size, so ripgrep is the only survivor.
	decision, err = atenea.Select("code.search", "scripts")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "ripgrep" {
		t.Fatalf("chosen = %s, want ripgrep", decision.Chosen.ID)
	}
}

// Health is the block that moves while Atenea runs. A provider going down has
// to change the answer without touching the settings file.
func TestSelectionFollowsHealthAtRuntime(t *testing.T) {
	atenea := build(t, catalog)

	before, err := atenea.Select("code.search", "api")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if before.Chosen.ID != "serena.search" {
		t.Fatalf("chosen = %s", before.Chosen.ID)
	}

	err = atenea.Registry().SetHealth("serena.search", contract.Health{
		State:  contract.HealthDown,
		Reason: "container exited",
	})
	if err != nil {
		t.Fatalf("SetHealth: %v", err)
	}

	after, err := atenea.Select("code.search", "api")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if after.Chosen.ID != "ripgrep" {
		t.Fatalf("chosen = %s, want the fallback", after.Chosen.ID)
	}
	if !strings.Contains(traceReason(after.Stages, "health", "serena.search"), "container exited") {
		t.Errorf("the trace does not explain the drop: %+v", after.Stages)
	}
}

// traceReason digs the explanation for one drop out of the funnel trace.
func traceReason(stages []selector.Stage, stageName, implementation string) string {
	for _, stage := range stages {
		if stage.Name != stageName {
			continue
		}
		for _, dropped := range stage.Dropped {
			if dropped.Implementation == implementation {
				return dropped.Reason
			}
		}
	}
	return ""
}

func TestUnknownCapabilityAndRepositoryAreNotFound(t *testing.T) {
	atenea := build(t, catalog)
	if _, err := atenea.Select("code.impact", "api"); contract.KindOf(err) != contract.FailureNotFound {
		t.Errorf("unknown capability: kind = %v", contract.KindOf(err))
	}
	if _, err := atenea.Select("code.search", "web"); contract.KindOf(err) != contract.FailureNotFound {
		t.Errorf("unknown repository: kind = %v", contract.KindOf(err))
	}
}

// A rule that quietly matches nothing is a preference the user believes is in
// force and is not, so the core refuses to boot with one.
func TestBrokenRulesStopTheBoot(t *testing.T) {
	cases := map[string]string{
		"unknown capability": `
[[selector.rule]]
capability = "code.impact"
prefer = "ripgrep"
`,
		"unknown implementation": `
[[selector.rule]]
capability = "code.search"
prefer = "grep"
`,
		"implementation of another capability": `
[[capability]]
id = "code.impact"
version = "1.0.0"
summary = "Estimate the blast radius of a change."
effects = ["read"]

[[selector.rule]]
capability = "code.impact"
prefer = "ripgrep"
`,
		"unknown repository": `
[[selector.rule]]
capability = "code.search"
repository = "web"
prefer = "ripgrep"
`,
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeTemp(t, catalog+extra)
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("config.Load: %v", err)
			}
			if _, err := core.New(cfg); err == nil {
				t.Fatal("expected the boot to fail")
			}
		})
	}
}

func TestValidRuleBoots(t *testing.T) {
	atenea := build(t, catalog+`
[[selector.rule]]
capability = "code.search"
repository = "api"
prefer = "ripgrep"
`)
	decision, err := atenea.Select("code.search", "api")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "ripgrep" {
		t.Fatalf("chosen = %s, want the rule to win over the healthier provider", decision.Chosen.ID)
	}
}

// A clean stop refuses new work and waits a bounded margin for what is already
// running. Cutting a writer off mid-flight can leave files half written.
func TestShutdownRefusesNewWorkAndWaitsForInFlight(t *testing.T) {
	atenea := build(t, catalog)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 200 {
			if _, err := atenea.Select("code.search", "api"); err != nil {
				// Once the stop begins, refusal is the expected answer.
				if contract.KindOf(err) != contract.FailureUnavailable {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
		}
	}()

	if err := atenea.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	wg.Wait()

	if !atenea.Stopping() {
		t.Error("Stopping() should report the stop")
	}
	if _, err := atenea.Select("code.search", "api"); contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("after shutdown: kind = %v, want unavailable", contract.KindOf(err))
	}
	// Stopping twice is not an error: a signal and a caller may race.
	if err := atenea.Shutdown(); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

func TestRunStopsWhenTheContextIsCancelled(t *testing.T) {
	atenea := build(t, catalog)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- atenea.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the context was canceled")
	}
}

func TestStatusReportsTheWholeCatalogue(t *testing.T) {
	atenea := build(t, catalog)
	status := atenea.Status()

	if status.Settings == "" || status.Version == "" {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(status.Funnel, "cost") {
		t.Errorf("the status must say out loud that cost is not wired yet: %q", status.Funnel)
	}
	if len(status.Capabilities) != 1 || len(status.Capabilities[0].Implementations) != 3 {
		t.Fatalf("capabilities = %+v", status.Capabilities)
	}
	// graph.search is unprobed, so the overall light cannot be green.
	if status.Light != core.LightAmber {
		t.Errorf("light = %s, want amber", status.Light)
	}

	if err := atenea.Registry().SetHealth("graph.search", contract.Health{State: contract.HealthAlive}); err != nil {
		t.Fatalf("SetHealth: %v", err)
	}
	if got := atenea.Status().Light; got != core.LightGreen {
		t.Errorf("light = %s, want green once everything is alive", got)
	}

	for _, id := range []string{"ripgrep", "serena.search", "graph.search"} {
		if err := atenea.Registry().SetHealth(id, contract.Health{State: contract.HealthDown}); err != nil {
			t.Fatalf("SetHealth: %v", err)
		}
	}
	// A capability nobody can answer is red, not amber.
	if got := atenea.Status().Light; got != core.LightRed {
		t.Errorf("light = %s, want red when nothing can answer", got)
	}
}
