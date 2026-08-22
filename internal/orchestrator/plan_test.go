package orchestrator_test

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestRunPlanExecutesMultipleCapabilitiesInDependencyOrder(t *testing.T) {
	runner := &fakeRunner{}
	agent, _ := build(t, runner, 2, t.TempDir())
	permission := contract.Permission{
		Task:    "review login",
		Effects: []contract.Effect{contract.EffectRead},
	}
	plan := contract.Plan{
		Task: "review login",
		Steps: []contract.Step{
			{ID: "search-first", Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "login"}, Permission: permission},
			{ID: "search-second", Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "session"}, Needs: []string{"search-first"},
				Permission: permission},
		},
	}

	result, err := agent.RunPlan(t.Context(), plan, 0)
	if err != nil {
		t.Fatalf("RunPlan: %v", err)
	}
	if result.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %s, want ok", result.Verdict)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(result.Steps))
	}
	if result.Steps[0].Step.ID != "search-first" || result.Steps[1].Step.ID != "search-second" {
		t.Fatalf("execution order = %s, %s", result.Steps[0].Step.ID, result.Steps[1].Step.ID)
	}
	if len(result.Phases) != 1 || result.Phases[0].Name != orchestrator.PhaseWork {
		t.Fatalf("phases = %#v, want one work phase", result.Phases)
	}
}
