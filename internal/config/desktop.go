package config

import (
	"fmt"
	"os"
	"strconv"
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

// DesktopProfilesFromFile reads only the profile blocks. The main config
// decoder still owns validation of the rest of Atenea's TOML document.
func DesktopProfilesFromFile(path string) ([]DesktopProfile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			profiles, buildErr := validateDesktopProfiles(defaultDesktopProfiles(), nil)
			return profiles, buildErr
		}
		return nil, err
	}
	var blocks []fileDesktopProfile
	var current *fileDesktopProfile
	flush := func() error {
		if current == nil {
			return nil
		}
		p, err := current.build()
		if err != nil {
			return err
		}
		blocks = append(blocks, fileDesktopProfileFrom(p))
		current = nil
		return nil
	}
	for _, rawLine := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(strings.SplitN(rawLine, "#", 2)[0])
		if line == "[[desktop_profile]]" {
			if err := flush(); err != nil {
				return nil, err
			}
			current = &fileDesktopProfile{}
			continue
		}
		if current == nil || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch key {
		case "name":
			current.Name = parseDesktopString(value)
		case "clients":
			current.Clients = parseDesktopStrings(value)
		case "mcp_mode":
			current.MCPMode = parseDesktopString(value)
		case "direct_mcp":
			current.DirectMCP = parseDesktopStrings(value)
		case "enabled_tools":
			current.EnabledTools = parseDesktopStrings(value)
		case "disabled_tools":
			current.DisabledTools = parseDesktopStrings(value)
		case "startup_timeout":
			current.StartupTimeout = parseDesktopString(value)
		case "tool_timeout":
			current.ToolTimeout = parseDesktopString(value)
		case "fallback":
			current.Fallback = parseDesktopString(value)
		case "client_flags":
			current.ClientFlags = parseDesktopStrings(value)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return validateDesktopProfiles(defaultDesktopProfiles(), blocks)
}

func parseDesktopString(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && ((raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'')) {
		return strings.ReplaceAll(raw[1:len(raw)-1], `\"`, `"`)
	}
	return raw
}

func parseDesktopStrings(raw string) []string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
		return nil
	}
	var values []string
	for _, part := range strings.Split(raw[1:len(raw)-1], ",") {
		if value := parseDesktopString(strings.TrimSpace(part)); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func fileDesktopProfileFrom(p DesktopProfile) fileDesktopProfile {
	return fileDesktopProfile{
		Name: p.Name, Clients: p.Clients, MCPMode: p.MCPMode, DirectMCP: p.DirectMCP,
		EnabledTools: p.EnabledTools, DisabledTools: p.DisabledTools,
		StartupTimeout: p.StartupTimeout.String(), ToolTimeout: p.ToolTimeout.String(),
		Fallback: p.Fallback, ClientFlags: p.ClientFlags,
	}
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
	for _, p := range overrides {
		if p.Name == "" {
			return nil, fmt.Errorf("desktop_profile requires name")
		}
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
		if len(p.Clients) > 0 && !containsDesktop(p.Clients, client) && !containsDesktop(p.Clients, "*") {
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

// DesktopPolicyEnv serializes non-sensitive profile policy for the Atenea MCP
// process. Values are allowlists/denylists only; no credentials are included.
func DesktopPolicyEnv(profile DesktopProfile) []string {
	return []string{
		"ATENEA_DESKTOP_PROFILE=" + profile.Name,
		"ATENEA_DESKTOP_FALLBACK=" + profile.Fallback,
		"ATENEA_DESKTOP_ENABLED_TOOLS=" + strings.Join(profile.EnabledTools, ","),
		"ATENEA_DESKTOP_DISABLED_TOOLS=" + strings.Join(profile.DisabledTools, ","),
		"ATENEA_DESKTOP_TOOL_TIMEOUT_MS=" + strconv.FormatInt(profile.ToolTimeout.Milliseconds(), 10),
	}
}
