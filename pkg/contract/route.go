package contract

import "slices"

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
