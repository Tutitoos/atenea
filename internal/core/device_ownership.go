package core

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/Tutitoos/atenea/internal/toolstats"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type deviceOwner struct {
	conversation, device string
	busy                 bool
}
type deviceSessionState struct{ name, device, label, platform string }

func deviceDependent(tool string) bool {
	switch tool {
	case "open", "close", "snapshot", "screenshot", "click", "wait", "fill", "type", "press", "back", "home", "scroll", "swipe", "hover", "longpress", "gesture", "keyboard", "orientation":
		return true
	}
	return false
}

func deviceRealm(args map[string]any) string {
	// No tokens or payloads are retained. Independent daemons have distinct owners.
	var parts []string
	for _, key := range []string{"daemonBaseUrl", "tenant", "stateDir"} {
		value, _ := args[key].(string)
		parts = append(parts, value)
	}
	return strings.Join(parts, "\x00") + "\x00"
}

func deviceFailure(code, message string) error {
	return &contract.Failure{Kind: contract.FailureInvalidInput, Code: code, Message: message, HealthNeutral: true}
}

func deviceSessionRows(body map[string]any) ([]deviceSessionState, bool) {
	if rows, ok := body["sessions"].([]any); ok {
		sessions := make([]deviceSessionState, 0, len(rows))
		for _, value := range rows {
			row, ok := value.(map[string]any)
			if !ok {
				return nil, false
			}
			name, _ := row["name"].(string)
			id, _ := row["device_id"].(string)
			if id == "" {
				id, _ = row["id"].(string)
			}
			label, _ := row["device"].(string)
			platform, _ := row["platform"].(string)
			// The official MCP SDK returns a nested device, while the CLI
			// and daemon wire expose the same identity in flat fields.
			if device, ok := row["device"].(map[string]any); ok {
				id, _ = device["id"].(string)
				label, _ = device["name"].(string)
				platform, _ = device["platform"].(string)
			}
			if name == "" || id == "" {
				return nil, false
			}
			sessions = append(sessions, deviceSessionState{name, id, label, platform})
		}
		return sessions, true
	}
	for _, key := range []string{"structuredContent", "data", "result"} {
		if nested, ok := body[key].(map[string]any); ok {
			if rows, ok := deviceSessionRows(nested); ok {
				return rows, true
			}
		}
	}
	if content, ok := body["content"].([]any); ok {
		for _, value := range content {
			row, _ := value.(map[string]any)
			text, _ := row["text"].(string)
			var nested map[string]any
			if json.Unmarshal([]byte(text), &nested) == nil {
				if rows, ok := deviceSessionRows(nested); ok {
					return rows, true
				}
			}
		}
	}
	return nil, false
}

func deviceSessionListArgs(args map[string]any) map[string]any {
	// 0.20.10 filters an implicit default session by cwd, hiding explicit
	// named sessions. A read-only list with an explicit name bypasses that
	// default scope; it neither creates nor adopts this inspection session.
	request := map[string]any{"action": "list", "mcpOutputFormat": "json", "session": "atenea-session-inspection"}
	for _, key := range []string{"session", "cwd", "stateDir", "daemonBaseUrl", "daemonAuthToken", "tenant"} {
		if value, ok := args[key]; ok {
			request[key] = value
		}
	}
	return request
}

func (v *conversation) deviceSessionsState(ctx context.Context, backend rawBackend, args map[string]any) ([]deviceSessionState, error) {
	request := deviceSessionListArgs(args)
	_, call := v.core.stats.Begin(ctx, toolstats.Event{Level: "attempt", Tool: "raw.agent-device.session", Provider: "agent-device"})
	raw, err := backend.Call(ctx, "session", request)
	var body map[string]any
	if err == nil {
		err = json.Unmarshal(raw, &body)
	}
	observation := observeMCP(body, nil, err)
	call.Finish(observation.Outcome, observation.Code, observation.Reason)
	if err != nil {
		return nil, deviceFailure("session_state_unverified", "Could not read session state before the action; no action was sent. "+contract.RedactRaw(err.Error()))
	}
	if observation.Outcome != "ok" {
		return nil, deviceFailure("session_state_unverified", observation.Reason)
	}
	sessions, ok := deviceSessionRows(body)
	if !ok {
		return nil, deviceFailure("compatibility_unverified", "Session listing did not match the supported response; run device.help and diagnose the installed version.")
	}
	return sessions, nil
}

func (v *conversation) reserveDeviceCall(ctx context.Context, backend rawBackend, tool string, args map[string]any) (func(), error) {
	if !deviceDependent(tool) {
		return nil, nil
	}
	session, _ := args["session"].(string)
	if session == "" {
		return nil, deviceFailure("SESSION_NOT_FOUND", "Use an explicit dedicated session name when opening a flow.")
	}
	cwd, _ := args["cwd"].(string)
	if cwd == "" || !filepath.IsAbs(cwd) {
		return nil, deviceFailure("INVALID_ARGS", "Use an explicit absolute cwd for this device flow.")
	}
	realm := deviceRealm(args)
	key := realm + session
	ownerID := v.session.ID()
	device := ""
	for _, name := range []string{"udid", "serial", "device"} {
		if value, _ := args[name].(string); value != "" {
			device = value
			break
		}
	}
	if tool == "open" && device == "" {
		return nil, deviceFailure("INVALID_ARGS", "Open a dedicated session with an explicit udid, serial or device; list devices to choose a free target.")
	}
	// Ownership is checked both before and after the read-only upstream probe.
	check := func() error {
		if owner := v.core.deviceOwners[key]; owner != nil {
			if owner.conversation != ownerID {
				return deviceFailure("DEVICE_BUSY", "This session belongs to another flow. Choose a dedicated session and free device, or wait for its owner to finish.")
			}
			if owner.busy {
				return deviceFailure("DEVICE_BUSY", "A dependent operation in this flow is still running. Wait for its result before another action.")
			}
		} else if tool != "open" {
			return deviceFailure("SESSION_NOT_FOUND", "This session is not owned by this flow. Open a dedicated named session first; existing sessions are never adopted automatically.")
		}
		return nil
	}
	v.core.deviceOwnersMu.Lock()
	err := check()
	v.core.deviceOwnersMu.Unlock()
	if err != nil {
		return nil, err
	}
	sessions, err := v.deviceSessionsState(ctx, backend, args)
	if err != nil {
		return nil, err
	}
	v.core.deviceOwnersMu.Lock()
	defer v.core.deviceOwnersMu.Unlock()
	if err = check(); err != nil {
		return nil, err
	}
	found := false
	for _, state := range sessions {
		if state.name == session {
			found = true
			if v.core.deviceOwners[key] == nil {
				return nil, deviceFailure("DEVICE_BUSY", "The named session already exists outside this flow. Use another session and free device.")
			}
			if device != "" && device != state.device && device != state.label {
				return nil, deviceFailure("INVALID_ARGS", "Explicit device differs from the device bound to this session.")
			}
			device = state.device
		} else if device != "" && (device == state.device || device == state.label) {
			return nil, deviceFailure("DEVICE_BUSY", "This device is occupied by another session. Choose a free device or wait for the owning flow.")
		}
	}
	if !found && tool != "open" {
		return nil, deviceFailure("SESSION_NOT_FOUND", "The flow's upstream session is no longer active. No dependent action was sent.")
	}
	for otherKey, owner := range v.core.deviceOwners {
		if otherKey != key && strings.HasPrefix(otherKey, realm) && device != "" && owner.device == device {
			return nil, deviceFailure("DEVICE_BUSY", "A different flow has reserved this device; choose another device or wait for completion.")
		}
	}
	if v.core.deviceOwners == nil {
		v.core.deviceOwners = make(map[string]*deviceOwner)
	}
	owner := &deviceOwner{conversation: ownerID, device: device, busy: true}
	v.core.deviceOwners[key] = owner
	return func() { v.core.deviceOwnersMu.Lock(); owner.busy = false; v.core.deviceOwnersMu.Unlock() }, nil
}

func (v *conversation) forgetDeviceOwner(args map[string]any) {
	if v.core == nil {
		return
	}
	session, _ := args["session"].(string)
	v.core.deviceOwnersMu.Lock()
	defer v.core.deviceOwnersMu.Unlock()
	delete(v.core.deviceOwners, deviceRealm(args)+session)
}
