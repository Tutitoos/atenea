package modelagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestImplementRouteRunsCodexWithAuthorizedWriteAndReturnsEvidence(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"role\":\"implement\",\"evidence\":[\"changed fixture\"],\"completeness\":1,\"stopped_at\":\"done\"}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":2}}'
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	grant := 1.0
	in := assignment{Type: "implement", BudgetUSD: &grant, Invisible: true,
		Effects: []string{"read", "write"},
		Context: map[string]json.RawMessage{"repository": json.RawMessage(`{"root":` + strconv.Quote(root) + `}`)},
		Route: &route{Role: "implement", Backend: "codex", Binary: binary,
			RequestedModel: "gpt-5.6-luna", RequestedReasoningEffort: "xhigh"},
	}
	out := run(context.Background(), in, "implement")
	if out.Verdict != "ok" || out.Completeness == nil || *out.Completeness != 1 {
		t.Fatalf("report = %+v", out)
	}
	if got, _ := out.Result["role"].(string); got != "implement" {
		t.Fatalf("role = %q", got)
	}
}

func TestImplementCarriesOneShotOperationIntoCodexPrompt(t *testing.T) {
	root := t.TempDir()
	promptFile := filepath.Join(t.TempDir(), "prompt")
	binary := filepath.Join(t.TempDir(), "codex")
	item := `{"type":"item.completed","item":{"type":"agent_message","text":"{\"role\":\"implement\",\"evidence\":[],\"completeness\":1,\"stopped_at\":\"\"}"}}`
	script := "#!/bin/sh\ncat > '" + promptFile + "'\nprintf '%s\\n' '" + strings.ReplaceAll(item, "'", "'\\''") + "'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	grant := 1.0
	in := assignment{Type: "implement", BudgetUSD: &grant, Invisible: true,
		ID: "assignment-1", WorkflowID: "workflow-1", Worktree: root,
		PolicyDigest: "policy-digest", GrantToken: strings.Repeat("a", 64),
		Effects: []string{"read", "write"}, Operations: []string{"commit"},
		Context: map[string]json.RawMessage{"repository": json.RawMessage(`{"root":` + strconv.Quote(root) + `}`)},
		Route: &route{Role: "implement", Backend: "codex", Binary: binary,
			RequestedModel: "gpt-5.6-luna", RequestedReasoningEffort: "xhigh"},
	}
	out := run(context.Background(), in, "implement")
	if out.Verdict != "ok" {
		if out.Reason != nil {
			t.Fatalf("report = %+v reason=%s: %s", out, out.Reason.Kind, out.Reason.Text)
		}
		t.Fatalf("report = %+v", out)
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "one-shot sensitive operations: commit") {
		t.Fatalf("prompt = %q, want explicit operation authorization", prompt)
	}
}

func TestMissingOrZeroBudgetRefusesModelBackedStage(t *testing.T) {
	zero := 0.0
	for _, budget := range []*float64{nil, &zero} {
		in := assignment{Type: "review", BudgetUSD: budget,
			Route: &route{Role: "review", Backend: "codex", Binary: filepath.Join(t.TempDir(), "missing"), RequestedModel: "gpt-5.6-sol"},
		}
		out := run(context.Background(), in, "review")
		if out.Verdict != "incomplete" || out.Reason == nil || out.Reason.Kind != "permission_denied" {
			t.Fatalf("budget %v report = %+v", budget, out)
		}
	}
}

func TestRouteRoleMustMatchExecutableAgentType(t *testing.T) {
	grant := 1.0
	in := assignment{Type: "audit", BudgetUSD: &grant,
		Route: &route{Role: "review", Backend: "codex", Binary: filepath.Join(t.TempDir(), "missing"), RequestedModel: "gpt-5.6-sol"},
	}
	out := run(context.Background(), in, "audit")
	if out.Verdict != "incomplete" || out.Reason == nil || out.Reason.Kind != "invalid_input" {
		t.Fatalf("report = %+v", out)
	}
}
