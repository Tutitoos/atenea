package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The receipt is the same for every provider: one atenea_usage block per step,
// carrying only selector metadata and never the failure text, and no
// provider-named alias beside it.
func TestUsageReceiptIsProviderNeutralAndLeaksNothing(t *testing.T) {
	for _, tc := range []struct {
		name, provider     string
		dispatched, failed bool
	}{
		{"success", "tokensave", true, false},
		{"failure", "tokensave", true, true},
		{"selected but not called", "tokensave", false, true},
		{"other provider", "ripgrep", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := orchestrator.StepResult{Dispatched: tc.dispatched}
			step.Decision.Chosen = contract.Implementation{ID: "test.impl", Provider: tc.provider}
			step.Step.Capability, step.Step.Repository = "symbol.overview", "test-repo"
			step.Failure = "private error must not be copied"
			step.Review.Parent = contract.VerdictOK
			if tc.failed {
				step.Review.Parent = contract.VerdictFailed
			}
			structured := map[string]any{"original": true}
			out := map[string]any{"content": []any{map[string]any{"type": "text", "text": "original"}}, "structuredContent": structured, "isError": tc.failed}
			appendToolUsage(out, &orchestrator.Result{Steps: []orchestrator.StepResult{step}})
			content := out["content"].([]any)
			if len(content) != 2 {
				t.Fatalf("content = %v", content)
			}
			if !reflect.DeepEqual(out["structuredContent"], structured) || out["isError"] != tc.failed || content[0].(map[string]any)["text"] != "original" {
				t.Fatal("original response changed")
			}
			text := content[1].(map[string]any)["text"].(string)
			var receipt map[string]map[string]any
			if err := json.Unmarshal([]byte(text), &receipt); err != nil {
				t.Fatal(err)
			}
			if len(receipt) != 1 {
				t.Fatalf("receipt carries more than atenea_usage: %v", receipt)
			}
			usage := receipt["atenea_usage"]
			if usage["repository"] != "test-repo" || usage["capability"] != "symbol.overview" || usage["verdict"] != step.Review.Parent.String() || usage["invoked"] != tc.dispatched {
				t.Fatalf("receipt = %v", usage)
			}
			if strings.Contains(text, "private") {
				t.Fatal("leaked failure text")
			}
		})
	}
}

func TestUsageAcceptsMissingResult(t *testing.T) {
	appendToolUsage(nil, nil)
	out := map[string]any{"isError": true}
	appendToolUsage(out, nil)
	if len(out) != 1 {
		t.Fatal("nil run modified response")
	}
}

func TestUsageReportsFallbackAndCancellationWithoutLeakingReasons(t *testing.T) {
	step := orchestrator.StepResult{Dispatched: true, FailureKind: contract.FailureCanceled}
	step.Step.Prefer = "tokensave"
	step.Decision.Chosen = contract.Implementation{ID: "ripgrep", Provider: "ripgrep"}
	step.Review.Parent = contract.VerdictCanceled
	step.Decision.Stages = []selector.Stage{{
		Name:    selector.StageReach,
		In:      []string{"tokensave.overview", "ripgrep"},
		Dropped: []selector.Drop{{Implementation: "tokensave.overview", Code: "repository_scope", Reason: "private detail", Raw: "private token"}},
	}}
	out := map[string]any{"content": []any{}}
	appendToolUsage(out, &orchestrator.Result{Steps: []orchestrator.StepResult{step, step}})
	blocks := out["content"].([]any)
	if len(blocks) != 2 {
		t.Fatal("one receipt per repeated or parallel step required")
	}
	text := blocks[0].(map[string]any)["text"].(string)
	for _, want := range []string{`"fallback":true`, `"verdict":"canceled"`, `"reason":"repository_scope"`, `"invoked":true`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, "private") {
		t.Fatal("leaked provider details")
	}
}
