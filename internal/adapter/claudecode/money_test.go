package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func billing(t *testing.T, stdout string, budget float64) *Runner {
	t.Helper()
	runner, err := New(Options{
		Binary:          stub(t, stdout),
		Implementations: []string{"claude.search"},
		BudgetUSD:       budget,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner
}

const answered = `{"is_error":false,"subtype":"success",
  "structured_output":{"matches":[{"path":"cmd/main.go","line":3,"column":1}]},
  "usage":{"input_tokens":120,"output_tokens":40},
  "total_cost_usd":0.0234,"num_turns":2}`

// What the far side charged travels back as a number of its own. A charge that
// only ever appeared inside a sentence could be read by a human and by nothing
// else, and the receipt would have to be re-derived by parsing prose.
func TestTheChargeComesBackAsANumber(t *testing.T) {
	runner := billing(t, answered, 0.25)
	req := request(t, map[string]any{"query": "TODO"})

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
	runner := billing(t, answered, 0.25)
	req := request(t, map[string]any{"query": "TODO"})

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
	runner := billing(t, free, 0.25)
	req := request(t, map[string]any{"query": "TODO"})

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

// The ceiling reaches the binary. A grant nobody enforced would make the whole
// report a decoration: the number on the receipt only means something if it is
// the number the far side was actually held to.
func TestTheCeilingIsPassedToTheBinary(t *testing.T) {
	runner := billing(t, answered, 0.25)
	req := request(t, map[string]any{"query": "TODO"})
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
		if i+1 >= len(argv) || argv[i+1] != "0.25" {
			t.Errorf("ceiling passed as %v, want 0.25", argv[i+1:])
		}
	}
	if !found {
		t.Error("the binary was invoked with no ceiling at all")
	}
}
