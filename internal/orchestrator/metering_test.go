package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// tally is a Meter that keeps what it was told instead of writing it.
type tally struct {
	mu      sync.Mutex
	rows    []metrics.Measurement
	settled int
}

func (t *tally) Record(m metrics.Measurement) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows = append(t.rows, m)
}

func (t *tally) Settle(context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.settled++
}

func (t *tally) taken() ([]metrics.Measurement, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]metrics.Measurement(nil), t.rows...), t.settled
}

func metered(t *testing.T, runner contract.Runner) (*orchestrator.Agent, *tally) {
	t.Helper()
	reg := catalog(t)
	if fake, ok := runner.(*fakeRunner); ok && fake.serves == nil {
		for _, capability := range reg.Capabilities() {
			impls, err := reg.ImplementationsFor(capability.ID)
			if err != nil {
				t.Fatalf("ImplementationsFor: %v", err)
			}
			for _, impl := range impls {
				fake.serves = append(fake.serves, impl.ID)
			}
		}
	}
	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	store, err := checkpoint.New("")
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	counter := &tally{}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog:     reg,
		Chooser:     chooser,
		Runner:      runner,
		Checkpoints: store,
		Meter:       counter,
		MaxParallel: 0,
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	return agent, counter
}

// Every step that closes is measured, and the row says which capability was
// asked, who answered and what it cost. Without all three the baseline cannot
// be read back per capability and per implementation, which is what it is for.
func TestEveryClosedStepIsMeasured(t *testing.T) {
	agent, counter := metered(t, &fakeRunner{})
	result, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	rows, _ := counter.taken()
	if len(rows) != len(result.Steps) {
		t.Fatalf("%d measurements for %d steps", len(rows), len(result.Steps))
	}
	for _, row := range rows {
		if row.RunID != result.RunID {
			t.Errorf("measurement carries run %q, want %q", row.RunID, result.RunID)
		}
		if row.Capability == "" || row.Implementation == "" || row.Repository == "" {
			t.Errorf("measurement is missing its key: %+v", row)
		}
		if row.StepID == "" {
			t.Errorf("measurement has no step: %+v", row)
		}
		if row.Spent.Duration <= 0 {
			t.Errorf("step %s reports no time at all", row.StepID)
		}
		if row.At.IsZero() {
			t.Errorf("step %s reports no time of day", row.StepID)
		}
	}
}

// The core's clock is the one that counts, not the adapter's. A far side that
// under-reports -- or does not report at all -- must not be able to write
// itself a cheaper baseline than the wait the caller actually sat through.
func TestTheDurationRecordedIsTheCoresNotTheAdapters(t *testing.T) {
	runner := &fakeRunner{
		delay: 20 * time.Millisecond,
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{
				Result:  map[string]any{"matches": []any{}},
				Verdict: contract.VerdictOK,
				// A far side claiming a call took a nanosecond.
				Spent: contract.Sample{Duration: time.Nanosecond, Tokens: 7},
			}, nil
		},
	}
	agent, counter := metered(t, runner)
	if _, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	rows, _ := counter.taken()
	if len(rows) == 0 {
		t.Fatal("nothing was measured")
	}
	for _, row := range rows {
		if row.Spent.Duration < 20*time.Millisecond {
			t.Errorf("step %s recorded %v, want at least the %v it really waited",
				row.StepID, row.Spent.Duration, 20*time.Millisecond)
		}
		// Tokens and memory still come from the far side: the core cannot see
		// either of them.
		if row.Spent.Tokens != 7 {
			t.Errorf("tokens = %d, want the far side's 7", row.Spent.Tokens)
		}
	}
}

// A provider that fails expensively must stop looking cheap, so a failed
// attempt is measured like any other -- with the bin it landed in and the
// reason a human would read.
func TestAFailedStepIsMeasuredWithItsBinAndReason(t *testing.T) {
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{}, contract.Fail(contract.FailureTimeout,
				"provider took too long")
		},
	}
	agent, counter := metered(t, runner)
	if _, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	rows, _ := counter.taken()
	if len(rows) == 0 {
		t.Fatal("a run that failed measured nothing")
	}
	found := false
	for _, row := range rows {
		if row.OK {
			continue
		}
		found = true
		if row.FailureKind != contract.FailureTimeout.String() {
			t.Errorf("kind = %q, want %q", row.FailureKind, contract.FailureTimeout)
		}
		if !strings.Contains(row.Failure, "took too long") {
			t.Errorf("reason = %q, want the provider's own words", row.Failure)
		}
	}
	if !found {
		t.Error("every measurement claims success on a run that failed")
	}
}

// A blocked step never reached a provider: nobody was chosen and no time was
// spent. Filing it would sit a row under an empty implementation id, and the
// selector reads those averages as if a real tool had produced them.
func TestABlockedStepIsNotMeasured(t *testing.T) {
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{}, contract.Fail(contract.FailureTimeout,
				"provider took too long")
		},
	}
	agent, counter := metered(t, runner)
	result, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	blocked := 0
	for _, step := range result.Steps {
		if step.Decision.Chosen.ID == "" {
			blocked++
		}
	}
	if blocked == 0 {
		t.Fatal("the fixture no longer blocks anything, so this proves nothing")
	}

	rows, _ := counter.taken()
	if len(rows) != len(result.Steps)-blocked {
		t.Errorf("%d measurements for %d steps of which %d never ran",
			len(rows), len(result.Steps), blocked)
	}
	for _, row := range rows {
		if row.Implementation == "" {
			t.Errorf("step %s was filed under no implementation at all", row.StepID)
		}
	}
}

// What the far side said about itself travels with the numbers it produced, so
// an upgrade starts a fresh baseline instead of averaging into the old one.
func TestTheToolVersionTravelsWithTheMeasurement(t *testing.T) {
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{
				Result:      map[string]any{"matches": []any{}},
				Verdict:     contract.VerdictOK,
				Spent:       contract.Sample{Duration: time.Millisecond, PeakRSS: 7 << 20},
				ToolVersion: "ripgrep 14.1.0",
			}, nil
		},
	}
	agent, counter := metered(t, runner)
	if _, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	rows, _ := counter.taken()
	for _, row := range rows {
		if row.ToolVersion != "ripgrep 14.1.0" {
			t.Errorf("version = %q, want what the far side answered", row.ToolVersion)
		}
		if row.Spent.PeakRSS != 7<<20 {
			t.Errorf("peak = %d, want the far side's figure", row.Spent.PeakRSS)
		}
	}
}

// Batching is only safe because of the two moments that do not wait for the
// beat. This is one of them: a crash after a phase must not take the phase's
// measurements with it.
func TestAClosingPhasePushesTheBatch(t *testing.T) {
	agent, counter := metered(t, &fakeRunner{})
	result, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	_, settled := counter.taken()
	if settled != len(result.Phases) {
		t.Fatalf("the batch was pushed %d time(s) over %d phase(s)", settled, len(result.Phases))
	}
	if settled == 0 {
		t.Fatal("a whole commission closed without the batch ever reaching disk")
	}
}

// Measuring is not what makes the work correct. A core with nobody collecting
// still dispatches, reviews and answers; it simply learns nothing.
func TestACoreWithNoMeterStillWorks(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	result, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v with no meter attached", result.Verdict)
	}
}
