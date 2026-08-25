package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// twoAttempts drives one step through the exact sequence a redo produces:
// claimed, finished badly, claimed again, finished well. Both claims go
// through Store.Claim, which is the single site the engine re-claims from
// (engine.go: the loop's only Claim call), so this is the real path and not a
// second one written for the test.
//
// The figures are the ones measured on wf1786845363956-1 on 2026-08-16:
// admin-config was cut at $0.62 having been funded a $0.45 share. What it
// would have cost to finish is the number this project cannot currently
// produce, and the reason is this function's second half.
func twoAttempts(t *testing.T, store *workflow.Store, id string) {
	t.Helper()
	one := step("a", "reader", nil)
	one.Permission.BudgetUSD = 0.45
	// Granted what the step is handed: a share drawn out of a zero grant is
	// the shape Compile refuses.
	funding := graphOf(one)
	funding.GrantUSD = 0.45
	plan, err := workflow.Compile(funding,
		[]config.AgentType{declared("reader", "/bin/true", config.PoolAgent)})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := store.Create(t.Context(), id, plan, "taxiprime-backend", time.Now(), 1); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Attempt 1: cut at its ceiling, having answered nothing.
	if err := store.Claim(t.Context(), id, "a", "tr-1", 1, time.Now(), 1); err != nil {
		t.Fatalf("Claim 1: %v", err)
	}
	cut := contract.Report{
		Verdict: contract.VerdictIncomplete,
		Reason: contract.Reason{
			Kind: contract.FailureUnavailable,
			Text: "claude code stopped at its spending ceiling before it could answer",
		},
		Spent: contract.Charge{
			USD: usd(0.62), InputTokens: 2, OutputTokens: 152,
			CacheReadTokens: 4772, CacheWriteTokens: 1416, PricedBy: "a test",
		},
	}
	if err := store.Finish(t.Context(), id, "a", workflow.StatusIncomplete, cut, time.Now()); err != nil {
		t.Fatalf("Finish 1: %v", err)
	}

	// Attempt 2: the same step, re-claimed. This is the write that destroys
	// the row above.
	if err := store.Claim(t.Context(), id, "a", "tr-2", 2, time.Now(), 1); err != nil {
		t.Fatalf("Claim 2: %v", err)
	}
	done := contract.Report{
		Result:  map[string]any{"ok": true},
		Verdict: contract.VerdictOK,
		Spent: contract.Charge{
			USD: usd(0.31), InputTokens: 4, OutputTokens: 5424,
			CacheReadTokens: 44999, CacheWriteTokens: 15090, PricedBy: "a test",
		},
	}
	if err := store.Finish(t.Context(), id, "a", workflow.StatusOK, done, time.Now()); err != nil {
		t.Fatalf("Finish 2: %v", err)
	}
}

func attemptStore(t *testing.T) *workflow.Store {
	t.Helper()
	store, err := workflow.Open(t.Context(), filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// The instrument has to produce a one before its zero means anything. The
// second attempt IS on the record -- so a reader that finds nothing about the
// first is reading a store that works, not a store that is down.
func TestTheLastAttemptIsOnTheRecord(t *testing.T) {
	store := attemptStore(t)
	twoAttempts(t, store, "wf-1")

	run, err := store.Load(t.Context(), "wf-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(run.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(run.Steps))
	}
	row := run.Steps[0]
	if row.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", row.Attempt)
	}
	if row.Status != workflow.StatusOK {
		t.Errorf("status = %s, want ok", row.Status)
	}
	if row.Spent.USD == nil || *row.Spent.USD != 0.31 {
		t.Errorf("spent = %v, want 0.31", row.Spent.USD)
	}
}

// What a share has to clear is what a step costs to FINISH, and the only rows
// that carry that are the ones that finished. A step cut at its ceiling is a
// lower bound -- which is why CostByType excludes it -- so the measurement the
// admission rule actually needs is the PAIR: cut at $0.62 under a $0.45 share,
// finished at $0.31 under the same one.
//
// This test says what the record can answer about the first half of that pair.
func TestTheReplacedAttemptIsRecoverable(t *testing.T) {
	store := attemptStore(t)
	twoAttempts(t, store, "wf-1")

	got, err := store.Attempts(t.Context(), "wf-1", "a")
	if err != nil {
		t.Fatalf("Attempts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("replaced attempts = %d, want 1 (the one that was cut)", len(got))
	}
	first := got[0]
	if first.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", first.Attempt)
	}
	if first.Status != workflow.StatusIncomplete {
		t.Errorf("status = %s, want incomplete", first.Status)
	}
	if first.GrantUSD != 0.45 {
		t.Errorf("share it ran under = %v, want 0.45", first.GrantUSD)
	}
	if first.Spent.USD == nil || *first.Spent.USD != 0.62 {
		t.Errorf("spent = %v, want 0.62 -- the figure it was cut at", first.Spent.USD)
	}
	if first.Spent.CacheWriteTokens != 1416 {
		t.Errorf("cache write = %d, want 1416", first.Spent.CacheWriteTokens)
	}
	if first.TraceID != "tr-1" {
		t.Errorf("trace = %q, want tr-1 -- the trace a reader follows", first.TraceID)
	}
	if first.Reason.Kind != contract.FailureUnavailable {
		t.Errorf("reason kind = %s, want unavailable", first.Reason.Kind)
	}
}

// Load has to hand the archive over, because Run.Spend is where a balance comes
// from and it cannot total money it was never given. Asserted through Budget
// rather than by reading the field: the wrong balance is what a person acts on.
func TestALoadedRunCarriesTheAttemptsItsBalanceNeeds(t *testing.T) {
	store := attemptStore(t)
	twoAttempts(t, store, "wf-1")

	run, err := store.Load(t.Context(), "wf-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(run.Superseded) != 1 {
		t.Fatalf("Superseded = %d attempts, want 1 -- Load did not read the archive", len(run.Superseded))
	}
	if got := run.Spend().SupersededUSD; got != 0.62 {
		t.Errorf("SupersededUSD = %v, want 0.62", got)
	}
	if line := run.Budget(); !strings.Contains(line, "a redo replaced") {
		t.Errorf("the balance does not name the replaced attempt:\n%s", line)
	}
}

// The store is not the actor. The engine is what re-claims, so the archive has
// to be filed on the path the engine actually walks -- Resume with --redo,
// through the same Claim the dispatch loop calls -- and not only on a sequence
// a test drove by hand.
func TestARedoneStepFilesTheAttemptItReplaced(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs.txt")
	entered := filepath.Join(dir, "entered")
	h := newHarness(t, config.Workflow{MaxParallelAgent: 1},
		declared("scribe", stub(t, dir, "scribe",
			"echo run >> "+runs+"\n"+
				"if [ -f "+filepath.Join(dir, "once")+" ]; then\n"+
				"  echo '{\"result\":{\"ok\":true},\"verdict\":\"ok\"}'\n"+
				"else\n  touch "+filepath.Join(dir, "once")+"\n  touch "+entered+"\n  sleep 5\nfi"),
			config.PoolAgent, contract.EffectRead, contract.EffectWrite),
	)
	graph := graphOf(withFiles(
		step("write", "scribe", nil, contract.EffectRead, contract.EffectWrite), "out.txt"))

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		for {
			if _, err := os.Stat(entered); err == nil {
				cancel()
				return
			}
			select {
			case <-deadline.C:
				cancel()
				return
			case <-ctx.Done():
				return
			default:
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()
	cut, err := h.engine.Start(ctx, graph)
	if err == nil {
		t.Fatal("Start: want the cut to reach the caller")
	}

	// Nothing filed yet: one attempt exists and it is still the live row.
	before, err := h.state.Attempts(t.Context(), cut.ID, "write")
	if err != nil {
		t.Fatalf("Attempts: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("filed attempts before the redo = %d, want 0", len(before))
	}

	if _, err := h.engine.Resume(t.Context(), cut.ID, []string{"write"}); err != nil {
		t.Fatalf("Resume --redo: %v", err)
	}
	if lines := countLines(t, runs); lines != 2 {
		t.Fatalf("the writer ran %d times, want exactly one repeat", lines)
	}

	after, err := h.state.Attempts(t.Context(), cut.ID, "write")
	if err != nil {
		t.Fatalf("Attempts: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("filed attempts after the redo = %d, want 1", len(after))
	}
	first := after[0]
	if first.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", first.Attempt)
	}
	if first.Status != workflow.StatusInterrupted {
		t.Errorf("status = %s, want interrupted", first.Status)
	}
	if !first.Cut() {
		t.Error("Cut() = false on an interrupted attempt, want true")
	}
	if first.TraceID == "" {
		t.Error("trace id is empty: a filed attempt a reader cannot follow is not a record")
	}
	// An interrupted attempt was cut before any report arrived, so nobody
	// priced it. Nil is the honest value and a zero here would be a receipt
	// claiming the work was weighed and came to nothing.
	if first.Spent.Measured() {
		t.Errorf("spent = %v, want unmeasured on a step cut mid-flight", first.Spent)
	}
}

// The linkage is built and the admission rule still cannot use it, because
// nothing can produce the pair it needs. A step cut at its spending ceiling
// finishes `incomplete` -- it reported, so it was judged -- and TWO separate
// refusals stand between that row and a second attempt at a larger share.
// The four steps that died on wf1786845363956-1 are exactly this status.
//
// Both barriers are asserted because they fail in order, and a change that
// removed only the first would leave the second, still with no pair.
func TestAStepCutAtItsCeilingCannotBeRedone(t *testing.T) {
	dir := t.TempDir()
	cut := "echo '{\"verdict\":\"incomplete\",\"reason\":{\"kind\":\"unavailable\"," +
		"\"text\":\"claude code stopped at its spending ceiling\"}}'"

	// Barrier 1: the run that held it is closed. A run whose steps all
	// reached a terminal status stops for no reason, and a run that stopped
	// for no reason is finished -- see Store.End. Nothing reopens it.
	h := newHarness(t, noCeiling(),
		declared("reader", stub(t, dir, "reader", cut), config.PoolAgent, contract.EffectRead),
	)
	run, err := h.engine.Start(t.Context(), graphOf(step("a", "reader", nil, contract.EffectRead)))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := statuses(t, run)["a"]; got != "incomplete" {
		t.Fatalf("status = %s, want incomplete", got)
	}
	_, err = h.engine.Resume(t.Context(), run.ID, []string{"a"})
	if err == nil {
		t.Fatal("Resume --redo on a finished run: want a refusal; if this now " +
			"succeeds, the pair the admission rule needs has become producible " +
			"and this test is the place to say so")
	}
	if !strings.Contains(err.Error(), "already finished") {
		t.Errorf("first refusal = %q, want it to name the closed run", err.Error())
	}

	// Barrier 2: even on a run still open, --redo is only for steps nobody
	// judged. The second step here is cut mid-flight, so the run stops with a
	// reason and stays open -- and the incomplete step beside it is still
	// refused, on its own status.
	dir2 := t.TempDir()
	fastDone := filepath.Join(dir2, "fast.done")
	h2 := newHarness(t, config.Workflow{MaxParallelAgent: 2},
		declared("reader", stub(t, dir2, "reader", cut+"\ntouch "+fastDone), config.PoolAgent, contract.EffectRead),
		declared("slow", stub(t, dir2, "slow", "sleep 5"), config.PoolAgent, contract.EffectRead),
	)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		// Wait for the fast process to emit its report and mark completion
		// instead of guessing how long process startup took. The marker is
		// written after stdout, so the extra grace lets Runner reap that
		// process and record `incomplete` before the slow sibling is cut.
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(fastDone); err == nil {
				time.Sleep(250 * time.Millisecond)
				cancel()
				return
			}
			if time.Now().After(deadline) {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	open, err := h2.engine.Start(ctx, graphOf(
		step("a", "reader", nil, contract.EffectRead),
		step("b", "slow", nil, contract.EffectRead)))
	if err == nil {
		t.Fatal("Start: want the cut to reach the caller, leaving the run open")
	}
	_, err = h2.engine.Resume(t.Context(), open.ID, []string{"a"})
	if err == nil {
		t.Fatal("Resume --redo of an incomplete step on an open run: want a refusal")
	}
	if !strings.Contains(err.Error(), "not interrupted") {
		t.Errorf("second refusal = %q, want it to name the step's status", err.Error())
	}
}

// The archive of a run that was corrected more than once reads as a sequence
// of events, so its order has to be the order the events happened in. It was
// the alphabet: `ORDER BY step_id, attempt` is chronological only inside one
// step, and a graph whose step `z` was sent back an hour before its step `a`
// listed a's correction first -- under a doc comment on [workflow.Run] that
// promises oldest first.
func TestTheArchiveIsOrderedByWhenEachAttemptWasReplaced(t *testing.T) {
	store := attemptStore(t)

	early, late := step("z", "reader", nil), step("a", "reader", nil)
	early.Permission.BudgetUSD = 0.20
	late.Permission.BudgetUSD = 0.20
	graph := graphOf(early, late)
	graph.GrantUSD = 0.40
	plan, err := workflow.Compile(graph,
		[]config.AgentType{declared("reader", "/bin/true", config.PoolAgent)})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := store.Create(t.Context(), "wf-order", plan, "taxiprime-backend", time.Now(), 1); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// `z` is replaced an hour before `a`. That disagreement between the clock
	// and the alphabet is the only arrangement in which the two orders are
	// distinguishable, so it is the one the fixture builds.
	cut := contract.Report{
		Verdict: contract.VerdictIncomplete,
		Reason: contract.Reason{
			Kind: contract.FailureUnavailable,
			Text: "claude code stopped at its spending ceiling before it could answer",
		},
		Spent: contract.Charge{USD: usd(0.20), PricedBy: "a test"},
	}
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	for hour, id := range []string{"z", "a"} {
		if err := store.Claim(t.Context(), "wf-order", id, "tr-"+id, 1, at, 1); err != nil {
			t.Fatalf("Claim %s: %v", id, err)
		}
		if err := store.Finish(t.Context(), "wf-order", id,
			workflow.StatusIncomplete, cut, at); err != nil {
			t.Fatalf("Finish %s: %v", id, err)
		}
		if err := store.Reset(t.Context(), "wf-order", id,
			at.Add(time.Duration(hour)*time.Hour)); err != nil {
			t.Fatalf("Reset %s: %v", id, err)
		}
	}

	run, err := store.Load(t.Context(), "wf-order")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var order []string
	for _, attempt := range run.Superseded {
		order = append(order, attempt.StepID)
	}
	if len(order) != 2 || order[0] != "z" || order[1] != "a" {
		t.Errorf("Superseded names %v, want [z a]: z was replaced at 12:00 and a at 13:00, "+
			"and the field documents oldest first", order)
	}
}
