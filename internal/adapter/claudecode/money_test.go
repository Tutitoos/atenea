package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// stub stands in for the real binary: one turn, one envelope on stdout, no
// login and no network. It is the only way to exercise what Run assembles
// after the answer comes back -- everything from the spawn to the outcome --
// without a machine that happens to be logged in.
func stub(t *testing.T, stdout string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ncat <<'ENVELOPE'\n" + stdout + "\nENVELOPE\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}
	return path
}

// billing builds the adapter over a stub binary. There is no ceiling here on
// purpose: this adapter no longer has one. What a call may spend arrives on
// the request, which is what `granted` sets.
func billing(t *testing.T, stdout string) *Runner {
	t.Helper()
	runner, err := New(Options{
		Binary:          stub(t, stdout),
		Implementations: []string{"claude.search"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner
}

// granted is a commission that may spend usd, cut down to this one step.
func granted(t *testing.T, usd float64, payload map[string]any) contract.RunRequest {
	t.Helper()
	req := request(t, payload)
	req.Permission.BudgetUSD = usd
	return req
}

const answered = `{"is_error":false,"subtype":"success",
  "structured_output":{"matches":[{"path":"cmd/main.go","line":3,"column":1}]},
  "usage":{"input_tokens":120,"output_tokens":40},
  "total_cost_usd":0.0234,"num_turns":2}`

// What the far side charged travels back as a number of its own. A charge that
// only ever appeared inside a sentence could be read by a human and by nothing
// else, and the receipt would have to be re-derived by parsing prose.
func TestTheChargeComesBackAsANumber(t *testing.T) {
	runner := billing(t, answered)
	req := granted(t, 0.25, map[string]any{"query": "TODO"})

	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.SpentUSD != 0.0234 {
		t.Errorf("SpentUSD = %v, want 0.0234", out.SpentUSD)
	}
	// The measured axes are unmoved: money is beside them, never inside them.
	if out.Spent.Tokens != 160 {
		t.Errorf("tokens = %d, want 160 -- the charge leaked into the baseline", out.Spent.Tokens)
	}
}

// The charge is reported against the ceiling it was drawn from. On its own a
// figure says nothing; next to the grant it says how close this call came to
// not answering at all, which is worth knowing before the next one fails.
func TestTheChargeIsReportedAgainstItsCeiling(t *testing.T) {
	runner := billing(t, answered)
	req := granted(t, 0.25, map[string]any{"query": "TODO"})

	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var note string
	for _, found := range out.Discoveries {
		if strings.Contains(found.Note, "$") {
			note = found.Note
		}
	}
	if note == "" {
		t.Fatal("a paid call reported no charge at all")
	}
	if !strings.Contains(note, "$0.0234") {
		t.Errorf("note does not say what was spent: %q", note)
	}
	if !strings.Contains(note, "$0.25") {
		t.Errorf("note does not say what the ceiling was: %q", note)
	}
}

// A free turn says nothing about money. The adapter reports what happened, and
// on a turn that cost nothing a line about dollars is noise that trains the
// reader to skip the line that matters.
func TestAFreeTurnReportsNoCharge(t *testing.T) {
	free := strings.Replace(answered, `"total_cost_usd":0.0234`, `"total_cost_usd":0`, 1)
	runner := billing(t, free)
	req := granted(t, 0.25, map[string]any{"query": "TODO"})

	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.SpentUSD != 0 {
		t.Errorf("SpentUSD = %v, want 0", out.SpentUSD)
	}
	for _, found := range out.Discoveries {
		if strings.Contains(found.Note, "$") {
			t.Errorf("a free turn reported a charge: %q", found.Note)
		}
	}
}

// The share the core cut reaches the binary. A grant nobody enforced would
// make the whole report a decoration: the number on the receipt only means
// something if it is the number the far side was actually held to.
//
// It is the request's figure, not a number this adapter kept, which is the
// whole difference between a ceiling per call and a ceiling per commission.
func TestTheGrantedShareIsPassedToTheBinary(t *testing.T) {
	runner := billing(t, answered)
	req := granted(t, 0.0625, map[string]any{"query": "TODO"})
	argv, err := runner.args(req, newSearch(t, req.Payload))
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	var found bool
	for i, arg := range argv {
		if arg != "--max-budget-usd" {
			continue
		}
		found = true
		if i+1 >= len(argv) || argv[i+1] != "0.0625" {
			t.Errorf("ceiling passed as %v, want the granted 0.0625", argv[i+1:])
		}
	}
	if !found {
		t.Error("the binary was invoked with no ceiling at all")
	}
}

// Claude Code may report a final ledger above --max-budget-usd. That is not a
// successful Atenea step: retain the observed charge, but refuse the result so
// an over-budget provider response cannot enter workflow history as valid.
func TestObservedOverspendIsRefused(t *testing.T) {
	overspent := strings.Replace(answered, `"total_cost_usd":0.0234`, `"total_cost_usd":0.310367`, 1)
	runner := billing(t, overspent)
	out, err := runner.Run(context.Background(), granted(t, 0.25, map[string]any{"query": "TODO"}))
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", contract.KindOf(err))
	}
	if out.SpentUSD != 0.310367 {
		t.Errorf("spent_usd = %v, want 0.310367", out.SpentUSD)
	}
	if out.Result != nil {
		t.Error("an over-budget answer was returned as a successful result")
	}
	if !strings.Contains(err.Error(), "provider ceiling was exceeded") {
		t.Errorf("error does not explain the provider limitation: %v", err)
	}
}

// A commission with nothing left does not get a free call.
//
// This is the half that did not exist while the ceiling lived here: an adapter
// with a private number could always afford itself, because its ceiling was
// refreshed on every invocation. Now the grant is spent down by the core and
// the adapter has to be able to hear "no".
func TestAnEmptyGrantIsRefusedBeforeSpawning(t *testing.T) {
	// A stub that would answer happily -- and must never be reached.
	runner := billing(t, answered)
	req := granted(t, 0, map[string]any{"query": "TODO"})

	out, err := runner.Run(context.Background(), req)
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", contract.KindOf(err))
	}
	if out.SpentUSD != 0 {
		t.Errorf("a refused call reported a charge of %v", out.SpentUSD)
	}
	if out.Result != nil {
		t.Error("the binary answered, so the refusal came after the money was gone")
	}
}

// Running out of money is a refusal, never a health verdict. The timeout bin
// is the one that says "this provider is too slow to use"; the unavailable bin
// says "it is down". Either would take the provider out of the funnel for
// every later step, including the ones that were never going to cost
// anything -- which is the opposite of what an exhausted grant means.
func TestAnEmptyGrantIsNotATimeoutAndNotAnOutage(t *testing.T) {
	runner := billing(t, answered)
	_, err := runner.Run(context.Background(), granted(t, 0, map[string]any{"query": "TODO"}))
	for _, wrong := range []contract.FailureKind{
		contract.FailureTimeout,
		contract.FailureUnavailable,
		contract.FailureNotFound,
	} {
		if contract.KindOf(err) == wrong {
			t.Errorf("an exhausted grant was filed as %v", wrong)
		}
	}
}
