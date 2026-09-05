package kivgraph_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/kivgraph"
)

func TestImplementationsPreservesEvidenceAndCoverage(t *testing.T) {
	root := t.TempDir()
	symbol := `{"name":"Reader","qualified_name":"Reader","kind":"interface","repository":"test","file_path":"main.ts","start_line":1,"end_line":2,"depth":1,"match":"lexical"}`
	answer := `{"snapshot_id":7,"total":1,"truncated":true,"next_cursor":"page-2","coverage":{"exact":1},"completeness":{"verdict":"LOWER_BOUND","invisible_scopes":[{"reason":"fixture"}]},"results":{"implementations":[{"repository":"test","file_path":"concrete.ts","start_line":5,"qualified_name":"Concrete","stable_key":"canonical","confidence":"EXACT_TYPECHECKED","provenance":"TYPESCRIPT_IMPL_STRUCTURAL","detection":"structural"}]}}`
	sess := &extensionSession{root: root, answers: map[string]string{
		"get_file_outline":     `{"snapshot_id":7,"coverage":{"exact":1},"completeness":{"verdict":"COMPLETE"},"results":{"symbols":[` + symbol + `]}}`,
		"find_implementations": answer,
	}}
	runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
	if err != nil {
		t.Fatal(err)
	}
	req := extensionRequest(t, root, "symbol.implementations", map[string]any{"file": "main.ts", "line": 1, "column": 1, "limit": 1})
	out, err := runner.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result["completeness"] != "LOWER_BOUND" || out.Result["next_cursor"] != "page-2" || out.Result["snapshot_id"] != 7 {
		t.Fatalf("metadata lost: %#v", out.Result)
	}
	row := out.Result["locations"].([]any)[0].(map[string]any)
	if row["stable_key"] != "canonical" || row["detection"] != "structural" {
		t.Fatalf("evidence lost: %#v", row)
	}
	sess.answers["find_implementations"] = strings.ReplaceAll(answer, `"repository":"test","file_path":"concrete.ts"`, `"repository":"foreign","file_path":"concrete.ts"`)
	if _, err := runner.Run(t.Context(), req); err == nil {
		t.Fatal("foreign identity presented as a local location")
	}
	sess.calls = nil
	req.Payload["limit"] = 501
	if _, err := runner.Run(t.Context(), req); err == nil {
		t.Fatal("invalid limit accepted")
	}
	for _, tool := range sess.calls {
		if tool == "find_implementations" {
			t.Fatal("invalid request reached implementation backend")
		}
	}
}
