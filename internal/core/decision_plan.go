package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/decision"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const toolDecisionPlan = "decision.plan"

func (v *conversation) decisionPlanTool() map[string]any {
	schema := v.aimedAt(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"objective": map[string]any{"type": "string", "description": "The complete user objective to plan."},
			"criterion": map[string]any{"type": "string", "description": "Optional user-supplied acceptance criterion."},
			"files":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Repository-relative files explicitly named by the user."},
			"context": map[string]any{
				"type": "object", "additionalProperties": false,
				"description": "Optional caller-supplied semantic context for a continuation. It cannot prove user acceptance, grant effects or authorize execution.",
				"properties": map[string]any{
					"version":                map[string]any{"type": "integer", "const": 1},
					"repository":             map[string]any{"type": "string"},
					"accepted_plan_id":       map[string]any{"type": "string"},
					"accepted_plan_revision": map[string]any{"type": "string"},
					"accepted_plan_current":  map[string]any{"type": "boolean", "description": "Set true only after the caller verifies this accepted plan revision is still current."},
					"active_objective":       map[string]any{"type": "string"},
					"scope_files":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 100},
					"constraints":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 100},
				},
				"required": []string{"version"},
			},
			"budget_usd":   map[string]any{"type": "number", "minimum": 0, "description": "Optional planning grant; zero uses configured policy."},
			"max_duration": map[string]any{"type": "string", "description": "Optional positive duration such as 30m."},
			"max_tokens":   map[string]any{"type": "integer", "minimum": 0, "description": "Optional per-turn token declaration; requires max_duration."},
		},
		"required": []string{"objective"},
	})
	if len(v.core.catalog.Repositories()) > 1 {
		if required, ok := schema["required"].([]string); ok {
			filtered := make([]string, 0, len(required))
			for _, name := range required {
				if name != repositoryArg {
					filtered = append(filtered, name)
				}
			}
			schema["required"] = filtered
		}
		schema["anyOf"] = []any{
			map[string]any{"required": []string{repositoryArg}},
			map[string]any{"required": []string{"context"},
				"properties": map[string]any{"context": map[string]any{"required": []string{"repository"}}}},
		}
	}
	return map[string]any{
		"name": toolDecisionPlan,
		"description": "Build ATENEA's complete explainable decision and workflow graph for a Codex Plan-mode request, WITHOUT executing or persisting it. " +
			"For a short continuation or pronoun-only action, supply the accepted plan's current context; otherwise the result is needs_context. " +
			"This chooses intent, agents, models, capabilities, policy and budget. It never launches a workflow and does not authorize effects.",
		"inputSchema": schema,
	}
}

func (v *conversation) decisionPlan(_ context.Context, args map[string]any) (any, *rpcError) {
	objective, _ := args["objective"].(string)
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolDecisionPlan + ": objective is required"}
	}
	decisionContext, err := decisionPlanIntentContext(args["context"])
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolDecisionPlan + ": context: " + err.Error()}
	}
	repositoryArgs := args
	if repository, _ := args[repositoryArg].(string); strings.TrimSpace(repository) == "" && decisionContext != nil && decisionContext.Repository != "" {
		repositoryArgs = make(map[string]any, len(args)+1)
		for key, value := range args {
			repositoryArgs[key] = value
		}
		repositoryArgs[repositoryArg] = decisionContext.Repository
	}
	repository, aimErr := v.workflowRepository(toolDecisionPlan, repositoryArgs)
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
		Context:         decisionContext,
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

func decisionPlanIntentContext(raw any) (*decision.IntentContext, error) {
	if raw == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode context: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	var context decision.IntentContext
	if err := decoder.Decode(&context); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("unexpected trailing data")
		}
		return nil, fmt.Errorf("invalid trailing data: %w", err)
	}
	if context.Version == 0 {
		return nil, fmt.Errorf("version is required")
	}
	return &context, nil
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
