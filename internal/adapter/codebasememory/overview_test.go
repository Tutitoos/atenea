package codebasememory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func symbolOverviewCapability() contract.Capability {
	return contract.Capability{
		ID:      "symbol.overview",
		Version: contract.Version{Major: 1},
		Summary: "What a file declares.",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "file", Type: contract.TypeString, Required: true},
			{Name: "depth", Type: contract.TypeInt},
		},
		Outputs: []contract.Field{
			{Name: "symbols", Type: contract.TypeRecordList, Required: true, Fields: []contract.Field{
				{Name: "name", Type: contract.TypeString, Required: true},
				{Name: "kind", Type: contract.TypeString, Required: true},
				{Name: "line", Type: contract.TypeInt, Required: true},
				{Name: "column", Type: contract.TypeInt, Required: true},
				{Name: "end_line", Type: contract.TypeInt},
			}},
		},
	}
}

func TestReadOverviewAskRefusesADeeperWalk(t *testing.T) {
	_, err := readOverviewAsk(map[string]any{"file": "a.go", "depth": 1})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err = %v)", got, err)
	}
	// The catalog keeps this call away with a bound, so the refusal has to
	// say what the provider cannot do rather than merely that it will not.
	if !strings.Contains(err.Error(), "top level") {
		t.Errorf("error %q does not explain what depth 0 means here", err)
	}
}

func TestReadOverviewAskAcceptsAnOmittedOrZeroDepth(t *testing.T) {
	for _, payload := range []map[string]any{
		{"file": "a.go"},
		{"file": "a.go", "depth": 0},
	} {
		if _, err := readOverviewAsk(payload); err != nil {
			t.Errorf("readOverviewAsk(%v): %v", payload, err)
		}
	}
}

func TestColumnOfFindsAWholeWordOnly(t *testing.T) {
	for _, tc := range []struct {
		text, name string
		want       int
		found      bool
	}{
		{"const maxLineBytes = 1 << 20", "maxLineBytes", 7, true},
		{"func (r *Runner) Run(ctx context.Context) {", "Run", 18, true},
		{"func (r *Runner) identifierAt(root string) {", "identifierAt", 18, true},
		{"type Runner struct {", "Run", 0, false},
		{"prefixRun := 1", "Run", 0, false},
		{"# Getting started", "Getting started", 3, true},
		{"nothing here", "", 0, false},
	} {
		got, ok := columnOf(tc.text, tc.name)
		if ok != tc.found || got != tc.want {
			t.Errorf("columnOf(%q, %q) = %d, %v; want %d, %v", tc.text, tc.name, got, ok, tc.want, tc.found)
		}
	}
}

// The whole answer bar one field comes out of the graph; the column comes
// out of the file. This checks the two halves meet on the same symbol.
func TestRunSymbolOverviewJoinsTheGraphToTheFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "internal/x/x.go", "package x\n\nconst Limit = 3\n\nfunc Run(n int) int {\n\treturn n\n}\n")
	path := fakeCodebaseMemory(t, map[string]string{
		"query_graph": `{"columns":["name","label","start_line","end_line"],"rows":[
			["x.go","File",0,0],
			["Run","Function",5,7],
			["Limit","Variable",3,3]
		],"total":3}`,
	})
	runner, err := New(Options{Binary: path, Implementations: []string{ImplOverview}, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := requestAt(t, root, symbolOverviewCapability(), ImplOverview, map[string]any{"file": "internal/x/x.go"})
	outcome, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	symbols, ok := outcome.Result["symbols"].([]any)
	if !ok {
		t.Fatalf("symbols = %T, want a list", outcome.Result["symbols"])
	}
	// The File node is not something the file declares, and the rows came
	// back out of order.
	if len(symbols) != 2 {
		t.Fatalf("symbols = %v, want the two declarations only", symbols)
	}
	first, _ := symbols[0].(map[string]any)
	if first["name"] != "Limit" || first["line"] != 3 || first["column"] != 7 {
		t.Errorf("first = %v, want Limit at 3:7", first)
	}
	if _, ok := first["end_line"]; ok {
		t.Errorf("first = %v, want no end_line on a one-line declaration", first)
	}
	second, _ := symbols[1].(map[string]any)
	if second["name"] != "Run" || second["line"] != 5 || second["column"] != 6 || second["end_line"] != 7 {
		t.Errorf("second = %v, want Run at 5:6 ending on 7", second)
	}
	if second["kind"] != "Function" {
		t.Errorf("kind = %v, want the graph's own word", second["kind"])
	}
}

// An empty answer and an unindexed file are different findings, and only the
// second is true when the graph has never heard of the path. Reporting the
// first would read as a finished answer.
func TestRunSymbolOverviewSeparatesEmptyFromUnindexed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "internal/x/x.go", "package x\n")
	path := fakeCodebaseMemory(t, map[string]string{
		"query_graph": `{"columns":["name","label","start_line","end_line"],"rows":[],"total":0}`,
	})
	runner, err := New(Options{Binary: path, Implementations: []string{ImplOverview}, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := requestAt(t, root, symbolOverviewCapability(), ImplOverview, map[string]any{"file": "internal/x/x.go"})
	_, err = runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found (err = %v)", got, err)
	}
	if !strings.Contains(err.Error(), "repository.index") {
		t.Errorf("error %q does not name the fix", err)
	}
}

// A file that is in the graph but declares nothing answers with an empty
// list, which is a real answer rather than a failure.
func TestRunSymbolOverviewAnswersEmptyForAFileThatDeclaresNothing(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "internal/x/x.go", "package x\n")
	path := fakeCodebaseMemory(t, map[string]string{
		"query_graph": `{"columns":["name","label","start_line","end_line"],"rows":[["x.go","File",0,0]],"total":1}`,
	})
	runner, err := New(Options{Binary: path, Implementations: []string{ImplOverview}, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := requestAt(t, root, symbolOverviewCapability(), ImplOverview, map[string]any{"file": "internal/x/x.go"})
	outcome, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := outcome.Result["symbols"].([]any); len(got) != 0 {
		t.Fatalf("symbols = %v, want an empty list", got)
	}
}

// The graph putting a name on a line the file does not have it on means the
// index is behind the tree. Every other line number in the same answer is
// then suspect, so the call fails once instead of misdirecting one symbol at
// a time.
func TestRunSymbolOverviewRefusesWhenTheIndexIsBehindTheFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "internal/x/x.go", "package x\n\nfunc Renamed() {}\n")
	path := fakeCodebaseMemory(t, map[string]string{
		"query_graph": `{"columns":["name","label","start_line","end_line"],"rows":[
			["x.go","File",0,0],
			["Original","Function",3,3]
		],"total":2}`,
	})
	runner, err := New(Options{Binary: path, Implementations: []string{ImplOverview}, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := requestAt(t, root, symbolOverviewCapability(), ImplOverview, map[string]any{"file": "internal/x/x.go"})
	_, err = runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable (err = %v)", got, err)
	}
	if !strings.Contains(err.Error(), "behind the file") {
		t.Errorf("error %q does not say why", err)
	}
}

// The graph would answer this without touching the disk, which is exactly
// why it is refused: the list of names a secret-carrying file declares is
// the shape of the secret.
func TestRunSymbolOverviewRefusesASensitiveFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".env", "TOKEN=1\n")
	path := fakeCodebaseMemory(t, map[string]string{
		"query_graph": `{"columns":[],"rows":[],"total":0}`,
	})
	runner, err := New(Options{
		Binary: path, Implementations: []string{ImplOverview},
		Sensitive: []string{".env"}, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := requestAt(t, root, symbolOverviewCapability(), ImplOverview, map[string]any{"file": ".env"})
	_, err = runner.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied (err = %v)", got, err)
	}
}

// Markdown headings are what a markdown file declares, and no language
// server has anything to say about one. Section is in the allowlist for
// that reason, so it has to survive the filter.
func TestRunSymbolOverviewAnswersForMarkdownSections(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/guide.md", "# Guide\n\n## Getting started\n\ntext\n")
	path := fakeCodebaseMemory(t, map[string]string{
		"query_graph": `{"columns":["name","label","start_line","end_line"],"rows":[
			["guide.md","File",0,0],
			["Guide","Section",1,2],
			["Getting started","Section",3,5]
		],"total":3}`,
	})
	runner, err := New(Options{Binary: path, Implementations: []string{ImplOverview}, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := requestAt(t, root, symbolOverviewCapability(), ImplOverview, map[string]any{"file": "docs/guide.md"})
	outcome, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	symbols := outcome.Result["symbols"].([]any)
	if len(symbols) != 2 {
		t.Fatalf("symbols = %v, want both headings", symbols)
	}
	second, _ := symbols[1].(map[string]any)
	if second["name"] != "Getting started" || second["line"] != 3 || second["column"] != 4 {
		t.Errorf("second = %v, want the nested heading at 3:4", second)
	}
}
