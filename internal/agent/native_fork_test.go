package agent_test

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func nativeForkDispatch(role string, effects ...contract.Effect) agent.Dispatch {
	parent := contract.RootAssignment("coordinator", "atenea-coordinator", contract.AgentOrchestrator,
		contract.Task{Objective: "coordinate"}, contract.Limits{})
	parent.Route = &contract.Route{ThreadID: "parent-thread"}
	return agent.Dispatch{
		Parent: &parent, Effects: effects,
		Route: &contract.Route{
			Model: "gpt-5.6-luna", RequestedModel: "gpt-5.6-luna", Backend: "codex",
			Role: role, VisibilityRequired: true, ParentThreadID: "parent-thread", NativeForkState: "pending",
		},
	}
}

func TestNativeForkRequiresWritePermissionOnlyForImplementer(t *testing.T) {
	runner := &agent.Runner{}
	if _, err := runner.PrepareNativeChild(t.Context(), nativeForkDispatch("implement", contract.EffectRead)); err == nil {
		t.Fatal("implement child accepted without write authorization")
	}
	if _, err := runner.PrepareNativeChild(t.Context(), nativeForkDispatch("review", contract.EffectRead, contract.EffectWrite)); err == nil {
		t.Fatal("review child accepted write authorization")
	}
}
