package model

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	adaptercodex "github.com/Tutitoos/atenea/internal/adapter/codex"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type nativeModelTransport struct {
	mu          sync.Mutex
	calls       []string
	notify      []string
	handler     func(adaptercodex.Notification)
	reroute     bool
	answer      string
	noise       int
	hang        bool
	interrupted bool
	startHang   bool
	closed      bool
}

func (t *nativeModelTransport) SetNotificationHandler(handler func(adaptercodex.Notification)) {
	t.handler = handler
}
func (t *nativeModelTransport) Close() error { t.closed = true; return nil }
func (t *nativeModelTransport) Notify(_ context.Context, method string, _ any) error {
	t.mu.Lock()
	t.notify = append(t.notify, method)
	t.mu.Unlock()
	return nil
}
func (t *nativeModelTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	_ = params
	t.mu.Lock()
	t.calls = append(t.calls, method)
	callNumber := len(t.calls)
	t.mu.Unlock()
	switch method {
	case "initialize":
		return json.RawMessage(`{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-app-server/0.151.0"}`), nil
	case "thread/start":
		return json.RawMessage(`{"thread":{"id":"thread-native","status":{"type":"idle"}},"model":"gpt-5.6-sol","modelProvider":"openai","reasoningEffort":"medium"}`), nil
	case "thread/resume":
		return json.RawMessage(`{"thread":{"id":"thread-native","status":{"type":"idle"}},"model":"gpt-5.6-sol","modelProvider":"openai","reasoningEffort":"medium"}`), nil
	case "turn/start":
		if t.startHang {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		turnID := "turn-native-1"
		if callNumber > 3 { // initialize, thread/start, turn/start is the first turn.
			turnID = "turn-native-2"
		}
		if t.hang {
			return json.RawMessage(`{"turn":{"id":"` + turnID + `","threadId":"thread-native","status":"started"}}`), nil
		}
		if t.reroute {
			t.handler(adaptercodex.Notification{Method: "model/rerouted", Params: json.RawMessage(`{"fromModel":"gpt-5.6-sol","toModel":"gpt-5.6-luna","reason":"fallback","threadId":"thread-native","turnId":"` + turnID + `"}`)})
		}
		answerText := "native answer"
		if t.answer != "" {
			answerText = t.answer
		} else if input, ok := params.(map[string]any); ok && input["outputSchema"] != nil {
			answerText = `{"ok":true}`
		}
		for i := 0; i < t.noise; i++ {
			t.handler(adaptercodex.Notification{Method: "item/started", Params: json.RawMessage(`{"threadId":"thread-native","turnId":"` + turnID + `"}`)})
		}
		item, _ := json.Marshal(map[string]any{"threadId": "thread-native", "turnId": turnID, "item": map[string]any{"id": "item-1", "type": "agentMessage", "text": answerText}})
		t.handler(adaptercodex.Notification{Method: "item/completed", Params: item})
		t.handler(adaptercodex.Notification{Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{"threadId":"thread-native","turnId":"` + turnID + `","tokenUsage":{"last":{"inputTokens":3,"outputTokens":2,"totalTokens":5},"total":{"inputTokens":3,"outputTokens":2,"totalTokens":5},"modelContextWindow":1000}}`)})
		t.handler(adaptercodex.Notification{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-native","turn":{"id":"` + turnID + `","threadId":"thread-native","status":"completed"}}`)})
		return json.RawMessage(`{"turn":{"id":"` + turnID + `","threadId":"thread-native","status":"started"}}`), nil
	case "turn/interrupt":
		t.interrupted = true
		t.handler(adaptercodex.Notification{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-native","turn":{"id":"turn-native-1","threadId":"thread-native","status":"interrupted"}}`)})
		return json.RawMessage(`{}`), nil
	default:
		return nil, errors.New("unexpected native method " + method)
	}
}

func TestCodexNativeCarriesAndValidatesOutputSchema(t *testing.T) {
	transport := &nativeModelTransport{}
	client := newNativeModelClient(t, transport)
	answer, err := client.Turn(t.Context(), Request{Role: RoleResearch, Prompt: "return json", VisibilityRequired: true, Schema: map[string]any{
		"type": "object", "required": []any{"ok"}, "additionalProperties": false,
		"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if string(answer.Structured) != `{"ok":true}` {
		t.Fatalf("structured = %s", answer.Structured)
	}
	var found bool
	for _, method := range transport.calls {
		if method == "turn/start" {
			found = true
		}
	}
	if !found {
		t.Fatal("turn/start was not sent")
	}
}

func TestCodexNativeCarriesCoverageIdentityAndSurvivesDiagnosticFlood(t *testing.T) {
	transport := &nativeModelTransport{answer: `{"plan":"ship it","completeness":0.75,"stopped_at":"final audit"}`, noise: 100}
	client := newNativeModelClient(t, transport)
	answer, err := client.Turn(t.Context(), Request{Role: RoleResearch, Prompt: "plan", ReasoningEffort: "medium", VisibilityRequired: true, Schema: map[string]any{
		"type": "object", "required": []any{"plan", "completeness", "stopped_at"},
		"properties": map[string]any{"plan": map[string]any{"type": "string"}, "completeness": map[string]any{"type": "number"}, "stopped_at": map[string]any{"type": "string"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Completeness == nil || *answer.Completeness != 0.75 || answer.StoppedAt != "final audit" {
		t.Fatalf("coverage = %v stopped_at=%q", answer.Completeness, answer.StoppedAt)
	}
	if answer.RequestedModel != "gpt-5.6-sol" || answer.ObservedModel != "gpt-5.6-sol" || answer.RequestedReasoningEffort != "medium" || answer.ObservedReasoningEffort != "medium" {
		t.Fatalf("identity = %+v", answer)
	}
}

func newNativeModelClient(t *testing.T, transport *nativeModelTransport) *Client {
	t.Helper()
	client, err := New(Options{
		Backend: BackendCodex, Binary: "/missing/codex", CodexNative: true,
		CodexNativeOptions: adaptercodex.AppServerOptions{Transport: transport, NativeTransport: true},
		Research:           "gpt-5.6-sol", ResearchReasoningEffort: "medium",
		Implement: "gpt-5.6-sol", ImplementReasoningEffort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestCodexNativeVisibleTurnUsesOneDurableThreadAndTypedUsage(t *testing.T) {
	transport := &nativeModelTransport{}
	client := newNativeModelClient(t, transport)
	answer, err := client.Turn(t.Context(), Request{Role: RoleResearch, Prompt: "inspect", VisibilityRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Text != "native answer" || answer.ThreadID != "thread-native" || answer.Spent.Tokens() != 5 {
		t.Fatalf("answer = %+v", answer)
	}
	if got := transport.notify; len(got) != 1 || got[0] != "initialized" {
		t.Fatalf("initialized notifications = %v", got)
	}
	second, err := client.Turn(t.Context(), Request{Role: RoleResearch, Prompt: "follow up", VisibilityRequired: true, ThreadID: answer.ThreadID})
	if err != nil {
		t.Fatal(err)
	}
	if second.ThreadID != answer.ThreadID {
		t.Fatalf("follow-up thread = %q", second.ThreadID)
	}
	var threadStarts, turnStarts int
	for _, method := range transport.calls {
		if method == "thread/start" {
			threadStarts++
		}
		if method == "turn/start" {
			turnStarts++
		}
	}
	if threadStarts != 1 || turnStarts != 2 {
		t.Fatalf("native calls = %v", transport.calls)
	}
}

func TestChargeFromNativeUsageUsesLastTurnAndDisjointCacheCounters(t *testing.T) {
	usage := adaptercodex.TokenUsage{
		Last: adaptercodex.TokenCounts{
			InputTokens: 8, CachedInputTokens: 3, CacheWriteInputTokens: 1,
			OutputTokens: 2, ReasoningOutputTokens: 1, TotalTokens: 10,
		},
		Total: adaptercodex.TokenCounts{
			InputTokens: 20, CachedInputTokens: 6, CacheWriteInputTokens: 2,
			OutputTokens: 5, ReasoningOutputTokens: 3, TotalTokens: 25,
		},
	}
	charge := chargeFromNativeUsage(usage)
	if charge.InputTokens != 4 || charge.CacheReadTokens != 3 || charge.CacheWriteTokens != 1 || charge.OutputTokens != 2 {
		t.Fatalf("charge does not contain disjoint last-turn counters: %+v", charge)
	}
	if charge.Tokens() != 10 {
		t.Fatalf("charge tokens = %d, want last.totalTokens 10", charge.Tokens())
	}
}

func TestChargeFromNativeUsageDoesNotFallBackToCumulativeTotal(t *testing.T) {
	usage := adaptercodex.TokenUsage{Total: adaptercodex.TokenCounts{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}
	if charge := chargeFromNativeUsage(usage); charge.Tokens() != 0 || charge.USD != nil {
		t.Fatalf("missing last-turn usage became cumulative spend: %+v", charge)
	}
}

func TestCodexNativeResumesDurableThreadInFreshClient(t *testing.T) {
	first := newNativeModelClient(t, &nativeModelTransport{})
	initial, err := first.Turn(t.Context(), Request{Role: RoleResearch, Prompt: "inspect", VisibilityRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	transport := &nativeModelTransport{}
	second := newNativeModelClient(t, transport)
	resumed, err := second.Turn(t.Context(), Request{Role: RoleResearch, Prompt: "continue", VisibilityRequired: true, ThreadID: initial.ThreadID})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ThreadID != initial.ThreadID {
		t.Fatalf("resumed thread = %q, want %q", resumed.ThreadID, initial.ThreadID)
	}
	var resumes, starts int
	for _, method := range transport.calls {
		if method == "thread/resume" {
			resumes++
		}
		if method == "thread/start" {
			starts++
		}
	}
	if resumes != 1 || starts != 0 {
		t.Fatalf("fresh-client calls = %v", transport.calls)
	}
}

func TestCodexNativeRejectsUnknownThreadAndVisibilityWithoutNative(t *testing.T) {
	transport := &nativeModelTransport{}
	client := newNativeModelClient(t, transport)
	if _, err := client.Turn(t.Context(), Request{Role: RoleResearch, Prompt: "x", VisibilityRequired: true, ThreadID: "foreign"}); err == nil {
		t.Fatal("foreign thread was accepted")
	}
	legacy, err := New(Options{Backend: BackendCodex, Binary: "/missing/codex", Research: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Turn(t.Context(), Request{Role: RoleResearch, Prompt: "x", VisibilityRequired: true}); err == nil {
		t.Fatal("visible request used legacy exec")
	}
}

func TestCodexNativeRerouteIsPerTurnAndFailsClosed(t *testing.T) {
	transport := &nativeModelTransport{reroute: true}
	client := newNativeModelClient(t, transport)
	if _, err := client.Turn(t.Context(), Request{Role: RoleResearch, Prompt: "inspect", VisibilityRequired: true}); err == nil {
		t.Fatal("rerouted turn was accepted")
	}
}

func TestCodexNativeRefusesSandboxChangeAndUnsupportedSurface(t *testing.T) {
	transport := &nativeModelTransport{}
	client := newNativeModelClient(t, transport)
	answer, err := client.Turn(t.Context(), Request{Role: RoleImplement, Prompt: "implement", VisibilityRequired: true, Effects: []contract.Effect{contract.EffectWrite}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Turn(t.Context(), Request{Role: RoleResearch, Prompt: "inspect", VisibilityRequired: true, ThreadID: answer.ThreadID}); err == nil {
		t.Fatal("read-only request reused a workspace-write thread")
	}
	other := newNativeModelClient(t, &nativeModelTransport{})
	if _, err := other.Turn(t.Context(), Request{Role: RoleResearch, Prompt: "inspect", VisibilityRequired: true, Tools: `{"mcpServers":{"custom":{}}}`}); err == nil {
		t.Fatal("native turn silently ignored a custom tool surface")
	}
}

func TestCodexNativeTimeoutInterruptsAndConfirmsRemoteTurn(t *testing.T) {
	transport := &nativeModelTransport{hang: true}
	client := newNativeModelClient(t, transport)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := client.Turn(ctx, Request{Role: RoleResearch, Prompt: "hang", VisibilityRequired: true}); err == nil {
		t.Fatal("timed out turn returned success")
	}
	if !transport.interrupted {
		t.Fatal("timed out turn was not interrupted")
	}
}

func TestCodexNativeStartTimeoutClosesUnidentifiedTurn(t *testing.T) {
	transport := &nativeModelTransport{startHang: true}
	client := newNativeModelClient(t, transport)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := client.Turn(ctx, Request{Role: RoleResearch, Prompt: "hang at start", VisibilityRequired: true}); err == nil {
		t.Fatal("turn/start timeout returned success")
	}
	if !transport.closed {
		t.Fatal("App Server remained open after unidentified turn/start timeout")
	}
}
