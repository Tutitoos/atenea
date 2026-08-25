package decision

import (
	"fmt"
	"strings"

	"github.com/Tutitoos/atenea/internal/floor"
)

// BudgetEstimate is a forecast for one model-backed step. It is separate
// from the provider receipt: the forecast admits or rejects a plan, while
// the workflow charge remains the source of truth after execution.
type BudgetEstimate struct {
	EstimatedUSD float64
	MinimumUSD   float64
	Source       string
}

// BudgetEstimator supplies measured or policy-backed estimates to the planner.
type BudgetEstimator interface {
	Estimate(repository, agent, model string) BudgetEstimate
}

// DefaultBudgetEstimator is the conservative first-install policy. It is
// deliberately above the observed cold starts for the configured Sonnet and
// Opus roles, leaving room for the answer after the repository is read.
type DefaultBudgetEstimator struct{}

// Estimate returns a conservative role/model cost forecast.
func (DefaultBudgetEstimator) Estimate(_, agent, model string) BudgetEstimate {
	base := 0.35
	switch agent {
	case "reader":
		base = 0.25
	case "plan":
		base = 0.45
	case "explore":
		base = 0.40
	}
	lower := strings.ToLower(model)
	if strings.Contains(lower, "opus") {
		base += 0.05
	}
	if strings.Contains(lower, "haiku") {
		base -= 0.05
	}
	if base < 0.10 {
		base = 0.10
	}
	return BudgetEstimate{EstimatedUSD: base, MinimumUSD: base * 0.80,
		Source: "conservative role/model baseline"}
}

// MeasuredBudgetEstimator combines the persisted cold-start floor with the
// conservative whole-step role forecast. A floor only prices startup, so it
// can raise a forecast but must never replace the answer allowance.
type MeasuredBudgetEstimator struct {
	Store *floor.Store
}

// Estimate returns the conservative forecast raised by a measured startup floor.
func (e MeasuredBudgetEstimator) Estimate(repository, agent, model string) BudgetEstimate {
	baseline := (DefaultBudgetEstimator{}).Estimate(repository, agent, model)
	if e.Store == nil {
		return baseline
	}
	measurement, ok, err := e.Store.Get(repository, agent, model)
	if err != nil || !ok {
		return baseline
	}
	startup := measurement.ColdStartUSD()
	if startup <= 0 {
		return baseline
	}
	// Source is the trace, and it has to describe the figure that was
	// actually returned. Stamped outside this branch it claimed a
	// measurement for every answer, including the ones where the measured
	// floor came in under the conservative baseline and the number handed
	// back was DefaultBudgetEstimator's -- so a reader chasing an estimate
	// went looking for a measurement that had no part in it.
	if startup*1.25 > baseline.EstimatedUSD {
		baseline.EstimatedUSD = startup * 1.25
		baseline.MinimumUSD = baseline.EstimatedUSD * 0.80
		baseline.Source = fmt.Sprintf("measured %s/%s/%s startup floor plus answer headroom",
			repository, agent, model)
	}
	return baseline
}
