package decision

import "testing"

func TestRulesModeUsesTheCurrentRequestWhenContextHasAnObjective(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
		want Kind
	}{
		{name: "change", text: "Implement the fix now.", want: KindChange},
		{name: "understand", text: "Explain the current search flow.", want: KindUnderstand},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := (Planner{Config: fixtureConfig("repo")}).Build(Request{
				Text: test.text, Repository: "repo", BudgetUSD: 10,
				Context: &IntentContext{Version: 1, Repository: "repo", ActiveObjective: "preserve tenant boundaries"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Intent != test.want || !plan.ContextUsed {
				t.Fatalf("contextual plan intent/context = %s/%t, want %s/true", plan.Intent, plan.ContextUsed, test.want)
			}
		})
	}
}

func TestAcceptedPlanContinuationUsesVerifiedContext(t *testing.T) {
	plan, err := (Planner{Config: fixtureConfig("repo")}).BuildContext(t.Context(), Request{
		Text: "hazlo", Repository: "repo", BudgetUSD: 10,
		Context: &IntentContext{
			Version: 1, Repository: "repo", AcceptedPlanID: "plan-123", AcceptedPlanRevision: "rev-1",
			AcceptedPlanCurrent: true, ActiveObjective: "implement the requested feature",
			ScopeFiles: []string{"internal/feature.go"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != KindChange || plan.Resolution != ResolutionResolved || !plan.ContextUsed ||
		plan.IntentEvidence.Mode != "rules" || plan.IntentEvidence.Source != "context" {
		t.Fatalf("continuation intent/evidence = %s/%+v", plan.Intent, plan.IntentEvidence)
	}
}
