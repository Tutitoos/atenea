package core_test

import (
	"strings"
	"testing"
)

func desktopMCPSettings(t *testing.T) string {
	t.Helper()
	settings := strings.Replace(mcpSettings(t), "[orchestrator]\n",
		"[orchestrator]\nclient_effects = [\"device\"]\n"+
			"client_denied_capabilities = [\"desktop.move\"]\n", 1)
	return settings + `

[[capability]]
id = "desktop.inspect"
version = "1.0.0"
summary = "Inspect a desktop application."
effects = ["read", "device"]

[[implementation]]
id = "macos.inspect"
provider = "desktop"
capability = "desktop.inspect"

[[capability]]
id = "desktop.move"
version = "1.0.0"
summary = "Move the desktop pointer."
effects = ["read", "device"]

[[implementation]]
id = "macos.move"
provider = "desktop"
capability = "desktop.move"
`
}

func conservativeDesktopMCPSettings(t *testing.T) string {
	t.Helper()
	return desktopMCPSettings(t) + `

[[desktop_profile]]
name = "claude"
clients = ["claude"]
mcp_mode = "atenea_only"
direct_mcp = []
enabled_tools = ["catalog.repositories"]
disabled_tools = []
startup_timeout = "10s"
tool_timeout = "60s"
fallback = "diagnostic"
client_flags = ["--strict-mcp-config"]

[[desktop_profile]]
name = "shared"
clients = ["claude", "chatgpt", "codex", "opencode", "omp"]
mcp_mode = "hybrid"
direct_mcp = []
enabled_tools = []
disabled_tools = []
startup_timeout = "10s"
tool_timeout = "60s"
fallback = "diagnostic"
client_flags = []
`
}

func TestMCPDesktopSurfaceHidesDeniedCapabilities(t *testing.T) {
	atenea := buildService(t, desktopMCPSettings(t))
	defer serve(t, atenea)()

	c := dial(t)
	c.handshake("codex")
	listed := result(t, c.call("tools/list", nil), "tools/list")
	tools, ok := listed["tools"].([]any)
	if !ok {
		t.Fatalf("tools = %v, want a list", listed["tools"])
	}
	seen := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		seen[name] = true
	}
	if !seen["desktop.inspect"] {
		t.Error("desktop.inspect is missing from the read-only MCP surface")
	}
	if seen["desktop.move"] {
		t.Error("desktop.move was advertised despite the MCP capability kill switch")
	}

	answer := result(t, c.call("tools/call", map[string]any{
		"name":      "desktop.move",
		"arguments": map[string]any{},
	}), "desktop.move")
	if isError, _ := answer["isError"].(bool); !isError {
		t.Fatalf("denied desktop.move was not returned as a tool error: %v", answer)
	}
	content, _ := answer["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("denial had no content: %v", answer)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if !strings.Contains(text, "not exposed") {
		t.Fatalf("denial did not name the MCP capability gate: %v", answer)
	}
}

func TestMCPInitializeAppliesProfileFiltersAndDiagnosticFallback(t *testing.T) {
	atenea := buildService(t, conservativeDesktopMCPSettings(t))
	defer serve(t, atenea)()

	c := dial(t)
	result(t, c.call("initialize", map[string]any{
		"protocolVersion": mcpVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "claude", "version": "2.1.231"},
		"_meta":           map[string]any{"atenea": map[string]any{"profile": "claude"}},
	}), "initialize")
	listed := result(t, c.call("tools/list", nil), "tools/list")
	tools, _ := listed["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool["name"] == "code.search" {
			t.Fatal("code.search bypassed the desktop allowlist")
		}
	}
	c.close()
	c = dial(t)
	result(t, c.call("initialize", map[string]any{
		"protocolVersion": mcpVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "claude", "version": "2.1.231"},
		"_meta":           map[string]any{"atenea": map[string]any{"profile": "shared"}},
	}), "initialize shared")
	answer := result(t, c.call("tools/call", map[string]any{"name": "unknown.tool", "arguments": map[string]any{}}), "unknown.tool")
	if isError, _ := answer["isError"].(bool); !isError {
		t.Fatalf("unknown tool was not a diagnostic error: %v", answer)
	}
	diagnostic, _ := answer["structuredContent"].(map[string]any)
	if diagnostic["error_code"] != "unknown_tool" || diagnostic["fallback_used"] != true {
		t.Fatalf("unexpected diagnostic: %v", diagnostic)
	}
	result(t, c.call("tools/list", nil), "tools/list after diagnostic")
}
