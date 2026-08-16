// Citation verification: the second of two checks this package can run.
//
// A citation is a place in prose where an answer names a specific file and
// line -- the one part of a prose answer ("summary", "findings") that is a
// falsifiable claim rather than a judgement call. Nothing here understands
// the narrative around a citation. It only checks that the citation itself
// points somewhere real and, where the prose also quotes a line of code
// beside it, that the quote is what that line actually holds.
//
// Exactly two forms are recognized, both fixed grammars, never inferred
// from context:
//
//	Form A: PATH:LINE           admin.routes.ts:3432
//	Form B: Line LINE of PATH   Line 23 of `company.routes.ts`
//
// A bare ":123" with no path attached -- common shorthand once a file has
// already been named earlier in the same answer -- is deliberately not
// resolved. Attributing it to "whatever file was mentioned last" is an
// inference about prose structure, not a reading of the citation itself,
// and the rule this file exists to keep is that it never infers what it
// was not told directly. Prose that only uses that shorthand produces zero
// citations here, and the report says so plainly rather than guessing.
//
// "Roughly matches", precisely: a citation with a quoted excerpt beside it
// -- a backtick-quoted span on the SAME prose line as the citation, and
// distinct from the citation's own (possibly backtick-wrapped) path --
// passes when the excerpt, whitespace collapsed and trimmed, is a
// substring of the cited line, or the cited line is a substring of it, also
// whitespace collapsed and trimmed. No fuzzy matching, no case-folding, no
// search of nearby lines: a quote that is content-correct but cited one
// line off is a mismatch, not a match found by looking around for it. A
// citation with no adjacent quote is checked for existence only -- the file
// opens, the line number is in range -- and is never promoted to "content
// verified" in what a report says it checked.
package reviewer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// citation is one falsifiable location claim pulled out of prose.
type citation struct {
	Path  string // as written in the prose, not yet resolved to disk
	Line  int
	Quote string // "" when no adjacent quoted excerpt was found beside it
}

// citeOutcome is what checking one citation against disk established.
type citeOutcome int

const (
	citeMatched citeOutcome = iota
	citeMismatched
	citeUnresolved
)

var (
	// Form A: a path token ending in a recognized extension, immediately
	// followed by ":" and a line number with no whitespace between them.
	formA = regexp.MustCompile(`([\w./-]+\.[A-Za-z]{1,5}):(\d+)\b`)
	// Form B: the fixed idiom "Line N of path", the path optionally
	// wrapped in backticks, "Line"/"of" case-insensitive.
	formB  = regexp.MustCompile(`(?i)\bline\s+(\d+)\s+of\s+` + "`" + `?([\w./-]+\.[A-Za-z]{1,5})` + "`" + `?`)
	quoted = regexp.MustCompile("`([^`]+)`")
)

// citations pulls every Form A / Form B citation out of s, one entry per
// match, in the order they appear. Anything not shaped exactly like one of
// the two grammars is not a citation as far as this function is concerned
// -- see the package doc above for why bare ":123" shorthand is excluded on
// purpose. Matching is done line by line, so a citation and the quote
// checked against it are always read from the same line of prose.
func citations(s string) []citation {
	var out []citation
	for _, line := range strings.Split(s, "\n") {
		for _, m := range formA.FindAllStringSubmatchIndex(line, -1) {
			n, err := strconv.Atoi(line[m[4]:m[5]])
			if err != nil {
				continue
			}
			out = append(out, citation{
				Path:  line[m[2]:m[3]],
				Line:  n,
				Quote: adjacentQuote(line, m[0], m[1]),
			})
		}
		for _, m := range formB.FindAllStringSubmatchIndex(line, -1) {
			n, err := strconv.Atoi(line[m[2]:m[3]])
			if err != nil {
				continue
			}
			out = append(out, citation{
				Path:  line[m[4]:m[5]],
				Line:  n,
				Quote: adjacentQuote(line, m[0], m[1]),
			})
		}
	}
	return out
}

// adjacentQuote returns the first backtick-quoted span on line that does
// not overlap the citation's own match span [start, end) -- a code excerpt
// cited beside a location, not the location's own (possibly
// backtick-wrapped) path.
func adjacentQuote(line string, start, end int) string {
	for _, m := range quoted.FindAllStringSubmatchIndex(line, -1) {
		if m[0] >= start && m[1] <= end {
			continue // this quote is the citation's own path, not an excerpt beside it
		}
		return line[m[2]:m[3]]
	}
	return ""
}

// normalizeSpace collapses runs of whitespace to a single space and trims
// the ends -- the only transformation "roughly matches" applies before
// comparing two strings.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// roughlyMatches is the whole of what "roughly matches" means: substring
// containment, either direction, after whitespace normalization. See the
// package doc above for why nothing fuzzier is attempted.
func roughlyMatches(quote, actual string) bool {
	q, a := normalizeSpace(quote), normalizeSpace(actual)
	if q == "" || a == "" {
		return false
	}
	return strings.Contains(a, q) || strings.Contains(q, a)
}

// checkCitation resolves one citation against the repository on disk.
//
// It never returns citeMismatched for a file it could not open -- the same
// rule check() already keeps for a subject's own `path`: an unopenable file
// is this reviewer's shortfall, not proof the citation is wrong, because a
// permission this process does not have looks identical to a path that was
// never real.
func checkCitation(in assignment, c citation) (citeOutcome, string) {
	name := c.Path
	if root := repositoryRoot(in); root != "" && !filepath.IsAbs(name) {
		name = filepath.Join(root, name)
	}
	body, err := os.ReadFile(name)
	if err != nil {
		return citeUnresolved, fmt.Sprintf("%s:%d: cannot re-read %s: %v", c.Path, c.Line, name, err)
	}
	lines := strings.Split(string(body), "\n")
	if c.Line < 1 || c.Line > len(lines) {
		return citeMismatched, fmt.Sprintf("%s:%d: the file has %d lines", c.Path, c.Line, len(lines))
	}
	if c.Quote == "" {
		return citeMatched, ""
	}
	actual := lines[c.Line-1]
	if !roughlyMatches(c.Quote, actual) {
		return citeMismatched, fmt.Sprintf("%s:%d: cited as %q, the line reads %q",
			c.Path, c.Line, c.Quote, strings.TrimSpace(actual))
	}
	return citeMatched, ""
}

// citationScope is printed on every report this file returns, ok or not.
// It is the answer to "what did this actually check" and it travels in the
// same field on every verdict, because the boundary matters most on the
// verdict most likely to be read as "the answer is correct".
const citationScope = "audited: that each cited file:line exists, and, where a quoted " +
	"excerpt sits beside a citation, whether that excerpt is on the cited line. Not " +
	"audited: any claim in the prose beyond the cited locations, including whether the " +
	"narrative correctly characterizes what the surrounding code does."

// judgeCitations audits a prose subject -- one whose result carries no
// `path`, so check() has nothing to open -- by pulling every citation out
// of every string field in its result and resolving each against disk.
//
// The aggregate rule: any mismatch fails the whole report, because a
// provably wrong citation is the specific defect this check exists to
// catch. Short of that, any citation this reviewer could not resolve at
// all leaves the report incomplete -- it cannot honestly say the citations
// hold when some of them were never checked. Only when every citation
// found resolves and none mismatch does the report read ok, and even then
// it says explicitly how much of that "ok" was existence-only versus a
// checked quote, and what it never looked at.
func judgeCitations(in assignment, s *subject) report {
	var all []citation
	for _, key := range sortedResultKeys(s.Result) {
		text, ok := s.Result[key].(string)
		if !ok {
			continue
		}
		all = append(all, citations(text)...)
	}
	if len(all) == 0 {
		return incomplete("the answer names no file:line citation in either form this " +
			"reviewer reads (`path:line` or `Line N of path`); nothing here can verify prose alone")
	}

	var existOnly, contentChecked int
	var mismatches, unresolved []string
	for _, c := range all {
		switch outcome, detail := checkCitation(in, c); outcome {
		case citeMatched:
			if c.Quote != "" {
				contentChecked++
			} else {
				existOnly++
			}
		case citeMismatched:
			mismatches = append(mismatches, detail)
		case citeUnresolved:
			unresolved = append(unresolved, detail)
		}
	}

	if len(mismatches) > 0 {
		out := refuse(strings.Join(mismatches, "; "))
		delete(out.Result, "checked")
		out.Result["existence_only"] = existOnly
		out.Result["content_checked"] = contentChecked
		out.Result["unresolved"] = len(unresolved)
		out.Result["scope"] = citationScope
		return out
	}
	if len(unresolved) > 0 {
		out := incomplete(strings.Join(unresolved, "; "))
		delete(out.Result, "checked")
		out.Result["existence_only"] = existOnly
		out.Result["content_checked"] = contentChecked
		out.Result["scope"] = citationScope
		return out
	}
	return report{
		Result: map[string]any{
			"existence_only":  existOnly,
			"content_checked": contentChecked,
			"scope":           citationScope,
			"subject":         s.RunID,
		},
		Verdict: "ok",
		Reason: &reason{
			Kind: "not_found",
			Text: fmt.Sprintf(
				"citation-only audit: %d %s confirmed to exist on disk (%d of them also matched "+
					"a quoted excerpt); nothing in the narrative beyond the cited locations was checked",
				existOnly+contentChecked, plural(existOnly+contentChecked, "location", "locations"), contentChecked),
		},
	}
}

func sortedResultKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
