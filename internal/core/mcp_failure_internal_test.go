package core

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestRefusalPreservesOutputReviewReason(t *testing.T) {
	const reason = "output does not match the capability: unknown output field(s): input"
	result := &orchestrator.Result{Steps: []orchestrator.StepResult{{
		Review: orchestrator.Review{Parent: contract.VerdictFailed, Reason: reason},
	}}}
	if got := refusalOf(result); got != reason {
		t.Fatalf("output failure lost its cause: %q", got)
	}
	result.Steps[0].Review.Reason = ""
	if got := refusalOf(result); got != "the run finished with verdict failed" {
		t.Fatalf("verdict is not readable: %q", got)
	}
	result.Steps[0].Review.Reason = "output enum mismatch: got password=SYNTHETIC-ONLY " + strings.Repeat("x", contract.MaxPersistedRaw+100)
	if got := refusalOf(result); strings.Contains(got, "SYNTHETIC-ONLY") || !strings.Contains(got, "[REDACTED") || len(got) > contract.MaxPersistedRaw+len("\n[TRUNCATED]") {
		t.Fatalf("review diagnostic was not redacted and bounded: length=%d", len(got))
	}
	capability := contract.Capability{ID: "test.output", Outputs: []contract.Field{{Name: "password", Type: contract.TypeString, Enum: []string{"allowed"}}}}
	err := capability.ValidateOutput(map[string]any{"password": "SYNTHETIC-ONLY"})
	result.Steps[0].Review.Reason = "output does not match the capability: " + err.Error()
	if got := refusalOf(result); strings.Contains(got, "SYNTHETIC-ONLY") || !strings.Contains(got, "value omitted") {
		t.Fatal("an unlabelled rejected output value escaped the diagnostic")
	}
}
