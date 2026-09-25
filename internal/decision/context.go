package decision

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// ResolutionStatus tells callers whether the planner had enough information
// to compile a workflow. NeedsContext plans are deliberately non-executable.
type ResolutionStatus string

const (
	// ResolutionResolved means the planner had enough information to compile a workflow.
	ResolutionResolved ResolutionStatus = "resolved"
	// ResolutionNeedsContext means clarification is required before planning can continue.
	ResolutionNeedsContext ResolutionStatus = "needs_context"
)

const (
	// ResolutionReasonMissingAcceptedPlan identifies a continuation without its accepted-plan reference.
	ResolutionReasonMissingAcceptedPlan = "missing_accepted_plan"
	// ResolutionReasonMissingPlanObjective identifies a continuation without its active objective.
	ResolutionReasonMissingPlanObjective = "missing_plan_objective"
	// ResolutionReasonInvalidContext identifies context that fails version or content validation.
	ResolutionReasonInvalidContext = "invalid_context"
	// ResolutionReasonRepositoryMismatch identifies context bound to another repository.
	ResolutionReasonRepositoryMismatch = "repository_mismatch"
	// ResolutionReasonUnboundContinuation identifies a continuation with no repository binding.
	ResolutionReasonUnboundContinuation = "unbound_continuation"
	// ResolutionReasonPlanFreshnessUnverified identifies a continuation whose caller has not attested that its plan revision is current.
	ResolutionReasonPlanFreshnessUnverified = "plan_freshness_unverified"
	// ResolutionReasonScopeMismatch identifies no overlap between requested and supplied files.
	ResolutionReasonScopeMismatch = "scope_mismatch"
)

// IntentContext carries concise caller-supplied conversation state. It is
// semantic context only: it never creates an effect grant or authorizes a run.
// Version is required so future context shapes can be introduced safely.
type IntentContext struct {
	Version              int      `json:"version"`
	Repository           string   `json:"repository,omitempty"`
	AcceptedPlanID       string   `json:"accepted_plan_id,omitempty"`
	AcceptedPlanRevision string   `json:"accepted_plan_revision,omitempty"`
	AcceptedPlanCurrent  bool     `json:"accepted_plan_current,omitempty"`
	ActiveObjective      string   `json:"active_objective,omitempty"`
	ScopeFiles           []string `json:"scope_files,omitempty"`
	Constraints          []string `json:"constraints,omitempty"`
}

const (
	maxIntentContextTextRunes = 6_000
	maxIntentContextItems     = 100
	maxIntentContextPathRunes = 512
)

type intentResolution struct {
	Status      ResolutionStatus
	ReasonCode  string
	Reason      string
	Intent      Kind
	Text        string
	Repository  string
	ScopeFiles  []string
	ContextUsed bool
}

func resolveIntent(current string, context *IntentContext, requestedRepository string) intentResolution {
	current = strings.TrimSpace(current)
	resolution := intentResolution{Status: ResolutionResolved, Intent: infer(current), Text: current}
	if needsAcceptedPlanContext(current) {
		if context == nil || strings.TrimSpace(context.AcceptedPlanID) == "" || strings.TrimSpace(context.AcceptedPlanRevision) == "" {
			return intentResolution{Status: ResolutionNeedsContext, ReasonCode: ResolutionReasonMissingAcceptedPlan,
				Reason: "the request continues a prior plan, but its accepted plan and revision were not supplied"}
		}
		if strings.TrimSpace(context.ActiveObjective) == "" {
			return intentResolution{Status: ResolutionNeedsContext, ReasonCode: ResolutionReasonMissingPlanObjective,
				Reason: "the request continues a prior plan, but its active objective was not supplied"}
		}
		if strings.TrimSpace(context.Repository) == "" {
			return intentResolution{Status: ResolutionNeedsContext, ReasonCode: ResolutionReasonUnboundContinuation,
				Reason: "the continuation has no repository-bound accepted plan"}
		}
		if !context.AcceptedPlanCurrent {
			return intentResolution{Status: ResolutionNeedsContext, ReasonCode: ResolutionReasonPlanFreshnessUnverified,
				Reason: "the caller has not verified that the accepted plan revision is still current"}
		}
	}
	if context == nil {
		return resolution
	}
	if err := validateIntentContext(context); err != nil {
		return intentResolution{Status: ResolutionNeedsContext, ReasonCode: ResolutionReasonInvalidContext,
			Reason: "the supplied decision context is incomplete or invalid: " + err.Error()}
	}
	repository := strings.TrimSpace(requestedRepository)
	contextRepository := strings.TrimSpace(context.Repository)
	if repository != "" && contextRepository != "" && repository != contextRepository {
		return intentResolution{Status: ResolutionNeedsContext, ReasonCode: ResolutionReasonRepositoryMismatch,
			Reason: "the supplied context points to a different repository than the current request"}
	}
	if repository == "" {
		repository = contextRepository
	}
	if needsAcceptedPlanContext(current) {
		resolution.Intent = KindChange
		resolution.Text = effectiveContextText(context, current)
		resolution.Repository = repository
		resolution.ScopeFiles = slices.Clone(context.ScopeFiles)
		resolution.ContextUsed = true
		return resolution
	}
	if contextHasDetails(context) {
		resolution.Text = effectiveContextText(context, current)
		resolution.Repository = repository
		resolution.ScopeFiles = slices.Clone(context.ScopeFiles)
		resolution.ContextUsed = true
	}
	return resolution
}

func validateIntentContext(context *IntentContext) error {
	if context.Version != 1 {
		return fmt.Errorf("unsupported version %d", context.Version)
	}
	if len(context.ScopeFiles) > maxIntentContextItems || len(context.Constraints) > maxIntentContextItems {
		return fmt.Errorf("context has too many scope or constraint entries")
	}
	textRunes := 0
	for _, value := range []string{context.Repository, context.AcceptedPlanID, context.AcceptedPlanRevision, context.ActiveObjective} {
		textRunes += utf8.RuneCountInString(value)
	}
	for _, file := range context.ScopeFiles {
		textRunes += utf8.RuneCountInString(file)
	}
	for _, value := range context.Constraints {
		textRunes += utf8.RuneCountInString(value)
	}
	if textRunes > maxIntentContextTextRunes {
		return fmt.Errorf("context text exceeds %d characters", maxIntentContextTextRunes)
	}
	if strings.TrimSpace(context.AcceptedPlanID) != "" && strings.TrimSpace(context.AcceptedPlanRevision) == "" {
		return fmt.Errorf("accepted_plan_revision is required with accepted_plan_id")
	}
	if strings.TrimSpace(context.AcceptedPlanRevision) != "" && strings.TrimSpace(context.AcceptedPlanID) == "" {
		return fmt.Errorf("accepted_plan_id is required with accepted_plan_revision")
	}
	if strings.TrimSpace(context.AcceptedPlanID) != "" {
		if !context.AcceptedPlanCurrent {
			return fmt.Errorf("accepted_plan_current must be true after the caller verifies the plan revision")
		}
		if strings.TrimSpace(context.ActiveObjective) == "" {
			return fmt.Errorf("active_objective is required with an accepted plan")
		}
		if strings.TrimSpace(context.Repository) == "" {
			return fmt.Errorf("repository is required with an accepted plan")
		}
	}
	if len(context.ScopeFiles) > 0 && strings.TrimSpace(context.Repository) == "" {
		return fmt.Errorf("repository is required with scope_files")
	}
	for i, file := range context.ScopeFiles {
		file = strings.TrimSpace(file)
		if file == "" || utf8.RuneCountInString(file) > maxIntentContextPathRunes || path.IsAbs(file) ||
			strings.Contains(file, `\`) || strings.ContainsAny(file, "\r\n\t\x00") || (len(file) >= 2 && file[1] == ':') {
			return fmt.Errorf("scope_files[%d] must be a short repository-relative path", i)
		}
		clean := path.Clean(file)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("scope_files[%d] escapes the repository", i)
		}
		if context.ScopeFiles[i] != file || clean != file {
			return fmt.Errorf("scope_files[%d] must use a normalized repository-relative path", i)
		}
	}
	for i, constraint := range context.Constraints {
		if strings.TrimSpace(constraint) == "" {
			return fmt.Errorf("constraints[%d] must not be empty", i)
		}
	}
	return nil
}

func needsAcceptedPlanContext(text string) bool {
	words := stripPolitePrefix(wordsOf(text))
	for _, phrase := range []string{
		"hazlo", "hazla", "hazlos", "hazlas", "hazlo ya", "adelante", "procede", "continua", "continúa", "continúe", //nolint:misspell // Spanish forms are intentional.
		"do it", "do that", "go ahead", "continue", "proceed", "apply it", "implement it", "carry it out",
		"execute the plan", "execute this plan", "run the plan", "run it", "ejecuta el plan", "ejecuta este plan",
	} {
		if words == " "+phrase+" " {
			return true
		}
	}
	return startsWithIntent(words,
		"hazlo", "hazla", "hazlos", "hazlas", "adelante", "procede", "continua", "continúa", "continúe", //nolint:misspell // Spanish forms are intentional.
		"do it", "do that", "fix it", "fix that", "add it", "add that", "implement it", "implement that",
		"change it", "change that", "modify it", "modify that", "update it", "update that", "edit it", "edit that",
		"build it", "build that", "apply it", "apply that", "proceed with it", "continue with it", "execute the plan", "execute this plan",
		"run the plan", "run it", "ejecuta el plan", "ejecuta este plan",
	)
}

func contextHasDetails(context *IntentContext) bool {
	return context != nil && (strings.TrimSpace(context.Repository) != "" || strings.TrimSpace(context.AcceptedPlanID) != "" || strings.TrimSpace(context.ActiveObjective) != "" ||
		len(context.ScopeFiles) > 0 || len(context.Constraints) > 0)
}

func effectiveContextText(context *IntentContext, current string) string {
	var parts []string
	if objective := strings.TrimSpace(context.ActiveObjective); objective != "" {
		parts = append(parts, "Active objective from the supplied plan context:\n"+objective)
	}
	if id := strings.TrimSpace(context.AcceptedPlanID); id != "" {
		parts = append(parts, "Plan reference: "+id+"@"+strings.TrimSpace(context.AcceptedPlanRevision))
		parts = append(parts, "Caller attests that this accepted plan revision is current; ATENEA cannot verify it independently.")
	}
	if len(context.ScopeFiles) > 0 {
		parts = append(parts, "Plan file focus (context only; this does not grant effects):\n- "+strings.Join(context.ScopeFiles, "\n- "))
	}
	if len(context.Constraints) > 0 {
		parts = append(parts, "Active constraints from the supplied context:\n- "+strings.Join(context.Constraints, "\n- "))
	}
	parts = append(parts, "Current user request:\n"+strings.TrimSpace(current))
	return strings.Join(parts, "\n\n")
}

func intersectFiles(requested, scoped []string) []string {
	allowed := make(map[string]struct{}, len(scoped))
	for _, file := range scoped {
		allowed[path.Clean(strings.TrimSpace(file))] = struct{}{}
	}
	seen := make(map[string]struct{}, len(requested))
	out := make([]string, 0, len(requested))
	for _, file := range requested {
		clean := path.Clean(strings.TrimSpace(file))
		if _, ok := allowed[clean]; !ok {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}
