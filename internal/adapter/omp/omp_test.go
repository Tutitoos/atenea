package omp

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// The adapter's whole job is translation, so these tests are about the two
// directions of it: what omp is told, and what its answer becomes. The real
// binary is exercised end to end from cmd; here the shapes are pinned down.

func newSearch(t *testing.T, payload map[string]any) search {
	t.Helper()
	s, err := readSearch(payload, DefaultMatchLimit)
	if err != nil {
		t.Fatalf("readSearch(%v): %v", payload, err)
	}
	return s
}

// The three option flags are declared as intent and omp's search has no flag
// for any of them, so the only place they can land is the pattern itself.
func TestIntentIsFoldedIntoThePattern(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name:    "plain text is quoted so punctuation is not a wildcard",
			payload: map[string]any{"query": "a.b(c)"},
			want:    `(?i)a\.b\(c\)`,
		},
		{
			name:    "regex intent hands the query over untouched",
			payload: map[string]any{"query": "a.b", "regex": true},
			want:    `(?i)a.b`,
		},
		{
			name:    "match_case drops the insensitivity omp would not otherwise know to skip",
			payload: map[string]any{"query": "Atenea", "match_case": true},
			want:    `Atenea`,
		},
		{
			name:    "whole_word fences the query",
			payload: map[string]any{"query": "run", "whole_word": true, "match_case": true},
			want:    `\b(?:run)\b`,
		},
		{
			name:    "the fence goes around the whole alternation, not the last branch",
			payload: map[string]any{"query": "run|walk", "regex": true, "whole_word": true, "match_case": true},
			want:    `\b(?:run|walk)\b`,
		},
		{
			name:    "every intent at once composes in one order",
			payload: map[string]any{"query": "a.b", "whole_word": true},
			want:    `(?i)\b(?:a\.b)\b`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newSearch(t, tc.payload).pattern; got != tc.want {
				t.Errorf("pattern = %q, want %q", got, tc.want)
			}
		})
	}
}

// A pattern omp cannot parse comes back as "0 matches" and a zero exit, which
// reads exactly like an honest empty answer. The adapter is the only thing
// standing between a typo and a false fact.
func TestABrokenQueryIsRefusedRatherThanAnsweredEmpty(t *testing.T) {
	_, err := readSearch(map[string]any{"query": "(unclosed", "regex": true}, DefaultMatchLimit)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err = %v)", got, err)
	}
	// The same text with no regex intent is a literal and must be fine.
	if _, err := readSearch(map[string]any{"query": "(unclosed"}, DefaultMatchLimit); err != nil {
		t.Fatalf("literal query was refused: %v", err)
	}
}

func TestAnEmptyQueryIsRefused(t *testing.T) {
	for _, payload := range []map[string]any{
		{},
		{"query": ""},
		{"query": "   "},
		{"query": 7},
	} {
		if _, err := readSearch(payload, DefaultMatchLimit); contract.KindOf(err) != contract.FailureInvalidInput {
			t.Errorf("payload %v: kind = %v, want invalid_input", payload, contract.KindOf(err))
		}
	}
}

func TestNegativeContextLinesIsRefused(t *testing.T) {
	_, err := readSearch(map[string]any{"query": "x", "context_lines": -1}, DefaultMatchLimit)
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}

// A JSON decoder hands whole numbers over as float64, and the payload arrives
// from an adapter that may well have spoken JSON on the way in.
func TestContextLinesAcceptsTheShapesAPayloadArrivesIn(t *testing.T) {
	for name, value := range map[string]any{"int": 5, "int64": int64(5), "float64": float64(5)} {
		if got := newSearch(t, map[string]any{"query": "x", "context_lines": value}).contextLines; got != 5 {
			t.Errorf("%s: context_lines = %d, want 5", name, got)
		}
	}
	if got := newSearch(t, map[string]any{"query": "x"}).contextLines; got != defaultContextLines {
		t.Errorf("absent context_lines = %d, want the declared default %d", got, defaultContextLines)
	}
}

// Repeating -g narrows to nothing instead of widening, so several file types
// have to become one brace.
func TestFileTypesBecomeASingleGlob(t *testing.T) {
	cases := []struct {
		name  string
		types []any
		want  string
	}{
		{"none asks for no filter at all", nil, ""},
		{"one is a plain glob", []any{"go"}, "*.go"},
		{"a leading dot is the same intent", []any{".go"}, "*.go"},
		{"several become one brace, never several flags", []any{"go", "toml"}, "*.{go,toml}"},
		{"case and padding are the caller's, not omp's", []any{" GO ", "TOML"}, "*.{go,toml}"},
		{"an empty entry is not a file type", []any{"go", "", "  "}, "*.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"query": "x"}
			if tc.types != nil {
				payload["file_types"] = tc.types
			}
			if got := newSearch(t, payload).glob; got != tc.want {
				t.Errorf("glob = %q, want %q", got, tc.want)
			}
		})
	}
}

// The command line is the whole of what omp is told, so its shape is a
// contract of its own.
func TestTheCommandLineSaysExactlyWhatWasAskedFor(t *testing.T) {
	s := newSearch(t, map[string]any{
		"query": "needle", "context_lines": 3, "file_types": []any{"go"}, "match_case": true,
	})
	got := s.args("/repo/internal")
	want := []string{"grep", "-C", "3", "-l", strconv.Itoa(DefaultMatchLimit), "-g", "*.go", "--", "needle", "/repo/internal"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("args =\n  %v\nwant\n  %v", got, want)
	}
}

// A query starting with a dash is a query, not a flag. omp only reads it as
// one if it arrives behind the separator.
func TestAQueryThatLooksLikeAFlagStaysBehindTheSeparator(t *testing.T) {
	s := newSearch(t, map[string]any{"query": "-race", "match_case": true})
	args := s.args("/repo")
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		t.Fatalf("no -- in %v", args)
	}
	if args[separator+1] != "-race" {
		t.Fatalf("query is at %v, want the bare -race straight after --", args[separator+1:])
	}
}

// Asking omp for nothing is not asking omp for everything: -l 0 quietly falls
// back to a small default and then calls the short answer complete.
func TestTheMatchCeilingIsAlwaysStatedOutLoud(t *testing.T) {
	runner, err := New(Options{Implementations: []string{"ripgrep"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if runner.matchLimit != DefaultMatchLimit {
		t.Fatalf("match limit = %d, want the default %d", runner.matchLimit, DefaultMatchLimit)
	}
	s := newSearch(t, map[string]any{"query": "x"})
	args := s.args("/repo")
	for i, arg := range args {
		if arg == "-l" && args[i+1] == "0" {
			t.Fatal("-l 0 was handed to omp, which is not the unlimited it looks like")
		}
	}
}

// Reading the answer back.

const sample = `Searching in: /repo
Pattern: (?i)needle
Mode: content, Limit: 10000, Context: 2, Gitignore: true

Total matches: 2
Files with matches: 2
Files searched: 27

internal/core/core.go-10- above
internal/core/core.go:11: the needle here
internal/core/core.go-12- below

pkg/contract/runner.go:5: another needle
`

func TestTheAnswerIsReadBackAsLinesAndContext(t *testing.T) {
	got, err := parse(sample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.sawSummary || got.matches != 2 {
		t.Fatalf("summary: matches = %d, saw = %v", got.matches, got.sawSummary)
	}
	if got.truncated {
		t.Error("nothing said the answer was cut short")
	}
	if len(got.hits) != 4 {
		t.Fatalf("hits = %d, want 4 printed lines", len(got.hits))
	}
	if len(got.hits.matched()) != 2 {
		t.Fatalf("matched = %d, want 2", len(got.hits.matched()))
	}
	first := got.hits[1]
	if first.path != "internal/core/core.go" || first.line != 11 || !first.match {
		t.Errorf("first match = %+v", first)
	}
	if first.text != "the needle here" {
		t.Errorf("text = %q", first.text)
	}
	// The header must not be mistaken for content.
	for _, h := range got.hits {
		if strings.HasPrefix(h.path, "Searching") || strings.HasPrefix(h.path, "Mode") {
			t.Errorf("a header line was read as a hit: %+v", h)
		}
	}
}

func TestTheSnippetIsRebuiltFromTheContextOmpAlreadyPrinted(t *testing.T) {
	got, err := parse(sample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snippet := got.hits.snippet(1, 2); snippet != "above\nthe needle here\nbelow" {
		t.Errorf("snippet = %q", snippet)
	}
	// A match with no context printed around it is its own snippet, and the
	// neighbor in another file must not leak into it.
	if snippet := got.hits.snippet(3, 2); snippet != "another needle" {
		t.Errorf("snippet = %q, want just the match line", snippet)
	}
}

// omp merges two nearby matches into one run of lines. The window belongs to
// the match, not to the block it was printed in.
func TestTwoNearbyMatchesGetTheirOwnWindows(t *testing.T) {
	merged := `Total matches: 2
Files searched: 1

a.go-1- one
a.go:2: two
a.go-3- three
a.go:4: four
a.go-5- five
`
	got, err := parse(merged)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snippet := got.hits.snippet(1, 1); snippet != "one\ntwo\nthree" {
		t.Errorf("first window = %q", snippet)
	}
	if snippet := got.hits.snippet(3, 1); snippet != "three\nfour\nfive" {
		t.Errorf("second window = %q", snippet)
	}
}

// A path with a dash or a colon in it still has to come apart at the right
// place: the separators are the ones omp put there, not the ones in the name.
func TestAwkwardPathsStillComeApartCorrectly(t *testing.T) {
	cases := []struct {
		line  string
		path  string
		num   int
		text  string
		match bool
	}{
		{"a/b.go:11: text", "a/b.go", 11, "text", true},
		{"a/b.go-11- text", "a/b.go", 11, "text", false},
		{"my-1-file.go:5: hit", "my-1-file.go", 5, "hit", true},
		{"odd:name:7: hit", "odd:name", 7, "hit", true},
		{"a.go-40- ", "a.go", 40, "", false},
		{"a.go:40: ", "a.go", 40, "", true},
		{"deep/x.go:3: code:42: in the text", "deep/x.go", 3, "code:42: in the text", true},
	}
	for _, tc := range cases {
		got, ok := readLine(tc.line)
		if !ok {
			t.Errorf("%q was not read at all", tc.line)
			continue
		}
		if got.path != tc.path || got.line != tc.num || got.text != tc.text || got.match != tc.match {
			t.Errorf("%q -> %+v, want path=%q line=%d text=%q match=%v",
				tc.line, got, tc.path, tc.num, tc.text, tc.match)
		}
	}
	for _, line := range []string{
		"", "Searching in: /repo", "Total matches: 2",
		"Mode: content, Limit: 20, Context: 2, Gitignore: true",
		"a.go:0: zero is not a line number",
		"a.go:11-mismatched separators",
		":11: no path at all",
	} {
		if got, ok := readLine(line); ok {
			t.Errorf("%q was read as a hit: %+v", line, got)
		}
	}
}

// The summary is a free checksum while the answer is whole. An output shape
// this adapter can no longer read has to fall back, because the alternative is
// reporting a search that read almost nothing as a search that found nothing.
func TestAnUnreadableFormatFallsBackInsteadOfAnsweringEmpty(t *testing.T) {
	_, err := parse("Total matches: 9\nFiles searched: 1\n\na.go:1: needle\n")
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable (err = %v)", got, err)
	}

	// Under truncation the two numbers legitimately disagree, so the same
	// guard must stay quiet or every capped search would look broken.
	capped, err := parse("Total matches: 7\nLimit reached: true\n\na.go:1: needle\n")
	if err != nil {
		t.Fatalf("a truncated answer was rejected: %v", err)
	}
	if !capped.truncated || len(capped.hits.matched()) != 1 {
		t.Fatalf("truncated report = %+v", capped)
	}

	// A count that is not a number is the same kind of problem.
	if _, err := parse("Total matches: lots\n\na.go:1: x\n"); contract.KindOf(err) != contract.FailureUnavailable {
		t.Errorf("an unreadable count -> %v, want unavailable", contract.KindOf(err))
	}
}

func TestOmpsOwnErrorsAreSortedIntoTheSharedBins(t *testing.T) {
	cases := []struct {
		stderr string
		want   contract.FailureKind
	}{
		{"Error: Path not found: No such file or directory (os error 2)", contract.FailureNotFound},
		{"Error: permission denied (os error 13)", contract.FailurePermissionDenied},
		{"Error: the search timed out", contract.FailureTimeout},
		{"Error: something nobody has seen before", contract.FailureUnavailable},
		{"", contract.FailureUnavailable},
	}
	for _, tc := range cases {
		err := failureFor(tc.stderr)
		if got := contract.KindOf(err); got != tc.want {
			t.Errorf("%q -> %v, want %v", tc.stderr, got, tc.want)
		}
		// The untranslated text has to survive for whoever debugs later.
		var failure *contract.Failure
		if !errors.As(err, &failure) {
			t.Fatalf("%q did not come back as a Failure", tc.stderr)
		}
		if tc.stderr != "" && failure.Raw == "" {
			t.Errorf("%q lost its raw text", tc.stderr)
		}
		if strings.Contains(failure.Message, "Error:") {
			t.Errorf("the prefix leaked into the message: %q", failure.Message)
		}
	}
}

// A stray error line on stdout is still a failure, never an empty answer.
func TestAnErrorOnStdoutIsNotAnEmptyAnswer(t *testing.T) {
	_, err := parse("Searching in: /nope\n\nError: Path not found\n")
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", got)
	}
}

func TestTruncationIsNoticed(t *testing.T) {
	got, err := parse("Total matches: 20\nLimit reached: true\n\na.go:1: x\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.truncated {
		t.Fatal("Limit reached: true was not noticed")
	}
	whole, err := parse("Total matches: 1\n\na.go:1: x\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if whole.truncated {
		t.Fatal("a whole answer was reported as cut short")
	}
}

func TestTheColumnIsRecoveredFromTheLineOmpReturned(t *testing.T) {
	s := newSearch(t, map[string]any{"query": "needle"})
	if got := s.column("a needle here"); got != 3 {
		t.Errorf("column = %d, want 3", got)
	}
	if got := s.column("NEEDLE at the front"); got != 1 {
		t.Errorf("case-insensitive column = %d, want 1", got)
	}
	// A line the expression cannot find again still has a right line number,
	// so the start of it is the honest answer rather than a failed search.
	if got := s.column("no match in here at all"); got != 1 {
		t.Errorf("unrecoverable column = %d, want 1", got)
	}
}

// Reading outside the unit of work is outside the commission, whatever the
// path says.
func TestAScopeThatLeavesTheRepositoryIsRefused(t *testing.T) {
	for _, scope := range []string{"..", "../elsewhere", "internal/../.."} {
		_, err := targets("/repo", []string{scope}, "current")
		if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
			t.Errorf("scope %q -> %v, want permission_denied", scope, got)
		}
	}
	roots, err := targets("/repo", []string{"internal", "pkg/contract"}, "current")
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	want := []string{filepath.FromSlash("/repo/internal"), filepath.FromSlash("/repo/pkg/contract")}
	if strings.Join(roots, ",") != strings.Join(want, ",") {
		t.Errorf("roots = %v, want %v", roots, want)
	}
	// No scope means the whole repository, in one run rather than none.
	if roots, err := targets("/repo", nil, "current"); err != nil || len(roots) != 1 {
		t.Errorf("empty scope -> %v, %v", roots, err)
	}
}

func TestSensitiveMatchesNeverLeaveTheAdapter(t *testing.T) {
	runner := newTestRunner(t)
	cases := map[string]bool{
		".env":                    true,
		"config/local.env":        false,
		"deploy/server.pem":       true,
		"internal/core/core.go":   false,
		"credentials.json":        true,
		"nested/credentials.json": true,
	}
	for relative, want := range cases {
		if got := runner.isSensitive(relative, filepath.Base(relative)); got != want {
			t.Errorf("isSensitive(%q) = %v, want %v", relative, got, want)
		}
	}
}

// omp prints a path relative to whatever it was pointed at, and an absolute
// one when that was a single file. Either way the caller was told about a
// repository, so what comes back has to be openable from the repository root.
func TestAPrintedPathIsRebasedOntoTheRepository(t *testing.T) {
	root := filepath.FromSlash("/repo")
	cases := []struct {
		name   string
		target string
		got    string
		want   string
		ok     bool
	}{
		{"the whole repository", root, "src/login.go", "src/login.go", true},
		{"a scope below the root is rejoined, not reported short",
			filepath.Join(root, "src"), "login.go", "src/login.go", true},
		{"a nested scope keeps its whole path",
			filepath.Join(root, "a", "b"), "c/d.go", "a/b/c/d.go", true},
		{"a single file target prints absolute and still lands inside",
			filepath.Join(root, "src", "login.go"), filepath.Join(root, "src", "login.go"), "src/login.go", true},
		{"an absolute path outside the repository is not part of the answer",
			root, filepath.FromSlash("/elsewhere/x.go"), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := relativeTo(root, tc.target, tc.got)
			if ok != tc.ok || got != tc.want {
				t.Errorf("relativeTo(%q, %q, %q) = %q, %v; want %q, %v",
					root, tc.target, tc.got, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestABrokenSensitivePatternIsRefusedAtBuildTime(t *testing.T) {
	_, err := New(Options{Sensitive: []string{"["}})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

func TestTheAdapterAnnouncesWhoItIsAndWhatItServes(t *testing.T) {
	runner := newTestRunner(t)
	if runner.ID() != "omp" {
		t.Errorf("ID = %q, want omp", runner.ID())
	}
	if !runner.Serves("ripgrep") || runner.Serves("serena.search") {
		t.Errorf("Serves is wrong: %v", runner.Implementations())
	}
	if got := runner.Sensitive(); len(got) != 3 {
		t.Errorf("Sensitive = %v, want the three configured patterns", got)
	}
}

// A client that is not installed is a provider that is unreachable, which is
// the bin fallback reads -- not a crash and not an empty answer.
func TestAMissingBinaryIsUnavailableRatherThanFatal(t *testing.T) {
	runner, err := New(Options{
		Binary:          "atenea-omp-that-is-not-installed",
		Implementations: []string{"ripgrep"},
	})
	if err != nil {
		t.Fatalf("New refused to build over a missing binary: %v", err)
	}
	_, err = runner.Run(context.Background(), request(t, map[string]any{"query": "x"}))
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable (err = %v)", got, err)
	}
}

func TestARequestThisAdapterCannotAnswerIsSortedNotAttempted(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()

	cases := []struct {
		name string
		bend func(*contract.RunRequest)
		want contract.FailureKind
	}{
		{
			name: "an implementation it does not serve",
			bend: func(r *contract.RunRequest) { r.Implementation.ID = "serena.search" },
			want: contract.FailureUnavailable,
		},
		{
			name: "an effect the commission does not cover",
			bend: func(r *contract.RunRequest) { r.Permission.Effects = nil },
			want: contract.FailurePermissionDenied,
		},
		{
			name: "a payload missing its required field",
			bend: func(r *contract.RunRequest) { r.Payload = map[string]any{} },
			want: contract.FailureInvalidInput,
		},
		{
			name: "a capability this adapter has never heard of",
			bend: func(r *contract.RunRequest) {
				r.Capability.ID = "symbol.definition"
				r.Implementation.Capability = "symbol.definition"
			},
			want: contract.FailureNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := request(t, map[string]any{"query": "x"})
			tc.bend(&req)
			outcome, err := runner.Run(ctx, req)
			if err == nil {
				t.Fatalf("wanted a refusal, got %+v", outcome)
			}
			if got := contract.KindOf(err); got != tc.want {
				t.Errorf("kind = %v, want %v (err = %v)", got, tc.want, err)
			}
		})
	}
}

// Helpers.

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	runner, err := New(Options{
		Implementations: []string{"ripgrep"},
		Sensitive:       []string{".env", "*.pem", "credentials.json"},
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner
}

// codeSearch mirrors the shipped capability, because the adapter has to
// produce something that passes the declared output schema, not something that
// merely looks right.
func codeSearch() contract.Capability {
	return contract.Capability{
		ID:      "code.search",
		Version: contract.Version{Major: 1},
		Summary: "Find literal text in a repository.",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "query", Type: contract.TypeString, Required: true},
			{Name: "scope", Type: contract.TypeStringList},
			{Name: "match_case", Type: contract.TypeBool},
			{Name: "regex", Type: contract.TypeBool},
			{Name: "whole_word", Type: contract.TypeBool},
			{Name: "file_types", Type: contract.TypeStringList},
			{Name: "context_lines", Type: contract.TypeInt},
		},
		Outputs: []contract.Field{{
			Name: "matches", Type: contract.TypeRecordList, Required: true,
			Fields: []contract.Field{
				{Name: "path", Type: contract.TypeString, Required: true},
				{Name: "line", Type: contract.TypeInt, Required: true},
				{Name: "column", Type: contract.TypeInt, Required: true},
				{Name: "snippet", Type: contract.TypeString},
			},
		}},
	}
}

func request(t *testing.T, payload map[string]any) contract.RunRequest {
	t.Helper()
	return requestAt(t, t.TempDir(), payload)
}

func requestAt(t *testing.T, root string, payload map[string]any) contract.RunRequest {
	t.Helper()
	return contract.RunRequest{
		Capability:     codeSearch(),
		Implementation: contract.Implementation{ID: "ripgrep", Provider: "ripgrep", Capability: "code.search"},
		Repository:     contract.NewRepository("current", root, []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil),
		Payload:        payload,
		Permission:     contract.Permission{Task: "find login", Effects: []contract.Effect{contract.EffectRead}},
	}
}

// ---------------------------------------------------------------------------
// Against the real client
// ---------------------------------------------------------------------------

// Everything above pins the translation against a written-down copy of omp's
// output. A copy of a format is exactly the thing that goes stale without
// anybody noticing, so this is the test that reads what omp prints today: it
// is the one that fails the day the format moves, and the reason the rest can
// stay fast and hermetic.
func TestTheRealClientAnswersTheShapeTheAdapterExpects(t *testing.T) {
	if _, err := osexec.LookPath(DefaultBinary); err != nil {
		t.Skipf("omp is not installed on this machine: %v", err)
	}
	root := t.TempDir()
	for name, body := range map[string]string{
		// The match sits at column 4, so a column invented as 1 would fail
		// here, and it has two lines above and below for the snippet.
		"src/login.go":  "package auth\n\n// TODO: rotate the key\n\nfunc Login() {}\n",
		"docs/notes.md": "TODO: write these\n",
		".env":          "TODO_SECRET=hunter2\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	outcome, err := newTestRunner(t).Run(context.Background(), requestAt(t, root, map[string]any{"query": "TODO"}))
	if err != nil {
		t.Fatalf("Run against the real client: %v", err)
	}
	if outcome.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v", outcome.Verdict)
	}
	if outcome.Spent.Tokens != 0 {
		t.Errorf("tokens = %d: a tool call does not spend any, and a made-up "+
			"figure would poison the baseline", outcome.Spent.Tokens)
	}

	byPath := map[string]map[string]any{}
	for _, raw := range outcome.Result["matches"].([]any) {
		record := raw.(map[string]any)
		byPath[record["path"].(string)] = record
	}
	if len(byPath) != 2 {
		t.Fatalf("matched %d files, want the two that are not secret: %v", len(byPath), byPath)
	}
	if _, leaked := byPath[".env"]; leaked {
		t.Error("a sensitive file reached the answer")
	}

	hit, ok := byPath["src/login.go"]
	if !ok {
		t.Fatalf("the go file is missing: %v", byPath)
	}
	if hit["line"] != 3 {
		t.Errorf("line = %v, want 3", hit["line"])
	}
	// omp prints no column at all. This one was recovered from the line it did
	// print, which is the whole reason the adapter compiles the pattern too.
	if hit["column"] != 4 {
		t.Errorf("column = %v, want 4", hit["column"])
	}
	want := "package auth\n\n// TODO: rotate the key\n\nfunc Login() {}"
	if hit["snippet"] != want {
		t.Errorf("snippet =\n%q\nwant\n%q", hit["snippet"], want)
	}
}

// A scope below the root is a different command line and a different set of
// paths coming back, so it is worth one real run of its own.
func TestTheRealClientHonoursAScopeBelowTheRoot(t *testing.T) {
	if _, err := osexec.LookPath(DefaultBinary); err != nil {
		t.Skipf("omp is not installed on this machine: %v", err)
	}
	root := t.TempDir()
	for name, body := range map[string]string{
		"src/login.go":  "// TODO here\n",
		"docs/notes.md": "// TODO there\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	outcome, err := newTestRunner(t).Run(context.Background(),
		requestAt(t, root, map[string]any{"query": "TODO", "scope": []any{"src"}}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	matches := outcome.Result["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("matched %d, want only the one inside the scope: %v", len(matches), matches)
	}
	// The path has to come back relative to the repository, not to the scope
	// omp was pointed at, or the caller cannot open what it was told about.
	if got := matches[0].(map[string]any)["path"]; got != "src/login.go" {
		t.Errorf("path = %v, want it relative to the repository root", got)
	}
}

// A search omp cannot reach is not an empty answer.
func TestTheRealClientReportsAMissingScopeRatherThanNothing(t *testing.T) {
	if _, err := osexec.LookPath(DefaultBinary); err != nil {
		t.Skipf("omp is not installed on this machine: %v", err)
	}
	root := t.TempDir()
	_, err := newTestRunner(t).Run(context.Background(),
		requestAt(t, root, map[string]any{"query": "TODO", "scope": []any{"nowhere"}}))
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found (err = %v)", got, err)
	}
}
