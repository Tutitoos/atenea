package workflow_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// What a step cut at its own spending ceiling looks like on the wire: a
// verdict of incomplete, the reason kind a real outage also writes -- see
// StepRow.CutAtItsCeiling for why those are the same word -- and a charge
// that reached the share.
func cutAtCeiling(t *testing.T, dir, name string, usd float64) string {
	t.Helper()
	body := fmt.Sprintf(`echo '{"verdict":"incomplete",`+
		`"reason":{"kind":"unavailable",`+
		`"text":"claude code stopped at its spending ceiling before it could answer"},`+
		`"spent":{"usd":%v,"priced_by":"a test"}}'`, usd)
	return stub(t, dir, name, body)
}

// rowFor is one step of a run, by id.
func rowFor(run workflow.Run, id string) (workflow.StepRow, bool) {
	for _, step := range run.Steps {
		if step.Step.ID == id {
			return step, true
		}
	}
	return workflow.StepRow{}, false
}

// cutThenAnswers dies at its ceiling on the first dispatch and answers on the
// second, which is the sequence a redo exists to produce. The counter is a
// file because each dispatch is a fresh process: two invocations of one script
// share nothing else.
func cutThenAnswers(t *testing.T, dir, name string, cut, spent float64) string {
	t.Helper()
	counter := filepath.Join(dir, name+".n")
	body := fmt.Sprintf(`n=$(cat %[1]q 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" >%[1]q
if [ "$n" = 1 ]; then
  echo '{"verdict":"incomplete","reason":{"kind":"unavailable",`+
		`"text":"claude code stopped at its spending ceiling before it could answer"},`+
		`"spent":{"usd":%[2]v,"priced_by":"a test"}}'
else
  echo '{"result":{"ok":true},"verdict":"ok",`+
		`"spent":{"input_tokens":11,"output_tokens":7,"usd":%[3]v,"priced_by":"a test"}}'
fi`, counter, cut, spent)
	return stub(t, dir, name, body)
}

// deadEnd is a run whose single step spent its whole share and answered
// nothing: the state 150 steps on this machine are in, and the one nothing
// could dispatch out of before redo existed.
func deadEnd(t *testing.T, share float64) (*harness, workflow.Run) {
	t.Helper()
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		declared("reader", cutAtCeiling(t, dir, "reader", share), config.PoolAgent))
	run, err := h.engine.Start(t.Context(), commissioned(funded("a", "reader", share)))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := statuses(t, run)["a"]; got != "incomplete" {
		t.Fatalf("step a is %s, want incomplete", got)
	}
	if !run.Closed {
		t.Fatal("the run is still open: a ceiling death is judged, so the run finishes")
	}
	return h, run
}

// The whole path, and the pair it leaves behind. Before this existed the only
// way to retry a step that ran out of money was to write a new plan, which is
// why the record holds 150 ceiling deaths and 2 re-dispatches.
func TestAStepCutAtItsCeilingIsRedoneAtARaisedShare(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		declared("reader", cutThenAnswers(t, dir, "reader", 0.10, 0.14), config.PoolAgent))
	run, err := h.engine.Start(t.Context(), commissioned(funded("a", "reader", 0.10)))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := statuses(t, run)["a"]; got != "incomplete" {
		t.Fatalf("step a is %s, want incomplete", got)
	}

	out, err := h.engine.Redo(t.Context(), run.ID,
		[]workflow.Raise{{StepID: "a", USD: 0.20}}, 0.20)
	if err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if got := statuses(t, out)["a"]; got != "ok" {
		t.Fatalf("step a is %s after the redo, want ok", got)
	}

	// The receipt the admission rule has never had. CostByType excludes a
	// ceiling death because its spend is a lower bound, so a share that
	// FINISHED the work is the only measurement of what the work costs -- and
	// it only exists once both halves are on the record together.
	live, ok := rowFor(out, "a")
	if !ok {
		t.Fatal("the run has no step a")
	}
	if live.Step.Permission.BudgetUSD != 0.20 {
		t.Errorf("the live share is $%.2f, want $0.20", live.Step.Permission.BudgetUSD)
	}
	if live.Spent.USD == nil || *live.Spent.USD != 0.14 {
		t.Errorf("the live spend is %v, want $0.14", live.Spent.USD)
	}
	attempts, err := h.state.Attempts(t.Context(), run.ID, "a")
	if err != nil {
		t.Fatalf("Attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("%d superseded attempts, want 1", len(attempts))
	}
	if attempts[0].GrantUSD != 0.10 {
		t.Errorf("the dead attempt ran under $%.2f, want $0.10: Reshare rewrote the archive",
			attempts[0].GrantUSD)
	}
	if !attempts[0].Cut() {
		t.Error("the dead attempt does not read as cut")
	}
}

// The gate log is where authorized money lives, and a redo authorizes money
// against work whose cost is already known -- which is why it is its own kind
// and not an expansion.
func TestARedoIsRecordedAsAGateSomebodyAnswered(t *testing.T) {
	h, run := deadEnd(t, 0.10)
	if _, err := h.engine.Redo(t.Context(), run.ID,
		[]workflow.Raise{{StepID: "a", USD: 0.20}}, 0.20); err != nil {
		// The second attempt dies at its ceiling too -- this stub always
		// does -- and that is not what this test is about.
		if !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("Redo: %v", err)
		}
	}
	gates, err := h.state.Gates(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Gates: %v", err)
	}
	last := gates[len(gates)-1]
	if last.Kind != workflow.KindRedo {
		t.Fatalf("the last gate is %s, want redo", last.Kind)
	}
	if last.Decision != workflow.DecisionApproved {
		t.Errorf("the redo gate is %s, want approved", last.Decision)
	}
	if last.Hand == "" {
		t.Error("the redo gate names nobody: an authorization with no hand is not one")
	}
	// The share it blessed, so a reader is not left diffing rows to find out
	// what the money was for.
	if len(last.Proposal.Steps) != 1 || last.Proposal.Steps[0].Permission.BudgetUSD != 0.20 {
		t.Errorf("the gate's proposal is %+v, want step a at $0.20", last.Proposal.Steps)
	}
}

// A step that died at its ceiling dies again at the same share. Spending real
// money to reproduce a result already on the record is the one thing this
// command must not make easy.
func TestARedoAtTheSameShareIsRefused(t *testing.T) {
	h, run := deadEnd(t, 0.10)
	_, err := h.engine.Redo(t.Context(), run.ID,
		[]workflow.Raise{{StepID: "a", USD: 0.10}}, 0)
	if err == nil {
		t.Fatal("a redo at the share that was already cut was accepted")
	}
	message := err.Error()
	for _, want := range []string{"$0.10", "not a raise", "same share buys the same result"} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, message)
		}
	}
	// And nothing moved: a refusal reopens no run.
	after, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !after.Closed {
		t.Error("the refused redo reopened the run anyway")
	}
}

// Two paths, two populations, and a refusal that names the other one. A step
// nobody judged may be run again as it was; that is resume's job and it costs
// no more money than it was already granted.
func TestARedoOfAStepNobodyJudgedNamesResume(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		declared("reader", answers(t, dir, "reader"), config.PoolAgent))
	run, _, err := h.engine.Create(t.Context(), commissioned(funded("a", "reader", 0.10)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.state.Interrupt(t.Context(), run.ID, "a", "cut by abort", time.Now()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	_, err = h.engine.Redo(t.Context(), run.ID,
		[]workflow.Raise{{StepID: "a", USD: 0.20}}, 0.20)
	if err == nil {
		t.Fatal("a redo of an interrupted step was accepted")
	}
	message := err.Error()
	if !strings.Contains(message, "interrupted") {
		t.Errorf("the refusal does not name the status:\n%s", message)
	}
	if !strings.Contains(message, "resume") {
		t.Errorf("the refusal does not name the path that does serve it:\n%s", message)
	}
}

// A step that answered is not a step that ran out of money, however much of
// its share it spent. The status is what separates them.
func TestARedoOfAStepThatAnsweredIsRefused(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		// Spends its whole share AND answers: the band alone would call this
		// a ceiling death.
		declared("reader", charged(t, dir, "reader", 11, 7, 0.10, "a test"), config.PoolAgent))
	run, err := h.engine.Start(t.Context(), commissioned(funded("a", "reader", 0.10)))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := statuses(t, run)["a"]; got != "ok" {
		t.Fatalf("step a is %s, want ok", got)
	}
	_, err = h.engine.Redo(t.Context(), run.ID,
		[]workflow.Raise{{StepID: "a", USD: 0.20}}, 0.20)
	if err == nil {
		t.Fatal("a step that answered at its ceiling was redone")
	}
	if !strings.Contains(err.Error(), "not cut at its ceiling") {
		t.Errorf("the refusal does not say why:\n%s", err)
	}
}

// The grant is the figure somebody authorized. A redo that moved it by itself
// would make the check that exists to catch unapproved spend unable to fail,
// so the operator raises it in the same breath or the redo refuses.
func TestARedoPastTheGrantIsRefusedUntilTheGrantIsRaised(t *testing.T) {
	h, run := deadEnd(t, 0.10)
	_, err := h.engine.Redo(t.Context(), run.ID,
		[]workflow.Raise{{StepID: "a", USD: 0.20}}, 0)
	if err == nil {
		t.Fatal("a raise past the grant was accepted with the grant untouched")
	}
	if !strings.Contains(err.Error(), "$0.10") {
		t.Errorf("the refusal does not name the grant it would exceed:\n%s", err)
	}

	// Named explicitly, the same raise goes through.
	if _, err := h.engine.Redo(t.Context(), run.ID,
		[]workflow.Raise{{StepID: "a", USD: 0.20}}, 0.20); err != nil {
		if !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("Redo with a raised grant: %v", err)
		}
	}
	after, err := h.state.Load(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.GrantUSD != 0.20 {
		t.Errorf("the grant is $%.2f, want $0.20", after.GrantUSD)
	}
}

// A grant is raised, never lowered: rows already on the record would otherwise
// read as having spent money nobody allowed.
func TestAGrantIsNotLowered(t *testing.T) {
	h, run := deadEnd(t, 0.10)
	err := h.state.Regrant(t.Context(), run.ID, 0.05)
	if err == nil {
		t.Fatal("the grant was lowered under a step that already ran")
	}
	if !strings.Contains(err.Error(), "never lowered") {
		t.Errorf("the refusal does not say why:\n%s", err)
	}
}

// A raised share meets the same admission rule a new plan would. Finding out
// by spending it is exactly what that rule exists to prevent -- and the case
// is real: a floor re-measured upward between the run and the retry.
func TestARaisedShareStillUnderTheFloorIsRefused(t *testing.T) {
	dir := t.TempDir()
	cheap := floorsOf("current", "reader", "claude-opus-5", measuredFloor(0.01))
	types := []config.AgentType{
		declared("reader", cutAtCeiling(t, dir, "reader", 0.10), config.PoolAgent),
	}
	h := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(), Repository: "current", Floors: cheap,
		ModelFor: func(string) string { return "claude-opus-5" },
	}, dir, types...)
	run, err := h.engine.Start(t.Context(), commissioned(funded("a", "reader", 0.10)))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Re-measured: what starting a turn costs turns out to be far more than
	// the plan was priced against.
	dear := floorsOf("current", "reader", "claude-opus-5", measuredFloor(5.00))
	second := newHarnessWith(t, workflow.Options{
		Lanes: noCeiling(), Repository: "current", Floors: dear,
		ModelFor: func(string) string { return "claude-opus-5" },
	}, dir, types...)
	_, err = second.engine.Redo(t.Context(), run.ID,
		[]workflow.Raise{{StepID: "a", USD: 0.20}}, 0.20)
	if err == nil {
		t.Fatal("a raise that cannot clear the floor was dispatched")
	}
	if !strings.Contains(err.Error(), "workflow create refused") &&
		!strings.Contains(err.Error(), "funded") {
		t.Errorf("the refusal is not the funding rule's:\n%s", err)
	}
}

// Naming one step twice is a typo whose silent reading is "the last one wins",
// and what it silently changes is how much money a step gets.
func TestAStepNamedTwiceIsRefused(t *testing.T) {
	h, run := deadEnd(t, 0.10)
	_, err := h.engine.Redo(t.Context(), run.ID, []workflow.Raise{
		{StepID: "a", USD: 0.20}, {StepID: "a", USD: 0.30},
	}, 1.00)
	if err == nil {
		t.Fatal("one step was named twice and accepted")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("the refusal does not say why:\n%s", err)
	}
}

// A token count of zero beside a real charge is not a measurement of a cheap
// step, it is a missing measurement. 29 rows on this machine are in that state
// and a reader totalling them is told the steps did almost nothing.
func TestARowChargedWithNoTokensReadsAsTruncated(t *testing.T) {
	usd := 0.62
	truncated := workflow.StepRow{Spent: contract.Charge{USD: &usd, PricedBy: "a test"}}
	if !truncated.Truncated() {
		t.Error("a row with dollars and no tokens does not read as truncated")
	}
	whole := workflow.StepRow{Spent: contract.Charge{
		InputTokens: 1416, CacheReadTokens: 4772, OutputTokens: 152,
		USD: &usd, PricedBy: "a test",
	}}
	if whole.Truncated() {
		t.Error("a row with tokens reads as truncated")
	}
	// Nothing reported at all is unmeasured, which the record already says a
	// different way. Calling it truncated would claim a charge nobody made.
	if (workflow.StepRow{}).Truncated() {
		t.Error("an unmeasured row reads as truncated")
	}
}

// The total has to carry the caveat, because the total is what gets read.
func TestARunWithATruncatedRowSaysItsTokenCountIsAFloor(t *testing.T) {
	cut, whole := 0.62, 0.19
	run := workflow.Run{GrantUSD: 1.00, Steps: []workflow.StepRow{
		{Spent: contract.Charge{USD: &cut, PricedBy: "a test"}},
		{Spent: contract.Charge{
			InputTokens: 90, OutputTokens: 10, USD: &whole, PricedBy: "a test",
		}},
	}}
	spend := run.Spend()
	if spend.TruncatedSteps != 1 {
		t.Fatalf("TruncatedSteps = %d, want 1", spend.TruncatedSteps)
	}
	if spend.MeasuredSteps != 2 {
		t.Errorf("MeasuredSteps = %d, want 2: the dollars are real", spend.MeasuredSteps)
	}
	line := run.Budget()
	for _, want := range []string{"at least 100 tokens", "1 step charged with no token record"} {
		if !strings.Contains(line, want) {
			t.Errorf("the budget line does not say %q:\n%s", want, line)
		}
	}
	// And a run with nothing missing does not grow the caveat.
	clean := workflow.Run{GrantUSD: 1.00, Steps: []workflow.StepRow{run.Steps[1]}}
	if got := clean.Budget(); strings.Contains(got, "at least") {
		t.Errorf("a whole run hedges its token count:\n%s", got)
	}
}
