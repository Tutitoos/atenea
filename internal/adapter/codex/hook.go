package codex

// This file contains the small host-owned command used by Codex's PreToolUse
// hook.  The hook is deliberately a separate process: Codex invokes command
// hooks with a JSON request on stdin, while the model runner owns the
// assignment and the one-shot grant that request has to consume.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Tutitoos/atenea/internal/activity"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const (
	// codexHookCommand is an internal command understood by the Atenea binary.
	// It is kept separate from the public command list because it is only ever
	// started by Codex, with the state file created for one model invocation.
	codexHookCommand = "codex-hook"

	hookDecisionAllow = "allow"
	hookDecisionDeny  = "deny"
)

type hookState struct {
	AssignmentID    string                       `json:"assignment_id,omitempty"`
	WorkflowID      string                       `json:"workflow_id,omitempty"`
	Worktree        string                       `json:"worktree"`
	PolicyDigest    string                       `json:"policy_digest,omitempty"`
	GrantToken      string                       `json:"grant_token,omitempty"`
	AllowWrite      bool                         `json:"allow_write"`
	AllowedBuiltins map[string]bool              `json:"allowed_builtins,omitempty"`
	Operations      map[contractOperation]string `json:"operations,omitempty"`
}

// contractOperation is kept as a string in the on-disk state so a hook state
// remains inspectable without importing Go enum details.  The adapter writes
// only names accepted by parseHookOperation.
type contractOperation string

type hookInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

const hookEventPreToolUse = "PreToolUse"

// hookSpecificOutput is the official response payload for a PreToolUse hook.
// The event name makes the response unambiguous if more hook event types are
// added later.
type hookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

type hookDecision struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

// hookInvocation is the ephemeral state and command string attached to one
// Codex process.  StatePath is removed by the model runner after the process
// exits; a hook cannot carry grants into a retry by finding user config.
type hookInvocation struct {
	StatePath string
	Command   string
	Notice    string
}

// NativeHook is the per-thread App Server hook configuration. Its state is
// private and must live exactly as long as the native thread that references
// its command.
type NativeHook struct {
	Config    map[string]any
	StatePath string
	Notice    string
}

// PrepareNativeHook reuses the same fail-closed authorization state as the
// one-shot Codex adapter and returns thread/start config understood by 0.151.
func PrepareNativeHook(root string, req ModelRequest) (NativeHook, error) {
	binary, err := os.Executable()
	if err != nil || strings.TrimSpace(binary) == "" {
		return NativeHook{}, errors.New("codex native hook executable is unavailable")
	}
	invocation, err := newHookInvocation(root, req, binary)
	if err != nil {
		return NativeHook{}, err
	}
	enabled, err := codexBuiltinFeatures(req.Sandbox, req.Builtins)
	if err != nil {
		_ = invocation.Close()
		return NativeHook{}, err
	}
	features := make(map[string]any, len(codexNativeFeatures)+1)
	features["hooks"] = true
	for _, feature := range codexNativeFeatures {
		features[feature] = enabled[feature]
	}
	return NativeHook{
		StatePath: invocation.StatePath,
		Notice:    invocation.Notice,
		Config: map[string]any{
			"web_search": "disabled",
			"features":   features,
			"hooks": map[string]any{"PreToolUse": []map[string]any{{
				"matcher": "^.*$",
				"hooks":   []map[string]any{{"type": "command", "command": invocation.Command}},
			}}},
		},
	}, nil
}

// Close is part of ATENEA's public orchestration contract.
func (h NativeHook) Close() error {
	if strings.TrimSpace(h.StatePath) == "" {
		return nil
	}
	return os.RemoveAll(filepath.Dir(h.StatePath))
}

func (h hookInvocation) Close() error {
	if strings.TrimSpace(h.StatePath) == "" {
		return nil
	}
	return os.RemoveAll(filepath.Dir(h.StatePath))
}

func newHookInvocation(root string, req ModelRequest, hookBinary string) (hookInvocation, error) {
	if strings.TrimSpace(hookBinary) == "" {
		return hookInvocation{}, errors.New("codex hook executable is empty")
	}
	info, err := os.Stat(hookBinary)
	if err != nil {
		return hookInvocation{}, fmt.Errorf("codex hook executable is unavailable: %w", err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return hookInvocation{}, errors.New("codex hook executable is not executable")
	}
	worktree, err := filepath.Abs(root)
	if err != nil {
		return hookInvocation{}, fmt.Errorf("codex hook worktree: %w", err)
	}
	if len(req.Operations) > 0 {
		if err := validateHookBinding(worktree, req); err != nil {
			return hookInvocation{}, err
		}
	}
	// The state directory is private to this invocation. It lives in the real
	// OS user's home rather than TMPDIR, because TMPDIR may be configured under
	// the served worktree and would then put authorization markers in the tree
	// the hook is protecting. It never touches ~/.codex or another user config
	// location.
	dir, err := newHookStateDirectory(worktree)
	if err != nil {
		return hookInvocation{}, fmt.Errorf("codex hook state: %w", err)
	}
	state := hookState{
		AssignmentID:    strings.TrimSpace(req.AssignmentID),
		WorkflowID:      strings.TrimSpace(req.WorkflowID),
		Worktree:        worktree,
		PolicyDigest:    strings.TrimSpace(req.PolicyDigest),
		GrantToken:      strings.TrimSpace(req.GrantToken),
		AllowWrite:      req.Sandbox == "workspace-write" && hasEffect(req.Effects, effectWriteName),
		AllowedBuiltins: make(map[string]bool, len(req.Builtins)),
		Operations:      make(map[contractOperation]string, len(req.Operations)),
	}
	for _, builtin := range req.Builtins {
		state.AllowedBuiltins[strings.ToLower(strings.TrimSpace(builtin))] = true
	}
	for _, operation := range req.Operations {
		name := contractOperation(operation.String())
		marker := filepath.Join(dir, "grant-"+safeStateName(string(name)))
		file, createErr := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			_ = os.RemoveAll(dir)
			return hookInvocation{}, fmt.Errorf("codex hook grant: %w", createErr)
		}
		_ = file.Close()
		state.Operations[name] = marker
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		_ = os.RemoveAll(dir)
		return hookInvocation{}, fmt.Errorf("codex hook state encoding: %w", err)
	}
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, encoded, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return hookInvocation{}, fmt.Errorf("codex hook state write: %w", err)
	}
	command := shellQuote(hookBinary) + " " + codexHookCommand + " --state " + shellQuote(statePath)
	if len(req.Operations) > 0 {
		// The host supplies the expected assignment id out of band from the
		// state file. A hook state whose binding is replaced or mismatched is
		// denied before it can consume any operation marker.
		command += " --assignment-id " + shellQuote(req.AssignmentID)
	}
	return hookInvocation{
		StatePath: statePath,
		Command:   command,
		Notice:    hookNotice(req.Operations),
	}, nil
}

func newHookStateDirectory(worktree string) (string, error) {
	current, err := user.Current()
	if err != nil || strings.TrimSpace(current.HomeDir) == "" {
		return "", errors.New("codex hook state has no real OS home directory")
	}
	home, err := filepath.Abs(current.HomeDir)
	if err != nil {
		return "", fmt.Errorf("codex hook home: %w", err)
	}
	info, err := os.Lstat(home)
	if err != nil {
		return "", fmt.Errorf("codex hook home: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("codex hook home is not a real directory")
	}
	if samePath(home, worktree) || withinDirectory(worktree, home) {
		return "", errors.New("codex hook home is inside the worktree")
	}
	dir, err := os.MkdirTemp(home, ".atenea-codex-hook-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("codex hook state permissions: %w", err)
	}
	return dir, nil
}

func hasEffect(effects []contract.Effect, name string) bool {
	for _, effect := range effects {
		if effect.String() == name {
			return true
		}
	}
	return false
}

const effectWriteName = "write"

func hookNotice(operations []contract.Operation) string {
	names := make([]string, 0, len(operations))
	for _, operation := range operations {
		names = append(names, operation.String())
	}
	slicesSortStrings(names)
	return "enforcement=codex-pretooluse; hook=active; state=ephemeral; partial=false; operations=" + strings.Join(names, ",")
}

// hookConfigOverride is a TOML value accepted by Codex's -c flag.  Keeping it
// inline makes the hook configuration scoped to this invocation even when the
// provider is started with --ignore-user-config.  strict-config is added by
// the model runner so an older Codex cannot silently ignore features.hooks.
func hookConfigOverride(command string) string {
	return `[{matcher="^.*$",hooks=[{type="command",command=` + tomlQuote(command) + `}]}]`
}

func tomlQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func safeStateName(value string) string {
	var b strings.Builder
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '-' || char == '_' {
			b.WriteRune(char)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func loadHookState(path string) (hookState, error) {
	clean := filepath.Clean(path)
	if err := requirePrivatePath(clean, false); err != nil {
		return hookState{}, err
	}
	if err := requirePrivatePath(filepath.Dir(clean), true); err != nil {
		return hookState{}, err
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return hookState{}, err
	}
	var state hookState
	if err := json.Unmarshal(data, &state); err != nil {
		return hookState{}, err
	}
	if !filepath.IsAbs(state.Worktree) || strings.TrimSpace(state.Worktree) == "" {
		return hookState{}, errors.New("codex hook state has no absolute worktree")
	}
	current, err := os.Getwd()
	if err != nil || !samePath(current, state.Worktree) {
		return hookState{}, errors.New("codex hook state is bound to another worktree")
	}
	if state.Operations == nil {
		state.Operations = map[contractOperation]string{}
	}
	if len(state.Operations) > 0 {
		if err := validateHookBinding(state.Worktree, ModelRequest{
			AssignmentID: state.AssignmentID,
			WorkflowID:   state.WorkflowID,
			Worktree:     state.Worktree,
			PolicyDigest: state.PolicyDigest,
			GrantToken:   state.GrantToken,
		}); err != nil {
			return hookState{}, err
		}
	}
	// A state file must not point grants outside its private directory.  This
	// also prevents a malformed or tampered state from turning a hook into an
	// arbitrary file remover.
	base := filepath.Dir(clean)
	for operation, marker := range state.Operations {
		if !operation.Known() || !withinDirectory(base, marker) {
			return hookState{}, errors.New("codex hook state has an invalid operation marker")
		}
		if err := requirePrivatePath(marker, false); err != nil {
			return hookState{}, err
		}
	}
	return state, nil
}

func requirePrivatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("codex hook state path is not private")
	}
	if directory {
		if !info.IsDir() {
			return errors.New("codex hook state directory is not a directory")
		}
	} else if !info.Mode().IsRegular() {
		return errors.New("codex hook state file is not regular")
	}
	return nil
}

func validateHookBinding(root string, req ModelRequest) error {
	if strings.TrimSpace(req.AssignmentID) == "" ||
		strings.TrimSpace(req.WorkflowID) == "" ||
		strings.TrimSpace(req.Worktree) == "" ||
		strings.TrimSpace(req.PolicyDigest) == "" {
		return errors.New("codex hook operation binding is incomplete")
	}
	if !samePath(root, req.Worktree) {
		return errors.New("codex hook operation binding is bound to another worktree")
	}
	if !strongGrantToken(req.GrantToken) {
		return errors.New("codex hook operation binding has a weak grant token")
	}
	return nil
}

func strongGrantToken(token string) bool {
	token = strings.TrimSpace(token)
	if len(token) < 64 || len(token)%2 != 0 {
		return false
	}
	decoded, err := hex.DecodeString(token)
	return err == nil && len(decoded) >= 32
}

func samePath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftResolved, leftErr := filepath.EvalSymlinks(left)
	rightResolved, rightErr := filepath.EvalSymlinks(right)
	if leftErr == nil && rightErr == nil {
		return leftResolved == rightResolved
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func withinDirectory(base, candidate string) bool {
	base, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, candidate)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (o contractOperation) Known() bool {
	switch o {
	case "commit", "push", "install", "deploy", "migrate", "merge":
		return true
	default:
		return false
	}
}

func allowDecision() hookDecision {
	return hookDecision{HookSpecificOutput: hookSpecificOutput{
		HookEventName:      hookEventPreToolUse,
		PermissionDecision: hookDecisionAllow,
	}}
}

func denyDecision(reason string) hookDecision {
	return hookDecision{HookSpecificOutput: hookSpecificOutput{
		HookEventName:            hookEventPreToolUse,
		PermissionDecision:       hookDecisionDeny,
		PermissionDecisionReason: reason,
	}}
}

// RunHook handles one Codex PreToolUse request.  It is exported so the
// atenea command can expose the same binary as the hook helper without a
// second installed executable.
func RunHook(args []string, stdin io.Reader, stdout io.Writer) error {
	statePath := ""
	expectedAssignmentID := ""
	for index := 0; index < len(args); index++ {
		if args[index] == "--state" && index+1 < len(args) {
			statePath = args[index+1]
			index++
			continue
		}
		if args[index] == "--assignment-id" && index+1 < len(args) {
			expectedAssignmentID = args[index+1]
			index++
			continue
		}
		return writeHookDecision(stdout, denyDecision("codex hook arguments are invalid"))
	}
	if statePath == "" {
		return writeHookDecision(stdout, denyDecision("codex hook state is missing"))
	}
	state, err := loadHookState(statePath)
	if err != nil {
		return writeHookDecision(stdout, denyDecision("codex hook state is unavailable"))
	}
	if expectedAssignmentID != "" && state.AssignmentID != expectedAssignmentID {
		return writeHookDecision(stdout, denyDecision("codex hook assignment binding does not match the request"))
	}
	var input hookInput
	decoder := json.NewDecoder(io.LimitReader(stdin, 1<<20))
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.ToolName) == "" {
		return writeHookDecision(stdout, denyDecision("codex hook request is invalid"))
	}
	if !strings.HasPrefix(input.ToolName, "mcp__atenea__") {
		if err := activity.PublishFromEnvironment(input.ToolName); err != nil {
			return writeHookDecision(stdout, denyDecision("tool activity could not be published before execution"))
		}
	}
	decision := decideHook(state, input)
	return writeHookDecision(stdout, decision)
}

func writeHookDecision(stdout io.Writer, decision hookDecision) error {
	return json.NewEncoder(stdout).Encode(decision)
}

func decideHook(state hookState, input hookInput) hookDecision {
	switch input.ToolName {
	case "Bash", "shell_command", "exec_command":
		if !allowsShell(state.AllowedBuiltins) {
			return denyDecision("shell tool is outside the declared built-in surface")
		}
		var payload struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(input.ToolInput, &payload); err != nil {
			return denyDecision("codex Bash input is invalid")
		}
		operation, unsafe := classifyBashCommand(payload.Command)
		if unsafe {
			return denyDecision("codex Bash command is ambiguous or indirect")
		}
		if operation == "" {
			return allowDecision()
		}
		return consumeOperation(state, operation)
	case "ApplyPatch", "apply_patch":
		if state.AllowWrite && state.AllowedBuiltins["applypatch"] {
			return allowDecision()
		}
		return denyDecision("ApplyPatch requires explicit write authorization")
	default:
		// Every MCP/local tool is matched by the inline hook.  There is no
		// portable capability metadata in a Codex hook request, so an unknown
		// tool is denied instead of being trusted to be read-only.  A future
		// adapter can add exact names to this classifier without weakening the
		// default.
		if strings.HasPrefix(input.ToolName, "mcp__") || strings.HasPrefix(input.ToolName, "local__") {
			return denyDecision("MCP/local tool has no hookable operation declaration")
		}
		return denyDecision("codex tool is outside the mediated surface")
	}
}

func allowsShell(allowed map[string]bool) bool {
	return allowed["shell"] || allowed["read"] || allowed["glob"] || allowed["grep"]
}

func consumeOperation(state hookState, operation contractOperation) hookDecision {
	if !operation.Known() {
		return denyDecision("sensitive operation is not recognized")
	}
	marker, ok := state.Operations[operation]
	if !ok {
		return denyDecision("sensitive operation has no matching one-shot grant")
	}
	marker = filepath.Clean(marker)
	base := filepath.Dir(marker)
	if err := requirePrivatePath(base, true); err != nil || !withinDirectory(base, marker) {
		return denyDecision("sensitive operation grant path is invalid")
	}
	if err := requirePrivatePath(marker, false); err != nil {
		return denyDecision("sensitive operation grant is unavailable")
	}
	usedMarker := filepath.Join(base, "used-"+safeStateName(string(operation)))
	if !withinDirectory(base, usedMarker) {
		return denyDecision("sensitive operation consumption path is invalid")
	}
	// Creation with O_EXCL is atomic across concurrent hook processes: exactly
	// one process can create the marker, so only that process may use the
	// one-shot grant. The original grant remains as evidence of the binding.
	used, err := os.OpenFile(usedMarker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return denyDecision("sensitive operation grant was already consumed")
	}
	if err := used.Close(); err != nil {
		return denyDecision("sensitive operation grant could not be consumed")
	}
	return allowDecision()
}

func classifyBashCommand(raw string) (contractOperation, bool) {
	command := strings.TrimSpace(raw)
	if command == "" || strings.ContainsAny(command, ";|&`$()<>\n\r\\\"'!~#\x00") {
		return "", true
	}
	fields := strings.Fields(command)
	if len(fields) == 0 || fields[0] == "" {
		return "", true
	}
	for _, field := range fields {
		if strings.ContainsAny(field, "*?[]{}") || strings.Contains(field, "/") || field == "." || field == ".." {
			return "", true
		}
	}
	if shellWrapper(fields[0]) {
		return "", true
	}
	if operation, ok := sensitiveBashOperation(fields); ok {
		return operation, false
	}
	if safeReadBashCommand(fields) {
		return "", false
	}
	return "", true
}

func sensitiveBashOperation(fields []string) (contractOperation, bool) {
	if len(fields) == 0 {
		return "", false
	}
	if fields[0] == "git" && len(fields) > 1 {
		switch fields[1] {
		case "commit":
			return "commit", true
		case "push":
			return "push", true
		case "merge":
			return "merge", true
		}
	}
	if operation, ok := installerOperation(fields); ok {
		return operation, true
	}
	if operation, ok := deployOperation(fields); ok {
		return operation, true
	}
	if operation, ok := migrationOperation(fields); ok {
		return operation, true
	}
	return "", false
}

func shellWrapper(command string) bool {
	switch command {
	case "sudo", "doas", "env", "command", "xargs", "exec", "eval", "source", ".", "sh", "bash", "zsh", "fish", "ksh", "dash", "python", "python3", "perl", "ruby", "node", "deno", "make", "just", "nohup", "timeout":
		return true
	default:
		return false
	}
}

func safeReadBashCommand(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "pwd", "ls", "cat", "head", "tail", "wc":
		return true
	case "rg", "grep":
		return safeSearchArgs(fields[1:])
	default:
		return false
	}
}

func safeSearchArgs(fields []string) bool {
	for _, field := range fields {
		lower := strings.ToLower(field)
		// These options execute a preprocessor or command for every matching
		// input. A direct search remains read-only; output-only filters do not
		// cross the process boundary and stay allowed.
		for _, prefix := range []string{"--exec", "--pre", "--pre-glob", "--hostname-bin"} {
			if lower == prefix || strings.HasPrefix(lower, prefix+"=") {
				return false
			}
		}
	}
	return true
}

func installerOperation(fields []string) (contractOperation, bool) {
	if len(fields) < 2 {
		return "", false
	}
	installers := map[string]struct{}{
		"brew": {}, "npm": {}, "pnpm": {}, "yarn": {}, "bun": {}, "pip": {}, "pip3": {},
		"uv": {}, "cargo": {}, "go": {}, "gem": {}, "apt": {}, "apt-get": {}, "apk": {},
		"pacman": {}, "dnf": {}, "yum": {},
	}
	if _, ok := installers[fields[0]]; !ok {
		return "", false
	}
	switch fields[1] {
	case "install", "add", "upgrade", "update", "remove", "uninstall":
		return "install", true
	}
	return "", false
}

func deployOperation(fields []string) (contractOperation, bool) {
	if len(fields) == 0 {
		return "", false
	}
	if fields[0] == "deploy" {
		return "deploy", true
	}
	for _, command := range []string{"vercel", "netlify", "fly", "railway", "render", "kubectl", "helm", "terraform", "docker", "gcloud", "aws"} {
		if fields[0] == command && len(fields) > 1 {
			for _, word := range fields[1:] {
				if word == "deploy" || word == "apply" || word == "publish" || word == "push" || word == "upgrade" {
					return "deploy", true
				}
			}
		}
	}
	return "", false
}

func migrationOperation(fields []string) (contractOperation, bool) {
	if len(fields) == 0 {
		return "", false
	}
	if fields[0] == "migrate" || fields[0] == "migration" {
		return "migrate", true
	}
	return "", false
}

func slicesSortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}
