package workflow

import (
	"encoding/json"

	"github.com/Tutitoos/atenea/pkg/contract"
)

type routeWire struct {
	Model        string            `json:"model,omitempty"`
	Fallbacks    []string          `json:"fallbacks,omitempty"`
	Backend      string            `json:"backend,omitempty"`
	Binary       string            `json:"binary,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Providers    map[string]string `json:"providers,omitempty"`
	Tools        []string          `json:"tools,omitempty"`
}

func jsonRoute(route *contract.Route) string {
	if route == nil {
		return ""
	}
	raw, err := json.Marshal(routeWire{Model: route.Model, Fallbacks: route.Fallbacks, Backend: route.Backend,
		Binary: route.Binary, Capabilities: route.Capabilities,
		Providers: route.Providers, Tools: route.Tools})
	if err != nil {
		return ""
	}
	return string(raw)
}

func readRoute(raw string) (*contract.Route, error) {
	if raw == "" {
		return nil, nil
	}
	var wire routeWire
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return nil, contract.Fail(contract.FailureUnavailable, "workflow: reading route: %v", err)
	}
	return &contract.Route{Model: wire.Model, Fallbacks: wire.Fallbacks, Backend: wire.Backend, Binary: wire.Binary,
		Capabilities: wire.Capabilities, Providers: wire.Providers, Tools: wire.Tools}, nil
}

func routeForGate(route *contract.Route) *routeWire {
	if route == nil {
		return nil
	}
	cloned := route.Clone()
	return &routeWire{Model: cloned.Model, Fallbacks: cloned.Fallbacks, Backend: cloned.Backend, Binary: cloned.Binary,
		Capabilities: cloned.Capabilities, Providers: cloned.Providers, Tools: cloned.Tools}
}

func routeFromGate(wire *routeWire) *contract.Route {
	if wire == nil {
		return nil
	}
	return &contract.Route{Model: wire.Model, Fallbacks: wire.Fallbacks, Backend: wire.Backend, Binary: wire.Binary,
		Capabilities: wire.Capabilities, Providers: wire.Providers, Tools: wire.Tools}
}
