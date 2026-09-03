package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/config"
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

// rawCatalogTimeout keeps one unavailable raw backend from stalling the
// complete desktop catalog. A client needs the tools that are ready now; a
// server that does not answer its own tools/list is reported as unavailable
// and can be retried on the next connection without holding every other raw
// server hostage.
const rawCatalogTimeout = 1 * time.Second

// The MCP surface. `atenea/status` is not part of it: that one is Atenea's own
// CLI asking after the service, it needs no chat behind it, and gating it on a
// handshake would mean `atenea status` had to pretend to be a model.
const (
	MethodInitialize = "initialize"
	MethodToolsList  = "tools/list"
	MethodToolsCall  = "tools/call"
	MethodCommand    = "atenea/command"

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
const routePreferArg = "_atenea_prefer"

// toolListRepositories is Atenea's own discovery tool: no repository required,
// no capability backing it, and no orchestrator in the path.
const toolListRepositories = "catalog.repositories"

// conversation is one connection's worth of state, which is exactly one chat.
//
// The session cannot be built until `initialize` says who is calling, and it
// has to die with the connection: a chats table that only grows is a leak that
// looks like popularity. Holding it here rather than in a map on the core means
// the lifetime is the connection's and nothing has to remember to clean up.
type conversation struct {
	core          *Core
	session       *Session
	clientName    string
	clientVersion string
	policy        desktopPolicy
	backendMu     sync.Mutex
	backends      map[string]passthrough.Backend
	// screen remembers whether this chat has been handed what is on the
	// display. See internal/core/tainted.go for why that has to be remembered
	// per chat rather than asked of the adapter.
	screen taint
}

func (v *conversation) close() {
	v.backendMu.Lock()
	for id, backend := range v.backends {
		backend.Close()
		delete(v.backends, id)
	}
	v.backendMu.Unlock()
	if v.session != nil {
		v.session.Close()
		v.session = nil
	}
}

// rawBackend returns the backend authorized for this conversation. Shared
// declarations reuse the process-wide session; per_chat declarations are lazy
// and keep exactly one upstream session in this connection until close.
func (v *conversation) rawBackend(id string) (rawBackend, bool) {
	declared, ok := v.core.backends[id]
	if !ok {
		return rawBackend{}, false
	}
	if declared.instance != config.InstancePerChat {
		return declared, true
	}
	v.backendMu.Lock()
	defer v.backendMu.Unlock()
	if v.backends == nil {
		v.backends = make(map[string]passthrough.Backend)
	}
	if backend, ok := v.backends[id]; ok {
		declared.Backend = backend
		return declared, true
	}
	backend := passthrough.New(declared.spec)
	v.backends[id] = backend
	declared.Backend = backend
	return declared, true
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
	case MethodCommand:
		result, rpcErr := v.command(req.Params)
		out.Result, out.Error = result, rpcErr
	case MethodStatus:
		out.Result = v.core.Status()
	case MethodDetect:
		result, rpcErr := v.detect(ctx, req.Params)
		out.Result, out.Error = result, rpcErr
	case MethodInitialize:
		result, rpcErr := v.initialize(req.Params)
		out.Result, out.Error = result, rpcErr
	case MethodToolsList:
		result, rpcErr := v.toolsList(ctx)
		out.Result, out.Error = result, rpcErr
	case MethodToolsCall:
		result, rpcErr := v.toolsCall(ctx, req.Params)
		out.Result, out.Error = result, rpcErr
	case methodPromptsList:
		result, rpcErr := v.promptsList()
		out.Result, out.Error = result, rpcErr
	case methodPromptsGet:
		result, rpcErr := v.promptsGet(req.Params)
		out.Result, out.Error = result, rpcErr
	default:
		out.Error = &rpcError{Code: codeMethodUnknown, Message: "unknown method " + req.Method}
	}
	return out
}

func (v *conversation) command(raw json.RawMessage) (any, *rpcError) {
	if v.session == nil {
		return nil, notInitialized()
	}
	var req CommandRequest
	if len(raw) > 0 && string(raw) != "null" {
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "atenea/command: " + err.Error()}
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, &rpcError{Code: codeInvalidParams, Message: "atenea/command: expected one JSON object"}
		}
	}
	response, err := v.core.Command(context.Background(), req)
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	text := response.Markdown
	if strings.EqualFold(req.Format, "json") {
		text = CommandJSON(response)
	} else if strings.EqualFold(req.Format, "text") {
		text = commandText(response.Markdown)
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": text}},
		"structuredContent": response,
		"isError":           false,
	}, nil
}

// detect answers a probe request on the service's behalf, in the service's
// environment. That last clause is the entire reason this method exists.
//
// Params are optional: an empty body is every repository, the same default the
// command's --repo flag has. A malformed body is refused rather than read as
// "everything", because a caller that sent something meant something.
func (v *conversation) detect(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var params detectParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &rpcError{Code: codeInvalid, Message: "params: " + err.Error()}
		}
	}
	out, err := v.core.Detect(ctx, params.Repository)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	return out, nil
}

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
	Meta            struct {
		Atenea struct {
			Profile   string `json:"profile"`
			Workspace string `json:"workspace"`
			Origin    struct {
				Surface   string `json:"surface"`
				Transport string `json:"transport"`
			} `json:"origin"`
			Session struct {
				Title      string `json:"title"`
				ExternalID string `json:"external_id"`
			} `json:"session"`
		} `json:"atenea"`
	} `json:"_meta"`
	ClientInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
	Capabilities struct {
		Experimental struct {
			Atenea struct {
				// Grant is the chat saying it wants less than the
				// settings file gives clients. A pointer, because an
				// absent key and an empty list are different answers:
				// one is "you decide", the other is "I will only read".
				Grant *[]string `json:"grant"`
			} `json:"atenea"`
		} `json:"experimental"`
	} `json:"capabilities"`
}

// initialize opens the chat this connection speaks for.
//
// The client's own name goes on it, which is the only reason the status screen
// can show two clients at once -- and the only way anybody sees the isolation
// working rather than taking it on trust.
//
// A chat holds what `client_effects` grants clients, and may say at the door
// that it wants less. It can never say it wants more: widening lives in the
// settings file, where the operator can see it, and a client naming an effect
// that was withheld is refused here rather than at its first call.
//
// Narrowing is worth having because the alternative is a client that holds
// write for the whole conversation to make one call that needed it.
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
	// A chat asking for less is parsed before the chat is opened, so a name
	// nobody recognizes is refused as bad input rather than quietly ignored:
	// a client that misspells `write` and is handed the full grant anyway
	// would have asked for a restraint and been given the opposite.
	var asked []contract.Effect
	if raw := params.Capabilities.Experimental.Atenea.Grant; raw != nil {
		asked = make([]contract.Effect, 0, len(*raw))
		for _, name := range *raw {
			effect, err := contract.ParseEffect(name)
			if err != nil {
				return nil, &rpcError{Code: codeInvalidParams,
					Message: "initialize: capabilities.experimental.atenea.grant: " + err.Error()}
			}
			asked = append(asked, effect)
		}
	}
	// A second initialize on one connection is the same client saying hello
	// twice. Taking it as a new chat would leave the first stranded in the
	// table with nothing to close it.
	profileName := strings.TrimSpace(params.Meta.Atenea.Profile)
	policy := desktopPolicy{Fallback: "none"}
	if profileName != "" {
		profile, err := config.ResolveDesktopProfile(v.core.settings.DesktopProfiles, profileName, desktopClientID(name))
		if err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "initialize profile: " + err.Error()}
		}
		policy = desktopPolicyFromProfile(profile)
	}
	v.close()
	session, err := v.core.Open(SessionOptions{
		Client: name, Grant: asked,
		Title: params.Meta.Atenea.Session.Title, ExternalID: params.Meta.Atenea.Session.ExternalID,
		Workspace: params.Meta.Atenea.Workspace,
		Origin:    SessionOrigin{Surface: params.Meta.Atenea.Origin.Surface, Transport: params.Meta.Atenea.Origin.Transport},
	})
	if err != nil {
		code := codeInternal
		if contract.KindOf(err) == contract.FailurePermissionDenied {
			code = codeInvalidParams
		}
		return nil, &rpcError{Code: code, Message: "opening the chat: " + err.Error()}
	}
	v.session = session
	v.clientName = name
	v.clientVersion = strings.TrimSpace(params.ClientInfo.Version)
	v.policy = policy

	// What the chat ended up holding, said out loud. A client that asked for
	// nothing still learns what it has, and one that asked to be narrowed can
	// see that it was -- neither has to infer a permission from a refusal.
	granted := make([]string, 0, len(session.Grant()))
	for _, effect := range session.Grant() {
		granted = append(granted, effect.String())
	}

	return map[string]any{
		"protocolVersion": mcpVersion,
		"capabilities": map[string]any{
			// No listChanged: the catalog comes from a settings file read
			// at startup, so it cannot change under a connected client, and
			// promising notifications nobody will ever send is a promise a
			// client may wait on.
			"tools":   map[string]any{},
			"prompts": map[string]any{"listChanged": false},
			"experimental": map[string]any{
				"atenea": map[string]any{"grant": granted},
			},
		},
		"serverInfo": map[string]any{
			"name":    "atenea",
			"version": buildinfo.Version,
		},
		"instructions": "Atenea decides and delegates: each tool is a capability, " +
			"and the implementation that answers it is chosen per call. Most tools " +
			"take a repository. Call catalog.repositories first to discover what is " +
			"registered, the absolute path of each, and what each can answer.",
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
	tools := make([]map[string]any, 0, 4+len(capabilities))
	tools = append(tools, map[string]any{
		"name":        commandTool,
		"description": "Run a closed, read-only Atenea chat command. Markdown is returned by default for desktop clients; use format=json for integrations.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"name":           map[string]any{"type": "string", "enum": readOnlyCommands, "description": "Read-only Atenea command"},
			"format":         map[string]any{"type": "string", "enum": []string{"markdown", "json", "text"}},
			"capability":     map[string]any{"type": "string"},
			"implementation": map[string]any{"type": "string"},
			"repository":     map[string]any{"type": "string", "description": "Registered Atenea repository id"},
			"id":             map[string]any{"type": "string"},
			"type":           map[string]any{"type": "string"},
			"verdict":        map[string]any{"type": "string"},
			"open":           map[string]any{"type": "boolean"},
			"since":          map[string]any{"type": "string"},
			"limit":          map[string]any{"type": "integer", "minimum": 0},
			"all":            map[string]any{"type": "boolean"},
			"client":         map[string]any{"type": "string"},
			"profile":        map[string]any{"type": "string"},
		}, "required": []string{"name"}},
	})
	tools = append(tools, v.repositoriesTool())
	for _, tool := range v.workflowTools() {
		// Aimed like every capability: the agents a graph spawns run at a
		// repository context level, and a workflow that silently picked one
		// would put the answer somewhere the caller never named.
		if schema, ok := tool["inputSchema"].(map[string]any); ok {
			tool["inputSchema"] = v.aimable(schema)
		}
		tools = append(tools, tool)
	}
	for _, capability := range capabilities {
		if slices.Contains(v.core.settings.Orchestrator.ClientDeniedCapabilities, capability.ID) {
			continue
		}
		if !v.core.capabilityOffered(capability.ID) {
			continue
		}
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
		// A named raw catalog is an explicit desktop selection. Do not make a
		// focused agent-device profile wait for unrelated raw servers (for
		// example a diagram backend that is down); those servers remain
		// available to profiles without a catalog selection or through their
		// own direct MCP entries.
		if len(v.policy.RawCatalogs) > 0 {
			if _, selected := v.policy.RawCatalogs[id]; !selected {
				continue
			}
		}
		backend, ok := v.rawBackend(id)
		if !ok || backend.Backend == nil {
			continue
		}
		catalogCtx, cancel := context.WithTimeout(ctx, rawCatalogTimeout)
		offered, err := backend.Tools(catalogCtx)
		cancel()
		// The client is still told nothing -- see the paragraph above, which
		// has not changed. What changed is that the Core now remembers why,
		// so `atenea status` can name this server and this cause instead of
		// the operator having to notice an absence.
		v.core.recordBackendListing(id, err)
		if err != nil {
			continue
		}
		if reporter, ok := backend.Backend.(interface {
			CatalogDrift() passthrough.CatalogDrift
		}); ok {
			drift := reporter.CatalogDrift()
			if len(drift.Missing) > 0 || len(drift.Added) > 0 {
				v.core.recordBackendListingNote(id, fmt.Sprintf("catalog drift: missing=%d added=%d", len(drift.Missing), len(drift.Added)))
			}
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
			entry["inputSchema"] = normalizeDesktopSchema(tool.InputSchema)
			if len(tool.OutputSchema) > 0 && string(tool.OutputSchema) != "null" {
				var output any
				if err := json.Unmarshal(tool.OutputSchema, &output); err == nil && output != nil {
					entry["outputSchema"] = output
				}
			}
			tools = append(tools, entry)
		}
	}
	return map[string]any{"tools": v.filterDesktopTools(tools)}, nil
}

// aimable adds the repository argument to a capability's declared inputs.
//
// The capability's schema is not edited in place -- InputSchema builds a fresh
// map per call today, and a mutation that depends on that is a bug waiting for
// the day somebody caches it.
func (v *conversation) aimable(schema map[string]any) map[string]any {
	out := v.aimedAt(schema)
	properties, _ := out["properties"].(map[string]any)
	properties[routePreferArg] = map[string]any{
		"type":        "string",
		"description": "Optional implementation preference stamped by the decision router; it is honored only if it survives the selector.",
	}
	return out
}

// aimedAt adds the repository argument to a schema and makes it required when
// the machine has more than one to choose between.
//
// Split out of aimable because the workflow tools need exactly this half and
// none of the other: route_prefer is the selector's, and a workflow step names
// its own agent. They used to declare no repository at all, which meant a
// caller had no way to aim them and the code behind them fell back to the
// service's working directory.
func (v *conversation) aimedAt(schema map[string]any) map[string]any {
	repos := v.core.catalog.Repositories()
	ids := make([]string, 0, len(repos))
	for _, repo := range repos {
		ids = append(ids, repo.ID)
	}
	description := "Repository to work in. Registered: " + strings.Join(ids, ", ") +
		" — call catalog.repositories for absolute paths and per-repo capability details."
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
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
}

func (p toolsCallParams) argumentMap() (map[string]any, error) {
	raw := p.Arguments
	if len(raw) == 0 || string(raw) == "null" {
		raw = p.Input
	}
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var arguments map[string]any
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return nil, err
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	return arguments, nil
}

// toolsCall runs one capability and answers in the two shapes a client may
// read.
//
// The split between an error here and an error in the result is the caller's
// to act on: a protocol error means the request was malformed and sending it
// again unchanged is pointless, while `isError` is an answer -- work ran, it
// did not go well, and a model can read why and try something else.
func (v *conversation) toolsCall(ctx context.Context, raw json.RawMessage) (result any, rpcErr *rpcError) {
	started := time.Now()
	requestedTool := "unknown"
	fallbackUsed := false
	outcomeHint := ""
	defer func() {
		outcome, errorCode := compatibilityOutcome(result, rpcErr, outcomeHint, fallbackUsed)
		v.recordCompatibility(requestedTool, outcome, errorCode, started, fallbackUsed)
	}()
	if v.session == nil {
		return nil, notInitialized()
	}
	var params toolsCallParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call: " + err.Error()}
	}
	requestedTool = strings.TrimSpace(params.Name)
	params.Name, fallbackUsed = normalizeDesktopToolName(requestedTool)
	arguments, err := params.argumentMap()
	if err != nil {
		return desktopDiagnostic("invalid_arguments", requestedTool, params.Name, fallbackUsed,
			"tool arguments must be a JSON object", "Retry with arguments or input as a JSON object."), nil
	}
	if !v.policy.allows(params.Name) {
		outcomeHint = "denied"
		return desktopDiagnostic("profile_denied", requestedTool, params.Name, fallbackUsed,
			fmt.Sprintf("tool %q is not enabled for desktop profile %q", params.Name, v.policy.Profile),
			"Enable the tool in the Atenea desktop profile and retry."), nil
	}
	// The reserved segment is checked before the catalog, and it is checked
	// by name rather than by looking for a backend: a name in the reserved
	// namespace can never be a capability, so falling through to the catalog
	// would answer "unknown capability, did you mean..." for a tool whose
	// real problem is that its backend is not declared here.
	if server, tool, ok := passthrough.Split(params.Name); ok {
		result, rpcErr := v.rawCall(ctx, server, tool, params)
		if result, ok := result.(map[string]any); ok && fallbackUsed {
			annotateDesktopResult(result, requestedTool, params.Name, true)
		}
		return result, rpcErr
	}
	ctx, cancel := v.policy.withToolTimeout(ctx)
	defer cancel()
	if params.Name == toolListRepositories {
		return v.listRepositories()
	}
	if params.Name == commandTool {
		encoded, err := json.Marshal(arguments)
		if err != nil {
			return nil, &rpcError{Code: codeInternal, Message: "atenea.command arguments: " + err.Error()}
		}
		return v.command(encoded)
	}
	switch params.Name {
	case toolWorkflowCreate:
		return v.workflowCreate(ctx, arguments)
	case toolWorkflowLaunch:
		return v.workflowLaunch(ctx, arguments)
	}
	capability, err := v.core.catalog.Capability(params.Name)
	if err != nil {
		if v.policy.Fallback == "diagnostic" {
			fallbackUsed = true
			return desktopDiagnostic("unknown_tool", requestedTool, params.Name, true,
				fmt.Sprintf("unknown tool %q", params.Name),
				"Call tools/list and retry with an advertised tool name."), nil
		}
		// The registry's own answer names the near miss when there is one,
		// which is worth more to a model than "unknown tool".
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if slices.Contains(v.core.settings.Orchestrator.ClientDeniedCapabilities, capability.ID) {
		return desktopDiagnostic("profile_denied", requestedTool, params.Name, fallbackUsed,
			fmt.Sprintf("capability %q is not exposed to MCP clients", capability.ID),
			"Call tools/list and retry with an advertised tool name."), nil
	}
	if !v.core.capabilityOffered(capability.ID) {
		return desktopDiagnostic("not_offered", requestedTool, params.Name, fallbackUsed,
			fmt.Sprintf("capability %q is declared but has no reachable implementation", capability.ID),
			"Call tools/list and retry with an advertised tool name."), nil
	}
	// Before the permission gate rather than after it, so a chat that may not
	// authorize the effect at all is told that first: "you were never granted
	// this" is a different sentence from "you may not do it now", and hearing
	// the second when the first is true sends somebody to fix the wrong thing.
	if err := v.screen.refuseIfTainted(capability); err != nil {
		return desktopDiagnostic("permission_denied", requestedTool, params.Name, fallbackUsed,
			err.Error(), "Request the required permission before retrying."), nil
	}
	// Marked on the way in rather than on the way out, and the difference
	// matters: a capability that failed halfway may still have put a window's
	// contents in front of the caller, and the answer to "did this chat see
	// the screen" has to be yes in that case too.
	v.screen.note(capability.ID)

	payload := maps.Clone(arguments)
	// The repository is Atenea's argument, not the capability's, and the
	// capability's schema refuses keys it never declared. Leaving it in would
	// fail validation on a field this layer put there.
	repository, _ := payload[repositoryArg].(string)
	delete(payload, repositoryArg)
	prefer, _ := payload[routePreferArg].(string)
	delete(payload, routePreferArg)

	repository = strings.TrimSpace(repository)
	if repository == "" {
		repos := v.core.catalog.Repositories()
		if len(repos) != 1 {
			return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf(
				"%s is required: %d repositories are registered", repositoryArg, len(repos))}
		}
		repository = repos[0].ID
	}

	runResult, runErr := v.session.Ask(ctx, orchestrator.Question{
		Capability: capability.ID,
		Repository: repository,
		Prefer:     prefer,
		Payload:    payload,
	})
	if runErr != nil {
		code := "tool_failure"
		recommendation := "Inspect the tool error and retry if appropriate."
		if ctx.Err() != nil {
			code = "tool_timeout"
			recommendation = "Retry with a smaller request or a profile with a longer allowed timeout."
		}
		return desktopDiagnostic(code, requestedTool, params.Name, fallbackUsed, runErr.Error(), recommendation), nil
	}
	answer, ok := answerOf(runResult)
	if !ok {
		return desktopDiagnostic("tool_failure", requestedTool, params.Name, fallbackUsed,
			refusalOf(runResult), "Inspect the tool error and retry if appropriate."), nil
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
	backend, ok := v.rawBackend(server)
	if !ok {
		if v.policy.Fallback == "diagnostic" {
			return desktopDiagnostic("backend_unavailable", params.Name, params.Name, true,
				fmt.Sprintf("%s: no backend named %q is declared with expose = \"raw\"", params.Name, server),
				"Call tools/list and verify that the raw MCP backend is available."), nil
		}
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf(
			"%s: no backend named %q is declared with expose = \"raw\"", params.Name, server)}
	}
	if timeout := backend.declared.ToolTimeoutOf(tool); timeout > 0 {
		ctx = passthrough.WithTimeoutOverride(ctx, timeout)
	}
	started := time.Now()
	arguments, err := params.argumentMap()
	if err != nil {
		return desktopDiagnostic("invalid_arguments", params.Name, params.Name, false,
			"tool arguments must be a JSON object", "Retry with arguments or input as a JSON object."), nil
	}
	// What this tool was declared to cause, held against what this chat may
	// authorize -- the same rule a capability crosses in Session.entitled,
	// applied at the same boundary. Not a gate of its own: a second gate on
	// the same seam is how the first one stops being load-bearing.
	//
	// It runs before the budget check inside Call, so a tool nobody may
	// authorize is refused whether or not it was ever on the allow list. A
	// refusal is filed like any other answer, because an attempt that was
	// stopped is exactly what an audit is looking for.
	effects := backend.declared.EffectsFor(tool, arguments)
	if err := v.session.entitled(effects); err != nil {
		v.core.fileRawReceipt(v.session, params.Name, effects, started, err)
		return desktopDiagnostic("permission_denied", params.Name, params.Name, false,
			err.Error(), "Request the required permission before retrying."), nil
	}
	result, err := backend.Call(ctx, tool, arguments)
	v.core.fileRawReceipt(v.session, params.Name, effects, started, err)
	// A call is the other place a backend's state becomes known for free.
	// Only an unavailable or timed-out one counts against it; see
	// recordBackendCall for why a refusal must not.
	v.core.recordBackendCall(server, err)
	if err != nil {
		// A backend's refusal is an answer, not a protocol error: the model
		// asked for something real and can read why it did not work. The
		// same split the capability path already makes.
		code := "backend_unavailable"
		recommendation := "Check the MCP backend and retry."
		if ctx.Err() != nil {
			code = "tool_timeout"
			recommendation = "Retry with a smaller request or a longer backend timeout."
		}
		return desktopDiagnostic(code, params.Name, params.Name, false, err.Error(), recommendation), nil
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
	return normalizeDesktopResult(out), nil
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

func desktopDiagnostic(code, requested, normalized string, fallback bool, message, recommendation string) map[string]any {
	diagnostic := map[string]any{
		"error_code":      code,
		"requested_tool":  requested,
		"normalized_tool": normalized,
		"fallback_used":   fallback,
		"recommendation":  recommendation,
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": message}},
		"structuredContent": diagnostic,
		"_meta":             map[string]any{"atenea": diagnostic},
		"isError":           true,
	}
}

func annotateDesktopResult(result map[string]any, requested, normalized string, fallback bool) {
	meta, _ := result["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["atenea"] = map[string]any{
		"requested_tool":  requested,
		"normalized_tool": normalized,
		"fallback_used":   fallback,
	}
	result["_meta"] = meta
}

// repositoriesTool is the schema entry for catalog.repositories, inserted
// before all capabilities so a client reading top-to-bottom sees it first.
func (v *conversation) repositoriesTool() map[string]any {
	return map[string]any{
		"name": toolListRepositories,
		"description": "List every repository registered with Atenea: name, absolute path on disk, " +
			"languages present, and which providers hold a ready index. " +
			"Call this before any code or symbol tool when you do not already know the repository name. " +
			"Symbol tools (symbol.*) require serena and a language server for the repo's languages; " +
			"Dart does not currently have a serena language server, so symbol tools are refused for " +
			"Dart-only repositories. Code-search and impact tools require the repository to be " +
			"indexed by the provider (check indexed_by).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

// listRepositories answers catalog.repositories inline, with no orchestrator
// and no repository argument: it is the question you ask before you know
// which repository to name.
func (v *conversation) listRepositories() (any, *rpcError) {
	repos := v.core.catalog.Repositories()
	entries := make([]map[string]any, 0, len(repos))
	for _, repo := range repos {
		abs, err := filepath.Abs(repo.Path)
		if err != nil {
			abs = repo.Path
		}
		entries = append(entries, map[string]any{
			"id":         repo.ID,
			"path":       abs,
			"languages":  repo.Languages,
			"indexed_by": repo.Indexes(),
		})
	}
	return toolResult(map[string]any{"repositories": entries})
}

// toolResult is the shape every tools/call answer takes: the text a client
// renders and the same thing structured, which are one payload written twice
// rather than two payloads that could disagree.
func toolResult(result map[string]any) (any, *rpcError) {
	body, err := json.Marshal(result)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: "serializing the answer: " + err.Error()}
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(body)}},
		"structuredContent": result,
		"isError":           false,
	}, nil
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
