package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func noCeiling() config.Workflow { return config.Workflow{} }

// A graph runs, in order, and the record holds what each step answered.
func TestAGraphRunsInOrder(t *testing.T) {
	dir := t.TempDir()
	order := filepath.Join(dir, "order")
	body := func(id string) string {
		return "echo " + id + " >> " + order + "\n" +
			`echo '{"result":{"ok":true},"verdict":"ok"}'`
	}
	h := newHarness(t, noCeiling(),
		declared("first", stub(t, dir, "first", body("first")), config.PoolAgent),
		declared("last", stub(t, dir, "last", body("last")), config.PoolAgent),
	)

	run, err := h.engine.Start(t.Context(), graphOf(
		step("look", "first", nil),
		step("act", "last", []string{"look"}),
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := statuses(t, run); got["look"] != "ok" || got["act"] != "ok" {
		t.Fatalf("statuses = %v, want both ok", got)
	}
	raw, err := os.ReadFile(order)
	if err != nil {
		t.Fatalf("reading the order log: %v", err)
	}
	if got := strings.Fields(string(raw)); len(got) != 2 || got[0] != "first" || got[1] != "last" {
		t.Fatalf("ran %v, want first then last", got)
	}

	// The answer is kept, not just the verdict: a resumed run has to be able
	// to report steps it did not re-run.
	look := stepOf(t, run, "look")
	if look.Result["ok"] != true {
		t.Fatalf("result = %v, want the agent's answer", look.Result)
	}
	if look.TraceID == "" {
		t.Fatal("the step points at no trace row")
	}
	if !run.Closed || run.Stop != workflow.StopNone {
		t.Fatalf("run = closed %v stop %q, want a clean finish", run.Closed, run.Stop)
	}
}

// Steps with nothing to wait for run together, and the lane ceiling is what
// says how many. Four steps, a ceiling of two: two at a time, never three.
func TestTheLaneCeilingCapsParallelSteps(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, config.Workflow{MaxParallelAgent: 2},
		declared("slow", counter(t, dir, "slow", "agent", 200*time.Millisecond), config.PoolAgent),
	)

	run, err := h.engine.Start(t.Context(), graphOf(
		step("a", "slow", nil),
		step("b", "slow", nil),
		step("c", "slow", nil),
		step("d", "slow", nil),
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for id, label := range statuses(t, run) {
		if label != "ok" {
			t.Fatalf("step %s = %s, want every queued step to have run", id, label)
		}
	}
	if got := peak(t, dir, "agent"); got != 2 {
		t.Fatalf("peak parallel = %d, want exactly the ceiling of 2", got)
	}
}

// The lanes are separate. With one slot each, an agent and a reviewer run at
// the same time -- which is the whole point of declaring a pool -- and two of
// either never do.
//
// Both reviewers audit one quick seed step rather than the slow work beside
// them: a reviewer is ordered after the answer it reads, so pointing them at
// the slow steps would test the edge instead of the lane.
func TestTheLanesDoNotShareTheirCeilings(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, config.Workflow{MaxParallelAgent: 1, MaxParallelReview: 1},
		declared("seed", answers(t, dir, "seed"), config.PoolAgent),
		declared("work", counter(t, dir, "work", "agent", 250*time.Millisecond), config.PoolAgent),
		declared("audit", counter(t, dir, "audit", "review", 250*time.Millisecond), config.PoolReview),
	)

	started := time.Now()
	run, err := h.engine.Start(t.Context(), graphOf(
		step("seed", "seed", nil),
		step("w1", "work", nil),
		step("w2", "work", nil),
		reviewing(step("r1", "audit", nil), "seed"),
		reviewing(step("r2", "audit", nil), "seed"),
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	elapsed := time.Since(started)
	if got := statuses(t, run); len(got) != 5 {
		t.Fatalf("statuses = %v", got)
	}
	if got := peak(t, dir, "agent"); got != 1 {
		t.Fatalf("agent lane peaked at %d, want its ceiling of 1", got)
	}
	if got := peak(t, dir, "review"); got != 1 {
		t.Fatalf("review lane peaked at %d, want its ceiling of 1", got)
	}
	// Four quarter-second steps through one shared ceiling would take a
	// second. Through two lanes they pair up, so anything under three
	// quarters proves the reviewers were not queued behind the work.
	if elapsed > 750*time.Millisecond {
		t.Fatalf("took %v: the lanes look like one queue, not two", elapsed)
	}
}

// A step that fails takes its dependents with it and nothing else.
func TestAFailureStopsOnlyWhatDependedOnIt(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		declared("good", answers(t, dir, "good"), config.PoolAgent),
		declared("bad", stub(t, dir, "bad",
			`echo '{"result":{},"verdict":"failed","reason":{"kind":"not_found","text":"nothing there"}}'`),
			config.PoolAgent),
	)

	run, err := h.engine.Start(t.Context(), graphOf(
		step("breaks", "bad", nil),
		step("after", "good", []string{"breaks"}),
		step("beside", "good", nil),
		step("later", "good", []string{"beside"}),
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := statuses(t, run)
	want := map[string]string{
		"breaks": "failed",
		"after":  "blocked",
		"beside": "ok",
		"later":  "ok",
	}
	for id, label := range want {
		if got[id] != label {
			t.Errorf("step %s = %s, want %s", id, got[id], label)
		}
	}
	// Blocked is derived, not stored: on the record the step is simply one
	// that never started.
	if raw := stepOf(t, run, "after").Status; raw != workflow.StatusPending {
		t.Fatalf("stored status of a blocked step = %s, want pending", raw)
	}
	if reason := stepOf(t, run, "breaks").Reason; reason.Text != "nothing there" {
		t.Fatalf("reason = %+v, want the agent's own", reason)
	}
}

// A step whose agent dies is incomplete, not failed: nothing judged it.
func TestADeadAgentIsIncomplete(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		declared("dies", stub(t, dir, "dies", "exit 3"), config.PoolAgent),
	)
	run, err := h.engine.Start(t.Context(), graphOf(step("a", "dies", nil)))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := statuses(t, run)["a"]; got != "incomplete" {
		t.Fatalf("status = %s, want incomplete", got)
	}
	if stepOf(t, run, "a").Reason.Empty() {
		t.Fatal("an incomplete step with no reason says nothing about the death")
	}
}

// Abort: what is running is cut, what is queued never spawns, and the record
// says the run was put down rather than that it finished.
func TestAbortCutsTheRunningAndSpawnsNothingQueued(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "queued-ran")
	h := newHarness(t, config.Workflow{MaxParallelAgent: 1},
		declared("slow", stub(t, dir, "slow",
			"sleep 5\n"+`echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent),
		declared("eager", stub(t, dir, "eager",
			"touch "+marker+"\n"+`echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent),
	)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	run, err := h.engine.Start(ctx, graphOf(
		step("running", "slow", nil),
		step("queued", "eager", nil),
	))
	if err == nil {
		t.Fatal("Start: want the cut to reach the caller")
	}
	if contract.KindOf(err) != contract.FailureCanceled {
		t.Fatalf("kind = %v, want canceled", contract.KindOf(err))
	}
	got := statuses(t, run)
	if got["running"] != "interrupted" {
		t.Fatalf("the cut step = %s, want interrupted", got["running"])
	}
	if got["queued"] == "ok" || ran(t, dir, "queued-ran") {
		t.Fatalf("a queued step spawned during an abort: %s", got["queued"])
	}
	if run.Stop != workflow.StopAborted {
		t.Fatalf("stop = %q, want aborted", run.Stop)
	}
	if run.Closed {
		t.Fatal("an aborted run is closed; nothing would offer to resume it")
	}

	// The agent was cut, and the accounting of it was not: its trace row is
	// closed incomplete rather than left open for a sweep that will not run
	// while this pid lives.
	for _, row := range rowsOf(t, h) {
		if row.Open() {
			t.Fatalf("trace row %s is still open after the abort", row.ID)
		}
		if row.Verdict != contract.VerdictIncomplete {
			t.Fatalf("trace row %s = %s, want incomplete", row.ID, row.Verdict)
		}
	}
}

// Resume after an abort: the read-only step nobody judged is dispatched
// again, as a second attempt that redoes the first, and the rest of the graph
// finishes.
func TestResumeRedoesReadOnlyWorkAndFinishes(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, config.Workflow{MaxParallelAgent: 1},
		declared("slow", stub(t, dir, "slow",
			"if [ -f "+filepath.Join(dir, "once")+" ]; then\n"+
				`  echo '{"result":{"ok":true},"verdict":"ok"}'`+"\n"+
				"else\n  touch "+filepath.Join(dir, "once")+"\n  sleep 5\nfi"),
			config.PoolAgent),
		declared("quick", answers(t, dir, "quick"), config.PoolAgent),
	)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	cut, err := h.engine.Start(ctx, graphOf(
		step("first", "slow", nil),
		step("second", "quick", []string{"first"}),
	))
	if err == nil {
		t.Fatal("Start: want the cut to reach the caller")
	}
	if got := statuses(t, cut)["first"]; got != "interrupted" {
		t.Fatalf("first = %s, want interrupted", got)
	}

	run, err := h.engine.Resume(t.Context(), cut.ID, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	got := statuses(t, run)
	if got["first"] != "ok" || got["second"] != "ok" {
		t.Fatalf("statuses = %v, want the graph finished", got)
	}
	if !run.Closed || run.Stop != workflow.StopNone {
		t.Fatalf("run = closed %v stop %q, want a clean finish", run.Closed, run.Stop)
	}

	// The second dispatch is a second row that says what it redoes, the same
	// way a review's relaunch does. Two attempts at one step must not read as
	// two unrelated runs.
	redone := stepOf(t, run, "first")
	if redone.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", redone.Attempt)
	}
	var linked bool
	for _, row := range rowsOf(t, h) {
		if row.ID == redone.TraceID {
			linked = row.RetryOf != "" && row.Attempt == 2
		}
	}
	if !linked {
		t.Fatal("the resumed run's trace row does not say which run it redoes")
	}
}

// A step that may have written something is NOT re-dispatched on its own. It
// stays unjudged, its dependents stay shut, and the operator is the one who
// decides to run it again.
func TestAnInterruptedWriterWaitsForRedo(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "writer-runs")
	h := newHarness(t, config.Workflow{MaxParallelAgent: 1},
		declared("scribe", stub(t, dir, "scribe",
			"echo run >> "+runs+"\n"+
				"if [ -f "+filepath.Join(dir, "once")+" ]; then\n"+
				`  echo '{"result":{"ok":true},"verdict":"ok"}'`+"\n"+
				"else\n  touch "+filepath.Join(dir, "once")+"\n  sleep 5\nfi"),
			config.PoolAgent, contract.EffectRead, contract.EffectWrite),
		declared("quick", answers(t, dir, "quick"), config.PoolAgent),
	)
	graph := graphOf(
		withFiles(step("write", "scribe", nil, contract.EffectRead, contract.EffectWrite), "out.txt"),
		step("after", "quick", []string{"write"}),
	)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	cut, err := h.engine.Start(ctx, graph)
	if err == nil {
		t.Fatal("Start: want the cut to reach the caller")
	}

	// A plain resume leaves it alone.
	held, err := h.engine.Resume(t.Context(), cut.ID, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	got := statuses(t, held)
	if got["write"] != "interrupted" {
		t.Fatalf("write = %s, want it left unjudged", got["write"])
	}
	if got["after"] != "blocked" {
		t.Fatalf("after = %s, want blocked behind the unjudged step", got["after"])
	}
	if lines := countLines(t, runs); lines != 1 {
		t.Fatalf("the writer ran %d times; a resume must not repeat it by itself", lines)
	}

	// Naming it is what runs it again.
	done, err := h.engine.Resume(t.Context(), cut.ID, []string{"write"})
	if err != nil {
		t.Fatalf("Resume --redo: %v", err)
	}
	got = statuses(t, done)
	if got["write"] != "ok" || got["after"] != "ok" {
		t.Fatalf("statuses = %v, want the graph finished after --redo", got)
	}
	if lines := countLines(t, runs); lines != 2 {
		t.Fatalf("the writer ran %d times, want exactly one repeat", lines)
	}
}

// --redo names a step nobody judged. Anything else is refused rather than
// quietly ignored: silence would read as having redone it.
func TestRedoOnlyAppliesToUnjudgedSteps(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		declared("good", answers(t, dir, "good"), config.PoolAgent),
	)
	run, err := h.engine.Start(t.Context(), graphOf(step("a", "good", nil)))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// A finished run does not resume at all.
	if _, err := h.engine.Resume(t.Context(), run.ID, []string{"a"}); err == nil {
		t.Fatal("Resume: want a refusal for a run that already finished")
	}
}

// The two-Ateneas case: a record saying running is not evidence, the pid is.
func TestARunOwnedByALiveProcessIsNotTakenOver(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(),
		PID:   4242,
		Alive: func(pid int) bool { return pid == 9999 },
	}, "", declared("good", answers(t, dir, "good"), config.PoolAgent))

	plan, err := workflow.Compile(graphOf(step("a", "good", nil)),
		[]config.AgentType{declared("good", "/bin/true", config.PoolAgent)})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// A run left behind by a process that is still alive.
	if err := h.state.Create(t.Context(), "wf-live", plan, time.Now(), 9999); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Claim(t.Context(), "wf-live", "a", "trace-1", 1, time.Now(), 9999); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	_, err = h.engine.Resume(t.Context(), "wf-live", nil)
	if err == nil {
		t.Fatal("Resume: want a refusal while another Atenea holds the run")
	}
	if !strings.Contains(err.Error(), "9999") {
		t.Fatalf("error %q does not name the process holding it", err)
	}
}

// Atenea dying is not the same as Atenea being cut, and a resume can tell:
// the record says running, the pid it names is gone, so nobody judged that
// step and the reason says why.
func TestARunLeftByADeadProcessResumes(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(),
		PID:   4242,
		Alive: func(int) bool { return false },
	}, "", declared("good", answers(t, dir, "good"), config.PoolAgent))

	plan, err := workflow.Compile(graphOf(
		step("a", "good", nil),
		step("b", "good", []string{"a"}),
	), []config.AgentType{declared("good", "/bin/true", config.PoolAgent)})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := h.state.Create(t.Context(), "wf-dead", plan, time.Now(), 5150); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Claim(t.Context(), "wf-dead", "a", "trace-1", 1, time.Now(), 5150); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	run, err := h.engine.Resume(t.Context(), "wf-dead", nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := statuses(t, run); got["a"] != "ok" || got["b"] != "ok" {
		t.Fatalf("statuses = %v, want the graph finished", got)
	}
	if got := stepOf(t, run, "a").Attempt; got != 2 {
		t.Fatalf("attempt = %d, want the orphan redone as a second try", got)
	}
}

// Money: the grant is recorded and split, and what was spent reads as
// unmeasured rather than as a measured zero, because nothing can report a
// charge yet.
func TestSpendIsUnmeasuredRatherThanZero(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		declared("good", answers(t, dir, "good"), config.PoolAgent),
	)
	graph := graphOf(step("a", "good", nil))
	graph.GrantUSD = 2
	graph.Steps[0].Permission.BudgetUSD = 1

	run, err := h.engine.Start(t.Context(), graph)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	spend := run.Spend()
	if spend.MeasuredSteps != 0 || spend.UnmeasuredSteps != 1 {
		t.Fatalf("Spend = %+v, want nothing measured", spend)
	}
	if spend.USD != nil {
		t.Fatalf("USD = %v, want nil: nothing reported a price", spend.USD)
	}
	if got := run.Budget(); !strings.Contains(got, "unmeasured") {
		t.Fatalf("budget line = %q, want it to say unmeasured", got)
	}
	if strings.Contains(run.Budget(), "$0.00 spent") {
		t.Fatalf("budget line = %q: a measured-looking zero is the lie this avoids", run.Budget())
	}
	if got := stepOf(t, run, "a").Step.Permission.BudgetUSD; got != 1 {
		t.Fatalf("step share = %v, want the grant it was handed", got)
	}
	if run.GrantUSD != 2 {
		t.Fatalf("grant = %v, want what the graph opened with", run.GrantUSD)
	}
}

// Money: every step measured reports the tokens and a dollar total, and the
// total never appears without saying whose price produced it.
func TestARunWhereEveryStepIsMeasuredReportsTokensAndAPricedTotal(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		declared("claude", charged(t, dir, "claude", 30, 10, 0.05, "anthropic"), config.PoolAgent),
		declared("gpt", charged(t, dir, "gpt", 20, 5, 0.03, "openai"), config.PoolAgent),
	)
	graph := graphOf(step("a", "claude", nil), step("b", "gpt", nil))
	graph.GrantUSD = 1

	run, err := h.engine.Start(t.Context(), graph)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	spend := run.Spend()
	if spend.MeasuredSteps != 2 || spend.UnmeasuredSteps != 0 {
		t.Fatalf("Spend = %+v, want both steps measured", spend)
	}
	if spend.Tokens != 65 {
		t.Fatalf("Tokens = %d, want 65 across both steps", spend.Tokens)
	}
	if spend.USD == nil {
		t.Fatalf("USD is nil, want a total: every measured step named a price")
	}
	if diff := *spend.USD - 0.08; diff > 0.0001 || diff < -0.0001 {
		t.Fatalf("USD = %v, want approximately 0.08", *spend.USD)
	}

	got := run.Budget()
	if !strings.Contains(got, "65 tokens") {
		t.Fatalf("budget line = %q, want the token total", got)
	}
	if !strings.Contains(got, "$0.08") {
		t.Fatalf("budget line = %q, want the dollar total", got)
	}
	if !strings.Contains(got, "priced by anthropic, openai") {
		t.Fatalf("budget line = %q, want the dollar figure to name both prices behind it", got)
	}
	if !strings.Contains(got, "$0.92 left") {
		t.Fatalf("budget line = %q, want what remains of the grant", got)
	}
}

// A mixed run must not average its measured and unmeasured steps into one
// figure: the split is named, and nothing implies the whole run is priced.
func TestAMixedRunDoesNotReportItsTotalAsMeasured(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		declared("priced", charged(t, dir, "priced", 40, 8, 0.10, "anthropic"), config.PoolAgent),
		declared("silent", answers(t, dir, "silent"), config.PoolAgent),
	)
	graph := graphOf(step("a", "priced", nil), step("b", "silent", nil))
	graph.GrantUSD = 5

	run, err := h.engine.Start(t.Context(), graph)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	spend := run.Spend()
	if spend.MeasuredSteps != 1 || spend.UnmeasuredSteps != 1 {
		t.Fatalf("Spend = %+v, want one step measured and one not", spend)
	}

	got := run.Budget()
	if !strings.Contains(got, "1 of 2 steps measured") {
		t.Fatalf("budget line = %q, want the split named", got)
	}
	if !strings.Contains(got, "48 tokens") {
		t.Fatalf("budget line = %q, want the measured step's tokens", got)
	}
	if !strings.Contains(got, "priced by anthropic") {
		t.Fatalf("budget line = %q, want the measured step's price named", got)
	}
	if strings.Contains(got, "left") {
		t.Fatalf("budget line = %q: a run with an unmeasured step must not claim to know what remains", got)
	}
}

// Every step writes a trace row, and the workflow points at it. Two records,
// one history: the workflow says what the graph did, the trace says what each
// process did.
func TestEveryStepPointsAtItsTrace(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		declared("good", answers(t, dir, "good"), config.PoolAgent),
	)
	run, err := h.engine.Start(t.Context(), graphOf(
		step("a", "good", nil),
		step("b", "good", nil),
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	rows := make(map[string]trace.Row)
	for _, row := range rowsOf(t, h) {
		rows[row.ID] = row
	}
	if len(rows) != 2 {
		t.Fatalf("wrote %d trace rows, want one per step", len(rows))
	}
	for _, step := range run.Steps {
		row, ok := rows[step.TraceID]
		if !ok {
			t.Fatalf("step %s points at %q, which is not a trace row", step.Step.ID, step.TraceID)
		}
		if row.TypeName != step.Step.TypeName {
			t.Fatalf("trace %s is a %s, step %s is a %s",
				row.ID, row.TypeName, step.Step.ID, step.Step.TypeName)
		}
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return len(strings.Fields(string(raw)))
}
