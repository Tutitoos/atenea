package config_test

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

// Headroom support was removed (#234), but "headroom" is not a reserved id: a
// settings file that still declares it is an ordinary MCP server and loads.
func TestAServerNamedHeadroomIsAnOrdinaryServer(t *testing.T) {
	cfg, err := config.Load(write(t, minimal+"\n[[mcp_server]]\nid = \"headroom\"\ncommand = [\"headroom\", \"mcp\", \"serve\"]\n"))
	if err != nil || len(cfg.MCPServers) != 1 || cfg.MCPServers[0].ID != "headroom" {
		t.Fatalf("load = %+v, %v", cfg.MCPServers, err)
	}
}
