package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// broke is one failed attempt with its bin, which is the axis every test here
// turns on: the same bin repeated is an outage, a scatter of bins is not.
func broke(at time.Time, impl, kind, reason string) Measurement {
	m := call(at, impl, "1.0.0", 20*time.Millisecond, 0, false)
	m.FailureKind = kind
	m.Failure = reason
	return m
}

func faultOf(t *testing.T, s *Store, impl string) Fault {
	t.Helper()
	return costsOf(t, s, "code.search", "current")[impl].Fault
}

// The bug this exists for: a provider that is simply down answers every call
// the same way, and nothing in the funnel noticed. Three of those in a row is
// not a run of bad luck, it is a provider to stop calling.
func TestARunOfOneFailureBinIsAnOutage(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	for i := range 3 {
		s.Record(broke(now.Add(time.Duration(i)*time.Second), "claude.search",
			"unavailable", "claude code is not logged in on this machine"))
	}

	fault := faultOf(t, s, "claude.search")
	if fault.Streak != 3 {
		t.Fatalf("streak = %d, want 3", fault.Streak)
	}
	if fault.Kind != "unavailable" {
		t.Errorf("kind = %q, want the shared bin", fault.Kind)
	}
	health, hurt := fault.Health(now.Add(time.Second))
	if !hurt {
		t.Fatal("three identical failures did not reach health at all")
	}
	if health.State != contract.HealthDown {
		t.Errorf("state = %v, want down", health.State)
	}
	if !strings.Contains(health.Reason, "not logged in") {
		t.Errorf("reason %q does not carry what the provider actually said", health.Reason)
	}
}

// The bug this one exists for: a streak trips the breaker and the funnel
// stops dispatching, so there is no fresh call left anywhere to inspect --
// the newest attempt's raw provider text is the only copy of the evidence,
// and it lives here or nowhere.
func TestHealthCarriesTheNewestRawText(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	for i := range 3 {
		m := broke(now.Add(time.Duration(i)*time.Second), "serena.implementations",
			"unavailable", "serena did not answer")
		m.Raw = fmt.Sprintf("attempt %d: no symbol matching found", i)
		s.Record(m)
	}

	fault := faultOf(t, s, "serena.implementations")
	if fault.Raw != "attempt 2: no symbol matching found" {
		t.Fatalf("fault.Raw = %q, want the newest attempt's text", fault.Raw)
	}
	health, hurt := fault.Health(now.Add(time.Second))
	if !hurt {
		t.Fatal("three identical failures did not reach health at all")
	}
	if health.Raw != "attempt 2: no symbol matching found" {
		t.Errorf("health.Raw = %q, want it carried over from the fault", health.Raw)
	}
}

// Breaking differently every time is a real finding and a different one. There
// is no single fault to name, so the provider ranks below the healthy ones
// instead of being dropped: the funnel would rather use a flaky provider than
// no provider.
func TestFailuresWithNoSingleCauseDegradeRatherThanDrop(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	s.Record(broke(now, "serena.definition", "timeout", "took too long"))
	s.Record(broke(now.Add(time.Second), "serena.definition", "unavailable", "no language server"))
	s.Record(broke(now.Add(2*time.Second), "serena.definition", "unspecified", "the adapter did not sort this"))

	fault := faultOf(t, s, "serena.definition")
	if fault.Streak != 3 {
		t.Fatalf("streak = %d, want 3", fault.Streak)
	}
	if fault.Kind != "" {
		t.Errorf("kind = %q, want empty: three different bins share no cause", fault.Kind)
	}
	health, hurt := fault.Health(now.Add(3 * time.Second))
	if !hurt || health.State != contract.HealthDegraded {
		t.Errorf("state = %v (reached=%v), want degraded", health.State, hurt)
	}
	if !health.Usable() {
		t.Error("a provider with no single fault was dropped from the funnel")
	}
}

// The bug this exists for, measured on a real machine before it was fixed.
//
// `claude.search` had never once succeeded on this repository, so the run
// since the last success was its ENTIRE history: five failures of one bin from
// the days it was not logged in, then three of another after that was fixed.
// Counting bins over the whole run found two, blanked the Kind, and reported a
// merely degraded provider -- so a cause that had been fixed days earlier was
// masking the one failing every call today, and it could never stop masking
// it: a provider that has never succeeded here never earns a clean slate to
// start a fresh run from.
//
// The pair originally measured was unavailable then permission_denied.
// permission_denied no longer reaches this record at all -- it is a fact about
// the request rather than about the provider -- so the run is rebuilt from two
// bins that do count. What is under test is the ordering, not which bins
// happened to expose it.
func TestAFreshSameKindRunOutweighsAnOldMixedHistory(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	for i := range 5 {
		s.Record(broke(now.Add(time.Duration(i)*time.Second), "claude.search",
			"unavailable", "unavailable: claude code is not logged in on this machine"))
	}
	for i := range 3 {
		s.Record(broke(now.Add(time.Duration(5+i)*time.Second), "claude.search",
			"timeout", "timeout: claude code took longer than the limit allows"))
	}

	fault := faultOf(t, s, "claude.search")
	if fault.Streak != 8 {
		t.Fatalf("streak = %d, want 8: the whole run since the last success", fault.Streak)
	}
	if fault.SameKindStreak != 3 {
		t.Fatalf("same-kind streak = %d, want 3: only the newest run shares a bin",
			fault.SameKindStreak)
	}
	if fault.Kind != "timeout" {
		t.Errorf("kind = %q, want timeout: three in a row at the newest end is nameable",
			fault.Kind)
	}
	health, hurt := fault.Health(now.Add(8 * time.Second))
	if !hurt {
		t.Fatal("eight failures in a row did not reach health at all")
	}
	if health.State != contract.HealthDown {
		t.Errorf("state = %v, want down: an old fixed cause masked today's real one",
			health.State)
	}
	if health.Usable() {
		t.Error("a provider failing every call the same way was left in the funnel")
	}
	// The count in the sentence is the run that earned the verdict, not the
	// whole history: claiming eight timeout failures when three of them were
	// that bin would be evidence nobody could check.
	if !strings.Contains(health.Reason, "3 timeout failures in a row") {
		t.Errorf("reason = %q, want the same-kind count, not the whole streak", health.Reason)
	}
}

// The other side of the same line, so the fix above cannot become "any bin
// repeated twice at the end is an outage". A tail shorter than FaultStreak is
// still a provider breaking differently, and the funnel would rather rank a
// flaky provider last than have nothing to call.
func TestAShortSameKindTailIsStillNotAnOutage(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	s.Record(broke(now, "serena.search", "timeout", "timeout: took too long"))
	s.Record(broke(now.Add(time.Second), "serena.search", "unspecified", "unspecified: the adapter did not sort this"))
	s.Record(broke(now.Add(2*time.Second), "serena.search", "unavailable", "unavailable: no server"))
	s.Record(broke(now.Add(3*time.Second), "serena.search", "unavailable", "unavailable: no server"))

	fault := faultOf(t, s, "serena.search")
	if fault.Streak != 4 {
		t.Fatalf("streak = %d, want 4", fault.Streak)
	}
	if fault.SameKindStreak != 2 {
		t.Fatalf("same-kind streak = %d, want 2: the tail stops at the unspecified",
			fault.SameKindStreak)
	}
	if fault.Kind != "" {
		t.Errorf("kind = %q, want empty: two in a row does not name an outage", fault.Kind)
	}
	health, hurt := fault.Health(now.Add(4 * time.Second))
	if !hurt || health.State != contract.HealthDegraded {
		t.Errorf("state = %v (reached=%v), want degraded", health.State, hurt)
	}
	if !health.Usable() {
		t.Error("a provider with no single fault was dropped from the funnel")
	}
}

// A bin that describes the request, not the provider, is not evidence about
// the provider and must never reach the record.
//
// Measured before this held: eight generated TypeScript files absent from a
// graph each returned an honest not_found, three in a row tripped the breaker,
// and symbol.overview went down for the entire repository -- every real file
// asked about after them was refused with "every implementation is down",
// while both providers were answering perfectly.
func TestARequestShapedRefusalNeverCondemnsTheProvider(t *testing.T) {
	now := time.Now().UTC()
	for _, bin := range []string{"not_found", "permission_denied", "invalid_input", "canceled"} {
		s := store(t, Options{})
		for i := range 5 {
			s.Record(broke(now.Add(time.Duration(i)*time.Second), "graph.overview",
				bin, bin+": the request could not be served"))
		}
		fault := faultOf(t, s, "graph.overview")
		if fault.Streak != 0 {
			t.Errorf("%s: streak = %d, want 0: the provider answered correctly every time",
				bin, fault.Streak)
		}
		if _, hurt := fault.Health(now.Add(5 * time.Second)); hurt {
			t.Errorf("%s: five of them condemned a provider that did nothing wrong", bin)
		}
	}
}

// The exemption must not work by swallowing the run: a refusal is not evidence
// in either direction, so it cannot rescue a provider that really is down.
func TestARefusalDoesNotBreakAGenuineOutage(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	s.Record(broke(now, "serena.overview", "unavailable", "unavailable: no server"))
	s.Record(broke(now.Add(time.Second), "serena.overview", "unavailable", "unavailable: no server"))
	s.Record(broke(now.Add(2*time.Second), "serena.overview", "not_found", "not_found: no such file"))
	s.Record(broke(now.Add(3*time.Second), "serena.overview", "unavailable", "unavailable: no server"))

	fault := faultOf(t, s, "serena.overview")
	if fault.SameKindStreak != 3 {
		t.Fatalf("same-kind streak = %d, want 3: the not_found is invisible, not a break",
			fault.SameKindStreak)
	}
	health, hurt := fault.Health(now.Add(4 * time.Second))
	if !hurt || health.State != contract.HealthDown {
		t.Errorf("state = %v (reached=%v), want down: a refusal laundered a real outage",
			health.State, hurt)
	}
}

// A streak is the run at the newest end of the record. One call that works
// ends it, which is the only way a provider ever comes back on its own.
func TestASuccessEndsTheStreak(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	for i := range 5 {
		s.Record(broke(now.Add(time.Duration(i)*time.Second), "ripgrep", "unavailable", "down"))
	}
	s.Record(call(now.Add(9*time.Second), "ripgrep", "1.0.0", 80*time.Millisecond, 0, true))

	fault := faultOf(t, s, "ripgrep")
	if fault.Streak != 0 {
		t.Errorf("streak = %d, want 0: the newest call worked", fault.Streak)
	}
	if _, hurt := fault.Health(now.Add(10 * time.Second)); hurt {
		t.Error("a provider that just answered was still called unhealthy")
	}
}

// Two is not a streak. The funnel hands an unmeasured implementation the
// break-in turn twice before its numbers are believed, and condemning it on
// the strength of the calls that were buying its baseline would mean a
// newcomer that stumbles once is never measured at all.
func TestTwoFailuresAreNotYetAnOutage(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	s.Record(broke(now, "claude.search", "unavailable", "down"))
	s.Record(broke(now.Add(time.Second), "claude.search", "unavailable", "down"))

	fault := faultOf(t, s, "claude.search")
	if fault.Streak != 2 {
		t.Fatalf("streak = %d, want 2", fault.Streak)
	}
	if _, hurt := fault.Health(now.Add(time.Second)); hurt {
		t.Errorf("two failures already condemned the provider, want %d", FaultStreak)
	}
}

// The way back. Health drops a provider from the funnel, so nothing calls it,
// so the streak that damns it can never be broken by a success -- unless the
// verdict expires. After the window the next call goes through; the older
// failures stay on record, so if it breaks again one failure is enough to
// close it, and recovery costs exactly one call either way.
func TestAnOldStreakLapsesSoTheProviderIsTriedAgain(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	for i := range 4 {
		s.Record(broke(now.Add(time.Duration(i)*time.Second), "claude.search", "unavailable", "down"))
	}

	fault := faultOf(t, s, "claude.search")
	if _, hurt := fault.Health(now.Add(time.Minute)); !hurt {
		t.Fatal("a fresh streak was already being ignored")
	}
	if _, hurt := fault.Health(now.Add(FaultWindow + time.Minute)); hurt {
		t.Errorf("the streak never lapsed, so nothing would ever call the provider again")
	}
}

// Folding summarizes what things cost and throws the ordering away. Attempt
// rows outlive their fold for the whole fine window precisely so questions
// about order can still be asked, and this is one of them.
func TestAStreakSurvivesFolding(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	old := now.Add(-3 * time.Hour)
	for i := range 3 {
		s.Record(broke(old.Add(time.Duration(i)*time.Second), "claude.search", "unavailable", "down"))
	}
	if err := s.Flush(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := s.Compact(t.Context(), now); err != nil {
		t.Fatalf("compact: %v", err)
	}

	if got := faultOf(t, s, "claude.search").Streak; got != 3 {
		t.Errorf("streak = %d after folding, want 3: the fold ate the ordering", got)
	}
}

// A folded record of pure failure must not come back as a price either. The
// rollup carries the successful half in its own columns for exactly this.
func TestFoldedFailuresAreStillNotAPrice(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	old := now.Add(-3 * time.Hour)
	s.Record(call(old, "ripgrep", "1.0.0", 100*time.Millisecond, 10, true))
	s.Record(broke(old.Add(time.Second), "ripgrep", "timeout", "took too long"))
	if err := s.Flush(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := s.Compact(t.Context(), now); err != nil {
		t.Fatalf("compact: %v", err)
	}

	rg := costsOf(t, s, "code.search", "current")["ripgrep"]
	if rg.Attempts != 2 || rg.Failures != 1 {
		t.Errorf("attempts/failures = %d/%d, want 2/1", rg.Attempts, rg.Failures)
	}
	if rg.Successes != 1 {
		t.Errorf("successes = %d, want 1", rg.Successes)
	}
	if rg.Spent.Duration != 100*time.Millisecond {
		t.Errorf("mean = %v, want 100ms: the folded failure priced the provider",
			rg.Spent.Duration)
	}
}

// Health from the record only ever makes a candidate worse. Somebody who
// probed and found a provider down knows something this store does not, and a
// milder verdict computed from a handful of rows must not overwrite it.
func TestTheRecordNeverPromotesAProviderSomebodyProbedDown(t *testing.T) {
	now := time.Now().UTC()
	base := map[string]Baseline{"serena.search": {
		Attempts: 3, Failures: 3,
		Fault: Fault{Streak: 3, Latest: now, Reason: "mixed"},
	}}
	candidates := []contract.Implementation{{
		ID:     "serena.search",
		Health: contract.Health{State: contract.HealthDown, Reason: "probed down"},
	}}

	Apply(base, candidates, now)
	if candidates[0].Health.State != contract.HealthDown {
		t.Errorf("state = %v, want down: a mixed streak upgraded a probed outage",
			candidates[0].Health.State)
	}
	if candidates[0].Health.Reason != "probed down" {
		t.Errorf("reason = %q, want the prober's", candidates[0].Health.Reason)
	}
}

// A streak is about the binary installed right now. The query's own comment
// has always said so, and until the `current` CTE existed nothing enforced it:
// tool_version appeared in no WHERE, no PARTITION BY and no GROUP BY, so three
// failures from the binary replaced this morning still condemned the one
// installed to fix them, and the funnel refused to call the fixed provider
// until FaultWindow had run out.
func TestAnUpgradeStartsTheStreakOver(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC()
	for i := range 3 {
		down := broke(now.Add(time.Duration(i)*time.Second), "claude.search",
			"unavailable", "claude code is not logged in on this machine")
		down.ToolVersion = "1.0.0"
		s.Record(down)
	}
	// The upgrade, and one failure under it: one fault is ordinary and must
	// not inherit the condemned binary's record.
	upgraded := broke(now.Add(4*time.Second), "claude.search",
		"unavailable", "claude code is not logged in on this machine")
	upgraded.ToolVersion = "2.0.0"
	s.Record(upgraded)

	fault := faultOf(t, s, "claude.search")
	if fault.Streak != 1 {
		t.Errorf("streak = %d, want 1: the three failures belong to the version that was replaced", fault.Streak)
	}
	if _, hurt := fault.Health(now.Add(5 * time.Second)); hurt {
		t.Error("the newly installed version was condemned by the old one's record")
	}
}
