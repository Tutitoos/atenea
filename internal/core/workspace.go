package core

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/workspacecontext"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// WorkspaceContext runs one ordinary code.context question per explicitly
// named repository. The workspace operation owns only validation and bounded
// fan-out; provider selection, per-repository cache and quality remain in the
// normal Agent path.
func (c *Core) WorkspaceContext(ctx context.Context, session *Session, req workspacecontext.Request) (workspacecontext.Result, error) {
	if c == nil || c.agent == nil {
		return workspacecontext.Result{}, contract.Fail(contract.FailureUnavailable, "workspace.context: core agent is unavailable")
	}
	maxParallel := c.settings.Orchestrator.MaxParallel
	if maxParallel <= 0 || maxParallel > workspacecontext.DefaultMaxParallel {
		maxParallel = workspacecontext.DefaultMaxParallel
	}
	coordinator, err := workspacecontext.New(workspacecontext.Config{
		MaxParallel:       maxParallel,
		StandingBudgetUSD: c.settings.Orchestrator.BudgetUSD,
	})
	if err != nil {
		return workspacecontext.Result{}, err
	}
	// MCP and other trusted callers may identify a configured repository by ID
	// alone. Resolve its path inside Core so the wire surface never needs to
	// disclose physical roots. A supplied root remains an assertion and is
	// checked by authorize below.
	for i := range req.Targets {
		repo, lookupErr := c.catalog.Repository(strings.TrimSpace(req.Targets[i].ID))
		if lookupErr != nil {
			return workspacecontext.Result{}, lookupErr
		}
		if strings.TrimSpace(req.Targets[i].Root) == "" {
			req.Targets[i].Root = repo.Path
		}
	}
	authorize := func(target workspacecontext.Target) error {
		repo, err := c.catalog.Repository(target.ID)
		if err != nil {
			return err
		}
		declared, err := workspacecontext.CanonicalRoot(repo.Path)
		if err != nil {
			return fmt.Errorf("repository %s path: %w", target.ID, err)
		}
		requested, err := workspacecontext.CanonicalRoot(target.Root)
		if err != nil {
			return err
		}
		if filepath.Clean(declared) != filepath.Clean(requested) {
			return fmt.Errorf("repository %s root is not its configured physical root", target.ID)
		}
		if session != nil && !session.Allows(contract.EffectRead) {
			return contract.Fail(contract.FailurePermissionDenied, "session may not authorize read for repository %s", target.ID)
		}
		return nil
	}
	dispatch := func(childCtx context.Context, child workspacecontext.ChildRequest) (workspacecontext.ChildResult, error) {
		payload := cloneWorkspacePayload(child.Payload)
		if child.Cursor != "" {
			if current, ok := payload["cursor"].(string); ok && strings.TrimSpace(current) != child.Cursor {
				return workspacecontext.ChildResult{}, fmt.Errorf("cursor for repository %s does not match its child request", child.Repository.ID)
			}
			payload["cursor"] = child.Cursor
		}
		question := orchestrator.Question{
			Capability: workspacecontext.ChildCapability,
			Repository: child.Repository.ID,
			Payload:    payload,
			Effects:    []contract.Effect{contract.EffectRead},
			BudgetUSD:  child.Permission.BudgetUSD,
		}
		var result *orchestrator.Result
		var runErr error
		if session != nil {
			result, runErr = session.Ask(childCtx, question)
		} else {
			result, runErr = c.agent.Ask(childCtx, question)
		}
		out := workspacecontext.ChildResult{}
		if result == nil || len(result.Steps) != 1 {
			if runErr == nil {
				runErr = fmt.Errorf("code.context for %s returned no single child step", child.Repository.ID)
			}
			return out, runErr
		}
		step := result.Steps[0]
		out.Result = cloneWorkspacePayload(step.Outcome.Result)
		out.Evidence = append([]contract.QueryEvidence(nil), step.Outcome.Evidence...)
		out.Provider = step.Decision.Chosen.Provider
		out.Implementation = step.Decision.Chosen.ID
		out.Notices = append([]string(nil), step.Outcome.Notices...)
		out.OutOfScope = step.Outcome.OutOfScope
		if step.Failure != "" && runErr == nil {
			runErr = fmt.Errorf("%s", step.Failure)
		}
		if step.Review.Parent != contract.VerdictOK && runErr == nil {
			runErr = fmt.Errorf("code.context for %s ended %s", child.Repository.ID, step.Review.Parent)
		}
		return out, runErr
	}
	return coordinator.Run(ctx, req, authorize, dispatch)
}

func cloneWorkspacePayload(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
