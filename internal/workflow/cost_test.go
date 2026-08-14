package workflow_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"

	_ "modernc.org/sqlite" // database/sql driver
)

// priced writes one finished step of a type, with a grant and what it spent.
// A nil spend is a run nobody could price.
func priced(t *testing.T, store *workflow.Store, id, repository, typeName string,
	grant float64, spent *float64) {
	t.Helper()
	one := step("a", typeName, nil)
	one.Permission.BudgetUSD = grant
	plan, err := workflow.Compile(graphOf(one),
		[]config.AgentType{declared(typeName, "/bin/true", config.PoolAgent)})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := store.Create(t.Context(), id, plan, repository, time.Now(), 1); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Claim(t.Context(), id, "a", "tr-"+id, 1, time.Now(), 1); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	report := contract.Report{Result: map[string]any{"ok": true}, Verdict: contract.VerdictOK}
	if spent != nil {
		report.Spent = contract.Charge{USD: spent, InputTokens: 10, PricedBy: "a test"}
	}
	if err := store.Finish(t.Context(), id, "a", workflow.StatusOK, report, time.Now()); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

func usd(v float64) *float64 { return &v }

func costStore(t *testing.T) *workflow.Store {
	t.Helper()
	store, err := workflow.Open(t.Context(), filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// A run that spent its whole grant is a lower bound, not a measurement: it
// stopped because it ran out, and nobody knows what the work costs. Averaging
// those in is how a measured table quietly becomes the under-estimate it was
// built to replace -- so they are excluded, and counted where the reader sees
// them.
func TestARunThatStoppedAtItsCeilingIsExcludedAndCounted(t *testing.T) {
	store := costStore(t)
	priced(t, store, "wf-1", "atenea", "explore", 2.50, usd(1.26))
	priced(t, store, "wf-2", "atenea", "explore", 2.50, usd(2.16))
	priced(t, store, "wf-3", "atenea", "explore", 2.50, usd(1.63))
	// Spent its grant to the cent.
	priced(t, store, "wf-4", "atenea", "explore", 0.60, usd(0.61))

	table, err := store.CostByType(t.Context(), "atenea")
	if err != nil {
		t.Fatalf("CostByType: %v", err)
	}
	got := table.Types["explore"]
	if got.N != 3 {
		t.Errorf("n = %d, want the 3 clean runs", got.N)
	}
	if got.AtCeiling != 1 {
		t.Errorf("at ceiling = %d, want the censored run counted", got.AtCeiling)
	}
	if got.MedianUSD != 1.63 {
		t.Errorf("median = %.2f, want 1.63: the ceilinged run must not drag it down", got.MedianUSD)
	}
	if got.MinUSD != 1.26 || got.MaxUSD != 2.16 {
		t.Errorf("range = $%.2f-$%.2f, want the clean rows only", got.MinUSD, got.MaxUSD)
	}
}

// A run nobody could price -- a turn killed at its timeout -- is not a cheap
// run. It leaves the median alone and is reported separately.
func TestAnUnpricedRunIsNotAZeroDollarRun(t *testing.T) {
	store := costStore(t)
	priced(t, store, "wf-1", "atenea", "explore", 2.50, usd(1.40))
	priced(t, store, "wf-2", "atenea", "explore", 2.50, nil)

	table, err := store.CostByType(t.Context(), "atenea")
	if err != nil {
		t.Fatalf("CostByType: %v", err)
	}
	got := table.Types["explore"]
	if got.N != 1 || got.MedianUSD != 1.40 {
		t.Errorf("median $%.2f over n=%d, want $1.40 over 1", got.MedianUSD, got.N)
	}
	if got.Unmeasured != 1 {
		t.Errorf("unmeasured = %d, want the unpriced run counted rather than averaged in", got.Unmeasured)
	}
}

// A repository with no rows of its own is told the truth: here is what the
// machine knows, and it is not scoped to your tree.
func TestARepositoryWithNoRunsFallsBackToMachineWideAndSaysSo(t *testing.T) {
	store := costStore(t)
	priced(t, store, "wf-1", "somewhere-else", "explore", 2.50, usd(1.40))

	table, err := store.CostByType(t.Context(), "a-tree-nobody-ran")
	if err != nil {
		t.Fatalf("CostByType: %v", err)
	}
	if table.Repository != "" {
		t.Errorf("repository = %q, want empty: these rows are not from that tree", table.Repository)
	}
	if got := table.Types["explore"]; got.N != 1 {
		t.Errorf("n = %d, want the machine-wide row", got.N)
	}
}

// Rows written against one repository do not answer for another when that
// other has rows of its own.
func TestCostsAreScopedToTheRepositoryThatHasThem(t *testing.T) {
	store := costStore(t)
	priced(t, store, "wf-1", "atenea", "explore", 2.50, usd(1.60))
	priced(t, store, "wf-2", "tiny-repo", "explore", 2.50, usd(0.10))

	table, err := store.CostByType(t.Context(), "tiny-repo")
	if err != nil {
		t.Fatalf("CostByType: %v", err)
	}
	if table.Repository != "tiny-repo" {
		t.Errorf("repository = %q, want the scope it was asked for", table.Repository)
	}
	if got := table.Types["explore"]; got.N != 1 || got.MedianUSD != 0.10 {
		t.Errorf("median $%.2f over n=%d, want $0.10 over 1", got.MedianUSD, got.N)
	}
}

// The column was added after machines had already recorded runs. CREATE TABLE
// IF NOT EXISTS does nothing to a table that exists, so without the migration
// every one of those machines fails on the first write.
func TestADatabaseWrittenBeforeTheRepositoryColumnStillOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := workflow.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Put the file back the way a machine that predates the column left it.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), "ALTER TABLE workflow DROP COLUMN repository"); err != nil {
		t.Fatalf("un-migrating: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store, err := workflow.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer func() { _ = store.Close() }()
	priced(t, store, "wf-1", "atenea", "explore", 2.50, usd(1.40))
	table, err := store.CostByType(t.Context(), "atenea")
	if err != nil {
		t.Fatalf("CostByType: %v", err)
	}
	if got := table.Types["explore"]; got.N != 1 {
		t.Errorf("n = %d, want the row this store just wrote", got.N)
	}
}
