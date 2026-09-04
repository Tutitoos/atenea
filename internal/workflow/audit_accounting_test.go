package workflow_test

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// TestAuditAutomaticRetryReusesSpentGrant checks the regression scenario: audit automatic retry reuses spent grant.
func TestAuditAutomaticRetryReusesSpentGrant(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(), declared("work", charged(t, dir, "work", 100, 20, 0.4, "synthetic"), config.PoolAgent), declared("judge", refusesOnce(t, dir, "judge", "incorrect"), config.PoolReview))
	g := graphOf(step("w", "work", nil), reviewing(step("j", "judge", nil), "w"))
	g.GrantUSD = 0.5
	g.Steps[0].Permission.BudgetUSD = 0.5
	run, err := h.engine.Start(t.Context(), g)
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("expected budget refusal: %v", err)
	}
	loaded, loadErr := h.state.Load(t.Context(), run.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	spend := loaded.Spend()
	total := spend.SupersededUSD
	if spend.USD != nil {
		total += *spend.USD
	}
	if total > g.GrantUSD+1e-9 {
		t.Fatalf("grant exceeded: %v", total)
	}
}

// TestConcurrentClaimsCannotOverreserve checks the regression scenario: concurrent claims cannot overreserve.
func TestConcurrentClaimsCannotOverreserve(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(), declared("work", stub(t, dir, "work", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))
	g := graphOf(step("a", "work", nil), step("b", "work", nil))
	g.GrantUSD = 1
	run, _, err := h.engine.Create(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- h.state.Claim(t.Context(), run.ID, id, "claim-"+id, 1, time.Now(), 123, 0.6)
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful reservations=%d", successes)
	}
}

// TestReviewInvalidChargeIsNotPersistedAsCredit checks the regression scenario: review invalid charge is not persisted as credit.
func TestReviewInvalidChargeIsNotPersistedAsCredit(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(), declared("work", charged(t, dir, "work", 10, 2, -10, "synthetic"), config.PoolAgent))
	g := graphOf(step("w", "work", nil))
	g.GrantUSD = 1
	g.Steps[0].Permission.BudgetUSD = 1
	run, err := h.engine.Start(t.Context(), g)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range loaded.Steps {
		if row.Spent.USD != nil && *row.Spent.USD < 0 {
			t.Fatalf("workflow persisted invalid credit: %v", *row.Spent.USD)
		}
	}
}

// TestInvalidSettlementKeepsBudgetReserved checks the regression scenario: invalid settlement keeps budget reserved.
func TestInvalidSettlementKeepsBudgetReserved(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(), declared("work", charged(t, dir, "work", 10, 2, 0.1, "synthetic"), config.PoolAgent))
	g := graphOf(step("a", "work", nil), step("b", "work", nil))
	g.GrantUSD = 1
	run, _, err := h.engine.Create(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.state.Claim(t.Context(), run.ID, "a", "a1", 1, time.Now(), 123, 1); err != nil {
		t.Fatal(err)
	}
	negative := -10.0
	err = h.state.Finish(t.Context(), run.ID, "a", workflow.StatusIncomplete, contract.Report{Spent: contract.Charge{USD: &negative, PricedBy: "fixture"}}, time.Now())
	if err == nil {
		t.Fatal("invalid settlement accepted")
	}
	if err = h.state.Claim(t.Context(), run.ID, "b", "b1", 1, time.Now(), 123, 0.1); contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("hold released: %v", err)
	}
}

// TestSettlementRollsBackWhenReservationWriteFails keeps outcome and cost atomic.
func TestSettlementRollsBackWhenReservationWriteFails(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(), declared("work", answers(t, dir, "work"), config.PoolAgent))
	g := graphOf(step("a", "work", nil))
	g.GrantUSD = 1
	run, _, err := h.engine.Create(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.state.Claim(t.Context(), run.ID, "a", "a1", 1, time.Now(), 123, 1); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "traces.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TRIGGER reject_settlement BEFORE UPDATE ON workflow_reservation BEGIN SELECT RAISE(ABORT,'settlement fixture'); END`); err != nil {
		t.Fatal(err)
	}
	usd := 0.2
	report := contract.Report{Verdict: contract.VerdictOK, Spent: contract.Charge{USD: &usd, PricedBy: "fixture"}}
	if err = h.state.Finish(t.Context(), run.ID, "a", workflow.StatusOK, report, time.Now()); err == nil {
		t.Fatal("settlement failure not observed")
	}
	loaded, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Steps[0].Status != workflow.StatusRunning || loaded.Steps[0].Spent.USD != nil {
		t.Fatalf("partial settlement: %+v", loaded)
	}
	if _, err = db.Exec(`DROP TRIGGER reject_settlement`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = h.state.Finish(t.Context(), run.ID, "a", workflow.StatusOK, report, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var reserved float64
	if err = db.QueryRow(`SELECT sum(reserved_usd) FROM workflow_reservation WHERE workflow_id=?`, run.ID).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 0.2 {
		t.Fatalf("repeated settlement changed spend: %v", reserved)
	}
}
