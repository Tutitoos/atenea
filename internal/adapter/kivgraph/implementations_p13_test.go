package kivgraph_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/acceptancefixture"
	"github.com/Tutitoos/atenea/internal/adapter/kivgraph"
)

func implementationsOutline(symbols string) string {
	return `{"snapshot_id":9,"coverage":{"exact":1},"completeness":{"verdict":"COMPLETE"},"results":{"symbols":[` + symbols + `]}}`
}

func implementationAnswer(row, coverage, page string) string {
	return `{"snapshot_id":9,"total":` + page + `,"returned":` + page + `,"truncated":false,"coverage":` + coverage + `,"completeness":{"verdict":"COMPLETE"},"results":{"implementations":[` + row + `]}}`
}

func TestImplementationsP13AcceptsGoAndTypeScriptExactRelations(t *testing.T) {
	fixture, active, fixtureErr := acceptancefixture.Load("S05", "S06")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if active && (!strings.Contains(string(fixture.Bytes), "MemoryRepository") || !strings.Contains(string(fixture.Bytes), "Repository")) {
		t.Fatal("sealed implementation fixture lacks its declared relation")
	}
	for _, tc := range []struct {
		name, file, language, outputFile, provenance, confidence string
	}{
		{"go", "main.go", "go", "impl.go", "GO_TYPES_USE", "EXACT_TYPECHECKED"},
		{"typescript-typechecked", "main.ts", "typescript", "impl.ts", "TYPESCRIPT_IMPL_STRUCTURAL", "EXACT_TYPECHECKED"},
		{"typescript-declaration-mapped", "main.ts", "typescript", "impl.ts", "TYPESCRIPT_IMPL_DECLARED", "EXACT_DECLARATION_MAPPED"},
		{"typescript-package-mapped", "main.ts", "typescript", "impl.ts", "TYPESCRIPT_IMPL_STRUCTURAL", "EXACT_PACKAGE_MAPPED"},
		{"typescript-structural-certain", "main.ts", "typescript", "impl.ts", "TYPESCRIPT_IMPL_STRUCTURAL", "STRUCTURAL_CERTAIN"},
	} {
		if active && tc.language != fixture.Language {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			subjectName, implementationName := "Reader", "Concrete"
			subjectLine, implementationLine := 1, 5
			requestFile, resultFile := tc.file, tc.outputFile
			if active {
				ext := ".go"
				subjectLine, implementationLine = 4, 9
				if fixture.Language == "typescript" {
					ext = ".ts"
					subjectLine, implementationLine = 1, 5
				}
				requestFile, resultFile = "repository"+ext, "repository"+ext
				subjectName, implementationName = "Repository", "MemoryRepository"
				if err := os.WriteFile(filepath.Join(root, requestFile), fixture.Bytes, 0600); err != nil {
					t.Fatal(err)
				}
			}
			detection := "structural"
			if tc.provenance == "TYPESCRIPT_IMPL_DECLARED" {
				detection = "declared"
			}
			answer := `{"repository":"test","file_path":"` + resultFile + `","start_line":` + fmt.Sprint(implementationLine) + `,"qualified_name":"` + implementationName + `","stable_key":"stable-1","confidence":"` + tc.confidence + `","provenance":"` + tc.provenance + `","detection":"` + detection + `","language":"` + tc.language + `","edge_kind":"IMPLEMENTS"}`
			sess := &extensionSession{root: root, snapshotID: 9, answers: map[string]string{
				"get_file_outline":     implementationsOutline(`{"name":"` + subjectName + `","qualified_name":"` + subjectName + `","kind":"interface","repository":"test","file_path":"` + requestFile + `","start_line":` + fmt.Sprint(subjectLine) + `,"end_line":` + fmt.Sprint(subjectLine+2) + `}`),
				"find_implementations": implementationAnswer(answer, `{"exact":1,"candidate":0,"unresolved_related":0,"package_level":0}`, "1"),
			}}
			runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
			if err != nil {
				t.Fatal(err)
			}
			req := extensionRequest(t, root, "symbol.implementations", map[string]any{"file": requestFile, "line": subjectLine, "column": 1, "language": tc.language})
			out, err := runner.Run(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			locations := out.Result["locations"].([]any)
			if len(locations) != 1 || locations[0].(map[string]any)["resolution"] != "exact" {
				t.Fatalf("exact location missing: %#v", out.Result)
			}
			if active {
				location := locations[0].(map[string]any)
				if location["qualified_name"] != "MemoryRepository" || location["path"] != resultFile || location["line"] != implementationLine {
					t.Fatalf("sealed Repository implementation did not cross product boundary: %#v", location)
				}
			}
		})
	}
}

func TestImplementationsP13NonExactEvidenceOnlyCountsCoverage(t *testing.T) {
	root := t.TempDir()
	sess := &extensionSession{root: root, snapshotID: 9, answers: map[string]string{
		"get_file_outline":     implementationsOutline(`{"name":"Reader","qualified_name":"Reader","kind":"interface","repository":"test","file_path":"main.go","start_line":1,"end_line":2}`),
		"find_implementations": `{"snapshot_id":9,"total":0,"returned":0,"truncated":false,"coverage":{"exact":0,"candidate":1,"unresolved_related":2,"package_level":3},"completeness":{"verdict":"LOWER_BOUND","invisible_scopes":[{"reason":"fixture"}]},"results":{"implementations":[]}}`,
	}}
	runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
	if err != nil {
		t.Fatal(err)
	}
	req := extensionRequest(t, root, "symbol.implementations", map[string]any{"file": "main.go", "line": 1, "column": 1})
	out, err := runner.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(out.Result["locations"].([]any)); got != 0 {
		t.Fatalf("candidate was exposed as semantic location: %#v", out.Result)
	}
	coverage := out.Result["coverage"].(map[string]any)
	if coverage["candidate"] != 1 {
		t.Fatalf("candidate coverage missing: %#v", coverage)
	}
}

func TestImplementationsP13RejectsDartBeforeProvider(t *testing.T) {
	root := t.TempDir()
	sess := &extensionSession{root: root, snapshotID: 9, answers: map[string]string{}}
	runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
	if err != nil {
		t.Fatal(err)
	}
	req := extensionRequest(t, root, "symbol.implementations", map[string]any{"file": "main.dart", "line": 1, "column": 1})
	if _, err := runner.Run(t.Context(), req); err == nil {
		t.Fatal("Dart request accepted")
	}
	if len(sess.calls) != 0 {
		t.Fatalf("unsupported language reached provider: %v", sess.calls)
	}
}

func TestImplementationsP13RejectsAmbiguityAndDoesNotFind(t *testing.T) {
	root := t.TempDir()
	sess := &extensionSession{root: root, snapshotID: 9, answers: map[string]string{
		"get_file_outline": implementationsOutline(`{"name":"One","qualified_name":"One","kind":"interface","repository":"test","file_path":"main.go","start_line":1,"end_line":3}, {"name":"Two","qualified_name":"Two","kind":"interface","repository":"test","file_path":"main.go","start_line":1,"end_line":4}`),
	}}
	runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
	if err != nil {
		t.Fatal(err)
	}
	req := extensionRequest(t, root, "symbol.implementations", map[string]any{"file": "main.go", "line": 1, "column": 1})
	if _, err := runner.Run(t.Context(), req); err == nil {
		t.Fatal("ambiguous subject accepted")
	}
	for _, call := range sess.calls {
		if call == "find_implementations" {
			t.Fatalf("ambiguous subject reached semantic provider: %v", sess.calls)
		}
	}
}

func TestImplementationsP13RejectsVocabulariesPaginationAndPaths(t *testing.T) {
	cases := []struct {
		name, row, envelope string
	}{
		{"vocabulary", `{"repository":"test","file_path":"impl.go","start_line":4,"qualified_name":"Concrete","stable_key":"stable-1","confidence":"MADE_UP","provenance":"GO_TYPES_USE","detection":"structural","language":"go","edge_kind":"IMPLEMENTS"}`, `{"snapshot_id":9,"total":1,"returned":1,"truncated":false,"coverage":{"exact":1},"completeness":{"verdict":"COMPLETE"},"results":{"implementations":[ROW]}}`},
		{"crossed-evidence", `{"repository":"test","file_path":"impl.go","start_line":4,"qualified_name":"Concrete","stable_key":"stable-1","confidence":"EXACT_TYPECHECKED","provenance":"GO_TYPES_USE","detection":"typed","language":"go","edge_kind":"IMPLEMENTS"}`, `{"snapshot_id":9,"total":1,"returned":1,"truncated":false,"coverage":{"exact":1},"completeness":{"verdict":"COMPLETE"},"results":{"implementations":[ROW]}}`},
		{"go-confidence", `{"repository":"test","file_path":"impl.go","start_line":4,"qualified_name":"Concrete","stable_key":"stable-1","confidence":"EXACT_PACKAGE_MAPPED","provenance":"GO_TYPES_USE","detection":"structural","language":"go","edge_kind":"IMPLEMENTS"}`, `{"snapshot_id":9,"total":1,"returned":1,"truncated":false,"coverage":{"exact":1},"completeness":{"verdict":"COMPLETE"},"results":{"implementations":[ROW]}}`},
		{"typescript-confidence", `{"repository":"test","file_path":"impl.ts","start_line":4,"qualified_name":"Concrete","stable_key":"stable-1","confidence":"EXACT_DECLARATION_MAPPED","provenance":"TYPESCRIPT_IMPL_STRUCTURAL","detection":"structural","language":"typescript","edge_kind":"IMPLEMENTS"}`, `{"snapshot_id":9,"total":1,"returned":1,"truncated":false,"coverage":{"exact":1},"completeness":{"verdict":"COMPLETE"},"results":{"implementations":[ROW]}}`},
		{"exact-count-zero", `{"repository":"test","file_path":"impl.go","start_line":4,"qualified_name":"Concrete","stable_key":"stable-1","confidence":"EXACT_TYPECHECKED","provenance":"GO_TYPES_USE","detection":"structural","language":"go","edge_kind":"IMPLEMENTS"}`, `{"snapshot_id":9,"total":1,"returned":1,"truncated":false,"coverage":{"exact":0},"completeness":{"verdict":"COMPLETE"},"results":{"implementations":[ROW]}}`},
		{"pagination", `{"repository":"test","file_path":"impl.go","start_line":4,"qualified_name":"Concrete","stable_key":"stable-1","confidence":"EXACT_TYPECHECKED","provenance":"GO_TYPES_USE","detection":"structural","language":"go","edge_kind":"IMPLEMENTS"}`, `{"snapshot_id":9,"total":1,"returned":1,"truncated":true,"coverage":{"exact":1},"completeness":{"verdict":"LOWER_BOUND"},"results":{"implementations":[ROW]}}`},
		{"path", `{"repository":"test","file_path":"../impl.go","start_line":4,"qualified_name":"Concrete","stable_key":"stable-1","confidence":"EXACT_TYPECHECKED","provenance":"GO_TYPES_USE","detection":"structural","language":"go","edge_kind":"IMPLEMENTS"}`, `{"snapshot_id":9,"total":1,"returned":1,"truncated":false,"coverage":{"exact":1},"completeness":{"verdict":"COMPLETE"},"results":{"implementations":[ROW]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			envelope := strings.ReplaceAll(tc.envelope, "ROW", tc.row)
			sess := &extensionSession{root: root, snapshotID: 9, answers: map[string]string{
				"get_file_outline":     implementationsOutline(`{"name":"Reader","qualified_name":"Reader","kind":"interface","repository":"test","file_path":"main.go","start_line":1,"end_line":2}`),
				"find_implementations": envelope,
			}}
			runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
			if err != nil {
				t.Fatal(err)
			}
			req := extensionRequest(t, root, "symbol.implementations", map[string]any{"file": "main.go", "line": 1, "column": 1})
			if _, err := runner.Run(t.Context(), req); err == nil {
				t.Fatalf("invalid %s accepted", tc.name)
			}
		})
	}
}

func TestImplementationsP13RejectsGenerationDriftBeforePublication(t *testing.T) {
	root := t.TempDir()
	row := `{"repository":"test","file_path":"impl.go","start_line":4,"qualified_name":"Concrete","stable_key":"stable-1","confidence":"EXACT_TYPECHECKED","provenance":"GO_TYPES_USE","detection":"typed","language":"go","edge_kind":"IMPLEMENTS"}`
	sess := &extensionSession{root: root, snapshotID: 9, answers: map[string]string{
		"get_file_outline":     implementationsOutline(`{"name":"Reader","qualified_name":"Reader","kind":"interface","repository":"test","file_path":"main.go","start_line":1,"end_line":2}`),
		"find_implementations": strings.Replace(implementationAnswer(row, `{"exact":1,"candidate":0,"unresolved_related":0,"package_level":0}`, "1"), `"snapshot_id":9`, `"snapshot_id":8`, 1),
	}}
	runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
	if err != nil {
		t.Fatal(err)
	}
	req := extensionRequest(t, root, "symbol.implementations", map[string]any{"file": "main.go", "line": 1, "column": 1})
	if _, err := runner.Run(t.Context(), req); err == nil {
		t.Fatal("generation drift was published")
	}
}

func TestImplementationsP13AppliesScopeToCanonicalProviderPaths(t *testing.T) {
	root := t.TempDir()
	row := `{"repository":"test","file_path":"other/impl.go","start_line":4,"qualified_name":"Concrete","stable_key":"stable-1","confidence":"EXACT_TYPECHECKED","provenance":"GO_TYPES_USE","detection":"structural","language":"go","edge_kind":"IMPLEMENTS"}`
	sess := &extensionSession{root: root, snapshotID: 9, answers: map[string]string{
		"get_file_outline":     implementationsOutline(`{"name":"Reader","qualified_name":"Reader","kind":"interface","repository":"test","file_path":"main.go","start_line":1,"end_line":2}`),
		"find_implementations": implementationAnswer(row, `{"exact":1,"candidate":0,"unresolved_related":0,"package_level":0}`, "1"),
	}}
	runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
	if err != nil {
		t.Fatal(err)
	}
	req := extensionRequest(t, root, "symbol.implementations", map[string]any{"file": "main.go", "line": 1, "column": 1, "scope": []any{"src"}})
	if _, err := runner.Run(t.Context(), req); err == nil {
		t.Fatal("out-of-scope provider row published")
	}
}

func TestImplementationsP13AllowsCompletePagesAndDowngradesUnresolvedCoverage(t *testing.T) {
	root := t.TempDir()
	row := `{"repository":"test","file_path":"impl.go","start_line":4,"qualified_name":"Concrete","stable_key":"stable-1","confidence":"EXACT_TYPECHECKED","provenance":"GO_TYPES_USE","detection":"structural","language":"go","edge_kind":"IMPLEMENTS"}`
	sess := &extensionSession{root: root, snapshotID: 9, answers: map[string]string{
		"get_file_outline":     implementationsOutline(`{"name":"Reader","qualified_name":"Reader","kind":"interface","repository":"test","file_path":"main.go","start_line":1,"end_line":2}`),
		"find_implementations": `{"snapshot_id":9,"total":2,"returned":1,"truncated":true,"next_cursor":"page-2","coverage":{"exact":2,"candidate":0,"unresolved_related":1,"package_level":0},"completeness":{"verdict":"COMPLETE"},"results":{"implementations":[` + row + `]}}`,
	}}
	runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
	if err != nil {
		t.Fatal(err)
	}
	req := extensionRequest(t, root, "symbol.implementations", map[string]any{"file": "main.go", "line": 1, "column": 1})
	out, err := runner.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result["completeness"] != "LOWER_BOUND" || out.Result["next_cursor"] != "page-2" {
		t.Fatalf("unsafe completeness or cursor handling: %#v", out.Result)
	}
}

func TestImplementationsP13AcceptsFinalContinuationPageWithGlobalTotal(t *testing.T) {
	root := t.TempDir()
	row := `{"repository":"test","file_path":"impl.go","start_line":4,"qualified_name":"Concrete","stable_key":"stable-1","confidence":"EXACT_TYPECHECKED","provenance":"GO_TYPES_USE","detection":"structural","language":"go","edge_kind":"IMPLEMENTS"}`
	sess := &extensionSession{root: root, snapshotID: 9, answers: map[string]string{
		"get_file_outline":     implementationsOutline(`{"name":"Reader","qualified_name":"Reader","kind":"interface","repository":"test","file_path":"main.go","start_line":1,"end_line":2}`),
		"find_implementations": `{"snapshot_id":9,"total":2,"returned":1,"truncated":false,"coverage":{"exact":2,"candidate":0,"unresolved_related":0,"package_level":0},"completeness":{"verdict":"COMPLETE"},"results":{"implementations":[` + row + `]}}`,
	}}
	runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
	if err != nil {
		t.Fatal(err)
	}
	req := extensionRequest(t, root, "symbol.implementations", map[string]any{"file": "main.go", "line": 1, "column": 1, "cursor": "page-1"})
	if _, err := runner.Run(t.Context(), req); err != nil {
		t.Fatalf("valid final continuation page rejected: %v", err)
	}
}
