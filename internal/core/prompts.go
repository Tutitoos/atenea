package core

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	methodPromptsList = "prompts/list"
	methodPromptsGet  = "prompts/get"
)

type promptDefinition struct {
	Name        string
	Title       string
	Description string
	Arguments   []map[string]any
}

func commandPrompts() []promptDefinition {
	return []promptDefinition{
		{Name: "help", Title: "Atenea help", Description: "List read-only Atenea chat commands."},
		{Name: "status", Title: "Atenea status", Description: "Show Atenea service health and client policy."},
		{Name: "metrics", Title: "Atenea metrics", Description: "Show measured capability and provider metrics.", Arguments: metricPromptArguments()},
		{Name: "traces", Title: "Atenea traces", Description: "Show recent Atenea execution traces.", Arguments: []map[string]any{
			{"name": "id", "description": "Optional execution id", "required": false},
			{"name": "type", "description": "Optional trace type", "required": false},
			{"name": "verdict", "description": "Optional verdict filter", "required": false},
			{"name": "open", "description": "Only open traces: true or false", "required": false},
			{"name": "since", "description": "Positive duration such as 24h", "required": false},
			{"name": "limit", "description": "Optional row limit", "required": false},
		}},
		{Name: "catalog", Title: "Atenea catalog", Description: "Show capabilities and repositories."},
		{Name: "doctor", Title: "Atenea doctor", Description: "Show compatibility telemetry for a client.", Arguments: []map[string]any{
			{"name": "client", "description": "claude, chatgpt or codex", "required": false},
			{"name": "profile", "description": "Optional Atenea desktop profile", "required": false},
		}},
		{Name: "detect", Title: "Atenea detect", Description: "Probe declared providers and indexes.", Arguments: []map[string]any{
			{"name": "repository", "description": "Optional registered repository id", "required": false},
		}},
		{Name: "incidents", Title: "Atenea incidents", Description: "Show the crash notebook.", Arguments: []map[string]any{
			{"name": "all", "description": "Include already-read incidents: true or false", "required": false},
		}},
		{Name: "floor", Title: "Atenea floor", Description: "Show measured turn-start costs."},
		{Name: "config", Title: "Atenea config", Description: "Show a redacted effective settings summary."},
		{Name: "intent", Title: "Atenea intent", Description: "Show safe client declarations for a repository.", Arguments: []map[string]any{
			{"name": "repository", "description": "Registered repository id", "required": true},
		}},
	}
}

func metricPromptArguments() []map[string]any {
	return []map[string]any{
		{"name": "capability", "description": "Optional capability filter", "required": false},
		{"name": "implementation", "description": "Optional implementation filter", "required": false},
		{"name": "repository", "description": "Optional repository filter", "required": false},
	}
}

func (v *conversation) promptsList() (any, *rpcError) {
	if v.session == nil {
		return nil, notInitialized()
	}
	out := make([]map[string]any, 0, len(commandPrompts()))
	for _, prompt := range commandPrompts() {
		entry := map[string]any{
			"name": prompt.Name, "title": prompt.Title, "description": prompt.Description,
		}
		if len(prompt.Arguments) > 0 {
			entry["arguments"] = prompt.Arguments
		}
		out = append(out, entry)
	}
	return map[string]any{"prompts": out}, nil
}

func (v *conversation) promptsGet(raw json.RawMessage) (any, *rpcError) {
	if v.session == nil {
		return nil, notInitialized()
	}
	var params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "prompts/get: " + err.Error()}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, &rpcError{Code: codeInvalidParams, Message: "prompts/get: expected one JSON object"}
	}
	var definition promptDefinition
	found := false
	for _, candidate := range commandPrompts() {
		if candidate.Name == params.Name {
			definition, found = candidate, true
			break
		}
	}
	if !found {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown Atenea prompt %q", params.Name)}
	}
	request := CommandRequest{Name: definition.Name, Format: "markdown"}
	for key, value := range params.Arguments {
		switch key {
		case "capability":
			request.Capability = value
		case "implementation":
			request.Implementation = value
		case "repository":
			request.Repository = value
		case "id":
			request.ID = value
		case "type":
			request.Type = value
		case "verdict":
			request.Verdict = value
		case "open":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, &rpcError{Code: codeInvalidParams, Message: "prompts/get: open must be true or false"}
			}
			request.Open = parsed
		case "since":
			request.Since = value
		case "limit":
			limit, err := strconv.Atoi(value)
			if err != nil {
				return nil, &rpcError{Code: codeInvalidParams, Message: "prompts/get: limit must be an integer"}
			}
			request.Limit = limit
		case "client":
			request.Client = value
		case "profile":
			request.Profile = value
		case "all":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, &rpcError{Code: codeInvalidParams, Message: "prompts/get: all must be true or false"}
			}
			request.All = parsed
		default:
			return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("prompts/get: unknown argument %q", key)}
		}
	}
	encoded, _ := json.Marshal(request)
	text := "Call the Atenea MCP tool `atenea.command` with this JSON and present its Markdown response unchanged:\n\n" + string(encoded)
	return map[string]any{
		"description": definition.Description,
		"messages": []any{map[string]any{
			"role": "user", "content": map[string]any{"type": "text", "text": text},
		}},
	}, nil
}
