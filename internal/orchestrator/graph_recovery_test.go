package orchestrator

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestGraphMaintenanceReopensOnlyRuntimeGraphFailures(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Repositories = []contract.Repository{{ID: "fixture", Path: t.TempDir()}}
	reg := registry.New()
	for _, cap := range cfg.Capabilities {
		if err := reg.AddCapability(cap); err != nil {
			t.Fatal(err)
		}
	}
	for _, impl := range cfg.Implementations {
		if err := reg.AddImplementation(impl); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.AddRepository(cfg.Repositories[0]); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"kivgraph.search", "ripgrep"} {
		if err := reg.SetHealth("fixture", id, contract.Health{State: contract.HealthDown}); err != nil {
			t.Fatal(err)
		}
	}
	a := &Agent{catalog: reg}
	a.reopenGraphQueries("fixture")
	for id, want := range map[string]contract.HealthState{"kivgraph.search": contract.HealthUnknown, "ripgrep": contract.HealthDown} {
		impl, err := reg.Implementation(id)
		if err != nil {
			t.Fatal(err)
		}
		if got := reg.Observed("fixture", []contract.Implementation{impl})[0].Health.State; got != want {
			t.Fatalf("%s health %v want %v", id, got, want)
		}
	}
}
