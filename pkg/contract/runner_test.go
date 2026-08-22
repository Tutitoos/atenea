package contract_test

import (
	"context"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestCostObserverStopsAnOverBudgetUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := 0
	ctx = contract.WithCostObserver(ctx, func(update contract.CostUpdate) bool {
		called++
		return update.SpentUSD <= 0.25
	})

	if contract.ReportCost(ctx, contract.CostUpdate{SpentUSD: 0.26, Known: true}) {
		t.Fatal("an over-budget update was allowed to continue")
	}
	if called != 1 {
		t.Fatalf("observer calls = %d, want 1", called)
	}
	if !contract.ReportCost(ctx, contract.CostUpdate{SpentUSD: 0, Known: false}) {
		t.Fatal("an unknown-cost update was unexpectedly rejected by the test observer")
	}
}

func TestReportCostWithoutObserverAllowsTheProvider(t *testing.T) {
	if !contract.ReportCost(context.Background(), contract.CostUpdate{SpentUSD: 99, Known: true}) {
		t.Fatal("a runner without an observer was stopped")
	}
	if contract.CostObserverFromContext(context.Background()) != nil {
		t.Fatal("a context without an observer returned one")
	}
}
