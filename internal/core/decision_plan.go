package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/decision"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const toolDecisionPlan = "decision.plan"

func (v *conversation) decisionPlanTool() map[string]any {
	return map[string]any{
		"name": toolDecisionPlan,
		"description": "Build ATENEA's complete explainable decision and workflow graph for a Codex Plan-mode request, WITHOUT executing or persisting it. " +
			"This chooses intent, agents, models, capabilities, policy and budget. It never launches a workflow and does not authorize effects.",
		"inputSchema": v.aimedAt(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"objective":    map[string]any{"type": "string", "description": "The complete user objective to plan."},
				"criterion":    map[string]any{"type": "string", "description": "Optional user-supplied acceptance criterion."},
				"files":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Repository-relative files explicitly named by the user."},
				"budget_usd":   map[string]any{"type": "number", "minimum": 0, "description": "Optional planning grant; zero uses configured policy."},
				"max_duration": map[string]any{"type": "string", "description": "Optional positive duration such as 30m."},
				"max_tokens":   map[string]any{"type": "integer", "minimum": 0, "description": "Optional per-turn token declaration; requires max_duration."},
			},
			"required": []string{"objective"},
		}),
	}
}

func (v *conversation) decisionPlan(_ context.Context, args map[string]any) (any, *rpcError) {
	objective, _ := args["objective"].(string)
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolDecisionPlan + ": objective is required"}
	}
	repository, aimErr := v.workflowRepository(toolDecisionPlan, args)
	if aimErr != nil {
		return nil, aimErr
	}
	files, err := decisionPlanFiles(args["files"])
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolDecisionPlan + ": " + err.Error()}
	}
	budget, ok := optionalNumber(args["budget_usd"])
	if !ok || budget < 0 {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolDecisionPlan + ": budget_usd must be a non-negative number"}
	}
	limits, err := decisionPlanLimits(args)
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolDecisionPlan + ": " + err.Error()}
	}
	criterion, _ := args["criterion"].(string)
	planner := decision.Planner{
		Config:    v.core.settings,
		Selector:  v.core,
		Estimator: decision.DefaultBudgetEstimator{},
		Ranker:    decision.StaticModelRanker{},
	}
	plan, err := planner.Build(decision.Request{
		Text:            objective,
		Criterion:       strings.TrimSpace(criterion),
		Limits:          limits,
		Repository:      repository,
		Files:           files,
		BudgetUSD:       budget,
		StandingEffects: v.core.settings.Orchestrator.StandingEffects,
	})
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: fmt.Sprintf("%s: encode result: %v", toolDecisionPlan, err)}
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, &rpcError{Code: codeInternal, Message: fmt.Sprintf("%s: encode result: %v", toolDecisionPlan, err)}
	}
	result["dry_run"] = true
	result["execution_authorized"] = false
	return toolResult(result)
}

func decisionPlanFiles(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("files must be an array of strings")
	}
	out := make([]string, 0, len(values))
	for _, rawValue := range values {
		value, ok := rawValue.(string)
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			return nil, fmt.Errorf("files must contain non-empty strings")
		}
		out = append(out, value)
	}
	return out, nil
}

func decisionPlanLimits(args map[string]any) (contract.Limits, error) {
	var limits contract.Limits
	if raw, exists := args["max_duration"]; exists {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return limits, fmt.Errorf("max_duration must be a positive duration")
		}
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil || duration <= 0 {
			return limits, fmt.Errorf("max_duration must be a positive duration")
		}
		limits.MaxDuration = duration
	}
	if raw, exists := args["max_tokens"]; exists {
		value, ok := optionalNumber(raw)
		if !ok || value < 0 || math.Trunc(value) != value || value > float64(math.MaxInt) {
			return limits, fmt.Errorf("max_tokens must be a non-negative integer")
		}
		limits.MaxTokens = int(value)
	}
	if limits.MaxTokens > 0 && limits.MaxDuration <= 0 {
		return limits, fmt.Errorf("max_tokens requires max_duration")
	}
	return limits, nil
}

func optionalNumber(raw any) (float64, bool) {
	if raw == nil {
		return 0, true
	}
	value, ok := raw.(float64)
	return value, ok && !math.IsNaN(value) && !math.IsInf(value, 0)
}
