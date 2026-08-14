package workflow_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The STATE column for a partial step names its coverage; a full ok never
// does. A reader scanning the column must not mistake one for the other.
func TestRunStateOfAPartialStepIsDistinctFromAFullOK(t *testing.T) {
	completeness := 0.55
	run := workflow.Run{
		Steps: []workflow.StepRow{
			{
				Step:    workflow.Step{ID: "whole"},
				Status:  workflow.StatusOK,
				Verdict: contract.VerdictOK,
				Result:  map[string]any{"found": "all of it"},
			},
			{
				Step:         workflow.Step{ID: "half"},
				Status:       workflow.StatusOK,
				Verdict:      contract.VerdictOK,
				Result:       map[string]any{"found": "half of it"},
				Completeness: &completeness,
				StoppedAt:    "the last two files",
			},
		},
	}

	whole := run.State(run.Steps[0])
	half := run.State(run.Steps[1])
	if whole != "ok" {
		t.Fatalf("State(whole) = %q, want plain %q", whole, "ok")
	}
	if half == whole {
		t.Fatalf("State(half) = %q, same as a full ok: a partial answer must read differently", half)
	}
	if !strings.Contains(half, "0.55") {
		t.Fatalf("State(half) = %q, want the coverage figure in it", half)
	}
}

// A step whose report never claimed a completeness figure reads as a plain
// label, same as before this field existed.
func TestRunStateOfAStepWithNoCompletenessClaimIsThePlainLabel(t *testing.T) {
	run := workflow.Run{
		Steps: []workflow.StepRow{
			{Step: workflow.Step{ID: "a"}, Status: workflow.StatusFailed, Verdict: contract.VerdictFailed},
		},
	}
	if got := run.State(run.Steps[0]); got != "failed" {
		t.Fatalf("State = %q, want %q", got, "failed")
	}
}
