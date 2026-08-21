package opencode

import (
	"context"
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

func TestRunParsesCompletedJSONStream(t *testing.T) {
	binary := executable(t, `
cat <<'JSON'
{"type":"text","part":{"id":"text-1","type":"text","text":"{\"summary\":\"done\",\"findings\":\"found\"}","time":{"end":2}}}
{"type":"step_finish","part":{"type":"step-finish","tokens":{"input":10,"output":4,"reasoning":1,"cache":{"read":20,"write":3}},"cost":0.12}}
JSON
`)
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

func TestRunRefusesAStreamWithoutTerminalStep(t *testing.T) {
	binary := executable(t, `
echo '{"type":"text","part":{"id":"text-1","type":"text","text":"answer","time":{"end":2}}}'
`)
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
