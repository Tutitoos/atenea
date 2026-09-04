package workflow_test

import (
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

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
