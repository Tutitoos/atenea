package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// worked is one successful attempt, the witness this file turns on.
func worked(at time.Time, impl string) Measurement {
	return call(at, impl, "1.0.0", 100*time.Millisecond, 0, true)
}

// The bug this exists for: a machine where every call succeeded reported
// health=unknown forever and the light never went green. The record held the
// answer and nothing read it, because the rule only looked for failures.
func TestARecentSuccessIsEvidenceOfHealth(t *testing.T) {
	now := time.Now().UTC()
	b := Baseline{Successes: 4, Attempts: 4, Success: now.Add(-time.Minute)}

	health, said := b.Health(now)
	if !said {
		t.Fatal("a call that worked a minute ago said nothing about health")
	}
	if health.State != contract.HealthAlive {
		t.Errorf("state = %v, want alive", health.State)
	}
}

// A success is a statement about the moment it happened. Left to speak
// forever it would report the past: the index was warm THEN, the user was
// logged in THEN.
func TestASuccessGoesStaleAndStopsSpeaking(t *testing.T) {
	now := time.Now().UTC()
	for _, age := range []time.Duration{SuccessWindow + time.Second, 6 * time.Hour, 72 * time.Hour} {
		b := Baseline{Successes: 40, Attempts: 40, Success: now.Add(-age)}
		if _, said := b.Health(now); said {
			t.Errorf("a success %v old still claimed the provider is well", age)
		}
	}
	// The boundary belongs to the success: exactly at the window it still
	// counts, so a provider does not flicker for a call landing on the edge.
	edge := Baseline{Successes: 1, Attempts: 1, Success: now.Add(-SuccessWindow)}
	if _, said := edge.Health(now); !said {
		t.Error("a success exactly at the window was thrown away")
	}
}

// One failure is not enough to condemn a provider -- that takes a streak --
// but it is far too much to call it well. The honest answer while the newest
// thing on record is a failure is that nobody knows.
func TestAFailureWithNothingAfterItBlocksThePromotion(t *testing.T) {
	now := time.Now().UTC()
	b := Baseline{
		Successes: 9, Attempts: 10, Failures: 1,
		Success: now.Add(-2 * time.Minute),
		Fault:   Fault{Streak: 1, SameKindStreak: 1, Kind: "timeout", Reason: "timeout: took too long", Latest: now},
	}

	health, said := b.Health(now)
	if said {
		t.Fatalf("state = %v: one failure after nine wins is neither well nor an outage",
			health.State)
	}
}

// The same shape once the streak has lapsed. The failures are old, so they no
// longer condemn -- but nothing has succeeded since them, so there is still
// nothing to promote on. A provider gets back into the funnel by being tried,
// not by the clock running out.
func TestALapsedStreakDoesNotBecomeHealthByWaiting(t *testing.T) {
	now := time.Now().UTC()
	b := Baseline{
		Successes: 5, Attempts: 8, Failures: 3,
		Success: now.Add(-30 * time.Minute),
		Fault: Fault{
			Streak: 3, SameKindStreak: 3, Kind: "unavailable", Reason: "unavailable: down",
			Latest: now.Add(-20 * time.Minute),
		},
	}

	if health, said := b.Health(now); said {
		t.Fatalf("state = %v, want nothing said: the newest call on record still failed",
			health.State)
	}
}

// A streak is a verdict about this repository and it overrules the settings
// file, which is somebody's opinion about the provider in general.
func TestAStreakStillOutranksADeclaredAlive(t *testing.T) {
	now := time.Now().UTC()
	base := map[string]Baseline{"fixture.search": {
		Attempts: 3, Failures: 3,
		Fault: Fault{Streak: 3, SameKindStreak: 3, Kind: "unavailable", Reason: "unavailable: no server", Latest: now},
	}}
	candidates := []contract.Implementation{{
		ID:     "fixture.search",
		Health: contract.Health{State: contract.HealthAlive, Score: 0.9, Reason: "declared"},
	}}

	Apply(base, candidates, now)
	if candidates[0].Health.State != contract.HealthDown {
		t.Errorf("state = %v, want down: three outages lost to a line in a config file",
			candidates[0].Health.State)
	}
}

// The asymmetry, which is the whole point. A probe marked this provider down
// seconds ago inside this process; the record is a file that may predate the
// outage entirely. Promoting on it would hide the failure the operator is
// standing in front of.
func TestPromotionNeverOverridesALiveProbe(t *testing.T) {
	now := time.Now().UTC()
	base := map[string]Baseline{"ripgrep": {
		Successes: 20, Attempts: 20, Success: now.Add(-time.Minute),
	}}
	for _, probed := range []contract.Health{
		{State: contract.HealthDown, Reason: "probed down"},
		{State: contract.HealthDegraded, Reason: "probed degraded"},
	} {
		candidates := []contract.Implementation{{ID: "ripgrep", Health: probed}}
		Apply(base, candidates, now)
		if candidates[0].Health.State != probed.State {
			t.Errorf("state = %v, want %v: the record talked over a probe",
				candidates[0].Health.State, probed.State)
		}
		if candidates[0].Health.Reason != probed.Reason {
			t.Errorf("reason = %q, want the prober's", candidates[0].Health.Reason)
		}
	}
}

// Promotion is a statement about the state and nothing else. Awarding a full
// score for having worked would put every promoted provider above every other
// on a figure invented here, and cost -- a real number, measured on both --
// would never be reached.
func TestPromotionDoesNotInventAScore(t *testing.T) {
	now := time.Now().UTC()
	base := map[string]Baseline{"ripgrep": {
		Successes: 3, Attempts: 3, Success: now.Add(-time.Second),
	}}
	candidates := []contract.Implementation{{
		ID:     "ripgrep",
		Health: contract.Health{State: contract.HealthUnknown, Score: 0.25},
	}}

	Apply(base, candidates, now)
	if candidates[0].Health.State != contract.HealthAlive {
		t.Fatalf("state = %v, want alive", candidates[0].Health.State)
	}
	if candidates[0].Health.Score != 0.25 {
		t.Errorf("score = %v, want the declared 0.25 carried through",
			candidates[0].Health.Score)
	}
}

// End to end through the store. The query rewrite this needs is the one that
// returns a row for an implementation whose newest attempt SUCCEEDED: the old
// one joined on the failure run and so had nothing to say about anything that
// was working.
func TestTheStoreReportsWhenSomethingLastWorked(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	s.Record(worked(now.Add(-2*time.Second), "ripgrep"))
	s.Record(worked(now.Add(-time.Second), "ripgrep"))

	rg := costsOf(t, s, "code.search", "current")["ripgrep"]
	if rg.Success.IsZero() {
		t.Fatal("two successful calls and the base cannot say when one last worked")
	}
	if got := rg.Success.UTC().Truncate(time.Second); !got.Equal(now.Add(-time.Second).Truncate(time.Second)) {
		t.Errorf("last success = %v, want the newer of the two", got)
	}
	if rg.Fault.Streak != 0 {
		t.Errorf("streak = %d, want 0", rg.Fault.Streak)
	}
	health, said := rg.Health(now)
	if !said || health.State != contract.HealthAlive {
		t.Errorf("health = %v (said %v), want alive", health.State, said)
	}
}

// The same store, one failure later. The success is still on record and still
// readable -- it is the ORDER that decides, not the presence of a win
// somewhere in the history.
func TestAWinInTheHistoryIsNotAWinNow(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	s.Record(worked(now.Add(-time.Minute), "ripgrep"))
	s.Record(broke(now.Add(-time.Second), "ripgrep", "timeout", "timeout: took too long"))

	rg := costsOf(t, s, "code.search", "current")["ripgrep"]
	if rg.Success.IsZero() {
		t.Fatal("the successful call vanished from the record")
	}
	if rg.Fault.Streak != 1 {
		t.Fatalf("streak = %d, want 1", rg.Fault.Streak)
	}
	if health, said := rg.Health(now); said {
		t.Errorf("state = %v, want nothing said: the newest call failed", health.State)
	}
}

// A provider nobody has ever called is not in the base at all, and must not be
// invented into one state or the other by the reader.
func TestSilenceIsStillNotEvidence(t *testing.T) {
	now := time.Now().UTC()
	if health, said := (Baseline{}).Health(now); said {
		t.Errorf("state = %v, want nothing said for an empty record", health.State)
	}
	// Attempts with no successes and no streak cannot happen in the store, but
	// the reader must not promote on a zero timestamp if it ever did.
	if _, said := (Baseline{Attempts: 4}).Health(now); said {
		t.Error("a record with no success at all was read as healthy")
	}
}

// Store.Health folds several repositories into one state per implementation,
// and the fold is "worst", never "whichever row the engine happened to hand
// over last".
//
// The variable is the WRITE ORDER, not the repository names. SQL promises
// nothing about row order, and what DuckDB actually does here is return groups
// roughly in the order they were first inserted -- so a test that only varied
// the names would put the failing repository last both times and a reader that
// simply kept the last row would pass it. Writing the success last is the
// arrangement that tells the two apart.
func TestTheWorstRepositoryWinsWhicheverWayTheRowsArrive(t *testing.T) {
	for _, successLast := range []bool{false, true} {
		s := store(t, Options{})
		now := time.Now().UTC()
		win := worked(now.Add(-time.Minute), "ripgrep")
		win.Repository = "api"
		fails := make([]Measurement, 0, 3)
		for i := range 3 {
			bad := broke(now.Add(time.Duration(i)*time.Second), "ripgrep",
				"unavailable", "unavailable: down")
			bad.Repository = "scripts"
			fails = append(fails, bad)
		}

		if successLast {
			for _, m := range fails {
				s.Record(m)
			}
			s.Record(win)
		} else {
			s.Record(win)
			for _, m := range fails {
				s.Record(m)
			}
		}

		got, err := s.Health(context.Background(), now.Add(time.Second))
		if err != nil {
			t.Fatalf("Health: %v", err)
		}
		v, found := got["ripgrep"]
		if !found {
			t.Fatalf("successLast=%v: nothing reported for a provider with a record", successLast)
		}
		if v.Health.State != contract.HealthDown {
			t.Errorf("successLast=%v: state = %v, want down: the healthy repository hid the dead one",
				successLast, v.Health.State)
		}
		if v.Repository != "scripts" {
			t.Errorf("successLast=%v: verdict came from %q, want the failing scripts",
				successLast, v.Repository)
		}
	}
}
