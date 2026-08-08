package core_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// seeded writes attempts straight into a throwaway base and returns a core
// built over it.
//
// Straight in, rather than by running a commission, because a commission ends
// in Shutdown and Shutdown closes the store. The screen has to be read while
// the core is still alive, which is the only state a person ever reads it in.
func seeded(t *testing.T, write func(*metrics.Store)) *core.Core {
	t.Helper()
	base := filepath.Join(t.TempDir(), "base.duckdb")
	store, err := metrics.Open(base, metrics.Options{})
	if err != nil {
		t.Fatalf("open the base: %v", err)
	}
	write(store)
	if err := store.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return build(t, catalog+"\n[metrics]\npath = \""+base+"\"\n")
}

// tried is one attempt against one repository of the fixture.
func tried(at time.Time, impl, repo string, ok bool, kind, reason string) metrics.Measurement {
	return metrics.Measurement{
		At: at, RunID: "r", StepID: "s",
		Capability: "code.search", Implementation: impl, Repository: repo,
		Provider: impl, ToolVersion: "1.0.0",
		Spent:       contract.Sample{Duration: 90 * time.Millisecond},
		OK:          ok,
		FailureKind: kind, Failure: reason,
	}
}

func shown(t *testing.T, s core.Status, impl string) core.ImplementationStatus {
	t.Helper()
	for _, c := range s.Capabilities {
		for _, i := range c.Implementations {
			if i.ID == impl {
				return i
			}
		}
	}
	t.Fatalf("%s is not on the screen", impl)
	return core.ImplementationStatus{}
}

// The bug this exists for: seven successful calls in a row, and the screen
// still said health=unknown with an amber light, because it read the catalog
// and never opened the base. Nothing a user could do would clear it.
func TestTheScreenReadsTheRecord(t *testing.T) {
	now := time.Now().UTC()
	atenea := seeded(t, func(s *metrics.Store) {
		for i := range 7 {
			s.Record(tried(now.Add(time.Duration(i)*time.Second), "graph.search", "api", true, "", ""))
		}
	})

	got := shown(t, atenea.Status(), "graph.search")
	if got.Health.State != contract.HealthAlive {
		t.Errorf("health = %v, want alive: seven successful calls went unread",
			got.Health.State)
	}
	if got.Light != core.LightGreen {
		t.Errorf("light = %s, want green", got.Light)
	}
	if !strings.Contains(got.Health.Reason, "worked") {
		t.Errorf("reason = %q, want it to say what it is claiming and when", got.Health.Reason)
	}
}

// A success is evidence with a shelf life, and the screen is where that
// matters most: it is read hours after the work, on a machine that has been
// asleep.
func TestTheScreenForgetsAnOldSuccess(t *testing.T) {
	stale := time.Now().UTC().Add(-metrics.SuccessWindow - time.Minute)
	atenea := seeded(t, func(s *metrics.Store) {
		s.Record(tried(stale, "graph.search", "api", true, "", ""))
	})

	got := shown(t, atenea.Status(), "graph.search")
	if got.Health.State != contract.HealthUnknown {
		t.Errorf("health = %v, want unknown: a stale success spoke for today",
			got.Health.State)
	}
}

// The screen is a summary, and a summary of a workspace is the worst of it. A
// provider that is warm on one repository and dead on another is not well.
func TestTheScreenTakesTheWorstRepositoryAndNamesIt(t *testing.T) {
	now := time.Now().UTC()
	atenea := seeded(t, func(s *metrics.Store) {
		s.Record(tried(now.Add(-time.Minute), "graph.search", "api", true, "", ""))
		for i := range 3 {
			s.Record(tried(now.Add(time.Duration(i)*time.Second), "graph.search", "scripts",
				false, "unavailable", "unavailable: no index on this one"))
		}
	})

	got := shown(t, atenea.Status(), "graph.search")
	if got.Health.State != contract.HealthDown {
		t.Fatalf("health = %v, want down: the healthy repository hid the dead one",
			got.Health.State)
	}
	if !strings.Contains(got.Health.Reason, "scripts") {
		t.Errorf("reason = %q, want the repository named: on a workspace, down where is the question",
			got.Health.Reason)
	}
}

// The record may drag a provider down past what the settings file claims,
// because a run of real failures on a real repository outranks an opinion.
func TestTheRecordOverrulesADeclaredAliveOnTheScreen(t *testing.T) {
	now := time.Now().UTC()
	atenea := seeded(t, func(s *metrics.Store) {
		for i := range 3 {
			s.Record(tried(now.Add(time.Duration(i)*time.Second), "ripgrep", "api",
				false, "timeout", "timeout: took too long"))
		}
	})

	got := shown(t, atenea.Status(), "ripgrep")
	if got.Health.State != contract.HealthDown {
		t.Errorf("health = %v, want down: three outages lost to a line in a config file",
			got.Health.State)
	}
	// Amber, not red. The funnel drops this provider and the work goes to
	// ripgrep, which is the system working. Red is for a capability with
	// nothing left to answer it -- see TestStatusReportsTheWholeCatalogue.
	if got := atenea.Status().Light; got != core.LightAmber {
		t.Errorf("light = %s, want amber: one dropped provider is not an outage", got)
	}
}

// The caption over the funnel used to be a constant that said the same thing
// on an empty base and on a machine running entirely on real figures -- which
// is the exact confusion it was written to prevent.
func TestTheFunnelLineFollowsTheBase(t *testing.T) {
	cold := build(t, catalog).Status().Funnel
	if !strings.Contains(cold, "nothing measured yet") {
		t.Errorf("cold caption = %q, want it to admit the base is empty", cold)
	}

	now := time.Now().UTC()
	warm := seeded(t, func(s *metrics.Store) {
		// Two, because two is what the funnel needs before it believes a
		// number over a declared estimate. One would leave the caption
		// claiming a measurement while every trace on the same machine still
		// says "estimated".
		s.Record(tried(now, "ripgrep", "api", true, "", ""))
		s.Record(tried(now.Add(time.Second), "ripgrep", "api", true, "", ""))
	}).Status().Funnel
	if warm == cold {
		t.Fatalf("the caption did not move when the base filled: %q", warm)
	}
	// One of the fixture's three implementations has been measured, and the
	// count is the whole point: it says which half of the screen to believe.
	if !strings.Contains(warm, "1 of 3") {
		t.Errorf("warm caption = %q, want it to count what is measured", warm)
	}
}

// The gap between the two is where the caption used to lie. A single call is
// on the record and prices nothing: the funnel spends its break-in turns
// buying a second one and says "estimated" in every trace while it does. A
// screen that called that measured contradicted every decision underneath it.
func TestOneCallDoesNotEarnTheCaption(t *testing.T) {
	now := time.Now().UTC()
	funnel := seeded(t, func(s *metrics.Store) {
		s.Record(tried(now, "ripgrep", "api", true, "", ""))
	}).Status().Funnel

	if strings.Contains(funnel, "1 of 3") {
		t.Errorf("caption = %q: one call is an attempt, not a price", funnel)
	}
	if !strings.Contains(funnel, "nothing measured yet") {
		t.Errorf("caption = %q, want it to admit nothing is priced yet", funnel)
	}
}

// Measuring can be switched off. Silence would be the wrong answer: with no
// base the declared estimates are not an opening position that real figures
// will overtake, they are the permanent ranking, and that is a bigger claim
// than any other state of this caption.
func TestWithMeasuringOffTheCaptionSaysSo(t *testing.T) {
	atenea := build(t, catalog+"\n[metrics]\nenabled = false\n")
	funnel := atenea.Status().Funnel

	if !strings.Contains(funnel, "cost") {
		t.Errorf("caption = %q, want the stages named whatever the base is doing", funnel)
	}
	if !strings.Contains(funnel, "measuring is off") {
		t.Errorf("caption = %q, want it to say the estimates are all there will ever be", funnel)
	}
	if strings.Contains(funnel, "nothing measured yet") {
		t.Errorf("caption = %q: 'yet' promises a base that is never coming", funnel)
	}
}
