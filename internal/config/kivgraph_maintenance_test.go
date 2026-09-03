package config_test

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

func TestKivgraphIndexEnvironmentIsExplicit(t *testing.T) {
	cfg, err := config.Load(write(t, minimal+"\n[orchestrator.kivgraph]\nendpoint = \"http://127.0.0.1:7788/mcp\"\nindex_env = [\"PATH=/fixture/bin\"]\nauto_reindex_registered = true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Orchestrator.Kivgraph.AutoReindexRegistered || len(cfg.Orchestrator.Kivgraph.IndexEnv) != 1 || cfg.Orchestrator.Kivgraph.IndexEnv[0] != "PATH=/fixture/bin" {
		t.Fatal("maintenance configuration lost")
	}
	_, err = config.Load(write(t, minimal+"\n[orchestrator.kivgraph]\nindex_env = [\"PATH\"]\n"))
	if err == nil {
		t.Fatal("invalid environment accepted")
	}
}
