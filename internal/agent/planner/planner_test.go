package planner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/agent/model"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// stub is a model that answers whatever the test wrote down. The point of the
// deps seam is that everything worth asserting here -- an answer in the wrong
// shape, an empty one, a turn that died holding a charge -- is reachable
// without a key, a network or a subscription.
type stub struct {
	answer model.Answer
	err    error
	seen   model.Request
	calls  int
}

func (s *stub) Turn(_ context.Context, req model.Request) (model.Answer, error) {
	s.calls++
	s.seen = req
	return s.answer, s.err
}

func structured(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func withTools(c caller) deps {
	return deps{client: c, tools: func() (string, error) { return "/tmp/mcp.json", nil }}
}

func usd(v float64) *float64 { return &v }

// charged is a measured charge: the number a real turn comes back with.
func charged() contract.Charge {
	return contract.Charge{InputTokens: 100, OutputTokens: 20, USD: usd(0.03), PricedBy: "claude-code"}
}

func exploreAssignment() assignment {
	return assignment{
		ID:   "a1",
		Type: "explore",
		Task: task{
			Objective: "find where the settings are read",
			Criterion: "the answer names a file",
		},
	}
}

func planAssignment(verdict string, result map[string]any) assignment {
	in := exploreAssignment()
	in.Type = "plan"
	in.Subject = &subject{
		RunID:   "r1",
		Type:    "explore",
		Verdict: verdict,
		Result:  result,
		Task:    in.Task,
	}
	return in
}

func exploration() map[string]any {
	return map[string]any{
		SummaryField:  "settings load in internal/config",
		FindingsField: "config.Load reads atenea.toml. ReadFile parses the graph. Nothing else touches it.",
	}
}

// ---- the exploration half -------------------------------------------------

func TestAnExplorationComesBackAsSummaryAndFindings(t *testing.T) {
	c := &stub{answer: model.Answer{
		Structured: structured(t, map[string]string{
			"summary":  "the settings load in one place",
			"findings": "config.Load reads the file. ReadFile parses the graph.",
		}),
		Spent: charged(),
	}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict != "ok" {
		t.Fatalf("verdict = %q (%s), want ok", got.Verdict, reasonOf(got))
	}
	if got.Result[SummaryField] != "the settings load in one place" {
		t.Errorf("summary = %v", got.Result[SummaryField])
	}
	if got.Spent == nil || got.Spent.USD == nil || *got.Spent.USD != 0.03 {
		t.Errorf("spent = %+v, want the turn's charge", got.Spent)
	}
}

func TestAnExplorationsFindingsBecomeDiscoveries(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary":  "one place",
		"findings": "First fact here. Second fact here. Third fact here. Fourth fact here.",
	})}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if len(got.Discovered) == 0 {
		t.Fatal("an exploration that found things discovered nothing")
	}
	if len(got.Discovered) > 3 {
		t.Errorf("discovered %d facts, want at most 3", len(got.Discovered))
	}
	for _, d := range got.Discovered {
		if d.Level != "repository" {
			t.Errorf("level = %q, want repository", d.Level)
		}
		if strings.TrimSpace(d.Note) == "" {
			t.Error("an empty discovery was recorded")
		}
	}
}

func TestAnExplorerWithNoServiceIsUnavailableNotFailed(t *testing.T) {
	c := &stub{}
	d := deps{client: c, tools: func() (string, error) {
		return "", contract.Fail(contract.FailureUnavailable, "no atenea service is listening at /tmp/atenea.sock")
	}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, d)

	if got.Verdict != "incomplete" {
		t.Errorf("verdict = %q, want incomplete: the explorer did not do badly, it could not run", got.Verdict)
	}
	if got.Reason == nil || got.Reason.Kind != "unavailable" {
		t.Errorf("reason = %+v, want kind unavailable", got.Reason)
	}
	if c.calls != 0 {
		t.Error("the model was called with no capabilities to reach")
	}
}

func TestAnEmptyExplorationIsRefused(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary": "", "findings": "",
	})}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", got.Verdict)
	}
}

func TestAnAnswerInTheWrongShapeIsRefusedAndKeepsItsCharge(t *testing.T) {
	c := &stub{answer: model.Answer{
		Structured: json.RawMessage(`{"summary": 3}`),
		Spent:      charged(),
	}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", got.Verdict)
	}
	if got.Spent == nil {
		t.Error("a refused answer threw away the charge the turn really cost")
	}
}

func TestATurnThatDiedStillReportsWhatItSpent(t *testing.T) {
	c := &stub{
		answer: model.Answer{Spent: charged()},
		err:    contract.Fail(contract.FailureTimeout, "the model did not answer within 30s"),
	}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict == "ok" {
		t.Fatal("a died turn answered ok")
	}
	if got.Spent == nil || got.Spent.USD == nil || *got.Spent.USD != 0.03 {
		t.Errorf("spent = %+v: a turn that occupied the machine for a minute cost that money", got.Spent)
	}
}

// ---- the planning half ----------------------------------------------------

func TestAPlanIsReturnedAsTOMLText(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"plan": "[[step]]\nid = \"read-a\"\n",
	})}}
	got := plan(context.Background(), planAssignment("ok", exploration()), config.Config{}, withTools(c))

	if got.Verdict != "ok" {
		t.Fatalf("verdict = %q (%s), want ok", got.Verdict, reasonOf(got))
	}
	if !strings.Contains(got.Result[PlanField].(string), "[[step]]") {
		t.Errorf("plan = %v", got.Result[PlanField])
	}
}

func TestPlanningWithoutAnExplorationIsUnavailable(t *testing.T) {
	c := &stub{}
	in := planAssignment("ok", exploration())
	in.Subject = nil
	got := plan(context.Background(), in, config.Config{}, withTools(c))

	if got.Verdict != "incomplete" {
		t.Errorf("verdict = %q, want incomplete", got.Verdict)
	}
	if c.calls != 0 {
		t.Error("the model was asked to plan from nothing")
	}
}

func TestPlanningOnARejectedExplorationIsRefusedBeforeTheModel(t *testing.T) {
	c := &stub{}
	got := plan(context.Background(), planAssignment("failed", nil), config.Config{}, withTools(c))

	if got.Verdict == "ok" {
		t.Fatal("a plan was built on an exploration that said it fell short")
	}
	if c.calls != 0 {
		t.Error("the model was paid to plan from a finding nobody accepted")
	}
	if got.Reason == nil || !strings.Contains(got.Reason.Text, "failed") {
		t.Errorf("reason = %+v, want it to name the exploration's verdict", got.Reason)
	}
}

func TestAnEmptyPlanIsRefused(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{"plan": "   "})}}
	got := plan(context.Background(), planAssignment("ok", exploration()), config.Config{}, withTools(c))

	if got.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", got.Verdict)
	}
}

func TestThePlannerIsToldWhatTheExplorationFound(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{"plan": "[[step]]"})}}
	plan(context.Background(), planAssignment("ok", exploration()), config.Config{}, withTools(c))

	if !strings.Contains(c.seen.Prompt, "config.Load reads atenea.toml") {
		t.Error("the planner's prompt does not carry the exploration it is planning from")
	}
}

// ---- the grant ------------------------------------------------------------

func TestTheGrantedShareReachesTheModel(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary": "s", "findings": "f",
	})}}
	in := exploreAssignment()
	in.BudgetUSD = usd(0.75)
	explore(context.Background(), in, config.Config{}, withTools(c))

	if c.seen.BudgetUSD != 0.75 {
		t.Errorf("budget = %v, want the step's share", c.seen.BudgetUSD)
	}
}

func TestAnUngrantedRunPassesNoCeiling(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary": "s", "findings": "f",
	})}}
	in := exploreAssignment() // BudgetUSD nil: nobody granted money
	explore(context.Background(), in, config.Config{}, withTools(c))

	if c.seen.BudgetUSD != 0 {
		t.Errorf("budget = %v, want 0 meaning no ceiling", c.seen.BudgetUSD)
	}
}

// A share of nothing is not freedom. run refuses it before it builds a client,
// so the two readings of zero never collapse into one.
func TestAShareOfNothingIsRefusedAndNoModelIsCalled(t *testing.T) {
	in := exploreAssignment()
	in.BudgetUSD = usd(0)
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	c := &stub{}
	var out strings.Builder
	if err := run(context.Background(), strings.NewReader(string(raw)), &out,
		func(_ context.Context, _ assignment, _ config.Config, _ deps) report {
			t.Fatal("a turn ran on a grant of nothing")
			return report{}
		}); err != nil {
		t.Fatalf("run: %v", err)
	}

	var got report
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("the answer is not readable: %v", err)
	}
	if got.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", got.Verdict)
	}
	if got.Reason == nil || got.Reason.Kind != "permission_denied" {
		t.Errorf("reason = %+v, want permission_denied", got.Reason)
	}
	if c.calls != 0 {
		t.Error("the model was called on a grant of nothing")
	}
}

func reasonOf(r report) string {
	if r.Reason == nil {
		return ""
	}
	return r.Reason.Text
}
