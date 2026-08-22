package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
		"sleep " + strconv.FormatFloat(delay.Seconds(), 'f', -1, 64) + "\n" +
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

func TestCodexRunnerIdentitySurfaceAndCapabilities(t *testing.T) {
	runner := newRunner(t, fakeCodex(t, "", 0, 0, ""), 10*time.Second)
	if runner.ID() != "codex" || runner.Surface() == "" {
		t.Fatalf("identity surface = %q", runner.Surface())
	}
	if got := runner.Capabilities(); len(got) != 1 || got[0] != CodeSearch {
		t.Fatalf("capabilities = %v", got)
	}
	if got := runner.Implementations(); len(got) != 1 || got[0] != "codex.search" {
		t.Fatalf("implementations = %v", got)
	}
}

func TestCodexEventAndPayloadHelpers(t *testing.T) {
	stream, err := parseEvents(`{"type":"error","error":{"message":"nested"}}
{"type":"turn.completed","total_cost_usd":0.125,"usage":{"input_tokens":2,"output_tokens":3}}
{"type":"item.completed","item":{"type":"agent_message","text":"answer"}}`)
	if err != nil || stream.Message != "answer" || stream.ErrorText != " nested" ||
		!stream.CostSeen || stream.CostUSD != 0.125 || stream.Usage.total() != 5 {
		t.Fatalf("stream = %+v, err=%v", stream, err)
	}
	if _, err := parseEvents("not json\n"); contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("invalid stream kind = %v", contract.KindOf(err))
	}
	if _, err := parseEvents("\n"); contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("empty stream kind = %v", contract.KindOf(err))
	}
	if got, ok := costFromJSON(json.RawMessage("null")); got != 0 || ok {
		t.Fatalf("null cost = %v, %v", got, ok)
	}
	if got, ok := costFromJSON(json.RawMessage(`"not-a-number"`)); got != 0 || ok {
		t.Fatalf("invalid cost = %v, %v", got, ok)
	}
	if !wantedType("src/main.TS", []string{"ts"}) || wantedType("README", []string{"md"}) {
		t.Fatal("wantedType did not normalize extensions")
	}
	if got, ok := intAt(map[string]any{"n": int64(3)}, "n"); !ok || got != 3 {
		t.Fatalf("intAt int64 = %v, %v", got, ok)
	}
	if got, ok := positive(float64(4)); !ok || got != 4 {
		t.Fatalf("positive float = %v, %v", got, ok)
	}
	if _, ok := positive(0); ok {
		t.Fatal("zero was accepted as positive")
	}
	if got := oneLine("  one\n two  "); got != "one two" {
		t.Fatalf("oneLine = %q", got)
	}
}

func TestCodexSourceSelectionAndValidation(t *testing.T) {
	binary := fakeCodex(t, "", 0, 0, "")
	runner, err := New(Options{
		Source: "terminal", TerminalBinary: binary, AppBinary: "/missing/codex",
		Implementations: []string{"codex.search"},
	})
	if err != nil {
		t.Fatalf("source runner: %v", err)
	}
	if !strings.HasPrefix(runner.Surface(), "terminal:") {
		t.Fatalf("source surface = %q", runner.Surface())
	}
	if _, err := New(Options{Source: "unknown"}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("unknown source kind = %v", contract.KindOf(err))
	}
	if _, err := New(Options{Sensitive: []string{"["}}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("bad sensitive pattern kind = %v", contract.KindOf(err))
	}
	if _, err := New(Options{Timeout: -time.Second}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("negative timeout kind = %v", contract.KindOf(err))
	}
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

func TestCodexStopsWhenAnIncrementalCostEventExceedsTheGrant(t *testing.T) {
	root := t.TempDir()
	body := "{\"type\":\"turn.completed\",\"total_cost_usd\":0.26}"
	runner := newRunner(t, fakeCodex(t, body, 0, time.Second, ""), 10*time.Second)
	ctx := contract.WithCostObserver(context.Background(), func(update contract.CostUpdate) bool {
		return update.SpentUSD <= 0.25
	})
	_, err := runner.Run(ctx, request(t, root, map[string]any{"query": "Firebase"}))
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied: %v", contract.KindOf(err), err)
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
	// Keep enough margin for macOS process-group startup; the fake still sleeps
	// for a full second, so this remains an unambiguous timeout test.
	runner := newRunner(t, fakeCodex(t, "", 0, time.Second, ""), 100*time.Millisecond)
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
	if items["additionalProperties"] != false {
		t.Fatalf("nested additionalProperties = %v, want false", items["additionalProperties"])
	}
}
