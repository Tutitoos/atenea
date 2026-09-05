package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
)

func TestNormalizeDesktopToolName(t *testing.T) {
	name, fallback := normalizeDesktopToolName(" raw/context7/search ")
	if name != "raw.context7.search" || !fallback {
		t.Fatalf("unexpected normalized tool: %q fallback=%v", name, fallback)
	}
	name, fallback = normalizeDesktopToolName("catalog.repositories")
	if name != "catalog.repositories" || fallback {
		t.Fatalf("unexpected unchanged tool: %q fallback=%v", name, fallback)
	}
}

func TestDesktopPolicyFiltersTools(t *testing.T) {
	policy := desktopPolicy{EnabledTools: map[string]bool{"catalog.repositories": true}}
	tools := policy.filterTools([]map[string]any{
		{"name": "catalog.repositories"},
		{"name": "code.search"},
	})
	if len(tools) != 1 || tools[0]["name"] != "catalog.repositories" {
		t.Fatalf("unexpected filtered tools: %#v", tools)
	}
}

func TestDefaultDesktopPolicyDoesNotAdvertiseSessionUnion(t *testing.T) {
	tools := desktopPolicy{}.filterTools([]map[string]any{
		{"name": "raw.agent-device.session"},
		{"name": "catalog.repositories"},
	})
	if len(tools) != 1 || tools[0]["name"] != "catalog.repositories" {
		t.Fatalf("default tools = %#v", tools)
	}
}

func TestRawCatalogSelectsCoreAndFull(t *testing.T) {
	corePolicy := desktopPolicyFromProfile(config.DesktopProfile{
		Name: "chatgpt", RawCatalogs: map[string]string{"agent-device": "core"},
	})
	tools := []map[string]any{
		{"name": "raw.agent-device.devices"},
		{"name": "raw.agent-device.record"},
		{"name": "catalog.repositories"},
	}
	filtered := corePolicy.filterTools(tools)
	if len(filtered) != 2 || filtered[0]["name"] != "raw.agent-device.devices" || filtered[1]["name"] != "catalog.repositories" {
		t.Fatalf("core tools = %#v", filtered)
	}
	fullPolicy := desktopPolicyFromProfile(config.DesktopProfile{
		Name: "agent-device-full", RawCatalogs: map[string]string{"agent-device": "full"},
	})
	if len(fullPolicy.filterTools(tools)) != 3 {
		t.Fatalf("full catalog unexpectedly filtered: %#v", fullPolicy.filterTools(tools))
	}
}

func TestReadCompatibilitySummaryIsSanitizedAndAggregated(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(stateHome, "atenea", "compatibility-20260829.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"timestamp":"2026-08-29T10:00:00.000000000Z","client":"chatgpt","tool":"code.search","outcome":"available","latency_ms":2,"fallback_used":false}` + "\n" +
		`{"timestamp":"2026-08-29T10:01:00.000000000Z","client":"chatgpt","tool":"secret","outcome":"error","error_code":"tool_timeout","fallback_used":false}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	summary := ReadCompatibilitySummary()
	if summary.Available != 1 || summary.Error != 1 || summary.LastErrorCode != "tool_timeout" {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if summary.LastEventAt == "" {
		t.Fatal("last event was not recorded")
	}
}

func TestCompatibilitySummaryNormalizesChatGPTEmbeddedClient(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	path := filepath.Join(stateHome, "atenea", "compatibility-20260829.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"timestamp":"2026-08-29T10:00:00.000000000Z","client":"codex-mcp-client","client_version":"0.150.0-alpha.12.2","tool":"code.search","outcome":"available","latency_ms":2,"fallback_used":false}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	summary := ReadCompatibilitySummaryFor("chatgpt", "chatgpt")
	if summary.Available != 1 {
		t.Fatalf("embedded ChatGPT event was not included: %#v", summary)
	}
}

func TestCompatibilityClientIDNormalizesEmbeddedChatGPT(t *testing.T) {
	if got := compatibilityClientID("codex-mcp-client", "chatgpt"); got != "chatgpt" {
		t.Fatalf("client id = %q, want chatgpt", got)
	}
}

func TestDesktopSchemaAndResultNormalization(t *testing.T) {
	schema := normalizeDesktopSchema(map[string]any{"required": []any{"query"}})
	if schema["type"] != "object" || schema["properties"] == nil || schema["required"] == nil {
		t.Fatalf("schema normalization lost fields: %#v", schema)
	}
	result := normalizeDesktopResult(map[string]any{"structuredContent": map[string]any{"answer": "ok"}})
	if result["isError"] != false || result["content"] == nil {
		t.Fatalf("result normalization = %#v", result)
	}
}

func TestCompatibilityRotationKeepsExistingRotations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compatibility-20260829.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", compatibilityMaxBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotateCompatibilityLog(path)
	if _, err := os.Stat(path + ".2"); err != nil {
		t.Fatalf("rotation did not choose next suffix: %v", err)
	}
}

func TestCompatibilityRetentionRemovesOnlyExpiredLogs(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "compatibility-old.jsonl")
	keep := filepath.Join(dir, "compatibility-new.jsonl")
	if err := os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-compatibilityRetention - time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	pruneCompatibilityLogs(dir, time.Now().Add(-compatibilityRetention))
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired log remains: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("current log was removed: %v", err)
	}
}

func TestCompatibilityOutcomeUsesStableDiagnosticCode(t *testing.T) {
	result := desktopDiagnostic("unknown_tool", "raw/x/y", "raw.x.y", true, "unknown", "list")
	outcome, code := compatibilityOutcome(result, nil, "", true)
	if outcome != "fallback" || code != "unknown_tool" {
		t.Fatalf("outcome=%s code=%s", outcome, code)
	}
	outcome, _ = compatibilityOutcome(nil, &rpcError{Code: -32602}, "", false)
	if outcome != "error" {
		t.Fatalf("rpc outcome=%s", outcome)
	}
}

func TestDesktopClientIDAndPolicyTimeout(t *testing.T) {
	if desktopClientID("Codex CLI") != "codex" || desktopClientID("unrecognized") != "" {
		t.Fatal("client normalization is not deterministic")
	}
	policy := desktopPolicyFromProfile(config.DesktopProfile{Name: "chatgpt", Fallback: "diagnostic", ToolTimeout: time.Second, EnabledTools: []string{"one"}})
	ctx, cancel := policy.withToolTimeout(context.Background())
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("context timed out before the deadline")
	default:
	}
	if policy.Profile != "chatgpt" || !policy.EnabledTools["one"] {
		t.Fatalf("policy = %#v", policy)
	}
}
