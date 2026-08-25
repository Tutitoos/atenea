package kivgraph

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func gitTestRepo(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Changed() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "Test"}, {"add", "main.go"}, {"commit", "-qm", "baseline"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	baseline := strings.TrimSpace(string(runGit(t, root, "rev-parse", "HEAD")))
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Changed() int { return 2 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, baseline
}

func runGit(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return output
}

func TestGitDiffMapsCurrentHunksAndScope(t *testing.T) {
	root, baseline := gitTestRepo(t)
	changed, ranges, _, err := gitDiff(context.Background(), root, baseline, []string{"main.go"})
	if err != nil {
		t.Fatalf("gitDiff: %v", err)
	}
	if len(changed) != 1 || changed[0] != "main.go" {
		t.Fatalf("changed = %v, want [main.go]", changed)
	}
	got := ranges["main.go"]
	if len(got) != 1 || got[0].start != 2 || got[0].end != 2 {
		t.Fatalf("ranges = %#v, want line 2", got)
	}
}

// The hunk parser reads "+++ b/<path>", and "b/" is only Git's default: both
// of these settings are ordinary entries in a developer's configuration and
// each one changes it. With either in force and the prefixes not pinned on the
// command line, no header matched, every file lost its hunks, and code.impact
// answered by covering whole files -- blaming every declaration in main.go for
// a one-line change, in an answer indistinguishable from a correct one.
func TestGitDiffReadsHunksWhateverThePrefixConfigurationSays(t *testing.T) {
	for _, setting := range []string{"diff.noprefix", "diff.mnemonicPrefix"} {
		t.Run(setting, func(t *testing.T) {
			root, baseline := gitTestRepo(t)
			runGit(t, root, "config", setting, "true")

			changed, ranges, unrecognized, err := gitDiff(context.Background(), root, baseline, nil)
			if err != nil {
				t.Fatalf("gitDiff: %v", err)
			}
			if len(changed) != 1 || changed[0] != "main.go" {
				t.Fatalf("changed = %v, want [main.go]", changed)
			}
			// Line 2 alone, not the whole file: the fallback covers 1..2 and
			// would pass a test that only asked for "some range".
			got := ranges["main.go"]
			if len(got) != 1 || got[0].start != 2 || got[0].end != 2 {
				t.Fatalf("ranges = %#v, want only the changed line 2", got)
			}
			if unrecognized != 0 {
				t.Fatalf("unrecognized = %d, want none: every header was read", unrecognized)
			}
		})
	}
}

func TestGitDiffIncludesUntrackedCurrentFiles(t *testing.T) {
	root, baseline := gitTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package main\nfunc New() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, ranges, _, err := gitDiff(context.Background(), root, baseline, nil)
	if err != nil {
		t.Fatalf("gitDiff: %v", err)
	}
	found := false
	for _, path := range changed {
		if path == "new.go" {
			found = true
		}
	}
	if !found || len(ranges["new.go"]) != 1 || ranges["new.go"][0].start != 1 {
		t.Fatalf("untracked changed=%v ranges=%#v, want new.go covered from line 1", changed, ranges["new.go"])
	}
}

func TestGitDiffKeepsDeletedFilesWithoutInventingCurrentRanges(t *testing.T) {
	root, baseline := gitTestRepo(t)
	if err := os.Remove(filepath.Join(root, "main.go")); err != nil {
		t.Fatal(err)
	}
	changed, ranges, _, err := gitDiff(context.Background(), root, baseline, nil)
	if err != nil {
		t.Fatalf("gitDiff: %v", err)
	}
	if len(changed) != 1 || changed[0] != "main.go" {
		t.Fatalf("changed = %v, want deleted main.go", changed)
	}
	if len(ranges["main.go"]) != 0 {
		t.Fatalf("ranges = %#v, want no current range for deletion", ranges["main.go"])
	}
}

func TestRunIndexUsesExplicitIndexerAndPostcondition(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	var gotRoot, gotMode string
	runner, err := New(Options{
		Session: func(context.Context) (*mcpstdio.Session, error) { return sess, nil },
		Index: func(_ context.Context, root, mode string) (IndexReport, error) {
			gotRoot, gotMode = root, mode
			return IndexReport{Generation: "next", Nodes: 3074, Edges: 11460}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out, err := runner.Run(context.Background(), request(t, repo, CapabilityIndex, map[string]any{"mode": "full"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotRoot != absPath(t, repo.Path) || gotMode != "full" {
		t.Fatalf("index args = (%q, %q), want (%q, full)", gotRoot, gotMode, absPath(t, repo.Path))
	}
	if out.Result["status"] != "ready" || out.Result["nodes"] != 3074 || out.Result["edges"] != 11460 {
		t.Fatalf("result = %#v, want ready/3074/11460", out.Result)
	}
	var notes []string
	for _, discovery := range out.Discoveries {
		notes = append(notes, discovery.Note)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "generation next") {
		t.Fatal("index result did not preserve the authoritative generation")
	}
}

func TestRunIndexRejectsUnsupportedMode(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", absPath(t, repo.Path)), false)
	runner, err := New(Options{
		Session: func(context.Context) (*mcpstdio.Session, error) { return sess, nil },
		Index:   func(context.Context, string, string) (IndexReport, error) { return IndexReport{}, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(context.Background(), request(t, repo, CapabilityIndex, map[string]any{"mode": "fast"}))
	if err == nil || !strings.Contains(err.Error(), "must be one of full") {
		t.Fatalf("error = %v, want unsupported mode", err)
	}
}

func TestParseIndexReportUsesFinalResultEvent(t *testing.T) {
	report, err := parseIndexReport(`{"event":"progress","progress":{"phase":"scan"}}
{"event":"result","result":{"passed":true,"generation_id":"000009","counts":{"symbols":12,"edges":34}}}`)
	if err != nil {
		t.Fatalf("parseIndexReport: %v", err)
	}
	if report.Generation != "000009" || report.Nodes != 12 || report.Edges != 34 {
		t.Fatalf("report = %#v, want generation 000009 and 12/34", report)
	}
}

func TestParseIndexReportRejectsMissingOrFailedResult(t *testing.T) {
	for name, stream := range map[string]string{
		"missing": `{"event":"progress"}`,
		"failed":  `{"event":"result","result":{"passed":false,"error":"gate failed"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseIndexReport(stream); err == nil {
				t.Fatal("parseIndexReport succeeded")
			}
		})
	}
}

func TestRunImpactIncludesChangedRootAndBlastRadius(t *testing.T) {
	root, baseline := gitTestRepo(t)
	repo := contract.NewRepository("current", root, nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", root), false)
	fake.on(toolOutline, `{"results":{"repository":"current","path":"main.go","symbols":[{"name":"Changed","qualified_name":"Changed","kind":"function","stable_key":"changed","start_line":2,"end_line":2}]}}`, false)
	fake.on(toolBlast, `{"results":{"traversal_truncated":false,"symbols":[{"name":"Caller","qualified_name":"Caller","kind":"function","depth":1,"repository":"current","file_path":"main.go","start_line":6,"stable_key":"caller"}]}}`, false)
	runner := newTestRunner(t, sess)
	capability := impactCapability()
	req := request(t, repo, CapabilityImpact, map[string]any{"baseline": baseline, "depth": 1})
	req.Capability = capability
	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.Result["changed_files"]; got == nil {
		t.Fatal("changed_files missing")
	}
	rows, ok := out.Result["affected_symbols"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("affected_symbols = %#v, want changed root plus caller", out.Result["affected_symbols"])
	}
	first := rows[0].(map[string]any)
	if first["name"] != "Changed" || first["depth"] != 0 {
		t.Fatalf("first impact row = %#v, want Changed at depth 0", first)
	}
	second := rows[1].(map[string]any)
	if second["name"] != "Caller" || second["depth"] != 1 {
		t.Fatalf("second impact row = %#v, want Caller at depth 1", second)
	}
}

func TestRunImpactOmitsForeignBlastRadiusAndExplainsBoundary(t *testing.T) {
	root, baseline := gitTestRepo(t)
	repo := contract.NewRepository("current", root, nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", root), false)
	fake.on(toolOutline, `{"results":{"repository":"current","path":"main.go","symbols":[{"name":"Changed","qualified_name":"Changed","kind":"function","stable_key":"changed","start_line":2,"end_line":2}]}}`, false)
	fake.on(toolBlast, `{"results":{"traversal_truncated":false,"symbols":[{"name":"Other","qualified_name":"Other","kind":"function","depth":1,"repository":"other","file_path":"other.go","start_line":4,"stable_key":"other"}]}}`, false)
	runner := newTestRunner(t, sess)
	req := request(t, repo, CapabilityImpact, map[string]any{"baseline": baseline, "depth": 1})
	req.Capability = impactCapability()
	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := out.Result["affected_symbols"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["name"] != "Changed" {
		t.Fatalf("affected_symbols = %#v, want only local changed root", rows)
	}
	var notes []string
	for _, discovery := range out.Discoveries {
		notes = append(notes, discovery.Note)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "other repositories") {
		t.Fatalf("notes = %v, want foreign-repository boundary", notes)
	}
}

// A diff of any size touches many declarations, and code.impact used to refuse
// the whole analysis the first time kivgraph could not turn one of them into a
// stable key -- throwing away every consumer of every other root over the one
// symbol the graph has not indexed. That is the same gap the unindexed-file
// branch already reports as a note rather than a failure.
func TestRunImpactNotesADeclarationItCannotAddressAndKeepsGoing(t *testing.T) {
	root, baseline := gitTestRepo(t)
	repo := contract.NewRepository("current", root, nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", root), false)
	// Two declarations over the one changed line. The first carries its own
	// stable key; the second has none, so it costs a get_symbol lookup -- and
	// that is the lookup the graph cannot answer.
	fake.on(toolOutline, `{"results":{"repository":"current","path":"main.go","symbols":[
		{"name":"Changed","qualified_name":"Changed","kind":"function","stable_key":"changed","start_line":1,"end_line":3},
		{"name":"Helper","qualified_name":"Helper","kind":"function","start_line":2,"end_line":2}]}}`, false)
	fake.on(toolGet, "SYMBOL_NOT_FOUND: no symbol named Helper", true)
	fake.on(toolBlast, `{"results":{"traversal_truncated":false,"symbols":[{"name":"Caller","qualified_name":"Caller","kind":"function","depth":1,"repository":"current","file_path":"main.go","start_line":6,"stable_key":"caller"}]}}`, false)

	req := request(t, repo, CapabilityImpact, map[string]any{"baseline": baseline, "depth": 1})
	req.Capability = impactCapability()
	out, err := newTestRunner(t, sess).Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows, ok := out.Result["affected_symbols"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("affected_symbols = %#v, want the addressable root and its caller", out.Result["affected_symbols"])
	}
	if !hasNote(out, "Helper at main.go:2") {
		t.Fatalf("discoveries = %#v, want one naming the declaration that could not be addressed", out.Discoveries)
	}
	// The "no current declaration over the changed lines" note would be a
	// second, untrue explanation of the same file: one WAS found.
	if hasNote(out, "no current declaration") {
		t.Fatalf("discoveries = %#v, want no claim that nothing overlapped", out.Discoveries)
	}
}

// The failures stableKeyOf does still raise name the capability that asked.
// All three of its messages said "symbol.consumers" literally, and runImpact
// calls it once per declaration the diff touched, so a code.impact failure
// sent whoever read it looking at the wrong capability.
func TestStableKeyFailuresNameTheCapabilityThatAsked(t *testing.T) {
	root, baseline := gitTestRepo(t)
	repo := contract.NewRepository("current", root, nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("current", root), false)
	fake.on(toolOutline, `{"results":{"repository":"current","path":"main.go","symbols":[
		{"name":"Helper","qualified_name":"Helper","kind":"function","start_line":2,"end_line":2}]}}`, false)
	// Not "nothing by that name": an answer nobody can read is a provider in
	// trouble, and it still stops the call.
	fake.on(toolGet, "this is not JSON", false)

	req := request(t, repo, CapabilityImpact, map[string]any{"baseline": baseline, "depth": 1})
	req.Capability = impactCapability()
	_, err := newTestRunner(t, sess).Run(context.Background(), req)
	if err == nil {
		t.Fatal("Run succeeded on an unreadable get_symbol answer")
	}
	if !strings.Contains(err.Error(), CapabilityImpact) {
		t.Fatalf("err = %v, want it to name %s", err, CapabilityImpact)
	}
	if strings.Contains(err.Error(), CapabilityConsumers) {
		t.Fatalf("err = %v, want it not to name %s", err, CapabilityConsumers)
	}
}
