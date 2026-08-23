package workflow_test

import (
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// A step whose agent reported nothing reads back unmeasured. The zero Charge
// that means "nobody could say" must survive a write and a read as exactly
// that -- not as a measured, real-looking zero.
func TestAStepWithNoChargeReadsBackUnmeasured(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Claim(t.Context(), run.ID, "a", "tr-1", 1, time.Now(), 4242); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	report := contract.Report{Result: map[string]any{"ok": true}, Verdict: contract.VerdictOK}
	if err := h.state.Finish(t.Context(), run.ID, "a", workflow.StatusOK, report, time.Now()); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	row := stepOf(t, loaded, "a")
	if row.Spent.Measured() {
		t.Fatalf("Spent = %+v, want unmeasured: nothing on the report said a charge", row.Spent)
	}
}

func TestBudgetForecastAndRouteReadBackWithTheStep(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))
	s := step("a", "worker", nil)
	s.BudgetEstimateUSD = 0.40
	s.BudgetMinimumUSD = 0.32
	s.BudgetSource = "measured startup floor plus answer headroom"
	s.Route = &contract.Route{Model: "claude-sonnet-5", Fallbacks: []string{"claude-haiku-4-5"}, Backend: "claude"}
	run, _, err := h.engine.Create(t.Context(), graphOf(s))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := stepOf(t, loaded, "a").Step
	if got.BudgetEstimateUSD != 0.40 || got.BudgetMinimumUSD != 0.32 || got.BudgetSource == "" {
		t.Fatalf("budget forecast = %.2f/%.2f/%q", got.BudgetEstimateUSD, got.BudgetMinimumUSD, got.BudgetSource)
	}
	if got.Route == nil || got.Route.Model != "claude-sonnet-5" || len(got.Route.Fallbacks) != 1 {
		t.Fatalf("route = %+v", got.Route)
	}
}

func TestCostByModelReadsSuccessfulRoutedSteps(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))
	start := time.Now().Add(-2 * time.Second)
	s := step("a", "worker", nil)
	s.Route = &contract.Route{Model: "claude-sonnet-5"}
	run, _, err := h.engine.Create(t.Context(), graphOf(s))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Claim(t.Context(), run.ID, "a", "tr-1", 1, start, 4242); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	usd := 0.42
	if err := h.state.Finish(t.Context(), run.ID, "a", workflow.StatusOK,
		contract.Report{Verdict: contract.VerdictOK, Spent: contract.Charge{USD: &usd, PricedBy: "anthropic"}},
		time.Now()); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	table, err := h.state.CostByModel(t.Context(), "")
	if err != nil {
		t.Fatalf("CostByModel: %v", err)
	}
	seen, ok := table.Performance("worker", "claude-sonnet-5")
	if !ok || seen.N != 1 || seen.MedianUSD != usd {
		t.Fatalf("model performance = %+v, found=%v", seen, ok)
	}
}

func TestCostByModelNormalizesReaderIntoExploreRole(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("reader", stub(t, dir, "reader", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))
	s := step("a", "reader", nil)
	s.Route = &contract.Route{Model: "claude-haiku-4-5"}
	run, _, err := h.engine.Create(t.Context(), graphOf(s))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Claim(t.Context(), run.ID, "a", "tr-1", 1, time.Now(), 4242); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	usd := 0.11
	if err := h.state.Finish(t.Context(), run.ID, "a", workflow.StatusOK,
		contract.Report{Verdict: contract.VerdictOK, Spent: contract.Charge{USD: &usd, PricedBy: "anthropic"}},
		time.Now()); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	table, err := h.state.CostByModel(t.Context(), "")
	if err != nil {
		t.Fatalf("CostByModel: %v", err)
	}
	if _, ok := table.Performance("reader", "claude-haiku-4-5"); ok {
		t.Fatal("reader role should be normalized to explore")
	}
	seen, ok := table.Performance("explore", "claude-haiku-4-5")
	if !ok || seen.N != 1 || seen.MedianUSD != usd {
		t.Fatalf("normalized model performance = %+v, found=%v", seen, ok)
	}
}

// A step that reported a real zero-dollar charge -- tokens all zero, but a
// price behind it -- reads back measured. That is a different fact from
// nothing having been reported, and the round trip must not collapse the two.
func TestAStepWithARealZeroDollarChargeReadsBackMeasured(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Claim(t.Context(), run.ID, "a", "tr-1", 1, time.Now(), 4242); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	zero := 0.0
	report := contract.Report{
		Result:  map[string]any{"ok": true},
		Verdict: contract.VerdictOK,
		Spent:   contract.Charge{USD: &zero, PricedBy: "anthropic"},
	}
	if err := h.state.Finish(t.Context(), run.ID, "a", workflow.StatusOK, report, time.Now()); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	row := stepOf(t, loaded, "a")
	if !row.Spent.Measured() {
		t.Fatalf("Spent = %+v, want measured: a real $0.00 with a price behind it "+
			"is not the same as nothing reported", row.Spent)
	}
	if row.Spent.USD == nil || *row.Spent.USD != 0 {
		t.Fatalf("USD = %v, want a real zero, not absent", row.Spent.USD)
	}
	if row.Spent.PricedBy != "anthropic" {
		t.Fatalf("PricedBy = %q, want the label preserved through the round trip", row.Spent.PricedBy)
	}
}

// A step reclaimed for a second attempt does not keep the first attempt's
// charge. Claim starts the row over, money included -- otherwise a redo that
// fails before reporting anything would still show what its first try cost.
func TestARedoneStepDoesNotKeepItsPreviousAttemptsCharge(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Claim(t.Context(), run.ID, "a", "tr-1", 1, time.Now(), 4242); err != nil {
		t.Fatalf("Claim (attempt 1): %v", err)
	}
	usd := 0.5
	first := contract.Report{
		Verdict: contract.VerdictOK,
		Spent:   contract.Charge{InputTokens: 100, USD: &usd, PricedBy: "anthropic"},
	}
	if err := h.state.Finish(t.Context(), run.ID, "a", workflow.StatusOK, first, time.Now()); err != nil {
		t.Fatalf("Finish (attempt 1): %v", err)
	}

	if err := h.state.Claim(t.Context(), run.ID, "a", "tr-2", 2, time.Now(), 4242); err != nil {
		t.Fatalf("Claim (attempt 2): %v", err)
	}
	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	row := stepOf(t, loaded, "a")
	if row.Spent.Measured() {
		t.Fatalf("Spent = %+v after a fresh Claim, want the first attempt's charge cleared", row.Spent)
	}
}

// A partial report's completeness and stopped_at survive a write and a read
// back intact -- the pair the reserved-answer split exists to record, and
// the two are read together: a coverage figure with no stopped_at column
// would be a number nobody could act on.
func TestAPartialReportsCompletenessAndStoppedAtRoundTrip(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Claim(t.Context(), run.ID, "a", "tr-1", 1, time.Now(), 4242); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	completeness := 0.55
	report := contract.Report{
		Result:       map[string]any{"ok": true},
		Verdict:      contract.VerdictOK,
		Completeness: &completeness,
		StoppedAt:    "the last two files, cut off by the read budget",
	}
	if err := h.state.Finish(t.Context(), run.ID, "a", workflow.StatusOK, report, time.Now()); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	row := stepOf(t, loaded, "a")
	if row.Completeness == nil || *row.Completeness != 0.55 {
		t.Fatalf("Completeness = %v, want 0.55", row.Completeness)
	}
	if row.StoppedAt != "the last two files, cut off by the read budget" {
		t.Fatalf("StoppedAt = %q, want the reason preserved through the round trip", row.StoppedAt)
	}
}

// A report that never claimed a completeness stores NULL and reads back
// nil, the same nullability spent_usd already has -- a step that made no
// coverage claim must not collapse into one that claimed a perfect 1.
func TestAReportWithNoCompletenessReadsBackNil(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Claim(t.Context(), run.ID, "a", "tr-1", 1, time.Now(), 4242); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	report := contract.Report{Result: map[string]any{"ok": true}, Verdict: contract.VerdictOK}
	if err := h.state.Finish(t.Context(), run.ID, "a", workflow.StatusOK, report, time.Now()); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	row := stepOf(t, loaded, "a")
	if row.Completeness != nil {
		t.Fatalf("Completeness = %v, want nil: nothing claimed a coverage figure", *row.Completeness)
	}
	if row.StoppedAt != "" {
		t.Fatalf("StoppedAt = %q, want empty", row.StoppedAt)
	}
}
