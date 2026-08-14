package workflow

import (
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// A partial answer -- VerdictOK with Completeness below 1 -- still yields
// StatusOK from outcome. See the WHY comment on outcome's VerdictOK case:
// Requirement.satisfiedBy and every gate keyed on OnOK or OnAnswered read
// StatusOK, and a partial answer is an answer, not the absence of one.
func TestOutcomeOfAPartialReportIsStatusOK(t *testing.T) {
	completeness := 0.55
	report := contract.Report{
		Result:       map[string]any{"found": "half of it"},
		Verdict:      contract.VerdictOK,
		Completeness: &completeness,
		StoppedAt:    "the last two files, cut off by the read budget",
	}
	if got := outcome(report, nil); got != StatusOK {
		t.Fatalf("outcome = %s, want %s: a partial answer is still an answer", got, StatusOK)
	}
}

// A whole answer -- no completeness claim at all -- is the ordinary case
// this behavior must not disturb.
func TestOutcomeOfAWholeReportIsStatusOK(t *testing.T) {
	report := contract.Report{Result: map[string]any{"found": "all of it"}, Verdict: contract.VerdictOK}
	if got := outcome(report, nil); got != StatusOK {
		t.Fatalf("outcome = %s, want %s", got, StatusOK)
	}
}
