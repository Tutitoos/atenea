package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// overlapped is a commission whose two waves each ran two steps at once: it
// cost 3.735s of tool time and took 2.689s of the operator's afternoon. The
// figures are the ones a real two-repository run produced on 2026-08-08.
func overlapped() *orchestrator.Result {
	step := func(id, phase string, d time.Duration) orchestrator.StepResult {
		return orchestrator.StepResult{
			Step:    contract.Step{ID: id, Capability: "code.search", Repository: "atenea"},
			Phase:   phase,
			Spent:   contract.Sample{Duration: d},
			Outcome: contract.Outcome{Verdict: contract.VerdictOK},
			Review:  orchestrator.Review{Child: contract.VerdictOK, Parent: contract.VerdictOK},
		}
	}
	return &orchestrator.Result{
		RunID:   "run-1",
		Task:    "TODO",
		Verdict: contract.VerdictOK,
		Steps: []orchestrator.StepResult{
			step("explore-atenea", orchestrator.PhaseExplore, 775*time.Millisecond),
			step("explore-lanplay", orchestrator.PhaseExplore, 778*time.Millisecond),
			step("search-atenea", orchestrator.PhaseWork, 1728*time.Millisecond),
			step("search-lanplay", orchestrator.PhaseWork, 454*time.Millisecond),
		},
		Phases: []orchestrator.Phase{
			{Name: orchestrator.PhaseExplore, Steps: 2, Spent: contract.Sample{Duration: 1553 * time.Millisecond}, Elapsed: 778 * time.Millisecond},
			{Name: orchestrator.PhaseWork, Steps: 2, Spent: contract.Sample{Duration: 2183 * time.Millisecond}, Elapsed: 1911 * time.Millisecond},
		},
		Spent:   contract.Sample{Duration: 3735 * time.Millisecond},
		Elapsed: 2689 * time.Millisecond,
	}
}

// A parallel run and a sequential one cost the same tool time and take very
// different amounts of the operator's life. A screen that prints only the sum
// cannot tell them apart, and the sum is the larger number: four steps of a
// 2.7s run are reported as 3.7s, which reads as the run being slower than it
// was. This is how the first wide wave ever dispatched on a real machine ran
// without anything on the screen saying so.
func TestTheScreenSaysWhatTheRunTookNotOnlyWhatItCost(t *testing.T) {
	var out bytes.Buffer
	printResult(&out, overlapped(), false)
	screen := out.String()

	if !strings.Contains(screen, "2.689s") {
		t.Errorf("the screen never says the run took 2.689s:\n%s", screen)
	}
	if !strings.Contains(screen, "3.735s") {
		t.Errorf("the screen no longer says what the run cost:\n%s", screen)
	}
	// The larger figure is the sum of concurrent steps, and a reader who takes
	// it for elapsed time has been told the run was a second slower than it
	// was. The word has to be on the line carrying it.
	line := lineHolding(t, screen, "3.735s")
	if !strings.Contains(line, "tool time") {
		t.Errorf("the summed figure is not named as tool time: %q", line)
	}
}

// Each phase says the same two things, and this is where the overlap is
// visible: the explore phase cost 1.553s across two steps and took 778ms,
// which is only possible because they ran together.
func TestEachPhaseSaysWhatItTookAndWhatItCost(t *testing.T) {
	var out bytes.Buffer
	printResult(&out, overlapped(), false)
	screen := out.String()

	explore := lineWith(t, screen, orchestrator.PhaseExplore)
	for _, want := range []string{"1.553s", "778ms"} {
		if !strings.Contains(explore, want) {
			t.Errorf("the explore line does not carry %s: %q", want, explore)
		}
	}
	work := lineWith(t, screen, orchestrator.PhaseWork)
	for _, want := range []string{"2.183s", "1.911s"} {
		if !strings.Contains(work, want) {
			t.Errorf("the work line does not carry %s: %q", want, work)
		}
	}
}

// The machine-readable view carries the same two figures, and one more the
// screen has no room for: when each step closed. A script that wants to know
// whether a wave overlapped needs the interval, and closed_at beside spent_ms
// is that interval -- without it the only way to see concurrency is to go and
// read the state directory by hand.
func TestTheJSONViewCarriesTheWallAndTheCloseTimes(t *testing.T) {
	result := overlapped()
	closed := time.Now()
	for i := range result.Steps {
		result.Steps[i].ClosedAt = closed
	}

	_, decoded := jsonOf(t, result)

	if decoded.ElapsedMS != 2689 {
		t.Errorf("elapsed_ms = %d, want 2689", decoded.ElapsedMS)
	}
	if len(decoded.Phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(decoded.Phases))
	}
	if decoded.Phases[0].ElapsedMS != 778 {
		t.Errorf("explore elapsed_ms = %d, want 778", decoded.Phases[0].ElapsedMS)
	}
	for _, step := range decoded.Steps {
		if step.ClosedAt.IsZero() {
			t.Errorf("step %s crosses the wire with no close time", step.ID)
		}
	}
}

// lineHolding returns the one line of the screen carrying a marker anywhere in
// it, which is how a figure is found when the label is not at the front.
func lineHolding(t *testing.T, screen, marker string) string {
	t.Helper()
	for line := range strings.SplitSeq(screen, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("no line of the screen holds %q:\n%s", marker, screen)
	return ""
}
