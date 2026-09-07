package workflow

import (
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestRouteRoundTripsWithProviderAndToolChoices(t *testing.T) {
	original := &contract.Route{
		Model: "sonnet", RequestedModel: "sonnet", Backend: "claude", Binary: "claude",
		Role: "research", ReasoningEffort: "medium", RequestedReasoningEffort: "medium",
		ObservedModel: "gpt-5.6-sol", ObservedReasoningEffort: "medium",
		Capabilities: []string{"code.search"},
		Providers:    map[string]string{"code.search": "ripgrep"},
		Tools:        []string{"Read", "Glob"},
	}
	decoded, err := readRoute(jsonRoute(original))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Model != original.Model || decoded.RequestedModel != original.RequestedModel ||
		decoded.Role != original.Role || decoded.ReasoningEffort != original.ReasoningEffort ||
		decoded.ObservedModel != original.ObservedModel || decoded.ObservedReasoningEffort != original.ObservedReasoningEffort ||
		decoded.Providers["code.search"] != "ripgrep" || len(decoded.Tools) != 2 {
		t.Fatalf("decoded route = %+v, want %+v", decoded, original)
	}
	decoded.Providers["code.search"] = "other"
	if original.Providers["code.search"] != "ripgrep" {
		t.Fatal("route decode aliased the provider map")
	}
}
