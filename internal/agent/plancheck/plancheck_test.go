package plancheck

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

// settings declares the two agent types the plans in this file name. A plan
// naming anything else is the unknown-type case, which is one of the refusals
// under test.
const settings = `
contract = "3.0.0"

[[agent]]
name = "reader"
kind = "specialized"
summary = "Reads a file"
command = "/bin/true"
context = ["repository"]
effects = ["read"]
max_duration = "30s"
max_tokens = 1

  [[agent.result]]
  name = "ok"
  type = "bool"
  required = true
  summary = "it read"

[[agent]]
name = "auditor"
kind = "specialized"
pool = "review"
summary = "Audits an answer"
command = "/bin/true"
context = ["repository"]
effects = ["read"]
max_duration = "30s"
max_tokens = 1

  [[agent.result]]
  name = "ok"
  type = "bool"
  required = true
  summary = "it holds"
`

func loaderFor(t *testing.T) loader {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return func(string) (config.Config, error) { return config.Load(path) }
}

// planned wraps a plan the way the review machinery hands it over: the
// planner's finished attempt, as a subject.
func planned(plan string) assignment {
	return assignment{
		Subject: &subject{
			RunID:   "run-1",
			Type:    "plan",
			Attempt: 1,
			Verdict: "ok",
			Result:  map[string]any{PlanField: plan},
		},
	}
}

const goodPlan = `
task = "read a file and have the answer audited"
budget_usd = 1.00

[[step]]
id = "read-a"
agent = "reader"
objective = "read a.txt"
criterion = "the counts match"
effects = ["read"]
budget_usd = 0.25

[[step]]
id = "audit-a"
agent = "auditor"
subject = "read-a"
objective = "audit what the reader said"
criterion = "the answer holds"
effects = ["read"]
budget_usd = 0.25
`

// soloPlan has one step and no reviewer, for the refusals that are about
// edges rather than about who may review whom.
const soloPlan = `
task = "read a file"
budget_usd = 1.00

[[step]]
id = "read-a"
agent = "reader"
objective = "read a.txt"
criterion = "the counts match"
effects = ["read"]
budget_usd = 0.25
`

// A graph the engine would accept is approved, and the reviewer says what it
// approved rather than just "ok".
func TestACompilingPlanIsApproved(t *testing.T) {
	got := judge(planned(goodPlan), loaderFor(t))

	if got.Verdict != "ok" {
		t.Fatalf("verdict = %q (%+v), want ok", got.Verdict, got.Reason)
	}
	if got.Result["steps"] != 2 {
		t.Errorf("steps = %v, want the two this plan declares", got.Result["steps"])
	}
	// The run it audited, named on the card: an approval nobody can trace
	// back to a plan is an approval of nothing in particular.
	if got.Result["subject"] != "run-1" {
		t.Errorf("subject = %v, want the audited run", got.Result["subject"])
	}
}

// Every refusal a planner can earn, and the sentence it is handed back. The
// text matters as much as the verdict: it is what the second attempt reads,
// and a planner told only "invalid" writes the same graph again.
func TestEveryCompileRefusalReachesThePlannerAsItsOwnSentence(t *testing.T) {
	load := loaderFor(t)
	for _, probe := range []struct {
		name string
		plan string
		want string
	}{
		{
			name: "an agent type nobody declared",
			plan: strings.Replace(goodPlan, `agent = "auditor"`, `agent = "revewer"`, 1),
			want: `no agent type "revewer"`,
		},
		{
			name: "a step waiting on itself",
			plan: strings.Replace(soloPlan, `id = "read-a"`, `id = "read-a"`+"\n"+`needs = ["read-a"]`, 1),
			want: "waits on itself",
		},
		{
			name: "an edge to a step that does not exist",
			plan: strings.Replace(soloPlan, `id = "read-a"`, `id = "read-a"`+"\n"+`needs = ["ghost"]`, 1),
			want: `waits on "ghost", which no step declares`,
		},
		{
			name: "shares past the grant",
			plan: strings.Replace(goodPlan, "budget_usd = 0.25", "budget_usd = 0.90", 2),
			want: "shares add up",
		},
		{
			name: "text that is not TOML at all",
			plan: "I have written you a lovely plan.",
			want: "workflow the plan:",
		},
		{
			name: "a key the format does not have",
			plan: goodPlan + "\nwhen = \"tuesday\"\n",
			want: "unknown key",
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			got := judge(planned(probe.plan), load)
			if got.Verdict != "failed" {
				t.Fatalf("verdict = %q, want failed: a graph the engine refuses is a wrong answer", got.Verdict)
			}
			if got.Reason == nil || !strings.Contains(got.Reason.Text, probe.want) {
				t.Errorf("sentence = %q, want it to carry %q", reasonOf(got), probe.want)
			}
		})
	}
}

// A cycle is the refusal that cannot be seen by reading one step, so it gets
// its own case.
func TestACycleIsRefused(t *testing.T) {
	plan := `
task = "a graph that never drains"
budget_usd = 1.00

[[step]]
id = "a"
agent = "reader"
objective = "read"
criterion = "read"
effects = ["read"]
needs = ["b"]

[[step]]
id = "b"
agent = "reader"
objective = "read"
criterion = "read"
effects = ["read"]
needs = ["a"]
`
	got := judge(planned(plan), loaderFor(t))
	if got.Verdict != "failed" {
		t.Fatalf("verdict = %q, want failed", got.Verdict)
	}
	if !strings.Contains(reasonOf(got), "cycle") {
		t.Errorf("sentence = %q, want it to name the cycle", reasonOf(got))
	}
}

// An answer with no plan in it is the planner failing at the one thing it is
// for, so it is refused rather than excused.
func TestAnAnswerWithNoPlanIsRefused(t *testing.T) {
	in := planned("")
	in.Subject.Result = map[string]any{"thoughts": "I considered several options"}

	got := judge(in, loaderFor(t))
	if got.Verdict != "failed" {
		t.Fatalf("verdict = %q, want failed", got.Verdict)
	}
	if !strings.Contains(reasonOf(got), PlanField) {
		t.Errorf("sentence = %q, want it to name the missing field", reasonOf(got))
	}
}

// A planner that reported its own failure is not re-litigated, and its own
// sentence survives instead of being replaced by a parse error about nothing.
func TestAPlannerThatFailedItselfKeepsItsOwnSentence(t *testing.T) {
	in := planned("")
	in.Subject.Verdict = "failed"
	in.Subject.Result = map[string]any{}
	in.Subject.Reason = &reason{Kind: "unavailable", Text: "the model never answered"}

	got := judge(in, loaderFor(t))
	if got.Verdict != "failed" {
		t.Fatalf("verdict = %q, want failed", got.Verdict)
	}
	if !strings.Contains(reasonOf(got), "the model never answered") {
		t.Errorf("sentence = %q, want the planner's own reason", reasonOf(got))
	}
}

// Settings that cannot be read are this reviewer's shortfall, not the
// planner's mistake. Calling it `failed` would earn a relaunch of a planner
// that did nothing wrong; calling it `ok` would approve a graph nobody
// compiled.
func TestUnreadableSettingsAreIncompleteRatherThanARefusal(t *testing.T) {
	broken := func(string) (config.Config, error) {
		return config.Load(filepath.Join(t.TempDir(), "nothing", "atenea.toml"))
	}
	got := judge(planned(goodPlan), broken)
	if got.Verdict != "incomplete" {
		t.Fatalf("verdict = %q, want incomplete", got.Verdict)
	}
}

// A review with no subject has nothing to look at, which is a shortfall of
// the dispatch rather than a judgement about a plan.
func TestAReviewWithNoSubjectIsIncomplete(t *testing.T) {
	if got := judge(assignment{}, loaderFor(t)); got.Verdict != "incomplete" {
		t.Fatalf("verdict = %q, want incomplete", got.Verdict)
	}
}

// The process is the contract: one JSON object in, one JSON object out, exit
// zero even when the verdict refuses.
func TestTheProcessAnswersOnStdout(t *testing.T) {
	in := planned(goodPlan)
	payload, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	t.Setenv("ATENEA_CONFIG", path)

	var out bytes.Buffer
	if err := Main(bytes.NewReader(payload), &out); err != nil {
		t.Fatalf("Main: %v", err)
	}
	var got report
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("the answer is not readable: %v", err)
	}
	if got.Verdict != "ok" {
		t.Errorf("verdict = %q (%+v), want ok", got.Verdict, got.Reason)
	}
}

func reasonOf(r report) string {
	if r.Reason == nil {
		return ""
	}
	return r.Reason.Text
}
