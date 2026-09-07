package workflow

import (
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestExplicitRedoDoesNotConsumeAutomaticRetryLimit(t *testing.T) {
	base := Run{
		Policy:     WorkflowPolicy{Version: "workflow-v1", MaxRetries: 1},
		Steps:      []StepRow{{Step: Step{ID: "read", Route: &contract.Route{RequestedModel: "model", Backend: "fixture"}}, Attempt: 2, RecoveryKind: "redo"}},
		Superseded: []AttemptRow{{StepID: "read", Attempt: 1, RecoveryKind: "redo", Status: StatusIncomplete}},
	}
	if retryLimitReached(base, nil, 1) {
		t.Fatal("explicit redo consumed the automatic retry limit")
	}

	base.Steps[0].RecoveryKind = "automatic_recovery"
	base.Superseded[0].RecoveryKind = "automatic_recovery"
	if !retryLimitReached(base, nil, 1) {
		t.Fatal("automatic recovery did not consume the persisted retry limit")
	}
}

func TestRecoveryStateKeepsOriginPerStep(t *testing.T) {
	run := Run{
		Policy: WorkflowPolicy{MaxRetries: 2},
		Steps: []StepRow{{
			Step:   Step{ID: "read", Route: &contract.Route{RequestedModel: "model", Backend: "fixture"}},
			Status: StatusOK, Attempt: 2, RecoveryKind: "redo", RecoveryReason: "explicit redo authorization",
		}},
		Superseded: []AttemptRow{{StepID: "read", Attempt: 1, RecoveryKind: "redo", RecoveryReason: "explicit redo authorization"}},
	}
	state := run.RecoveryState()
	if state.RetryUsed != 0 || len(state.Steps) != 1 || state.Steps[0].RecoveryKind != "redo" {
		t.Fatalf("state=%+v, explicit redo must remain outside automatic retries", state)
	}
}
