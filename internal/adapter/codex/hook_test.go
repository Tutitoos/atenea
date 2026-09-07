package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Tutitoos/atenea/internal/activity"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestCodexHookDeniesWhenActivityCannotBePublished(t *testing.T) {
	server, err := activity.Start(func([]activity.Notice) error { return errors.New("chat closed") })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	t.Setenv(activity.Environment, server.Path())
	state := makeHookState(t)
	var output bytes.Buffer
	if err := RunHook([]string{"--state", state}, hookRequest(t, "Bash", map[string]string{"command": "pwd"}), &output); err != nil {
		t.Fatal(err)
	}
	if got := readDecision(t, &output); got.PermissionDecision != hookDecisionDeny || !strings.Contains(got.PermissionDecisionReason, "activity") {
		t.Fatalf("decision = %#v", got)
	}
}

func makeHookState(t *testing.T, operations ...contract.Operation) string {
	t.Helper()
	worktree, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := hookState{
		AssignmentID: "assignment-1", WorkflowID: "workflow-1", Worktree: worktree,
		PolicyDigest: "policy-digest", GrantToken: strings.Repeat("a", 64),
		Operations: map[contractOperation]string{}, AllowedBuiltins: map[string]bool{"shell": true, "applypatch": true},
	}
	for _, operation := range operations {
		marker := filepath.Join(dir, "grant-"+safeStateName(operation.String()))
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		state.Operations[contractOperation(operation.String())] = marker
	}
	path := filepath.Join(dir, "state.json")
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hookRequest(t *testing.T, toolName string, input any) *bytes.Buffer {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"tool_name": toolName, "tool_input": input})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewBuffer(encoded)
}

func readDecision(t *testing.T, output *bytes.Buffer) hookSpecificOutput {
	t.Helper()
	var decision hookDecision
	if err := json.Unmarshal(output.Bytes(), &decision); err != nil {
		t.Fatalf("decision = %q: %v", output.String(), err)
	}
	if decision.HookSpecificOutput.HookEventName != hookEventPreToolUse {
		t.Fatalf("hook event = %q, want %q", decision.HookSpecificOutput.HookEventName, hookEventPreToolUse)
	}
	return decision.HookSpecificOutput
}

func TestCodexHookDeniesSensitiveBashWithoutExactGrant(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	state := makeHookState(t)
	var output bytes.Buffer
	if err := RunHook([]string{"--state", state}, hookRequest(t, "Bash", map[string]string{"command": "git commit -m message"}), &output); err != nil {
		t.Fatal(err)
	}
	decision := readDecision(t, &output)
	if decision.PermissionDecision != hookDecisionDeny {
		t.Fatalf("decision = %+v, want deny", decision)
	}
}

func TestCodexHookFailsClosedWhenStateIsMissingOrMalformed(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), ""} {
		var output bytes.Buffer
		if err := RunHook([]string{"--state", path}, hookRequest(t, "Bash", map[string]string{"command": "git push"}), &output); err != nil {
			t.Fatal(err)
		}
		if got := readDecision(t, &output).PermissionDecision; got != hookDecisionDeny {
			t.Fatalf("state %q decision = %q, want deny", path, got)
		}
	}
}

func TestCodexHookAllowsAndConsumesOneShotAtomically(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	state := makeHookState(t, contract.OperationCommit)
	var first bytes.Buffer
	if err := RunHook([]string{"--state", state}, hookRequest(t, "Bash", map[string]string{"command": "git commit -m message"}), &first); err != nil {
		t.Fatal(err)
	}
	if got := readDecision(t, &first); got.PermissionDecision != hookDecisionAllow {
		t.Fatalf("first decision = %+v, want allow", got)
	}
	var second bytes.Buffer
	if err := RunHook([]string{"--state", state}, hookRequest(t, "Bash", map[string]string{"command": "git commit -m message"}), &second); err != nil {
		t.Fatal(err)
	}
	if got := readDecision(t, &second).PermissionDecision; got != hookDecisionDeny {
		t.Fatalf("second decision = %q, want deny", got)
	}
}

func TestCodexHookConcurrentOneShotHasOneWinner(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	state := makeHookState(t, contract.OperationPush)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allows := 0
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var output bytes.Buffer
			if err := RunHook([]string{"--state", state}, hookRequest(t, "Bash", map[string]string{"command": "git push origin main"}), &output); err != nil {
				t.Errorf("RunHook: %v", err)
				return
			}
			if readDecision(t, &output).PermissionDecision == hookDecisionAllow {
				mu.Lock()
				allows++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allows != 1 {
		t.Fatalf("allow count = %d, want exactly one", allows)
	}
}

func TestCodexHookRejectsCompositeAndIndirectCommands(t *testing.T) {
	for _, command := range []string{
		"git commit -m message && git push",
		"sh -c 'git commit -m message'",
		"git commit $(cat message)",
		"./deploy.sh",
	} {
		operation, unsafe := classifyBashCommand(command)
		if operation != "" || !unsafe {
			t.Errorf("classifyBashCommand(%q) = operation %q, unsafe %v; want fail-closed", command, operation, unsafe)
		}
	}
}

func TestCodexHookAllowsOnlyDirectReadCommands(t *testing.T) {
	for _, command := range []string{
		"pwd",
		"ls -la",
		"cat README.md",
		"head -n 20 README.md",
		"tail -f server.log",
		"wc -l README.md",
		"rg TODO internal",
		"grep -R TODO internal",
	} {
		operation, unsafe := classifyBashCommand(command)
		if operation != "" || unsafe {
			t.Errorf("classifyBashCommand(%q) = operation %q, unsafe %v; want read-only allow", command, operation, unsafe)
		}
	}
}

func TestCodexHookDeniesBuiltinsOutsideDeclaredSurface(t *testing.T) {
	state := hookState{AllowedBuiltins: map[string]bool{}, AllowWrite: true}
	for _, input := range []hookInput{
		{ToolName: "shell_command", ToolInput: json.RawMessage(`{"command":"pwd"}`)},
		{ToolName: "apply_patch", ToolInput: json.RawMessage(`{"patch":"x"}`)},
	} {
		if got := decideHook(state, input).HookSpecificOutput.PermissionDecision; got != hookDecisionDeny {
			t.Fatalf("tool %s decision = %q", input.ToolName, got)
		}
	}
}

func TestCodexHookDeniesEveryUnapprovedToolName(t *testing.T) {
	state := hookState{AllowedBuiltins: map[string]bool{}, AllowWrite: false}
	for _, name := range []string{"view_image", "spawn_agent", "js", "web_search", "future_tool"} {
		if got := decideHook(state, hookInput{ToolName: name, ToolInput: json.RawMessage(`{}`)}).HookSpecificOutput.PermissionDecision; got != hookDecisionDeny {
			t.Errorf("tool %s decision = %q, want deny", name, got)
		}
	}
}

func TestPrepareNativeHookMatchesAllAndClosesNativeFeatures(t *testing.T) {
	hook, err := PrepareNativeHook(t.TempDir(), ModelRequest{Sandbox: "read-only", Builtins: []string{"Read"}})
	if err != nil {
		t.Fatal(err)
	}
	defer hook.Close()
	if got := hook.Config["web_search"]; got != "disabled" {
		t.Fatalf("web_search = %#v", got)
	}
	features := hook.Config["features"].(map[string]any)
	if features["shell_tool"] != true || features["multi_agent"] != false || features["view_image"] != false || features["js_repl"] != false {
		t.Fatalf("native features = %#v", features)
	}
	pre := hook.Config["hooks"].(map[string]any)["PreToolUse"].([]map[string]any)
	if got := pre[0]["matcher"]; got != "^.*$" {
		t.Fatalf("matcher = %#v", got)
	}
}

func TestCodexHookDeniesAllGitReadCommands(t *testing.T) {
	for _, command := range []string{
		"git status --short",
		"git diff --stat",
		"git log -1",
		"git show HEAD",
		"git rev-parse HEAD",
		"git grep TODO",
		"git grep -Osh NEEDLE payload",
		"git grep -Osh NEEDLE payload -- '*.go'",
		"git ci -m message",
		"git commit-all -m message",
		"git COMMIT -m message",
	} {
		operation, unsafe := classifyBashCommand(command)
		if operation != "" || !unsafe {
			t.Errorf("classifyBashCommand(%q) = operation %q, unsafe %v; want deny", command, operation, unsafe)
		}
	}
}

func TestCodexHookDeniesUnclassifiedBashAndUnsafeReadOptions(t *testing.T) {
	for _, command := range []string{
		"curl https://example.com",
		"go test ./...",
		"go build ./...",
		"gofmt -w file.go",
		"npm run test",
		"find . -type f",
		"sed -n 1,10p README.md",
		"sh -c pwd",
		"/bin/ls",
		"ls src/internal",
		"pwd; git status",
		"git status -c",
		"git diff --ext-diff",
		"git show --textconv",
		"git -c alias.st= status",
		"rg --exec=cat TODO",
		"rg --hostname-bin=payload TODO",
		"rg --pre=cat TODO",
		"rg --pre-glob='*.pdf' TODO",
		"unknown-command",
	} {
		operation, unsafe := classifyBashCommand(command)
		if operation != "" || !unsafe {
			t.Errorf("classifyBashCommand(%q) = operation %q, unsafe %v; want deny", command, operation, unsafe)
		}
	}
}

func TestCodexHookInstallerCommandsWithoutVerbsDenyWithoutPanic(t *testing.T) {
	for _, command := range []string{"npm", "brew", "go"} {
		operation, unsafe := classifyBashCommand(command)
		if operation != "" || !unsafe {
			t.Errorf("classifyBashCommand(%q) = operation %q, unsafe %v; want deny", command, operation, unsafe)
		}
	}
}

func TestCodexHookShortOperationVectorsDoNotPanic(t *testing.T) {
	vectors := [][]string{
		nil,
		{},
		{"npm"},
		{"brew"},
		{"go"},
		{"deploy"},
		{"migration"},
	}
	for _, fields := range vectors {
		installerOperation(fields)
		deployOperation(fields)
		migrationOperation(fields)
	}
}

func FuzzCodexHookClassifyBashCommandNeverPanics(f *testing.F) {
	for _, seed := range []string{
		"",
		"npm",
		"brew",
		"go",
		"git grep -Osh NEEDLE payload",
		"git -c alias.st= status",
		"rg --pre-glob='*.pdf' TODO",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, command string) {
		classifyBashCommand(command)
	})
}

func TestCodexHookRejectedBashDoesNotConsumeGrantMarker(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	statePath := makeHookState(t, contract.OperationCommit)
	var state hookState
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	marker := state.Operations[contractOperation(contract.OperationCommit.String())]
	var output bytes.Buffer
	if err := RunHook([]string{"--state", statePath}, hookRequest(t, "Bash", map[string]string{
		"command": "git grep -Osh NEEDLE payload",
	}), &output); err != nil {
		t.Fatal(err)
	}
	if got := readDecision(t, &output).PermissionDecision; got != hookDecisionDeny {
		t.Fatalf("decision = %q, want deny", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(marker), "used-commit")); !os.IsNotExist(err) {
		t.Fatalf("rejected command created bypass marker: stat error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("grant marker was removed or changed: %v", err)
	}
}

func TestCodexHookKeepsDirectSensitiveCommandsAsOperations(t *testing.T) {
	for _, test := range []struct {
		command   string
		operation contractOperation
	}{
		{command: "git commit -m message", operation: "commit"},
		{command: "git push origin main", operation: "push"},
		{command: "git merge feature", operation: "merge"},
		{command: "npm install package", operation: "install"},
		{command: "deploy production", operation: "deploy"},
		{command: "migrate up", operation: "migrate"},
	} {
		operation, unsafe := classifyBashCommand(test.command)
		if operation != test.operation || unsafe {
			t.Errorf("classifyBashCommand(%q) = operation %q, unsafe %v; want operation %q", test.command, operation, unsafe, test.operation)
		}
	}
}

func TestCodexHookRejectsUnknownMCPAndAllowsAuthorizedPatch(t *testing.T) {
	statePath := makeHookState(t)
	state, err := loadHookStateFromAnyWorktree(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := decideHook(state, hookInput{ToolName: "mcp__atenea__write", ToolInput: json.RawMessage(`{}`)}).HookSpecificOutput.PermissionDecision; got != hookDecisionDeny {
		t.Fatalf("MCP decision = %q, want deny", got)
	}
	state.AllowWrite = true
	if got := decideHook(state, hookInput{ToolName: "ApplyPatch", ToolInput: json.RawMessage(`{"patch":"x"}`)}).HookSpecificOutput.PermissionDecision; got != hookDecisionAllow {
		t.Fatalf("ApplyPatch decision = %q, want allow", got)
	}
}

func TestNewHookInvocationRejectsIncompleteOrMismatchedOperationBinding(t *testing.T) {
	root := t.TempDir()
	hookBinary := filepath.Join(t.TempDir(), "hook")
	if err := os.WriteFile(hookBinary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	base := ModelRequest{
		AssignmentID: "assignment-1", WorkflowID: "workflow-1", Worktree: root,
		PolicyDigest: "policy-digest", GrantToken: strings.Repeat("a", 64),
		Operations: []contract.Operation{contract.OperationCommit},
	}
	for name, mutate := range map[string]func(*ModelRequest){
		"assignment": func(req *ModelRequest) { req.AssignmentID = "" },
		"workflow":   func(req *ModelRequest) { req.WorkflowID = "" },
		"worktree":   func(req *ModelRequest) { req.Worktree = t.TempDir() },
		"policy":     func(req *ModelRequest) { req.PolicyDigest = "" },
		"weak token": func(req *ModelRequest) { req.GrantToken = "weak" },
	} {
		t.Run(name, func(t *testing.T) {
			req := base
			mutate(&req)
			if _, err := newHookInvocation(root, req, hookBinary); err == nil {
				t.Fatal("newHookInvocation succeeded with an invalid operation binding")
			}
		})
	}
}

func TestNewHookInvocationStateIsOutsideTempAndWorktree(t *testing.T) {
	root := t.TempDir()
	tmp := filepath.Join(root, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	hookBinary := filepath.Join(t.TempDir(), "hook")
	if err := os.WriteFile(hookBinary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	invocation, err := newHookInvocation(root, ModelRequest{
		AssignmentID: "assignment-1", WorkflowID: "workflow-1", Worktree: root,
		PolicyDigest: "policy-digest", GrantToken: strings.Repeat("a", 64),
		Operations: []contract.Operation{contract.OperationCommit},
	}, hookBinary)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(invocation.StatePath)) })
	stateDir := filepath.Dir(invocation.StatePath)
	if withinDirectory(root, stateDir) || withinDirectory(tmp, stateDir) {
		t.Fatalf("hook state %q is inside worktree %q or TMPDIR %q", stateDir, root, tmp)
	}
	info, err := os.Stat(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("hook state mode = %o, want 700", info.Mode().Perm())
	}
}

func TestRunHookRejectsTamperedAssignmentBindingAndInsecureStateDirectory(t *testing.T) {
	statePath := makeHookState(t, contract.OperationCommit)
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state hookState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state.AssignmentID = "other-assignment"
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunHook([]string{"--state", statePath, "--assignment-id", "assignment-1"}, hookRequest(t, "Bash", map[string]string{"command": "git commit -m message"}), &output); err != nil {
		t.Fatal(err)
	}
	if got := readDecision(t, &output).PermissionDecision; got != hookDecisionDeny {
		t.Fatalf("tampered assignment decision = %q, want deny", got)
	}

	insecureDir := filepath.Dir(statePath)
	if err := os.Chmod(insecureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHookState(statePath); err == nil {
		t.Fatal("loadHookState accepted a non-private state directory")
	}
}

func TestLoadHookStateRejectsPublicOrSymlinkedStateFile(t *testing.T) {
	statePath := makeHookState(t)
	if err := os.Chmod(statePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHookState(statePath); err == nil {
		t.Fatal("loadHookState accepted a state file with public permissions")
	}

	if err := os.Chmod(statePath, 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(filepath.Dir(statePath), "state-link.json")
	if err := os.Symlink(statePath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHookState(symlinkPath); err == nil {
		t.Fatal("loadHookState accepted a symlinked state file")
	}
}

// loadHookStateFromAnyWorktree exercises the pure decision path without
// changing the process cwd, while RunHook's integration path still checks the
// inherited worktree binding above.
func loadHookStateFromAnyWorktree(path string) (hookState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return hookState{}, err
	}
	var state hookState
	if err := json.Unmarshal(data, &state); err != nil {
		return hookState{}, err
	}
	return state, nil
}

func TestCodexHookNoticeHasEnforcementEvidence(t *testing.T) {
	notice := hookNotice([]contract.Operation{contract.OperationPush, contract.OperationCommit})
	if strings.Contains(notice, "partial=true") || !strings.Contains(notice, "hook=active") || !strings.Contains(notice, "state=ephemeral") {
		t.Fatalf("notice = %q, want active enforcement evidence", notice)
	}
}
