package claudecode

import (
	"context"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// What a turn that died at its ceiling really printed. The charge is above the
// grant it was given, which is a fact about the client and not a typo here:
// --max-budget-usd stops a turn, it does not cap the bill.
const ranOutOfMoney = `{"is_error":true,"subtype":"error_max_budget_usd",
  "terminal_reason":"budget_exhausted","errors":["Reached maximum budget ($0.25)"],
  "usage":{"input_tokens":4,"output_tokens":530,
           "cache_read_input_tokens":12130,"cache_creation_input_tokens":1238},
  "total_cost_usd":0.3540745,"num_turns":9}`

// A failed turn is still a turn that was paid for.
//
// The adapter used to return an empty outcome beside the error, so everything
// the far side reported about the attempt went in the bin with it. Three things
// broke at once: the measurement base recorded the failure as costing nothing,
// the receipt showed `spent_usd` empty for a call that charged 35 cents, and —
// because the core spends its purse down by what comes back — one commission
// could charge past its whole grant without any of the arithmetic noticing.
func TestAFailedTurnStillReportsWhatItSpent(t *testing.T) {
	runner := billing(t, ranOutOfMoney)
	req := granted(t, 0.25, map[string]any{"query": "TODO"})

	out, err := runner.Run(context.Background(), req)

	if err == nil {
		t.Fatal("a turn that ran out of money came back clean")
	}
	if out.SpentUSD != 0.3540745 {
		t.Errorf("spent_usd = %v, want 0.3540745: the money was charged whatever the verdict", out.SpentUSD)
	}
	if want := 4 + 530 + 12130 + 1238; out.Spent.Tokens != want {
		t.Errorf("tokens = %d, want %d", out.Spent.Tokens, want)
	}
	if out.Spent.Duration <= 0 {
		t.Error("the attempt took no time at all, which cannot be true")
	}
	// And the bin is still the one that says whose decision this was.
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Errorf("kind = %v, want permission_denied", got)
	}
}

// The version of the tool that did the spending travels too. A measurement
// filed against the wrong version quietly averages a new binary's numbers into
// an old one's, and a failure is a measurement like any other.
func TestAFailedTurnIsStillAttributedToAVersion(t *testing.T) {
	runner := billing(t, ranOutOfMoney)

	out, _ := runner.Run(context.Background(), granted(t, 0.25, map[string]any{"query": "TODO"}))

	if strings.TrimSpace(out.ToolVersion) == "" {
		t.Error("nothing says which binary spent the money")
	}
}

// The other half of the same rule, and the one that keeps it honest: a turn
// that was never spawned charges nothing. A refusal before the process exists
// has no weight to report, and inventing one would put time on a bill for work
// that never happened.
func TestARefusalBeforeSpawningWeighsNothing(t *testing.T) {
	runner := billing(t, answered)
	req := granted(t, 0, map[string]any{"query": "TODO"})

	out, err := runner.Run(context.Background(), req)

	if err == nil {
		t.Fatal("an unfunded call was allowed to spawn")
	}
	if out.SpentUSD != 0 || out.Spent.Tokens != 0 {
		t.Errorf("an unspawned turn reported $%v and %d tokens", out.SpentUSD, out.Spent.Tokens)
	}
	if out.Spent.Duration != 0 {
		t.Errorf("an unspawned turn reported %v on the clock", out.Spent.Duration)
	}
}
