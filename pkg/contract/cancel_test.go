package contract_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// The two context errors look the same at a call site -- the work did not
// finish, ctx.Err() is non-nil -- and mean opposite things. Every adapter used
// to sort them by hand, and all three sorted them wrong in the same direction:
// towards blaming the provider.
func TestTheTwoWaysToNotFinishAreDifferentBins(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want contract.FailureKind
	}{
		{"the user changed their mind", context.Canceled, contract.FailureCanceled},
		{"the provider ran out of time", context.DeadlineExceeded, contract.FailureTimeout},
		{"wrapped, as a real call site hands it over",
			fmt.Errorf("dialing: %w", context.Canceled), contract.FailureCanceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contract.StopKind(tc.err); got != tc.want {
				t.Errorf("StopKind = %v, want %v", got, tc.want)
			}
		})
	}
}

// The sentence has to match the bin, because the bin is for scripts and the
// sentence is for the person reading the trace. A canceled call that quotes a
// ceiling nobody reached is the bug as it was reported: ctrl-c after two
// seconds, and the screen said five minutes.
func TestACanceledCallNeverQuotesTheCeiling(t *testing.T) {
	failure := contract.Stopped(context.Canceled, "claude code", 5*time.Minute)

	if failure.Kind != contract.FailureCanceled {
		t.Errorf("kind = %v, want canceled", failure.Kind)
	}
	for _, forbidden := range []string{"longer than", "5m", "timeout"} {
		if strings.Contains(failure.Error(), forbidden) {
			t.Errorf("error = %q, must not contain %q", failure.Error(), forbidden)
		}
	}
	if !strings.Contains(failure.Message, "claude code") {
		t.Errorf("message = %q, want the provider named", failure.Message)
	}
}

// And the other way, because a timeout IS about the provider and the limit it
// missed is the useful part of saying so.
func TestATimeoutStillNamesTheLimitItMissed(t *testing.T) {
	failure := contract.Stopped(context.DeadlineExceeded, "fixture", 90*time.Second)

	if failure.Kind != contract.FailureTimeout {
		t.Errorf("kind = %v, want timeout", failure.Kind)
	}
	if !strings.Contains(failure.Message, "1m30s") {
		t.Errorf("message = %q, want the limit spelled out", failure.Message)
	}
}

// A bin nothing can name is a bin that leaks: every consumer switch would fall
// through to "unspecified", which is the signal reserved for an adapter that
// did not do its job.
func TestTheCanceledBinHasAName(t *testing.T) {
	if got := contract.FailureCanceled.String(); got != "canceled" {
		t.Errorf("name = %q, want canceled", got)
	}
	if contract.FailureCanceled == contract.FailureTimeout {
		t.Error("canceled and timeout are the same value: nothing can tell them apart")
	}
}

// KindOf is what every consumer actually calls, and it has to keep working
// through the wrapping a real error takes on between the adapter and the CLI.
func TestACancellationSurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("step ask-current: %w",
		contract.Stopped(context.Canceled, "omp", time.Minute))

	if got := contract.KindOf(wrapped); got != contract.FailureCanceled {
		t.Errorf("kind = %v, want canceled after wrapping", got)
	}
	var failure *contract.Failure
	if !errors.As(wrapped, &failure) {
		t.Fatal("the failure did not survive as itself")
	}
}
