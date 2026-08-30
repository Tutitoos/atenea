// Package agentdevice describes the MCP surface supported by the pinned
// agent-device release. Keeping this manifest in Atenea makes an upgrade a
// deliberate compatibility event instead of silently widening a raw backend.
package agentdevice

import "slices"

// Version is the agent-device release whose MCP surface this manifest pins.
const Version = "0.20.10"

var allTools = []string{
	"alert", "app-switcher", "apps", "appstate", "artifacts", "audio",
	"back", "batch", "boot", "capabilities", "click", "clipboard", "close",
	"debug", "devices", "diff", "doctor", "events", "fill", "find", "focus",
	"get", "gesture", "home", "hover", "install", "install-from-source", "is",
	"keyboard", "logs", "longpress", "metro", "network", "open", "orientation",
	"perf", "press", "push", "react-native", "record", "reinstall", "replay",
	"screenshot", "scroll", "session", "settings", "shutdown", "snapshot", "swipe",
	"test", "trace", "trigger-app-event", "tv-remote", "type", "viewport", "wait", "help",
}

var coreTools = []string{
	"devices", "capabilities", "doctor", "apps", "appstate", "boot", "open", "close",
	"snapshot", "screenshot", "find", "click", "fill", "type", "press", "back", "home",
	"scroll", "swipe", "wait",
}

// Tools returns the immutable full manifest as a copy.
func Tools() []string { return slices.Clone(allTools) }

// CoreTools returns the compact default catalog as a copy.
func CoreTools() []string { return slices.Clone(coreTools) }

// CatalogAllows reports whether a tool belongs to a named catalog.
func CatalogAllows(catalog, tool string) bool {
	tool = normalize(tool)
	switch catalog {
	case "full":
		return slices.Contains(allTools, tool)
	case "core":
		return slices.Contains(coreTools, tool)
	default:
		return false
	}
}

func normalize(tool string) string {
	const prefix = "raw.agent-device."
	if len(tool) >= len(prefix) && tool[:len(prefix)] == prefix {
		return tool[len(prefix):]
	}
	return tool
}

// Missing returns manifest tools absent from an upstream tools/list response.
func Missing(offered []string) []string {
	missing := make([]string, 0)
	for _, want := range allTools {
		if !slices.Contains(offered, want) {
			missing = append(missing, want)
		}
	}
	return missing
}
