package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/allowance"
	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/internal/procstat"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// ModelRequest is the provider-neutral request used by Atenea's general
// model runtime. The search adapter and this entry point share resolve,
// process containment and the JSONL scanner; only the prompt/schema differ.
type ModelRequest struct {
	Model  string
	Prompt string
	Dir    string
	// These fields bind the ephemeral hook state to the assignment that
	// authorized the turn. They are intentionally carried separately from the
	// prompt, which is model-controlled input and cannot establish authority.
	AssignmentID    string
	WorkflowID      string
	Worktree        string
	PolicyDigest    string
	GrantToken      string
	Schema          map[string]any
	BudgetUSD       float64
	MaxTokens       int
	Timeout         time.Duration
	Sandbox         string
	ReasoningEffort string
	Tools           string
	Builtins        []string
	// Effects and Operations are the authorization carried by the assignment.
	// Operations are one-shot grants; write access alone never implies one.
	Effects    []contract.Effect
	Operations []contract.Operation
}

// ModelAnswer is the Codex result consumed by internal/agent/model.
type ModelAnswer struct {
	Text         string
	Structured   map[string]any
	Spent        contract.Charge
	Notices      []string
	Completeness *float64
	StoppedAt    string
}

// codexNativeFeatures is the native Codex surface disabled for every Atenea
// model turn. An empty Tools/Builtins request must remain genuinely toolless;
// sandboxing alone does not disable native tools.
var codexNativeFeatures = []string{
	"apps", "artifact", "auth_elicitation", "browser_use", "browser_use_external",
	"browser_use_full_cdp_access", "code_mode", "code_mode_host", "computer_use",
	"goals", "image_generation", "in_app_browser", "js_repl", "multi_agent",
	"multi_agent_v2", "plugins", "request_permissions_tool", "shell_snapshot",
	"shell_tool", "tool_call_mcp_elicitation", "tool_suggest", "unified_exec",
	"view_image", "workspace_dependencies",
}

// RunModel executes one general Codex turn. It deliberately has no fallback
// behavior: model selection and retry policy belong to the caller's role
// configuration, and this provider reports one observed invocation.
func (r *Runner) RunModel(ctx context.Context, req ModelRequest) (ModelAnswer, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return ModelAnswer{}, contract.Fail(contract.FailureInvalidInput, "codex model request: prompt is required")
	}
	if req.BudgetUSD < 0 {
		return ModelAnswer{}, contract.Fail(contract.FailureInvalidInput, "codex model request: budget must not be negative")
	}
	if req.MaxTokens < 0 {
		return ModelAnswer{}, contract.Fail(contract.FailureInvalidInput, "codex model request: max_tokens must not be negative")
	}
	if req.Sandbox != "read-only" && req.Sandbox != "workspace-write" {
		return ModelAnswer{}, contract.Fail(contract.FailureInvalidInput,
			"codex model request: sandbox must be read-only or workspace-write")
	}
	if req.Sandbox == "workspace-write" && strings.TrimSpace(req.Dir) == "" {
		return ModelAnswer{}, contract.Fail(contract.FailureInvalidInput,
			"codex model request: workspace-write requires a working directory")
	}
	if err := validateOperations(req.Effects, req.Operations); err != nil {
		return ModelAnswer{}, err
	}
	if _, err := codexMCPOverrides(req.Tools); err != nil {
		return ModelAnswer{}, err
	}
	enabled, err := codexBuiltinFeatures(req.Sandbox, req.Builtins)
	if err != nil {
		return ModelAnswer{}, err
	}
	var schema map[string]any
	if len(req.Schema) > 0 {
		var copyErr error
		schema, copyErr = cloneSchema(req.Schema)
		if copyErr != nil {
			return ModelAnswer{}, contract.Fail(contract.FailureInvalidInput,
				"codex model schema cannot be copied: %v", copyErr)
		}
	}
	root := req.Dir
	if root == "" {
		root, _ = os.Getwd()
	}
	answer, peak, err := r.invokeModel(ctx, root, req, enabled)
	spent := contract.Charge{
		InputTokens:      answer.Usage.InputTokens,
		OutputTokens:     answer.Usage.OutputTokens,
		CacheReadTokens:  answer.Usage.CacheRead,
		CacheWriteTokens: answer.Usage.CacheWrite,
	}
	if answer.CostSeen {
		spent.USD = ptr(answer.CostUSD)
		spent.PricedBy = "codex"
	} else if spent.Tokens() > 0 {
		estimate := allowance.EstimatedUSD(spent.InputTokens, spent.OutputTokens,
			spent.CacheReadTokens, spent.CacheWriteTokens)
		spent.USD = ptr(estimate)
		spent.PricedBy = "estimate:allowance"
	}
	_ = peak // the general model contract currently records tokens and USD only.
	if req.BudgetUSD > 0 && spent.USD == nil {
		return ModelAnswer{Spent: spent}, contract.Fail(contract.FailureUnavailable,
			"codex did not report monetary usage; positive budget cannot be verified")
	}
	// Only provider-reported USD is a known monetary ledger. The conservative
	// estimate above is retained and enforced locally, but must not be relabelled
	// as an observation when published to the commission observer.
	if !contract.ReportCost(ctx, contract.CostUpdate{SpentUSD: answer.CostUSD, Known: answer.CostSeen}) {
		return ModelAnswer{Spent: spent}, contract.Fail(contract.FailurePermissionDenied,
			"codex exceeded its monetary permission during the turn")
	}
	if err != nil {
		return ModelAnswer{Spent: spent}, err
	}
	if spent.USD != nil && req.BudgetUSD > 0 && *spent.USD > req.BudgetUSD {
		return ModelAnswer{Spent: spent}, contract.Fail(contract.FailurePermissionDenied,
			"codex reported a cost above the requested budget")
	}
	if req.MaxTokens > 0 && spent.InputTokens+spent.OutputTokens > req.MaxTokens {
		return ModelAnswer{Spent: spent}, contract.Fail(contract.FailurePermissionDenied,
			"codex reported %d tokens above the requested limit of %d",
			spent.InputTokens+spent.OutputTokens, req.MaxTokens)
	}
	out := ModelAnswer{Text: answer.Message, Spent: spent, Notices: sandboxPolicyNotices(req)}
	if len(schema) == 0 {
		return out, nil
	}
	if strings.TrimSpace(answer.Message) == "" {
		return ModelAnswer{Spent: spent}, contract.Fail(contract.FailureUnavailable,
			"codex completed without a structured answer")
	}
	if err := json.Unmarshal([]byte(answer.Message), &out.Structured); err != nil {
		return ModelAnswer{Spent: spent}, contract.Fail(contract.FailureUnavailable,
			"codex returned invalid structured JSON")
	}
	if err := validateModelSchema(schema, out.Structured, "result"); err != nil {
		return ModelAnswer{Spent: spent}, contract.Fail(contract.FailureUnavailable,
			"codex structured answer does not match schema: %v", err)
	}
	if raw, ok := out.Structured["completeness"].(float64); ok {
		if raw < 0 || raw > 1 {
			return ModelAnswer{Spent: spent}, contract.Fail(contract.FailureUnavailable,
				"codex completeness must be between 0 and 1")
		}
		out.Completeness = &raw
	}
	if stopped, ok := out.Structured["stopped_at"].(string); ok {
		out.StoppedAt = stopped
	}
	return out, nil
}

func sandboxPolicyNotices(req ModelRequest) []string {
	if !needsCodexHook(req) {
		return nil
	}
	names := make([]string, 0, len(req.Operations))
	for _, operation := range req.Operations {
		names = append(names, operation.String())
	}
	sort.Strings(names)
	return []string{"enforcement=codex-pretooluse; hook=active; state=ephemeral; partial=false; operations=" + strings.Join(names, ",")}
}

func needsCodexHook(req ModelRequest) bool {
	return req.Sandbox == "workspace-write" || len(req.Builtins) > 0 ||
		strings.TrimSpace(req.Tools) != "" || len(req.Operations) > 0
}

func cloneSchema(input map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, err
	}
	return output, nil
}

func ptr(value float64) *float64 { return &value }

func (r *Runner) invokeModel(ctx context.Context, root string, req ModelRequest, enabled map[string]bool) (response, int64, error) {
	resolved, err := r.resolve()
	if err != nil {
		return response{}, 0, contract.Fail(contract.FailureUnavailable,
			"codex executable is unavailable for source %q", r.source)
	}
	overrides, err := codexMCPOverrides(req.Tools)
	if err != nil {
		return response{}, 0, err
	}
	var schemaName string
	if len(req.Schema) > 0 {
		rawSchema, err := json.Marshal(req.Schema)
		if err != nil {
			return response{}, 0, contract.Fail(contract.FailureInvalidInput, "codex model schema cannot be encoded")
		}
		schema := make(map[string]any)
		if err := json.Unmarshal(rawSchema, &schema); err != nil {
			return response{}, 0, contract.Fail(contract.FailureInvalidInput, "codex model schema cannot be copied")
		}
		makeStrict(schema)
		encoded, err := json.Marshal(schema)
		if err != nil {
			return response{}, 0, contract.Fail(contract.FailureInvalidInput, "codex model schema cannot be encoded")
		}
		file, err := os.CreateTemp("", "atenea-codex-model-schema-*.json")
		if err != nil {
			return response{}, 0, contract.Fail(contract.FailureUnavailable, "codex model schema could not be prepared")
		}
		schemaName = file.Name()
		defer func() { _ = os.Remove(schemaName) }()
		if _, err := file.Write(encoded); err != nil {
			_ = file.Close()
			return response{}, 0, contract.Fail(contract.FailureUnavailable, "codex model schema could not be written")
		}
		if err := file.Close(); err != nil {
			return response{}, 0, contract.Fail(contract.FailureUnavailable, "codex model schema could not be closed")
		}
	}
	limit := req.Timeout
	if limit <= 0 {
		limit = r.timeout
	}
	turnCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	args := []string{"exec", "--ephemeral", "--sandbox", req.Sandbox,
		"--ignore-user-config", "--ignore-rules", "--cd", root,
		"--json", "--color", "never", "-c", `web_search="disabled"`}
	var hook hookInvocation
	if needsCodexHook(req) {
		hook, err = newHookInvocation(root, req, r.hookBinary)
		if err != nil {
			return response{}, 0, contract.Fail(contract.FailureUnavailable,
				"codex hook enforcement is unavailable")
		}
		defer func() { _ = os.RemoveAll(filepath.Dir(hook.StatePath)) }()
		// --strict-config makes a Codex runtime that does not understand hooks
		// fail before it can execute a tool. The trust flag is required for the
		// ephemeral inline hook to run under Codex's hook trust model. User and
		// project hooks remain ignored by --ignore-user-config; this is only the
		// host-owned Atenea binary whose state and binding were just validated.
		// Without strict config and explicit hook trust, an older or untrusted
		// runtime could silently skip the inline policy and make partial=true a
		// lie.
		args = append(args, "--strict-config", "--dangerously-bypass-hook-trust",
			"-c", "features.hooks=true", "-c",
			"hooks.PreToolUse="+hookConfigOverride(hook.Command))
	}
	// The sandbox's network switch is derived from the assignment's external
	// effect. It is deliberately independent of write: editing the worktree
	// never authorizes a network operation.
	network := "false"
	for _, effect := range req.Effects {
		if effect == contract.EffectExternal {
			network = "true"
			break
		}
	}
	args = append(args, "-c", "sandbox_workspace_write.network_access="+network)
	for _, feature := range codexNativeFeatures {
		if enabled[feature] {
			args = append(args, "--enable", feature)
		} else {
			args = append(args, "--disable", feature)
		}
	}
	args = append(args, overrides...)
	if strings.TrimSpace(req.Model) != "" {
		args = append(args, "--model", req.Model)
	}
	if strings.TrimSpace(req.ReasoningEffort) != "" {
		args = append(args, "-c", "model_reasoning_effort="+strings.TrimSpace(req.ReasoningEffort))
	}
	if schemaName != "" {
		args = append(args, "--output-schema", schemaName)
	}
	cmd := exec.CommandContext(turnCtx, resolved.Path, args...)
	cmd.Dir = root
	// A repository or parent process may set a ripgrep config containing
	// command-executing options such as --pre. The hook validates the explicit
	// argv, so the managed provider must also neutralize inherited hidden argv.
	cmd.Env = append(os.Environ(), "RIPGREP_CONFIG_PATH=/dev/null")
	cmd.Stdin = strings.NewReader(req.Prompt)
	procgroup.Contain(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return response{}, 0, contract.Fail(contract.FailureUnavailable, "codex model output stream could not be opened")
	}
	stderr := procgroup.NewCapture(func() { _ = procgroup.Kill(cmd) })
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return response{}, 0, classifyFailure("", err)
	}
	var parsed eventStream
	unreadable, stopped, scanErr := scanEvents(stdout, &parsed, func() bool {
		if !parsed.CostSeen || contract.ReportCost(turnCtx, contract.CostUpdate{SpentUSD: parsed.CostUSD, Known: true}) {
			return true
		}
		cancel()
		return false
	})
	runErr := cmd.Wait()
	peak := procstat.PeakRSS(cmd.ProcessState)
	if scanErr != nil {
		return parsedResponse(parsed, unreadable), peak, scanErr
	}
	if stopped {
		return parsedResponse(parsed, unreadable), peak, contract.Fail(contract.FailurePermissionDenied,
			"codex exceeded its monetary permission during the event stream")
	}
	if ctxErr := turnCtx.Err(); ctxErr != nil {
		return parsedResponse(parsed, unreadable), peak, contract.Stopped(ctxErr, "codex", limit)
	}
	if runErr != nil {
		return parsedResponse(parsed, unreadable), peak, classifyFailure(parsed.ErrorText+" "+stderr.String(), runErr)
	}
	if parsed.TerminalError != "" {
		// Provider text can contain prompts, paths or credentials. Classification
		// keeps the public failure useful without echoing the raw terminal event.
		return parsedResponse(parsed, unreadable), peak,
			classifyFailure(parsed.TerminalError, errors.New("codex terminal failure"))
	}
	if !parsed.TurnCompleted {
		return parsedResponse(parsed, unreadable), peak, contract.Fail(contract.FailureUnavailable,
			"codex completed without a turn.completed event")
	}
	if parsed.Message == "" {
		return parsedResponse(parsed, unreadable), peak, contract.Fail(contract.FailureUnavailable,
			"codex completed without a final JSON answer")
	}
	return parsedResponse(parsed, unreadable), peak, nil
}

// codexBuiltinFeatures is the closed translation between Atenea's portable
// surface names and Codex's feature switches. Codex has no separate native
// Read/Glob/Grep feature: its shell tool is the only supported filesystem
// surface, so read-only requests get that feature under a read-only sandbox.
// An empty list remains genuinely toolless because every native feature is
// explicitly disabled below.
func codexBuiltinFeatures(sandbox string, builtins []string) (map[string]bool, error) {
	enabled := make(map[string]bool)
	for _, builtin := range builtins {
		name := strings.TrimSpace(builtin)
		if name == "" {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"codex model backend received an empty builtin name")
		}
		var feature string
		switch name {
		case "Read", "Glob", "Grep":
			feature = "shell_tool"
		case "Shell", "ApplyPatch":
			if sandbox != "workspace-write" {
				return nil, contract.Fail(contract.FailurePermissionDenied,
					"codex builtin %s requires workspace-write", name)
			}
			feature = "shell_tool"
		default:
			return nil, contract.Fail(contract.FailureUnavailable,
				"codex model backend received an unsupported builtin surface")
		}
		enabled[feature] = true
	}
	return enabled, nil
}

// validateOperations enforces the part of the assignment contract that the
// Codex adapter can check before spawning an untrusted provider process.
// EffectWrite is necessary for every sensitive operation, while operations
// that leave the machine also need EffectExternal. The adapter never guesses
// an operation from a write effect.
func validateOperations(effects []contract.Effect, operations []contract.Operation) error {
	hasWrite, hasExternal := false, false
	for _, effect := range effects {
		hasWrite = hasWrite || effect == contract.EffectWrite
		hasExternal = hasExternal || effect == contract.EffectExternal
	}
	for _, operation := range operations {
		if !operation.Known() {
			return contract.Fail(contract.FailureInvalidInput,
				"codex model backend received an unsupported operation")
		}
		if !hasWrite {
			return contract.Fail(contract.FailurePermissionDenied,
				"codex sensitive operation requires explicit write authorization")
		}
		if operationNeedsExternal(operation) && !hasExternal {
			return contract.Fail(contract.FailurePermissionDenied,
				"codex sensitive operation requires explicit external authorization")
		}
	}
	return nil
}

func operationNeedsExternal(operation contract.Operation) bool {
	switch operation {
	case contract.OperationPush, contract.OperationInstall,
		contract.OperationDeploy, contract.OperationMigrate:
		return true
	default:
		return false
	}
}

type codexMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func codexMCPOverrides(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var config struct {
		Servers map[string]codexMCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil || len(config.Servers) == 0 {
		return nil, contract.Fail(contract.FailureUnavailable,
			"codex model backend received an unsupported MCP tool surface")
	}
	names := make([]string, 0, len(config.Servers))
	for name, server := range config.Servers {
		if name == "" || !validMCPName(name) || strings.TrimSpace(server.Command) == "" {
			return nil, contract.Fail(contract.FailureUnavailable,
				"codex model backend received an unsupported MCP tool surface")
		}
		for _, arg := range server.Args {
			if strings.ContainsRune(arg, '\x00') {
				return nil, contract.Fail(contract.FailureUnavailable,
					"codex model backend received an unsupported MCP tool surface")
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names)*2)
	for _, name := range names {
		server := config.Servers[name]
		out = append(out, "-c", "mcp_servers."+name+".command="+strconv.Quote(server.Command))
		args := server.Args
		if args == nil {
			args = []string{}
		}
		argsJSON, _ := json.Marshal(args)
		out = append(out, "-c", "mcp_servers."+name+".args="+string(argsJSON))
	}
	return out, nil
}

func validMCPName(name string) bool {
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func validateModelSchema(schema map[string]any, value any, path string) error {
	if constant, ok := schema["const"]; ok && !reflect.DeepEqual(constant, value) {
		return fmt.Errorf("%s has the wrong constant", path)
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, option := range enum {
			if reflect.DeepEqual(option, value) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s is outside the schema enum", path)
		}
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok || object == nil {
			return fmt.Errorf("%s must be an object", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		switch required := schema["required"].(type) {
		case []any:
			for _, item := range required {
				name, _ := item.(string)
				if _, exists := object[name]; !exists {
					return fmt.Errorf("%s.%s is required", path, name)
				}
			}
		case []string:
			for _, name := range required {
				if _, exists := object[name]; !exists {
					return fmt.Errorf("%s.%s is required", path, name)
				}
			}
		}
		for name, child := range object {
			property, exists := properties[name]
			if !exists {
				closed, declared := schema["additionalProperties"].(bool)
				if declared && !closed {
					return fmt.Errorf("%s.%s is not supported", path, name)
				}
				continue
			}
			childSchema, ok := property.(map[string]any)
			if ok {
				if err := validateModelSchema(childSchema, child, path+"."+name); err != nil {
					return err
				}
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for index, item := range items {
			if err := validateModelSchema(itemSchema, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case "number":
		number, ok := value.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("%s must be a finite number", path)
		}
		if err := validateNumericBounds(schema, number, path); err != nil {
			return err
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number {
			return fmt.Errorf("%s must be a finite integer", path)
		}
		if err := validateNumericBounds(schema, number, path); err != nil {
			return err
		}
	}
	return nil
}

// ValidateModelSchema exposes the same closed JSON-schema subset used by the
// one-shot Codex adapter to the native App Server path. Keeping one validator
// prevents a visible turn from accepting a shape that an invisible turn
// rejects.
func ValidateModelSchema(schema map[string]any, value any) error {
	return validateModelSchema(schema, value, "result")
}

func validateNumericBounds(schema map[string]any, value float64, path string) error {
	if minimum, ok := schemaNumber(schema["minimum"]); ok && value < minimum {
		return fmt.Errorf("%s is below minimum %.g", path, minimum)
	}
	if maximum, ok := schemaNumber(schema["maximum"]); ok && value > maximum {
		return fmt.Errorf("%s is above maximum %.g", path, maximum)
	}
	return nil
}

func schemaNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, !math.IsNaN(number) && !math.IsInf(number, 0)
	case float32:
		return float64(number), !math.IsNaN(float64(number)) && !math.IsInf(float64(number), 0)
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	default:
		return 0, false
	}
}

func parsedResponse(parsed eventStream, unreadable int) response {
	return response{Structured: map[string]any{}, Usage: parsed.Usage,
		CostUSD: parsed.CostUSD, CostSeen: parsed.CostSeen, Message: parsed.Message,
		Unreadable: unreadable}
}
