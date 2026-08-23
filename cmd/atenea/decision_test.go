package main

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

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
