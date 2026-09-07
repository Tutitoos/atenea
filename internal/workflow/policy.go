package workflow

import (
	"slices"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// WorkflowPolicy is the immutable ceiling captured when a workflow is
// created. It survives configuration reloads and MCP reconnects. Session
// grants are intersected with Effects at the boundary; they never widen it.
type WorkflowPolicy struct {
	Name              string
	Version           string
	Digest            string
	Criterion         string
	Effects           []contract.Effect
	Operations        []contract.Operation
	MaxBudgetUSD      float64
	MaxDuration       time.Duration
	MaxTokens         int
	MaxRetries        int
	MaxParallelAgent  int
	MaxParallelReview int
}

func (p WorkflowPolicy) clone() WorkflowPolicy {
	p.Effects = slices.Clone(p.Effects)
	p.Operations = slices.Clone(p.Operations)
	return p
}

func (p WorkflowPolicy) valid() error {
	if p.MaxBudgetUSD < 0 || p.MaxDuration < 0 || p.MaxTokens < 0 || p.MaxRetries < 0 || p.MaxParallelAgent < 0 || p.MaxParallelReview < 0 {
		return contract.Fail(contract.FailureInvalidInput, "workflow policy has a negative limit")
	}
	for _, operation := range p.Operations {
		if !operation.Known() {
			return contract.Fail(contract.FailureInvalidInput, "workflow policy has unknown operation %q", operation)
		}
	}
	return nil
}
