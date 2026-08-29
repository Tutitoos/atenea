package core

import "testing"

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
