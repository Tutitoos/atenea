package contract_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestKindOfSortsWrappedFailures(t *testing.T) {
	err := fmt.Errorf("while selecting: %w",
		contract.Fail(contract.FailureUnavailable, "serena is down"))
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("KindOf = %v, want unavailable", got)
	}
}

// Anything the core did not sort itself lands in the unspecified bin. That is
// the signal that some adapter failed to do its one job.
func TestKindOfUnsortedErrorIsUnspecified(t *testing.T) {
	if got := contract.KindOf(errors.New("boom")); got != contract.FailureUnspecified {
		t.Fatalf("KindOf = %v, want unspecified", got)
	}
}

// Reaching outside the machine is its own bin, never a flavor of the ordinary
// permission error: no undo takes it back, so it must be distinguishable.
func TestExternalDeniedIsItsOwnBin(t *testing.T) {
	if contract.FailureExternalDenied == contract.FailurePermissionDenied {
		t.Fatal("external denial collapsed into the ordinary permission bin")
	}
	if contract.FailureExternalDenied.String() != "external_denied" {
		t.Fatalf("name = %q", contract.FailureExternalDenied.String())
	}
}

// The untranslated provider output survives translation: a human still has to
// be able to search for it verbatim.
func TestWithRawKeepsTheOriginalMessage(t *testing.T) {
	base := contract.Fail(contract.FailureTimeout, "provider took too long")
	withRaw := base.WithRaw("rg: operation timed out after 30s")
	if base.Raw != "" {
		t.Fatal("WithRaw mutated the original failure")
	}
	if withRaw.Raw != "rg: operation timed out after 30s" {
		t.Fatalf("Raw = %q", withRaw.Raw)
	}
	if withRaw.Error() != "timeout: provider took too long" {
		t.Fatalf("Error() = %q", withRaw.Error())
	}
}
