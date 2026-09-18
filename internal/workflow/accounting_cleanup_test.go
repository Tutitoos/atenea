package workflow_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type accountingDispatcher struct {
	ids            atomic.Int32
	siblingStarted atomic.Bool
	failFinish     bool
}

// NextID returns a deterministic reservation identifier for fault injection.
func (d *accountingDispatcher) NextID() string { return fmt.Sprintf("id%d", d.ids.Add(1)) }

// Dispatch completes the measured call before the dependent accounting failure.
// The independent sibling remains active until the engine cancels it.
func (d *accountingDispatcher) Dispatch(ctx context.Context, call agent.Dispatch) (contract.Report, contract.Assignment, error) {
	switch call.ID {
	case "id1":
		usd := 0.2
		return contract.Report{Verdict: contract.VerdictOK, Result: map[string]any{"ok": true}, Spent: contract.Charge{USD: &usd, PricedBy: "fixture"}}, contract.Assignment{}, nil
	case "id2":
		d.siblingStarted.Store(true)
		<-ctx.Done()
		return contract.Report{}, contract.Assignment{}, contract.Fail(contract.FailureCanceled, "fixture canceled")
	case "id3":
		if !d.failFinish {
			return contract.Report{}, contract.Assignment{}, contract.Fail(contract.FailureInvalidInput, "claim fixture unexpectedly dispatched")
		}
		usd := -1.0
		return contract.Report{Verdict: contract.VerdictOK, Spent: contract.Charge{USD: &usd, PricedBy: "fixture"}}, contract.Assignment{}, nil
	}
	return contract.Report{}, contract.Assignment{}, contract.Fail(contract.FailureInvalidInput, "unexpected fixture dispatch %s", call.ID)
}

// TestAccountingFailuresReleaseOwnership covers Claim and Finish with active siblings.
func TestAccountingFailuresReleaseOwnership(t *testing.T) {
	for _, finish := range []bool{false, true} {
		t.Run(fmt.Sprint(finish), func(t *testing.T) {
			dir := t.TempDir()
			worker := declared("work", answers(t, dir, "work"), config.PoolAgent)
			h := newHarnessOver(t, dir, noCeiling(), worker)
			d := &accountingDispatcher{failFinish: finish}
			engine, err := workflow.New(workflow.Options{Runner: d, Store: h.state, Types: []config.AgentType{worker}})
			if err != nil {
				t.Fatal(err)
			}
			if !finish {
				db, err := sql.Open("sqlite", filepath.Join(dir, "traces.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err = db.Exec(`CREATE TRIGGER reject_third BEFORE INSERT ON workflow_reservation WHEN NEW.trace_id='id3' BEGIN SELECT RAISE(ABORT,'claim fixture'); END`); err != nil {
					t.Fatal(err)
				}
			}
			g := graphOf(step("a", "work", nil), step("c", "work", nil), step("b", "work", []string{"a"}))
			g.GrantUSD = 1
			run, err := engine.Start(t.Context(), g)
			if err == nil {
				t.Fatal("accounting failure was accepted")
			}
			if !d.siblingStarted.Load() {
				t.Fatal("active sibling fixture was not dispatched")
			}
			loaded, err := h.state.Load(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.WriterPID != 0 || loaded.Closed || loaded.Stop != workflow.StopUnjudged {
				t.Fatalf("ownership leaked: %+v", loaded)
			}
			measured := 0
			for _, row := range loaded.Steps {
				if row.WriterPID != 0 || row.Status == workflow.StatusRunning {
					t.Fatalf("active step leaked: %+v", row)
				}
				if row.Spent.USD != nil {
					if *row.Spent.USD != 0.2 {
						t.Fatal("invalid cost persisted")
					}
					measured++
				}
			}
			if measured != 1 {
				t.Fatalf("valid completed charge lost: %+v", loaded)
			}
			if row := stepOf(t, loaded, "a"); row.Spent.USD == nil || *row.Spent.USD != 0.2 {
				t.Fatalf("completed first step lost its charge: %+v", row)
			}
			if row := stepOf(t, loaded, "c"); row.Status != workflow.StatusInterrupted {
				t.Fatalf("active sibling was not interrupted: %+v", row)
			}
			// A fresh real engine may refuse a redo whose old reservations exhaust
			// the grant, but it must not duplicate the charge or retain ownership.
			_, _ = h.engine.Resume(t.Context(), run.ID, []string{"a", "b", "c"})
			after, err := h.state.Load(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			spend := after.Spend()
			total := spend.SupersededUSD
			if spend.USD != nil {
				total += *spend.USD
			}
			if total != 0.2 || after.WriterPID != 0 {
				t.Fatalf("resume duplicated cost or ownership: %+v", after)
			}

		})
	}
}
