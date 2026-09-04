package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// fixtureTimeout leaves room for process startup under -race and concurrent
// package execution. Production timeouts are tested separately; these fake
// clients should never fail because instrumentation delayed their first byte.
const fixtureTimeout = 15 * time.Second

func executable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 1.18.20; exit 0; fi\n"+body), 0o700); err != nil {
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
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
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

func TestSchemaAndErrorHelpersCoverProviderBoundaries(t *testing.T) {
	for _, value := range []any{json.Number("1.5"), float64(2), float32(3), int(4), int64(5)} {
		if _, err := schemaNumber(value); err != nil {
			t.Errorf("schemaNumber(%T): %v", value, err)
		}
	}
	if _, err := schemaNumber("nope"); err == nil {
		t.Fatal("schemaNumber accepted text")
	}
	if !schemaContains([]any{"a", map[string]any{"b": 1}}, map[string]any{"b": 1}) {
		t.Fatal("schemaContains missed a deep equal value")
	}
	if got := appendWithout([]string{"A=1", "B=2"}, "A", "3"); !slices.Equal(got, []string{"B=2", "A=3"}) {
		t.Fatalf("appendWithout = %v", got)
	}
	if got := firstToken("  first second"); got != "first" || firstToken(" ") != "" {
		t.Fatalf("firstToken = %q", got)
	}
	if got := rawError(nil, json.RawMessage(`{"error":{"data":{"message":"nested"}}}`), nil); got != "nested" {
		t.Fatalf("nested rawError = %q", got)
	}
	if got := rawError(nil, nil, []byte("fallback")); got != "fallback" {
		t.Fatalf("fallback rawError = %q", got)
	}
	for _, message := range []string{"rate limit", "unauthorized", "context window", "budget", "permission denied", "not found", "timeout", "other"} {
		if failureFor(message, nil) == nil {
			t.Fatalf("failureFor(%q) returned nil", message)
		}
	}
}

func TestOpenCodeSchemaAndPartHelpers(t *testing.T) {
	if got := schemaStrings([]string{"a"}); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("schemaStrings strings = %v", got)
	}
	if got := schemaStrings([]any{"a", 1, "b"}); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("schemaStrings any = %v", got)
	}
	if len(schemaStrings(42)) != 0 {
		t.Fatal("schemaStrings accepted a scalar")
	}
	if got, ok := schemaNumberField(float64(2)); !ok || got != 2 {
		t.Fatalf("schemaNumberField = %v, %v", got, ok)
	}
	if _, ok := schemaNumberField("no"); ok {
		t.Fatal("schemaNumberField accepted text")
	}
	if _, err := decodePart(json.RawMessage(`{"type":"text","text":"ok"}`), "text", nil); err != nil {
		t.Fatalf("decodePart valid: %v", err)
	}
	if _, err := decodePart(nil, "text", []byte("event")); contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("decodePart missing kind = %v", contract.KindOf(err))
	}
	if _, err := decodePart(json.RawMessage(`{"type":"wrong"}`), "text", nil); contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("decodePart wrong type kind = %v", contract.KindOf(err))
	}
}

func TestRunRecordsToolUseEventsAsEvidence(t *testing.T) {
	binary := executable(t, `
cat <<'JSON'
{"type":"tool_use","part":{"type":"tool","tool":"atenea_code_search"}}
{"type":"text","part":{"id":"text-1","type":"text","text":"answer","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish"}}
JSON
`)
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	answer, err := runner.Run(context.Background(), Request{Prompt: "answer"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Equal(answer.ToolCalls, []string{"atenea_code_search"}) {
		t.Fatalf("tool calls = %v, want [atenea_code_search]", answer.ToolCalls)
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
			runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
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

func TestRunRecoversAValidJSONObjectAfterLeadingNarration(t *testing.T) {
	binary := executable(t, `
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"I inspected the repository. {\"summary\":\"done\"}","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish"}}
JSON
`)
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	answer, err := runner.Run(context.Background(), Request{
		Prompt: "answer", Schema: map[string]any{"type": "object", "required": []any{"summary"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(answer.Structured) != `{"summary":"done"}` {
		t.Fatalf("structured = %s", answer.Structured)
	}
}

func TestRunRefusesTrailingStructuredJSON(t *testing.T) {
	binary := executable(t, `
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"{\"ok\":true} trailing","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish"}}
JSON
`)
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(context.Background(), Request{
		Prompt: "answer",
		Schema: map[string]any{"type": "object"},
	})
	if contract.KindOf(err) != contract.FailureInvalidInput || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("Run error = %v, want invalid_input trailing-data error", err)
	}
}

// Running out of the call's own ceiling is not a refusal by policy -- there
// is no larger pool it was cut from -- so it bins as unavailable, exactly as
// the Claude backend bins the identical fact. This asserted permission_denied
// until 2026-08-25, which made the verdict on one budget exhaustion depend on
// which backend was configured.
func TestARunOverItsOwnCeilingIsUnavailableAsOnTheOtherBackend(t *testing.T) {
	binary := executable(t, `
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"answer","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish","cost":0.26}}
JSON
`)
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	answer, err := runner.Run(context.Background(), Request{Prompt: "answer", BudgetUSD: 0.25})
	if contract.KindOf(err) != contract.FailureUnavailable || answer.Spent.USD == nil || *answer.Spent.USD != 0.26 {
		t.Fatalf("Run answer = %+v, error = %v, want an unavailable turn carrying the observed cost", answer, err)
	}
}

func TestRunKillsTheProcessAfterAnObservedCostOverrun(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "after-limit")
	binary := executable(t, fmt.Sprintf(`
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"answer","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish","cost":0.26}}
JSON
sleep 30
printf reached > %s
`, shellQuote(marker)))
	runner, err := New(Options{Binary: binary, Timeout: time.Minute})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	started := time.Now()
	answer, err := runner.Run(context.Background(), Request{Prompt: "answer", BudgetUSD: 0.25})
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("Run error = %v, want unavailable", err)
	}
	if answer.Spent.USD == nil || *answer.Spent.USD != 0.26 {
		t.Fatalf("spent = %+v, want observed cost 0.26", answer.Spent)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("overrun stop took %s", elapsed)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("process continued after limit; marker stat error = %v", statErr)
	}
}

func TestRunAcceptsAnObservedCostWithinTheBudget(t *testing.T) {
	binary := executable(t, `
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"answer","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish","cost":0.25}}
JSON
`)
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := runner.Run(context.Background(), Request{Prompt: "answer", BudgetUSD: 0.25}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunRejectsNonFiniteBudgets(t *testing.T) {
	binary := executable(t, `exit 99`)
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, budget := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := runner.Run(context.Background(), Request{Prompt: "answer", BudgetUSD: budget})
		if contract.KindOf(err) != contract.FailureInvalidInput {
			t.Errorf("budget %v: kind = %v, want invalid_input", budget, contract.KindOf(err))
		}
	}
}

func TestRunRefusesAStreamWithoutTerminalStep(t *testing.T) {
	binary := fixtureExecutable(t, "incomplete.jsonl")
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
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
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
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
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(context.Background(), Request{Prompt: "answer"})
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("Run error kind = %v, want permission_denied: %v", contract.KindOf(err), err)
	}
}

func TestRunMapsProviderBoundaryErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
		kind contract.FailureKind
	}{
		{name: "rate limit", text: "429 too many requests", kind: contract.FailureUnavailable},
		{name: "authentication", text: "unauthorized: API key required", kind: contract.FailureUnavailable},
		{name: "context", text: "context length exceeded", kind: contract.FailureInvalidInput},
		{name: "budget", text: "budget exhausted", kind: contract.FailurePermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := executable(t, "echo '"+test.text+"' >&2\nprintf '%s\\n' "+shellQuote(`{"type":"error","error":{"data":{"message":"`+test.text+`"}}}`)+"\nexit 1")
			runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = runner.Run(context.Background(), Request{Prompt: "answer"})
			if got := contract.KindOf(err); got != test.kind {
				t.Fatalf("kind = %v, want %v: %v", got, test.kind, err)
			}
		})
	}
}

func TestRunRefusesACompletedEventWithoutAPart(t *testing.T) {
	binary := executable(t, `
echo '{"type":"step_finish","part":null}'
`)
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
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
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
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
	for _, want := range []string{"run\n", "--format\njson\n", "--model\nprovider/model\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("arguments %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "--pure\n") {
		t.Fatalf("--pure must not be passed to authenticated OpenCode sessions: %q", got)
	}
	if strings.Contains(got, "--auto") {
		t.Fatalf("unsafe auto-approval appeared in arguments: %q", got)
	}
}

func TestRunOmitsPureWhenInjectingMCPConfig(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("ATENEA_OPENCODE_TEST_ARGS", argsFile)
	binary := executable(t, `
printf '%s\n' "$@" > "$ATENEA_OPENCODE_TEST_ARGS"
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"answer","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish"}}
JSON
`)
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := runner.Run(context.Background(), Request{
		Model: "provider/model", Prompt: "answer", Tools: `{"mcpServers":{}}`,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if strings.Contains(string(args), "--pure\n") {
		t.Fatalf("--pure must be omitted when MCP config is injected: %q", args)
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
	// The child is deliberately held for 30 seconds. Ten seconds leaves a
	// generous margin for the race detector and loaded CI hosts while still
	// proving the observed allowance stops the process well before that hold.
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("observed stop took %s", elapsed)
	}
}

func TestRunRejectsAnAnswerAboveTheDeclaredTokenLimit(t *testing.T) {
	binary := executable(t, `
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"answer","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish","tokens":{"input":80,"output":30,"cache":{"read":0,"write":0}},"cost":0.01}}
JSON
`)
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	answer, err := runner.Run(context.Background(), Request{Prompt: "answer", MaxTokens: 100})
	if err == nil || contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("Run error = %v, want permission_denied", err)
	}
	if answer.Spent.Tokens() != 110 {
		t.Fatalf("spent tokens = %d, want 110", answer.Spent.Tokens())
	}
}

func TestRunDoesNotStopAtAnIntermediateToolStep(t *testing.T) {
	binary := executable(t, `
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"I am reading.","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":100,"output":10}}}
{"type":"text","part":{"id":"text-2","type":"text","text":"{\"summary\":\"done\"}","time":{"end":3}}}
{"type":"step_finish","part":{"type":"step-finish","reason":"stop","tokens":{"input":10,"output":5}}}
JSON
`)
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The allowance is deliberately far above what these two steps weigh
	// (185 input-equivalent tokens). It used to be 1, which only passed
	// because reached() waited for `finished` and so never fired mid-turn:
	// an allowance of one token that a hundred-token step does not trip is
	// the defect, not the guarantee. What this test is about is the `reason`
	// field -- an intermediate tool step is not the end of the turn.
	answer, err := runner.Run(context.Background(), Request{
		Prompt: "answer", ReadTokens: 10_000,
		Schema: map[string]any{"type": "object", "required": []any{"summary"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(answer.Structured) != `{"summary":"done"}` {
		t.Fatalf("structured = %s", answer.Structured)
	}
}

// The read allowance protects the turn that calls tools, and that turn never
// sets `finished` while it is still working. Two intermediate steps that
// together weigh past the allowance have to stop the process and hand the
// session to the tool-free finalization pass, well before the
// maxOpenCodeToolSteps count that is the other, cruder boundary.
func TestAnAllowanceCrossedBetweenToolStepsFinalizesTheSession(t *testing.T) {
	binary := executable(t, `
resume=0
for arg in "$@"; do
  if [ "$arg" = "--session" ]; then resume=1; fi
done
if [ "$resume" -eq 1 ]; then
cat <<'JSON'
{"type":"text","sessionID":"ses-allowance","part":{"id":"text-final","type":"text","text":"{\"summary\":\"what the reading bought\"}","time":{"end":2}}}
{"type":"step_finish","sessionID":"ses-allowance","part":{"type":"step-finish","reason":"stop","tokens":{"input":20,"output":8}}}
JSON
else
cat <<'JSON'
{"type":"step_finish","sessionID":"ses-allowance","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":300,"output":10}}}
{"type":"step_finish","sessionID":"ses-allowance","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":300,"output":10}}}
JSON
sleep 30
fi
`)
	runner, err := New(Options{Binary: binary, Timeout: time.Minute})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	started := time.Now()
	// Each tool step weighs 350 input-equivalent tokens (300 input, 10
	// output at x5), so the allowance is crossed on the second one and not
	// on the first.
	answer, err := runner.Run(context.Background(), Request{
		Prompt: "answer", ReadTokens: 600,
		Schema: map[string]any{"type": "object", "required": []any{"summary"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(answer.Structured) != `{"summary":"what the reading bought"}` {
		t.Fatalf("structured = %s, want the finalization pass's answer", answer.Structured)
	}
	if answer.Passes != 2 {
		t.Fatalf("passes = %d, want the tool pass plus the finalization", answer.Passes)
	}
	// The child holds for 30 seconds after its second step. Ten leaves room
	// for the race detector and a loaded host while still proving the
	// allowance stopped it rather than the stream simply ending.
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("the allowance took %s to stop a turn it should have cut at the second step", elapsed)
	}
}

func TestRunFinalizesAStuckToolSession(t *testing.T) {
	binary := executable(t, `
resume=0
for arg in "$@"; do
  if [ "$arg" = "--session" ]; then resume=1; fi
done
if [ "$resume" -eq 1 ]; then
cat <<'JSON'
{"type":"text","sessionID":"ses-finalize","part":{"id":"text-final","type":"text","text":"{\"summary\":\"partial evidence\"}","time":{"end":2}}}
{"type":"step_finish","sessionID":"ses-finalize","part":{"type":"step-finish","reason":"stop","tokens":{"input":20,"output":8}}}
JSON
else
cat <<'JSON'
{"type":"step_finish","sessionID":"ses-finalize","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":20,"output":8}}}
{"type":"step_finish","sessionID":"ses-finalize","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":20,"output":8}}}
{"type":"step_finish","sessionID":"ses-finalize","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":20,"output":8}}}
{"type":"step_finish","sessionID":"ses-finalize","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":20,"output":8}}}
JSON
fi
`)
	runner, err := New(Options{Binary: binary, Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	answer, err := runner.Run(context.Background(), Request{
		Prompt: "answer", Schema: map[string]any{"type": "object", "required": []any{"summary"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(answer.Structured) != `{"summary":"partial evidence"}` {
		t.Fatalf("structured = %s", answer.Structured)
	}
	if answer.Passes != 2 {
		t.Fatalf("passes = %d, want initial plus finalization", answer.Passes)
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

// A stream that breaks partway through still billed for the steps it did
// deliver. Both of these paths returned an empty Answer, so a turn killed by
// one unreadable line was recorded as a turn that cost nothing -- while every
// other failing path in Run reports what the provider had already charged.
func TestABrokenStreamStillReportsWhatTheProviderCharged(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "an event this adapter cannot read",
			body: `
cat <<'JSON'
{"type":"step_finish","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":400,"output":20},"cost":0.07}}
{"type":"step_finish","part":"this is not a part object"}
JSON
`,
		},
		{
			name: "a line longer than the scanner will take",
			body: `
cat <<'JSON'
{"type":"step_finish","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":400,"output":20},"cost":0.07}}
JSON
head -c 9000000 /dev/zero | tr '\0' 'x'
printf '\n'
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, err := New(Options{Binary: executable(t, tc.body), Timeout: fixtureTimeout})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			answer, err := runner.Run(context.Background(), Request{Prompt: "answer"})
			if err == nil {
				t.Fatal("a broken stream was accepted")
			}
			if answer.Spent.InputTokens != 400 || answer.Spent.USD == nil || *answer.Spent.USD != 0.07 {
				t.Fatalf("spent = %+v, want the step the provider had already charged for", answer.Spent)
			}
		})
	}
}

// A request may be given more room than the runner's own default -- a plan
// turn is not a search turn -- and that larger deadline has to be the one the
// turn runs under and the one a timeout names. The runner used to re-wrap the
// call with its own ceiling, so the smaller of the two always won and the
// failure named a limit that had not expired.
func TestARequestTimeoutLongerThanTheRunnersIsTheOneThatApplies(t *testing.T) {
	binary := executable(t, `
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"answer","time":{"end":2}}}
JSON
sleep 1
cat <<'JSON'
{"type":"step_finish","part":{"type":"step-finish","reason":"stop","tokens":{"input":10,"output":2}}}
JSON
`)
	// A quarter second is less than the child takes to finish, so a runner
	// that kept its own ceiling would report a timeout of 250ms.
	runner, err := New(Options{Binary: binary, Timeout: 250 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	answer, err := runner.Run(context.Background(), Request{Prompt: "answer", Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if answer.Text != "answer" {
		t.Fatalf("text = %q, want the answer the longer deadline paid for", answer.Text)
	}
}

// The limit a timeout names has to be the one that expired, or whoever reads
// the report goes looking for a ceiling that never fired.
func TestATimeoutNamesTheDeadlineThatActuallyApplied(t *testing.T) {
	binary := executable(t, `
sleep 30
`)
	runner, err := New(Options{Binary: binary, Timeout: time.Minute})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(context.Background(), Request{Prompt: "answer", Timeout: 80 * time.Millisecond})
	if contract.KindOf(err) != contract.FailureTimeout {
		t.Fatalf("Run error kind = %v, want timeout: %v", contract.KindOf(err), err)
	}
	if !strings.Contains(err.Error(), "80ms") {
		t.Fatalf("Run error = %q, want the request's own 80ms deadline named", err)
	}
}

func TestARequestWithANegativeTimeoutIsRefused(t *testing.T) {
	runner, err := New(Options{Binary: executable(t, "exit 0"), Timeout: fixtureTimeout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = runner.Run(context.Background(), Request{Prompt: "answer", Timeout: -time.Second})
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("Run error kind = %v, want invalid_input: %v", contract.KindOf(err), err)
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

func TestLiveOpenCodeMatrix(t *testing.T) {
	if os.Getenv("ATENEA_OPENCODE_SMOKE") != "1" {
		t.Skip("set ATENEA_OPENCODE_SMOKE=1 to run real provider matrix")
	}
	models := splitNonEmpty(os.Getenv("ATENEA_OPENCODE_MODELS"))
	if len(models) == 0 {
		t.Fatal("ATENEA_OPENCODE_MODELS must contain at least one provider/model")
	}
	dir := strings.TrimSpace(os.Getenv("ATENEA_OPENCODE_SMOKE_DIR"))
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			t.Fatalf("working directory: %v", err)
		}
	}
	tools := strings.TrimSpace(os.Getenv("ATENEA_OPENCODE_MCP_CONFIG"))
	for _, model := range models {
		t.Run(strings.NewReplacer("/", "_", ":", "_").Replace(model), func(t *testing.T) {
			runner, err := New(Options{Binary: os.Getenv("ATENEA_OPENCODE_BINARY"), Timeout: 120 * time.Second})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			prompt := "Return exactly this JSON object and nothing else: {\"ok\":true}."
			if tools != "" {
				prompt = "Use the atenea MCP server. Call atenea_catalog_repositories, then return exactly this JSON object and nothing else: {\"mcp_used\":true}."
			}
			started := time.Now()
			var answer Answer
			var runErr error
			for attempt := 1; attempt <= 3; attempt++ {
				answer, runErr = runner.Run(t.Context(), Request{
					Model:  model,
					Dir:    dir,
					Prompt: prompt,
					Tools:  tools,
					Schema: matrixSchema(tools != ""),
				})
				if runErr == nil || contract.KindOf(runErr) != contract.FailureUnavailable ||
					!strings.Contains(runErr.Error(), "without a step_finish event") || attempt == 3 {
					break
				}
				t.Logf("retrying transient terminal-event failure (%d/3): %v", attempt, runErr)
			}
			err = runErr
			if err != nil {
				t.Fatalf("OpenCode matrix failed: %v (provider output: %q)", err, contract.RawOf(err))
			}
			if tools != "" && !hasAteneaToolCall(answer.ToolCalls) {
				t.Fatalf("MCP config did not produce an Atenea tool call: %v", answer.ToolCalls)
			}
			cost := "unmeasured"
			if answer.Spent.USD != nil {
				cost = fmt.Sprintf("%.6f", *answer.Spent.USD)
			}
			t.Logf("provider matrix passed: model=%s version=%s mcp=%t tool_calls=%v elapsed=%s input_tokens=%d output_tokens=%d cost_usd=%s",
				model, runner.Version(t.Context()), tools != "", answer.ToolCalls, time.Since(started).Round(time.Millisecond),
				answer.Spent.InputTokens, answer.Spent.OutputTokens, cost)
		})
	}
}

func matrixSchema(mcp bool) map[string]any {
	property := "ok"
	if mcp {
		property = "mcp_used"
	}
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{property: map[string]any{"type": "boolean"}},
		"required":             []any{property},
		"additionalProperties": false,
	}
}

func hasAteneaToolCall(calls []string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, "atenea_") {
			return true
		}
	}
	return false
}

func splitNonEmpty(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
