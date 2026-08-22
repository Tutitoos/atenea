package orchestrator_test

import (
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func BenchmarkRunPlanConcurrentMediumDAG(b *testing.B) {
	runner := &fakeRunner{}
	agent, _ := build(b, runner, 4, b.TempDir())
	permission := contract.Permission{Task: "load benchmark", Effects: []contract.Effect{contract.EffectRead}}
	steps := make([]contract.Step, 0, 32)
	for layer := range 4 {
		for slot := range 8 {
			step := contract.Step{
				ID:         "load" + string(rune('a'+layer)) + string(rune('a'+slot)),
				Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "login"}, Permission: permission,
			}
			if layer > 0 {
				step.Needs = []string{"load" + string(rune('a'+layer-1)) + string(rune('a'+slot))}
			}
			steps = append(steps, step)
		}
	}
	plan := contract.Plan{Task: "load benchmark", Steps: steps}
	if err := plan.Validate(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		result, err := agent.RunPlan(b.Context(), plan, 0)
		if err != nil || result.Verdict != contract.VerdictOK {
			b.Fatalf("RunPlan: result=%v err=%v", result, err)
		}
	}
}
