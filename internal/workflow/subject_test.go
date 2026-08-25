package workflow_test

import (
	"context"
	"encoding/json"
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

// records writes a stub that keeps the assignment it was handed and then
// answers ok. The test reads what actually arrived on the wire rather than
// what the engine meant to send; the two agree only if the whole path works.
func records(t *testing.T, dir, name string) (command, saved string) {
	t.Helper()
	saved = filepath.Join(dir, name+".assignment")
	script := "#!/bin/sh\ncat > " + saved + "\n" +
		`echo '{"result":{"ok":true},"verdict":"ok"}'` + "\n"
	command = filepath.Join(dir, name)
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the recorder: %v", err)
	}
	return command, saved
}

// wireSubject is the subject half of the assignment JSON, named exactly as the
// wire names it. Reading it with its own struct is deliberate: if the wire
// renames a field, this test fails instead of quietly seeing a zero value.
type wireSubject struct {
	RunID    string         `json:"run_id"`
	TypeName string         `json:"type"`
	Attempt  int            `json:"attempt"`
	Task     wireTask       `json:"task"`
	Result   map[string]any `json:"result"`
	Verdict  string         `json:"verdict"`
	Reason   *wireReason    `json:"reason"`
}

type wireTask struct {
	Objective string   `json:"objective"`
	Files     []string `json:"files"`
	Criterion string   `json:"criterion"`
}

type wireReason struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func readSubject(t *testing.T, path string) wireSubject {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the assignment: %v", err)
	}
	var envelope struct {
		Subject *wireSubject `json:"subject"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("parsing the assignment: %v", err)
	}
	if envelope.Subject == nil {
		t.Fatalf("no subject on the assignment: %s", raw)
	}
	return *envelope.Subject
}

// The whole validated report travels, and it is the upstream's own task the
// reviewer is shown -- not the reviewer's, and not a summary of either.
func TestTheSubjectCarriesTheWholeAnswer(t *testing.T) {
	dir := t.TempDir()
	judge, saved := records(t, dir, "judge")
	h := newHarness(t, noCeiling(),
		declared("work", stub(t, dir, "work",
			`echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent),
		declared("judge", judge, config.PoolReview),
	)

	work := step("w", "work", nil)
	work.Task = contract.Task{
		Objective: "count the lines in notes.md",
		Files:     []string{"notes.md"},
		Criterion: "the count matches the file",
	}
	run, err := h.engine.Start(t.Context(), graphOf(work, reviewing(step("j", "judge", nil), "w")))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := statuses(t, run); got["j"] != "ok" {
		t.Fatalf("statuses = %v, want the reviewer to have run", got)
	}

	subject := readSubject(t, saved)
	if subject.TypeName != "work" {
		t.Errorf("subject type = %q, want the agent that answered", subject.TypeName)
	}
	if subject.Verdict != "ok" {
		t.Errorf("subject verdict = %q, want the verdict as validated", subject.Verdict)
	}
	if subject.Result["ok"] != true {
		t.Errorf("subject result = %v, want the answer itself", subject.Result)
	}
	if subject.Attempt != 1 {
		t.Errorf("subject attempt = %d, want the try it was", subject.Attempt)
	}
	// The task is the work's, criterion included: a review judges the answer
	// against what was actually asked.
	if subject.Task.Objective != "count the lines in notes.md" {
		t.Errorf("subject objective = %q, want the work's own", subject.Task.Objective)
	}
	if subject.Task.Criterion != "the count matches the file" {
		t.Errorf("subject criterion = %q, want the work's own", subject.Task.Criterion)
	}
	// The run id has to be the trace row the work really ran as, or the
	// review cannot be linked back to what it reviewed.
	var workTrace string
	for _, row := range run.Steps {
		if row.Step.ID == "w" {
			workTrace = row.TraceID
		}
	}
	if subject.RunID != workTrace {
		t.Errorf("subject run id = %q, want the work's trace %q", subject.RunID, workTrace)
	}

	// The trace row has to carry the link too, the same one --review writes.
	// Kept only in the workflow tables, a graph's reviews would be invisible
	// to every reader of the traces.
	rows, err := h.traces.List(t.Context(), trace.Filter{TypeName: "judge"})
	if err != nil {
		t.Fatalf("listing traces: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("reviewer trace rows = %d, want 1", len(rows))
	}
	if rows[0].Reviews != workTrace {
		t.Errorf("trace reviews = %q, want the run it audited %q", rows[0].Reviews, workTrace)
	}
}

// A failure is an answer. The default bar hands it over, because "it says it
// failed" is the claim most worth auditing.
func TestAFailedAnswerIsStillReviewed(t *testing.T) {
	dir := t.TempDir()
	judge, saved := records(t, dir, "judge")
	h := newHarness(t, noCeiling(),
		declared("work", stub(t, dir, "work",
			`echo '{"result":{},"verdict":"failed","reason":{"kind":"not_found","text":"no such file"}}'`),
			config.PoolAgent),
		declared("judge", judge, config.PoolReview),
	)

	run, err := h.engine.Start(t.Context(), graphOf(
		step("w", "work", nil),
		reviewing(step("j", "judge", nil), "w"),
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := statuses(t, run)
	if got["w"] != "failed" || got["j"] != "ok" {
		t.Fatalf("statuses = %v, want the failure audited", got)
	}
	subject := readSubject(t, saved)
	if subject.Verdict != "failed" {
		t.Errorf("subject verdict = %q, want the failure as it stood", subject.Verdict)
	}
	if subject.Reason == nil || subject.Reason.Text != "no such file" {
		t.Errorf("subject reason = %+v, want the sentence the agent gave", subject.Reason)
	}
}

// An agent that stopped short still answered. Same bar, same handover.
func TestAnIncompleteAnswerIsStillReviewed(t *testing.T) {
	dir := t.TempDir()
	judge, _ := records(t, dir, "judge")
	h := newHarness(t, noCeiling(),
		declared("work", stub(t, dir, "work",
			`echo '{"result":{"ok":true},"verdict":"incomplete","reason":{"kind":"timeout","text":"ran out of room"}}'`),
			config.PoolAgent),
		declared("judge", judge, config.PoolReview),
	)

	run, err := h.engine.Start(t.Context(), graphOf(
		step("w", "work", nil),
		reviewing(step("j", "judge", nil), "w"),
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := statuses(t, run)
	if got["w"] != "incomplete" || got["j"] != "ok" {
		t.Fatalf("statuses = %v, want the incomplete answer audited", got)
	}
}

// `on = "ok"` is the stricter bar, and it is a word rather than a second
// `needs` line naming the same step: two lines that look redundant are two
// lines a reader tidies down to one.
func TestOnOKSkipsAReviewOfAFailure(t *testing.T) {
	dir := t.TempDir()
	judge, saved := records(t, dir, "judge")
	h := newHarness(t, noCeiling(),
		declared("work", stub(t, dir, "work",
			`echo '{"result":{},"verdict":"failed","reason":{"kind":"not_found","text":"no such file"}}'`),
			config.PoolAgent),
		declared("judge", judge, config.PoolReview),
	)

	run, err := h.engine.Start(t.Context(), graphOf(
		step("w", "work", nil),
		reviewingOnly(step("j", "judge", nil), "w", workflow.OnOK),
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := run.Label(rowOf(t, run, "j")); got != "blocked" {
		t.Fatalf("reviewer = %s, want it held back by the stricter bar", got)
	}
	if _, err := os.Stat(saved); err == nil {
		t.Fatal("the reviewer ran anyway")
	}
	if reason := run.BlockReason("j"); reason != "waits on w" {
		t.Errorf("block reason = %q, want the plain ordering sentence", reason)
	}
}

// The one case that is never handed over: a run nobody judged. There is no
// verdict to carry, and a card claiming an answer that was never given is the
// mistake this whole rule exists to refuse.
func TestASubjectNobodyJudgedBlocksTheReview(t *testing.T) {
	dir := t.TempDir()
	judge, saved := records(t, dir, "judge")
	h := newHarness(t, config.Workflow{MaxParallelAgent: 1},
		// It says when it has started, so the cut lands on a run that is
		// genuinely under way. A fixed delay cancels whatever the machine
		// happened to be doing 300ms in, which under load is a run whose
		// agent had not spawned.
		declared("slow", stub(t, dir, "slow",
			"touch "+filepath.Join(dir, "slow-started")+"\nsleep 5"), config.PoolAgent),
		declared("judge", judge, config.PoolReview),
	)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		waitFor(t, dir, "slow-started")
		cancel()
	}()
	run, err := h.engine.Start(ctx, graphOf(
		step("w", "slow", nil),
		reviewing(step("j", "judge", nil), "w"),
	))
	if err == nil {
		t.Fatal("a cut run reported success")
	}
	got := statuses(t, run)
	if got["w"] != "interrupted" {
		t.Fatalf("statuses = %v, want the cut step unjudged", got)
	}
	if label := run.Label(rowOf(t, run, "j")); label != "blocked" {
		t.Fatalf("reviewer = %s, want it blocked by an unjudged subject", label)
	}
	if _, err := os.Stat(saved); err == nil {
		t.Fatal("a reviewer was handed a card from a run nobody judged")
	}

	// The sentence carries its own cure: neither the block nor the command
	// that clears it is guessable from "waits on w".
	reason := run.BlockReason("j")
	for _, want := range []string{"never judged", "no answer to review",
		"atenea workflow resume " + run.ID} {
		if !strings.Contains(reason, want) {
			t.Errorf("block reason %q does not mention %q", reason, want)
		}
	}
	// The subject only read, so a plain resume redoes it. Printing --redo
	// here would teach the bigger hammer for no reason.
	if strings.Contains(reason, "--redo") {
		t.Errorf("block reason %q asks for --redo to redo a read-only step", reason)
	}
}

// The same block, over a subject that may have written: the cure is the
// heavier one, because a resume deliberately leaves that step alone.
func TestAnUnjudgedWriterNamesRedoInItsCure(t *testing.T) {
	dir := t.TempDir()
	judge, _ := records(t, dir, "judge")
	h := newHarness(t, config.Workflow{MaxParallelAgent: 1},
		declared("scribe", stub(t, dir, "scribe",
			"touch "+filepath.Join(dir, "scribe-started")+"\nsleep 5"), config.PoolAgent,
			contract.EffectRead, contract.EffectWrite),
		declared("judge", judge, config.PoolReview),
	)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		waitFor(t, dir, "scribe-started")
		cancel()
	}()
	run, _ := h.engine.Start(ctx, graphOf(
		step("w", "scribe", nil, contract.EffectRead, contract.EffectWrite),
		reviewing(step("j", "judge", nil), "w"),
	))
	if got := statuses(t, run); got["w"] != "interrupted" {
		t.Fatalf("statuses = %v, want the writer unjudged", got)
	}
	if reason := run.BlockReason("j"); !strings.Contains(reason, "--redo w") {
		t.Errorf("block reason %q does not name --redo for a step resume will not touch", reason)
	}
}

// rowOf pulls one step's record out of a run.
func rowOf(t *testing.T, run workflow.Run, id string) workflow.StepRow {
	t.Helper()
	for _, row := range run.Steps {
		if row.Step.ID == id {
			return row
		}
	}
	t.Fatalf("no step %s on the record", id)
	return workflow.StepRow{}
}

// The answer a reviewer audits may have been given by an Atenea that is gone.
// Rebuilt from the record, the card has to be the same one: the run that
// answered, its task, its verdict, its result.
func TestASubjectSurvivesAResume(t *testing.T) {
	dir := t.TempDir()
	judge, saved := records(t, dir, "judge")
	entered := filepath.Join(dir, "review-entered")
	// The work answers, then the reviewer's slot is taken by a step that
	// hangs until the run is cut -- so the review is dispatched only by the
	// second process, reading an answer this one never saw.
	h := newHarness(t, config.Workflow{MaxParallelAgent: 1, MaxParallelReview: 1},
		declared("work", stub(t, dir, "work",
			`echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent),
		declared("hangs", stub(t, dir, "hangs", "touch "+entered+"\nsleep 5"), config.PoolReview),
		declared("judge", judge, config.PoolReview),
	)

	work := step("w", "work", nil)
	work.Task = contract.Task{
		Objective: "read notes.md",
		Files:     []string{"notes.md"},
		Criterion: "the count matches",
	}
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
	first, err := h.engine.Start(ctx, graphOf(
		work,
		reviewing(step("hog", "hangs", nil), "w"),
		reviewing(step("j", "judge", nil), "w"),
	))
	if err == nil {
		t.Fatal("a cut run reported success")
	}
	if got := statuses(t, first); got["w"] != "ok" || got["j"] != "pending" {
		t.Fatalf("statuses = %v, want the work answered and the review not yet run", got)
	}
	if _, err := os.Stat(saved); err == nil {
		t.Fatal("the reviewer ran in the first process; this tests the second")
	}

	// A second engine over the same database: nothing in memory carries over.
	second := newHarnessOver(t, h.dir, config.Workflow{MaxParallelAgent: 1, MaxParallelReview: 1},
		declared("work", stub(t, dir, "work",
			`echo '{"result":{"ok":true},"verdict":"ok"}'`), config.PoolAgent),
		declared("hangs", answers(t, dir, "hangs-quick"), config.PoolReview),
		declared("judge", judge, config.PoolReview),
	)
	run, err := second.engine.Resume(t.Context(), first.ID, []string{"hog"})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := statuses(t, run); got["j"] != "ok" {
		t.Fatalf("statuses = %v, want the review dispatched by the resume", got)
	}

	subject := readSubject(t, saved)
	if subject.Verdict != "ok" || subject.Result["ok"] != true {
		t.Errorf("subject = %+v, want the answer as it was given", subject)
	}
	if subject.Task.Objective != "read notes.md" || subject.Task.Criterion != "the count matches" {
		t.Errorf("subject task = %+v, want the work's own, off the record", subject.Task)
	}
	if subject.RunID != rowOf(t, run, "w").TraceID {
		t.Errorf("subject run id = %q, want the trace the work really ran as", subject.RunID)
	}
}
