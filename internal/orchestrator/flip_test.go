package orchestrator_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// billed is a far side whose two providers cost what the settings file says
// they do not. It is how the loop gets closed without a real tool: the work
// is fake, but the seconds and the tokens the base learns from are the ones
// this runner actually burned.
type billed struct {
	slow time.Duration
}

func (b billed) ID() string { return "billed" }

func (b billed) Implementations() []string { return []string{"fast", "slow"} }

func (b billed) Capabilities() []string { return []string{"code.search"} }

func (b billed) Serves(id string) bool { return id == "fast" || id == "slow" }

func (b billed) Run(_ context.Context, req contract.RunRequest) (contract.Outcome, error) {
	// "fast" is the one the settings file calls cheap. It is not.
	tokens := 20
	if req.Implementation.ID == "fast" {
		time.Sleep(b.slow)
		tokens = 900
	}
	return contract.Outcome{
		Verdict: contract.VerdictOK,
		Spent:   contract.Sample{Tokens: tokens},
		Result:  map[string]any{"matches": []any{}},
	}, nil
}

// The loop, closed on a real database: the orchestrator writes what a step
// cost, the funnel reads it back on the next ask, and the answer changes
// because of it. Every piece of this brick is in the path -- the store's
// write, its per-version read, break-in mode and the cost comparison -- and
// nothing about the run is mocked except the tool at the far end.
//
// The rhythm is the interesting half. A cold funnel ranks on the settings
// file, so it opens with whoever the estimate favors; break-in then hands the
// turn to whoever owes the base measurements, alternating until both have
// paid; and only once the numbers exist does cost decide for real -- at which
// point the estimate loses and stays lost.
func TestADecisionFlipsOnceTheBaseHasRealNumbers(t *testing.T) {
	dir := t.TempDir()
	store, err := metrics.Open(filepath.Join(dir, "metrics.duckdb"), metrics.Options{})
	if err != nil {
		t.Fatalf("metrics.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	checkpoints, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog:     twins(t),
		Chooser:     chooser,
		Runner:      billed{slow: 25 * time.Millisecond},
		Checkpoints: checkpoints,
		Meter:       flushing{store},
		Base:        store,
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}

	ask := func() (string, string) {
		t.Helper()
		result, err := agent.Ask(context.Background(), orchestrator.Question{
			Capability: "code.search",
			Repository: "api",
			Payload:    map[string]any{"query": "TODO"},
		})
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		step := only(t, result)
		return step.Decision.Chosen.ID, step.Decision.Reason
	}

	// Two measurements each is what break-in asks for, and the two providers
	// take turns getting them. Until the last one lands, the estimate is all
	// the funnel has to rank the pair on.
	want := []string{"fast", "slow", "fast", "slow"}
	for i, expected := range want {
		chosen, reason := ask()
		if chosen != expected {
			t.Fatalf("ask %d chose %s, want %s (%s)", i+1, chosen, expected, reason)
		}
		if strings.Contains(reason, "(measured)") {
			t.Fatalf("ask %d claimed measured numbers before both had any: %s", i+1, reason)
		}
	}

	// Both have paid their two turns. From here the base outranks the file,
	// and it says the settings file had it backwards.
	chosen, reason := ask()
	if chosen != "slow" {
		t.Fatalf("with both measured the funnel chose %s, want slow: %s", chosen, reason)
	}
	if !strings.Contains(reason, "(measured)") {
		t.Errorf("the flip was explained as %q, which does not say the numbers were real", reason)
	}

	// And it stays flipped. A funnel that oscillated would be one still
	// ranking on the estimate half the time.
	for i := range 3 {
		if chosen, reason := ask(); chosen != "slow" {
			t.Fatalf("ask %d after the flip went back to %s: %s", i+1, chosen, reason)
		}
	}

	// The base is not just being consulted, it is being filled: six asks in,
	// the losing provider stopped being asked and its count stopped growing.
	costs, err := store.Baselines(context.Background(), "code.search", "api", "")
	if err != nil {
		t.Fatalf("Baselines: %v", err)
	}
	if costs["fast"].Attempts != 2 {
		t.Errorf("fast was measured %d times, want the 2 break-in gave it",
			costs["fast"].Attempts)
	}
	if costs["slow"].Attempts < 5 {
		t.Errorf("slow was measured %d times, want every turn it won",
			costs["slow"].Attempts)
	}
	if costs["fast"].Spent.Tokens <= costs["slow"].Spent.Tokens {
		t.Errorf("the base learned fast=%d slow=%d tokens; the flip was luck, not measurement",
			costs["fast"].Spent.Tokens, costs["slow"].Spent.Tokens)
	}
}

// flushing is the meter the core installs, minus the beat scheduler: a settle
// here writes the batch immediately, which is what makes the next ask read
// what this one just did.
type flushing struct{ store *metrics.Store }

func (f flushing) Record(m metrics.Measurement) { f.store.Record(m) }
func (f flushing) Settle(ctx context.Context)   { _ = f.store.Flush(ctx) }
