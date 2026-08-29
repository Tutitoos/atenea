package kivgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestIntentSearchScopesRepositoryAndReturnsRankedDiagnostics(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("atenea", absPath(t, repo.Path)), false)
	fake.on(toolIntent, `{
		"truncated":true,"next_cursor":"intent-v1:next",
		"coverage":{"exact":1,"candidate":0,"unresolved_related":0,"package_level":0},
		"guidance":"continue with the cursor",
		"results":{"terms":[{"term":"dispatch","symbols":401,"frequency":"common"}],
		"unmatched_terms":["router"],"symbols":[{"qualified_name":"Runner.Run","kind":"method",
		"repository":"atenea","file_path":"internal/core/run.go","start_line":41,"end_line":88,"terms":2,"match":"lexical"}]}}`, false)
	runner := newTestRunner(t, sess)

	out, err := runner.Run(context.Background(), request(t, repo, CapabilityIntent, map[string]any{
		"intent": "where is capability dispatch", "keywords": []any{"router"}, "path_prefix": "internal", "limit": 1,
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	asked := fake.callsTo(toolIntent)
	if len(asked) != 1 || asked[0]["repo"] != "atenea" || asked[0]["view"] != "full" || asked[0]["path_prefix"] != "internal" {
		t.Fatalf("find_by_intent args = %#v", asked)
	}
	matches := out.Result["matches"].([]any)
	if got := matches[0].(map[string]any)["rank"]; got != 1 {
		t.Fatalf("rank = %#v, want 1", got)
	}
	if got := matches[0].(map[string]any)["name"]; got != "Run" {
		t.Fatalf("name = %#v, want derived simple name Run", got)
	}
	if out.Result["next_cursor"] != "intent-v1:next" || !hasNote(out, "guidance") {
		t.Fatalf("outcome = %#v", out)
	}
}

func TestIntentSearchRejectsBoundsAndForeignRows(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]any
	}{
		{"empty intent", map[string]any{"intent": ""}},
		{"limit zero", map[string]any{"intent": "x", "limit": 0}},
		{"limit above maximum", map[string]any{"intent": "x", "limit": maximumIntentLimit + 1}},
		{"too many keywords", map[string]any{"intent": "x", "keywords": make([]any, maximumIntentKeywords+1)}},
		{"escaping prefix", map[string]any{"intent": "x", "path_prefix": "../other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			fake, sess := newFakeKivgraph(t)
			fake.on(toolStatus, readyStatus("atenea", absPath(t, repo.Path)), false)
			runner := newTestRunner(t, sess)
			_, err := runner.Run(context.Background(), request(t, repo, CapabilityIntent, tc.payload))
			if contract.KindOf(err) != contract.FailureInvalidInput {
				t.Fatalf("kind = %v, err=%v", contract.KindOf(err), err)
			}
		})
	}

	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("atenea", absPath(t, repo.Path)), false)
	fake.on(toolIntent, `{"truncated":false,"next_cursor":null,"coverage":{},"results":{"symbols":[
		{"name":"X","qualified_name":"X","kind":"class","repository":"elsewhere","file_path":"x.go","start_line":1,"end_line":1,"terms":1,"match":"lexical"}
	]}}`, false)
	_, err := newTestRunner(t, sess).Run(context.Background(), request(t, repo, CapabilityIntent, map[string]any{"intent": "x"}))
	if err == nil || contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("err = %v, want foreign repository refusal", err)
	}
}

func TestDependenciesResolvePositionAndPreserveWitnessOrder(t *testing.T) {
	repo := testRepo(t)
	fake, sess := newFakeKivgraph(t)
	fake.on(toolStatus, readyStatus("atenea", absPath(t, repo.Path)), false)
	fake.on(toolOutline, `{"truncated":false,"next_cursor":null,"coverage":{},"completeness":{"verdict":"COMPLETE"},
		"results":{"symbols":[{"name":"Run","qualified_name":"Runner.Run","kind":"method","start_line":40,"end_line":90}]}}`, false)
	fake.on(toolDependencies, `{"truncated":false,"next_cursor":null,"coverage":{"exact":2},"completeness":{"verdict":"COMPLETE"},
		"results":{"root_repository":"atenea","reached":2,"deepest_depth":2,"traversal_truncated":false,
		"witness_to":"Store.Save","witness_hops":2,"nodes":[
		{"name":"Validate","qualified_name":"Request.Validate","kind":"method","depth":1,"repository":"atenea","file_path":"internal/core/request.go","start_line":10,"end_line":20,"reached_from":"Runner.Run","via_kind":"CALLS","via_confidence":"EXACT","via_provenance":"parser"},
		{"name":"Save","qualified_name":"Store.Save","kind":"method","depth":2,"repository":"atenea","file_path":"internal/store/store.go","start_line":30,"end_line":44,"reached_from":"Request.Validate","via_kind":"CALLS","via_confidence":"EXACT","via_provenance":"parser"}
		]}}`, false)
	runner := newTestRunner(t, sess)
	out, err := runner.Run(context.Background(), request(t, repo, CapabilityDependencies, map[string]any{
		"file": "internal/core/run.go", "line": 50, "column": 3, "to": "Store.Save", "to_path": "internal/store/store.go", "depth": 5,
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	asked := fake.callsTo(toolDependencies)
	if len(asked) != 1 || asked[0]["qualified_name"] != "Runner.Run" || asked[0]["repository"] != "atenea" || asked[0]["view"] != "full" {
		t.Fatalf("trace args = %#v", asked)
	}
	rows := out.Result["dependencies"].([]any)
	if rows[0].(map[string]any)["qualified_name"] != "Request.Validate" || rows[1].(map[string]any)["qualified_name"] != "Store.Save" {
		t.Fatalf("witness order = %#v", rows)
	}
	if out.Result["witness_hops"] != 2 || !hasNote(out, "COMPLETE") {
		t.Fatalf("outcome = %#v", out)
	}
}

func TestQueryMetadataFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		required      bool
	}{
		{"missing verdict", `{"truncated":false,"next_cursor":null,"coverage":{}}`, true},
		{"unknown verdict", `{"truncated":false,"next_cursor":null,"coverage":{},"completeness":{"verdict":"MAYBE"}}`, true},
		{"cursor without truncation", `{"truncated":false,"next_cursor":"next","coverage":{},"completeness":{"verdict":"COMPLETE"}}`, true},
		{"truncation without cursor", `{"truncated":true,"next_cursor":null,"coverage":{},"completeness":{"verdict":"COMPLETE"}}`, true},
		{"empty lower bound", `{"truncated":false,"next_cursor":null,"coverage":{},"completeness":{"verdict":"LOWER_BOUND"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := queryMetadata("probe", tc.payload, tc.required); err == nil {
				t.Fatal("expected refusal")
			}
		})
	}
	notes, err := queryMetadata("probe", `{"truncated":false,"next_cursor":null,"coverage":{},"completeness":{"verdict":"LOWER_BOUND","blind_spots":[{"reason":"UNRESOLVED","file_path":"a.go"}],"fallback":{"pattern":"Foo","paths":["a.go"]}}}`, true)
	if err != nil || len(notes) == 0 || !strings.Contains(notes[0], "LOWER_BOUND") || !strings.Contains(notes[0], "Foo") {
		t.Fatalf("notes=%v err=%v", notes, err)
	}
}
