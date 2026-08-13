package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// reviewerType declares a fake reviewer over a stub. Real reviewers are model
// harnesses; everything this loop does happens around one, so none is needed
// to exercise it.
func reviewerType(command string) config.AgentType {
	return config.AgentType{
		Spec: contract.AgentTypeSpec{
			Name: "critic",
			Kind: contract.AgentSpecialized,
			Result: []contract.Field{
				{Name: "checked", Type: contract.TypeInt, Required: true, Summary: "claims verified"},
			},
		},
		Summary: "a fake reviewer that answers fixed verdicts",
		Command: command,
		Context: []contract.ContextLevel{contract.ContextRepository},
		Effects: []contract.Effect{contract.EffectRead},
		Limits:  contract.Limits{MaxDuration: 10 * time.Second, MaxTokens: 100},
		Pool:    config.PoolReview,
	}
}

// verdicts builds a reviewer that answers one verdict per call, in order, and
// keeps every subject it was handed.
func verdicts(t *testing.T, dir string, answers ...string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"n=$(ls " + dir + "/subject-* 2>/dev/null | wc -l)\n" +
		"n=$((n + 1))\n" +
		"cat >" + dir + "/subject-$n.json\n" +
		"case $n in\n"
	for i, answer := range answers {
		script += "  " + strconv.Itoa(i+1) + ") cat <<'REPORT'\n" + answer + "\nREPORT\n  ;;\n"
	}
	script += "  *) cat <<'REPORT'\n" +
		`{"result":{"checked":0},"verdict":"incomplete","reason":{"kind":"unavailable","text":"asked once too often"}}` +
		"\nREPORT\n  ;;\nesac\n"

	path := filepath.Join(dir, "critic")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the reviewer stub: %v", err)
	}
	return path
}

const (
	passes  = `{"result":{"checked":3},"verdict":"ok"}`
	refuses = `{"result":{"checked":3},"verdict":"failed","reason":{"kind":"invalid_input","text":"bytes: answered 999, the file has 14"}}`
)

// reviewed builds a runner over a work type and a reviewer, returning both the
// runner and the store so a test can read the rows that were written.
func reviewed(t *testing.T, work config.AgentType, reviewer config.AgentType) (*agent.Runner, *trace.Store) {
	t.Helper()
	store, err := trace.Open(t.Context(), filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("opening the trace store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	r, err := agent.New(agent.Options{
		Types: []config.AgentType{work, reviewer},
		Store: store,
		Self:  "/nonexistent/atenea",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, store
}

// An accepted answer is one attempt and one review, and nothing is relaunched.
func TestAcceptedOnTheFirstAttempt(t *testing.T) {
	dir := t.TempDir()
	work := declared(answers(t, `{"result":{"path":"a.txt"},"verdict":"ok"}`))
	r, store := reviewed(t, work, reviewerType(verdicts(t, dir, passes)))

	run, err := r.RunReviewed(t.Context(), "reader", "critic", task())
	if err != nil {
		t.Fatalf("RunReviewed: %v", err)
	}
	if !run.Accepted() || len(run.Attempts) != 1 {
		t.Fatalf("run = %+v, want one accepted attempt", run)
	}
	if run.Final().Result["path"] != "a.txt" {
		t.Fatalf("final result = %v, want the work's answer", run.Final().Result)
	}

	rows, err := store.List(t.Context(), trace.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("wrote %d rows, want the work and its review", len(rows))
	}
	work1 := run.Attempts[0]
	review := rowByID(t, rows, work1.Review.ID)
	if review.Reviews != work1.Work.ID {
		t.Fatalf("review row reviews %q, want %q", review.Reviews, work1.Work.ID)
	}
	if review.RetryOf != "" || review.Attempt != 1 {
		t.Fatalf("review row = %+v, want a first, non-retry row", review)
	}
	if got := rowByID(t, rows, work1.Work.ID); got.Attempt != 1 || got.RetryOf != "" {
		t.Fatalf("work row = %+v, want attempt 1 and no retry link", got)
	}
}

// A refusal relaunches the work once, and the second attempt is a SECOND row
// linked to the first -- not a fresh run of the same type.
func TestRefusalRelaunchesOnceAndLinksTheRows(t *testing.T) {
	dir := t.TempDir()
	work := declared(answers(t, `{"result":{"path":"a.txt"},"verdict":"ok"}`))
	r, store := reviewed(t, work, reviewerType(verdicts(t, dir, refuses, passes)))

	run, err := r.RunReviewed(t.Context(), "reader", "critic", task())
	if err != nil {
		t.Fatalf("RunReviewed: %v", err)
	}
	if !run.Accepted() {
		t.Fatal("run: the second attempt passed; want accepted")
	}
	if len(run.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(run.Attempts))
	}

	rows, _ := store.List(t.Context(), trace.Filter{})
	if len(rows) != 4 {
		t.Fatalf("wrote %d rows, want two attempts and two reviews", len(rows))
	}
	first, second := run.Attempts[0], run.Attempts[1]
	secondRow := rowByID(t, rows, second.Work.ID)
	if secondRow.Attempt != 2 {
		t.Fatalf("second attempt row attempt = %d, want 2", secondRow.Attempt)
	}
	if secondRow.RetryOf != first.Work.ID {
		t.Fatalf("second attempt redoes %q, want %q", secondRow.RetryOf, first.Work.ID)
	}
	if firstRow := rowByID(t, rows, first.Work.ID); firstRow.RetryOf != "" {
		t.Fatalf("first attempt has a retry link %q; it redoes nothing", firstRow.RetryOf)
	}
	// The chain is walkable from either end.
	redone, err := store.List(t.Context(), trace.Filter{RetryOf: first.Work.ID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(redone) != 1 || redone[0].ID != second.Work.ID {
		t.Fatalf("rows redoing the first attempt = %+v, want the second", redone)
	}
}

// The relaunch is handed its own rejected answer and the sentence that
// rejected it. An agent told only "try again" reruns the same mistake.
func TestTheRelaunchCarriesTheRejection(t *testing.T) {
	dir := t.TempDir()
	handed := t.TempDir()
	work := declared(stub(t, "n=$(ls "+handed+"/card-* 2>/dev/null | wc -l)\n"+
		"cat >"+handed+"/card-$((n + 1)).json\ncat <<'REPORT'\n"+
		`{"result":{"path":"a.txt"},"verdict":"ok"}`+"\nREPORT"))
	r, _ := reviewed(t, work, reviewerType(verdicts(t, dir, refuses, passes)))

	run, err := r.RunReviewed(t.Context(), "reader", "critic", task())
	if err != nil {
		t.Fatalf("RunReviewed: %v", err)
	}

	cards := readCards(t, handed)
	if len(cards) != 2 {
		t.Fatalf("the work was handed %d cards, want 2", len(cards))
	}
	if cards[0].Subject != nil {
		t.Fatalf("the first attempt was handed a subject: %+v", cards[0].Subject)
	}
	subject := cards[1].Subject
	if subject == nil {
		t.Fatal("the relaunch was handed no subject; it cannot know what it failed")
	}
	if subject.RunID != run.Attempts[0].Work.ID {
		t.Fatalf("subject run id = %q, want the first attempt %q",
			subject.RunID, run.Attempts[0].Work.ID)
	}
	if subject.Attempt != 1 {
		t.Fatalf("subject attempt = %d, want the attempt being redone", subject.Attempt)
	}
	if subject.ReviewID != run.Attempts[0].Review.ID {
		t.Fatalf("subject review id = %q, want %q", subject.ReviewID, run.Attempts[0].Review.ID)
	}
	if subject.Rejection == nil || !strings.Contains(subject.Rejection.Text, "999") {
		t.Fatalf("rejection = %+v, want the reviewer's sentence", subject.Rejection)
	}
	if subject.Result["path"] != "a.txt" {
		t.Fatalf("subject result = %v, want the answer that was refused", subject.Result)
	}
}

// The reviewer is handed the answer, the criterion and the files. It cannot
// judge what it was not shown.
func TestTheReviewerIsHandedTheAnswerAndTheCriterion(t *testing.T) {
	dir := t.TempDir()
	work := declared(answers(t, `{"result":{"path":"a.txt"},"verdict":"ok"}`))
	r, _ := reviewed(t, work, reviewerType(verdicts(t, dir, passes)))

	want := task()
	want.Files = []string{"a.txt"}
	if _, err := r.RunReviewed(t.Context(), "reader", "critic", want); err != nil {
		t.Fatalf("RunReviewed: %v", err)
	}

	cards := readCards(t, dir)
	if len(cards) != 1 {
		t.Fatalf("the reviewer was handed %d cards, want 1", len(cards))
	}
	card := cards[0]
	if card.Subject == nil {
		t.Fatal("the reviewer was handed no subject")
	}
	if card.Subject.Result["path"] != "a.txt" {
		t.Fatalf("subject result = %v, want the work's answer", card.Subject.Result)
	}
	if card.Subject.Task.Criterion != want.Criterion {
		t.Fatalf("subject criterion = %q, want the work's own %q",
			card.Subject.Task.Criterion, want.Criterion)
	}
	if card.Task.Criterion != want.Criterion {
		t.Fatalf("review criterion = %q, want the work's own", card.Task.Criterion)
	}
	if len(card.Task.Files) != 1 || card.Task.Files[0] != "a.txt" {
		t.Fatalf("review files = %v, want what the work touched", card.Task.Files)
	}
	if card.Subject.ReviewID != "" || card.Subject.Rejection != nil {
		t.Fatalf("the reviewer was handed a rejection: %+v", card.Subject)
	}
}

// Two refusals end it. No third attempt, and the error carries the reviewer's
// own reason kind so an exit code says what the trace says.
func TestASecondRefusalStopsThere(t *testing.T) {
	dir := t.TempDir()
	work := declared(answers(t, `{"result":{"path":"a.txt"},"verdict":"ok"}`))
	r, store := reviewed(t, work, reviewerType(verdicts(t, dir, refuses, refuses)))

	run, err := r.RunReviewed(t.Context(), "reader", "critic", task())
	if err == nil {
		t.Fatal("RunReviewed: want an error when nothing was accepted")
	}
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want the reviewer's own bin", contract.KindOf(err))
	}
	if len(run.Attempts) != agent.MaxAttempts {
		t.Fatalf("attempts = %d, want the cap of %d", len(run.Attempts), agent.MaxAttempts)
	}
	if run.Accepted() {
		t.Fatal("run: accepted after two refusals")
	}
	rows, _ := store.List(t.Context(), trace.Filter{TypeName: "reader"})
	if len(rows) != 2 {
		t.Fatalf("the work ran %d times, want exactly %d", len(rows), agent.MaxAttempts)
	}
	// The last attempt's report is still what a caller reads, refused or not:
	// hiding it would leave them with an error and no answer to look at.
	if run.Final().Result["path"] != "a.txt" {
		t.Fatalf("final = %v, want the last answer", run.Final().Result)
	}
}

// A death is not reviewed. There is nothing to judge, and re-running a
// crashed process is a retry policy, not an audit.
func TestADeathIsNotReviewed(t *testing.T) {
	dir := t.TempDir()
	work := declared(stub(t, "cat >/dev/null\nexit 4"))
	r, store := reviewed(t, work, reviewerType(verdicts(t, dir, passes)))

	run, err := r.RunReviewed(t.Context(), "reader", "critic", task())
	if err == nil {
		t.Fatal("RunReviewed: want the death to reach the caller")
	}
	if len(run.Attempts) != 1 || run.Attempts[0].Reviewed {
		t.Fatalf("attempts = %+v, want one unreviewed attempt", run.Attempts)
	}
	rows, _ := store.List(t.Context(), trace.Filter{TypeName: "critic"})
	if len(rows) != 0 {
		t.Fatalf("the reviewer ran %d times over a run that never answered", len(rows))
	}
}

// A reviewer that dies leaves the answer unjudged, and that is what the caller
// is told. An unreviewed answer passed off as accepted is the exact failure
// this loop exists to prevent.
func TestAReviewerThatDiesDoesNotAccept(t *testing.T) {
	work := declared(answers(t, `{"result":{"path":"a.txt"},"verdict":"ok"}`))
	r, _ := reviewed(t, work, reviewerType(stub(t, "cat >/dev/null\nexit 7")))

	run, err := r.RunReviewed(t.Context(), "reader", "critic", task())
	if err == nil {
		t.Fatal("RunReviewed: want the reviewer's death to reach the caller")
	}
	if run.Accepted() {
		t.Fatal("run: accepted by a reviewer that never answered")
	}
	if len(run.Attempts) != 1 {
		t.Fatalf("attempts = %d; a dead reviewer must not trigger a relaunch", len(run.Attempts))
	}
}

// An undeclared reviewer is refused before the work runs. Running first and
// discovering there is nobody to check it spends real time to produce an
// unreviewed answer.
func TestAnUnknownReviewerIsRefusedBeforeTheWorkRuns(t *testing.T) {
	work := declared(answers(t, `{"result":{"path":"a.txt"},"verdict":"ok"}`))
	r, store := reviewed(t, work, reviewerType("/bin/true"))

	if _, err := r.RunReviewed(t.Context(), "reader", "nobody", task()); err == nil {
		t.Fatal("RunReviewed: want a refusal for an undeclared reviewer")
	}
	rows, _ := store.List(t.Context(), trace.Filter{})
	if len(rows) != 0 {
		t.Fatalf("wrote %d rows before refusing", len(rows))
	}
}

// cardWire is the half of the assignment a test needs to read back.
type cardWire struct {
	Task struct {
		Objective string   `json:"objective"`
		Files     []string `json:"files"`
		Criterion string   `json:"criterion"`
	} `json:"task"`
	Subject *struct {
		RunID   string         `json:"run_id"`
		Attempt int            `json:"attempt"`
		Result  map[string]any `json:"result"`
		Verdict string         `json:"verdict"`
		Task    struct {
			Criterion string `json:"criterion"`
		} `json:"task"`
		ReviewID  string `json:"review_id"`
		Rejection *struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"rejection"`
	} `json:"subject"`
}

// readCards reads every assignment a stub captured, in the order they were
// written.
func readCards(t *testing.T, dir string) []cardWire {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("looking for captured cards: %v", err)
	}
	// Every capturing stub numbers its files in order, so the name is the
	// order. Sorting by mtime instead would be a coin flip between two
	// spawns that landed inside the same filesystem timestamp.
	sort.Strings(entries)

	out := make([]cardWire, 0, len(entries))
	for _, entry := range entries {
		raw, err := os.ReadFile(entry)
		if err != nil {
			t.Fatalf("reading %s: %v", entry, err)
		}
		var card cardWire
		if err := json.Unmarshal(raw, &card); err != nil {
			t.Fatalf("%s is not an assignment: %v", entry, err)
		}
		out = append(out, card)
	}
	return out
}

func rowByID(t *testing.T, rows []trace.Row, id string) trace.Row {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no trace row for %s", id)
	return trace.Row{}
}
