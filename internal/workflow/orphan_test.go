package workflow_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Shares are a division of the grant, and dividing nothing gives nothing.
//
// Compile skipped this arithmetic when the grant was zero -- `graph.GrantUSD >
// 0 &&` -- which exempted it exactly where it is most obviously wrong. A graph
// declaring per-step budgets and no graph budget compiled, the run was written,
// and Store.Ask (which never had the exemption) refused afterwards.
func TestSharesOutOfAZeroGrantAreRefusedAtCompile(t *testing.T) {
	_, err := workflow.Compile(
		func() workflow.Graph {
			g := graphOf(funded("a", "reader", 5.00))
			g.GrantUSD = 0
			return g
		}(),
		[]config.AgentType{declared("reader", "/bin/true", config.PoolAgent)})
	if err == nil {
		t.Fatal("a graph whose steps divide a grant of nothing compiled")
	}
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want invalid_input", contract.KindOf(err))
	}
	if !strings.Contains(err.Error(), "$0.00 grant") {
		t.Errorf("refusal = %q, want it to name the grant it divides", err)
	}
}

// What the exemption above left behind: a workflow row with every step pending
// and no gate at all. `list` showed it. `resume` ran it -- spending the shares
// with nobody having approved a penny.
//
// Two locks now, because one of them being a rule about money and the other a
// rule about authorization means neither depends on the other holding.
func TestARunNothingAuthorizedIsNotExecutable(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(), PID: 1, Alive: func(int) bool { return false },
	}, dir, declared("reader", answers(t, dir, "reader"), config.PoolAgent))

	// Written the way Create used to leave one: straight to the store, with
	// nothing ever asked about it.
	plan, err := workflow.Compile(graphOf(step("a", "reader", nil)),
		[]config.AgentType{declared("reader", "/bin/true", config.PoolAgent)})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := h.state.Create(t.Context(), "wf-orphan", plan, "", time.Now(), 0); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = h.engine.Resume(t.Context(), "wf-orphan", nil)
	if err == nil {
		t.Fatal("a run with no launch gate was executed")
	}
	if !strings.Contains(err.Error(), "no launch gate") {
		t.Errorf("refusal = %q, want it to say nothing authorized the run", err)
	}
}

// And Create no longer leaves one. A refusal from the gate takes the run with
// it, so there is nothing for `list` to show or `resume` to find.
func TestARefusedCreateLeavesNoRunBehind(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(), PID: 1,
	}, dir, declared("reader", answers(t, dir, "reader"), config.PoolAgent))

	before, err := h.state.List(t.Context(), 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Over the grant: refused by Compile now, and by Ask before it.
	graph := commissioned(funded("a", "reader", 0.50))
	graph.GrantUSD = 0.10
	if _, _, err := h.engine.Create(t.Context(), graph); err == nil {
		t.Fatal("a plan asking for more than its grant was written down")
	}

	after, err := h.state.List(t.Context(), 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("runs on the record = %d, want the %d there were: a refused create left one behind",
			len(after), len(before))
	}
}

// Discard is for a run nobody was ever asked about. A run with a gate has a
// record, and a cleanup path does not get to delete a record.
func TestDiscardRefusesARunWithAGateOnTheRecord(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(), PID: 1,
	}, dir, declared("reader", answers(t, dir, "reader"), config.PoolAgent))

	run, _, err := h.engine.Create(t.Context(), commissioned(funded("a", "reader", 0.10)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Discard(t.Context(), run.ID); err == nil {
		t.Fatal("a run with a launch gate on the record was discarded")
	}
}
