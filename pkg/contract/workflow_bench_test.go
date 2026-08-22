package contract_test

import (
	"fmt"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func BenchmarkPlanLayersMediumDAG(b *testing.B) {
	permission := contract.Permission{
		Task:    "benchmark plan",
		Effects: []contract.Effect{contract.EffectRead},
	}
	steps := make([]contract.Step, 0, 256)
	for layer := range 4 {
		for slot := range 64 {
			id := fmt.Sprintf("step%03d", layer*64+slot)
			step := contract.Step{
				ID: id, Capability: "code.search", Repository: "repo",
				Payload: map[string]any{"query": "login"}, Permission: permission,
			}
			if layer > 0 {
				step.Needs = []string{fmt.Sprintf("step%03d", (layer-1)*64+slot)}
			}
			steps = append(steps, step)
		}
	}
	plan := contract.Plan{Task: "benchmark plan", Steps: steps}
	if err := plan.Validate(); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := plan.Layers(); err != nil {
			b.Fatal(err)
		}
	}
}
