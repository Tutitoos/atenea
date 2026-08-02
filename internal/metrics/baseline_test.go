package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// call is one attempt with the axes and the version spelled out, because every
// test here is about exactly those.
func call(at time.Time, impl, version string, d time.Duration, tokens int, ok bool) Measurement {
	m := attempt(at, "code.search", impl)
	m.ToolVersion = version
	m.Spent = contract.Sample{Duration: d, Tokens: tokens, PeakRSS: 4 << 20}
	m.OK = ok
	return m
}

func costsOf(t *testing.T, s *Store, capability, repository string) map[string]Baseline {
	t.Helper()
	out, err := s.Costs(context.Background(), capability, repository)
	if err != nil {
		t.Fatalf("Costs: %v", err)
	}
	return out
}

// The funnel ranks on what one call costs, not on what every call cost added
// up. A store that handed back totals would make the most-used implementation
// look like the most expensive one, which is precisely backwards.
func TestTheBaselineIsAnAveragePerCall(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	s.Record(call(now, "ripgrep", "14.1.0", 100*time.Millisecond, 10, true))
	s.Record(call(now.Add(time.Second), "ripgrep", "14.1.0", 300*time.Millisecond, 30, true))

	got := costsOf(t, s, "code.search", "current")
	rg, ok := got["ripgrep"]
	if !ok {
		t.Fatalf("ripgrep is missing from %v", got)
	}
	if rg.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", rg.Attempts)
	}
	if rg.Spent.Duration != 200*time.Millisecond {
		t.Errorf("mean duration = %v, want 200ms", rg.Spent.Duration)
	}
	if rg.Spent.Tokens != 20 {
		t.Errorf("mean tokens = %d, want 20", rg.Spent.Tokens)
	}
}

// An upgraded tool starts a fresh baseline. Averaging the old binary's numbers
// into the new one's is what makes a funnel take weeks to notice that a
// provider got faster -- and it is the reason the version is recorded at all.
func TestOnlyTheRunningVersionCounts(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	// The old, slow binary, measured many times.
	for i := range 6 {
		s.Record(call(now.Add(time.Duration(i)*time.Second), "serena.search", "1.0.0",
			2*time.Second, 900, true))
	}
	// Then the upgrade, measured once.
	s.Record(call(now.Add(time.Minute), "serena.search", "2.0.0", 50*time.Millisecond, 10, true))

	got := costsOf(t, s, "code.search", "current")
	serena := got["serena.search"]
	if serena.ToolVersion != "2.0.0" {
		t.Errorf("version = %q, want 2.0.0", serena.ToolVersion)
	}
	if serena.Attempts != 1 {
		t.Errorf("attempts = %d, want 1: the old version's six must not carry over", serena.Attempts)
	}
	if serena.Spent.Duration != 50*time.Millisecond {
		t.Errorf("mean = %v, want 50ms: the old numbers dragged the average", serena.Spent.Duration)
	}
}

// A failed attempt is still a measurement. A tool that gives up quickly is not
// fast, and one that hangs before failing has still eaten the wait -- so both
// count towards what it costs and towards earning its way out of break-in.
func TestFailedAttemptsAreStillMeasurements(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	s.Record(call(now, "ripgrep", "14.1.0", 100*time.Millisecond, 0, true))
	s.Record(call(now.Add(time.Second), "ripgrep", "14.1.0", 300*time.Millisecond, 0, false))

	rg := costsOf(t, s, "code.search", "current")["ripgrep"]
	if rg.Attempts != 2 {
		t.Errorf("attempts = %d, want 2: the failed call was dropped", rg.Attempts)
	}
	if rg.Spent.Duration != 200*time.Millisecond {
		t.Errorf("mean = %v, want 200ms", rg.Spent.Duration)
	}
}

// Cost is asked per repository because it is not a property of the tool: the
// same provider is cheap with a warm index and expensive without one. Numbers
// from one repository answering for another would be the funnel's worst kind
// of lie -- confident and wrong.
func TestCostIsAskedPerRepository(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	warm := call(now, "serena.search", "1.0.0", 20*time.Millisecond, 5, true)
	warm.Repository = "api"
	cold := call(now, "serena.search", "1.0.0", 4*time.Second, 800, true)
	cold.Repository = "scripts"
	s.Record(warm)
	s.Record(cold)

	if got := costsOf(t, s, "code.search", "api")["serena.search"]; got.Spent.Duration != 20*time.Millisecond {
		t.Errorf("api mean = %v, want 20ms", got.Spent.Duration)
	}
	if got := costsOf(t, s, "code.search", "scripts")["serena.search"]; got.Spent.Duration != 4*time.Second {
		t.Errorf("scripts mean = %v, want 4s", got.Spent.Duration)
	}
	if got := costsOf(t, s, "code.search", "nowhere"); len(got) != 0 {
		t.Errorf("a repository nobody measured answered with %v", got)
	}
}

// Never measured is not the same as free. An implementation absent from the
// answer is one the funnel must keep ranking on its estimate, and a zero
// returned in its place would read as the cheapest thing on the machine.
func TestNeverMeasuredIsAbsentRatherThanZero(t *testing.T) {
	s := store(t, Options{})
	s.Record(call(time.Now().UTC(), "ripgrep", "14.1.0", time.Millisecond, 0, true))

	got := costsOf(t, s, "code.search", "current")
	if _, ok := got["serena.search"]; ok {
		t.Error("an implementation nobody measured came back with a figure")
	}
	if _, ok := got["ripgrep"]; !ok {
		t.Error("the one that was measured is missing")
	}
}

// The compaction ladder folds attempts into hourly rollups. The funnel has to
// keep seeing the same cost across that fold: a baseline that changed the day
// history was tidied would re-rank providers for no reason anybody could name.
func TestFoldedHistoryStillAnswers(t *testing.T) {
	s := store(t, Options{})
	old := time.Now().UTC().Add(-48 * time.Hour)
	s.Record(call(old, "ripgrep", "14.1.0", 100*time.Millisecond, 10, true))
	s.Record(call(old.Add(time.Second), "ripgrep", "14.1.0", 300*time.Millisecond, 30, true))

	before := costsOf(t, s, "code.search", "current")["ripgrep"]
	if err := s.Compact(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after := costsOf(t, s, "code.search", "current")["ripgrep"]

	if before.Attempts != after.Attempts || before.Spent.Duration != after.Spent.Duration {
		t.Errorf("folding changed the baseline: %+v -> %+v", before, after)
	}
	if after.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", after.Attempts)
	}
}
