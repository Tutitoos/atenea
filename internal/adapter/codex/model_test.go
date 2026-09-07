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

func TestRunModelUsesRequestedSandboxAndReasoningEffort(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	envFile := filepath.Join(t.TempDir(), "ripgrep-env")
	t.Setenv("RIPGREP_CONFIG_PATH", filepath.Join(t.TempDir(), "hostile-ripgrep-config"))
	body, err := json.Marshal(map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}
	item, err := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{
		"type": "agent_message", "text": string(body),
	}})
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > '" + argsFile + "'\n" +
		"printf '%s' \"$RIPGREP_CONFIG_PATH\" > '" + envFile + "'\n" +
		"printf '%s\\n' '" + strings.ReplaceAll(string(item), "'", "'\\''") + "'\n" +
		"printf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.RunModel(context.Background(), ModelRequest{
		Model: "gpt-5.6-luna", Prompt: "implement", Dir: t.TempDir(),
		Schema:  map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}},
		Sandbox: "workspace-write", ReasoningEffort: "xhigh",
	})
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if out.Structured["ok"] != true || out.Spent.InputTokens != 2 || out.Spent.OutputTokens != 3 {
		t.Fatalf("answer = %+v", out)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	joined := string(args)
	for _, want := range []string{"--sandbox\nworkspace-write", "--model\ngpt-5.6-luna", "-c\nmodel_reasoning_effort=xhigh", "--disable\nshell_tool", "--disable\nunified_exec"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q missing %q", joined, want)
		}
	}
	if raw, err := os.ReadFile(envFile); err != nil || string(raw) != "/dev/null" {
		t.Fatalf("RIPGREP_CONFIG_PATH = %q, err = %v; want /dev/null", raw, err)
	}
}

func TestRunModelTranslatesControlledMCPConfigAndEnablesAuthorizedReadSurface(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	item := `{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\nprintf '%s\\n' '" + item + "'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	tools := `{"mcpServers":{"atenea":{"command":"/bin/atenea","args":["mcp"]}}}`
	if _, err := runner.RunModel(context.Background(), ModelRequest{
		Prompt: "research", Sandbox: "read-only", Tools: tools, Builtins: []string{"Read"},
	}); err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	joined := string(raw)
	for _, want := range []string{"mcp_servers.atenea.command=", "mcp_servers.atenea.args=[\"mcp\"]", "--enable\nshell_tool", "--disable\nunified_exec"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q missing %q", joined, want)
		}
	}
}

func TestRunModelDerivesNetworkFromExternalEffectAndCarriesOneShotOperation(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	root := t.TempDir()
	item := `{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\nprintf '%s\\n' '" + item + "'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.RunModel(context.Background(), ModelRequest{
		Prompt: "publish", Sandbox: "workspace-write", Dir: root,
		Effects:    []contract.Effect{contract.EffectWrite, contract.EffectExternal},
		Operations: []contract.Operation{contract.OperationPush}, AssignmentID: "assignment-1",
		WorkflowID: "workflow-1", Worktree: root, PolicyDigest: "policy-digest", GrantToken: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if len(out.Notices) != 1 || !strings.HasPrefix(out.Notices[0], "enforcement=codex-pretooluse; hook=active;") || strings.Contains(out.Notices[0], "partial=true") {
		t.Fatalf("notices = %v, want active hook enforcement evidence", out.Notices)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	joined := string(raw)
	if !strings.Contains(joined, "sandbox_workspace_write.network_access=true") {
		t.Fatalf("argv = %q, want external network authorization", joined)
	}
	if !strings.Contains(joined, "--disable\nshell_tool") {
		t.Fatalf("argv = %q, want no native tools when Builtins is empty", joined)
	}
}

func TestRunModelUsesEphemeralCodexHookConfiguration(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	root := t.TempDir()
	item := `{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\nprintf '%s\\n' '" + item + "'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, HookBinary: binary, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunModel(context.Background(), ModelRequest{
		Prompt: "commit", Sandbox: "workspace-write", Dir: root,
		Effects:    []contract.Effect{contract.EffectWrite},
		Operations: []contract.Operation{contract.OperationCommit}, AssignmentID: "assignment-1",
		WorkflowID: "workflow-1", Worktree: root, PolicyDigest: "policy-digest", GrantToken: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	joined := string(args)
	for _, want := range []string{
		"--strict-config",
		"--dangerously-bypass-hook-trust",
		"features.hooks=true",
		"hooks.PreToolUse=[{matcher=",
		`matcher="^.*$"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "--ignore-user-config=false") {
		t.Fatal("hook configuration attempted to re-enable user config")
	}
}

func TestRunModelRejectsMissingHookBeforeProviderStarts(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf started > '" + started + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, HookBinary: filepath.Join(t.TempDir(), "missing"), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunModel(context.Background(), ModelRequest{
		Prompt: "deploy", Sandbox: "workspace-write", Dir: t.TempDir(),
		Effects:    []contract.Effect{contract.EffectWrite, contract.EffectExternal},
		Operations: []contract.Operation{contract.OperationDeploy},
	})
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("error kind = %v, want unavailable", contract.KindOf(err))
	}
	if _, statErr := os.Stat(started); !os.IsNotExist(statErr) {
		t.Fatalf("provider started: stat=%v", statErr)
	}
}

func TestRunModelRejectsSensitiveOperationWithoutMatchingEffect(t *testing.T) {
	runner, err := New(Options{Binary: filepath.Join(t.TempDir(), "does-not-exist")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunModel(context.Background(), ModelRequest{
		Prompt: "push", Sandbox: "workspace-write", Dir: t.TempDir(),
		Effects:    []contract.Effect{contract.EffectWrite},
		Operations: []contract.Operation{contract.OperationPush},
	})
	if contract.KindOf(err) != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", contract.KindOf(err))
	}
}

func TestRunModelRejectsUnsupportedSandboxBeforeStartingProvider(t *testing.T) {
	runner, err := New(Options{Binary: filepath.Join(t.TempDir(), "does-not-exist")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunModel(context.Background(), ModelRequest{Prompt: "x", Sandbox: "danger-full-access"})
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}

func TestRunModelRejectsUnknownBuiltinBeforeStartingProvider(t *testing.T) {
	runner, err := New(Options{Binary: filepath.Join(t.TempDir(), "does-not-exist")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunModel(context.Background(), ModelRequest{
		Prompt: "inspect", Sandbox: "read-only", Builtins: []string{"Unknown"},
	})
	if contract.KindOf(err) != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", contract.KindOf(err))
	}
}

func TestRunModelRejectsSchemaMismatch(t *testing.T) {
	item := `{"type":"item.completed","item":{"type":"agent_message","text":"{\"ok\":\"wrong\"}"}}`
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s\\n' '" + item + "'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunModel(context.Background(), ModelRequest{
		Prompt: "answer", Sandbox: "read-only",
		Schema: map[string]any{
			"type": "object", "required": []any{"ok"},
			"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
		},
	})
	if contract.KindOf(err) != contract.FailureUnavailable || !strings.Contains(err.Error(), "does not match schema") {
		t.Fatalf("error = %v, want schema rejection", err)
	}
}

func TestRunModelRejectsAdditionalPropertiesWhenSchemaClosesObject(t *testing.T) {
	item := `{"type":"item.completed","item":{"type":"agent_message","text":"{\"ok\":true,\"extra\":true}"}}`
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s\\n' '" + item + "'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunModel(context.Background(), ModelRequest{
		Prompt: "answer", Sandbox: "read-only",
		Schema: map[string]any{"type": "object", "additionalProperties": false,
			"properties": map[string]any{"ok": map[string]any{"type": "boolean"}}},
	})
	if contract.KindOf(err) != contract.FailureUnavailable || !strings.Contains(err.Error(), "does not match schema") {
		t.Fatalf("error = %v, want additional property rejection", err)
	}
}

func TestRunModelRejectsUnknownPositiveCostAndKeepsTokens(t *testing.T) {
	item := `{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s\\n' '" + item + "'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.RunModel(context.Background(), ModelRequest{
		Prompt: "answer", Sandbox: "read-only", BudgetUSD: 0.000001,
	})
	if contract.KindOf(err) != contract.FailurePermissionDenied || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("error = %v, want conservative estimate rejection", err)
	}
	if out.Spent.InputTokens != 7 || out.Spent.OutputTokens != 3 {
		t.Fatalf("spent = %+v, want token evidence preserved", out.Spent)
	}
	if out.Spent.USD == nil || !strings.HasPrefix(out.Spent.PricedBy, "estimate:") {
		t.Fatalf("spent = %+v, want estimated monetary evidence", out.Spent)
	}
}

func TestRunModelDoesNotMutateCallerSchema(t *testing.T) {
	item := `{"type":"item.completed","item":{"type":"agent_message","text":"{\"ok\":true}"}}`
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s\\n' '" + item + "'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"total_cost_usd\":0,\"usage\":{}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
	}
	if _, err := runner.RunModel(context.Background(), ModelRequest{Prompt: "answer", Sandbox: "read-only", Schema: schema}); err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if _, exists := schema["additionalProperties"]; exists {
		t.Fatal("RunModel mutated caller schema")
	}
}

func TestRunModelRejectsTerminalFailureAfterAgentMessage(t *testing.T) {
	item := `{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s\\n' '" + item + "'\nprintf '%s\\n' '{\"type\":\"turn.failed\",\"message\":\"provider stopped\"}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunModel(context.Background(), ModelRequest{Prompt: "answer", Sandbox: "read-only"})
	if contract.KindOf(err) != contract.FailureUnavailable || strings.Contains(err.Error(), "provider stopped") {
		t.Fatalf("error = %v, want redacted terminal failure", err)
	}
}

func TestRunModelRejectsAgentMessageWithoutTurnCompletion(t *testing.T) {
	item := `{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s\\n' '" + item + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunModel(context.Background(), ModelRequest{Prompt: "answer", Sandbox: "read-only"})
	if contract.KindOf(err) != contract.FailureUnavailable || !strings.Contains(err.Error(), "turn.completed") {
		t.Fatalf("error = %v, want missing terminal event", err)
	}
}

func TestRunModelRejectsCompletenessOutsideRange(t *testing.T) {
	item := `{"type":"item.completed","item":{"type":"agent_message","text":"{\"completeness\":2}"}}`
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s\\n' '" + item + "'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Options{Binary: binary, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.RunModel(context.Background(), ModelRequest{
		Prompt: "answer", Sandbox: "read-only",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"completeness": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		}},
	})
	if contract.KindOf(err) != contract.FailureUnavailable || !strings.Contains(err.Error(), "does not match schema") {
		t.Fatalf("error = %v, want range rejection", err)
	}
}
