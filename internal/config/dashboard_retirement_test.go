package config_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

// The provider dashboard command is gone (#235). A settings file whose
// [[mcp_server]] still carries a dashboard key is refused naming the server and
// the edit, not accepted and not the bare "unknown key" a typo gets.
func TestRemovedDashboardKeyIsRefusedWithItsRemedy(t *testing.T) {
	for name, body := range map[string]string{
		"url server":     "\n[[mcp_server]]\nid = \"fixture\"\nurl = \"http://127.0.0.1:1/mcp\"\ndashboard = \"http://127.0.0.1:1/ui\"\n",
		"command server": "\n[[mcp_server]]\nid = \"fixture\"\ncommand = [\"sh\"]\ndashboard = \"http://127.0.0.1:1\"\n",
		"empty value":    "\n[[mcp_server]]\nid = \"fixture\"\ncommand = [\"sh\"]\ndashboard = \"\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(write(t, minimal+body))
			refusedWith(t, err, `mcp_server "fixture" key dashboard was removed in contract 5.0.0 with the provider dashboard command; delete the key`)
		})
	}
}

// Every server that carries the key is named, in one refusal.
func TestEveryServerWithARemovedDashboardKeyIsNamed(t *testing.T) {
	body := minimal +
		"\n[[mcp_server]]\nid = \"one\"\ncommand = [\"sh\"]\ndashboard = \"http://127.0.0.1:1\"\n" +
		"\n[[mcp_server]]\nid = \"two\"\ncommand = [\"sh\"]\n" +
		"\n[[mcp_server]]\nid = \"three\"\ncommand = [\"sh\"]\ndashboard = \"http://127.0.0.1:2\"\n"
	_, err := config.Load(write(t, body))
	if err == nil {
		t.Fatal("file accepted")
	}
	for _, want := range []string{`mcp_server "one" key dashboard`, `mcp_server "three" key dashboard`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), `"two"`) {
		t.Fatalf("error %q names a server without the key", err)
	}
}

// Servers without the key still load, and the shipped defaults declare none.
func TestAServerWithoutADashboardKeyStillLoads(t *testing.T) {
	cfg, err := config.Load(write(t, minimal+"\n[[mcp_server]]\nid = \"fixture\"\ncommand = [\"sh\"]\n"))
	if err != nil || len(cfg.MCPServers) != 1 {
		t.Fatalf("load = %+v, %v", cfg.MCPServers, err)
	}
	if _, err := config.Defaults(); err != nil {
		t.Fatal(err)
	}
}
