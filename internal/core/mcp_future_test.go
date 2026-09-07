package core

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/mcpcompat"
)

func TestWithModernServerMetaPreservesFutureResult(t *testing.T) {
	got, ok := withModernServerMeta(map[string]any{
		"resultType":   "future_result",
		"requestState": "opaque-state",
		"payload":      map[string]any{"keep": true},
	}, true).(map[string]any)
	if !ok {
		t.Fatal("future result was not returned as an object")
	}
	if got["resultType"] != "future_result" || got["requestState"] != "opaque-state" {
		t.Fatalf("future result was rewritten: %v", got)
	}
	if _, exists := got["ttlMs"]; exists {
		t.Fatalf("future result received complete-only cache fields: %v", got)
	}
	meta, ok := got["_meta"].(map[string]any)
	if !ok || meta[mcpcompat.ServerInfoKey] == nil {
		t.Fatalf("future result lost server metadata: %v", got)
	}
}

func TestWithModernServerMetaAddsCompleteOnlyWhenMarkerIsAbsent(t *testing.T) {
	got, ok := withModernServerMeta(map[string]any{"content": []any{}}, true).(map[string]any)
	if !ok || got["resultType"] != string(mcpcompat.ResultComplete) || got["ttlMs"] != int64(0) || got["cacheScope"] != "private" {
		t.Fatalf("legacy result normalization = %v", got)
	}
}
