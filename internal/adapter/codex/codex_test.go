package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func codeSearch() contract.Capability {
	return contract.Capability{
		ID: "code.search", Version: contract.Version{Major: 1},
		Summary: "Find text in a repository.",
		Effects: []contract.Effect{contract.EffectRead, contract.EffectProcess},
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

func request(t *testing.T, root string, payload map[string]any) contract.RunRequest {
	t.Helper()
	return contract.RunRequest{
		Capability:     codeSearch(),
		Implementation: contract.Implementation{ID: "codex.search", Provider: "codex", Capability: "code.search"},
		Repository:     contract.NewRepository("repo", root, []string{"typescript", "dart"}, contract.ScaleSmall, contract.VCSUnspecified, nil),
		Payload:        payload,
		Permission: contract.Permission{
			Task: "search", Effects: []contract.Effect{contract.EffectRead, contract.EffectProcess}, BudgetUSD: 0.25,
		},
	}
}

func fakeCodex(t *testing.T, stdout string, exit int, delay time.Duration, stderr string) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "codex")
	quoted := "'" + strings.ReplaceAll(stdout, "'", "'\\''") + "'"
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 'codex fake 1.0'; exit 0; fi\n" +
		"cat >/dev/null\n" +
		"sleep " + delay.String() + "\n" +
		"printf '%s\\n' " + quoted + "\n" +
		"printf '%s\\n' '" + strings.ReplaceAll(stderr, "'", "'\\''") + "' >&2\n" +
		"exit " + formatInt(exit) + "\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake Codex: %v", err)
	}
	return binary
}

func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

func eventJSON(t *testing.T, text string) string {
	t.Helper()
	item, err := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{
		"type": "agent_message", "text": text,
	}})
	if err != nil {
		t.Fatal(err)
	}
	done := `{"type":"turn.completed","usage":{"input_tokens":12,"output_tokens":4}}`
	return string(item) + "\n" + done
}

func newRunner(t *testing.T, binary string, timeout time.Duration) *Runner {
	t.Helper()
	runner, err := New(Options{Binary: binary, Implementations: []string{"codex.search"}, Timeout: timeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner
}

func TestCodexValidJSONLBecomesCodeSearch(t *testing.T) {
	root := t.TempDir()
	body := eventJSON(t, `{"matches":[{"path":"src/main.ts","line":4,"column":7,"snippet":"Firebase"},{"path":"apps/app/lib/main.dart","line":9,"column":3,"snippet":"Firebase"}]}`)
	runner := newRunner(t, fakeCodex(t, body, 0, 0, ""), 10*time.Second)
	out, err := runner.Run(context.Background(), request(t, root, map[string]any{"query": "Firebase"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Verdict != contract.VerdictOK || out.Spent.Tokens != 16 {
		t.Fatalf("outcome = %+v", out)
	}
	matches := out.Result["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("matches = %v, want TypeScript and Dart results", matches)
	}
	if _, leaked := matches[0].(map[string]any)["snippet"]; leaked {
		t.Fatal("Codex result returned file content in snippet")
	}
}

func TestCodexInvalidJSONIsRecoverableWithoutRawOutput(t *testing.T) {
	runner := newRunner(t, fakeCodex(t, eventJSON(t, "not json"), 0, 0, "secret-token"), 10*time.Second)
	_, err := runner.Run(context.Background(), request(t, t.TempDir(), map[string]any{"query": "x"}))
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", contract.KindOf(err))
	}
	if strings.Contains(err.Error(), "secret-token") || contract.RawOf(err) != "" {
		t.Fatalf("provider output leaked through error: %v", err)
	}
}

func TestCodexUnauthenticatedProcessIsRecoverable(t *testing.T) {
	runner := newRunner(t, fakeCodex(t, "", 1, 0, "not logged in; oauth required"), 10*time.Second)
	_, err := runner.Run(context.Background(), request(t, t.TempDir(), map[string]any{"query": "x"}))
	if contract.KindOf(err) != contract.FailureUnavailable || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("err = %v, want unavailable authentication failure", err)
	}
	if contract.RawOf(err) != "" {
		t.Fatal("authentication stderr was retained as raw provider output")
	}
}

func TestCodexTimeoutIsRecoverable(t *testing.T) {
	runner := newRunner(t, fakeCodex(t, "", 0, time.Second, ""), 20*time.Millisecond)
	_, err := runner.Run(context.Background(), request(t, t.TempDir(), map[string]any{"query": "x"}))
	if contract.KindOf(err) != contract.FailureTimeout {
		t.Fatalf("kind = %v, want timeout: %v", contract.KindOf(err), err)
	}
}

func TestCodexProcessErrorIsRecoverable(t *testing.T) {
	runner := newRunner(t, fakeCodex(t, "", 1, 0, "fatal child process"), 10*time.Second)
	_, err := runner.Run(context.Background(), request(t, t.TempDir(), map[string]any{"query": "x"}))
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", contract.KindOf(err))
	}
}

func TestCodexDropsResultsOutsideRepositoryAndSensitiveFiles(t *testing.T) {
	root := t.TempDir()
	body := eventJSON(t, `{"matches":[{"path":"inside.ts","line":1,"column":1},{"path":"../outside.ts","line":1,"column":1},{"path":".env","line":1,"column":1}]}`)
	runner, err := New(Options{Binary: fakeCodex(t, body, 0, 0, ""), Implementations: []string{"codex.search"}, Sensitive: []string{".env"}, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.Run(context.Background(), request(t, root, map[string]any{"query": "x"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	matches := out.Result["matches"].([]any)
	if len(matches) != 1 || matches[0].(map[string]any)["path"] != "inside.ts" {
		t.Fatalf("matches = %v, want only repository-local non-sensitive result", matches)
	}
}

func TestCodexRejectsScopeOutsideRepositoryBeforeProcess(t *testing.T) {
	runner := newRunner(t, fakeCodex(t, "", 0, 0, ""), 10*time.Second)
	_, err := runner.Run(context.Background(), request(t, t.TempDir(), map[string]any{
		"query": "x", "scope": []any{"../outside"},
	}))
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", contract.KindOf(err))
	}
}

func TestCodexUsesStrictSchemaRequiredByCodex(t *testing.T) {
	schema, err := strictOutputSchema(codeSearch())
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatal(err)
	}
	items := decoded["properties"].(map[string]any)["matches"].(map[string]any)["items"].(map[string]any)
	required := items["required"].([]any)
	if len(required) != 4 {
		t.Fatalf("required = %v, want all Codex object properties", required)
	}
}
