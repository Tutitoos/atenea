package semanticreviewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/agent/model"
	"github.com/Tutitoos/atenea/pkg/contract"
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

// Every review this agent finishes cost a model turn, and the report is the
// only place that turn's money is ever counted. A report with no `spent`
// means "an agent that spends nothing" on the wire -- see reportWire.Spent --
// which is the opposite of what this one does, and it held for the failing
// outcomes too: a turn that died at its ceiling occupied the provider and was
// billed for it.
func TestEveryOutcomeReportsWhatTheTurnCost(t *testing.T) {
	usd := 0.0312
	charged := contract.Charge{
		InputTokens: 1200, OutputTokens: 90, CacheReadTokens: 40, CacheWriteTokens: 7,
		USD: &usd, PricedBy: "provider",
	}
	answered := func(verdict string) model.Answer {
		return model.Answer{
			Structured: []byte(`{"verdict":"` + verdict + `","confidence":80,"claims":["claim"],` +
				`"gaps":[],"evidence":["line 10"],"scope":"subject result only"}`),
			Spent: charged,
		}
	}
	for _, tc := range []struct {
		name   string
		answer model.Answer
		err    error
		want   string
	}{
		{name: "supported", answer: answered("supported"), want: "ok"},
		{name: "unsupported", answer: answered("unsupported"), want: "failed"},
		{name: "indeterminate", answer: answered("indeterminate"), want: "incomplete"},
		{name: "an answer this agent cannot read", answer: model.Answer{Structured: []byte("not-json"), Spent: charged}, want: "incomplete"},
		{name: "a turn that died holding a charge", answer: model.Answer{Spent: charged}, err: errors.New("provider unavailable"), want: "incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := judge(context.Background(), testAssignment(), &fakeCaller{answer: tc.answer, err: tc.err}, "/repo")
			if got.Verdict != tc.want {
				t.Fatalf("verdict = %s, want %s (%+v)", got.Verdict, tc.want, got)
			}
			if got.Spent == nil {
				t.Fatalf("a review that called a model reported no spend at all: %+v", got)
			}
			if got.Spent.USD == nil || *got.Spent.USD != usd || got.Spent.InputTokens != 1200 {
				t.Fatalf("spent = %+v, want the turn's own charge", got.Spent)
			}
			// The wire key is what the engine reads back, so a field
			// renamed here would be a report that still counts nothing.
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(encoded), `"spent":{`) {
				t.Fatalf("report = %s, want a spent object on the wire", encoded)
			}
		})
	}
}

// A shortfall raised before any turn was attempted is the other half of the
// rule: nil is what unmeasured looks like, and writing a zeroed charge there
// would claim a turn that really was free.
func TestAReviewThatNeverCalledAModelReportsNoSpend(t *testing.T) {
	got := run(context.Background(), assignment{})
	if got.Verdict != "incomplete" {
		t.Fatalf("verdict = %s, want incomplete", got.Verdict)
	}
	if got.Spent != nil {
		t.Fatalf("spent = %+v, want nil: no turn was ever attempted", got.Spent)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "spent") {
		t.Fatalf("report = %s, want no spent key at all", encoded)
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

// The shipped settings leave both roles unset -- a fresh install must not
// spend money on its first dispatch -- and a route that names a backend and a
// binary but no model does not fill that in. The refusal has to come before
// anything spawns.
//
// The settings file is named here rather than inherited. Without ATENEA_CONFIG
// this read whatever settings the machine running the test happens to have,
// so on a developer's own machine -- where both roles ARE configured -- it
// spawned the real CLI and billed a real turn to assert a refusal.
func TestRunWithNoModelConfiguredDoesNotInventOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atenea.toml")
	settings := "contract = \"4.0.0\"\n\n[model]\nbinary = " +
		strconv.Quote(filepath.Join(t.TempDir(), "no-such-cli")) + "\nexplore = \"\"\nplan = \"\"\n"
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	t.Setenv("ATENEA_CONFIG", path)

	in := testAssignment()
	in.Route = &route{Backend: "claude", Model: ""}
	got := run(context.Background(), in)
	if got.Verdict != "incomplete" || got.Reason == nil || !strings.Contains(got.Reason.Text, "model") {
		t.Fatalf("run = %+v, want an unavailable model reason", got)
	}
	if got.Spent != nil {
		t.Fatalf("spent = %+v, want nil: a refusal before the spawn costs nothing", got.Spent)
	}
}

func TestMainRejectsMalformedAssignments(t *testing.T) {
	var out bytes.Buffer
	if err := Main(strings.NewReader("not-json"), &out); err == nil {
		t.Fatal("malformed assignment was accepted")
	}
}
