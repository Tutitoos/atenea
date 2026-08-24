package semanticreviewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/agent/model"
)

type fakeCaller struct {
	answer model.Answer
	err    error
	seen   model.Request
}

func (f *fakeCaller) Turn(_ context.Context, req model.Request) (model.Answer, error) {
	f.seen = req
	return f.answer, f.err
}

func testAssignment() assignment {
	in := assignment{}
	in.Task.Objective = "check the conclusion"
	in.Task.Criterion = "the conclusion follows from the evidence"
	in.Task.Files = []string{"README.md"}
	in.Limits.MaxTokens = 1200
	in.BudgetUSD = float64Ptr(0.25)
	in.Subject = &subject{RunID: "run-1", Type: "reader", Verdict: "ok", Result: map[string]any{
		"findings": "README.md:10 supports the claim",
	}}
	return in
}

func float64Ptr(value float64) *float64 { return &value }

func TestJudgeMapsSemanticVerdictsAndCarriesRequest(t *testing.T) {
	tests := []struct {
		name, verdict, want string
	}{
		{name: "supported", verdict: "supported", want: "ok"},
		{name: "unsupported", verdict: "unsupported", want: "failed"},
		{name: "indeterminate", verdict: "indeterminate", want: "incomplete"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := testAssignment()
			fake := &fakeCaller{answer: model.Answer{Structured: []byte(`{"verdict":"` + tc.verdict + `","confidence":80,"claims":["claim"],"gaps":[],"evidence":["line 10"],"scope":"subject result only"}`)}}
			got := judge(context.Background(), in, fake, "/repo")
			if got.Verdict != tc.want {
				t.Fatalf("verdict = %s, want %s (%+v)", got.Verdict, tc.want, got)
			}
			if fake.seen.Role != model.RoleExplore || fake.seen.Dir != "/repo" || fake.seen.MaxTokens != 1200 || fake.seen.BudgetUSD != 0.25 {
				t.Fatalf("request = %+v", fake.seen)
			}
			if !strings.Contains(fake.seen.Prompt, "untrusted evidence") {
				t.Fatal("prompt did not mark subject evidence as untrusted")
			}
		})
	}
}

func TestJudgeKeepsUncertaintyAndProviderFailuresIncomplete(t *testing.T) {
	in := testAssignment()
	for _, tc := range []struct {
		name   string
		answer model.Answer
		err    error
	}{
		{name: "invalid json", answer: model.Answer{Structured: []byte("not-json")}},
		{name: "unknown verdict", answer: model.Answer{Structured: []byte(`{"verdict":"maybe","confidence":80,"claims":[],"gaps":[],"evidence":[],"scope":"x"}`)}},
		{name: "invalid confidence", answer: model.Answer{Structured: []byte(`{"verdict":"supported","confidence":101,"claims":[],"gaps":[],"evidence":[],"scope":"x"}`)}},
		{name: "provider error", err: errors.New("provider unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := judge(context.Background(), in, &fakeCaller{answer: tc.answer, err: tc.err}, "/repo")
			if got.Verdict != "incomplete" || got.Reason == nil {
				t.Fatalf("got %+v, want incomplete with reason", got)
			}
		})
	}
}

func TestPromptSchemaAndHelpersAreExplicit(t *testing.T) {
	in := testAssignment()
	prompt, err := promptFor(in)
	if err != nil || !strings.Contains(prompt, "README.md") || !strings.Contains(prompt, "Distinguish supported") {
		t.Fatalf("prompt = %q, err = %v", prompt, err)
	}
	got := schema()
	if got["additionalProperties"] != false || len(got["required"].([]any)) != 6 {
		t.Fatalf("schema = %#v", got)
	}
	in.Context = map[string]json.RawMessage{"repository": []byte(`{"root":"/workspace/repo"}`)}
	if root := repositoryRoot(in); root != "/workspace/repo" {
		t.Fatalf("root = %q", root)
	}
	if reasonText(nil) != "no reason given" || reasonText(&reason{Text: "why"}) != "why" {
		t.Fatal("reason text helper lost its explicit fallback")
	}
}

func TestRunStopsBeforeModelForMissingOrFailedSubjects(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func() assignment
	}{
		{name: "missing", make: func() assignment { return assignment{} }},
		{name: "failed", make: func() assignment {
			in := testAssignment()
			in.Subject.Verdict = "failed"
			in.Subject.Reason = &reason{Text: "the answer was rejected"}
			return in
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(context.Background(), tc.make())
			if got.Verdict != "incomplete" || got.Reason == nil {
				t.Fatalf("run = %+v", got)
			}
		})
	}
}

func TestRunWithDefaultSettingsDoesNotInventAConfiguredModel(t *testing.T) {
	in := testAssignment()
	in.Route = &route{Backend: "claude", Binary: "claude", Model: ""}
	got := run(context.Background(), in)
	if got.Verdict != "incomplete" || got.Reason == nil || !strings.Contains(got.Reason.Text, "model") {
		t.Fatalf("run = %+v, want an unavailable model reason", got)
	}
}

func TestMainRejectsMalformedAssignments(t *testing.T) {
	var out bytes.Buffer
	if err := Main(strings.NewReader("not-json"), &out); err == nil {
		t.Fatal("malformed assignment was accepted")
	}
}
