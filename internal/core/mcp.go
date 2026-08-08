package core

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/passthrough"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// mcpVersion is the one revision of MCP this server speaks.
//
// One, and not a list. Claiming a version means honoring its semantics, and a
// list is a promise about revisions nobody here has read. A client asking for
// something else is answered with this and decides for itself whether to go on
// -- which is what the specification asks for, and is honest in a way that
// echoing the client's own version back would not be.
const mcpVersion = "2025-06-18"

// The MCP surface. `atenea/status` is not part of it: that one is Atenea's own
// CLI asking after the service, it needs no chat behind it, and gating it on a
// handshake would mean `atenea status` had to pretend to be a model.
const (
	MethodInitialize = "initialize"
	MethodToolsList  = "tools/list"
	MethodToolsCall  = "tools/call"

	// notificationPrefix marks the messages that are owed no answer. Every
	// notification MCP defines lives under it.
	notificationPrefix = "notifications/"

	codeInvalidParams = -32602
	codeInternal      = -32603
)

// repositoryArg is Atenea's own argument, added to every tool and belonging to
// no capability.
//
// The unit of work is the repository, and a capability's declaration says
// nothing about which one to run against -- deliberately, because that is the
// orchestrator's question. A caller at a terminal answers it with `--repo`. A
// model has no equivalent unless the tool asks, so the tool asks.
const repositoryArg = "repository"

// conversation is one connection's worth of state, which is exactly one chat.
//
// The session cannot be built until `initialize` says who is calling, and it
// has to die with the connection: a chats table that only grows is a leak that
// looks like popularity. Holding it here rather than in a map on the core means
// the lifetime is the connection's and nothing has to remember to clean up.
type conversation struct {
	core    *Core
	session *Session
}

func (v *conversation) close() {
	if v.session != nil {
		v.session.Close()
		v.session = nil
	}
}

// dispatch answers one message, or returns nil for one that is owed no answer.
func (v *conversation) dispatch(ctx context.Context, req rpcRequest) *rpcResponse {
	// A notification carries no id and gets no reply. Answering one puts a
	// line on the wire the client is not reading for, and every response
	// after it is read as the answer to the wrong request.
	if req.ID == nil && strings.HasPrefix(req.Method, notificationPrefix) {
		return nil
	}
	out := &rpcResponse{JSONRPC: rpcVersion, ID: req.ID}
	if req.JSONRPC != rpcVersion {
		out.Error = &rpcError{Code: codeInvalid, Message: "jsonrpc must be " + rpcVersion}
		return out
	}
	switch req.Method {
	case MethodStatus:
		out.Result = v.core.Status()
	case MethodInitialize:
		result, rpcErr := v.initialize(req.Params)
		out.Result, out.Error = result, rpcErr
	case MethodToolsList:
		result, rpcErr := v.toolsList(ctx)
		out.Result, out.Error = result, rpcErr
	case MethodToolsCall:
		result, rpcErr := v.toolsCall(ctx, req.Params)
		out.Result, out.Error = result, rpcErr
	default:
		out.Error = &rpcError{Code: codeMethodUnknown, Message: "unknown method " + req.Method}
	}
	return out
}

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

// initialize opens the chat this connection speaks for.
//
// The client's own name goes on it, which is the only reason the status screen
// can show two clients at once -- and the only way anybody sees the isolation
// working rather than taking it on trust.
//
// No grant is asked for and none is given. A chat starts able to look and
// nothing else, and still runs under the settings file's standing grant,
// because a session grant only ever widens the operator's floor. A client that
// could name its own effects at the door would be granting itself permission.
func (v *conversation) initialize(raw json.RawMessage) (any, *rpcError) {
	var params initializeParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "initialize: " + err.Error()}
		}
	}
	name := strings.TrimSpace(params.ClientInfo.Name)
	if name == "" {
		name = "unknown"
	}
	// A second initialize on one connection is the same client saying hello
	// twice. Taking it as a new chat would leave the first stranded in the
	// table with nothing to close it.
	v.close()
	session, err := v.core.Open(SessionOptions{Client: name})
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: "opening the chat: " + err.Error()}
	}
	v.session = session

	return map[string]any{
		"protocolVersion": mcpVersion,
		"capabilities": map[string]any{
			// No listChanged: the catalog comes from a settings file read
			// at startup, so it cannot change under a connected client, and
			// promising notifications nobody will ever send is a promise a
			// client may wait on.
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "atenea",
			"version": buildinfo.Version,
		},
		"instructions": "Atenea decides and delegates: each tool is a capability, " +
			"and the implementation that answers it is chosen per call. The unit " +
			"of work is the repository, so every tool takes one.",
	}, nil
}

// toolsList turns the catalog into tools.
//
// Read from the registry every time rather than built once at startup: it is
// eight entries and a map walk, and a cached copy would be a second place the
// catalog lives.
func (v *conversation) toolsList(ctx context.Context) (any, *rpcError) {
	if v.session == nil {
		return nil, notInitialized()
	}
	capabilities := v.core.catalog.Capabilities()
	tools := make([]map[string]any, 0, len(capabilities))
	for _, capability := range capabilities {
		input, err := capability.InputSchema()
		if err != nil {
			return nil, &rpcError{Code: codeInternal,
				Message: fmt.Sprintf("%s: input schema: %v", capability.ID, err)}
		}
		output, err := capability.OutputSchema()
		if err != nil {
			return nil, &rpcError{Code: codeInternal,
				Message: fmt.Sprintf("%s: output schema: %v", capability.ID, err)}
		}
		tools = append(tools, map[string]any{
			"name":         capability.ID,
			"description":  capability.Summary,
			"inputSchema":  v.aimable(input),
			"outputSchema": output,
		})
	}
	// The backends' own tools come after the capabilities and are never
	// mixed into them: a client reading this list top to bottom sees what
	// Atenea promises first and what it merely forwards second. A backend
	// that does not answer is left out rather than listed as broken --
	// telling a model about a tool that cannot run is worse than not
	// mentioning it, and `atenea wrap` already reports a server that is down
	// where an operator will read it.
	for _, id := range slices.Sorted(maps.Keys(v.core.backends)) {
		backend := v.core.backends[id]
		offered, err := backend.Tools(ctx)
		if err != nil {
			continue
		}
		for _, tool := range offered {
			entry := map[string]any{
				"name":        passthrough.Name(id, tool.Name),
				"description": tool.Description,
			}
			// The schema is forwarded exactly as the backend gave it, and
			// the repository argument is not added: that argument is
			// Atenea's own question about which repository a capability
			// runs in, and a raw tool has no idea what a repository is.
			if len(tool.InputSchema) > 0 {
				entry["inputSchema"] = tool.InputSchema
			} else {
				entry["inputSchema"] = map[string]any{"type": "object"}
			}
			tools = append(tools, entry)
		}
	}
	return map[string]any{"tools": tools}, nil
}

// aimable adds the repository argument to a capability's declared inputs.
//
// The capability's schema is not edited in place -- InputSchema builds a fresh
// map per call today, and a mutation that depends on that is a bug waiting for
// the day somebody caches it.
func (v *conversation) aimable(schema map[string]any) map[string]any {
	repos := v.core.catalog.Repositories()
	ids := make([]string, 0, len(repos))
	for _, repo := range repos {
		ids = append(ids, repo.ID)
	}
	description := "Repository to work in. Registered: " + strings.Join(ids, ", ")
	if len(ids) == 0 {
		description = "Repository to work in. None are registered on this machine."
	}

	out := maps.Clone(schema)
	if out == nil {
		out = map[string]any{"type": "object"}
	}
	properties, _ := out["properties"].(map[string]any)
	properties = maps.Clone(properties)
	if properties == nil {
		properties = map[string]any{}
	}
	properties[repositoryArg] = map[string]any{
		"type":        "string",
		"description": description,
	}
	out["properties"] = properties

	// Required only when the machine has a choice to make. With one
	// repository registered, demanding its name of every caller is ceremony
	// -- the CLI does not, and the two should not disagree.
	if len(ids) > 1 {
		required, _ := out["required"].([]string)
		if existing, ok := out["required"].([]any); ok {
			required = make([]string, 0, len(existing))
			for _, item := range existing {
				required = append(required, fmt.Sprint(item))
			}
		}
		out["required"] = append(slices.Clone(required), repositoryArg)
	}
	return out
}

type toolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// toolsCall runs one capability and answers in the two shapes a client may
// read.
//
// The split between an error here and an error in the result is the caller's
// to act on: a protocol error means the request was malformed and sending it
// again unchanged is pointless, while `isError` is an answer -- work ran, it
// did not go well, and a model can read why and try something else.
func (v *conversation) toolsCall(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	if v.session == nil {
		return nil, notInitialized()
	}
	var params toolsCallParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call: " + err.Error()}
	}
	// The reserved segment is checked before the catalog, and it is checked
	// by name rather than by looking for a backend: a name in the reserved
	// namespace can never be a capability, so falling through to the catalog
	// would answer "unknown capability, did you mean..." for a tool whose
	// real problem is that its backend is not declared here.
	if server, tool, ok := passthrough.Split(params.Name); ok {
		return v.rawCall(ctx, server, tool, params)
	}
	capability, err := v.core.catalog.Capability(params.Name)
	if err != nil {
		// The registry's own answer names the near miss when there is one,
		// which is worth more to a model than "unknown tool".
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}

	payload := maps.Clone(params.Arguments)
	if payload == nil {
		payload = map[string]any{}
	}
	// The repository is Atenea's argument, not the capability's, and the
	// capability's schema refuses keys it never declared. Leaving it in would
	// fail validation on a field this layer put there.
	repository, _ := payload[repositoryArg].(string)
	delete(payload, repositoryArg)

	repository = strings.TrimSpace(repository)
	if repository == "" {
		repos := v.core.catalog.Repositories()
		if len(repos) != 1 {
			return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf(
				"%s is required: %d repositories are registered", repositoryArg, len(repos))}
		}
		repository = repos[0].ID
	}

	result, runErr := v.session.Ask(ctx, orchestrator.Question{
		Capability: capability.ID,
		Repository: repository,
		Payload:    payload,
	})
	if runErr != nil {
		return toolFailure(runErr.Error()), nil
	}
	answer, ok := answerOf(result)
	if !ok {
		return toolFailure(refusalOf(result)), nil
	}
	// The text block carries the same answer serialized, because a client
	// that cannot read structuredContent must not get a different story --
	// and because the specification asks for it.
	body, err := json.Marshal(answer)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: "serializing the answer: " + err.Error()}
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(body)}},
		"structuredContent": answer,
		"isError":           false,
	}, nil
}

// rawCall forwards one tool to its backend and files the receipt for it.
//
// Nothing about this path touches the orchestrator, the selector or the
// measurement base, and each omission is a decision rather than a shortcut. A
// raw tool has no competitor, so a funnel would be a stage with one candidate
// and a measurement would be a number nothing can be compared against. What
// it does keep is the receipt: a call that reached somebody else's server on
// this machine's behalf is exactly the kind of thing an operator later needs
// to find, and "it was only a passthrough" is how a trail grows holes.
func (v *conversation) rawCall(ctx context.Context, server, tool string, params toolsCallParams) (any, *rpcError) {
	backend, ok := v.core.backends[server]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf(
			"%s: no backend named %q is declared with expose = \"raw\"", params.Name, server)}
	}
	started := time.Now()
	// What this tool was declared to cause, held against what this chat may
	// authorize -- the same rule a capability crosses in Session.entitled,
	// applied at the same boundary. Not a gate of its own: a second gate on
	// the same seam is how the first one stops being load-bearing.
	//
	// It runs before the budget check inside Call, so a tool nobody may
	// authorize is refused whether or not it was ever on the allow list. A
	// refusal is filed like any other answer, because an attempt that was
	// stopped is exactly what an audit is looking for.
	effects := backend.declared.EffectsOf(tool)
	if err := v.session.entitled(effects); err != nil {
		v.core.fileRawReceipt(v.session, params.Name, effects, started, err)
		return toolFailure(err.Error()), nil
	}
	result, err := backend.Call(ctx, tool, params.Arguments)
	v.core.fileRawReceipt(v.session, params.Name, effects, started, err)
	if err != nil {
		// A backend's refusal is an answer, not a protocol error: the model
		// asked for something real and can read why it did not work. The
		// same split the capability path already makes.
		return toolFailure(err.Error()), nil
	}
	// The backend's own result is handed back whole. It already carries
	// `content` and may carry `structuredContent` and `isError`; re-wrapping
	// it here would mean this layer deciding what a tool it knows nothing
	// about meant to say.
	var out map[string]any
	if err := json.Unmarshal(result, &out); err != nil {
		return nil, &rpcError{Code: codeInternal, Message: fmt.Sprintf(
			"%s: the backend's answer is not an object: %v", params.Name, err)}
	}
	return out, nil
}

// answerOf finds the one answer in a result, and says so when there is none.
func answerOf(result *orchestrator.Result) (map[string]any, bool) {
	if result == nil || len(result.Steps) != 1 {
		return nil, false
	}
	step := result.Steps[0]
	if step.Review.Parent != contract.VerdictOK {
		return nil, false
	}
	return step.Outcome.Result, true
}

// refusalOf explains a run that produced no answer, in the words the run used.
func refusalOf(result *orchestrator.Result) string {
	if result == nil {
		return "the run produced no result"
	}
	for _, step := range result.Steps {
		if step.Failure != "" {
			return step.Failure
		}
	}
	if len(result.Steps) == 0 {
		return "nothing ran"
	}
	return "the run finished with verdict " + string(result.Steps[0].Review.Parent)
}

func toolFailure(message string) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": message}},
		"isError": true,
	}
}

// notInitialized refuses work to a client that never said who it was.
//
// Not pedantry about message order: until the handshake there is no chat, so
// the work would have nothing to be attributed to -- invisible on the status
// screen and outside the isolation every other caller runs inside.
func notInitialized() *rpcError {
	return &rpcError{Code: codeInvalid,
		Message: "initialize must come first: there is no chat to work on behalf of yet"}
}
