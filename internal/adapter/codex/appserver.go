package codex

// This file is the native Codex App Server transport. It is intentionally
// separate from model.go: `codex exec` is a one-shot fallback for explicitly
// invisible/CI work, while App Server owns durable threads and emits the
// identity/usage events needed by workflow receipts.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const appServerCommand = "app-server"

// ErrModelRerouted is part of ATENEA's public orchestration contract.
var ErrModelRerouted = errors.New("codex app server rerouted the requested model")

// RPCError is part of ATENEA's public orchestration contract.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("codex app server rpc error %d: %s", e.Code, e.Message)
}

// Transport is part of ATENEA's public orchestration contract.
type Transport interface {
	Call(context.Context, string, any) (json.RawMessage, error)
	Notify(context.Context, string, any) error
	Close() error
}

// Notification is part of ATENEA's public orchestration contract.
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	// Revision is Atenea-local sequencing for usage notifications. The native
	// wire has no usage_revision field; keeping it on the typed event avoids
	// pretending it was server evidence while making the event consumable by
	// reconnecting callers.
	Revision uint64 `json:"-"`
}

// EventHandler is part of ATENEA's public orchestration contract.
type EventHandler interface {
	SetNotificationHandler(func(Notification))
}

// AppServerOptions is part of ATENEA's public orchestration contract.
type AppServerOptions struct {
	Transport       Transport
	Binary          string
	Command         []string
	Timeout         time.Duration
	NativeTransport bool
}

// Client is part of ATENEA's public orchestration contract.
type Client struct {
	transport     Transport
	native        bool
	mu            sync.Mutex
	closed        bool
	initialized   bool
	threadID      string
	receipt       ExecutionReceipt
	reroutes      map[string]ModelRerouted
	usageRevision uint64
	events        chan Notification
	usageEvents   chan TokenUsageUpdated
	turns         map[string]*TurnObservation
	turnWake      chan struct{}
}

// TurnObservation is durable state accumulated for one exact App Server
// turn. Events remains a best-effort diagnostic stream; this state is what a
// production caller waits on so terminal notifications cannot be dropped.
type TurnObservation struct {
	Message       string
	Usage         TokenUsage
	UsageRevision uint64
	Reroute       *ModelRerouted
	Completed     *TurnCompleted
}

// ExecutionReceipt is part of ATENEA's public orchestration contract.
type ExecutionReceipt struct {
	RequestedModel             string   `json:"requested_model,omitempty"`
	ObservedModel              string   `json:"observed_model,omitempty"`
	RequestedEffort            string   `json:"requested_effort,omitempty"`
	ObservedEffort             string   `json:"observed_effort,omitempty"`
	RequestedPermissions       []string `json:"requested_permissions,omitempty"`
	RequestedPermissionProfile string   `json:"requested_permission_profile,omitempty"`
	ObservedProtocol           string   `json:"observed_protocol,omitempty"`
	ObservedUserAgent          string   `json:"observed_user_agent,omitempty"`
	ModelRerouted              bool     `json:"model_rerouted"`
}

// ClientInfo is part of ATENEA's public orchestration contract.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeRequest is part of ATENEA's public orchestration contract.
type InitializeRequest struct {
	ClientInfo   ClientInfo     `json:"clientInfo"`
	Capabilities map[string]any `json:"capabilities"`
}

// InitializeResult is part of ATENEA's public orchestration contract.
type InitializeResult struct {
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
	UserAgent      string `json:"userAgent"`
}

// Model is part of ATENEA's public orchestration contract.
type Model struct {
	ID                        string                  `json:"id"`
	Model                     string                  `json:"model"`
	DisplayName               string                  `json:"displayName"`
	DefaultReasoningEffort    string                  `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []ReasoningEffortOption `json:"supportedReasoningEfforts"`
}

// ReasoningEffortOption is part of ATENEA's public orchestration contract.
type ReasoningEffortOption struct {
	Description     string `json:"description"`
	ReasoningEffort string `json:"reasoningEffort"`
}

// ModelListResult is part of ATENEA's public orchestration contract.
type ModelListResult struct {
	Data       []Model `json:"data"`
	NextCursor string  `json:"nextCursor,omitempty"`
}

// ModelProviderCapabilities is part of ATENEA's public orchestration contract.
type ModelProviderCapabilities struct {
	ImageGeneration bool `json:"imageGeneration"`
	NamespaceTools  bool `json:"namespaceTools"`
	WebSearch       bool `json:"webSearch"`
}

// ThreadStartRequest is part of ATENEA's public orchestration contract.
type ThreadStartRequest struct {
	Model   string
	Workdir string
	// Permissions is retained for source compatibility and rejected when set;
	// App Server 0.153.4 accepts Sandbox and ApprovalPolicy instead.
	Permissions           string
	Sandbox               string
	ApprovalPolicy        string
	DeveloperInstructions string
	Config                map[string]any
	VisibilityRequired    bool
}

// ThreadResumeRequest is part of ATENEA's public orchestration contract.
type ThreadResumeRequest struct {
	ThreadID string
	Model    string
	Workdir  string
	// Permissions is retained for source compatibility and rejected when set.
	Permissions           string
	Sandbox               string
	ApprovalPolicy        string
	DeveloperInstructions string
	Config                map[string]any
	VisibilityRequired    bool
}

// ThreadForkRequest creates a native child thread through App Server's
// thread/fork method. ATENEA remains responsible for specialist roles,
// concurrency limits, and the durable parent-child workflow mapping.
type ThreadForkRequest struct {
	ParentThreadID        string
	Model                 string
	Workdir               string
	Sandbox               string
	ApprovalPolicy        string
	DeveloperInstructions string
	Config                map[string]any
	VisibilityRequired    bool
}

// ThreadStarted is part of ATENEA's public orchestration contract.
type ThreadStarted struct {
	Thread          Thread `json:"thread"`
	Model           string `json:"model"`
	ModelProvider   string `json:"modelProvider"`
	ReasoningEffort string `json:"reasoningEffort"`
}

// TurnStartRequest is part of ATENEA's public orchestration contract.
type TurnStartRequest struct {
	ThreadID        string
	Prompt          string
	Model           string
	ReasoningEffort string
	// Permissions is retained for source compatibility and rejected when set.
	Permissions        string
	OutputSchema       map[string]any
	VisibilityRequired bool
}

// TurnStarted is part of ATENEA's public orchestration contract.
type TurnStarted struct {
	Turn Turn `json:"turn"`
}

// TokenCounts is part of ATENEA's public orchestration contract.
type TokenCounts struct {
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
}

// TokenUsage is part of ATENEA's public orchestration contract.
type TokenUsage struct {
	Last               TokenCounts `json:"last"`
	Total              TokenCounts `json:"total"`
	ModelContextWindow int64       `json:"modelContextWindow"`
}

// TokenUsageUpdated is part of ATENEA's public orchestration contract.
type TokenUsageUpdated struct {
	ThreadID   string     `json:"threadId"`
	TurnID     string     `json:"turnId"`
	TokenUsage TokenUsage `json:"tokenUsage"`
	Revision   uint64     `json:"-"`
}

// ModelRerouted is part of ATENEA's public orchestration contract.
type ModelRerouted struct {
	FromModel string `json:"fromModel"`
	ToModel   string `json:"toModel"`
	Reason    string `json:"reason"`
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
}

// AgentMessageItem is part of ATENEA's public orchestration contract.
type AgentMessageItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

// ItemCompleted is part of ATENEA's public orchestration contract.
type ItemCompleted struct {
	ThreadID string           `json:"threadId"`
	TurnID   string           `json:"turnId"`
	Item     AgentMessageItem `json:"item"`
}

// TurnCompleted is part of ATENEA's public orchestration contract.
type TurnCompleted struct {
	ThreadID string `json:"threadId"`
	Turn     Turn   `json:"turn"`
}

// ThreadListResult is part of ATENEA's public orchestration contract.
type ThreadListResult struct {
	Data            []Thread `json:"data"`
	NextCursor      string   `json:"nextCursor,omitempty"`
	BackwardsCursor string   `json:"backwardsCursor,omitempty"`
}

// Thread is part of ATENEA's public orchestration contract.
type Thread struct {
	ID     string       `json:"id"`
	Status ThreadStatus `json:"status"`
}

// ThreadStatus is part of ATENEA's public orchestration contract.
type ThreadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags,omitempty"`
}

// Turn is part of ATENEA's public orchestration contract.
type Turn struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
	Status   string `json:"status"`
}

// NewAppServerClient is part of ATENEA's public orchestration contract.
func NewAppServerClient(opts AppServerOptions) (*Client, error) {
	transport := opts.Transport
	if transport == nil {
		binary := strings.TrimSpace(opts.Binary)
		if binary == "" {
			binary = DefaultBinary
		}
		args := slices.Clone(opts.Command)
		if len(args) == 0 {
			args = []string{"--dangerously-bypass-hook-trust", appServerCommand, "--strict-config"}
		}
		var err error
		transport, err = NewProcessTransport(binary, args...)
		if err != nil {
			return nil, err
		}
	}
	c := &Client{transport: transport, native: opts.NativeTransport || opts.Transport == nil, reroutes: make(map[string]ModelRerouted), events: make(chan Notification, 32), usageEvents: make(chan TokenUsageUpdated, 32), turns: make(map[string]*TurnObservation), turnWake: make(chan struct{}, 1)}
	if source, ok := transport.(EventHandler); ok {
		source.SetNotificationHandler(c.handleNotification)
	}
	return c, nil
}

// Events is part of ATENEA's public orchestration contract.
func (c *Client) Events() <-chan Notification { return c.events }

// UsageEvents exposes token usage with Atenea's locally derived monotonic
// revision. The native notification itself has no usage_revision field.
func (c *Client) UsageEvents() <-chan TokenUsageUpdated { return c.usageEvents }

// WaitTurn waits for and consumes the terminal observation of one turn.
// Notifications received before this call remain in the correlated state.
func (c *Client) WaitTurn(ctx context.Context, threadID, turnID string) (TurnObservation, error) {
	key := rerouteKey(threadID, turnID)
	for {
		c.mu.Lock()
		state := c.turns[key]
		if state != nil && state.Completed != nil {
			out := *state
			if state.Reroute != nil {
				route := *state.Reroute
				out.Reroute = &route
			}
			completed := *state.Completed
			out.Completed = &completed
			delete(c.turns, key)
			c.mu.Unlock()
			return out, nil
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return TurnObservation{}, ctx.Err()
		case <-c.turnWake:
		}
	}
}

// Close is part of ATENEA's public orchestration contract.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.transport.Close()
}

// Initialize is part of ATENEA's public orchestration contract.
func (c *Client) Initialize(ctx context.Context, req InitializeRequest) (InitializeResult, error) {
	if strings.TrimSpace(req.ClientInfo.Name) == "" || strings.TrimSpace(req.ClientInfo.Version) == "" {
		return InitializeResult{}, errors.New("codex app server: client identity is required")
	}
	if req.Capabilities == nil {
		req.Capabilities = map[string]any{}
	}
	result, err := c.call(ctx, "initialize", req)
	if err != nil {
		return InitializeResult{}, err
	}
	var out InitializeResult
	if err := json.Unmarshal(result, &out); err != nil {
		return InitializeResult{}, errors.New("codex app server: malformed initialize result")
	}
	if out.CodexHome == "" || out.PlatformFamily == "" || out.PlatformOS == "" || out.UserAgent == "" {
		return InitializeResult{}, errors.New("codex app server: initialize result missing required runtime identity")
	}
	if err := c.transport.Notify(ctx, "initialized", nil); err != nil {
		return InitializeResult{}, fmt.Errorf("codex app server: initialized notification failed: %w", err)
	}
	c.mu.Lock()
	c.initialized = true
	c.receipt.ObservedUserAgent = out.UserAgent
	c.mu.Unlock()
	return out, nil
}

// ModelList is part of ATENEA's public orchestration contract.
func (c *Client) ModelList(ctx context.Context) (ModelListResult, error) {
	result, err := c.call(ctx, "model/list", map[string]any{})
	if err != nil {
		return ModelListResult{}, err
	}
	var out ModelListResult
	if err := json.Unmarshal(result, &out); err != nil {
		return ModelListResult{}, fmt.Errorf("codex app server: malformed model/list result: %w", err)
	}
	if out.Data == nil {
		return ModelListResult{}, errors.New("codex app server: model/list result missing data")
	}
	return out, nil
}

// ModelProviderCapabilitiesRead is part of ATENEA's public orchestration contract.
func (c *Client) ModelProviderCapabilitiesRead(ctx context.Context, _ ...string) (ModelProviderCapabilities, error) {
	result, err := c.call(ctx, "modelProvider/capabilities/read", map[string]any{})
	if err != nil {
		return ModelProviderCapabilities{}, err
	}
	var out ModelProviderCapabilities
	if err := json.Unmarshal(result, &out); err != nil {
		return ModelProviderCapabilities{}, fmt.Errorf("codex app server: malformed provider capabilities: %w", err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(result, &fields) != nil || fields["imageGeneration"] == nil || fields["namespaceTools"] == nil || fields["webSearch"] == nil {
		return ModelProviderCapabilities{}, errors.New("codex app server: malformed provider capabilities")
	}
	return out, nil
}

// ThreadStart is part of ATENEA's public orchestration contract.
func (c *Client) ThreadStart(ctx context.Context, req ThreadStartRequest) (ThreadStarted, error) {
	if req.VisibilityRequired && !c.native {
		return ThreadStarted{}, errors.New("codex app server: visibility_required requires native App Server transport")
	}
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(req.Sandbox) == "" || strings.TrimSpace(req.ApprovalPolicy) == "" {
		return ThreadStarted{}, errors.New("codex app server: thread/start requires model, sandbox and approval policy")
	}
	if strings.TrimSpace(req.Permissions) != "" {
		return ThreadStarted{}, errors.New("codex app server: permissions is unsupported; use sandbox and approval policy")
	}
	params := map[string]any{
		"model":     req.Model,
		"ephemeral": false,
	}
	if req.Workdir != "" {
		params["cwd"] = req.Workdir
	}
	if strings.TrimSpace(req.Sandbox) != "" {
		params["sandbox"] = req.Sandbox
	}
	if strings.TrimSpace(req.ApprovalPolicy) != "" {
		params["approvalPolicy"] = req.ApprovalPolicy
	}
	if strings.TrimSpace(req.DeveloperInstructions) != "" {
		params["developerInstructions"] = req.DeveloperInstructions
	}
	if len(req.Config) > 0 {
		params["config"] = req.Config
	}
	result, err := c.call(ctx, "thread/start", params)
	if err != nil {
		return ThreadStarted{}, err
	}
	event, err := ParseThreadStarted(result)
	if err != nil {
		return ThreadStarted{}, err
	}
	threadID := event.Thread.ID
	if threadID == "" {
		return ThreadStarted{}, errors.New("codex app server: thread/started has no thread id")
	}
	if event.Model != "" && event.Model != req.Model {
		return ThreadStarted{}, fmt.Errorf("%w: requested=%s observed=%s", ErrModelRerouted, req.Model, event.Model)
	}
	c.mu.Lock()
	c.threadID = threadID
	c.receipt.RequestedModel = req.Model
	requestedProfile := req.Sandbox
	c.receipt.RequestedPermissionProfile = requestedProfile
	if requestedProfile != "" {
		c.receipt.RequestedPermissions = []string{requestedProfile}
	} else {
		c.receipt.RequestedPermissions = nil
	}
	if event.Model != "" {
		c.receipt.ObservedModel = event.Model
	}
	if event.ReasoningEffort != "" {
		c.receipt.ObservedEffort = event.ReasoningEffort
	}
	c.mu.Unlock()
	return event, nil
}

// ThreadResume restores a durable thread in a fresh App Server process while
// reapplying the complete host-owned execution surface. Mutable user defaults
// are never allowed to fill a missing model, sandbox or approval policy.
func (c *Client) ThreadResume(ctx context.Context, req ThreadResumeRequest) (ThreadStarted, error) {
	if req.VisibilityRequired && !c.native {
		return ThreadStarted{}, errors.New("codex app server: visibility_required requires native App Server transport")
	}
	if strings.TrimSpace(req.ThreadID) == "" || strings.TrimSpace(req.Model) == "" ||
		strings.TrimSpace(req.Sandbox) == "" ||
		strings.TrimSpace(req.ApprovalPolicy) == "" {
		return ThreadStarted{}, errors.New("codex app server: thread/resume requires thread, model, sandbox and approval policy")
	}
	if strings.TrimSpace(req.Permissions) != "" {
		return ThreadStarted{}, errors.New("codex app server: permissions is unsupported; use sandbox and approval policy")
	}
	params := map[string]any{
		"threadId": req.ThreadID, "model": req.Model,
		"sandbox": req.Sandbox, "approvalPolicy": req.ApprovalPolicy,
		"developerInstructions": req.DeveloperInstructions, "config": req.Config,
		"excludeTurns": true,
	}
	if strings.TrimSpace(req.Workdir) != "" {
		params["cwd"] = req.Workdir
	}
	result, err := c.call(ctx, "thread/resume", params)
	if err != nil {
		return ThreadStarted{}, err
	}
	event, err := ParseThreadStarted(result)
	if err != nil {
		return ThreadStarted{}, err
	}
	if event.Thread.ID != req.ThreadID {
		return ThreadStarted{}, errors.New("codex app server: thread/resume returned an unexpected thread id")
	}
	if event.Model != "" && event.Model != req.Model {
		return ThreadStarted{}, fmt.Errorf("%w: requested=%s observed=%s", ErrModelRerouted, req.Model, event.Model)
	}
	c.mu.Lock()
	c.threadID = req.ThreadID
	c.receipt.RequestedModel = req.Model
	requestedProfile := req.Sandbox
	c.receipt.RequestedPermissionProfile = requestedProfile
	if requestedProfile != "" {
		c.receipt.RequestedPermissions = []string{requestedProfile}
	}
	if event.Model != "" {
		c.receipt.ObservedModel = event.Model
	}
	if event.ReasoningEffort != "" {
		c.receipt.ObservedEffort = event.ReasoningEffort
	}
	c.mu.Unlock()
	return event, nil
}

// ThreadFork creates an App Server child thread without changing the root
// thread stored by this client. The returned identifier must differ from its
// parent so callers can persist an unambiguous specialist relationship.
func (c *Client) ThreadFork(ctx context.Context, req ThreadForkRequest) (ThreadStarted, error) {
	if req.VisibilityRequired && !c.native {
		return ThreadStarted{}, errors.New("codex app server: visibility_required requires native App Server transport")
	}
	if strings.TrimSpace(req.ParentThreadID) == "" {
		return ThreadStarted{}, errors.New("codex app server: parent thread id is required")
	}
	if strings.TrimSpace(req.Model) == "" {
		return ThreadStarted{}, errors.New("codex app server: model is required")
	}
	if strings.TrimSpace(req.Sandbox) == "" || strings.TrimSpace(req.ApprovalPolicy) == "" {
		return ThreadStarted{}, errors.New("codex app server: sandbox and approval policy are required")
	}
	params := map[string]any{
		"threadId":              req.ParentThreadID,
		"model":                 req.Model,
		"sandbox":               req.Sandbox,
		"approvalPolicy":        req.ApprovalPolicy,
		"developerInstructions": req.DeveloperInstructions,
		"config":                req.Config,
		"ephemeral":             false,
		"excludeTurns":          true,
	}
	if strings.TrimSpace(req.Workdir) != "" {
		params["cwd"] = req.Workdir
	}
	result, err := c.call(ctx, "thread/fork", params)
	if err != nil {
		return ThreadStarted{}, err
	}
	event, err := ParseThreadStarted(result)
	if err != nil {
		return ThreadStarted{}, err
	}
	if event.Thread.ID == "" || event.Thread.ID == req.ParentThreadID {
		return ThreadStarted{}, errors.New("codex app server: thread/fork returned an invalid child thread id")
	}
	if event.Model != "" && event.Model != req.Model {
		return ThreadStarted{}, fmt.Errorf("%w: requested=%s observed=%s", ErrModelRerouted, req.Model, event.Model)
	}
	return event, nil
}

// TurnInterrupt requests cancellation for one exact running turn.
func (c *Client) TurnInterrupt(ctx context.Context, threadID, turnID string) error {
	if strings.TrimSpace(threadID) == "" || strings.TrimSpace(turnID) == "" {
		return errors.New("codex app server: thread and turn ids are required for interruption")
	}
	_, err := c.call(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID})
	return err
}

// TurnStart is part of ATENEA's public orchestration contract.
func (c *Client) TurnStart(ctx context.Context, req TurnStartRequest) (TurnStarted, error) {
	if req.VisibilityRequired && !c.native {
		return TurnStarted{}, errors.New("codex app server: visibility_required requires native App Server transport")
	}
	if strings.TrimSpace(req.ThreadID) == "" {
		return TurnStarted{}, errors.New("codex app server: thread id is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return TurnStarted{}, errors.New("codex app server: prompt is required")
	}
	if strings.TrimSpace(req.Permissions) != "" {
		return TurnStarted{}, errors.New("codex app server: permissions is unsupported; set the thread sandbox")
	}
	params := map[string]any{
		"threadId": req.ThreadID,
		"input":    []map[string]string{{"type": "text", "text": req.Prompt}},
		"model":    req.Model,
		"effort":   req.ReasoningEffort,
	}
	if len(req.OutputSchema) > 0 {
		params["outputSchema"] = req.OutputSchema
	}
	result, err := c.call(ctx, "turn/start", params)
	if err != nil {
		return TurnStarted{}, err
	}
	event, err := ParseTurnStarted(result)
	if err != nil {
		return TurnStarted{}, err
	}
	c.mu.Lock()
	if route, ok := c.reroutes[rerouteKey(req.ThreadID, event.Turn.ID)]; ok {
		c.mu.Unlock()
		return event, fmt.Errorf("%w: requested=%s observed=%s", ErrModelRerouted, route.FromModel, route.ToModel)
	}
	if event.Turn.ThreadID == "" {
		event.Turn.ThreadID = req.ThreadID
	}
	if req.Model != "" {
		c.receipt.RequestedModel = req.Model
	}
	if req.ReasoningEffort != "" {
		c.receipt.RequestedEffort = req.ReasoningEffort
		if c.receipt.ObservedEffort != "" && c.receipt.ObservedEffort != req.ReasoningEffort {
			c.receipt.ObservedEffort = ""
		}
	}
	c.mu.Unlock()
	return event, nil
}

func rerouteKey(threadID, turnID string) string { return threadID + "\x00" + turnID }

// RerouteFor returns the observed route for one exact turn. It never exposes
// a route from another thread or makes rerouting a sticky client-wide state.
func (c *Client) RerouteFor(threadID, turnID string) (ModelRerouted, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	route, ok := c.reroutes[rerouteKey(threadID, turnID)]
	return route, ok
}

// ThreadList is part of ATENEA's public orchestration contract.
func (c *Client) ThreadList(ctx context.Context) (ThreadListResult, error) {
	result, err := c.call(ctx, "thread/list", map[string]any{})
	if err != nil {
		return ThreadListResult{}, err
	}
	var out ThreadListResult
	if err := json.Unmarshal(result, &out); err != nil {
		return out, fmt.Errorf("codex app server: malformed thread/list result: %w", err)
	}
	if out.Data == nil {
		return ThreadListResult{}, errors.New("codex app server: thread/list result missing data")
	}
	for _, thread := range out.Data {
		if thread.ID == "" || thread.Status.Type == "" {
			return ThreadListResult{}, errors.New("codex app server: thread/list contains malformed thread status")
		}
	}
	return out, nil
}

// Receipt is part of ATENEA's public orchestration contract.
func (c *Client) Receipt() ExecutionReceipt {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.receipt
	out.RequestedPermissions = slices.Clone(out.RequestedPermissions)
	return out
}

// UsageRevision is Atenea's local monotonic revision for token usage events.
// The native notification carries no usage_revision field, so this
// counter is deliberately not presented as server evidence.
func (c *Client) UsageRevision() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usageRevision
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	initialized := c.initialized
	closed := c.closed
	c.mu.Unlock()
	if method != "initialize" && !initialized {
		return nil, errors.New("codex app server: initialize is required")
	}
	if closed {
		return nil, errors.New("codex app server: client is closed")
	}
	return c.transport.Call(ctx, method, params)
}

func (c *Client) handleNotification(notification Notification) {
	var revision uint64
	var wake bool
	if notification.Method == "model/rerouted" {
		var event ModelRerouted
		if json.Unmarshal(notification.Params, &event) == nil && event.FromModel != "" && event.ToModel != "" {
			c.mu.Lock()
			if event.ThreadID != "" && event.TurnID != "" {
				key := rerouteKey(event.ThreadID, event.TurnID)
				c.reroutes[key] = event
				c.turnStateLocked(key).Reroute = &event
				wake = true
			}
			c.receipt.ModelRerouted = true
			c.mu.Unlock()
		}
	}
	if notification.Method == "thread/tokenUsage/updated" {
		var event TokenUsageUpdated
		if json.Unmarshal(notification.Params, &event) == nil && event.ThreadID != "" && event.TurnID != "" {
			c.mu.Lock()
			c.usageRevision++
			event.Revision = c.usageRevision
			revision = event.Revision
			c.turnStateLocked(rerouteKey(event.ThreadID, event.TurnID)).Usage = event.TokenUsage
			c.turnStateLocked(rerouteKey(event.ThreadID, event.TurnID)).UsageRevision = event.Revision
			wake = true
			c.mu.Unlock()
			select {
			case c.usageEvents <- event:
			default:
			}
		}
	}
	if notification.Method == "item/completed" {
		if event, err := ParseItemCompleted(notification); err == nil {
			c.mu.Lock()
			c.turnStateLocked(rerouteKey(event.ThreadID, event.TurnID)).Message = event.Item.Text
			wake = true
			c.mu.Unlock()
		}
	}
	if notification.Method == "turn/completed" {
		if event, err := ParseTurnCompleted(notification); err == nil {
			c.mu.Lock()
			c.turnStateLocked(rerouteKey(event.ThreadID, event.Turn.ID)).Completed = &event
			wake = true
			c.mu.Unlock()
		}
	}
	if wake {
		select {
		case c.turnWake <- struct{}{}:
		default:
		}
	}
	notification.Revision = revision
	select {
	case c.events <- notification:
	default:
	}
}

func (c *Client) turnStateLocked(key string) *TurnObservation {
	state := c.turns[key]
	if state == nil {
		state = &TurnObservation{}
		c.turns[key] = state
	}
	return state
}

// ParseThreadStarted is part of ATENEA's public orchestration contract.
func ParseThreadStarted(raw json.RawMessage) (ThreadStarted, error) {
	var out ThreadStarted
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("codex app server: malformed thread/started: %w", err)
	}
	if out.Thread.ID == "" || out.Thread.Status.Type == "" {
		return out, errors.New("codex app server: thread/started missing id")
	}
	return out, nil
}

// ParseTurnStarted is part of ATENEA's public orchestration contract.
func ParseTurnStarted(raw json.RawMessage) (TurnStarted, error) {
	var out TurnStarted
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("codex app server: malformed turn/started: %w", err)
	}
	if out.Turn.ID == "" {
		return out, errors.New("codex app server: turn/started missing id")
	}
	return out, nil
}

// ParseTokenUsageUpdated is part of ATENEA's public orchestration contract.
func ParseTokenUsageUpdated(notification Notification) (TokenUsageUpdated, error) {
	if notification.Method != "thread/tokenUsage/updated" {
		return TokenUsageUpdated{}, fmt.Errorf("unexpected notification %q", notification.Method)
	}
	var out TokenUsageUpdated
	if err := json.Unmarshal(notification.Params, &out); err != nil {
		return out, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(notification.Params, &fields) != nil || fields["tokenUsage"] == nil {
		return out, errors.New("codex app server: token usage result missing tokenUsage")
	}
	if out.ThreadID == "" || out.TurnID == "" {
		return out, errors.New("codex app server: token usage lacks thread or turn id")
	}
	return out, nil
}

// ParseModelRerouted is part of ATENEA's public orchestration contract.
func ParseModelRerouted(notification Notification) (ModelRerouted, error) {
	if notification.Method != "model/rerouted" {
		return ModelRerouted{}, fmt.Errorf("unexpected notification %q", notification.Method)
	}
	var out ModelRerouted
	if err := json.Unmarshal(notification.Params, &out); err != nil {
		return out, err
	}
	if out.FromModel == "" || out.ToModel == "" {
		return out, errors.New("codex app server: model/rerouted lacks from or to model")
	}
	return out, nil
}

// ParseItemCompleted is part of ATENEA's public orchestration contract.
func ParseItemCompleted(notification Notification) (ItemCompleted, error) {
	if notification.Method != "item/completed" {
		return ItemCompleted{}, fmt.Errorf("unexpected notification %q", notification.Method)
	}
	var out ItemCompleted
	if err := json.Unmarshal(notification.Params, &out); err != nil {
		return out, err
	}
	if out.ThreadID == "" || out.TurnID == "" || out.Item.ID == "" || out.Item.Type != "agentMessage" {
		return out, errors.New("codex app server: item/completed is not an agent message")
	}
	return out, nil
}

// ParseTurnCompleted is part of ATENEA's public orchestration contract.
func ParseTurnCompleted(notification Notification) (TurnCompleted, error) {
	if notification.Method != "turn/completed" {
		return TurnCompleted{}, fmt.Errorf("unexpected notification %q", notification.Method)
	}
	var out TurnCompleted
	if err := json.Unmarshal(notification.Params, &out); err != nil {
		return out, err
	}
	if out.ThreadID == "" {
		out.ThreadID = out.Turn.ThreadID
	}
	if out.ThreadID == "" || out.Turn.ID == "" || out.Turn.Status == "" {
		return out, errors.New("codex app server: turn/completed is missing turn identity")
	}
	return out, nil
}

// ProcessTransport is newline-delimited JSON-RPC over `codex app-server`.
type ProcessTransport struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	mu             sync.Mutex
	writeMu        sync.Mutex
	next           atomic.Uint64
	pending        map[uint64]chan processResponse
	closed         chan struct{}
	onNotification func(Notification)
	closeOnce      sync.Once
	processOnce    sync.Once
}

type processResponse struct {
	result json.RawMessage
	err    error
}

// NewProcessTransport is part of ATENEA's public orchestration contract.
func NewProcessTransport(binary string, args ...string) (*ProcessTransport, error) {
	if strings.TrimSpace(binary) == "" {
		return nil, errors.New("codex app server: binary is required")
	}
	cmd := exec.Command(binary, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	t := &ProcessTransport{cmd: cmd, stdin: stdin, pending: make(map[uint64]chan processResponse), closed: make(chan struct{})}
	go t.read(stdout)
	return t, nil
}

// SetNotificationHandler is part of ATENEA's public orchestration contract.
func (t *ProcessTransport) SetNotificationHandler(handler func(Notification)) {
	t.mu.Lock()
	t.onNotification = handler
	t.mu.Unlock()
}

// Call is part of ATENEA's public orchestration contract.
func (t *ProcessTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := t.next.Add(1)
	response := make(chan processResponse, 1)
	t.mu.Lock()
	t.pending[id] = response
	t.mu.Unlock()
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := t.write(ctx, request); err != nil {
		t.remove(id)
		return nil, err
	}
	select {
	case result := <-response:
		return result.result, result.err
	case <-ctx.Done():
		t.remove(id)
		return nil, ctx.Err()
	case <-t.closed:
		return nil, errors.New("codex app server: transport closed")
	}
}

// Notify is part of ATENEA's public orchestration contract.
func (t *ProcessTransport) Notify(ctx context.Context, method string, params any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	envelope := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		envelope["params"] = params
	}
	return t.write(ctx, envelope)
}

func (t *ProcessTransport) write(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		t.writeMu.Lock()
		defer t.writeMu.Unlock()
		_, writeErr := t.stdin.Write(append(data, '\n'))
		done <- writeErr
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// A blocked pipe write has no context API. Closing stdin is the only
		// reliable way to unblock it and prevents a canceled request from
		// being written later after its caller has returned.
		_ = t.stdin.Close()
		return ctx.Err()
	case <-t.closed:
		_ = t.stdin.Close()
		return errors.New("codex app server: transport closed")
	}
}

func (t *ProcessTransport) remove(id uint64) { t.mu.Lock(); delete(t.pending, id); t.mu.Unlock() }

func (t *ProcessTransport) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &envelope) != nil {
			continue
		}
		if envelope.Method != "" {
			t.mu.Lock()
			handler := t.onNotification
			t.mu.Unlock()
			if handler != nil {
				handler(Notification{JSONRPC: "2.0", Method: envelope.Method, Params: envelope.Params})
			}
			continue
		}
		id, err := strconv.ParseUint(strings.Trim(string(envelope.ID), `"`), 10, 64)
		if err != nil {
			continue
		}
		t.mu.Lock()
		response := t.pending[id]
		delete(t.pending, id)
		t.mu.Unlock()
		if response == nil {
			continue
		}
		if envelope.Error != nil {
			response <- processResponse{err: envelope.Error}
		} else {
			response <- processResponse{result: envelope.Result}
		}
	}
	t.closeOnce.Do(func() { close(t.closed) })
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, response := range t.pending {
		delete(t.pending, id)
		response <- processResponse{err: errors.New("codex app server: process ended")}
	}
}

// Close is part of ATENEA's public orchestration contract.
func (t *ProcessTransport) Close() error {
	var err error
	t.closeOnce.Do(func() { close(t.closed) })
	t.processOnce.Do(func() {
		_ = t.stdin.Close()
		if killErr := t.cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			err = killErr
		}
		_, _ = t.cmd.Process.Wait()
	})
	return err
}

// AppServerDigest is part of ATENEA's public orchestration contract.
func AppServerDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
