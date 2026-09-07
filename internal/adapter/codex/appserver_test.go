package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeAppTransport struct {
	calls []struct {
		method string
		params map[string]any
	}
	responses map[string]json.RawMessage
	handler   func(Notification)
	notifies  []struct {
		method string
		params any
	}
	order  []string
	closed bool
}

func TestDefaultAppServerArgsTrustOnlyTheIsolatedAteneaHook(t *testing.T) {
	plain := strings.Join(defaultAppServerArgs(AppServerOptions{}), " ")
	if strings.Contains(plain, "--dangerously-bypass-hook-trust") {
		t.Fatalf("default arguments trust ambient hooks: %s", plain)
	}

	isolated := defaultAppServerArgs(AppServerOptions{IsolateAmbientHooks: true, TrustAteneaHook: true})
	joined := strings.Join(isolated, " ")
	if isolated[0] != "--dangerously-bypass-hook-trust" || !strings.Contains(joined, "features.plugins=false") {
		t.Fatalf("isolated arguments = %q", isolated)
	}
	for _, event := range []string{"PreToolUse", "PermissionRequest", "PostToolUse", "PreCompact", "PostCompact", "SessionStart", "SessionEnd", "UserPromptSubmit", "SubagentStart", "SubagentStop", "Stop"} {
		if !strings.Contains(joined, "hooks."+event+"=[]") {
			t.Fatalf("isolated arguments do not clear %s: %s", event, joined)
		}
	}
}

func TestAppServerRejectsHookTrustWithoutIsolation(t *testing.T) {
	_, err := NewAppServerClient(AppServerOptions{Transport: appServerFake(), TrustAteneaHook: true})
	if err == nil || !strings.Contains(err.Error(), "requires ambient hook isolation") {
		t.Fatalf("error = %v", err)
	}
}

func TestAppServerRejectsManagedIsolationWithCustomCommand(t *testing.T) {
	_, err := NewAppServerClient(AppServerOptions{
		Binary: "codex", Command: []string{"app-server"}, IsolateAmbientHooks: true,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined with a custom command") {
		t.Fatalf("error = %v", err)
	}
}

func TestProcessTransportBlockedWriteHonorsContext(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	transport := &ProcessTransport{stdin: writer, pending: make(map[uint64]chan processResponse), closed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = transport.Call(ctx, "turn/start", map[string]any{"payload": strings.Repeat("x", 8<<20)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked write error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("blocked write ignored context for %v", elapsed)
	}
}

func (f *fakeAppTransport) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	value, _ := params.(map[string]any)
	f.order = append(f.order, "call:"+method)
	f.calls = append(f.calls, struct {
		method string
		params map[string]any
	}{method, value})
	if response, ok := f.responses[method]; ok {
		return response, nil
	}
	return nil, errors.New("unexpected method " + method)
}
func (f *fakeAppTransport) Notify(_ context.Context, method string, params any) error {
	f.notifies = append(f.notifies, struct {
		method string
		params any
	}{method, params})
	f.order = append(f.order, "notify:"+method)
	return nil
}
func (f *fakeAppTransport) Close() error                                      { f.closed = true; return nil }
func (f *fakeAppTransport) SetNotificationHandler(handler func(Notification)) { f.handler = handler }

func appServerFake() *fakeAppTransport {
	return &fakeAppTransport{responses: map[string]json.RawMessage{
		"initialize":                      json.RawMessage(`{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-app-server/0.151.0"}`),
		"model/list":                      json.RawMessage(`{"data":[{"id":"model-1","model":"gpt-5.6-sol","displayName":"Sol","defaultReasoningEffort":"medium","supportedReasoningEfforts":[{"reasoningEffort":"medium","description":"balanced"}]}],"nextCursor":""}`),
		"modelProvider/capabilities/read": json.RawMessage(`{"imageGeneration":false,"namespaceTools":true,"webSearch":true}`),
		"thread/start":                    json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}},"model":"gpt-5.6-sol","modelProvider":"openai","reasoningEffort":"medium"}`),
		"thread/fork":                     json.RawMessage(`{"thread":{"id":"thread-child","status":{"type":"idle"}},"model":"gpt-5.6-sol","modelProvider":"openai","reasoningEffort":"medium"}`),
		"turn/start":                      json.RawMessage(`{"turn":{"id":"turn-1","threadId":"thread-1","status":"started"}}`),
		"thread/list":                     json.RawMessage(`{"data":[{"id":"thread-1","status":{"type":"active","activeFlags":[]}}],"nextCursor":"","backwardsCursor":""}`),
	}}
}

func TestAppServerNativeLifecycleCarriesDurabilityAndIdentity(t *testing.T) {
	transport := appServerFake()
	client, err := NewAppServerClient(AppServerOptions{Transport: transport, NativeTransport: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	initResult, err := client.Initialize(t.Context(), InitializeRequest{ClientInfo: ClientInfo{Name: "atenea", Version: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if initResult.UserAgent == "" {
		t.Fatal("initialize did not return runtime identity")
	}
	if _, err := client.ModelList(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ModelProviderCapabilitiesRead(t.Context(), "ignored-compat-arg"); err != nil {
		t.Fatal(err)
	}
	hookConfig := map[string]any{"features": map[string]any{"hooks": true}}
	thread, err := client.ThreadStart(t.Context(), ThreadStartRequest{Model: "gpt-5.6-sol", Sandbox: "read-only", ApprovalPolicy: "never", DeveloperInstructions: "stay within the declared surface", Config: hookConfig, VisibilityRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	if thread.Thread.ID != "thread-1" {
		t.Fatalf("thread = %+v", thread)
	}
	if _, err := client.TurnStart(t.Context(), TurnStartRequest{ThreadID: "thread-1", Prompt: "inspect", Model: "gpt-5.6-sol", ReasoningEffort: "medium", VisibilityRequired: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ThreadList(t.Context()); err != nil {
		t.Fatal(err)
	}
	child, err := client.ThreadFork(t.Context(), ThreadForkRequest{ParentThreadID: "thread-1", Model: "gpt-5.6-sol", Workdir: "/tmp/work", Sandbox: "read-only", ApprovalPolicy: "never", DeveloperInstructions: "specialist read only", Config: hookConfig, VisibilityRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	if child.Thread.ID != "thread-child" {
		t.Fatalf("child thread = %+v", child)
	}
	var start, fork, turn, caps map[string]any
	for _, call := range transport.calls {
		switch call.method {
		case "thread/start":
			start = call.params
		case "turn/start":
			turn = call.params
		case "thread/fork":
			fork = call.params
		case "modelProvider/capabilities/read":
			caps = call.params
		}
	}
	if start["ephemeral"] != false {
		t.Fatalf("durability params = %#v", start)
	}
	if _, ok := start["reasoningEffort"]; ok {
		t.Fatalf("thread/start sent unsupported reasoningEffort: %#v", start)
	}
	if _, ok := start["permissions"]; ok {
		t.Fatalf("thread/start sent unsupported permissions: %#v", start)
	}
	if start["approvalPolicy"] != "never" || start["developerInstructions"] != "stay within the declared surface" {
		t.Fatalf("native restrictions = %#v", start)
	}
	if !reflect.DeepEqual(start["config"], hookConfig) {
		t.Fatalf("native hook config = %#v", start["config"])
	}
	if fork["threadId"] != "thread-1" || fork["model"] != "gpt-5.6-sol" || fork["cwd"] != "/tmp/work" {
		t.Fatalf("fork identity params = %#v", fork)
	}
	if fork["sandbox"] != "read-only" || fork["approvalPolicy"] != "never" || fork["developerInstructions"] != "specialist read only" {
		t.Fatalf("fork restrictions = %#v", fork)
	}
	if fork["ephemeral"] != false || fork["excludeTurns"] != true || !reflect.DeepEqual(fork["config"], hookConfig) {
		t.Fatalf("fork durability params = %#v", fork)
	}
	if turn["effort"] != "medium" {
		t.Fatalf("turn effort = %#v", turn["effort"])
	}
	if _, ok := turn["reasoningEffort"]; ok {
		t.Fatalf("turn/start sent old reasoningEffort: %#v", turn)
	}
	if _, ok := turn["permissions"]; ok {
		t.Fatalf("turn/start sent unsupported permissions: %#v", turn)
	}
	if !reflect.DeepEqual(caps, map[string]any{}) {
		t.Fatalf("capability params = %#v", caps)
	}
	if got := client.Receipt(); got.RequestedModel != "gpt-5.6-sol" || got.ObservedUserAgent == "" || !reflect.DeepEqual(got.RequestedPermissions, []string{"read-only"}) {
		t.Fatalf("receipt = %+v", got)
	}
	if len(transport.notifies) != 1 || transport.notifies[0].method != "initialized" || transport.notifies[0].params != nil {
		t.Fatalf("initialized notification = %+v", transport.notifies)
	}
	if len(transport.order) < 2 || transport.order[0] != "call:initialize" || transport.order[1] != "notify:initialized" {
		t.Fatalf("initialize ordering = %v", transport.order)
	}
}

func TestThreadForkFailsClosedAndRejectsParentAsChild(t *testing.T) {
	transport := appServerFake()
	client, err := NewAppServerClient(AppServerOptions{Transport: transport, NativeTransport: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	req := ThreadForkRequest{ParentThreadID: "thread-1", Model: "gpt-5.6-sol", Sandbox: "read-only", ApprovalPolicy: "never"}
	if _, err := client.ThreadFork(t.Context(), req); err == nil || !strings.Contains(err.Error(), "initialize is required") {
		t.Fatalf("pre-initialize fork error = %v", err)
	}
	if _, err := client.Initialize(t.Context(), InitializeRequest{ClientInfo: ClientInfo{Name: "atenea", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	transport.responses["thread/fork"] = json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}},"model":"gpt-5.6-sol"}`)
	if _, err := client.ThreadFork(t.Context(), req); err == nil || !strings.Contains(err.Error(), "invalid child thread id") {
		t.Fatalf("parent-as-child error = %v", err)
	}
}

func TestAppServerVisibilityFailsClosedBeforeNonNativeCall(t *testing.T) {
	transport := appServerFake()
	client, err := NewAppServerClient(AppServerOptions{Transport: transport, NativeTransport: false})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.ThreadStart(t.Context(), ThreadStartRequest{Model: "gpt-5.6-sol", VisibilityRequired: true}); err == nil {
		t.Fatal("visibility requirement was ignored")
	}
	if len(transport.calls) != 0 {
		t.Fatalf("transport called before native visibility check: %+v", transport.calls)
	}
}

func TestThreadStartRejectsMutableDefaultsAndLegacyPermissions(t *testing.T) {
	transport := appServerFake()
	client, err := NewAppServerClient(AppServerOptions{Transport: transport, NativeTransport: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Initialize(t.Context(), InitializeRequest{ClientInfo: ClientInfo{Name: "atenea", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ThreadStart(t.Context(), ThreadStartRequest{Model: "gpt-5.6-sol"}); err == nil || !strings.Contains(err.Error(), "requires model, sandbox and approval policy") {
		t.Fatalf("missing restrictions error = %v", err)
	}
	if _, err := client.ThreadStart(t.Context(), ThreadStartRequest{Model: "gpt-5.6-sol", Sandbox: "read-only", ApprovalPolicy: "never", Permissions: "read"}); err == nil || !strings.Contains(err.Error(), "permissions is unsupported") {
		t.Fatalf("legacy permissions error = %v", err)
	}
}

func TestAppServerRerouteBlocksTurnAndTypedEvents(t *testing.T) {
	transport := appServerFake()
	client, err := NewAppServerClient(AppServerOptions{Transport: transport, NativeTransport: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Initialize(t.Context(), InitializeRequest{ClientInfo: ClientInfo{Name: "atenea", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ThreadStart(t.Context(), ThreadStartRequest{Model: "gpt-5.6-sol", Sandbox: "read-only", ApprovalPolicy: "never"}); err != nil {
		t.Fatal(err)
	}
	transport.handler(Notification{Method: "model/rerouted", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","fromModel":"gpt-5.6-sol","toModel":"other","reason":"unavailable"}`)})
	if _, err := client.TurnStart(t.Context(), TurnStartRequest{ThreadID: "thread-1", Prompt: "inspect", Model: "gpt-5.6-sol"}); !errors.Is(err, ErrModelRerouted) {
		t.Fatalf("reroute error = %v", err)
	}
	usage, err := ParseTokenUsageUpdated(Notification{Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":2,"outputTokens":3},"total":{"inputTokens":2,"outputTokens":3},"modelContextWindow":128000}}`)})
	if err != nil || usage.TokenUsage.Total.OutputTokens != 3 {
		t.Fatalf("usage = %+v, err=%v", usage, err)
	}
	route, err := ParseModelRerouted(Notification{Method: "model/rerouted", Params: json.RawMessage(`{"fromModel":"a","toModel":"b","reason":"test"}`)})
	if err != nil || route.FromModel != "a" || route.ToModel != "b" {
		t.Fatalf("route = %+v, err=%v", route, err)
	}
	transport.handler(Notification{Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"last":{},"total":{},"modelContextWindow":128000}}`)})
	if client.UsageRevision() != 1 {
		t.Fatalf("usage revision = %d", client.UsageRevision())
	}
	transport.handler(Notification{Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"last":{},"total":{},"modelContextWindow":128000}}`)})
	first := <-client.UsageEvents()
	second := <-client.UsageEvents()
	if first.Revision != 1 || second.Revision != 2 {
		t.Fatalf("usage event revisions = %d, %d", first.Revision, second.Revision)
	}
}

func TestAppServerRejectsInventedLegacyShapes(t *testing.T) {
	if _, err := ParseThreadStarted(json.RawMessage(`{"id":"thread-1"}`)); err == nil {
		t.Fatal("accepted flat thread/start response")
	}
	if _, err := ParseTurnStarted(json.RawMessage(`{"id":"turn-1"}`)); err == nil {
		t.Fatal("accepted flat turn/start response")
	}
	transportStatus := appServerFake()
	transportStatus.responses["thread/start"] = json.RawMessage(`{"thread":{"id":"thread-1","status":"active"},"model":"gpt-5.6-sol","modelProvider":"openai","reasoningEffort":"medium"}`)
	statusClient, err := NewAppServerClient(AppServerOptions{Transport: transportStatus, NativeTransport: true})
	if err != nil {
		t.Fatal(err)
	}
	defer statusClient.Close()
	if _, err := statusClient.Initialize(t.Context(), InitializeRequest{ClientInfo: ClientInfo{Name: "atenea", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := statusClient.ThreadStart(t.Context(), ThreadStartRequest{Model: "gpt-5.6-sol", Sandbox: "read-only", ApprovalPolicy: "never"}); err == nil {
		t.Fatal("accepted string thread status")
	}
	if _, err := ParseModelRerouted(Notification{Method: "model/rerouted", Params: json.RawMessage(`{"requestedModel":"a","observedModel":"b"}`)}); err == nil {
		t.Fatal("accepted old reroute shape")
	}
	transport := appServerFake()
	transport.responses["model/list"] = json.RawMessage(`{"models":[]}`)
	transport.responses["modelProvider/capabilities/read"] = json.RawMessage(`{"provider":"openai","capabilities":{}}`)
	transport.responses["thread/list"] = json.RawMessage(`{"threads":[]}`)
	client, err := NewAppServerClient(AppServerOptions{Transport: transport, NativeTransport: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Initialize(t.Context(), InitializeRequest{ClientInfo: ClientInfo{Name: "atenea", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ModelList(t.Context()); err == nil {
		t.Fatal("accepted old model/list shape")
	}
	if _, err := client.ModelProviderCapabilitiesRead(t.Context()); err == nil {
		t.Fatal("accepted old capabilities shape")
	}
	if _, err := client.ThreadList(t.Context()); err == nil {
		t.Fatal("accepted old thread/list shape")
	}
	if _, err := ParseTokenUsageUpdated(Notification{Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{"threadId":"t","turnId":"u","usage":{"totalTokens":5}}`)}); err == nil {
		t.Fatal("accepted old token usage shape")
	}
}

func TestInitializeRequiresRuntimeIdentityButDoesNotInventProtocol(t *testing.T) {
	transport := appServerFake()
	transport.responses["initialize"] = json.RawMessage(`{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"linux","userAgent":"codex-app-server/0.151.0"}`)
	client, err := NewAppServerClient(AppServerOptions{Transport: transport, NativeTransport: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Initialize(t.Context(), InitializeRequest{ClientInfo: ClientInfo{Name: "atenea", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	if got := client.Receipt(); got.ObservedProtocol != "" {
		t.Fatalf("protocol was invented: %+v", got)
	}
}

func TestAppServerProcessTransportUsesHermeticJSONRPCFixture(t *testing.T) {
	t.Setenv("ATENEA_CODEX_APPSERVER_HELPER", "1")
	transport, err := NewProcessTransport(os.Args[0], "-test.run=TestCodexAppServerHelper", "--")
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewAppServerClient(AppServerOptions{Transport: transport, NativeTransport: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Initialize(t.Context(), InitializeRequest{ClientInfo: ClientInfo{Name: "atenea", Version: "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ModelList(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCodexAppServerHelper(t *testing.T) {
	if os.Getenv("ATENEA_CODEX_APPSERVER_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		var result string
		switch request.Method {
		case "initialize":
			result = `{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"test","userAgent":"fixture"}`
		case "model/list":
			result = `{"data":[],"nextCursor":""}`
		default:
			result = `{}`
		}
		_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"result":%s}`+"\n", request.ID, result)
	}
	os.Exit(0)
}
