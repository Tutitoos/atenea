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
	Model        string
	Fallbacks    []string
	Backend      string
	Binary       string
	Capabilities []string
	Providers    map[string]string
	Tools        []string
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
	if strings.TrimSpace(r.Backend) != "" && strings.TrimSpace(r.Model) == "" {
		return Fail(FailureInvalidInput,
			"route: backend %q with no model: the record names the machinery and not what it runs", r.Backend)
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
