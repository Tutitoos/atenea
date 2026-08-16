package reviewer_test

import (
	"os"
	"path/filepath"
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
	if got.Result["unresolved"] != float64(1) {
		t.Fatalf("unresolved = %v, want 1 -- a caller should not have to count semicolons in the reason text", got.Result["unresolved"])
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

// cardOverTree builds an assignment like card, but over a small directory
// tree instead of one file at the root -- for citations that name a file
// by base name only, resolved by repoIndex rather than a literal join.
func cardOverTree(t *testing.T, files map[string]string, result map[string]any) map[string]any {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("making %s: %v", filepath.Dir(rel), err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", rel, err)
		}
	}
	return map[string]any{
		"task": map[string]any{"objective": "audit", "files": nil, "criterion": "citations hold"},
		"context": map[string]any{
			"repository": map[string]any{"id": "current", "root": dir},
		},
		"subject": map[string]any{
			"run_id":  "abc-1",
			"type":    "reader",
			"attempt": 1,
			"task":    map[string]any{"objective": "read", "files": nil, "criterion": "n/a"},
			"result":  result,
			"verdict": "ok",
		},
	}
}

// A citation naming a file by base name only -- the shorthand a real
// answer uses once it has already qualified the full path earlier in the
// same prose -- resolves when exactly one file in the repository carries
// that name, an objective fact about the tree rather than a guess about
// which sentence the shorthand refers to.
func TestAUniqueBaseNameResolvesWithoutTheDirectory(t *testing.T) {
	got := run(t, cardOverTree(t, map[string]string{
		"src/modules/drivers/drivers.routes.ts": "one\ntwo\n",
	}, map[string]any{
		"findings": "See drivers.routes.ts:2, which reads `two`.",
	}))
	if got.Verdict != "ok" {
		t.Fatalf("verdict = %s (%v), want ok", got.Verdict, got.Reason)
	}
	if got.Result["content_checked"] != float64(1) {
		t.Fatalf("content_checked = %v, want 1", got.Result["content_checked"])
	}
}

// A base name that more than one file in the repository shares stays
// unresolved: picking one would be a guess about which the citation meant,
// not a reading of the filesystem.
func TestAnAmbiguousBaseNameStaysUnresolved(t *testing.T) {
	got := run(t, cardOverTree(t, map[string]string{
		"src/modules/admin/admin.routes.ts":  "one\n",
		"src/modules/sentry/admin.routes.ts": "one\n",
	}, map[string]any{
		"findings": "Declared at admin.routes.ts:1.",
	}))
	if got.Verdict != "incomplete" {
		t.Fatalf("verdict = %s (%v), want incomplete", got.Verdict, got.Reason)
	}
	if !strings.Contains(got.Reason.Text, "admin.routes.ts:1") {
		t.Fatalf("reason %q does not name the ambiguous citation", got.Reason.Text)
	}
}

// A citation whose exact path resolves is used as written, never diverted
// to a same-name file elsewhere in the tree.
func TestAnExactPathIsPreferredOverBaseNameFallback(t *testing.T) {
	got := run(t, cardOverTree(t, map[string]string{
		"a.txt":           "one\ntwo\n",
		"other/dir/a.txt": "three\nfour\n",
	}, map[string]any{
		"findings": "See a.txt:2, which reads `two`.",
	}))
	if got.Verdict != "ok" {
		t.Fatalf("verdict = %s (%v), want ok", got.Verdict, got.Reason)
	}
	if got.Result["content_checked"] != float64(1) {
		t.Fatalf("content_checked = %v, want 1 (the root a.txt, not the ambiguous fallback)", got.Result["content_checked"])
	}
}

// A citation written wrapped in backticks -- “ `path:line` “, an
// ordinary way to cite a location in markdown -- with no separate code
// excerpt on the line is existence-only, not a mismatch against its own
// text. Found live: a real reviewer run against a real answer refused a
// citation this way, comparing "path:line" against the source line and
// calling the mismatch a defect.
func TestABacktickWrappedCitationWithNoExcerptIsExistenceOnly(t *testing.T) {
	got := run(t, card(t, "one\ntwo\n", map[string]any{
		"findings": "The only cited instance is `a.txt:2`, which I did not reach.",
	}, "ok"))
	if got.Verdict != "ok" {
		t.Fatalf("verdict = %s (%v), want ok", got.Verdict, got.Reason)
	}
	if got.Result["existence_only"] != float64(1) {
		t.Fatalf("existence_only = %v, want 1", got.Result["existence_only"])
	}
	if got.Result["content_checked"] != float64(0) {
		t.Fatalf("content_checked = %v, want 0 -- the backticks wrap the citation, not a code excerpt", got.Result["content_checked"])
	}
}

// A bare filename mentioned in backticks beside an unrelated citation on
// the same line is not treated as the citation's excerpt -- it is a path
// reference in the prose, not a code excerpt. Found live: a real reviewer
// run refused `src/server.ts:243` because the line also named
// “ `src/modules/admin/admin.routes.ts` “ in passing, and compared that
// unrelated filename against the source at server.ts:243 as if it were a
// quoted excerpt.
func TestABareFilenameBesideAnUnrelatedCitationIsNotItsQuote(t *testing.T) {
	got := run(t, card(t, "one\ntwo\n", map[string]any{
		"findings": "Nothing else was read. Line 317 and `a.txt:2` are cited as given, " +
			"per `other.routes.ts`.",
	}, "ok"))
	if got.Verdict != "ok" {
		t.Fatalf("verdict = %s (%v), want ok", got.Verdict, got.Reason)
	}
	if got.Result["existence_only"] != float64(1) {
		t.Fatalf("existence_only = %v, want 1", got.Result["existence_only"])
	}
	if got.Result["content_checked"] != float64(0) {
		t.Fatalf("content_checked = %v, want 0 -- `other.routes.ts` is a filename mention, not this citation's excerpt", got.Result["content_checked"])
	}
}
