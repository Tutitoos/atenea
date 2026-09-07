// Package modelagent runs the generic model-backed implementation, review and
// audit agent types. The role is carried by the assignment and is also part of
// the structured result, so a report cannot be mistaken for another stage.
package modelagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Tutitoos/atenea/internal/agent/model"
	"github.com/Tutitoos/atenea/internal/agent/planner"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type assignment struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	WorkflowID   string `json:"workflow_id"`
	Worktree     string `json:"worktree"`
	PolicyDigest string `json:"policy_digest"`
	GrantToken   string `json:"grant_token"`
	Task         struct {
		Objective string   `json:"objective"`
		Files     []string `json:"files"`
		Criterion string   `json:"criterion"`
	} `json:"task"`
	Limits struct {
		MaxTokens int `json:"max_tokens"`
	} `json:"limits"`
	BudgetUSD          *float64                   `json:"budget_usd"`
	Effects            []string                   `json:"effects"`
	Operations         []string                   `json:"operations"`
	Context            map[string]json.RawMessage `json:"context"`
	Route              *route                     `json:"route"`
	Subject            map[string]any             `json:"subject"`
	VisibilityRequired bool                       `json:"visibility_required,omitempty"`
	ThreadID           string                     `json:"thread_id,omitempty"`
	Invisible          bool                       `json:"invisible,omitempty"`
	CI                 bool                       `json:"ci,omitempty"`
}

type route struct {
	Model                    string `json:"model"`
	RequestedModel           string `json:"requested_model"`
	Role                     string `json:"role"`
	ReasoningEffort          string `json:"reasoning_effort"`
	RequestedReasoningEffort string `json:"requested_reasoning_effort"`
	Backend                  string `json:"backend"`
	Binary                   string `json:"binary"`
	VisibilityRequired       bool   `json:"visibility_required,omitempty"`
	ThreadID                 string `json:"thread_id,omitempty"`
	Invisible                bool   `json:"invisible,omitempty"`
	CI                       bool   `json:"ci,omitempty"`
}

type report struct {
	Result                   map[string]any  `json:"result"`
	ThreadID                 string          `json:"thread_id,omitempty"`
	TurnID                   string          `json:"turn_id,omitempty"`
	UsageRevision            uint64          `json:"usage_revision,omitempty"`
	RequestedModel           string          `json:"requested_model,omitempty"`
	ObservedModel            string          `json:"observed_model,omitempty"`
	RequestedReasoningEffort string          `json:"requested_reasoning_effort,omitempty"`
	ObservedReasoningEffort  string          `json:"observed_reasoning_effort,omitempty"`
	Verdict                  string          `json:"verdict"`
	Reason                   *reason         `json:"reason,omitempty"`
	Spent                    *planner.Charge `json:"spent,omitempty"`
	Completeness             *float64        `json:"completeness,omitempty"`
	StoppedAt                string          `json:"stopped_at,omitempty"`
	Notices                  []string        `json:"notices,omitempty"`
}

type reason struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Main is part of ATENEA's public orchestration contract.
func Main(ctx context.Context, stdin io.Reader, stdout io.Writer, kind string) error {
	var in assignment
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return fmt.Errorf("model agent assignment: %w", err)
	}
	out := run(ctx, in, kind)
	return json.NewEncoder(stdout).Encode(out)
}

func run(ctx context.Context, in assignment, kind string) report {
	role := model.Role(kind)
	if role != model.RoleImplement && role != model.RoleReview && role != model.RoleAudit {
		return failed(contract.FailureInvalidInput, "unsupported model agent role "+kind)
	}
	if in.Route != nil && strings.TrimSpace(in.Route.Role) != "" && strings.TrimSpace(in.Route.Role) != kind {
		return failed(contract.FailureInvalidInput,
			"assignment route role does not match the executable agent type")
	}
	root := repositoryRoot(in)
	cfg, err := config.LoadEffectiveIn("", root)
	if err != nil {
		return failed(contract.FailureUnavailable, "settings could not be read: "+contract.MessageOf(err))
	}
	if in.Route != nil {
		// A non-empty route role was checked against kind above. Keeping the
		// executable kind authoritative prevents persisted/manual routes from
		// silently selecting another fixed model profile.
		requested := in.Route.RequestedModel
		if requested == "" {
			requested = in.Route.Model
		}
		if in.Route.Backend != "" {
			cfg.Model.Backend = in.Route.Backend
		}
		if in.Route.Binary != "" {
			cfg.Model.Binary = in.Route.Binary
		}
		setRoleModel(&cfg, role, requested)
		effort := in.Route.RequestedReasoningEffort
		if effort == "" {
			effort = in.Route.ReasoningEffort
		}
		setRoleEffort(&cfg, role, effort)
	}
	client, err := model.New(model.Options(cfg.Model))
	if err != nil {
		return failed(contract.FailureUnavailable, "model agent unavailable: "+contract.MessageOf(err))
	}
	effects := make([]contract.Effect, 0, len(in.Effects))
	for _, name := range in.Effects {
		effect, parseErr := contract.ParseEffect(name)
		if parseErr != nil {
			return failed(contract.FailureInvalidInput, "invalid agent effects: "+parseErr.Error())
		}
		effects = append(effects, effect)
	}
	operations := make([]contract.Operation, 0, len(in.Operations))
	for _, name := range in.Operations {
		operation, parseErr := contract.ParseOperation(name)
		if parseErr != nil {
			return failed(contract.FailureInvalidInput, "invalid agent operations: "+parseErr.Error())
		}
		operations = append(operations, operation)
	}
	if in.BudgetUSD == nil || *in.BudgetUSD == 0 {
		return failed(contract.FailurePermissionDenied,
			"no positive monetary grant: this model-backed stage cannot run without spending authorization")
	}
	budget := *in.BudgetUSD
	explicitVisibility := in.VisibilityRequired
	invisible, ci := in.Invisible, in.CI
	threadID := in.ThreadID
	if in.Route != nil {
		explicitVisibility = explicitVisibility || in.Route.VisibilityRequired
		invisible = invisible || in.Route.Invisible
		ci = ci || in.Route.CI
		if threadID == "" {
			threadID = in.Route.ThreadID
		}
	}
	if explicitVisibility && (invisible || ci) {
		return failed(contract.FailureInvalidInput,
			"visibility_required cannot be combined with invisible or CI execution")
	}
	codexNative := cfg.Model.NativeCodex()
	visibilityRequired := explicitVisibility || (codexNative && !invisible && !ci)
	answer, err := client.Turn(ctx, model.Request{
		Role: role, Prompt: promptFor(in, string(role)), Schema: schema(string(role)),
		Dir: root, BudgetUSD: budget, MaxTokens: in.Limits.MaxTokens, Effects: effects,
		VisibilityRequired: visibilityRequired,
		Invisible:          invisible,
		CI:                 ci,
		ThreadID:           threadID,
		AssignmentID:       in.ID, WorkflowID: in.WorkflowID, Worktree: in.Worktree,
		PolicyDigest: in.PolicyDigest, GrantToken: in.GrantToken,
		// The portable generic stages have an explicit native surface. Writes
		// use Codex's shell feature under workspace-write; review and audit use
		// the same feature under read-only. No provider default is inherited.
		Builtins: builtinsFor(role), Operations: operations,
	})
	spent := planner.Spent(answer.Spent)
	if err != nil {
		out := report{Verdict: "incomplete", Reason: &reason{Kind: contract.KindOf(err).String(), Text: contract.MessageOf(err)}, Spent: spent, Notices: answer.Notices}
		out.observe(answer)
		return out
	}
	var result struct {
		Role         string   `json:"role"`
		Evidence     []string `json:"evidence"`
		Completeness float64  `json:"completeness"`
		StoppedAt    string   `json:"stopped_at"`
	}
	if err := json.Unmarshal(answer.Structured, &result); err != nil || result.Role != string(role) {
		out := report{Verdict: "failed", Reason: &reason{Kind: "invalid_input", Text: "model agent returned an invalid role result"}, Spent: spent, Notices: answer.Notices}
		out.observe(answer)
		return out
	}
	out := report{Result: map[string]any{"role": result.Role, "evidence": result.Evidence, "completeness": result.Completeness, "stopped_at": result.StoppedAt}, Verdict: "ok", Spent: spent, Completeness: &result.Completeness, StoppedAt: result.StoppedAt, Notices: answer.Notices}
	out.observe(answer)
	return out
}

func (r *report) observe(answer model.Answer) {
	r.ThreadID = answer.ThreadID
	r.TurnID, r.UsageRevision = answer.TurnID, answer.UsageRevision
	r.RequestedModel, r.ObservedModel = answer.RequestedModel, answer.ObservedModel
	r.RequestedReasoningEffort, r.ObservedReasoningEffort = answer.RequestedReasoningEffort, answer.ObservedReasoningEffort
}

func builtinsFor(role model.Role) []string {
	if role == model.RoleImplement {
		return []string{"Shell", "ApplyPatch"}
	}
	return []string{"Read", "Glob", "Grep"}
}

func failed(kind contract.FailureKind, text string) report {
	return report{Verdict: "incomplete", Reason: &reason{Kind: kind.String(), Text: text}}
}

func setRoleModel(cfg *config.Config, role model.Role, name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	switch role {
	case model.RoleImplement:
		cfg.Model.Implement = name
	case model.RoleReview:
		cfg.Model.Review = name
	case model.RoleAudit:
		cfg.Model.Audit = name
	}
}

func setRoleEffort(cfg *config.Config, role model.Role, effort string) {
	switch role {
	case model.RoleImplement:
		cfg.Model.ImplementReasoningEffort = effort
	case model.RoleReview:
		cfg.Model.ReviewReasoningEffort = effort
	case model.RoleAudit:
		cfg.Model.AuditReasoningEffort = effort
	}
}

func repositoryRoot(in assignment) string {
	var level struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal(in.Context["repository"], &level); err != nil {
		return ""
	}
	if level.Root != "" {
		if _, err := os.Stat(level.Root); err == nil {
			return level.Root
		}
	}
	return ""
}

func promptFor(in assignment, role string) string {
	subject, _ := json.Marshal(in.Subject)
	return fmt.Sprintf("You are Atenea's %s stage. Complete the objective using only the assigned repository context and subject evidence. Return structured evidence, an honest completeness fraction from 0 to 1, and where you stopped. Objective: %s\nCriterion: %s\nFiles: %s\nSubject: %s", role, in.Task.Objective, in.Task.Criterion, strings.Join(in.Task.Files, ", "), subject)
}

func schema(role string) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []any{"role", "evidence", "completeness", "stopped_at"}, "properties": map[string]any{
		"role":         map[string]any{"type": "string", "const": role},
		"evidence":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"completeness": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		"stopped_at":   map[string]any{"type": "string"},
	}}
}
