package config

import (
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Workflow is how a graph of agent steps is scheduled: one ceiling per lane.
//
// The lanes exist so that auditing cannot be crowded out by the work it
// audits. One shared ceiling would fill every slot with agents at exactly the
// moment the machine is busiest, leaving the reviewers queued behind them and
// the answers piling up unjudged -- see [Pool].
type Workflow struct {
	// MaxParallelAgent caps how many steps in the agent lane run at once.
	// Zero means no ceiling, the same reading as orchestrator.max_parallel:
	// the real limit is the machine, and the machine differs everywhere.
	MaxParallelAgent int
	// MaxParallelReview caps the review lane.
	//
	// Sized the same as the agent lane by default. Nobody has measured what
	// reviewing costs against what it audits on this machine, and a default
	// that pretended otherwise would be a number with a story attached and no
	// reading behind it. What matters here is that the two are separate, not
	// that one is smaller.
	MaxParallelReview int
}

// Cap is the ceiling for one lane, or zero for no ceiling.
func (w Workflow) Cap(pool Pool) int {
	if pool == PoolReview {
		return w.MaxParallelReview
	}
	return w.MaxParallelAgent
}

// The lane defaults. Four is the orchestrator's ceiling, for the same reason:
// it keeps a laptop responsive, and it is a fixed number rather than one
// derived from the measurement base.
const (
	defaultMaxParallelAgent  = 4
	defaultMaxParallelReview = 4
)

type fileWorkflow struct {
	MaxParallelAgent  *int `toml:"max_parallel_agent"`
	MaxParallelReview *int `toml:"max_parallel_review"`
}

func (w fileWorkflow) build(source string) (Workflow, error) {
	out := Workflow{
		MaxParallelAgent:  defaultMaxParallelAgent,
		MaxParallelReview: defaultMaxParallelReview,
	}
	lanes := []struct {
		key   string
		value *int
		out   *int
	}{
		{"max_parallel_agent", w.MaxParallelAgent, &out.MaxParallelAgent},
		{"max_parallel_review", w.MaxParallelReview, &out.MaxParallelReview},
	}
	for _, lane := range lanes {
		if lane.value == nil {
			continue
		}
		if *lane.value < 0 || *lane.value > maxMaxParallel {
			return Workflow{}, contract.Fail(contract.FailureInvalidInput,
				"settings %s: workflow.%s must be between 0 and %d, got %d",
				source, lane.key, maxMaxParallel, *lane.value)
		}
		*lane.out = *lane.value
	}
	return out, nil
}
