package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// jsonOf runs printResultJSON and hands back both the raw text (for the tests
// that care whether a key was written at all) and the decoded shape (for the
// tests that care what a key holds).
func jsonOf(t *testing.T, result *orchestrator.Result) (string, jsonResult) {
	t.Helper()
	var out bytes.Buffer
	printResultJSON(&out, result)
	body := out.String()
	var decoded jsonResult
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("--json wrote invalid json: %v\n%s", err, body)
	}
	return body, decoded
}

// A --json run is read by a program, not an eye: the output has to be the
// one thing every consumer can rely on to parse, and it has to carry the
// same facts printResult puts on a screen.
func TestJSONOutputIsWellFormedAndComplete(t *testing.T) {
	_, decoded := jsonOf(t, receipt(0.0234))

	if decoded.Run != "run-1" {
		t.Errorf("run = %q, want run-1", decoded.Run)
	}
	if decoded.Task != "find TODO" {
		t.Errorf("task = %q, want %q", decoded.Task, "find TODO")
	}
	if decoded.Verdict != "ok" {
		t.Errorf("verdict = %q, want ok", decoded.Verdict)
	}
	if len(decoded.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(decoded.Steps))
	}
	if decoded.Steps[0].ID != "search-current" {
		t.Errorf("steps[0].id = %q, want search-current", decoded.Steps[0].ID)
	}
}

// Same money rule as the prose renderer: a charge is reported.
func TestJSONReportsWhatWasCharged(t *testing.T) {
	body, decoded := jsonOf(t, receipt(0.0234))

	if !strings.Contains(body, `"charged_usd": 0.0234`) {
		t.Errorf("the json does not say what it cost:\n%s", body)
	}
	if decoded.ChargedUSD == nil || *decoded.ChargedUSD != 0.0234 {
		t.Errorf("charged_usd = %v, want 0.0234", decoded.ChargedUSD)
	}
}

// A run nobody priced carries no charged_usd key at all -- not a zero, an
// absence -- the same distinction printResult draws by leaving the line off
// the screen entirely rather than printing "$0.0000".
func TestARunNobodyPricedMentionsNoMoneyInJSON(t *testing.T) {
	body, decoded := jsonOf(t, receipt(0))

	if strings.Contains(body, "charged_usd") {
		t.Errorf("a run nobody priced wrote a charged_usd key:\n%s", body)
	}
	if decoded.ChargedUSD != nil {
		t.Errorf("charged_usd = %v, want it absent", *decoded.ChargedUSD)
	}
}

// And the other zero: a provider that answered, and answered nothing.
//
// These two documents used to be byte-for-byte identical, because omitempty on
// a float drops a measured zero exactly as it drops an absence. A script
// reading this output could not tell a free provider from a silent one, which
// is the same collapse contract.Charge.USD carries a pointer to avoid.
func TestAProviderThatChargedNothingSaysSoInJSON(t *testing.T) {
	body, decoded := jsonOf(t, pricedAtZero())

	if !strings.Contains(body, `"charged_usd": 0`) {
		t.Errorf("a measured zero did not reach the document:\n%s", body)
	}
	if decoded.ChargedUSD == nil {
		t.Fatal("charged_usd is absent on a run the provider priced at zero")
	}
	if *decoded.ChargedUSD != 0 {
		t.Errorf("charged_usd = %v, want a measured 0", *decoded.ChargedUSD)
	}
}

// The per-step breakdown is what tells a script which provider to go and
// look at, same reasoning as the prose trace.
func TestJSONNamesTheStepThatPaidAndOverspent(t *testing.T) {
	over := receipt(0.30)
	over.Steps[0].Step.Permission.BudgetUSD = 0.25

	body, decoded := jsonOf(t, over)

	if len(decoded.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(decoded.Steps))
	}
	step := decoded.Steps[0]
	if step.ChargedUSD == nil || *step.ChargedUSD != 0.30 {
		t.Errorf("steps[0].charged_usd = %v, want 0.30", step.ChargedUSD)
	}
	if diff := step.OverspentUSD - 0.05; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("steps[0].overspent_usd = %v, want 0.05", step.OverspentUSD)
	}
	if !strings.Contains(body, `"overspent_usd"`) {
		t.Errorf("the json does not name the overspend:\n%s", body)
	}
}

// A step that stayed inside its share writes no overspent_usd key, matching
// printResult's "the line only exists for the run where it is not zero."
func TestAStepWithinItsShareMentionsNoOverspendInJSON(t *testing.T) {
	body, _ := jsonOf(t, receipt(0.0234))

	if strings.Contains(body, "overspent_usd") {
		t.Errorf("a step within its share wrote an overspent_usd key:\n%s", body)
	}
}

// Matches are counted out of a split-up commission. A direct ask -- no work
// phase in the receipt -- has an answer rather than a tally, and the key has
// to be gone, not zero: printResult skips the line for exactly this reason.
func TestJSONMatchesOmittedForADirectAsk(t *testing.T) {
	result := receipt(0)
	result.Phases = []orchestrator.Phase{{Name: orchestrator.PhaseAsk, Steps: 1}}

	body, decoded := jsonOf(t, result)

	if strings.Contains(body, `"matches"`) {
		t.Errorf("a direct ask wrote a matches key:\n%s", body)
	}
	if decoded.Matches != nil {
		t.Errorf("matches = %v, want nil (omitted)", *decoded.Matches)
	}
}

// A split commission that genuinely found nothing still writes matches: 0 --
// the pointer is what tells "counted, zero" apart from "not counted", which a
// plain int zero value could never do once decoded.
func TestJSONMatchesPresentAndZeroForASplitCommissionWithNoHits(t *testing.T) {
	result := receipt(0)
	result.Phases = []orchestrator.Phase{{Name: orchestrator.PhaseWork, Steps: 1}}
	result.Matches = 0

	body, decoded := jsonOf(t, result)

	if !strings.Contains(body, `"matches": 0`) {
		t.Errorf("a split commission with no hits omitted matches:\n%s", body)
	}
	if decoded.Matches == nil || *decoded.Matches != 0 {
		t.Errorf("matches = %v, want a present 0", decoded.Matches)
	}
}

// A canceled step has no review to report: there was no opinion on either
// side, and printResult skips the whole block for the same reason. The json
// twin has to leave the key out rather than write zero-value verdicts that
// would read as an opinion nobody holds.
func TestJSONCanceledStepOmitsReview(t *testing.T) {
	result := receipt(0)
	result.Steps[0].FailureKind = contract.FailureCanceled
	result.Steps[0].Failure = "canceled: stopped before it finished"
	result.Steps[0].Review = orchestrator.Review{}

	body, decoded := jsonOf(t, result)

	if strings.Contains(body, `"review"`) {
		t.Errorf("a canceled step wrote a review key:\n%s", body)
	}
	if decoded.Steps[0].Review != nil {
		t.Errorf("steps[0].review = %+v, want nil", decoded.Steps[0].Review)
	}
	if decoded.Steps[0].FailureKind != "canceled" {
		t.Errorf("steps[0].failure_kind = %q, want canceled", decoded.Steps[0].FailureKind)
	}
}

// A step that finished carries its review: the funnel's own record of what
// the child said and what the parent made of it.
func TestJSONFinishedStepCarriesItsReview(t *testing.T) {
	_, decoded := jsonOf(t, receipt(0))

	step := decoded.Steps[0]
	if step.Review == nil {
		t.Fatalf("steps[0].review = nil, want a review")
	}
	if step.Review.Child != "ok" || step.Review.Parent != "ok" {
		t.Errorf("review = %+v, want child=ok parent=ok", step.Review)
	}
}

// The answer a capability produced rides on the step, exactly as printAnswer
// shows it -- a script reading json does not get a lesser copy.
func TestJSONStepCarriesTheAnswer(t *testing.T) {
	result := receipt(0)
	result.Steps[0].Outcome.Result = map[string]any{"matches": []any{"main.go:3"}}

	_, decoded := jsonOf(t, result)

	got, ok := decoded.Steps[0].Result["matches"].([]any)
	if !ok || len(got) != 1 || got[0] != "main.go:3" {
		t.Errorf("steps[0].result = %+v, want the answer untouched", decoded.Steps[0].Result)
	}
}

// A candidate the funnel dropped is why the run picked whoever it picked.
// printResult's trace prints one line per drop; json carries the same list.
func TestJSONCarriesDroppedCandidates(t *testing.T) {
	result := receipt(0)
	result.Steps[0].Decision.Stages = []selector.Stage{{
		Name: "health",
		Dropped: []selector.Drop{
			{Implementation: "claude.search", Reason: "no attached runner serves it"},
		},
	}}

	body, decoded := jsonOf(t, result)

	if len(decoded.Steps[0].Dropped) != 1 {
		t.Fatalf("dropped = %d, want 1", len(decoded.Steps[0].Dropped))
	}
	drop := decoded.Steps[0].Dropped[0]
	if drop.Implementation != "claude.search" || drop.Reason != "no attached runner serves it" {
		t.Errorf("dropped[0] = %+v", drop)
	}
	if !strings.Contains(body, "claude.search") {
		t.Errorf("the json does not name the dropped candidate:\n%s", body)
	}
}

func TestDetectionJSONCarriesSourceAndProbeDetails(t *testing.T) {
	detection := core.Detection{
		PID:      1234,
		Settings: "/tmp/atenea.toml",
		Servers: []core.ServerProbe{{
			ID: "filesystem", Transport: "stdio", Where: "local", Dashboard: "http://localhost:1",
			Expose: "raw", OK: true, Name: "fs", Version: "1.2", Took: 1500 * time.Millisecond, PinnedPath: true,
		}, {
			ID: "broken", Transport: "stdio", Where: "local", Reason: "not found", Took: 2 * time.Millisecond,
		}},
		Indexes: []core.IndexReport{{Repository: "api", Provider: "serena", Ready: true}, {
			Repository: "web", Provider: "serena", Hint: "index missing", Err: "probe failed",
		}},
	}
	var out bytes.Buffer
	printDetectionJSON(&out, detection, answeredBy{Service: true, PID: 99})
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("detection json invalid: %v", err)
	}
	answered := decoded["answered_by"].(map[string]any)
	if answered["by"] != "service" || answered["pid"].(float64) != 99 {
		t.Fatalf("answered_by = %#v", answered)
	}
	if len(decoded["servers"].([]any)) != 2 || len(decoded["indexes"].([]any)) != 2 {
		t.Fatalf("detection arrays = %#v", decoded)
	}
	if !strings.Contains(out.String(), "filesystem") || !strings.Contains(out.String(), "probe failed") {
		t.Fatalf("detection json omitted probe details: %s", out.String())
	}
	out.Reset()
	printDetectionJSON(&out, detection, answeredBy{Elsewhere: "/other.toml", Refused: true})
	if !strings.Contains(out.String(), "other.toml") || !strings.Contains(out.String(), "refused") {
		t.Fatalf("fallback detection json = %s", out.String())
	}
}

// Duration crosses the wire in milliseconds, an ordinary integer, not a raw
// time.Duration -- a script should not have to know Go's nanosecond encoding
// to read how long a run took.
func TestJSONDurationIsMilliseconds(t *testing.T) {
	result := receipt(0)
	result.Spent.Duration = 2 * time.Second

	_, decoded := jsonOf(t, result)

	if decoded.SpentMS != 2000 {
		t.Errorf("spent_ms = %d, want 2000", decoded.SpentMS)
	}
}
