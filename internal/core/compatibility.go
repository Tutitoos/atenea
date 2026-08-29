package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/platform"
)

type desktopPolicy struct {
	Profile       string
	Fallback      string
	EnabledTools  map[string]bool
	DisabledTools map[string]bool
	ToolTimeout   time.Duration
}

func desktopPolicyFromEnv() desktopPolicy {
	policy := desktopPolicy{
		Profile:       os.Getenv("ATENEA_DESKTOP_PROFILE"),
		Fallback:      os.Getenv("ATENEA_DESKTOP_FALLBACK"),
		EnabledTools:  desktopCSV(os.Getenv("ATENEA_DESKTOP_ENABLED_TOOLS")),
		DisabledTools: desktopCSV(os.Getenv("ATENEA_DESKTOP_DISABLED_TOOLS")),
	}
	if milliseconds, err := strconv.ParseInt(os.Getenv("ATENEA_DESKTOP_TOOL_TIMEOUT_MS"), 10, 64); err == nil && milliseconds > 0 {
		policy.ToolTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	if policy.Fallback == "" {
		policy.Fallback = "none"
	}
	return policy
}

func (p desktopPolicy) withToolTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if p.ToolTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, p.ToolTimeout)
}

func desktopCSV(raw string) map[string]bool {
	values := make(map[string]bool)
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values[value] = true
		}
	}
	return values
}

func (p desktopPolicy) allows(tool string) bool {
	if len(p.EnabledTools) > 0 && !p.EnabledTools[tool] {
		return false
	}
	return !p.DisabledTools[tool]
}

func (p desktopPolicy) filterTools(tools []map[string]any) []map[string]any {
	if len(p.EnabledTools) == 0 && len(p.DisabledTools) == 0 {
		return tools
	}
	filtered := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		if p.allows(name) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func normalizeDesktopSchema(value any) map[string]any {
	result := map[string]any{}
	if value != nil {
		if raw, err := json.Marshal(value); err == nil {
			_ = json.Unmarshal(raw, &result)
		}
	}
	if result == nil {
		result = map[string]any{}
	}
	if _, ok := result["type"]; !ok {
		result["type"] = "object"
	}
	if _, ok := result["properties"]; !ok {
		result["properties"] = map[string]any{}
	}
	return result
}

func normalizeDesktopResult(result map[string]any) map[string]any {
	if _, ok := result["content"]; !ok {
		value := result["structuredContent"]
		if value == nil {
			value = result
		}
		body, err := json.Marshal(value)
		if err == nil {
			result["content"] = []any{map[string]any{"type": "text", "text": string(body)}}
		}
	}
	if _, ok := result["isError"]; !ok {
		result["isError"] = false
	}
	return result
}

func normalizeDesktopToolName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if !strings.HasPrefix(name, "raw/") {
		return name, false
	}
	parts := strings.Split(strings.TrimPrefix(name, "raw/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return name, false
	}
	return "raw." + parts[0] + "." + parts[1], true
}

type compatibilityEvent struct {
	Timestamp     string `json:"timestamp"`
	Client        string `json:"client"`
	ClientVersion string `json:"client_version,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Server        string `json:"server"`
	Tool          string `json:"tool"`
	Outcome       string `json:"outcome"`
	LatencyMS     int64  `json:"latency_ms"`
	FallbackUsed  bool   `json:"fallback_used"`
	ErrorCode     string `json:"error_code,omitempty"`
}

var compatibilityLogMu sync.Mutex

func (v *conversation) recordCompatibility(tool, outcome, errorCode string, started time.Time, fallbackUsed bool) {
	path := filepath.Join(platform.StateDir(), "compatibility-"+time.Now().UTC().Format("20060102")+".jsonl")
	event := compatibilityEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Client:    v.clientName, ClientVersion: v.clientVersion, Profile: v.policy.Profile,
		Server: "atenea", Tool: tool, Outcome: outcome,
		LatencyMS: time.Since(started).Milliseconds(), FallbackUsed: fallbackUsed,
		ErrorCode: errorCode,
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	compatibilityLogMu.Lock()
	defer compatibilityLogMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if info, err := os.Stat(path); err == nil && info.Size() >= 10*1024*1024 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s\n", data)
}

func compatibilityOutcome(result any, rpcErr *rpcError, hinted string, fallback bool) (string, string) {
	if hinted != "" {
		return hinted, ""
	}
	if rpcErr != nil {
		return "error", fmt.Sprint(rpcErr.Code)
	}
	if body, ok := result.(map[string]any); ok {
		if failed, ok := body["isError"].(bool); ok && failed {
			if fallback {
				return "fallback", "tool_failure"
			}
			return "error", "tool_failure"
		}
	}
	return "available", ""
}

func (v *conversation) filterDesktopTools(tools []map[string]any) []map[string]any {
	return v.policy.filterTools(tools)
}
