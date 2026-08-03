package claudecode

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// errFakeExitStatus is what os/exec hands back for a child that exited 1, and
// it is exactly what the adapter used to fall back to when the envelope had no
// result: a sentence that names nothing at all.
var errFakeExitStatus = errors.New("exit status 1")

// measuredCeilingEnvelope is what the real client printed, copied from a live
// turn that ran for 54s and stopped at a $0.25 ceiling. The fields that matter
// are the ones NOT here: there is no `result` and no `structured_output` at
// all, which is what broke the reading of this failure.
const measuredCeilingEnvelope = `{
  "type": "result",
  "subtype": "error_max_budget_usd",
  "is_error": true,
  "stop_reason": "tool_use",
  "terminal_reason": "budget_exhausted",
  "errors": ["Reached maximum budget ($0.25)"],
  "num_turns": 9,
  "duration_ms": 54377,
  "total_cost_usd": 0.3540745,
  "permission_denials": [],
  "usage": {
    "input_tokens": 4,
    "output_tokens": 530,
    "cache_read_input_tokens": 12130,
    "cache_creation_input_tokens": 1238
  }
}`

// The bin a spending ceiling belongs in, read off a real envelope.
//
// This is the whole defect in one assertion. The adapter read `result`, which
// this envelope does not have, fell back to the child's exit status — "exit
// status 1", which names nothing — and landed in the catch-all: `unavailable`,
// reported to the user as "claude code did not answer". That is the one bin
// that marks a provider down, so a grant of ours being too small took Claude
// Code out of the funnel and read on screen as a broken client.
func TestASpendingCeilingIsNotTheProviderFailing(t *testing.T) {
	out, err := parse([]byte(measuredCeilingEnvelope))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !out.IsError {
		t.Fatal("the envelope does not even read as an error")
	}
	if out.Result != "" {
		t.Fatal("this fixture is meant to have no result field; it is the point")
	}

	failure := failureFor(out.reason(), errFakeExitStatus)

	if got := contract.KindOf(failure); got != contract.FailurePermissionDenied {
		t.Errorf("kind = %v, want permission_denied: a ceiling of ours is a refusal on this machine, not an outage", got)
	}
	if got := contract.KindOf(failure); got == contract.FailureUnavailable {
		t.Error("unavailable marks the provider down, so this would drop it from the funnel")
	}
	if !strings.Contains(failure.Message, "ceiling") {
		t.Errorf("message = %q, want it to name the ceiling so the reader knows what to raise", failure.Message)
	}
}

// The reason is read in a measured order, and each field gets a turn only when
// the ones before it are empty. Subtype comes last for a reason of its own: it
// reports "success" on turns that failed, so consulting it early would call an
// authentication failure a result.
func TestTheReasonComesFromWhicheverFieldHasOne(t *testing.T) {
	cases := []struct {
		name string
		env  envelope
		want string
	}{
		{"a result when there is one", envelope{
			Result: "You are not logged in", Errors: []string{"ignored"},
			TerminalReason: "ignored", Subtype: "ignored",
		}, "You are not logged in"},
		{"errors when there is no result", envelope{
			Errors:  []string{"Reached maximum budget ($0.25)"},
			Subtype: "error_max_budget_usd", TerminalReason: "budget_exhausted",
		}, "Reached maximum budget ($0.25)"},
		{"every error, not just the first", envelope{
			Errors: []string{"first thing", "second thing"},
		}, "first thing; second thing"},
		{"the terminal reason when errors is empty", envelope{
			TerminalReason: "budget_exhausted", Subtype: "error_max_budget_usd",
		}, "budget_exhausted"},
		{"the subtype last of all", envelope{Subtype: "error_max_budget_usd"},
			"error_max_budget_usd"},
		{"nothing at all is a real answer", envelope{}, ""},
		{"whitespace is not a reason", envelope{Result: "   \n ", Subtype: "error_x"},
			"error_x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.env.reason(); got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// An authentication failure is the measurement that decided the order: the
// client reports subtype "success" on a turn that plainly failed. Reading the
// subtype first would bin that as the search working.
func TestAnExpiredLoginIsStillReadFromTheResult(t *testing.T) {
	env := envelope{
		IsError: true,
		Subtype: "success",
		Result:  "Failed to authenticate: OAuth session expired and could not be refreshed",
	}
	if got := contract.KindOf(failureFor(env.reason(), nil)); got != contract.FailureUnavailable {
		t.Errorf("kind = %v, want unavailable: a client nobody is logged into is unreachable from here", got)
	}
}
