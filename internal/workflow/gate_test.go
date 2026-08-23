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
	h := newHarnessOver(t, dir, noCeiling(),
		declared("slow", stub(t, dir, "slow", "sleep 1\n"+
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

	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = h.state.Ask(context.Background(), run.ID, workflow.KindApprove,
			workflow.Proposal{Steps: []workflow.Step{step("added", "eager", nil)}}, time.Now())
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	done := make(chan workflow.Run, 1)
	go func() {
		out, _ := h.engine.Run(ctx, gate.RunID)
		done <- out
	}()

	// Wait for the observable condition rather than sleeping a fixed amount.
	// Under the full benchmark suite the process can be delayed by unrelated
	// package work, while the contract we need to assert is simply that the
	// gate is open and the already-started step has landed.
	deadline := time.Now().Add(3 * time.Second)
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
	if err := first.state.Own(t.Context(), run.ID, 4242); err != nil {
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
