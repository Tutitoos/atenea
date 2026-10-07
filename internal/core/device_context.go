package core

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/Tutitoos/atenea/internal/agentdevice"
	"github.com/Tutitoos/atenea/internal/passthrough"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func deviceBackendVersion(backend rawBackend) string {
	if observed, ok := backend.Backend.(interface{ Version() string }); ok {
		return observed.Version()
	}
	return ""
}

func deviceBackendWorkspace(backend rawBackend) string {
	if bound, ok := backend.Backend.(interface{ WorkingDirectory() string }); ok {
		return bound.WorkingDirectory()
	}
	return ""
}

func verifiedDeviceWorkspace(backend rawBackend) bool {
	path := deviceBackendWorkspace(backend)
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func deviceContractError(err error) error {
	code := "INVALID_ARGS"
	if strings.Contains(err.Error(), "compatibility unverified") {
		code = "compatibility_unverified"
	}
	return &contract.Failure{Kind: contract.FailureInvalidInput, Code: code, Message: err.Error(), HealthNeutral: true}
}

func validateDeviceCatalog(catalog passthrough.CatalogContract, tool string, args map[string]any) error {
	version := catalog.Version
	if agentdevice.IsCandidate(version) {
		workspace := catalog.WorkingDirectory
		if !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
			return deviceFailure("compatibility_unverified", "agent-device 0.21.23 requires an explicit working_directory on its raw stdio backend; no action was sent.")
		}
		// Only the dedicated read-only inspection can inherit the operator binding.
		if tool == "session" && args["action"] == "list" {
			if _, supplied := args["cwd"]; !supplied {
				args["cwd"] = workspace
			}
		}
		if args["cwd"] != workspace {
			return deviceFailure("INVALID_ARGS", "cwd must equal this backend's configured working_directory; request context cannot redirect its process.")
		}
	}
	guarded := tool == "open" || tool == "click" || tool == "fill" || tool == "wait"
	if guarded || (version != "" && strings.TrimPrefix(version, "v") != agentdevice.Version) {
		var schema json.RawMessage
		for _, t := range catalog.Tools {
			if t.Name == tool {
				schema = t.InputSchema
				break
			}
		}
		if err := agentdevice.Validate(version, tool, schema, args); err != nil {
			return deviceContractError(err)
		}
	}
	return nil
}

func prepareDeviceContract(ctx context.Context, backend rawBackend, tool string, args map[string]any) (passthrough.CatalogContract, error) {
	version := deviceBackendVersion(backend)
	guarded := tool == "open" || tool == "click" || tool == "fill" || tool == "wait"
	if strings.TrimPrefix(version, "v") == agentdevice.Version && !guarded {
		return passthrough.CatalogContract{Version: version}, nil
	}
	if strings.TrimPrefix(version, "v") == agentdevice.Version && guarded {
		if atomic, ok := backend.Backend.(passthrough.ContractBackend); ok {
			catalog, err := atomic.ContractTools(ctx)
			if err != nil {
				return catalog, err
			}
			if catalog.Version != version {
				return catalog, deviceFailure("compatibility_unverified", "Upstream release changed during contract discovery; no action was sent.")
			}
			return catalog, validateDeviceCatalog(catalog, tool, args)
		}
	}
	if !agentdevice.IsCandidate(version) {
		tools, err := backend.Tools(ctx)
		if err != nil {
			return passthrough.CatalogContract{}, err
		}
		version = deviceBackendVersion(backend)
		if !agentdevice.IsCandidate(version) {
			if guarded && strings.TrimPrefix(version, "v") == agentdevice.Version {
				if atomic, ok := backend.Backend.(passthrough.ContractBackend); ok {
					catalog, err := atomic.ContractTools(ctx)
					if err != nil {
						return catalog, err
					}
					if catalog.Version != version {
						return catalog, deviceFailure("compatibility_unverified", "Upstream release changed during contract discovery; no action was sent.")
					}
					return catalog, validateDeviceCatalog(catalog, tool, args)
				}
			}
			catalog := passthrough.CatalogContract{Tools: tools, Version: version}
			return catalog, validateDeviceCatalog(catalog, tool, args)
		}
	}
	atomic, ok := backend.Backend.(passthrough.ContractBackend)
	if !ok {
		return passthrough.CatalogContract{}, deviceFailure("compatibility_unverified", "Candidate backend cannot bind validation to its dispatch process; no action was sent.")
	}
	catalog, err := atomic.ContractTools(ctx)
	if err != nil {
		return passthrough.CatalogContract{}, err
	}
	if catalog.Version != version {
		return passthrough.CatalogContract{}, deviceFailure("compatibility_unverified", "Upstream release changed during contract discovery; no action was sent.")
	}
	return catalog, validateDeviceCatalog(catalog, tool, args)
}

func validateDeviceContract(ctx context.Context, backend rawBackend, tool string, args map[string]any) error {
	_, err := prepareDeviceContract(ctx, backend, tool, args)
	return err
}

func callDeviceContract(ctx context.Context, backend rawBackend, tool string, args map[string]any, catalog passthrough.CatalogContract) (json.RawMessage, error) {
	if agentdevice.IsCandidate(catalog.Version) || catalog.Bound() {
		atomic, ok := backend.Backend.(passthrough.ContractBackend)
		if !ok {
			return nil, deviceFailure("compatibility_unverified", "Candidate backend cannot bind validation to dispatch; no action was sent.")
		}
		return atomic.CallContract(ctx, tool, agentdevice.WireArguments(catalog.Version, args), catalog)
	}
	return backend.Call(ctx, tool, args)
}

func (v *conversation) prepareDeviceCall(ctx context.Context, backend rawBackend, tool string, args map[string]any) (passthrough.CatalogContract, error) {
	catalog, err := prepareDeviceContract(ctx, backend, tool, args)
	if err != nil {
		return catalog, err
	}
	switch tool {
	case "snapshot", "screenshot", "click", "wait", "fill", "type", "press", "back", "home", "scroll", "swipe", "close", "hover", "longpress", "gesture", "keyboard", "orientation":
		v.deviceMu.Lock()
		defer v.deviceMu.Unlock()
		if session, _ := args["session"].(string); session == "" {
			if session, _ = v.deviceContext["session"].(string); session == "" {
				return catalog, deviceFailure("SESSION_NOT_FOUND", "No session is bound to this conversation. Call atenea.command name=device.sessions, then use an explicit session and cwd belonging to this task.")
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
	// A saved baseline context cannot inject old-only realm/runner fields.
	if agentdevice.IsCandidate(catalog.Version) {
		return catalog, validateDeviceCatalog(catalog, tool, args)
	}
	return catalog, nil
}

func (v *conversation) validateDeviceCall(ctx context.Context, backend rawBackend, tool string, args map[string]any) error {
	_, err := v.prepareDeviceCall(ctx, backend, tool, args)
	return err
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
