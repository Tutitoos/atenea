package core

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestWorkflowSnapshotUsesStableStringsAndUnlaunchedState(t *testing.T) {
	asked := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	run := workflow.Run{
		ID: "wf-test", Task: "inspect", Repository: "repo", GrantUSD: 2,
		Steps: []workflow.StepRow{{
			Step: workflow.Step{ID: "read", TypeName: "explore",
				Task: contract.Task{Objective: "read"}, Permission: contract.Permission{Effects: []contract.Effect{contract.EffectRead}}},
			Status:  workflow.StatusPending,
			Notices: []string{"partial result retained"},
		}},
	}
	gate := workflow.Gate{RunID: run.ID, Ordinal: 0, Kind: workflow.KindLaunch,
		Digest: "full-digest", Decision: workflow.DecisionWaiting, Asked: asked}
	snapshot := workflowSnapshot(run, []workflow.Gate{gate})
	if snapshot["state"] != "unlaunched" || snapshot["repository"] != "repo" || snapshot["writer_pid"] != 0 || snapshot["ownership"] != "none" {
		t.Fatalf("snapshot metadata = %v", snapshot)
	}
	steps, ok := snapshot["steps"].([]map[string]any)
	if !ok || len(steps) != 1 || len(steps[0]["notices"].([]string)) != 1 {
		t.Fatalf("step notices = %#v, want one durable notice", snapshot["steps"])
	}
	spend, ok := snapshot["spend"].(map[string]any)
	if !ok || spend["unknown_steps"] != 1 || spend["observed_steps"] != 0 || spend["estimated_steps"] != 0 {
		t.Fatalf("spend = %#v, want unknown unlaunched step", snapshot["spend"])
	}
	gates, ok := snapshot["gates"].([]map[string]any)
	if !ok || len(gates) != 1 {
		t.Fatalf("gates = %#v, want one stable DTO", snapshot["gates"])
	}
	for _, key := range []string{"kind", "decision", "digest", "asked_at", "answered_at", "hand", "reason"} {
		if _, ok := gates[0][key].(string); !ok {
			t.Errorf("gate %s = %#v, want string", key, gates[0][key])
		}
	}
	if gates[0]["kind"] != "launch" || gates[0]["decision"] != "waiting" || gates[0]["digest"] != "full-digest" {
		t.Errorf("gate DTO = %#v", gates[0])
	}
}

func TestWorkflowToolResultLeadsWithDeterministicPlanMarkdown(t *testing.T) {
	point := workflow.PlanPoint{ID: "P06", Title: "Visible progress", State: workflow.PointAccepted}
	snapshot := workflowSnapshot(workflow.Run{ID: "wf-plan", PlanRevision: 2, Points: []workflow.PlanPoint{point}}, nil)
	result, rpcErr := toolResult(snapshot)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	payload := result.(map[string]any)
	content := payload["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content blocks = %d, want markdown plus JSON", len(content))
	}
	markdown := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(markdown, "[x] **P06.**") || !strings.Contains(markdown, "100 % · 1/1") {
		t.Fatalf("first content block = %q", markdown)
	}
}

func TestWorkflowSnapshotAccumulatesSupersededSpendByCertainty(t *testing.T) {
	live := 0.30
	oldObserved := 0.40
	oldEstimated := 0.20
	run := workflow.Run{
		ID: "wf-cost", GrantUSD: 3,
		Steps: []workflow.StepRow{{
			Step:   workflow.Step{ID: "work", TypeName: "reader"},
			Status: workflow.StatusOK,
			Spent:  contract.Charge{USD: &live, PricedBy: "provider"},
		}},
		Superseded: []workflow.AttemptRow{
			{StepID: "work", Attempt: 1, Spent: contract.Charge{USD: &oldObserved, PricedBy: "provider"}},
			{StepID: "work", Attempt: 2, Spent: contract.Charge{USD: &oldEstimated, PricedBy: "estimate:allowance"}},
			{StepID: "work", Attempt: 3, Spent: contract.Charge{InputTokens: 20}},
		},
	}

	spend, ok := workflowSnapshot(run, nil)["spend"].(map[string]any)
	if !ok {
		t.Fatal("snapshot has no spend object")
	}
	if spend["observed_steps"] != 2 || spend["estimated_steps"] != 1 || spend["unknown_steps"] != 1 {
		t.Fatalf("classified spend = %#v, want accumulated observed=2 estimated=1 unknown=1", spend)
	}
	supersededUSD, usdOK := spend["superseded_usd"].(float64)
	if spend["superseded_attempts"] != 3 || !usdOK || math.Abs(supersededUSD-0.60) > 1e-9 {
		t.Fatalf("superseded spend = %#v, want 3 attempts and $0.60", spend)
	}
	if spend["usd"] != (*float64)(nil) {
		t.Fatalf("usd = %#v, want nil while an archived attempt is unknown", spend["usd"])
	}
	if got, ok := spend["observed_usd"].(*float64); !ok || *got != 0.70 {
		t.Fatalf("observed_usd = %#v, want $0.70", spend["observed_usd"])
	}
	if got, ok := spend["estimated_usd"].(*float64); !ok || *got != 0.20 {
		t.Fatalf("estimated_usd = %#v, want $0.20", spend["estimated_usd"])
	}
}
