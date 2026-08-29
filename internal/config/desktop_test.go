package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopProfilesFromFileAndResolve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atenea.toml")
	contents := `[[desktop_profile]]
name = "claude"
clients = ["claude"]
mcp_mode = "atenea_only"
startup_timeout = "2s"
tool_timeout = "3s"
fallback = "diagnostic"
client_flags = ["--strict-mcp-config"]
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := DesktopProfilesFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := ResolveDesktopProfile(profiles, "", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if profile.StartupTimeout.Seconds() != 2 || profile.ToolTimeout.Seconds() != 3 {
		t.Fatalf("unexpected timeouts: startup=%s tool=%s", profile.StartupTimeout, profile.ToolTimeout)
	}
}

func TestFilterDesktopMCPServersNeverExposesRaw(t *testing.T) {
	servers := []MCPServer{
		{ID: "declared", Expose: "on"},
		{ID: "raw", Expose: "raw"},
	}
	profile := DesktopProfile{Name: "shared", MCPMode: "hybrid", DirectMCP: []string{"*"}}
	filtered := FilterDesktopMCPServers(servers, profile)
	if len(filtered) != 1 || filtered[0].ID != "declared" {
		t.Fatalf("unexpected direct servers: %#v", filtered)
	}
}
