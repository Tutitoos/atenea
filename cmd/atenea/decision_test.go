package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/decision"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestCmdDecideValidatesCommissionBeforeLoadingSettings(t *testing.T) {
	if err := cmdDecide("", nil, &bytes.Buffer{}); err == nil {
		t.Fatal("cmdDecide accepted an empty commission")
	}
}

func TestGraphForRepositoryKeepsOnlyItsIsolatedSubgraph(t *testing.T) {
	graph := workflow.Graph{Task: "inspect", GrantUSD: 4, Steps: []workflow.Step{
		{ID: "explore-api", TypeName: "explore", Permission: contract.Permission{BudgetUSD: 1}},
		{ID: "plan-api", TypeName: "plan", Needs: []string{"explore-api"}, Subject: "explore-api", Permission: contract.Permission{BudgetUSD: 1}},
		{ID: "explore-web", TypeName: "explore", Permission: contract.Permission{BudgetUSD: 1}},
		{ID: "plan-web", TypeName: "plan", Needs: []string{"explore-web"}, Subject: "explore-web", Permission: contract.Permission{BudgetUSD: 1}},
	}}

	got := graphForRepository(graph, "api")
	if len(got.Steps) != 2 || got.Steps[1].Needs[0] != "explore-api" || got.Steps[1].Subject != "explore-api" {
		t.Fatalf("api graph = %+v, want its dependent pair", got)
	}
	if got.GrantUSD != 2 {
		t.Fatalf("api grant = %.2f, want 2", got.GrantUSD)
	}
}

func TestGraphForRepositoryDropsForeignDependenciesAndSubjects(t *testing.T) {
	graph := workflow.Graph{GrantUSD: 3, Steps: []workflow.Step{
		{ID: "explore-api", TypeName: "explore", Permission: contract.Permission{BudgetUSD: 1}},
		{ID: "plan-api", TypeName: "plan", Needs: []string{"explore-api", "explore-web", "missing"}, Subject: "explore-web", Permission: contract.Permission{BudgetUSD: 1}},
	}}

	got := graphForRepository(graph, "api")
	if len(got.Steps) != 2 || len(got.Steps[1].Needs) != 1 || got.Steps[1].Needs[0] != "explore-api" {
		t.Fatalf("filtered graph = %+v, want only the local dependency", got)
	}
	if got.Steps[1].Subject != "" || got.GrantUSD != 2 {
		t.Fatalf("filtered graph subject/grant = %q/%.2f, want empty/2", got.Steps[1].Subject, got.GrantUSD)
	}
}

func TestDecisionPresentationAndConfirmationGuards(t *testing.T) {
	plan := decision.Plan{
		Intent:       decision.KindPlan,
		Agent:        "plan",
		Repositories: []string{"repo"},
		Effects:      []contract.Effect{contract.EffectRead, contract.EffectWrite},
		Models:       []decision.ModelChoice{{Role: "plan", Backend: "claude", Name: "opus", Fallbacks: []string{"fallback"}, Reason: "missing"}},
		Tools:        []decision.ToolChoice{{ID: "raw.docs.query", Kind: "raw", Selected: false, Reason: "not selected"}},
		Capabilities: []decision.CapabilityChoice{
			{ID: "code.search", Repository: "repo", Providers: []string{"ripgrep"}, Reason: "candidate"},
			{ID: "symbol.definition", Repository: "repo", Unavailable: true, Reason: "offline"},
		},
		Budget: decision.BudgetSummary{GrantedUSD: 1, RequiredUSD: 2, MinimumUSD: 1, MarginUSD: -1},
		Workflow: workflow.Graph{Steps: []workflow.Step{{
			ID: "explore-repo", TypeName: "explore", Permission: contract.Permission{BudgetUSD: 1, Effects: []contract.Effect{contract.EffectRead}},
		}}},
		Reasons: []decision.Reason{{Stage: "intent", Message: "classified"}},
	}

	var out bytes.Buffer
	printDecisionPlan(&out, plan, true)
	if !strings.Contains(out.String(), "unavailable") || !strings.Contains(out.String(), "reasons") || !strings.Contains(out.String(), "insufficient") {
		t.Fatalf("decision output = %q, want complete diagnostic sections", out.String())
	}
	var jsonOut bytes.Buffer
	if err := printDecisionJSON(&jsonOut, plan); err != nil || !strings.Contains(jsonOut.String(), `"intent": "plan"`) {
		t.Fatalf("decision json = %q, err=%v", jsonOut.String(), err)
	}

	if requiresDecisionConfirmation(decision.Plan{}, "") {
		t.Fatal("empty plan should not require confirmation")
	}
	if !requiresDecisionConfirmation(plan, "") || !requiresDecisionConfirmation(decision.Plan{}, "raw.docs.query") {
		t.Fatal("write effects and raw tools should require confirmation")
	}
	if got := effectNames([]contract.Effect{contract.EffectRead, contract.EffectWrite}); len(got) != 2 || got[0] != "read" {
		t.Fatalf("effect names = %v", got)
	}
}
