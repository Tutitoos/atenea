package reviewer_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/agent/reviewer"
)

type report struct {
	Result  map[string]any `json:"result"`
	Verdict string         `json:"verdict"`
	Reason  *struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"reason"`
}

// run feeds the reviewer one assignment and reads its report.
func run(t *testing.T, card map[string]any) report {
	t.Helper()
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("building the assignment: %v", err)
	}
	var out bytes.Buffer
	if err := reviewer.Main(bytes.NewReader(raw), &out); err != nil {
		t.Fatalf("Main: %v", err)
	}
	var got report
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("the reviewer wrote something that is not a report: %v (%s)", err, out.String())
	}
	return got
}

// card builds an assignment carrying a subject over a real file.
func card(t *testing.T, body string, result map[string]any, verdict string) map[string]any {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing the file under review: %v", err)
	}
	return map[string]any{
		"task": map[string]any{
			"objective": "audit attempt 1 of filereader",
			"files":     []string{"a.txt"},
			"criterion": "the numbers match the file",
		},
		"context": map[string]any{
			"repository": map[string]any{"id": "current", "root": dir},
		},
		"subject": map[string]any{
			"run_id":  "abc-1",
			"type":    "filereader",
			"attempt": 1,
			"task": map[string]any{
				"objective": "read a.txt and answer",
				"files":     []string{"a.txt"},
				"criterion": "the numbers match the file",
			},
			"result":  result,
			"verdict": verdict,
		},
	}
}

// A true answer passes, and the review says how many claims it actually
// checked -- a review that verified nothing must not read like one that
// verified three things.
func TestATrueAnswerPasses(t *testing.T) {
	got := run(t, card(t, "one\ntwo\n", map[string]any{
		"path": "a.txt", "bytes": 8, "lines": 2, "content": "one\ntwo\n",
	}, "ok"))
	if got.Verdict != "ok" {
		t.Fatalf("verdict = %s (%v), want ok", got.Verdict, got.Reason)
	}
	if got.Result["checked"] != float64(3) {
		t.Fatalf("checked = %v, want 3", got.Result["checked"])
	}
	if got.Result["subject"] != "abc-1" {
		t.Fatalf("subject = %v, want the run it audited", got.Result["subject"])
	}
}

// Wrong numbers are refused, and the refusal names both figures. "It is
// wrong" is not a sentence the agent being relaunched can act on.
func TestWrongNumbersAreRefused(t *testing.T) {
	got := run(t, card(t, "one\ntwo\n", map[string]any{
		"path": "a.txt", "bytes": 999, "lines": 99,
	}, "ok"))
	if got.Verdict != "failed" {
		t.Fatalf("verdict = %s, want failed", got.Verdict)
	}
	if got.Reason == nil {
		t.Fatal("a refusal with no reason is an opinion")
	}
	for _, want := range []string{"999", "8", "99", "2"} {
		if !strings.Contains(got.Reason.Text, want) {
			t.Errorf("reason %q does not name %s", got.Reason.Text, want)
		}
	}
}

// Content that does not match the file is refused even when the counts do.
func TestContentIsCompared(t *testing.T) {
	got := run(t, card(t, "one\ntwo\n", map[string]any{
		"path": "a.txt", "bytes": 8, "lines": 2, "content": "something else",
	}, "ok"))
	if got.Verdict != "failed" {
		t.Fatalf("verdict = %s, want failed", got.Verdict)
	}
	if !strings.Contains(got.Reason.Text, "content") {
		t.Fatalf("reason %q does not name the content", got.Reason.Text)
	}
}

// A run that already reported its own failure is agreed with, not
// re-litigated, and the reviewer keeps the two shapes apart: a failed run is
// refused (which earns a relaunch), an incomplete one is incomplete.
func TestASubjectThatFailedIsAgreedWith(t *testing.T) {
	base := card(t, "one\n", map[string]any{"path": "a.txt"}, "failed")
	base["subject"].(map[string]any)["reason"] = map[string]any{
		"kind": "not_found", "text": "no such file: a.txt",
	}
	got := run(t, base)
	if got.Verdict != "failed" {
		t.Fatalf("verdict = %s, want failed", got.Verdict)
	}
	if !strings.Contains(got.Reason.Text, "no such file") {
		t.Fatalf("reason %q does not carry what the run said", got.Reason.Text)
	}
}

func TestAnIncompleteSubjectStaysIncomplete(t *testing.T) {
	base := card(t, "one\n", map[string]any{"path": "a.txt"}, "incomplete")
	base["subject"].(map[string]any)["reason"] = map[string]any{
		"kind": "invalid_input", "text": "counted the file but did not carry its body",
	}
	got := run(t, base)
	if got.Verdict != "incomplete" {
		t.Fatalf("verdict = %s, want incomplete", got.Verdict)
	}
}

// The reviewer's own shortfall is incomplete, never ok and never failed. An
// unreadable file does not prove the answer wrong, and approving it would be
// the reviewer signing off on something it never looked at.
func TestAFileTheReviewerCannotReadIsIncomplete(t *testing.T) {
	base := card(t, "one\n", map[string]any{"path": "missing.txt", "bytes": 4}, "ok")
	got := run(t, base)
	if got.Verdict != "incomplete" {
		t.Fatalf("verdict = %s, want incomplete", got.Verdict)
	}
	if !strings.Contains(got.Reason.Text, "missing.txt") {
		t.Fatalf("reason %q does not name what it could not read", got.Reason.Text)
	}
}

// An answer with nothing verifiable in it is incomplete too: there is no
// difference between "I checked and it is right" and "there was nothing to
// check" unless the reviewer makes one.
func TestNothingVerifiableIsIncomplete(t *testing.T) {
	got := run(t, card(t, "one\n", map[string]any{"path": "a.txt"}, "ok"))
	if got.Verdict != "incomplete" {
		t.Fatalf("verdict = %s (%v), want incomplete", got.Verdict, got.Reason)
	}
}

func TestNoSubjectIsIncomplete(t *testing.T) {
	got := run(t, map[string]any{
		"task": map[string]any{"objective": "audit", "criterion": "x"},
	})
	if got.Verdict != "incomplete" {
		t.Fatalf("verdict = %s, want incomplete", got.Verdict)
	}
	if !strings.Contains(got.Reason.Text, "no subject") {
		t.Fatalf("reason %q does not say what was missing", got.Reason.Text)
	}
}

// Refusing an answer is the reviewer working. It exits zero on every path it
// controls, the same as any other agent: the report is the channel.
func TestItExitsCleanWhateverTheVerdict(t *testing.T) {
	raw, err := json.Marshal(card(t, "one\n", map[string]any{
		"path": "a.txt", "bytes": 999,
	}, "ok"))
	if err != nil {
		t.Fatalf("building the assignment: %v", err)
	}
	var out bytes.Buffer
	if err := reviewer.Main(bytes.NewReader(raw), &out); err != nil {
		t.Fatalf("Main returned an error for a refusal: %v", err)
	}
}
