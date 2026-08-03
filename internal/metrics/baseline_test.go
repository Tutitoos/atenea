package metrics

import (
	"context"
	"strings"
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
	out, err := s.Baselines(context.Background(), capability, repository)
	if err != nil {
		t.Fatalf("Baselines: %v", err)
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

// A failed attempt is kept and counted, but it is not a price.
//
// The first version of this store averaged the two together on the argument
// that a tool which hangs before failing has still eaten the wait. That much
// is true; the conclusion drawn from it was not. The same average also lets an
// implementation that refuses INSTANTLY -- no login, no index, no server --
// record a stream of very fast, very cheap calls, and the funnel then hands it
// everything while every commission fails. Failing cheaply must not pay.
func TestAFailedAttemptIsCountedButIsNotAPrice(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	s.Record(call(now, "ripgrep", "14.1.0", 100*time.Millisecond, 0, true))
	s.Record(call(now.Add(time.Second), "ripgrep", "14.1.0", 300*time.Millisecond, 0, false))

	rg := costsOf(t, s, "code.search", "current")["ripgrep"]
	if rg.Attempts != 2 {
		t.Errorf("attempts = %d, want 2: the failed call was dropped from the record", rg.Attempts)
	}
	if rg.Failures != 1 {
		t.Errorf("failures = %d, want 1", rg.Failures)
	}
	if rg.Successes != 1 {
		t.Errorf("successes = %d, want 1: only the working call is a measurement", rg.Successes)
	}
	if rg.Spent.Duration != 100*time.Millisecond {
		t.Errorf("mean = %v, want 100ms: the failure was averaged into the price", rg.Spent.Duration)
	}
}

// The shape that started all this: a provider that only ever fails, and fails
// fast. It must hold no price at all, so the funnel falls back to whatever
// estimate was declared for it instead of believing a number made of refusals.
func TestAProviderThatOnlyRefusesHasNoPrice(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	for i := range 12 {
		s.Record(call(now.Add(time.Duration(i)*time.Second), "claude.search", "2.1.220",
			20*time.Millisecond, 0, false))
	}

	claude := costsOf(t, s, "code.search", "current")["claude.search"]
	if claude.Attempts != 12 || claude.Failures != 12 {
		t.Errorf("attempts/failures = %d/%d, want 12/12", claude.Attempts, claude.Failures)
	}
	if claude.Successes != 0 {
		t.Errorf("successes = %d, want 0", claude.Successes)
	}
	if claude.Spent.Duration != 0 || claude.Spent.Tokens != 0 {
		t.Errorf("spent = %v/%d, want nothing: twelve refusals are not a price",
			claude.Spent.Duration, claude.Spent.Tokens)
	}

	// The seam that matters: the estimate somebody declared must survive
	// contact with a record made entirely of refusals, and the trace must say
	// why a provider with twelve attempts is still ranking on a guess.
	candidates := []contract.Implementation{{
		ID:   "claude.search",
		Cost: contract.Cost{Estimated: contract.Sample{Duration: 3 * time.Second, Tokens: 900}},
	}}
	notices := Apply(costsOf(t, s, "code.search", "current"), candidates, now.Add(time.Hour))
	if candidates[0].Cost.Samples != 0 {
		t.Errorf("samples = %d, want 0: a record of pure failure passed for a measurement",
			candidates[0].Cost.Samples)
	}
	// And the count of what it cost to learn that, because the funnel cannot
	// act on the notice above without it. Zero here reads as a provider on its
	// first outing, which is what the break-in rotation promotes -- so a record
	// of pure failure would keep winning dispatches on the strength of having
	// no measurements, and every win would add another failure to the record.
	if candidates[0].Cost.Attempts != claude.Attempts {
		t.Errorf("attempts = %d, want %d: the funnel cannot tell a first outing "+
			"from a record of nothing but failure",
			candidates[0].Cost.Attempts, claude.Attempts)
	}
	if !candidates[0].Cost.Barren(1) {
		t.Error("a record of twelve failures did not read as barren")
	}
	if got := candidates[0].Cost.Effective(1); got.Duration != 3*time.Second {
		t.Errorf("effective = %v, want the declared 3s estimate", got.Duration)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "none of them successful") {
		t.Errorf("notices = %v, want one saying the attempts never worked", notices)
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
