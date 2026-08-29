package core

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/metrics"
)

func TestCommandMarkdownUsesChatFriendlySections(t *testing.T) {
	response := CommandResponse{
		Command: "metrics",
		Status:  "ok",
		Summary: "1 measurement row(s)",
		Data: []metrics.Row{{
			Capability: "code.search", Implementation: "ripgrep", Provider: "ripgrep",
			Repository: "api", Successes: 3, Failures: 1,
		}},
	}
	got := commandMarkdown(response)
	for _, want := range []string{"## Atenea: metrics", "**Estado:** `ok`", "| Capacidad |", "| code.search |", "| 3 | 1 |"} {
		if !strings.Contains(got, want) {
			t.Errorf("Markdown missing %q:\n%s", want, got)
		}
	}
}

func TestCommandMarkdownEscapesTableContent(t *testing.T) {
	got := commandMarkdown(CommandResponse{
		Command: "metrics", Status: "ok", Summary: "summary",
		Data: []metrics.Row{{Capability: "a|b", Implementation: "impl", Provider: "p", Repository: "r"}},
	})
	if strings.Contains(got, "| a|b |") || !strings.Contains(got, `a\|b`) {
		t.Fatalf("table content was not escaped: %s", got)
	}
}

func TestCommandTextPreservesContentWhileRemovingFormatting(t *testing.T) {
	got := commandText("## Atenea: status\n\n**Estado:** `ok`\n")
	if got != "Atenea: status\n\nEstado: ok\n" {
		t.Fatalf("plain command output = %q", got)
	}
}
