package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/agentdevice"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/passthrough"
	"github.com/Tutitoos/atenea/internal/platform"
)

type desktopPolicy struct {
	Profile       string
	Fallback      string
	EnabledTools  map[string]bool
	DisabledTools map[string]bool
	RawCatalogs   map[string]string
	ToolTimeout   time.Duration
}

func desktopPolicyFromProfile(profile config.DesktopProfile) desktopPolicy {
	return desktopPolicy{
		Profile:       profile.Name,
		Fallback:      profile.Fallback,
		EnabledTools:  desktopSet(profile.EnabledTools),
		DisabledTools: desktopSet(profile.DisabledTools),
		RawCatalogs:   cloneCatalogs(profile.RawCatalogs),
		ToolTimeout:   profile.ToolTimeout,
	}
}

func (p desktopPolicy) withToolTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if p.ToolTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, p.ToolTimeout)
}

func desktopSet(raw []string) map[string]bool {
	values := make(map[string]bool)
	for _, value := range raw {
		if value = strings.TrimSpace(value); value != "" {
			values[value] = true
		}
	}
	return values
}

func cloneCatalogs(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for server, catalog := range raw {
		out[server] = catalog
	}
	return out
}

func (p desktopPolicy) allows(tool string) bool {
	// The upstream session action union is intentionally available only through
	// atenea.command device.sessions, which constructs an action=list request.
	if tool == "raw.agent-device.session" {
		return false
	}
	if p.DisabledTools[tool] {
		return false
	}
	if server, raw, ok := passthrough.Split(tool); ok {
		if catalog, selected := p.RawCatalogs[server]; selected {
			return agentdevice.CatalogAllows(catalog, raw)
		}
	}
	if len(p.EnabledTools) > 0 && !p.EnabledTools[tool] {
		return false
	}
	return true
}

func (p desktopPolicy) filterTools(tools []map[string]any) []map[string]any {
	if len(p.EnabledTools) == 0 && len(p.DisabledTools) == 0 && len(p.RawCatalogs) == 0 {
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
	RequestID     string `json:"request_id,omitempty"`
	ReceiptID     string `json:"receipt_id,omitempty"`
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

const (
	compatibilityMaxBytes  = 10 * 1024 * 1024
	compatibilityRetention = 14 * 24 * time.Hour
)

// CompatibilitySummary is the sanitized aggregate exposed to diagnostics.
// It deliberately contains no request or response payloads.
type CompatibilitySummary struct {
	LastEventAt   string `json:"last_event_at,omitempty"`
	Available     int    `json:"available"`
	Denied        int    `json:"denied"`
	Fallback      int    `json:"fallback"`
	Error         int    `json:"error"`
	LastErrorCode string `json:"last_error_code,omitempty"`
}

func (v *conversation) recordCompatibility(tool, outcome, errorCode string, started time.Time, fallbackUsed bool, ids ...string) {
	path := filepath.Join(platform.StateDir(), "compatibility-"+time.Now().UTC().Format("20060102")+".jsonl")
	event := compatibilityEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Client:    compatibilityClientID(v.clientName, v.policy.Profile), ClientVersion: v.clientVersion, Profile: v.policy.Profile,
		Server: "atenea", Tool: tool, Outcome: outcome,
		LatencyMS: time.Since(started).Milliseconds(), FallbackUsed: fallbackUsed,
		ErrorCode: errorCode,
	}
	if len(ids) > 0 {
		event.RequestID = ids[0]
	}
	if len(ids) > 1 {
		event.ReceiptID = ids[1]
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
	rotateCompatibilityLog(path)
	pruneCompatibilityLogs(filepath.Dir(path), time.Now().UTC().Add(-compatibilityRetention))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s\n", data)
}

func rotateCompatibilityLog(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < compatibilityMaxBytes {
		return
	}
	for suffix := 1; ; suffix++ {
		rotated := fmt.Sprintf("%s.%d", path, suffix)
		if _, err := os.Stat(rotated); err == nil {
			continue
		}
		_ = os.Rename(path, rotated)
		return
	}
}

func pruneCompatibilityLogs(dir string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "compatibility-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// ReadCompatibilitySummary reads only structured compatibility fields and is
// safe for doctor/status output. Malformed or unrelated files are ignored.
func ReadCompatibilitySummary() CompatibilitySummary {
	return readCompatibilitySummary("", "")
}

// ReadCompatibilitySummaryFor returns sanitized counters for one desktop
// client/profile. Empty filters keep the all-client aggregate behavior.
func ReadCompatibilitySummaryFor(client, profile string) CompatibilitySummary {
	return readCompatibilitySummary(client, profile)
}

func readCompatibilitySummary(client, profile string) CompatibilitySummary {
	var summary CompatibilitySummary
	lastErrorAt := ""
	client = compatibilityClientID(client, "")
	entries, err := os.ReadDir(platform.StateDir())
	if err != nil {
		return summary
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "compatibility-") {
			continue
		}
		paths = append(paths, filepath.Join(platform.StateDir(), entry.Name()))
	}
	sort.Strings(paths)
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		decoder := json.NewDecoder(file)
		for {
			var event compatibilityEvent
			if err := decoder.Decode(&event); err != nil {
				break
			}
			eventClient := compatibilityClientID(event.Client, event.Profile)
			profileMatch := profile == "" || event.Profile == profile ||
				(profile == "chatgpt" && event.Profile == "" && eventClient == "chatgpt")
			if (client != "" && eventClient != client) || !profileMatch {
				continue
			}
			switch event.Outcome {
			case "available":
				summary.Available++
			case "denied":
				summary.Denied++
			case "fallback":
				summary.Fallback++
			case "error":
				summary.Error++
			}
			if event.Timestamp > summary.LastEventAt {
				summary.LastEventAt = event.Timestamp
			}
			if event.ErrorCode != "" && event.Timestamp > lastErrorAt {
				summary.LastErrorCode = event.ErrorCode
				lastErrorAt = event.Timestamp
			}
		}
		_ = file.Close()
	}
	return summary
}

func compatibilityOutcome(result any, rpcErr *rpcError, hinted string, fallback bool) (string, string) {
	o := observeMCP(result, rpcErr, nil)
	if hinted != "" {
		return hinted, o.Code
	}
	return o.compatibility(fallback), o.Code
}

func (v *conversation) filterDesktopTools(tools []map[string]any) []map[string]any {
	return v.policy.filterTools(tools)
}

func compatibilityClientID(name, profile string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "codex-mcp-client" {
		return "chatgpt"
	}
	if id := desktopClientID(name); id != "" {
		return id
	}
	return name
}

func desktopClientID(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(name, "claude"):
		return "claude"
	case strings.Contains(name, "chatgpt"):
		return "chatgpt"
	case strings.Contains(name, "codex"):
		return "codex"
	case strings.Contains(name, "opencode"):
		return "opencode"
	case strings.Contains(name, "omp"):
		return "omp"
	default:
		return ""
	}
}
