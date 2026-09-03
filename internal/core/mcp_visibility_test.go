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

func TestKivgraphUsageOnlyReportsDispatchedProvider(t *testing.T) {
	for _, tc := range []struct {
		name, provider           string
		dispatched, failed, want bool
	}{
		{"success", "kivgraph", true, false, true},
		{"failure", "kivgraph", true, true, true},
		{"selected but not called", "kivgraph", false, true, false},
		{"other provider", "ripgrep", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := orchestrator.StepResult{Dispatched: tc.dispatched}
			step.Decision.Chosen = contract.Implementation{ID: "test.impl", Provider: tc.provider}
			step.Step.Capability, step.Step.Repository = "symbol.definition", "test-repo"
			step.Failure = "private error must not be copied"
			step.Review.Parent = contract.VerdictOK
			if tc.failed {
				step.Review.Parent = contract.VerdictFailed
			}
			structured := map[string]any{"original": true}
			out := map[string]any{"content": []any{map[string]any{"type": "text", "text": "original"}}, "structuredContent": structured, "isError": tc.failed}
			appendToolUsage(out, &orchestrator.Result{Steps: []orchestrator.StepResult{step}})
			content := out["content"].([]any)
			wantLen := 2
			if len(content) != wantLen {
				t.Fatalf("content = %v", content)
			}
			if !reflect.DeepEqual(out["structuredContent"], structured) || out["isError"] != tc.failed || content[0].(map[string]any)["text"] != "original" {
				t.Fatal("original response changed")
			}
			{
				text := content[1].(map[string]any)["text"].(string)
				var receipt map[string]map[string]any
				if err := json.Unmarshal([]byte(text), &receipt); err != nil {
					t.Fatal(err)
				}
				_, hasLegacy := receipt["atenea_kivgraph_usage"]
				if hasLegacy != tc.want {
					t.Fatal("incorrect Kivgraph alias")
				}
				usage := receipt["atenea_usage"]
				if usage["repository"] != "test-repo" || usage["capability"] != "symbol.definition" || usage["verdict"] != step.Review.Parent.String() || usage["invoked"] != tc.dispatched {
					t.Fatalf("receipt = %v", usage)
				}
				if strings.Contains(text, "private") {
					t.Fatal("leaked failure text")
				}
			}
		})
	}
}

func TestKivgraphUsageAcceptsMissingResult(t *testing.T) {
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
	step.Decision.Chosen = contract.Implementation{ID: "kivgraph.overview", Provider: "kivgraph"}
	step.Review.Parent = contract.VerdictCanceled
	step.Decision.Stages = []selector.Stage{{Name: selector.StageReach,
		In:      []string{"tokensave.overview", "kivgraph.overview"},
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
