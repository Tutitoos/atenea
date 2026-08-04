package codebasememory

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// --- payload parsing -------------------------------------------------------

func TestReadImpactAskParsesAValidPayload(t *testing.T) {
	got, err := readImpactAsk(map[string]any{
		"baseline":        "origin/main",
		"scope":           []any{"internal"},
		"depth":           3,
		"include_snippet": true,
		"snippet_lines":   6,
	})
	if err != nil {
		t.Fatalf("readImpactAsk: %v", err)
	}
	want := impactAsk{baseline: "origin/main", scope: []string{"internal"}, depth: 3, snippet: true, lines: 6}
	if got.baseline != want.baseline || !slices.Equal(got.scope, want.scope) ||
		got.depth != want.depth || got.snippet != want.snippet || got.lines != want.lines {
		t.Errorf("readImpactAsk = %+v, want %+v", got, want)
	}
}

func TestReadImpactAskAppliesDefaults(t *testing.T) {
	got, err := readImpactAsk(map[string]any{"baseline": "HEAD~1"})
	if err != nil {
		t.Fatalf("readImpactAsk: %v", err)
	}
	if got.depth != defaultDepth {
		t.Errorf("depth = %d, want the default %d", got.depth, defaultDepth)
	}
	if got.lines != defaultSnippetLines {
		t.Errorf("lines = %d, want the default %d", got.lines, defaultSnippetLines)
	}
}

func TestReadImpactAskAllowsAZeroDepthUnlikeSymbolCalls(t *testing.T) {
	// code.impact's own depth may be 0 -- "just tell me what changed, do not
	// walk callers at all" is a legitimate, cheaper question. symbol.calls
	// has no such reading: a call graph walk of zero hops is nothing asked.
	got, err := readImpactAsk(map[string]any{"baseline": "HEAD", "depth": 0})
	if err != nil {
		t.Fatalf("readImpactAsk: %v", err)
	}
	if got.depth != 0 {
		t.Errorf("depth = %d, want 0 preserved rather than defaulted", got.depth)
	}
}

func TestReadImpactAskRejectsInvalidPayloads(t *testing.T) {
	valid := map[string]any{"baseline": "HEAD"}
	withOverride := func(key string, value any) map[string]any {
		out := make(map[string]any, len(valid))
		for k, v := range valid {
			out[k] = v
		}
		if value == nil {
			delete(out, key)
		} else {
			out[key] = value
		}
		return out
	}
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"baseline is missing", withOverride("baseline", nil)},
		{"baseline is blank", withOverride("baseline", "   ")},
		{"baseline looks like a git option", withOverride("baseline", "--force")},
		{"baseline looks like a short option", withOverride("baseline", "-x")},
		{"depth is negative", withOverride("depth", -1)},
		{"snippet_lines is zero", withOverride("snippet_lines", 0)},
		{"snippet_lines is negative", withOverride("snippet_lines", -3)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readImpactAsk(tc.payload)
			if got := contract.KindOf(err); got != contract.FailureInvalidInput {
				t.Errorf("kind = %v, want invalid_input (err = %v)", got, err)
			}
		})
	}
}

// --- hunk parsing ------------------------------------------------------

func TestParseHunksTracksCurrentTreeLineRanges(t *testing.T) {
	cases := []struct {
		name      string
		diff      string
		wantFiles []string
		wantHunks []hunk
	}{
		{
			name: "a one-line change with no comma-count on either side",
			diff: "diff --git a/foo.go b/foo.go\n" +
				"index 1111111..2222222 100644\n" +
				"--- a/foo.go\n" +
				"+++ b/foo.go\n" +
				"@@ -5 +5 @@ func something() {\n" +
				"-old line\n" +
				"+new line\n",
			wantFiles: []string{"foo.go"},
			wantHunks: []hunk{{file: "foo.go", start: 5, end: 5}},
		},
		{
			name: "a pure addition of lines within an existing file",
			diff: "diff --git a/foo.go b/foo.go\n" +
				"index 1111111..2222222 100644\n" +
				"--- a/foo.go\n" +
				"+++ b/foo.go\n" +
				"@@ -10,0 +11,3 @@\n" +
				"+new1\n+new2\n+new3\n",
			wantFiles: []string{"foo.go"},
			wantHunks: []hunk{{file: "foo.go", start: 11, end: 13}},
		},
		{
			name: "a pure deletion of lines leaves a single anchor at the closest current line",
			diff: "diff --git a/foo.go b/foo.go\n" +
				"index 1111111..2222222 100644\n" +
				"--- a/foo.go\n" +
				"+++ b/foo.go\n" +
				"@@ -20,2 +19,0 @@\n" +
				"-removed1\n-removed2\n",
			wantFiles: []string{"foo.go"},
			wantHunks: []hunk{{file: "foo.go", start: 19, end: 19}},
		},
		{
			name: "a whole new file",
			diff: "diff --git a/brand-new.go b/brand-new.go\n" +
				"new file mode 100644\n" +
				"index 0000000..1111111\n" +
				"--- /dev/null\n" +
				"+++ b/brand-new.go\n" +
				"@@ -0,0 +1,3 @@\n" +
				"+a\n+b\n+c\n",
			wantFiles: []string{"brand-new.go"},
			wantHunks: []hunk{{file: "brand-new.go", start: 1, end: 3}},
		},
		{
			name: "a whole file deletion is reported as a changed file with no hunk to attach",
			diff: "diff --git a/gone.go b/gone.go\n" +
				"deleted file mode 100644\n" +
				"index 1111111..0000000\n" +
				"--- a/gone.go\n" +
				"+++ /dev/null\n" +
				"@@ -1,3 +0,0 @@\n" +
				"-a\n-b\n-c\n",
			wantFiles: []string{"gone.go"},
			wantHunks: nil,
		},
		{
			name: "a rename with a content change reports only the new name",
			diff: "diff --git a/old_name.go b/new_name.go\n" +
				"similarity index 91%\n" +
				"rename from old_name.go\n" +
				"rename to new_name.go\n" +
				"index 1111111..2222222 100644\n" +
				"--- a/old_name.go\n" +
				"+++ b/new_name.go\n" +
				"@@ -5 +5 @@\n" +
				"-old\n+new\n",
			wantFiles: []string{"new_name.go"},
			wantHunks: []hunk{{file: "new_name.go", start: 5, end: 5}},
		},
		{
			name: "multiple hunks in the same file",
			diff: "diff --git a/multi.go b/multi.go\n" +
				"index 1111111..2222222 100644\n" +
				"--- a/multi.go\n" +
				"+++ b/multi.go\n" +
				"@@ -3,2 +3,2 @@\n" +
				"-old3\n-old4\n+new3\n+new4\n" +
				"@@ -10 +10,3 @@\n" +
				"-old10\n+new10a\n+new10b\n+new10c\n",
			wantFiles: []string{"multi.go"},
			wantHunks: []hunk{{file: "multi.go", start: 3, end: 4}, {file: "multi.go", start: 10, end: 12}},
		},
		{
			name: "multiple files are both reported, sorted",
			diff: "diff --git a/z.go b/z.go\n--- a/z.go\n+++ b/z.go\n@@ -1 +1 @@\n-a\n+b\n" +
				"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-a\n+b\n",
			wantFiles: []string{"a.go", "z.go"},
			wantHunks: []hunk{{file: "z.go", start: 1, end: 1}, {file: "a.go", start: 1, end: 1}},
		},
		{
			name:      "no diff at all",
			diff:      "",
			wantFiles: nil,
			wantHunks: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, hunks := parseHunks(tc.diff)
			if !slices.Equal(files, tc.wantFiles) {
				t.Errorf("files = %v, want %v", files, tc.wantFiles)
			}
			if !slices.Equal(hunks, tc.wantHunks) {
				t.Errorf("hunks = %v, want %v", hunks, tc.wantHunks)
			}
		})
	}
}

// diff itself is a thin "run git, then parseHunks" wrapper; this proves that
// wiring against a real repository rather than trusting it by inspection.
func TestDiffEndToEndAgainstARealGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := initGitRepo(t)
	writeFile(t, root, "keep.go", "package x\n\nfunc Keep() int {\n\treturn 1\n}\n")
	writeFile(t, root, "remove.go", "package x\n\nfunc Gone() {}\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "--quiet", "-m", "initial")

	// Modify one file, delete another, add a third -- all three shapes in
	// one diff, exactly what a real commission's baseline could look like.
	writeFile(t, root, "keep.go", "package x\n\nfunc Keep() int {\n\treturn 2\n}\n")
	if err := os.Remove(filepath.Join(root, "remove.go")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	writeFile(t, root, "added.go", "package x\n\nfunc Added() {}\n")
	// git diff never reports an untracked file, staged or not -- it has no
	// baseline snapshot to compare against until it is at least added.
	gitRun(t, root, "add", "added.go")

	runner := newTestRunner(t)
	files, hunks, err := runner.diff(t.Context(), root, "HEAD", nil, &meter{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	want := []string{"added.go", "keep.go", "remove.go"}
	if !slices.Equal(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	byFile := map[string][]hunk{}
	for _, h := range hunks {
		byFile[h.file] = append(byFile[h.file], h)
	}
	if len(byFile["keep.go"]) != 1 || byFile["keep.go"][0].start != 4 {
		t.Errorf("keep.go hunks = %v, want one hunk starting at line 4", byFile["keep.go"])
	}
	if len(byFile["added.go"]) != 1 {
		t.Errorf("added.go hunks = %v, want one hunk covering the new lines", byFile["added.go"])
	}
	if len(byFile["remove.go"]) != 0 {
		t.Errorf("remove.go hunks = %v, want none: nothing in the current tree to point at", byFile["remove.go"])
	}
}

// --- correlating hunks with the graph --------------------------------

func TestDirectlyChangedPicksTheSmallestEnclosingSymbol(t *testing.T) {
	symbols := map[string][]symbolRange{
		"file.go": {
			{qualifiedName: "pkg.File", name: "File", kind: "File", start: 1, end: 100},
			{qualifiedName: "pkg.File.Method", name: "Method", kind: "Method", start: 10, end: 20},
		},
	}
	hunks := []hunk{{file: "file.go", start: 12, end: 15}}
	got := directlyChanged(hunks, symbols)
	if len(got) != 1 || got[0].qualifiedName != "pkg.File.Method" {
		t.Fatalf("directlyChanged = %+v, want only pkg.File.Method (the smaller enclosing symbol)", got)
	}
}

func TestDirectlyChangedDedupsMultipleHunksInsideTheSameSymbol(t *testing.T) {
	symbols := map[string][]symbolRange{
		"file.go": {{qualifiedName: "pkg.Fn", name: "Fn", start: 1, end: 50}},
	}
	hunks := []hunk{
		{file: "file.go", start: 5, end: 5},
		{file: "file.go", start: 30, end: 32},
	}
	got := directlyChanged(hunks, symbols)
	if len(got) != 1 {
		t.Fatalf("directlyChanged = %+v, want the one symbol deduplicated across both hunks", got)
	}
}

func TestDirectlyChangedSkipsAHunkThatOverlapsNoKnownSymbol(t *testing.T) {
	symbols := map[string][]symbolRange{
		"file.go": {{qualifiedName: "pkg.Fn", name: "Fn", start: 1, end: 10}},
	}
	hunks := []hunk{{file: "file.go", start: 500, end: 510}}
	if got := directlyChanged(hunks, symbols); len(got) != 0 {
		t.Errorf("directlyChanged = %+v, want none: nothing in the graph covers that range", got)
	}
	if got := directlyChanged(hunks, map[string][]symbolRange{}); len(got) != 0 {
		t.Errorf("directlyChanged against an unknown file = %+v, want none", got)
	}
}

func TestDirectlyChangedCapsAtMaxImpactSeedsButKeepsThemAll(t *testing.T) {
	symbols := map[string][]symbolRange{}
	var hunks []hunk
	const total = maxImpactSeeds + 7
	for i := range total {
		name := "pkg.Fn" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		symbols["file.go"] = append(symbols["file.go"], symbolRange{qualifiedName: name, start: i * 10, end: i*10 + 5})
		hunks = append(hunks, hunk{file: "file.go", start: i * 10, end: i * 10})
	}
	capped := directlyChanged(hunks, symbols)
	if len(capped) != maxImpactSeeds {
		t.Fatalf("directlyChanged returned %d, want the cap of %d", len(capped), maxImpactSeeds)
	}
	if !slices.IsSortedFunc(capped, func(a, b symbolRange) int { return strings.Compare(a.qualifiedName, b.qualifiedName) }) {
		t.Error("directlyChanged is not sorted by qualified name")
	}
	all := directlyChangedAll(hunks, symbols)
	if len(all) != total {
		t.Fatalf("directlyChangedAll returned %d, want every one of the %d symbols the cap would otherwise drop", len(all), total)
	}
}

func TestSeedFileIndexMapsQualifiedNamesBackToTheirFile(t *testing.T) {
	symbols := map[string][]symbolRange{
		"a.go": {{qualifiedName: "pkg.A", start: 1, end: 5}},
		"b.go": {{qualifiedName: "pkg.B", start: 1, end: 5}},
	}
	index := seedFileIndex(nil, symbols)
	if index["pkg.A"] != "a.go" {
		t.Errorf("pkg.A -> %q, want a.go", index["pkg.A"])
	}
	if index["pkg.B"] != "b.go" {
		t.Errorf("pkg.B -> %q, want b.go", index["pkg.B"])
	}
}

// gitRun runs git in root, failing the test on error.
func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
