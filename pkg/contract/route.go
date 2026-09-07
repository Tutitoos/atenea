package contract

import (
	"slices"
	"strings"
)

// Route is the execution choice made before an agent starts.
//
// It is deliberately metadata, not permission: Effects and BudgetUSD remain
// the authority boundary. Route makes the selected model, capability intent,
// provider hints and tool surface travel with the assignment so a spawned
// agent cannot silently fall back to a different model or an unreviewed tool
// set merely because it was launched by another caller.
type Route struct {
	Model string
	// RequestedModel is the canonical requested model. Model remains the
	// legacy alias used by older callers and is kept synchronized when a route
	// is created by Atenea.
	RequestedModel           string
	ObservedModel            string
	Role                     string
	ReasoningEffort          string
	RequestedReasoningEffort string
	ObservedReasoningEffort  string
	Fallbacks                []string
	Backend                  string
	Binary                   string
	Capabilities             []string
	Providers                map[string]string
	Tools                    []string
	// VisibilityRequired selects the native interactive transport for a model
	// turn. ThreadID is the durable App Server continuation identity; it is
	// opaque and must only be reused by the client that received it.
	VisibilityRequired bool
	ThreadID           string
	// ParentThreadID identifies the durable coordinator thread from which a
	// specialist thread was forked. NativeForkState is empty for ordinary
	// routes, pending while the external effect is uncertain, and complete
	// only after the distinct child ThreadID is durably stored.
	ParentThreadID  string
	NativeForkState string
}

// Validate refuses a route that cannot do the job the type exists for.
//
// The job is to make a decision survive being handed to somebody else, so the
// failure it must prevent is a route that reads as a decision and resolves to
// nothing. A named backend with no model is exactly that shape: the record
// says which machinery runs the turn and stays silent about what it runs,
// which is the silence the launched agent fills from whatever the machine's
// defaults happen to say -- the fallback Route was added to stop. An empty
// entry in the tool or fallback list is the same failure one element down.
//
// Capability ids are checked against the same expression Capability.Validate
// uses, because a route naming a capability the catalog could not have
// declared is a decision about nothing. Nothing here is a permission check:
// Effects and BudgetUSD remain the authority boundary, and this validator
// deliberately says nothing about whether the route was allowed.
func (r Route) Validate() error {
	if r.NativeForkState != "" && r.NativeForkState != "pending" && r.NativeForkState != "complete" {
		return Fail(FailureInvalidInput, "route: unknown native fork state %q", r.NativeForkState)
	}
	if r.NativeForkState != "" && strings.TrimSpace(r.ParentThreadID) == "" {
		return Fail(FailureInvalidInput, "route: native fork state requires parent thread id")
	}
	if r.NativeForkState == "complete" && strings.TrimSpace(r.ThreadID) == "" {
		return Fail(FailureInvalidInput, "route: completed native fork requires child thread id")
	}
	if r.NativeForkState == "pending" && strings.TrimSpace(r.ThreadID) != "" {
		return Fail(FailureInvalidInput, "route: pending native fork must not have child thread id")
	}
	if strings.TrimSpace(r.ParentThreadID) != "" && strings.TrimSpace(r.ParentThreadID) == strings.TrimSpace(r.ThreadID) {
		return Fail(FailureInvalidInput, "route: parent and child thread ids must differ")
	}
	if r.Model != "" && r.RequestedModel != "" && strings.TrimSpace(r.Model) != strings.TrimSpace(r.RequestedModel) {
		return Fail(FailureInvalidInput, "route: model and requested_model disagree")
	}
	if r.ReasoningEffort != "" && r.RequestedReasoningEffort != "" && strings.TrimSpace(r.ReasoningEffort) != strings.TrimSpace(r.RequestedReasoningEffort) {
		return Fail(FailureInvalidInput, "route: reasoning_effort and requested_reasoning_effort disagree")
	}
	if r.Role != "" {
		switch strings.ToLower(strings.TrimSpace(r.Role)) {
		case "explore", "research", "plan", "implement", "review", "audit":
		default:
			return Fail(FailureInvalidInput, "route: unknown role %q", r.Role)
		}
	}
	if r.ReasoningEffort != "" {
		switch strings.ToLower(strings.TrimSpace(r.ReasoningEffort)) {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		default:
			return Fail(FailureInvalidInput, "route: unknown reasoning effort %q", r.ReasoningEffort)
		}
	}
	if r.RequestedReasoningEffort != "" {
		switch strings.ToLower(strings.TrimSpace(r.RequestedReasoningEffort)) {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		default:
			return Fail(FailureInvalidInput, "route: unknown requested reasoning effort %q", r.RequestedReasoningEffort)
		}
	}
	if strings.TrimSpace(r.Backend) != "" && strings.TrimSpace(r.Model) == "" {
		if strings.TrimSpace(r.RequestedModel) == "" {
			return Fail(FailureInvalidInput,
				"route: backend %q with no model: the record names the machinery and not what it runs", r.Backend)
		}
	}
	for _, pair := range [2]struct {
		field string
		list  []string
	}{{"fallbacks", r.Fallbacks}, {"tools", r.Tools}} {
		for i, entry := range pair.list {
			if strings.TrimSpace(entry) == "" {
				return Fail(FailureInvalidInput,
					"route: %s[%d] is empty", pair.field, i)
			}
		}
	}
	for i, capability := range r.Capabilities {
		if !capabilityID.MatchString(capability) {
			return Fail(FailureInvalidInput,
				"route: capabilities[%d] %q is not a capability id", i, capability)
		}
	}
	for capability, provider := range r.Providers {
		if !capabilityID.MatchString(capability) {
			return Fail(FailureInvalidInput,
				"route: providers is keyed by %q, which is not a capability id", capability)
		}
		if strings.TrimSpace(provider) == "" {
			return Fail(FailureInvalidInput,
				"route: capability %q is routed to an empty provider", capability)
		}
	}
	return nil
}

// Clone returns an independent route.
func (r Route) Clone() Route {
	r.Capabilities = slices.Clone(r.Capabilities)
	r.Fallbacks = slices.Clone(r.Fallbacks)
	r.Tools = slices.Clone(r.Tools)
	if r.Providers != nil {
		r.Providers = make(map[string]string, len(r.Providers))
		for capability, provider := range r.Providers {
			r.Providers[capability] = provider
		}
	}
	return r
}
