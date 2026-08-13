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
