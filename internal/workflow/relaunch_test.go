package workflow_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// A review that judged and said no sends the work back once, and the second
// attempt is a real second run: the whole point of the correction loop, and
// the thing a graph could not do before.

// refusesOnce is a reviewer that refuses the first answer it is shown and
// accepts the second. It counts on disk because each run is its own process.
func refusesOnce(t *testing.T, dir, name, sentence string) string {
	t.Helper()
	counter := filepath.Join(dir, name+".calls")
	body := fmt.Sprintf(`n=$(cat %[1]q 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > %[1]q
if [ "$n" = "1" ]; then
  echo '{"result":{},"verdict":"failed","reason":{"kind":"invalid_input","text":%[2]q}}'
else
  echo '{"result":{"ok":true},"verdict":"ok"}'
fi`, counter, sentence)
	return stub(t, dir, name, body)
}

// countingWork records how many times it ran and what it was handed each time.
func countingWork(t *testing.T, dir, name string) (command, log string) {
	t.Helper()
	log = filepath.Join(dir, name+".cards")
	script := "#!/bin/sh\ncat >> " + log + "\n" +
		`echo '{"result":{"ok":true},"verdict":"ok"}'` + "\n"
	command = filepath.Join(dir, name)
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}
	return command, log
}

// cards reads every assignment a counting stub was handed, in order.
func cards(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the cards: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	var out []map[string]any
	for {
		var one map[string]any
		if err := dec.Decode(&one); err != nil {
			break
		}
		out = append(out, one)
	}
	return out
}

func TestARefusedStepRunsAgainAndTheSecondAnswerIsAccepted(t *testing.T) {
	dir := t.TempDir()
	work, log := countingWork(t, dir, "work")
	h := newHarness(t, noCeiling(),
		declared("work", work, config.PoolAgent),
		declared("judge", refusesOnce(t, dir, "judge", "the count is wrong"), config.PoolReview),
	)

	run, err := h.engine.Start(t.Context(),
		graphOf(step("w", "work", nil), reviewing(step("j", "judge", nil), "w")))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	got := statuses(t, run)
	if got["w"] != "ok" || got["j"] != "ok" {
		t.Fatalf("statuses = %v, want both ok after the correction", got)
	}
	if handed := cards(t, log); len(handed) != 2 {
		t.Fatalf("the work ran %d times, want 2: refused once, then corrected", len(handed))
	}
	// Two attempts on the record, not one overwritten.
	for _, s := range run.Steps {
		if s.Step.ID == "w" && s.Attempt != 2 {
			t.Errorf("attempt = %d, want the second", s.Attempt)
		}
	}
}

// What the second attempt is handed: its own refused answer and the sentence
// that refused it. A relaunch told only "try again" reruns the same mistake.
func TestTheRelaunchedStepIsHandedTheRejection(t *testing.T) {
	dir := t.TempDir()
	work, log := countingWork(t, dir, "work")
	h := newHarness(t, noCeiling(),
		declared("work", work, config.PoolAgent),
		declared("judge", refusesOnce(t, dir, "judge", "notes.md has 40 lines, not 12"), config.PoolReview),
	)

	if _, err := h.engine.Start(t.Context(),
		graphOf(step("w", "work", nil), reviewing(step("j", "judge", nil), "w"))); err != nil {
		t.Fatalf("Start: %v", err)
	}

	handed := cards(t, log)
	if len(handed) != 2 {
		t.Fatalf("the work ran %d times, want 2", len(handed))
	}
	if handed[0]["rejected"] != nil {
		t.Errorf("the first attempt was handed a rejection: %v", handed[0]["rejected"])
	}
	rejected, ok := handed[1]["rejected"].(map[string]any)
	if !ok {
		t.Fatalf("the relaunch was handed no rejected card: %v", handed[1])
	}
	reason, ok := rejected["rejection"].(map[string]any)
	if !ok {
		t.Fatalf("the rejected card carries no rejection: %v", rejected)
	}
	if text, _ := reason["text"].(string); !strings.Contains(text, "40 lines") {
		t.Errorf("rejection = %v, want the reviewer's own sentence", reason)
	}
	if rejected["review_id"] == "" || rejected["review_id"] == nil {
		t.Error("the rejected card does not name the review that refused it")
	}
}

// A step whose work reads another step's answer keeps that input on the
// second try. Replacing it with its own refused answer would leave it
// re-planning from nothing, which is a worse attempt than the first.
func TestARelaunchKeepsTheInputItWasWorkingFrom(t *testing.T) {
	dir := t.TempDir()
	planner, log := countingWork(t, dir, "planner")
	types := []config.AgentType{
		declared("explorer", stub(t, dir, "explorer",
			`echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent),
		declared("planner", planner, config.PoolAgent),
		declared("judge", refusesOnce(t, dir, "judge", "that graph does not compile"), config.PoolReview),
	}
	types[1].ReadsSubject = true
	h := newHarness(t, noCeiling(), types...)

	if _, err := h.engine.Start(t.Context(), graphOf(
		step("e", "explorer", nil),
		reviewing(step("p", "planner", nil), "e"),
		reviewing(step("j", "judge", nil), "p"),
	)); err != nil {
		t.Fatalf("Start: %v", err)
	}

	handed := cards(t, log)
	if len(handed) != 2 {
		t.Fatalf("the planner ran %d times, want 2", len(handed))
	}
	subject, ok := handed[1]["subject"].(map[string]any)
	if !ok {
		t.Fatalf("the relaunched planner lost its input: %v", handed[1])
	}
	if subject["type"] != "explorer" {
		t.Errorf("subject type = %v, want the exploration it plans from", subject["type"])
	}
	if handed[1]["rejected"] == nil {
		t.Error("the relaunch kept its input but was not told what was wrong")
	}
}

// The bar is a judgement, not any bad news. A reviewer that could not judge --
// it died, it timed out, its own dependency was down -- has said nothing about
// the work, and re-running the work because its auditor broke spends money on
// somebody else's outage.
func TestAReviewThatCouldNotJudgeDoesNotRelaunchTheWork(t *testing.T) {
	dir := t.TempDir()
	work, log := countingWork(t, dir, "work")
	h := newHarness(t, noCeiling(),
		declared("work", work, config.PoolAgent),
		declared("judge", stub(t, dir, "judge",
			`echo '{"result":{},"verdict":"incomplete","reason":{"kind":"unavailable","text":"the file store is down"}}'`),
			config.PoolReview),
	)

	if _, err := h.engine.Start(t.Context(),
		graphOf(step("w", "work", nil), reviewing(step("j", "judge", nil), "w"))); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if handed := cards(t, log); len(handed) != 1 {
		t.Fatalf("the work ran %d times, want 1: nobody judged it", len(handed))
	}
}

// Twice refused is the end. A third attempt is a loop that bills for each
// turn of it, and an agent told the same thing twice writes the same answer.
func TestWorkRefusedTwiceIsNotRunAThirdTime(t *testing.T) {
	dir := t.TempDir()
	work, log := countingWork(t, dir, "work")
	h := newHarness(t, noCeiling(),
		declared("work", work, config.PoolAgent),
		declared("judge", stub(t, dir, "judge",
			`echo '{"result":{},"verdict":"failed","reason":{"kind":"invalid_input","text":"still wrong"}}'`),
			config.PoolReview),
	)

	run, err := h.engine.Start(t.Context(),
		graphOf(step("w", "work", nil), reviewing(step("j", "judge", nil), "w")))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if handed := cards(t, log); len(handed) != 2 {
		t.Fatalf("the work ran %d times, want 2: one correction, not a loop", len(handed))
	}
	if got := statuses(t, run); got["j"] != "failed" {
		t.Errorf("statuses = %v, want the review's refusal to stand", got)
	}
}

// The refused attempt cost real money. A receipt that keeps only the accepted
// half understates the bill by exactly what the correction cost.
func TestTheRefusedAttemptsChargeStaysOnTheReceipt(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(),
		declared("work", charged(t, dir, "work", 1000, 200, 0.03, "a price list"), config.PoolAgent),
		declared("judge", refusesOnce(t, dir, "judge", "wrong"), config.PoolReview),
	)

	run, err := h.engine.Start(t.Context(),
		graphOf(step("w", "work", nil), reviewing(step("j", "judge", nil), "w")))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	spend := run.Spend()
	if spend.USD == nil {
		t.Fatalf("the run reports no dollar figure: %+v", spend)
	}
	if *spend.USD < 0.059 || *spend.USD > 0.061 {
		t.Errorf("spent $%.4f, want both attempts ($0.06)", *spend.USD)
	}
	if spend.Tokens != 2400 {
		t.Errorf("tokens = %d, want both attempts (2400)", spend.Tokens)
	}
}

// Charge addition on its own: two runs of one piece of work, and the rule
// about a figure only one half carries.
func TestAddingChargesKeepsTheDollarFigureOnlyWhenBothHaveOne(t *testing.T) {
	priced := contract.Charge{InputTokens: 10, USD: usdOf(0.02), PricedBy: "list"}
	unpriced := contract.Charge{InputTokens: 5}

	both := priced.Plus(contract.Charge{InputTokens: 1, USD: usdOf(0.01), PricedBy: "list"})
	if both.USD == nil || *both.USD != 0.03 {
		t.Errorf("usd = %v, want the two added", both.USD)
	}
	if both.InputTokens != 11 {
		t.Errorf("tokens = %d, want 11", both.InputTokens)
	}

	half := priced.Plus(unpriced)
	if half.USD != nil {
		t.Errorf("usd = %v, want nil: printing the priced half as the total is the lie", half.USD)
	}
	if half.InputTokens != 15 {
		t.Errorf("tokens = %d, want 15: counts are never guessed", half.InputTokens)
	}

	nothing := contract.Charge{}
	if got := priced.Plus(nothing); got.USD == nil || *got.USD != 0.02 {
		t.Errorf("adding an unmeasured charge changed the figure: %+v", got)
	}
}

func usdOf(v float64) *float64 { return &v }
