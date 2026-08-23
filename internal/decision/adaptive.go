package decision

import "fmt"

// ModelPerformance is the small evidence surface needed by adaptive model
// choice. Persistence stays outside the decision package.
type ModelPerformance struct {
	Samples       int
	MedianUSD     float64
	MedianLatency int64 // milliseconds; zero means latency was not measured
}

// ModelHistory is read-only: planning may learn from completed work, never
// manufacture evidence while choosing a route.
type ModelHistory interface {
	Performance(repository, role, model string) (ModelPerformance, bool)
}

// ModelRanker chooses only among the configured primary model and fallbacks.
type ModelRanker interface {
	SelectModel(repository, role, primary string, candidates []string) (string, string)
}

// AdaptiveModelRanker changes the primary only after three successful samples
// prove a fallback is at least 15% cheaper. This prevents model flapping and
// never promotes a model that was not explicitly declared.
type AdaptiveModelRanker struct {
	History        ModelHistory
	MinimumSamples int
	MinimumSaving  float64
}

// SelectModel promotes a declared fallback only when measured history clears
// the configured sample and savings thresholds.
func (r AdaptiveModelRanker) SelectModel(repository, role, primary string, candidates []string) (string, string) {
	if primary == "" {
		return "", "no primary model configured"
	}
	if role == "plan" {
		return primary, "plan role is pinned to its configured high-reasoning model"
	}
	if r.History == nil {
		return primary, "configured primary; no model history available"
	}
	minSamples := r.MinimumSamples
	if minSamples <= 0 {
		minSamples = 3
	}
	minSaving := r.MinimumSaving
	if minSaving <= 0 {
		minSaving = 0.15
	}
	base, ok := r.History.Performance(repository, role, primary)
	if !ok || base.Samples < minSamples || base.MedianUSD <= 0 {
		return primary, fmt.Sprintf("configured primary; waiting for %d successful samples", minSamples)
	}
	chosen, best := primary, base.MedianUSD
	for _, candidate := range candidates {
		if candidate == "" || candidate == primary {
			continue
		}
		seen, ok := r.History.Performance(repository, role, candidate)
		if !ok || seen.Samples < minSamples || seen.MedianUSD <= 0 {
			continue
		}
		if seen.MedianUSD < best*(1-minSaving) {
			chosen, best = candidate, seen.MedianUSD
		}
	}
	if chosen == primary {
		return primary, fmt.Sprintf("configured primary; no declared fallback is at least %.0f%% cheaper", minSaving*100)
	}
	return chosen, fmt.Sprintf("adaptive choice: %s has %.0f%% lower measured median cost than %s", chosen,
		(1-best/base.MedianUSD)*100, primary)
}

// StaticModelRanker is the offline policy when no history store can be read.
type StaticModelRanker struct{}

// SelectModel keeps the configured primary when adaptive history is unavailable.
func (StaticModelRanker) SelectModel(_, _, primary string, _ []string) (string, string) {
	return primary, "configured primary; adaptive history unavailable"
}
