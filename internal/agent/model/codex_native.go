package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	adaptercodex "github.com/Tutitoos/atenea/internal/adapter/codex"
	"github.com/Tutitoos/atenea/internal/allowance"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// turnCodexNative is the visible Codex path. It deliberately owns one
// durable App Server client and one thread per model Client: a follow-up can
// only use the thread returned by this client, and a caller cannot smuggle a
// different thread into the same session. The one-shot exec path remains
// available only to requests that explicitly say they are invisible/CI.
func (c *Client) turnCodexNative(parent context.Context, dir string, timeout time.Duration, req Request) (Answer, error) {
	c.nativeTurnMu.Lock()
	defer c.nativeTurnMu.Unlock()

	ctx := parent
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
		defer cancel()
	}
	modelName, err := c.modelFor(req.Role)
	if err != nil {
		return Answer{}, err
	}
	sandbox, err := nativeSandbox(req)
	if err != nil {
		return Answer{}, err
	}
	if strings.TrimSpace(req.Tools) != "" {
		return Answer{}, contract.Fail(contract.FailureUnavailable,
			"codex native App Server cannot yet enforce custom MCP tools; refusing to ignore the requested surface")
	}
	surface, err := nativeSurfaceDigest(req)
	if err != nil {
		return Answer{}, err
	}

	c.nativeMu.Lock()
	threadID := c.nativeThreadID
	threadModel := c.nativeThreadModel
	threadSandbox := c.nativeThreadSandbox
	threadSurface := c.nativeThreadSurface
	c.nativeMu.Unlock()
	if threadID != "" && (threadModel != modelName || threadSandbox != sandbox || threadSurface != surface) {
		return Answer{}, contract.Fail(contract.FailurePermissionDenied,
			"codex native continuation cannot change model, sandbox, or authorized tool surface")
	}
	if req.ThreadID != "" {
		if threadID != "" && req.ThreadID != threadID {
			return Answer{}, contract.Fail(contract.FailurePermissionDenied,
				"codex native thread %q is not the durable thread owned by this client", req.ThreadID)
		}
	}
	native, err := c.ensureNativeInitialized(ctx)
	if err != nil {
		return Answer{}, err
	}
	if threadID == "" {
		hook, hookErr := adaptercodex.PrepareNativeHook(dir, adaptercodex.ModelRequest{
			Model: modelName, Dir: dir, Sandbox: sandbox, Builtins: req.Builtins,
			Effects: req.Effects, Operations: req.Operations, AssignmentID: req.AssignmentID,
			WorkflowID: req.WorkflowID, Worktree: req.Worktree, PolicyDigest: req.PolicyDigest, GrantToken: req.GrantToken,
		})
		if hookErr != nil {
			return Answer{}, contract.Fail(contract.FailureUnavailable, "codex native hook enforcement is unavailable: %v", hookErr)
		}
		var started adaptercodex.ThreadStarted
		var startErr error
		if req.ThreadID != "" {
			started, startErr = native.ThreadResume(ctx, adaptercodex.ThreadResumeRequest{
				ThreadID: req.ThreadID, Model: modelName, Workdir: dir, Sandbox: sandbox,
				ApprovalPolicy: "never", DeveloperInstructions: nativeInstructions(req),
				Config: hook.Config, VisibilityRequired: true,
			})
		} else {
			started, startErr = native.ThreadStart(ctx, adaptercodex.ThreadStartRequest{
				Model: modelName, Workdir: dir, Sandbox: sandbox, ApprovalPolicy: "never",
				DeveloperInstructions: nativeInstructions(req), Config: hook.Config, VisibilityRequired: true,
			})
		}
		if startErr != nil {
			_ = hook.Close()
			if ctx.Err() != nil {
				_ = c.closeUncertainNative(native, "thread/start timed out without a thread identity")
				return Answer{}, contract.Fail(contract.FailureTimeout, "codex native thread/start timed out and App Server was closed")
			}
			return Answer{}, startErr
		}
		threadID = started.Thread.ID
		c.nativeMu.Lock()
		if c.nativeThreadID != "" && c.nativeThreadID != threadID {
			c.nativeMu.Unlock()
			return Answer{}, errors.New("codex native server returned a second thread for one client")
		}
		c.nativeThreadID = threadID
		c.nativeThreadModel = modelName
		c.nativeThreadSandbox = sandbox
		c.nativeThreadSurface = surface
		c.nativeHook = hook
		c.nativeMu.Unlock()
	}

	started, err := native.TurnStart(ctx, adaptercodex.TurnStartRequest{
		ThreadID: threadID, Prompt: req.sentPrompt(), Model: modelName,
		ReasoningEffort: req.ReasoningEffort, OutputSchema: req.Schema, VisibilityRequired: true,
	})
	if err != nil {
		if ctx.Err() != nil {
			_ = c.closeUncertainNative(native, "turn/start timed out without a turn identity")
			return Answer{ThreadID: threadID}, contract.Fail(contract.FailureTimeout, "codex native turn/start timed out and App Server was closed")
		}
		return Answer{ThreadID: threadID}, err
	}
	turnID := started.Turn.ID
	text, usage, err := c.waitNativeTurn(ctx, native, threadID, turnID, modelName)
	receipt := native.Receipt()
	answer := Answer{ThreadID: threadID, TurnID: turnID, UsageRevision: native.UsageRevision(), Spent: chargeFromNativeUsage(usage),
		RequestedModel: receipt.RequestedModel, ObservedModel: receipt.ObservedModel,
		RequestedReasoningEffort: receipt.RequestedEffort, ObservedReasoningEffort: receipt.ObservedEffort}
	c.nativeMu.Lock()
	if c.nativeHook.Notice != "" {
		answer.Notices = append(answer.Notices, c.nativeHook.Notice)
	}
	c.nativeMu.Unlock()
	if err != nil {
		return answer, err
	}
	answer.Text = text
	answer.Passes = 1
	if len(req.Schema) > 0 {
		shape := []byte(strings.TrimSpace(text))
		if len(shape) == 0 || shape[0] != '{' || !json.Valid(shape) {
			return answer, contract.Fail(contract.FailureInvalidInput,
				"codex native agent message is not a JSON object for the requested schema")
		}
		var value any
		if err := json.Unmarshal(shape, &value); err != nil {
			return answer, contract.Fail(contract.FailureInvalidInput, "codex native structured answer is not valid JSON: %v", err)
		}
		if err := adaptercodex.ValidateModelSchema(req.Schema, value); err != nil {
			return answer, contract.Fail(contract.FailureInvalidInput, "codex native structured answer does not match the requested schema: %v", err)
		}
		answer.Structured = append(json.RawMessage(nil), shape...)
		if claimed, ok := claimOf(envelope{StructuredOutput: shape}); ok {
			answer.Completeness, answer.StoppedAt = claimed.reported()
		}
	}
	return enforceMaxTokens(answer, req.MaxTokens, nil)
}

func (c *Client) closeUncertainNative(native *adaptercodex.Client, reason string) error {
	closeErr := native.Close()
	c.nativeMu.Lock()
	hook := c.nativeHook
	c.native = nil
	c.nativeInitialized = false
	c.nativeHook = adaptercodex.NativeHook{}
	c.nativeInitErr = contract.Fail(contract.FailureUnavailable, "codex native App Server was closed: %s", reason)
	c.nativeMu.Unlock()
	_ = hook.Close()
	return closeErr
}

func nativeInstructions(req Request) string {
	allowed := "no named built-in tools"
	if len(req.Builtins) > 0 {
		allowed = strings.Join(req.Builtins, ", ")
	}
	return "Follow the assigned role and output schema. Do not request permission escalation. The declared built-in capability surface is: " + allowed + "."
}

func nativeSurfaceDigest(req Request) (string, error) {
	operations := make([]string, 0, len(req.Operations))
	for _, operation := range req.Operations {
		operations = append(operations, operation.String())
	}
	return adaptercodex.AppServerDigest(map[string]any{
		"builtins": req.Builtins, "operations": operations, "assignment_id": req.AssignmentID,
		"workflow_id": req.WorkflowID, "worktree": req.Worktree, "policy_digest": req.PolicyDigest, "grant_token": req.GrantToken,
	})
}

func nativeSandbox(req Request) (string, error) {
	// App Server's permissions field names a configured server profile. It is
	// not the custom agent TOML directory. The root turn therefore carries the
	// actual sandbox mode, whose vocabulary is validated by the same rule as
	// the invisible adapter.
	sandbox, err := sandboxFor(req.Role, req.Effects)
	if err != nil {
		return "", err
	}
	return sandbox, nil
}

func (c *Client) ensureNativeInitialized(ctx context.Context) (*adaptercodex.Client, error) {
	c.nativeMu.Lock()
	if c.nativeInitErr != nil {
		err := c.nativeInitErr
		c.nativeMu.Unlock()
		return nil, err
	}
	if c.nativeInitialized && c.native != nil {
		native := c.native
		c.nativeMu.Unlock()
		return native, nil
	}
	if c.nativeOptions == nil {
		err := contract.Fail(contract.FailureUnavailable, "codex native App Server is not configured")
		c.nativeInitErr = err
		c.nativeMu.Unlock()
		return nil, err
	}
	options := *c.nativeOptions
	c.nativeMu.Unlock()

	native, err := adaptercodex.NewAppServerClient(options)
	if err == nil {
		_, err = native.Initialize(ctx, adaptercodex.InitializeRequest{
			ClientInfo:   adaptercodex.ClientInfo{Name: "atenea", Version: "atenea-native/1"},
			Capabilities: map[string]any{},
		})
	}
	c.nativeMu.Lock()
	defer c.nativeMu.Unlock()
	if err != nil {
		if native != nil {
			_ = native.Close()
		}
		c.nativeInitErr = err
		return nil, err
	}
	c.native = native
	c.nativeInitialized = true
	return native, nil
}

func (c *Client) waitNativeTurn(ctx context.Context, native *adaptercodex.Client, threadID, turnID, requestedModel string) (string, adaptercodex.TokenUsage, error) {
	observed, err := native.WaitTurn(ctx, threadID, turnID)
	if err != nil {
		if stopErr := c.stopNativeTurn(native, threadID, turnID); stopErr != nil {
			return "", adaptercodex.TokenUsage{}, contract.Fail(contract.FailureTimeout, "codex native turn %s timed out and cancellation could not be confirmed: %v", turnID, stopErr)
		}
		return "", adaptercodex.TokenUsage{}, contract.Fail(contract.FailureTimeout, "codex native turn %s was interrupted after timeout: %v", turnID, err)
	}
	if observed.Reroute != nil && (observed.Reroute.FromModel != requestedModel || observed.Reroute.ToModel != requestedModel) {
		return observed.Message, observed.Usage, fmt.Errorf("%w: thread=%s turn=%s from=%s to=%s requested=%s", adaptercodex.ErrModelRerouted, threadID, turnID, observed.Reroute.FromModel, observed.Reroute.ToModel, requestedModel)
	}
	if observed.Completed.Turn.Status != "completed" {
		return observed.Message, observed.Usage, contract.Fail(contract.FailureUnavailable, "codex native turn %s completed with status %q", turnID, observed.Completed.Turn.Status)
	}
	if strings.TrimSpace(observed.Message) == "" {
		return observed.Message, observed.Usage, contract.Fail(contract.FailureInvalidInput, "codex native turn %s completed without an agent message", turnID)
	}
	return observed.Message, observed.Usage, nil
}

func (c *Client) stopNativeTurn(native *adaptercodex.Client, threadID, turnID string) error {
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := native.TurnInterrupt(stopCtx, threadID, turnID); err == nil {
		if _, waitErr := native.WaitTurn(stopCtx, threadID, turnID); waitErr == nil {
			return nil
		}
	}
	closeErr := native.Close()
	c.nativeMu.Lock()
	hook := c.nativeHook
	c.nativeHook = adaptercodex.NativeHook{}
	c.nativeInitErr = contract.Fail(contract.FailureUnavailable, "codex native App Server was closed after unconfirmed cancellation")
	c.nativeMu.Unlock()
	_ = hook.Close()
	if closeErr != nil {
		return fmt.Errorf("closing App Server after cancellation failure: %w", closeErr)
	}
	return errors.New("app server closed because turn interruption was not confirmed")
}

func chargeFromNativeUsage(usage adaptercodex.TokenUsage) contract.Charge {
	// App Server's total is cumulative for the thread, while last is the
	// physical turn represented by this receipt. Cached reads and cache writes
	// are components of inputTokens, so split them out before Charge.Tokens
	// adds each billing category.
	counts := usage.Last
	uncachedInput := counts.InputTokens - counts.CachedInputTokens - counts.CacheWriteInputTokens
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	charge := contract.Charge{
		InputTokens: int(uncachedInput), OutputTokens: int(counts.OutputTokens),
		CacheReadTokens: int(counts.CachedInputTokens), CacheWriteTokens: int(counts.CacheWriteInputTokens),
	}
	if charge.Tokens() > 0 {
		usd := allowance.EstimatedUSD(charge.InputTokens, charge.OutputTokens, charge.CacheReadTokens, charge.CacheWriteTokens)
		charge.USD = &usd
		charge.PricedBy = "estimate:codex-app-server-usage"
	}
	return charge
}
