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
	ids        atomic.Int32
	failFinish bool
	second     chan struct{}
}

// NextID returns a deterministic reservation identifier for fault injection.
func (d *accountingDispatcher) NextID() string { return fmt.Sprintf("id%d", d.ids.Add(1)) }

// Dispatch coordinates a rejected charge with another in-flight measured call.
func (d *accountingDispatcher) Dispatch(ctx context.Context, call agent.Dispatch) (contract.Report, contract.Assignment, error) {
	if d.failFinish && call.ID == "id1" {
		<-d.second
		usd := -1.0
		return contract.Report{Verdict: contract.VerdictOK, Spent: contract.Charge{USD: &usd, PricedBy: "fixture"}}, contract.Assignment{}, nil
	}
	if d.failFinish {
		close(d.second)
	}
	<-ctx.Done()
	usd := 0.2
	return contract.Report{Spent: contract.Charge{USD: &usd, PricedBy: "fixture"}}, contract.Assignment{}, contract.Fail(contract.FailureCanceled, "fixture canceled")
}

// TestAccountingFailuresReleaseOwnership covers Claim and Finish with active siblings.
func TestAccountingFailuresReleaseOwnership(t *testing.T) {
	for _, finish := range []bool{false, true} {
		t.Run(fmt.Sprint(finish), func(t *testing.T) {
			dir := t.TempDir()
			worker := declared("work", answers(t, dir, "work"), config.PoolAgent)
			h := newHarnessOver(t, dir, noCeiling(), worker)
			d := &accountingDispatcher{failFinish: finish, second: make(chan struct{})}
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
				if _, err = db.Exec(`CREATE TRIGGER reject_second BEFORE INSERT ON workflow_reservation WHEN NEW.trace_id='id2' BEGIN SELECT RAISE(ABORT,'claim fixture'); END`); err != nil {
					t.Fatal(err)
				}
			}
			g := graphOf(step("a", "work", nil), step("b", "work", nil))
			g.GrantUSD = 1
			run, err := engine.Start(t.Context(), g)
			if err == nil {
				t.Fatal("accounting failure was accepted")
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
			// A fresh real engine may refuse a redo whose old reservations exhaust
			// the grant, but it must not duplicate the charge or retain ownership.
			_, _ = h.engine.Resume(t.Context(), run.ID, []string{"a", "b"})
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
