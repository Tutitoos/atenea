package opencode

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

func executable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatalf("write fake opencode: %v", err)
	}
	return path
}

func fixtureExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture path: %v", err)
	}
	return executable(t, "cat "+shellQuote(path))
}

func TestRunParsesCompletedJSONStream(t *testing.T) {
	binary := fixtureExecutable(t, "completed.jsonl")
	runner, err := New(Options{Binary: binary, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	answer, err := runner.Run(context.Background(), Request{
		Model:  "anthropic/sonnet",
		Prompt: "answer",
		Schema: map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(answer.Structured) != `{"summary":"done","findings":"found"}` {
		t.Errorf("structured = %s", answer.Structured)
	}
	if answer.Spent.InputTokens != 10 || answer.Spent.OutputTokens != 4 ||
		answer.Spent.CacheReadTokens != 20 || answer.Spent.CacheWriteTokens != 3 {
		t.Errorf("usage = %+v", answer.Spent)
	}
	if answer.Spent.USD == nil || *answer.Spent.USD != 0.12 {
		t.Errorf("cost = %+v", answer.Spent.USD)
	}
}

func TestRunEnforcesTheRequestedStructuredSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":      map[string]any{"type": "string"},
			"completeness": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		},
		"required":             []any{"summary", "completeness"},
		"additionalProperties": false,
	}
	tests := []struct {
		name   string
		answer string
		want   string
	}{
		{name: "missing required", answer: `{"summary":"done"}`, want: "completeness is required"},
		{name: "undeclared property", answer: `{"summary":"done","completeness":1,"extra":true}`, want: "extra is not declared"},
		{name: "number outside bounds", answer: `{"summary":"done","completeness":2}`, want: "at most 1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encodedText, err := json.Marshal(test.answer)
			if err != nil {
				t.Fatalf("encode answer: %v", err)
			}
			textEvent := `{"type":"text","part":{"id":"text-1","type":"text","text":` + string(encodedText) + `,"time":{"end":2}}}`
			finishEvent := `{"type":"step_finish","part":{"type":"step-finish"}}`
			binary := executable(t, "printf '%s\\n' "+shellQuote(textEvent)+"\nprintf '%s\\n' "+shellQuote(finishEvent))
			runner, err := New(Options{Binary: binary, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			answer, err := runner.Run(context.Background(), Request{Prompt: "answer", Schema: schema})
			if contract.KindOf(err) != contract.FailureInvalidInput || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run answer = %+v, error = %v, want invalid_input containing %q", answer, err, test.want)
			}
		})
	}
}

func TestRunRefusesAStreamWithoutTerminalStep(t *testing.T) {
	binary := fixtureExecutable(t, "incomplete.jsonl")
	runner, err := New(Options{Binary: binary, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(context.Background(), Request{Prompt: "answer"})
	if contract.KindOf(err) != contract.FailureUnavailable || !strings.Contains(err.Error(), "step_finish") {
		t.Fatalf("Run error = %v, want unavailable terminal-event error", err)
	}
}

func TestRunMapsPermissionErrors(t *testing.T) {
	binary := executable(t, `
echo '{"type":"error","error":{"name":"PermissionDenied","data":{"message":"permission denied"}}}'
exit 1
`)
	runner, err := New(Options{Binary: binary, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(context.Background(), Request{Prompt: "answer"})
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("Run error kind = %v, want permission_denied: %v", contract.KindOf(err), err)
	}
}

func TestRunMapsNestedSessionErrors(t *testing.T) {
	binary := executable(t, "cat "+shellQuote(filepath.Join("testdata", "session-error.jsonl"))+"\nexit 1")
	runner, err := New(Options{Binary: binary, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(context.Background(), Request{Prompt: "answer"})
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("Run error kind = %v, want permission_denied: %v", contract.KindOf(err), err)
	}
}

func TestRunRefusesACompletedEventWithoutAPart(t *testing.T) {
	binary := executable(t, `
echo '{"type":"step_finish","part":null}'
`)
	runner, err := New(Options{Binary: binary, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(context.Background(), Request{Prompt: "answer"})
	if contract.KindOf(err) != contract.FailureUnavailable || !strings.Contains(err.Error(), "missing its part") {
		t.Fatalf("Run error = %v, want unavailable missing-part error", err)
	}
}

func TestRunUsesSafeHeadlessArguments(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("ATENEA_OPENCODE_TEST_ARGS", argsFile)
	binary := executable(t, `
printf '%s\n' "$@" > "$ATENEA_OPENCODE_TEST_ARGS"
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"answer","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish"}}
JSON
`)
	runner, err := New(Options{Binary: binary, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := runner.Run(context.Background(), Request{Model: "provider/model", Prompt: "answer"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	got := string(args)
	for _, want := range []string{"run\n", "--format\njson\n", "--pure\n", "--model\nprovider/model\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("arguments %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "--auto") {
		t.Fatalf("unsafe auto-approval appeared in arguments: %q", got)
	}
}

func TestRunStopsAfterAnObservedAllowance(t *testing.T) {
	binary := executable(t, `
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"answer","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish","tokens":{"input":100,"output":10,"cache":{"read":0,"write":0}},"cost":0.01}}
JSON
sleep 30
`)
	runner, err := New(Options{Binary: binary, Timeout: time.Minute})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	started := time.Now()
	answer, err := runner.Run(context.Background(), Request{Prompt: "answer", ReadTokens: 50})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if answer.Text != "answer" {
		t.Errorf("text = %q", answer.Text)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("observed stop took %s", elapsed)
	}
}

func TestRunStopsWhenItsContextIsCanceled(t *testing.T) {
	binary := executable(t, `
sleep 30
`)
	runner, err := New(Options{Binary: binary, Timeout: time.Minute})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = runner.Run(ctx, Request{Prompt: "answer"})
	if contract.KindOf(err) != contract.FailureTimeout {
		t.Fatalf("Run error kind = %v, want timeout: %v", contract.KindOf(err), err)
	}
}

func TestOpenCodeConfigTranslatesClaudeMCPShape(t *testing.T) {
	got, err := openCodeConfig(`{"mcpServers":{"atenea":{"command":"/bin/atenea","args":["mcp"],"env":{"X":"Y"}},"docs":{"url":"http://127.0.0.1:9/mcp"}}}`)
	if err != nil {
		t.Fatalf("openCodeConfig: %v", err)
	}
	if !strings.Contains(got, `"type":"local"`) || !strings.Contains(got, `"command":["/bin/atenea","mcp"]`) ||
		!strings.Contains(got, `"type":"remote"`) || !strings.Contains(got, `"url":"http://127.0.0.1:9/mcp"`) {
		t.Errorf("translated config = %s", got)
	}
}

func TestLiveOpenCodeSmoke(t *testing.T) {
	if os.Getenv("ATENEA_OPENCODE_SMOKE") != "1" {
		t.Skip("set ATENEA_OPENCODE_SMOKE=1 to run a real provider smoke test")
	}
	model := strings.TrimSpace(os.Getenv("ATENEA_OPENCODE_MODEL"))
	if model == "" {
		t.Fatal("ATENEA_OPENCODE_MODEL is required when the real smoke test is enabled")
	}
	dir := strings.TrimSpace(os.Getenv("ATENEA_OPENCODE_SMOKE_DIR"))
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			t.Fatalf("working directory: %v", err)
		}
	}
	runner, err := New(Options{Binary: os.Getenv("ATENEA_OPENCODE_BINARY"), Timeout: 90 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	answer, err := runner.Run(t.Context(), Request{
		Model:  model,
		Dir:    dir,
		Prompt: "Return exactly this JSON object and nothing else: {\"ok\":true}.",
		Schema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"ok": map[string]any{"type": "boolean"}},
			"required":             []any{"ok"},
			"additionalProperties": false,
		},
	})
	if err != nil {
		t.Fatalf("OpenCode smoke failed: %v", err)
	}
	if answer.Text == "" || len(answer.Structured) == 0 {
		t.Fatalf("OpenCode smoke returned no structured answer: %+v", answer)
	}
	t.Logf("OpenCode smoke passed: model=%s version=%s input_tokens=%d output_tokens=%d", model,
		runner.Version(t.Context()), answer.Spent.InputTokens, answer.Spent.OutputTokens)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
