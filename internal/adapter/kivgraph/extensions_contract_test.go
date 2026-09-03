package kivgraph_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/kivgraph"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type extensionSession struct {
	root    string
	answers map[string]string
	calls   []string
}

func (s *extensionSession) Call(_ context.Context, tool string, args map[string]any) (string, error) {
	s.calls = append(s.calls, tool)
	if tool == "graph_status" {
		body, _ := json.Marshal(map[string]any{"results": map[string]any{"status": "ready", "snapshot_id": 1, "symbols": 2, "edges": 1, "files": 1, "repositories": 1, "repository_freshness": []any{map[string]any{"name": "test", "path": s.root}}}})
		return string(body), nil
	}
	text, ok := s.answers[tool]
	if !ok {
		return "", fmt.Errorf("unexpected tool %s", tool)
	}
	return text, nil
}

func extensionRequest(t *testing.T, root, capID string, payload map[string]any) contract.RunRequest {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	req := contract.RunRequest{Repository: contract.NewRepository("test", root, nil, contract.ScaleSmall, contract.VCSUnspecified, nil), Payload: payload, Permission: contract.Permission{Task: "test", Effects: []contract.Effect{contract.EffectRead}}}
	for _, cap := range cfg.Capabilities {
		if cap.ID == capID {
			req.Capability = cap
		}
	}
	for _, impl := range cfg.Implementations {
		if impl.Capability == capID && impl.Provider == "kivgraph" {
			req.Implementation = impl
		}
	}
	if req.Implementation.ID == "" {
		t.Fatalf("no Kivgraph implementation for %s", capID)
	}
	return req
}

func TestNewReadCapabilitiesHonorShippedContracts(t *testing.T) {
	symbol := `{"name":"Run","qualified_name":"Runner.Run","kind":"method","repository":"test","file_path":"main.go","start_line":1,"end_line":2,"depth":1,"match":"lexical"}`
	envelope := func(results string) string {
		return `{"snapshot_id":1,"coverage":{"exact":1},"completeness":{"verdict":"COMPLETE"},"truncated":false,"results":` + results + `}`
	}
	for _, tc := range []struct {
		id      string
		payload map[string]any
		answers map[string]string
		result  string
	}{
		{"symbol.search", map[string]any{"query": "Run"}, map[string]string{"find_symbol": envelope("[" + symbol + "]")}, "matches"},
		{"symbol.search", map[string]any{"query": "Run"}, map[string]string{"find_symbol": `{"snapshot_id":1,"results":[` + symbol + `]}`}, "matches"},
		{"symbol.source", map[string]any{"symbols": []any{map[string]any{"path": "main.go", "qualified_name": "Runner.Run"}}}, map[string]string{"get_source": "test:main.go:1-2\nfunc Run() {}"}, "source"},
		{"graph.repositories", map[string]any{}, map[string]string{"list_repositories": envelope(`[{"name":"test","path":"/test","moved":false}]`)}, "repositories"},
		{"symbol.impact", map[string]any{"file": "main.go", "line": 1, "column": 1}, map[string]string{"get_file_outline": envelope(`{"symbols":[` + symbol + `]}`), "get_blast_radius": envelope(`{"symbols":[` + strings.ReplaceAll(symbol, `"repository":"test"`, `"repository":"consumer"`) + `],"traversal_truncated":false}`)}, "symbols"},
		{"code.context", map[string]any{"task": "find runner"}, map[string]string{"find_by_intent": envelope(`{"symbols":[` + symbol + `]}`), "get_symbol": envelope(symbol)}, "symbols"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			root := t.TempDir()
			sess := &extensionSession{root: root, answers: tc.answers}
			runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
			if err != nil {
				t.Fatal(err)
			}
			req := extensionRequest(t, root, tc.id, tc.payload)
			out, err := runner.Run(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := out.Result[tc.result]; !ok {
				t.Fatalf("missing %s: %#v", tc.result, out)
			}
			if err := req.Capability.ValidateOutput(out.Result); err != nil {
				t.Fatal(err)
			}
			if len(out.Evidence) == 0 {
				t.Fatal("missing graph evidence")
			}
			if tc.id == "symbol.impact" && out.Result["symbols"].([]any)[0].(map[string]any)["repository"] != "consumer" {
				t.Fatal("cross-repository identity lost")
			}
		})
	}
}

func TestReadExtensionsRejectInvalidBoundsBeforeQuery(t *testing.T) {
	for _, tc := range []struct {
		id      string
		payload map[string]any
	}{
		{"symbol.search", map[string]any{"query": "x", "limit": 201}},
		{"symbol.source", map[string]any{"symbols": []any{}}},
		{"symbol.source", map[string]any{"symbols": []any{map[string]any{"path": "../escape", "qualified_name": "X"}}}},
		{"symbol.impact", map[string]any{"file": "../escape", "line": 1, "column": 1}},
		{"graph.repositories", map[string]any{"limit": 501}},
		{"code.context", map[string]any{"task": "x", "limit": 21}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			root := t.TempDir()
			sess := &extensionSession{root: root}
			runner, err := kivgraph.New(kivgraph.Options{Session: func(context.Context) (kivgraph.Session, error) { return sess, nil }})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), extensionRequest(t, root, tc.id, tc.payload))
			if contract.KindOf(err) != contract.FailureInvalidInput {
				t.Fatalf("expected input refusal, got %v", err)
			}
			if len(sess.calls) != 1 {
				t.Fatalf("query was dispatched: %v", sess.calls)
			}
		})
	}
}
