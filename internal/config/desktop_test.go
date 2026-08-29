package config

import (
	"testing"
	"time"
)

func TestDesktopProfilesAndResolve(t *testing.T) {
	profiles, err := validateDesktopProfiles(defaultDesktopProfiles(), []fileDesktopProfile{{
		Name: "claude", Clients: []string{"claude"}, MCPMode: "atenea_only",
		StartupTimeout: "2s", ToolTimeout: "3s", Fallback: "diagnostic",
		ClientFlags: []string{"--strict-mcp-config"},
	}})
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

func TestDefaultsLoadDesktopProfilesFromTheMainDecoder(t *testing.T) {
	cfg, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DesktopProfiles) != 3 {
		t.Fatalf("profiles = %d, want three presets", len(cfg.DesktopProfiles))
	}
	if cfg.DesktopProfiles[0].StartupTimeout != 10*time.Second {
		t.Fatalf("startup timeout = %s", cfg.DesktopProfiles[0].StartupTimeout)
	}
}

func TestDesktopProfilesRejectDuplicateOverrides(t *testing.T) {
	_, err := validateDesktopProfiles(defaultDesktopProfiles(), []fileDesktopProfile{
		{Name: "claude", MCPMode: "atenea_only", Fallback: "diagnostic"},
		{Name: "claude", MCPMode: "atenea_only", Fallback: "diagnostic"},
	})
	if err == nil {
		t.Fatal("duplicate profile was accepted")
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
