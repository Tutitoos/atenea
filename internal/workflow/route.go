package workflow

import (
	"encoding/json"

	"github.com/Tutitoos/atenea/pkg/contract"
)

type routeWire struct {
	Model                    string            `json:"model,omitempty"`
	RequestedModel           string            `json:"requested_model,omitempty"`
	ObservedModel            string            `json:"observed_model,omitempty"`
	Role                     string            `json:"role,omitempty"`
	ReasoningEffort          string            `json:"reasoning_effort,omitempty"`
	RequestedReasoningEffort string            `json:"requested_reasoning_effort,omitempty"`
	ObservedReasoningEffort  string            `json:"observed_reasoning_effort,omitempty"`
	Fallbacks                []string          `json:"fallbacks,omitempty"`
	Backend                  string            `json:"backend,omitempty"`
	Binary                   string            `json:"binary,omitempty"`
	Capabilities             []string          `json:"capabilities,omitempty"`
	Providers                map[string]string `json:"providers,omitempty"`
	Tools                    []string          `json:"tools,omitempty"`
	VisibilityRequired       bool              `json:"visibility_required,omitempty"`
	ThreadID                 string            `json:"thread_id,omitempty"`
}

func jsonRoute(route *contract.Route) string {
	if route == nil {
		return ""
	}
	raw, err := json.Marshal(routeWire{Model: route.Model, RequestedModel: route.RequestedModel, ObservedModel: route.ObservedModel,
		Role: route.Role, ReasoningEffort: route.ReasoningEffort,
		RequestedReasoningEffort: route.RequestedReasoningEffort, ObservedReasoningEffort: route.ObservedReasoningEffort,
		Fallbacks: route.Fallbacks, Backend: route.Backend,
		Binary: route.Binary, Capabilities: route.Capabilities,
		Providers: route.Providers, Tools: route.Tools, VisibilityRequired: route.VisibilityRequired, ThreadID: route.ThreadID})
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
	return &contract.Route{Model: wire.Model, RequestedModel: wire.RequestedModel, ObservedModel: wire.ObservedModel,
		Role: wire.Role, ReasoningEffort: wire.ReasoningEffort,
		RequestedReasoningEffort: wire.RequestedReasoningEffort, ObservedReasoningEffort: wire.ObservedReasoningEffort,
		Fallbacks: wire.Fallbacks, Backend: wire.Backend, Binary: wire.Binary,
		Capabilities: wire.Capabilities, Providers: wire.Providers, Tools: wire.Tools, VisibilityRequired: wire.VisibilityRequired, ThreadID: wire.ThreadID}, nil
}

func routeForGate(route *contract.Route) *routeWire {
	if route == nil {
		return nil
	}
	cloned := route.Clone()
	return &routeWire{Model: cloned.Model, RequestedModel: cloned.RequestedModel, ObservedModel: cloned.ObservedModel,
		Role: cloned.Role, ReasoningEffort: cloned.ReasoningEffort,
		RequestedReasoningEffort: cloned.RequestedReasoningEffort, ObservedReasoningEffort: cloned.ObservedReasoningEffort,
		Fallbacks: cloned.Fallbacks, Backend: cloned.Backend, Binary: cloned.Binary,
		Capabilities: cloned.Capabilities, Providers: cloned.Providers, Tools: cloned.Tools, VisibilityRequired: cloned.VisibilityRequired, ThreadID: cloned.ThreadID}
}

func routeFromGate(wire *routeWire) *contract.Route {
	if wire == nil {
		return nil
	}
	return &contract.Route{Model: wire.Model, RequestedModel: wire.RequestedModel, ObservedModel: wire.ObservedModel,
		Role: wire.Role, ReasoningEffort: wire.ReasoningEffort,
		RequestedReasoningEffort: wire.RequestedReasoningEffort, ObservedReasoningEffort: wire.ObservedReasoningEffort,
		Fallbacks: wire.Fallbacks, Backend: wire.Backend, Binary: wire.Binary,
		Capabilities: wire.Capabilities, Providers: wire.Providers, Tools: wire.Tools, VisibilityRequired: wire.VisibilityRequired, ThreadID: wire.ThreadID}
}
