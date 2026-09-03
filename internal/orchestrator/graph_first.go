package orchestrator

import (
	"slices"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// exploration chooses contracts, not aliases: a prose task never becomes the
// query argument of a structural tool. Literal requests retain text search.
func (a *Agent) exploration(task string, repo contract.Repository) (string, map[string]any, string) {
	lower := strings.ToLower(task)
	literal := false
	for _, word := range []string{"literal", "regex", "grep", "texto exacto", "cadena exacta"} {
		literal = literal || strings.Contains(lower, word)
	}
	if !literal {
		capability, err := a.catalog.Capability(contextCapability)
		candidates, implErr := a.catalog.ImplementationsFor(contextCapability)
		reachable := a.runner.Implementations()
		if a.reach != nil {
			reachable, _ = a.reach(repo)
		}
		payload := map[string]any{"task": task, "limit": 10}
		if err == nil && implErr == nil && capability.ValidateInput(payload) == nil {
			for _, impl := range candidates {
				if impl.Provider == "kivgraph" && slices.Contains(reachable, impl.ID) {
					return contextCapability, payload, impl.ID
				}
			}
		}
	}
	payload := map[string]any{"query": task}
	a.hint(payload, "context_lines", probeContextLines)
	return searchCapability, payload, ""
}
