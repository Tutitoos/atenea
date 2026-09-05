package core

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Tutitoos/atenea/internal/agentdevice"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func (v *conversation) validateDeviceCall(ctx context.Context, backend rawBackend, tool string, args map[string]any) error {
	if tool == "click" || tool == "wait" {
		tools, err := backend.Tools(ctx)
		if err != nil {
			return err
		}
		version := ""
		if observed, ok := backend.Backend.(interface{ Version() string }); ok {
			version = observed.Version()
		}
		var schema json.RawMessage
		for _, t := range tools {
			if t.Name == tool {
				schema = t.InputSchema
				break
			}
		}
		if err := agentdevice.Validate(version, tool, schema, args); err != nil {
			code := "INVALID_ARGS"
			if strings.Contains(err.Error(), "compatibility unverified") {
				code = "compatibility_unverified"
			}
			return &contract.Failure{Kind: contract.FailureInvalidInput, Code: code, Message: err.Error(), HealthNeutral: true}
		}
	}
	switch tool {
	case "snapshot", "screenshot", "click", "wait", "fill", "type", "press", "back", "home", "scroll", "swipe", "close", "hover", "longpress", "gesture", "keyboard", "orientation":
		v.deviceMu.Lock()
		defer v.deviceMu.Unlock()
		if session, _ := args["session"].(string); session == "" {
			if session, _ = v.deviceContext["session"].(string); session == "" {
				return &contract.Failure{Kind: contract.FailureInvalidInput, Code: "SESSION_NOT_FOUND", HealthNeutral: true, Message: "No session is bound to this conversation. Call atenea.command name=device.sessions, then use an explicit session and cwd belonging to this task."}
			}
			args["session"] = session
		}
		if args["session"] == v.deviceContext["session"] {
			for k, value := range v.deviceContext {
				if _, exists := args[k]; !exists {
					args[k] = value
				}
			}
		}
	}
	return nil
}

// Successful explicit opens establish context only for this conversation.
// No global default or other conversation's session is ever selected.
func (v *conversation) rememberDeviceContext(tool string, args map[string]any) {
	v.deviceMu.Lock()
	defer v.deviceMu.Unlock()
	if tool == "close" && args["session"] == v.deviceContext["session"] {
		v.deviceContext = nil
		v.forgetDeviceOwner(args)
		return
	}
	if tool != "open" {
		return
	}
	if session, _ := args["session"].(string); session == "" {
		return
	}
	v.deviceContext = map[string]any{}
	for _, key := range []string{"session", "cwd", "platform", "deviceTarget", "device", "udid", "serial", "stateDir", "daemonBaseUrl", "tenant"} {
		if value, exists := args[key]; exists {
			v.deviceContext[key] = value
		}
	}
}
