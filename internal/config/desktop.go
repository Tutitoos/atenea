package config

import (
	"fmt"
	"strings"
	"time"
)

// DesktopProfile describes the policy used when Atenea is consumed by a
// desktop agent. The profile is intentionally independent from the client
// configuration format so it can be rendered as JSON or TOML.
type DesktopProfile struct {
	Name           string
	Clients        []string
	MCPMode        string
	DirectMCP      []string
	EnabledTools   []string
	DisabledTools  []string
	StartupTimeout time.Duration
	ToolTimeout    time.Duration
	Fallback       string
	ClientFlags    []string
}

type fileDesktopProfile struct {
	Name           string
	Clients        []string
	MCPMode        string   `toml:"mcp_mode"`
	DirectMCP      []string `toml:"direct_mcp"`
	EnabledTools   []string `toml:"enabled_tools"`
	DisabledTools  []string `toml:"disabled_tools"`
	StartupTimeout string   `toml:"startup_timeout"`
	ToolTimeout    string   `toml:"tool_timeout"`
	Fallback       string
	ClientFlags    []string `toml:"client_flags"`
}

func (f fileDesktopProfile) build() (DesktopProfile, error) {
	startup, err := parseDesktopDuration(f.StartupTimeout, 10*time.Second)
	if err != nil {
		return DesktopProfile{}, fmt.Errorf("desktop_profile %q startup_timeout: %w", f.Name, err)
	}
	tool, err := parseDesktopDuration(f.ToolTimeout, 60*time.Second)
	if err != nil {
		return DesktopProfile{}, fmt.Errorf("desktop_profile %q tool_timeout: %w", f.Name, err)
	}
	return DesktopProfile{
		Name: f.Name, Clients: append([]string(nil), f.Clients...),
		MCPMode: f.MCPMode, DirectMCP: append([]string(nil), f.DirectMCP...),
		EnabledTools:   append([]string(nil), f.EnabledTools...),
		DisabledTools:  append([]string(nil), f.DisabledTools...),
		StartupTimeout: startup, ToolTimeout: tool, Fallback: f.Fallback,
		ClientFlags: append([]string(nil), f.ClientFlags...),
	}, nil
}

func parseDesktopDuration(raw string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		if err == nil {
			err = fmt.Errorf("must be positive")
		}
		return 0, err
	}
	return d, nil
}

func defaultDesktopProfiles() []fileDesktopProfile {
	return []fileDesktopProfile{
		{Name: "claude", Clients: []string{"claude"}, MCPMode: "atenea_only", Fallback: "diagnostic", ClientFlags: []string{"--strict-mcp-config"}},
		{Name: "chatgpt", Clients: []string{"chatgpt", "codex"}, MCPMode: "atenea_only", Fallback: "diagnostic"},
		{Name: "shared", Clients: []string{"claude", "chatgpt", "codex", "opencode", "omp"}, MCPMode: "hybrid", DirectMCP: []string{"*"}, Fallback: "diagnostic"},
	}
}

func validateDesktopProfiles(defaults, overrides []fileDesktopProfile) ([]DesktopProfile, error) {
	merged := make(map[string]fileDesktopProfile, len(defaults)+len(overrides))
	order := make([]string, 0, len(defaults)+len(overrides))
	for _, p := range defaults {
		merged[p.Name] = p
		order = append(order, p.Name)
	}
	seenOverrides := make(map[string]bool, len(overrides))
	for _, p := range overrides {
		if p.Name == "" {
			return nil, fmt.Errorf("desktop_profile requires name")
		}
		if seenOverrides[p.Name] {
			return nil, fmt.Errorf("desktop_profile %q is declared twice", p.Name)
		}
		seenOverrides[p.Name] = true
		if _, exists := merged[p.Name]; !exists {
			order = append(order, p.Name)
		}
		merged[p.Name] = p
	}
	profiles := make([]DesktopProfile, 0, len(order))
	for _, name := range order {
		p := merged[name]
		built, err := p.build()
		if err != nil {
			return nil, err
		}
		if built.MCPMode != "atenea_only" && built.MCPMode != "hybrid" {
			return nil, fmt.Errorf("desktop_profile %q: mcp_mode must be atenea_only or hybrid", name)
		}
		if built.Fallback != "diagnostic" && built.Fallback != "none" {
			return nil, fmt.Errorf("desktop_profile %q: fallback must be diagnostic or none", name)
		}
		if len(built.DirectMCP) > 1 && containsDesktop(built.DirectMCP, "*") {
			return nil, fmt.Errorf("desktop_profile %q: direct_mcp wildcard cannot be combined", name)
		}
		profiles = append(profiles, built)
	}
	return profiles, nil
}

func containsDesktop(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// ResolveDesktopProfile returns the named profile, or the built-in profile
// selected for a client when name is empty.
func ResolveDesktopProfile(profiles []DesktopProfile, name, client string) (DesktopProfile, error) {
	if name == "" {
		switch client {
		case "claude":
			name = "claude"
		case "chatgpt", "codex":
			name = "chatgpt"
		default:
			name = "shared"
		}
	}
	for _, p := range profiles {
		if p.Name != name {
			continue
		}
		if client != "" && len(p.Clients) > 0 && !containsDesktop(p.Clients, client) && !containsDesktop(p.Clients, "*") {
			return DesktopProfile{}, fmt.Errorf("desktop_profile %q is not compatible with client %q", name, client)
		}
		return p, nil
	}
	return DesktopProfile{}, fmt.Errorf("desktop_profile %q not found", name)
}

// ValidateDesktopProfiles checks references that require the complete Atenea
// MCP catalog. It is called by commands after the main settings file has been
// decoded, before any client process or backend is started.
func ValidateDesktopProfiles(profiles []DesktopProfile, servers []MCPServer) error {
	validClients := map[string]bool{"claude": true, "chatgpt": true, "codex": true, "opencode": true, "omp": true}
	serverByID := make(map[string]MCPServer, len(servers))
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	for _, profile := range profiles {
		for _, client := range profile.Clients {
			if client != "*" && !validClients[client] {
				return fmt.Errorf("desktop_profile %q: unknown client %q", profile.Name, client)
			}
		}
		for _, id := range profile.DirectMCP {
			if id == "*" {
				continue
			}
			server, ok := serverByID[id]
			if !ok {
				return fmt.Errorf("desktop_profile %q: direct_mcp references unknown MCP %q", profile.Name, id)
			}
			if server.Expose != "on" {
				return fmt.Errorf("desktop_profile %q: direct_mcp %q must use expose = \"on\"", profile.Name, id)
			}
		}
	}
	return nil
}

// FilterDesktopMCPServers returns the servers a client may connect to
// directly. Atenea itself is always added separately by the wrapper.
func FilterDesktopMCPServers(servers []MCPServer, profile DesktopProfile) []MCPServer {
	if profile.MCPMode != "hybrid" {
		return nil
	}
	all := containsDesktop(profile.DirectMCP, "*")
	filtered := make([]MCPServer, 0, len(servers))
	for _, server := range servers {
		if server.Expose != "on" || (!all && !containsDesktop(profile.DirectMCP, server.ID)) {
			continue
		}
		filtered = append(filtered, server)
	}
	return filtered
}
