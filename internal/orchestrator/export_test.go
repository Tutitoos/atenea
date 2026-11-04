package orchestrator

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/observability"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// DispatchPlan runs a hand-built plan through the same dispatch Ask uses.
//
// Ask only ever builds a one-step plan, so waves, the parallel ceiling, the
// per-wave budget shares and blocked branches are reachable in production only
// through that single step. The tests of that machinery need a plan with more
// steps than that, and this is the door they use; it is compiled into the test
// binary only. The graph is copied before execution so a caller can reuse its
// plan after the run.
func (a *Agent) DispatchPlan(ctx context.Context, plan contract.Plan, budgetUSD float64) (result *Result, err error) {
	if strings.TrimSpace(plan.Task) == "" {
		return nil, contract.Fail(contract.FailureInvalidInput, "plan: task is required")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if err := validGrant("plan", budgetUSD); err != nil {
		return nil, err
	}
	if a.runner == nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"no runner is attached, so nothing can be dispatched")
	}
	if budgetUSD == 0 {
		budgetUSD = a.budget
	}

	plan = plan.Clone()
	started := time.Now()
	result = &Result{RunID: checkpoint.NewID(started), Task: plan.Task, Plan: plan}
	a.emit(observability.Event{Kind: "run.started", RunID: result.RunID})
	ctx = checkpoint.WithRunID(ctx, result.RunID)
	record := checkpoint.Run{
		RequestID: checkpoint.RequestID(ctx),
		ID:        result.RunID, Kind: checkpoint.KindAsk, Task: plan.Task,
		Started: started, BudgetUSD: budgetUSD,
		ContractVersion: contract.Current.String(), Plan: plan,
	}
	seenRepos := make(map[string]struct{}, len(plan.Steps))
	for _, step := range plan.Steps {
		seenRepos[step.Repository] = struct{}{}
	}
	for repo := range seenRepos {
		record.Repositories = append(record.Repositories, repo)
	}
	slices.Sort(record.Repositories)

	defer func() {
		result.Spent = totalSpent(result.Steps)
		result.Elapsed = time.Since(started)
		result.SpentUSD, result.SpentUSDKnown = totalUSD(result.Steps)
		result.Verdict = overallVerdict(result.Steps, ctx.Err())
		result.Discoveries = append(result.Discoveries, reported(result.Steps)...)
		record.Closed = true
		record.Verdict = result.Verdict.String()
		record.Updated = time.Now()
		if saveErr := a.checkpoints.Save(record); saveErr != nil && err == nil {
			err = saveErr
		}
		a.emit(observability.Event{Kind: "run.closed", RunID: result.RunID, State: result.Verdict.String(), DurationMS: result.Elapsed.Milliseconds(), Tokens: int64(result.Spent.Tokens)})
	}()

	_, err = a.dispatch(ctx, plan, PhaseAsk, result, &record, newGrant(budgetUSD), nil)
	return result, err
}
