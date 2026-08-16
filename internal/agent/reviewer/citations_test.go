package reviewer_test

import (
	"strings"
	"testing"
)

// A citation with a quote beside it that matches the real line passes, and
// the report says how much of that pass was existence-only versus a
// checked quote -- an all-existence-only pass must not read like one where
// the content was verified too.
func TestACitationWithAMatchingQuotePasses(t *testing.T) {
	got := run(t, card(t, "one\ntwo\nthree\n", map[string]any{
		"findings": "The second line is confirmed at a.txt:2, which reads `two`.",
	}, "ok"))
	if got.Verdict != "ok" {
		t.Fatalf("verdict = %s (%v), want ok", got.Verdict, got.Reason)
	}
	if got.Result["content_checked"] != float64(1) {
		t.Fatalf("content_checked = %v, want 1", got.Result["content_checked"])
	}
	if got.Result["existence_only"] != float64(0) {
		t.Fatalf("existence_only = %v, want 0", got.Result["existence_only"])
	}
	if got.Reason == nil || !strings.Contains(got.Reason.Text, "1") {
		t.Fatalf("reason %v does not say what was checked", got.Reason)
	}
	if scope, _ := got.Result["scope"].(string); !strings.Contains(scope, "Not audited") {
		t.Fatalf("scope %q does not name what was not audited", scope)
	}
}

// A citation with no quote beside it is checked for existence only, and the
// report keeps that fact separate from a content-checked pass.
func TestACitationWithNoQuoteIsExistenceOnly(t *testing.T) {
	got := run(t, card(t, "one\ntwo\n", map[string]any{
		"summary": "The claim is grounded at a.txt:1.",
	}, "ok"))
	if got.Verdict != "ok" {
		t.Fatalf("verdict = %s (%v), want ok", got.Verdict, got.Reason)
	}
	if got.Result["existence_only"] != float64(1) {
		t.Fatalf("existence_only = %v, want 1", got.Result["existence_only"])
	}
	if got.Result["content_checked"] != float64(0) {
		t.Fatalf("content_checked = %v, want 0", got.Result["content_checked"])
	}
}

// A quoted excerpt that is not what the cited line actually holds is
// refused, naming both the citation and what the line really says.
func TestAMismatchedQuoteIsRefused(t *testing.T) {
	got := run(t, card(t, "one\ntwo\n", map[string]any{
		"findings": "At a.txt:2 it reads `three`, which is the crux of it.",
	}, "ok"))
	if got.Verdict != "failed" {
		t.Fatalf("verdict = %s, want failed", got.Verdict)
	}
	for _, want := range []string{"a.txt:2", "three", "two"} {
		if !strings.Contains(got.Reason.Text, want) {
			t.Errorf("reason %q does not name %s", got.Reason.Text, want)
		}
	}
}

// A citation to a line number the file does not have is refused, not
// silently dropped.
func TestALineOutOfRangeIsRefused(t *testing.T) {
	got := run(t, card(t, "one\ntwo\n", map[string]any{
		"findings": "See a.txt:99 for the detail.",
	}, "ok"))
	if got.Verdict != "failed" {
		t.Fatalf("verdict = %s, want failed", got.Verdict)
	}
	if !strings.Contains(got.Reason.Text, "a.txt:99") {
		t.Fatalf("reason %q does not name the citation", got.Reason.Text)
	}
}

// The second recognized grammar, "Line N of path", is read the same as
// "path:N", quote and all.
func TestLineOfPathIsRecognized(t *testing.T) {
	got := run(t, card(t, "one\ntwo\n", map[string]any{
		"findings": "Line 2 of `a.txt` reads `two`.",
	}, "ok"))
	if got.Verdict != "ok" {
		t.Fatalf("verdict = %s (%v), want ok", got.Verdict, got.Reason)
	}
	if got.Result["content_checked"] != float64(1) {
		t.Fatalf("content_checked = %v, want 1", got.Result["content_checked"])
	}
}

// A citation to a file this reviewer cannot open is incomplete, not a
// silent pass and not a refusal -- the same rule check() already keeps for
// a subject's own claimed path.
func TestAnUnresolvableCitationIsIncomplete(t *testing.T) {
	got := run(t, card(t, "one\n", map[string]any{
		"findings": "Declared at missing.txt:1.",
	}, "ok"))
	if got.Verdict != "incomplete" {
		t.Fatalf("verdict = %s, want incomplete", got.Verdict)
	}
	if !strings.Contains(got.Reason.Text, "missing.txt") {
		t.Fatalf("reason %q does not name what it could not read", got.Reason.Text)
	}
}

// Bare ":N" shorthand with no path attached is not resolved -- inferring
// which file it means from context is exactly the guess this checker
// refuses to make -- so prose that only uses it is incomplete, the same as
// prose with no citation at all.
func TestBareLineShorthandIsNotACitation(t *testing.T) {
	got := run(t, card(t, "one\n", map[string]any{
		"findings": "The behavior is unchanged, declared here: :23.",
	}, "ok"))
	if got.Verdict != "incomplete" {
		t.Fatalf("verdict = %s, want incomplete", got.Verdict)
	}
	if !strings.Contains(got.Reason.Text, "no file:line citation") {
		t.Fatalf("reason %q does not say nothing was found", got.Reason.Text)
	}
}

// Citations are pulled from every string field in the result, not just one
// hardcoded key, and their outcomes aggregate together.
func TestCitationsAggregateAcrossResultFields(t *testing.T) {
	got := run(t, card(t, "one\ntwo\nthree\n", map[string]any{
		"summary":  "Grounded at a.txt:1.",
		"findings": "Also see a.txt:3, which reads `three`.",
	}, "ok"))
	if got.Verdict != "ok" {
		t.Fatalf("verdict = %s (%v), want ok", got.Verdict, got.Reason)
	}
	if got.Result["existence_only"] != float64(1) {
		t.Fatalf("existence_only = %v, want 1", got.Result["existence_only"])
	}
	if got.Result["content_checked"] != float64(1) {
		t.Fatalf("content_checked = %v, want 1", got.Result["content_checked"])
	}
}

// One mismatch fails the whole report even alongside citations that held.
func TestOneMismatchFailsEvenWithOthersHolding(t *testing.T) {
	got := run(t, card(t, "one\ntwo\nthree\n", map[string]any{
		"summary":  "Grounded at a.txt:1, which reads `one`.",
		"findings": "Also see a.txt:2, which reads `nope`.",
	}, "ok"))
	if got.Verdict != "failed" {
		t.Fatalf("verdict = %s, want failed", got.Verdict)
	}
	if !strings.Contains(got.Reason.Text, "a.txt:2") {
		t.Fatalf("reason %q does not name the bad citation", got.Reason.Text)
	}
}
