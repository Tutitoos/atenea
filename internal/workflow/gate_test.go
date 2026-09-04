package workflow_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
)

// approve answers whatever gate a run is waiting on, as a person would.
func approve(t *testing.T, h *harness, id string) workflow.Gate {
	t.Helper()
	gate, ok, err := h.state.OpenGate(t.Context(), id)
	if err != nil || !ok {
		t.Fatalf("open gate on %s: %v (found %v)", id, err, ok)
	}
	answered, err := h.state.Answer(t.Context(), id, gate.Ordinal,
		workflow.DecisionApproved, "tester via test", "", time.Now())
	if err != nil {
		t.Fatalf("approving gate %d: %v", gate.Ordinal, err)
	}
	return answered
}

// A created workflow writes its plan down and spawns nothing.
func TestCreateRunsNothing(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, gate, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gate.Kind != workflow.KindLaunch || !gate.Waiting() {
		t.Fatalf("gate is %s %s, want a waiting launch", gate.Kind, gate.Decision)
	}
	if gate.Ordinal != 0 {
		t.Errorf("launch is gate %d, want 0", gate.Ordinal)
	}
	if got := statuses(t, run)["a"]; got != "pending" {
		t.Errorf("step a is %q after create, want pending: nothing may spawn before a launch", got)
	}
	if run.Closed {
		t.Error("a created run is closed; it has not been launched yet")
	}
}

// The launch is gate 0 and an expansion is not, so the log says which act
// each was without anybody counting ordinals.
func TestALaunchAndAnApprovalAreDifferentWords(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := h.state.Ask(t.Context(), run.ID, workflow.KindApprove,
		workflow.Proposal{Steps: []workflow.Step{step("b", "worker", nil)}}, time.Now()); err == nil {
		t.Fatal("asked a second question while the first was unanswered")
	}
	approve(t, h, run.ID)
	if _, err := h.state.Ask(t.Context(), run.ID, workflow.KindApprove,
		workflow.Proposal{Steps: []workflow.Step{step("b", "worker", nil)}}, time.Now()); err != nil {
		t.Fatalf("Ask an expansion: %v", err)
	}
	gates, err := h.state.Gates(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Gates: %v", err)
	}
	if len(gates) != 2 {
		t.Fatalf("%d gates, want 2", len(gates))
	}
	if gates[0].Kind != workflow.KindLaunch {
		t.Errorf("gate 0 is %s, want launch", gates[0].Kind)
	}
	if gates[1].Kind != workflow.KindApprove {
		t.Errorf("gate 1 is %s, want approve", gates[1].Kind)
	}
}

// Only a step that has not STARTED may be replanned. Not "not executed": a
// running step has begun to touch the world.
func TestOnlyANotStartedStepMayBeReplanned(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approve(t, h, run.ID)
	// Started, not finished: the rule is about a step having begun to touch
	// the world, and a running step is past the line even though it has
	// produced nothing yet.
	if err := h.state.Claim(t.Context(), run.ID, "a", "tr-1", 1, time.Now(), 4242); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	_, err = h.state.Ask(t.Context(), run.ID, workflow.KindApprove,
		workflow.Proposal{
			Steps:    []workflow.Step{step("b", "worker", nil)},
			Replaces: []string{"a"},
		}, time.Now())
	if err == nil {
		t.Fatal("replanned a step that had already run")
	}
	if !strings.Contains(err.Error(), "has not started") {
		t.Errorf("refusal %q does not say why: the rule is about starting, not finishing", err)
	}
}

// The freeze: while a gate is open nothing new is dispatched, and what was
// already spawned finishes.
func TestAnOpenGateFreezesDispatchAndLetsTheRunningLand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "second-ran")
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	h := newHarnessOver(t, dir, noCeiling(),
		declared("slow", stub(t, dir, "slow", "touch "+ready+"\nwhile [ ! -f "+release+" ]; do sleep 0.01; done\n"+
			`echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent),
		declared("eager", stub(t, dir, "eager", "touch "+marker+"\n"+
			`echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	// `slow` runs immediately; `late` waits on it. A gate opens while slow
	// is in flight, so slow must land and late must not start.
	run, gate, err := h.engine.Create(t.Context(),
		graphOf(step("slow", "slow", nil), step("late", "eager", []string{"slow"})))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approve(t, h, run.ID)

	// On t.Context(), not on a private four-second budget. `go test` already
	// bounds this with its own -timeout, and the second, much smaller clock
	// bounded the machine rather than the property: the freeze either holds or
	// it does not, and there is no duration after which a correct engine
	// becomes a wrong one.
	done := make(chan workflow.Run, 1)
	go func() {
		out, _ := h.engine.Run(t.Context(), gate.RunID)
		done <- out
	}()

	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-t.Context().Done():
			t.Fatal("worker never started")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := h.state.Ask(t.Context(), run.ID, workflow.KindApprove,
		workflow.Proposal{Steps: []workflow.Step{step("added", "eager", nil)}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}

	// Wait for the observable condition rather than sleeping a fixed amount.
	// Under the full benchmark suite the process can be delayed by unrelated
	// package work, while the contract we need to assert is simply that the
	// gate is open and the already-started step has landed.
	//
	// The deadline is a backstop that names the failure, not a budget this
	// test is expected to come close to. Measured: 1.76s wall inside the full
	// package run, a second of which is `slow`'s own sleep. The old three
	// seconds left 1.24s of headroom over that -- less than the 1.2-1.5s that
	// entering this package cold costs, which is the difference between
	// TestAGraphRunsInOrder's 1.00s in-suite and its 2.2-2.5s run on its own.
	// A margin thinner than a startup cost the same package already measures
	// is a margin that will one day fail on a machine where nothing is wrong.
	deadline := time.Now().Add(30 * time.Second)
	var open workflow.Gate
	var held workflow.Run
	for {
		var openOK bool
		open, openOK, err = h.state.OpenGate(t.Context(), run.ID)
		if err != nil {
			t.Fatalf("OpenGate: %v", err)
		}
		held, err = h.state.Load(t.Context(), run.ID)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if openOK && statuses(t, held)["slow"] == "ok" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the open gate did not let the running step land before the deadline")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if got := statuses(t, held)["slow"]; got != "ok" {
		t.Errorf("slow is %q while the gate waits, want ok: what is already spawned must finish", got)
	}
	if got := statuses(t, held)["late"]; got != "pending" {
		t.Errorf("late is %q while the gate waits, want pending: the freeze dispatches nothing new", got)
	}

	// Answering releases it, and the added step runs.
	if _, err := h.state.Answer(t.Context(), run.ID, open.Ordinal,
		workflow.DecisionApproved, "tester via test", "", time.Now()); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	out := <-done
	final := statuses(t, out)
	for _, id := range []string{"slow", "late", "added"} {
		if final[id] != "ok" {
			t.Errorf("%s is %q after the gate cleared, want ok", id, final[id])
		}
	}
}

// An approval binds to a digest, not to a moment.
func TestAnApprovalIsOfAPlanNotAMoment(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, gate, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	answered := approve(t, h, run.ID)

	// The proposal read back is not the one that was approved.
	tampered := answered
	tampered.Proposal.Steps[0].Task.Objective = "something else entirely"
	err = h.state.Apply(t.Context(), run.ID, tampered, workflow.Plan{})
	if err == nil {
		t.Fatal("applied a plan that differs from the one approved")
	}
	if !strings.Contains(err.Error(), "not the plan that was approved") {
		t.Errorf("refusal %q does not name the problem", err)
	}
	if got := workflow.Short(gate.Digest); len(got) != 12 {
		t.Errorf("short digest %q is not 12 characters", got)
	}
}

// Reordering the steps of a proposal is a different plan; listing the
// replaced steps in another order is not.
func TestTheDigestCoversWhatWasReadAndNotHowItWasTyped(t *testing.T) {
	a, b := step("a", "worker", nil), step("b", "worker", nil)
	one := workflow.Proposal{Steps: []workflow.Step{a, b}, Replaces: []string{"x", "y"}}
	two := workflow.Proposal{Steps: []workflow.Step{a, b}, Replaces: []string{"y", "x"}}
	if one.Digest() != two.Digest() {
		t.Error("the order the replaced steps were listed in changed the digest")
	}
	swapped := workflow.Proposal{Steps: []workflow.Step{b, a}, Replaces: []string{"x", "y"}}
	if one.Digest() == swapped.Digest() {
		t.Error("two different graphs share a digest")
	}
	graver := workflow.Proposal{Steps: []workflow.Step{a, b}, Replaces: []string{"x"}}
	if one.Digest() == graver.Digest() {
		t.Error("removing a step from the replace set left the digest alone")
	}
}

// Three expansions, and the fourth is refused with the count in the sentence.
func TestExpansionsAreExhausted(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approve(t, h, run.ID)
	for i := range workflow.MaxExpansions {
		id := string(rune('b' + i))
		if _, err := h.state.Ask(t.Context(), run.ID, workflow.KindApprove,
			workflow.Proposal{Steps: []workflow.Step{step(id, "worker", nil)}}, time.Now()); err != nil {
			t.Fatalf("expansion %d: %v", i+1, err)
		}
		approve(t, h, run.ID)
	}
	_, err = h.state.Ask(t.Context(), run.ID, workflow.KindApprove,
		workflow.Proposal{Steps: []workflow.Step{step("z", "worker", nil)}}, time.Now())
	if err == nil {
		t.Fatal("a fourth expansion was allowed")
	}
	if !strings.Contains(err.Error(), "expansions exhausted (3 of 3)") {
		t.Errorf("refusal %q does not say which cap ran out, or where it stands", err)
	}
}

// The grant runs out of ALLOCATION, and the sentence says allocated rather
// than spent, because nothing here can report a charge.
func TestTheGrantIsExhaustedByAllocationNotBySpend(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	graph := graphOf(costing(step("a", "worker", nil), 0.40))
	graph.GrantUSD = 0.50
	run, _, err := h.engine.Create(t.Context(), graph)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approve(t, h, run.ID)

	_, err = h.state.Ask(t.Context(), run.ID, workflow.KindApprove,
		workflow.Proposal{Steps: []workflow.Step{costing(step("b", "worker", nil), 0.20)}}, time.Now())
	if err == nil {
		t.Fatal("an expansion past the grant was put to a person")
	}
	if !strings.Contains(err.Error(), "allocated") {
		t.Errorf("refusal %q reads as spend; nothing here has measured a charge", err)
	}
	// The grant has $0.10 free, so it is not fully allocated and the
	// sentence must not say it is.
	if strings.Contains(err.Error(), "fully allocated") {
		t.Errorf("refusal %q claims the grant is spent out while $0.10 of it is free", err)
	}
	if strings.Contains(err.Error(), "spent") {
		t.Errorf("refusal %q claims a spend nobody measured", err)
	}
	// What fits is still allowed: the cap is on the sum, not on the count.
	if _, err := h.state.Ask(t.Context(), run.ID, workflow.KindApprove,
		workflow.Proposal{Steps: []workflow.Step{costing(step("b", "worker", nil), 0.10)}}, time.Now()); err != nil {
		t.Fatalf("an expansion inside the grant was refused: %v", err)
	}
}

// A rejected launch never ran, and does not read as finished or as aborted.
func TestARejectedLaunchNeverRan(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", "touch "+marker+"\n"+
			`echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, gate, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := h.state.Answer(t.Context(), run.ID, gate.Ordinal,
		workflow.DecisionRejected, "tester via test", "the plan reads two files it should not",
		time.Now()); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	out, err := h.engine.Run(t.Context(), run.ID)
	if err == nil {
		t.Fatal("a rejected plan ran without complaint")
	}
	if out.Stop != workflow.StopRejected {
		t.Errorf("stop is %q, want rejected", out.Stop)
	}
	if got := statuses(t, out)["a"]; got != "pending" {
		t.Errorf("step a is %q, want pending: nothing may run on a refused plan", got)
	}
	if fileExists(marker) {
		t.Error("the agent spawned despite the rejection")
	}
}

// A rejected EXPANSION is not a rejected run: the graph already approved
// finishes.
func TestARejectedExpansionLeavesTheApprovedGraphAlone(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approve(t, h, run.ID)
	gate, err := h.state.Ask(t.Context(), run.ID, workflow.KindApprove,
		workflow.Proposal{Steps: []workflow.Step{step("b", "worker", nil)}}, time.Now())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if _, err := h.state.Answer(t.Context(), run.ID, gate.Ordinal,
		workflow.DecisionRejected, "tester via test", "not needed", time.Now()); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	out, err := h.engine.Run(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := statuses(t, out)["a"]; got != "ok" {
		t.Errorf("step a is %q, want ok: a refused expansion does not cancel an approved graph", got)
	}
	if _, ok := statuses(t, out)["b"]; ok {
		t.Error("the refused step is in the graph")
	}
	if !out.Closed {
		t.Error("the run did not finish")
	}
}

// A rejection needs a sentence. A refusal with no reason tells the next
// reader that something was turned down and nothing about what to do next.
func TestARejectionNeedsAReason(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, gate, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := h.state.Answer(t.Context(), run.ID, gate.Ordinal,
		workflow.DecisionRejected, "tester via test", "  ", time.Now()); err == nil {
		t.Fatal("a rejection with no reason was accepted")
	}
}

// Two answers to one question is a fact worth surfacing, not a silent
// overwrite of whoever got there first.
func TestAGateIsAnsweredOnce(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, gate, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := h.state.Answer(t.Context(), run.ID, gate.Ordinal,
		workflow.DecisionApproved, "first via test", "", time.Now()); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	_, err = h.state.Answer(t.Context(), run.ID, gate.Ordinal,
		workflow.DecisionRejected, "second via test", "changed my mind", time.Now())
	if err == nil {
		t.Fatal("a second answer overwrote the first")
	}
	if !strings.Contains(err.Error(), "first via test") {
		t.Errorf("refusal %q does not name who answered", err)
	}
}

// Two answers that arrive at once, which is the case the sequential test
// above does not reach. There, the second caller's own read already sees a
// decided gate. Here every caller reads a waiting one, so the read decides
// nothing and only the UPDATE's `decision = 'waiting'` predicate can say which
// answer is real.
//
// The assertion is on what each caller is TOLD, not only on what the row ends
// up holding -- the row was always right. Answer returning nil to a hand whose
// decision was discarded is the whole failure: `atenea workflow approve`
// prints its confirmation and the operator walks away believing they blessed a
// run whose gate carries somebody else's name, and possibly somebody else's
// rejection.
func TestOnlyOneOfManySimultaneousAnswersIsAccepted(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", stub(t, dir, "worker", `echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent))

	run, gate, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	type answer struct {
		hand string
		gate workflow.Gate
		err  error
	}
	const hands = 8
	var start atomic.Bool
	var ready sync.WaitGroup
	ready.Add(hands)
	answers := make(chan answer, hands)
	for i := range hands {
		go func() {
			hand := fmt.Sprintf("hand-%d via test", i)
			// Warm this goroutine's connection before the barrier. Opening a
			// sqlite file costs enough that a pool doing it under the barrier
			// staggers the hands past each other: measured on this test, the
			// window opened in 1 run out of 10 with the warm-up removed and in
			// 10 out of 10 with it, so without this the test would pass on a
			// defect nine times in ten.
			if _, err := h.state.Gate(t.Context(), run.ID, gate.Ordinal); err != nil {
				answers <- answer{hand: hand, err: err}
				ready.Done()
				return
			}
			ready.Done()
			for !start.Load() {
				runtime.Gosched()
			}
			got, err := h.state.Answer(t.Context(), run.ID, gate.Ordinal,
				workflow.DecisionApproved, hand, "", time.Now())
			answers <- answer{hand: hand, gate: got, err: err}
		}()
	}
	ready.Wait()
	start.Store(true)

	var accepted []string
	var got []answer
	for range hands {
		a := <-answers
		got = append(got, a)
		if a.err == nil {
			accepted = append(accepted, a.hand)
		}
	}
	if len(accepted) != 1 {
		t.Fatalf("%d of %d hands were told their answer was accepted, want exactly 1: %v",
			len(accepted), hands, accepted)
	}

	// The one told yes has to be the one on the row. Anything else means the
	// winner was decided twice, by two different pieces of code.
	stored, err := h.state.Gate(t.Context(), run.ID, gate.Ordinal)
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if stored.Hand != accepted[0] {
		t.Errorf("the record says %q answered, but %q was told it had", stored.Hand, accepted[0])
	}
	for _, a := range got {
		// Every caller, winner and loser alike, is handed the gate as it
		// really is. A loser used to be handed the one this call had built in
		// memory, carrying its own hand on a decision nobody recorded.
		if a.gate.Hand != stored.Hand {
			t.Errorf("%s was handed a gate answered by %q, want the recorded %q",
				a.hand, a.gate.Hand, stored.Hand)
		}
		if a.err != nil && !strings.Contains(a.err.Error(), stored.Hand) {
			t.Errorf("the refusal given to %s does not name the winner %q: %v",
				a.hand, stored.Hand, a.err)
		}
	}
}

// The record says who answered as far as this machine can tell, and no more.
func TestTheHandIsWhatTheMachineKnows(t *testing.T) {
	hand := workflow.Hand("cli")
	if !strings.HasSuffix(hand, " via cli") {
		t.Errorf("hand %q does not name the surface it arrived through", hand)
	}
	if strings.TrimSpace(strings.TrimSuffix(hand, " via cli")) == "" {
		t.Error("hand names no user at all")
	}
}

// A gate outlives the Atenea that opened it.
//
// The question is a row, so the process that asked it is not the only thing
// that can hear the answer. This is the claim the design rests on: a workflow
// parked on a gate survives a restart, and the next Atenea takes it over and
// finds the question exactly where it was left.
func TestAGateOutlivesTheAteneaThatOpenedIt(t *testing.T) {
	dir := t.TempDir()
	worker := declared("worker", answers(t, dir, "worker"), config.PoolAgent)

	// The first Atenea: pid 4242, which the second one will find dead.
	first := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(), PID: 4242, Alive: func(int) bool { return true },
	}, dir, worker)

	run, _, err := first.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approve(t, first, run.ID)

	// It gets as far as opening an expansion gate, then dies. Nobody
	// answered, and nothing closed the run.
	if err := first.state.Own(t.Context(), run.ID, 4242, 0); err != nil {
		t.Fatalf("Own: %v", err)
	}
	if _, err := first.state.Ask(t.Context(), run.ID, workflow.KindApprove,
		workflow.Proposal{Steps: []workflow.Step{step("b", "worker", nil)}}, time.Now()); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	// A live first Atenea is refused, which is what makes the takeover mean
	// something: it is the death that permits it, not the wanting.
	live := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(), PID: 99, Alive: func(int) bool { return true },
	}, dir, worker)
	if _, err := live.engine.Resume(t.Context(), run.ID, nil); err == nil {
		t.Fatal("a second Atenea took over a run whose owner is alive")
	}

	// Now it is gone. The second Atenea reads the same record.
	second := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(), PID: 99, Alive: func(pid int) bool { return pid != 4242 },
	}, dir, worker)

	gate, ok, err := second.state.OpenGate(t.Context(), run.ID)
	if err != nil || !ok {
		t.Fatalf("the question died with the process that asked it: %v (found %v)", err, ok)
	}
	if gate.Kind != workflow.KindApprove || !gate.Waiting() {
		t.Fatalf("gate is %s %s, want a waiting approve", gate.Kind, gate.Decision)
	}

	// Answered on the new process, and the run carries on from there.
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, _ = second.state.Answer(context.Background(), run.ID, gate.Ordinal,
			workflow.DecisionApproved, "tester via test", "", time.Now())
	}()
	out, err := second.engine.Resume(t.Context(), run.ID, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	final := statuses(t, out)
	for _, id := range []string{"a", "b"} {
		if final[id] != "ok" {
			t.Errorf("%s is %q after the takeover, want ok", id, final[id])
		}
	}
	if !out.Closed {
		t.Error("the run did not finish under the second Atenea")
	}

	// The log says the answer arrived after the asker was gone.
	gates, err := second.state.Gates(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Gates: %v", err)
	}
	last := gates[len(gates)-1]
	if last.Decision != workflow.DecisionApproved {
		t.Errorf("gate %d is %s, want approved", last.Ordinal, last.Decision)
	}
	if last.Answered.Before(last.Asked) {
		t.Error("the gate was answered before it was asked")
	}
}

// The launch gate says what happened to it, and a refusal is not a launch.
func TestARefusedLaunchDoesNotReadAsOneThatRan(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessOver(t, dir, noCeiling(),
		declared("worker", answers(t, dir, "worker"), config.PoolAgent))

	run, gate, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := h.state.Answer(t.Context(), run.ID, gate.Ordinal,
		workflow.DecisionRejected, "tester via test", "reads a file it should not",
		time.Now()); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	_, err = h.engine.Launch(t.Context(), run.ID)
	if err == nil {
		t.Fatal("a refused plan launched")
	}
	if strings.Contains(err.Error(), "launched already") {
		t.Errorf("refusal %q says it ran; it was turned down", err)
	}
	if !strings.Contains(err.Error(), "reads a file it should not") {
		t.Errorf("refusal %q does not carry the reason it was refused for", err)
	}
}

// costing puts a share of the grant on a step.
func costing(s workflow.Step, usd float64) workflow.Step {
	s.Permission.BudgetUSD = usd
	return s
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
