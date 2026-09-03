package contract_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestKindOfSortsWrappedFailures(t *testing.T) {
	err := fmt.Errorf("while selecting: %w",
		contract.Fail(contract.FailureUnavailable, "fixture is down"))
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

// RawOf recovers the same evidence WithRaw attached, through a wrapped error
// exactly like KindOf does -- the two are read together at the one place
// that turns a failure into a step result, and must agree on what "sorted"
// means.
func TestRawOfRecoversWrappedRawText(t *testing.T) {
	err := fmt.Errorf("while selecting: %w",
		contract.Fail(contract.FailureUnavailable, "fixture is down").
			WithRaw("connection refused"))
	if got := contract.RawOf(err); got != "connection refused" {
		t.Fatalf("RawOf = %q, want %q", got, "connection refused")
	}
}

// A failure raised without WithRaw has nothing to recover, and an error the
// core never raised at all -- same case KindOf reports as unspecified -- has
// nothing either. Both must answer "", not panic and not fabricate text.
func TestRawOfWithNoRawTextIsEmpty(t *testing.T) {
	if got := contract.RawOf(contract.Fail(contract.FailureTimeout, "slow")); got != "" {
		t.Fatalf("RawOf = %q, want empty", got)
	}
	if got := contract.RawOf(errors.New("boom")); got != "" {
		t.Fatalf("RawOf = %q, want empty", got)
	}
}

// Every refusal this package raises has to land in a declared bin.
//
// FailureUnspecified is defined as the signal that some adapter failed to do
// its one job, so it is never a category a constructor here may choose. Four
// of the ten Parse functions already returned *Failure and six returned a
// plain fmt.Errorf, which KindOf could only sort as unspecified -- so a
// settings file with one misspelled enum was reported as an internal fault
// rather than as the input mistake it is, and the caller that switches on the
// bin to decide whether to blame the user could not tell them apart.
func TestEveryParseRefusalIsBinnedAsInvalidInput(t *testing.T) {
	refusals := map[string]func() error{
		"effect":          func() error { _, err := contract.ParseEffect("delete"); return err },
		"field type":      func() error { _, err := contract.ParseFieldType("decimal"); return err },
		"scale":           func() error { _, err := contract.ParseScale("huge"); return err },
		"vcs":             func() error { _, err := contract.ParseVCS("maybe"); return err },
		"health state":    func() error { _, err := contract.ParseHealthState("sick"); return err },
		"scope guarantee": func() error { _, err := contract.ParseScopeGuarantee("loose"); return err },
		"failure kind":    func() error { _, err := contract.ParseFailureKind("exploded"); return err },
		"verdict":         func() error { _, err := contract.ParseVerdict("maybe"); return err },
		"agent type":      func() error { _, err := contract.ParseAgentType("wizard"); return err },
		"context level":   func() error { _, err := contract.ParseContextLevel("everything"); return err },
	}
	for name, refuse := range refusals {
		t.Run(name, func(t *testing.T) {
			err := refuse()
			if err == nil {
				t.Fatal("an unknown name was accepted")
			}
			if got := contract.KindOf(err); got != contract.FailureInvalidInput {
				t.Fatalf("KindOf = %v, want invalid_input: %v", got, err)
			}
		})
	}
}
