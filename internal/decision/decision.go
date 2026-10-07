// Package decision turns a natural-language commission into an explainable
// execution plan. It deliberately sits above the capability selector and the
// workflow engine: it chooses the shape of the work, while those packages
// continue to own provider selection and graph validation.
package decision

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Kind is the coarse intent used to choose an initial workflow shape. It is
// intentionally small and deterministic; a future model classifier can return
// the same vocabulary without changing the compiler below it.
type Kind string

// Intent kinds supported by the deterministic decision compiler.
const (
	// KindUnderstand is part of ATENEA's public orchestration contract.
	KindUnderstand Kind = "understand"
	// KindSearch is part of ATENEA's public orchestration contract.
	KindSearch Kind = "search"
	// KindPlan is part of ATENEA's public orchestration contract.
	KindPlan Kind = "plan"
	// KindChange is part of ATENEA's public orchestration contract.
	KindChange Kind = "change"
)

// Request is the input to the decision layer.
type Request struct {
	Text            string
	Context         *IntentContext
	AcceptedPlan    *AcceptedPlanReference
	Criterion       string
	Limits          contract.Limits
	Repository      string
	Files           []string
	BudgetUSD       float64
	Effects         []contract.Effect
	StandingEffects []contract.Effect
	Prefer          string
	Tool            string
}

// IntentClassifier proposes one of ATENEA's closed set of planning intents.
// Its result can affect the workflow shape, but never grants effects or
// authorizes execution.
type IntentClassifier interface {
	Classify(context.Context, string) (IntentClassification, error)
}

// IntentClassification is the typed result returned by a model-backed
// classifier. Confidence is Laya's calibrated answer_confidence value.
type IntentClassification struct {
	Intent        Kind    `json:"intent"`
	Confidence    float64 `json:"confidence"`
	Model         string  `json:"model,omitempty"`
	RoutingModel  string  `json:"routing_model,omitempty"`
	RoutingReason string  `json:"routing_reason,omitempty"`
}

// IntentEvidence records the classifier mode, selected source, model proposal,
// and any deterministic fallback used to build this plan.
type IntentEvidence struct {
	Mode           string                `json:"mode"`
	Source         string                `json:"source"`
	Laya           *IntentClassification `json:"laya,omitempty"`
	FallbackReason string                `json:"fallback_reason,omitempty"`
}

// ModelChoice describes the model role selected for one agent type.
type ModelChoice struct {
	Role            string   `json:"role"`
	Backend         string   `json:"backend"`
	Binary          string   `json:"binary"`
	Name            string   `json:"name"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	Fallbacks       []string `json:"fallbacks,omitempty"`
	Available       bool     `json:"available"`
	Reason          string   `json:"reason"`
}

// ToolChoice describes a tool surface offered to a planned agent. Native
// capabilities and raw MCP tools are kept visibly separate: raw tools have no
// semantic capability contract and must never be mistaken for one.
type ToolChoice struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	Source     string            `json:"source"`
	Capability string            `json:"capability,omitempty"`
	Effects    []contract.Effect `json:"effects,omitempty"`
	Selected   bool              `json:"selected"`
	Reason     string            `json:"reason"`
}

// CapabilityChoice records the capability intent and the providers that could
// answer it. Chosen is filled when the caller supplies a live selector.
type CapabilityChoice struct {
	ID          string   `json:"id"`
	Repository  string   `json:"repository"`
	Providers   []string `json:"providers"`
	Chosen      string   `json:"chosen,omitempty"`
	Reason      string   `json:"reason"`
	Unavailable bool     `json:"unavailable"`
}

// Reason explains one decision or warning in the plan.
type Reason struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

// Plan is the complete dry-run result. Workflow is compiled before it is
// returned, so a caller can trust that its agent names, edges, permissions and
// budget are structurally valid.
type Plan struct {
	Text             string             `json:"text"`
	Resolution       ResolutionStatus   `json:"resolution"`
	ResolutionReason string             `json:"resolution_reason,omitempty"`
	ContextUsed      bool               `json:"context_used,omitempty"`
	Criterion        string             `json:"criterion"`
	Limits           contract.Limits    `json:"limits"`
	Coordinator      string             `json:"coordinator"`
	Specialists      []string           `json:"specialists"`
	Intent           Kind               `json:"intent"`
	IntentEvidence   IntentEvidence     `json:"intent_evidence"`
	Repositories     []string           `json:"repositories"`
	Effects          []contract.Effect  `json:"effects"`
	Agent            string             `json:"agent"`
	Models           []ModelChoice      `json:"models"`
	Tools            []ToolChoice       `json:"tools"`
	Capabilities     []CapabilityChoice `json:"capabilities"`
	Budget           BudgetSummary      `json:"budget"`
	Workflow         workflow.Graph     `json:"workflow"`
	Valid            bool               `json:"valid"`
	Reasons          []Reason           `json:"reasons"`
}

// BudgetSummary is the preflight accounting for the compiled workflow.
type BudgetSummary struct {
	GrantedUSD  float64 `json:"granted_usd"`
	RequiredUSD float64 `json:"required_usd"`
	MinimumUSD  float64 `json:"minimum_usd"`
	MarginUSD   float64 `json:"margin_usd"`
	Sufficient  bool    `json:"sufficient"`
}

// Selector is the subset of the core used by the decision layer. Keeping it
// narrow makes the planner cheap to test and ensures the production path uses
// the existing funnel rather than a second provider-ranking algorithm.
type Selector interface {
	SelectWithPreference(capabilityID, repositoryID, prefer string) (selector.Decision, error)
}

// Planner builds a plan from effective settings and, optionally, a live core
// selector. Without a selector it still produces a useful static catalog
// plan, which is what makes `--dry-run` safe on an offline machine.
type Planner struct {
	Config        config.Config
	Selector      Selector
	Estimator     BudgetEstimator
	Ranker        ModelRanker
	Classifier    IntentClassifier
	AcceptedPlans *AcceptedPlanStore
}

// Build creates and validates one decision plan.
func (p Planner) Build(req Request) (Plan, error) {
	return p.BuildContext(context.Background(), req)
}

// BuildContext creates a decision plan while bounding model-backed intent
// classification to the lifetime of the caller's request.
func (p Planner) BuildContext(ctx context.Context, req Request) (Plan, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return Plan{}, contract.Fail(contract.FailureInvalidInput, "decision: text is required")
	}
	if req.BudgetUSD < 0 {
		return Plan{}, contract.Fail(contract.FailureInvalidInput,
			"decision: budget must not be negative, got %v", req.BudgetUSD)
	}
	if req.BudgetUSD == 0 {
		req.BudgetUSD = p.Config.Orchestrator.BudgetUSD
	}
	if req.Limits.MaxDuration < 0 || req.Limits.MaxTokens < 0 {
		return Plan{}, contract.Fail(contract.FailureInvalidInput, "decision: limits cannot be negative")
	}
	if req.Limits.MaxTokens > 0 && req.Limits.MaxDuration <= 0 {
		return Plan{}, contract.Fail(contract.FailureInvalidInput, "decision: max tokens requires a positive max duration")
	}

	if req.AcceptedPlan != nil {
		var root string
		for _, repo := range p.Config.Repositories {
			if repo.ID == req.Repository {
				root = repo.Path
				break
			}
		}
		store := AcceptedPlanStore{}
		if p.AcceptedPlans != nil {
			store = *p.AcceptedPlans
		}
		var err error
		if req.Context != nil || root == "" {
			err = fmt.Errorf("accepted_plan requires an explicit declared repository and cannot be combined with context")
		} else {
			req.Context, err = store.Resolve(ctx, *req.AcceptedPlan, req.Repository, root)
		}
		if err != nil {
			// Missing or stale context is a successful abstention, not a planner failure.
			return Plan{Text: text, Resolution: ResolutionNeedsContext, ResolutionReason: "unverified_accepted_plan", //nolint:nilerr // Report the error as non-executable needs_context, like inline context resolution.
				Workflow: workflow.Graph{Task: text}, Reasons: []Reason{{Stage: "context", Message: err.Error()}}}, nil
		}
	}
	resolution := resolveIntent(req.Text, req.Context, req.Repository)
	effectiveText, contextFiles := resolution.Text, resolution.ScopeFiles
	if resolution.Status == ResolutionNeedsContext {
		return Plan{
			Text:             text,
			Resolution:       ResolutionNeedsContext,
			ResolutionReason: resolution.ReasonCode,
			Intent:           "",
			Workflow:         workflow.Graph{Task: text},
			Reasons:          []Reason{{Stage: "intent", Message: resolution.Reason}},
		}, nil
	}
	if resolution.Repository != "" {
		req.Repository = resolution.Repository
	}
	req.Text = effectiveText
	if len(contextFiles) > 0 {
		if len(req.Files) == 0 {
			req.Files = contextFiles
		} else {
			req.Files = intersectFiles(req.Files, contextFiles)
			if len(req.Files) == 0 {
				return Plan{
					Text:             text,
					Resolution:       ResolutionNeedsContext,
					ResolutionReason: ResolutionReasonScopeMismatch,
					Workflow:         workflow.Graph{Task: text},
					Reasons:          []Reason{{Stage: "scope", Message: "the requested files do not overlap the accepted plan scope"}},
				}, nil
			}
		}
	}

	repos, err := p.repositories(req.Repository)
	if err != nil {
		return Plan{}, err
	}
	var intent Kind
	var intentEvidence IntentEvidence
	if needsAcceptedPlanContext(text) {
		mode := strings.ToLower(strings.TrimSpace(p.Config.Decision.Mode))
		if mode == "" {
			mode = "rules"
		}
		// The accepted-plan continuation resolves the current action already;
		// a classifier must not reinterpret "do it" without that authority.
		intent = resolution.Intent
		intentEvidence = IntentEvidence{Mode: mode, Source: "context"}
	} else {
		// Deterministic rules describe the user's current request. Laya may also
		// consider the separately supplied context included in req.Text.
		intent, intentEvidence = p.classifyIntent(ctx, text, req.Text)
	}
	agent := p.agentFor(intent, req.Files)
	criterion := strings.TrimSpace(req.Criterion)
	if criterion == "" {
		criterion = "all requested repositories have an evidence-backed answer and every claimed change is verified"
	}
	plan := Plan{
		Text:           text,
		Resolution:     ResolutionResolved,
		ContextUsed:    resolution.ContextUsed,
		Criterion:      criterion,
		Limits:         req.Limits,
		Coordinator:    "atenea-coordinator",
		Specialists:    specialistRoles(agent, intent, p.Config),
		Intent:         intent,
		IntentEvidence: intentEvidence,
		Repositories:   repos,
		Effects:        mergeEffects(req.StandingEffects, req.Effects),
		Agent:          agent,
		Reasons: []Reason{
			{Stage: "intent", Message: intentReason(intent, intentEvidence)},
			{Stage: "policy", Message: "user constraints and declared effects are applied before provider choice"},
		},
	}
	if intentEvidence.Laya != nil {
		observation := intentEvidence.Laya
		model := observation.Model
		if model == "" {
			model = "unknown model"
		}
		message := fmt.Sprintf("Laya proposed %s with answer confidence %.2f using %s", observation.Intent, observation.Confidence, model)
		if observation.RoutingModel != "" {
			message += " (routed to " + observation.RoutingModel + ")"
		}
		if intentEvidence.Source == "context" {
			message += "; the accepted-plan context remained selected"
		} else if intentEvidence.Source != "laya" {
			message += "; the deterministic intent remained selected"
		}
		plan.Reasons = append(plan.Reasons, Reason{Stage: "intent", Message: message})
	}
	if intentEvidence.FallbackReason != "" {
		plan.Reasons = append(plan.Reasons, Reason{Stage: "intent", Message: "Laya was not selected: " + intentEvidence.FallbackReason})
	}
	plan.Models = p.modelsFor(agent, intent, firstRepository(repos), slices.Contains(plan.Effects, contract.EffectWrite))
	plan.Tools = p.toolsFor(agent, intent, req.Tool)
	if req.Tool != "" && !selectedToolExists(plan.Tools, req.Tool) {
		plan.Reasons = append(plan.Reasons, Reason{Stage: "tool", Message: "requested tool is not declared or allow-listed: " + req.Tool})
	}
	modelsReady := true
	toolsReady := req.Tool == "" || selectedToolExists(plan.Tools, req.Tool)
	coordinatorReady := hasAgent(p.Config, "atenea-coordinator")
	if !coordinatorReady {
		plan.Reasons = append(plan.Reasons, Reason{Stage: "workflow", Message: "atenea-coordinator agent type is required"})
	}
	changeChainReady := true
	if intent == KindChange && slices.Contains(plan.Effects, contract.EffectWrite) {
		changeChainReady = hasAgent(p.Config, "implement") && hasAgent(p.Config, "review") && hasAgent(p.Config, "audit")
		if !changeChainReady {
			plan.Reasons = append(plan.Reasons, Reason{Stage: "workflow", Message: "authorized changes require declared implement, review and audit agent types"})
		}
	}
	for _, model := range plan.Models {
		if model.Available {
			continue
		}
		modelsReady = false
		plan.Reasons = append(plan.Reasons, Reason{Stage: "model", Message: model.Role + ": " + model.Reason})
	}

	capabilities := p.capabilitiesFor(intent)
	for _, repo := range repos {
		for _, id := range capabilities {
			choice := p.capabilityChoice(id, repo, req.Prefer)
			plan.Capabilities = append(plan.Capabilities, choice)
		}
	}
	plan.Workflow = p.workflowFor(req, intent, agent, repos)
	plan.Budget = budgetSummary(plan.Workflow)
	if !plan.Budget.Sufficient {
		plan.Reasons = append(plan.Reasons, Reason{Stage: "budget", Message: fmt.Sprintf(
			"commission grants $%.2f but the routed workflow requires about $%.2f (minimum $%.2f); increase --budget to at least $%.2f",
			plan.Budget.GrantedUSD, plan.Budget.RequiredUSD, plan.Budget.MinimumUSD, plan.Budget.RequiredUSD)})
	}
	p.stampRoutes(&plan, agent, intent)
	compiled, compileErr := workflow.Compile(plan.Workflow, p.Config.Agents)
	if compileErr != nil {
		plan.Valid = false
		plan.Reasons = append(plan.Reasons, Reason{Stage: "workflow", Message: compileErr.Error()})
		// A compile failure is part of the dry-run result, not a planner
		// transport failure; callers receive the invalid plan and its reasons.
		return plan, nil //nolint:nilerr // invalid plans are reported in-band
	}
	plan.Valid = modelsReady && toolsReady && coordinatorReady && changeChainReady && plan.Budget.Sufficient
	plan.Reasons = append(plan.Reasons, Reason{Stage: "workflow",
		Message: fmt.Sprintf("compiled %d step(s) into %d wave(s)", len(compiled.Graph.Steps), waveCount(compiled.Graph))})
	return plan, nil
}

func (p Planner) classifyIntent(ctx context.Context, requestText, classifierText string) (Kind, IntentEvidence) {
	rulesIntent := infer(requestText)
	mode := strings.ToLower(strings.TrimSpace(p.Config.Decision.Mode))
	if mode == "" {
		mode = "rules"
	}
	evidence := IntentEvidence{Mode: mode, Source: "rules"}
	if mode == "rules" {
		return rulesIntent, evidence
	}
	if mode != "observe" && mode != "laya" {
		evidence.FallbackReason = "unsupported classifier mode"
		return rulesIntent, evidence
	}

	classifier := p.Classifier
	if classifier == nil && p.Config.Decision.LayaEndpoint != "" {
		classifier = NewLayaClassifier(p.Config.Decision)
	}
	if classifier == nil {
		evidence.FallbackReason = "Laya endpoint is not configured"
		return rulesIntent, evidence
	}
	classification, err := classifier.Classify(ctx, classifierText)
	if err != nil {
		evidence.FallbackReason = "service request failed"
		return rulesIntent, evidence
	}
	if !validIntentClassification(classification) {
		evidence.FallbackReason = "response did not contain a supported intent and confidence"
		return rulesIntent, evidence
	}
	evidence.Laya = &classification
	if mode == "observe" {
		return rulesIntent, evidence
	}
	minimum := p.Config.Decision.MinimumConfidence
	if minimum <= 0 || minimum > 1 || math.IsNaN(minimum) {
		minimum = 0.8
	}
	if classification.Confidence < minimum {
		evidence.FallbackReason = fmt.Sprintf("answer confidence %.2f is below the configured minimum %.2f", classification.Confidence, minimum)
		return rulesIntent, evidence
	}
	evidence.Source = "laya"
	return classification.Intent, evidence
}

func validIntentClassification(classification IntentClassification) bool {
	switch classification.Intent {
	case KindUnderstand, KindSearch, KindPlan, KindChange:
	default:
		return false
	}
	return !math.IsNaN(classification.Confidence) && !math.IsInf(classification.Confidence, 0) &&
		classification.Confidence >= 0 && classification.Confidence <= 1
}

func intentReason(intent Kind, evidence IntentEvidence) string {
	switch evidence.Source {
	case "laya":
		return fmt.Sprintf("classified as %s by Laya at answer confidence %.2f", intent, evidence.Laya.Confidence)
	case "context":
		return fmt.Sprintf("resolved as %s from the accepted-plan continuation context", intent)
	default:
		return fmt.Sprintf("classified as %s from the request text", intent)
	}
}

func (p Planner) stampRoutes(plan *Plan, agent string, kind Kind) {
	for i := range plan.Workflow.Steps {
		step := &plan.Workflow.Steps[i]
		role := modelRole(step.TypeName)
		if role == "" {
			role = modelRole(agent)
		}
		route := contract.Route{}
		if role != "" {
			model := p.modelChoice(role, repositoryFromStep(step.ID, plan.Repositories))
			route.Role = model.Role
			route.Model, route.RequestedModel = model.Name, model.Name
			route.Fallbacks, route.Backend, route.Binary = slices.Clone(model.Fallbacks), model.Backend, model.Binary
			route.ReasoningEffort, route.RequestedReasoningEffort = model.ReasoningEffort, model.ReasoningEffort
		}
		if step.TypeName == "explore" {
			route.Capabilities = p.capabilitiesFor(kind)
			for _, choice := range plan.Capabilities {
				if choice.Repository == repositoryFromStep(step.ID, plan.Repositories) && choice.Chosen != "" {
					if route.Providers == nil {
						route.Providers = make(map[string]string)
					}
					route.Providers[choice.ID] = choice.Chosen
				}
			}
		}
		if step.TypeName != "plan" && step.TypeName != "review" && step.TypeName != "audit" {
			for _, tool := range plan.Tools {
				if tool.Selected {
					route.Tools = append(route.Tools, tool.ID)
				}
			}
		}
		if strings.EqualFold(route.Backend, "codex") && p.Config.Model.NativeCodex() {
			route.VisibilityRequired = true
			// A visible Codex turn must stay on the native App Server. A
			// fallback would silently change the requested transport/model.
			route.Fallbacks = nil
		}
		step.Route = &route
	}
}

func repositoryFromStep(id string, repositories []string) string {
	if id == "coordinate" {
		return firstRepository(repositories)
	}
	for _, repo := range repositories {
		if id == "explore-"+repo || id == "plan-"+repo || id == "implement-"+repo || id == "review-"+repo || id == "audit-"+repo {
			return repo
		}
	}
	return ""
}

func (p Planner) repositories(id string) ([]string, error) {
	if strings.TrimSpace(id) != "" {
		for _, repo := range p.Config.Repositories {
			if repo.ID == id {
				return []string{id}, nil
			}
		}
		return nil, contract.Fail(contract.FailureNotFound, "decision: repository %q is not declared", id)
	}
	if len(p.Config.Repositories) == 0 {
		return nil, contract.Fail(contract.FailureNotFound, "decision: no repositories are declared")
	}
	out := make([]string, 0, len(p.Config.Repositories))
	for _, repo := range p.Config.Repositories {
		out = append(out, repo.ID)
	}
	sort.Strings(out)
	return out, nil
}

// The vocabulary each intent is recognized by, as whole words rather than as
// substrings.
//
// These lists were matched with strings.Contains, and with three-letter words
// in them that was wrong on ordinary technical English: "prefix" contains
// "fix", "address" contains "add", and "explanation" contains "plan". Since
// KindChange is tested first, "where is the prefix handler" -- a search --
// came out as a change, which is the classification that decides whether the
// plan asks for write effects and whether the CLI stops to ask for --confirm.
//
// The inflections are spelled out instead of stemmed. A stemmer for two
// languages is a dependency and a source of its own surprises, where this is
// a dozen words somebody types at a prompt, and a form that is missing
// degrades to KindUnderstand -- the conservative end, which reads and does
// not change anything.
var (
	planPointReference = regexp.MustCompile(`(?i)\bp[0-9]+\b`)
	changeWords        = []string{
		"implementar", "implementa", "implement", "implements", "implementing",
		"cambiar", "cambia", "change", "changes", "changing",
		"fix", "fixes", "fixed", "fixing",
		"refactorizar", "refactoriza", "refactor", "refactors", "refactoring",
		"construir", "construye", "build", "builds", "building",
		"añadir", "añade", "add", "adds", "adding",
		"migrar", "migra", "migrate", "migrates", "migrating",
		"corregir", "corrige", "arreglar", "arregla", "editar", "edita",
		"modificar", "modifica", "actualizar", "actualiza",
	}
	planWords = []string{
		"planificar", "planifica", "plan", "plans", "planning",
		"cómo harías", "how would",
		"diseñar", "diseña", "design", "designs",
	}
	searchWords = []string{
		"buscar", "busca", "find", "finds", "finding",
		"search", "searches", "searching",
		"dónde", "donde", "where", "localizar", "localiza", "locate", "locates",
	}
)

func infer(text string) Kind {
	cleaned := strings.ToLower(stripQuotedText(text))
	cleaned = removeNegatedChangePhrases(cleaned)
	words := wordsOf(cleaned)
	if hasFutureChangeDiscussion(words) {
		return KindPlan
	}
	if containsAny(words, "not only", "not just") && containsAny(words, changeWords...) {
		return KindChange
	}
	if hasConditionalChangeCommand(cleaned) {
		return KindChange
	}
	if hasExplicitFollowupChange(cleaned) {
		return KindChange
	}
	if isNecessityQuestion(cleaned) {
		return KindPlan
	}
	if isStatusQuestion(cleaned) {
		return KindSearch
	}
	if isPlanPointAmendment(cleaned, words) {
		if hasChangeAfterPlanPoint(words) {
			return KindChange
		}
		return KindPlan
	}
	if hasChangeAfterTransition(words) {
		return KindChange
	}
	if startsWithIntent(words, "understand", "explain", "summarize", "summarise", "describe", "tell me what", "dime qué", "explica", "resume", "resúmeme", "describe") { //nolint:misspell // British spelling is intentional.
		return KindUnderstand
	}
	if startsWithIntent(words, "please", "can you", "could you", "would you", "por favor", "vale", "puedes", "podrías", "podrias", "solo", "only", "just") {
		return infer(strings.TrimSpace(stripPolitePrefix(words)))
	}
	// Questions about a course of action are planning, including accentless
	// Spanish input. Keep this before the change vocabulary.
	if startsWithIntent(words, "cómo podemos", "como podemos", "cómo lo hacemos", "como lo hacemos", "cómo hacemos", "como hacemos", "qué hacemos", "que hacemos", "ahora qué hacemos", "ahora que hacemos", "qué hay que hacer", "que hay que hacer", "ahora qué hay que hacer", "ahora que hay que hacer") {
		return KindPlan
	}
	// Status verification retrieves evidence; the mention of a PR or a change
	// does not ask for a modification. Avoid treating all reviews as searches.
	if startsWithIntent(words, "revisa si", "revisa que", "comprueba si", "comprueba que", "verifica si", "verifica que", "consulta el estado", "consulta las", "consulta los") {
		for _, verb := range []string{"corrige", "arregla", "implementa", "aplica", "cambia", "modifica", "actualiza", "edita", "añade"} {
			// "si no cambia" can be an indicative status question. A
			// conditional command needs an explicit clause boundary.
			normalized := strings.Join(strings.Fields(cleaned), " ") + " "
			conditional := false
			for _, boundary := range []string{"; si no, ", ". si no, ", ", si no, "} {
				conditional = conditional || strings.Contains(normalized, boundary+verb+" ")
			}
			if conditional {
				return KindChange
			}
		}
		return KindSearch
	}
	if containsAny(words, "how would", "how could", "how can we", "how do i", "how to", "design how", "diseña cómo", "cómo arreglar", "cómo corregir", "cómo modificar", "como arreglar", "como corregir", "como modificar", "tell me how you would", "outline how") ||
		startsWithIntent(words, "tell me how to") {
		return KindPlan
	}
	if startsWithIntent(words, "plan", "planifica", "planificar", "diseña", "diseñar", "design", "outline", "propose", "propón", "proponer", "prepare a plan", "preparar un plan") {
		return KindPlan
	}
	if startsWithIntent(words, "find", "search", "locate", "where", "buscar", "busca", "localiza", "dónde", "donde", "look up", "investiga") {
		return KindSearch
	}
	if containsAny(words, planWords...) {
		return KindPlan
	}
	if containsAny(words, searchWords...) {
		return KindSearch
	}
	if containsAny(words, changeWords...) {
		return KindChange
	}
	return KindUnderstand
}

// finalQuestionClause ignores earlier questions and sentences so "Is it done?
// Fix it" is classified from its final command, while "And now? Have you
// finished?" remains a status request. A final question mark is optional.
func finalQuestionClause(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimRight(text, " \t\r\n!¡.")
	for {
		i := strings.LastIndexAny(text, "?.;:\n")
		if i < 0 {
			break
		}
		tail := strings.TrimSpace(text[i+1:])
		if tail == "" {
			text = text[:i]
			continue
		}
		courtesy := strings.TrimSpace(wordsOf(tail))
		if courtesy == "" || courtesy == "gracias" || courtesy == "muchas gracias" || courtesy == "por favor" ||
			courtesy == "please" || courtesy == "thanks" || courtesy == "thank you" {
			text = text[:i]
			continue
		}
		text = tail
		break
	}
	return stripPolitePrefix(wordsOf(text))
}

func isNecessityQuestion(text string) bool {
	if !strings.Contains(text, "?") {
		return false
	}
	question := finalQuestionClause(text)
	return startsWithIntent(question, "hay que", "no hay que")
}

// A completed-work question may mention a change without requesting one.
func isStatusQuestion(text string) bool {
	question := finalQuestionClause(text)
	if isStatusLead(question) {
		return true
	}
	if i := strings.LastIndex(text, "?"); i >= 0 {
		previous := text[:i]
		if boundary := strings.LastIndexAny(previous, "?.;:\n"); boundary >= 0 {
			previous = previous[boundary+1:]
		}
		if !isStatusLead(stripPolitePrefix(wordsOf(previous))) {
			return false
		}
		following := stripPolitePrefix(wordsOf(text[i+1:]))
		return !startsWithIntent(following,
			"corrige", "corrígelo", "arregla", "arréglalo", "implementa", "impleméntalo",
			"aplica", "aplícalo", "cambia", "modifica", "actualiza", "edita", "añade",
			"fix", "implement", "apply", "change", "modify", "update", "edit", "add")
	}
	return false
}

func isStatusLead(question string) bool {
	if startsWithIntent(question, "hay que", "no hay que") {
		return false
	}
	if startsWithIntent(question, "are there any", "is there any", "no hay", "hay", "queda", "quedan") {
		return containsAny(question,
			"comentario", "comentarios", "incidencia", "incidencias", "pendiente", "pendientes",
			"tarea", "tareas", "error", "errores", "fallo", "fallos", "cambio", "cambios",
			"que arreglar", "para arreglar", "que corregir", "para corregir",
			"bug", "bugs", "issue", "issues", "error", "errors", "change", "changes", "task", "tasks",
			"fix", "fixes", "work")
	}
	return startsWithIntent(question,
		"has terminado de", "ya has terminado de", "han terminado de", "se ha terminado de",
		"habéis terminado de", "habeis terminado de", "have you finished", "are you done",
		"has it been fixed", "is it fixed")
}

func hasExplicitFollowupChange(text string) bool {
	for _, boundary := range []string{",", " y ", " and "} {
		if i := strings.Index(text, boundary); i >= 0 {
			if !isStatusQuestion(text[:i]) {
				continue
			}
			if strings.Contains(text[i+len(boundary):], "?") {
				continue // Within a question, a present-tense verb may be descriptive.
			}
			following := wordsOf(text[i+len(boundary):])
			if startsWithIntent(following,
				"corrige", "corrígelo", "arregla", "arréglalo", "implementa", "impleméntalo",
				"aplica", "aplícalo", "cambia", "modifica", "actualiza", "edita", "añade",
				"fix", "implement", "apply", "change", "modify", "update", "edit", "add") {
				return true
			}
		}
	}
	return false
}

func hasConditionalChangeCommand(text string) bool {
	const marker = "si no,"
	if i := strings.Index(text, marker); i >= 0 {
		command := wordsOf(strings.TrimSpace(text[i+len(marker):]))
		return startsWithIntent(command,
			"corrige", "corrígelo", "arregla", "arréglalo", "implementa", "aplica",
			"cambia", "modifica", "actualiza", "edita", "añade")
	}
	return false
}

// Numbered P-sections are plan entries. The P-number reference avoids
// interpreting an ordinary request to add a product phase as plan editing.
func isPlanPointAmendment(text, words string) bool {
	refs := planPointReference.FindAllStringIndex(text, -1)
	if len(refs) == 0 {
		return false
	}
	if !containsAny(words, "plan", "planes") && containsAny(words,
		"al producto", "al sistema", "al código", "en el código", "al repositorio", //nolint:misspell // Spanish word for product.
		"to the product", "to the system", "to the code", "in the code", "to the codebase", "to the repository") {
		return false
	}
	if len(refs) < 2 && !containsAny(words, "un p", "el p", "al plan", "del plan") {
		if !containsAny(words, "a p", "the p", "to the plan") {
			return false
		}
	}
	return containsAny(words,
		"añadir un p", "añade un p", "agregar un p", "agrega un p", "insertar un p", "inserta un p",
		"añadir el p", "añade el p", "agregar el p", "agrega el p", "insertar el p", "inserta el p",
		"añadir p", "añade p", "agregar p", "agrega p", "insertar p", "inserta p",
		"add p", "insert p", "add a p", "insert a p", "add the p", "insert the p")
}

func hasChangeAfterPlanPoint(words string) bool {
	for _, command := range []string{
		"y corrige", "y arregla", "y implementa", "y aplica", "y cambia", "y modifica",
		"y actualiza", "y edita", "y añade", "y corrígelo", "y arréglalo", "y impleméntalo",
		"y aplícalo", "y hazlo", "y ejecútalo",
		"and fix", "and implement", "and apply", "and change", "and modify",
		"and update", "and edit", "and add", "and do it", "and execute it",
	} {
		if i := strings.Index(words, " "+command+" "); i >= 0 {
			following := wordsOf(words[i+len(command)+2:])
			if isPlanPointTextEdit(command, following) {
				continue
			}
			if !containsAny(following, "plan", "planes", "p", "punto", "puntos", "fase", "fases", "point", "points", "phase", "phases") {
				return true
			}
		}
	}
	return false
}

func isPlanPointTextEdit(command, following string) bool {
	if command != "and update" && command != "and edit" && command != "y actualiza" && command != "y edita" {
		return false
	}
	if containsAny(following, "code", "codebase", "código", "system", "sistema", "product", "producto") { //nolint:misspell // Spanish word for product.
		return false
	}
	return startsWithIntent(following,
		"it", "its wording", "its text", "its title", "its description", "the wording", "the text", "the title", "the description",
		"lo", "su redacción", "su texto", "su título", "su descripción", "la redacción", "el texto", "el título", "la descripción")
}

func startsWithIntent(words string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if words == " "+prefix+" " || strings.HasPrefix(words, " "+prefix+" ") {
			return true
		}
	}
	return false
}

func hasChangeAfterTransition(words string) bool {
	for _, transition := range []string{" then ", " and then ", " after that ", " followed by ", " luego ", " después ", " despues ", " a continuación "} {
		if i := strings.Index(words, transition); i >= 0 {
			// Classify the requested second phase rather than searching for a
			// change verb anywhere in it. For example, "explain how to fix"
			// asks for guidance, while "fix it" asks for a change.
			if infer(strings.TrimSpace(words[i+len(transition):])) == KindChange {
				return true
			}
		}
	}
	return false
}

// Future or modal discussion mentions a change without asking ATENEA to perform it now.
func hasFutureChangeDiscussion(words string) bool {
	if !containsAny(words, changeWords...) && !containsAny(words,
		"corrígelo", "arréglalo", "impleméntalo", "aplícalo", "hazlo", "ejecútalo") {
		return false
	}
	if startsWithIntent(words, "should i", "should we", "could we", "would we", "debería", "deberias", "deberías", "deberíamos", "deberiamos") {
		return true
	}
	if !containsAny(words,
		"next week", "next month", "tomorrow", "later", "later on", "eventually", "in the future", "down the road", "someday",
		"we will", "we ll", "we should", "we might", "we may", "we could", "i will", "i ll", "they will", "you will", "going to",
		"mañana", "más adelante", "en el futuro", "a futuro", "la semana que viene", "el mes que viene", "habría que",
	) {
		return false
	}
	return !containsAny(words, "now", "right now", "today", "immediately", "ahora", "ahora mismo", "hoy", "ya", "inmediatamente")
}

func stripPolitePrefix(words string) string {
	for {
		stripped := false
		for _, prefix := range []string{"please", "can you", "could you", "would you", "por favor", "vale", "puedes", "podrías", "podrias", "solo", "only", "just"} {
			if strings.HasPrefix(words, " "+prefix+" ") {
				words = " " + strings.TrimSpace(strings.TrimPrefix(words, " "+prefix+" ")) + " "
				stripped = true
				break
			}
		}
		if !stripped {
			return words
		}
	}
}

func removeNegatedChangePhrases(text string) string {
	for _, phrase := range []string{
		"do not make changes", "don't make changes", "don’t make changes", "do not change", "don't change", "don’t change",
		"do not implement", "don't implement", "don’t implement", "do not edit", "don't edit", "don’t edit",
		"do not fix", "don't fix", "don’t fix", "do not migrate", "don't migrate", "don’t migrate",
		"do not modify", "don't modify", "don’t modify", "do not refactor", "don't refactor", "don’t refactor",
		"do not apply", "don't apply", "don’t apply", "no hagas cambios", "no cambies", "no cambie", "no implementar",
		"no implementes", "no edites", "no editar", "no modifiques", "no modificar", "no apliques", "no aplicar", //nolint:misspell // Spanish conjugations are intentional.
		"no corrijas", "no arregles", "no actualices",
	} {
		text = strings.ReplaceAll(text, phrase, strings.Repeat(" ", utf8.RuneCountInString(phrase)))
	}
	return text
}

func stripQuotedText(text string) string {
	var out strings.Builder
	var quote rune
	runes := []rune(text)
	for i, r := range runes {
		if quote != 0 {
			if r == quote {
				quote = 0
				out.WriteByte(' ')
			} else {
				out.WriteByte(' ')
			}
			continue
		}
		if (r == '\'' || r == '’') && i > 0 && i+1 < len(runes) && unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1]) {
			out.WriteRune(r)
			continue
		}
		if r == '"' || r == '`' || r == '“' || r == '‘' || r == '\'' {
			switch r {
			case '“':
				quote = '”'
			case '‘':
				quote = '’'
			default:
				quote = r
			}
			out.WriteByte(' ')
			continue
		}
		if r == '”' || r == '’' {
			out.WriteByte(' ')
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// wordsOf reduces a commission to its words, lowercased, separated by single
// spaces and fenced by one at each end.
//
// The fence is what makes a plain substring search a word search: " fix "
// cannot match inside "prefix". It also lets a two-word entry like "how
// would" stay one entry, which a set of single tokens could not express
// without the caller reassembling the phrase itself.
func wordsOf(text string) string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r)
	})
	return " " + strings.Join(fields, " ") + " "
}

// containsAny reports whether any of values appears in words as a whole word
// or whole phrase. words must come from wordsOf.
func containsAny(words string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(words, " "+value+" ") {
			return true
		}
	}
	return false
}

func (p Planner) agentFor(kind Kind, files []string) string {
	if kind == KindSearch && len(files) > 0 && hasAgent(p.Config, "reader") {
		return "reader"
	}
	if hasAgent(p.Config, "explore") {
		return "explore"
	}
	if len(p.Config.Agents) > 0 {
		return p.Config.Agents[0].Spec.Name
	}
	return ""
}

func hasAgent(cfg config.Config, name string) bool {
	for _, agent := range cfg.Agents {
		if agent.Spec.Name == name {
			return true
		}
	}
	return false
}

func (p Planner) modelsFor(agent string, kind Kind, repository string, effectful bool) []ModelChoice {
	role := modelRole(agent)
	if role == "" {
		return nil
	}
	roles := []string{role}
	if (kind == KindPlan || kind == KindChange) && role != "plan" && hasAgent(p.Config, "plan") {
		roles = append(roles, "plan")
	}
	if kind == KindChange && effectful && hasAgent(p.Config, "implement") && hasAgent(p.Config, "review") && hasAgent(p.Config, "audit") {
		roles = append(roles, "implement", "review", "audit")
	}
	out := make([]ModelChoice, 0, len(roles))
	seen := make(map[string]bool, len(roles))
	for _, role := range roles {
		if role == "" || seen[role] {
			continue
		}
		seen[role] = true
		out = append(out, p.modelChoice(role, repository))
	}
	return out
}

func modelRole(agent string) string {
	switch agent {
	case "atenea-coordinator":
		return "research"
	case "explore", "reader":
		return "explore"
	case "research":
		return "research"
	case "plan":
		return "plan"
	case "implement":
		return "implement"
	case "review", "semantic-reviewer":
		return "review"
	case "audit":
		return "audit"
	default:
		return ""
	}
}

func (p Planner) modelChoice(role, repository string) ModelChoice {
	name, fallbacks, effort := p.Config.Model.Explore, p.Config.Model.ExploreFallbacks, ""
	switch role {
	case "research":
		name, fallbacks, effort = p.Config.Model.Research, nil, p.Config.Model.ResearchReasoningEffort
		if name == "" {
			name = p.Config.Model.Explore
		}
	case "plan":
		name, fallbacks, effort = p.Config.Model.Plan, p.Config.Model.PlanFallbacks, p.Config.Model.PlanReasoningEffort
	case "implement":
		name, fallbacks, effort = p.Config.Model.Implement, nil, p.Config.Model.ImplementReasoningEffort
	case "review":
		name, fallbacks, effort = p.Config.Model.Review, nil, p.Config.Model.ReviewReasoningEffort
	case "audit":
		name, fallbacks, effort = p.Config.Model.Audit, nil, p.Config.Model.AuditReasoningEffort
	}
	backend := p.Config.Model.Backend
	if backend == "" {
		backend = "claude"
	}
	if role == "explore" && backend == "codex" && p.Config.Model.Research != "" {
		name, fallbacks, effort = p.Config.Model.Research, nil, p.Config.Model.ResearchReasoningEffort
		// `explore` remains the executable agent type for compatibility, while
		// the routed model role is the canonical Codex research profile.
		role = "research"
	}
	auto := strings.EqualFold(strings.TrimSpace(name), "auto")
	if auto {
		candidates := autoModelCandidates(role, backend, fallbacks)
		if len(candidates) == 0 {
			return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary, ReasoningEffort: effort,
				Reason: fmt.Sprintf("%s=auto needs declared model candidates for backend %s", role, backend)}
		}
		name = candidates[0]
		fallbacks = candidates[1:]
	}
	if role == "plan" && backend == "claude" {
		if name != "" && name != "claude-opus-5" {
			return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary, ReasoningEffort: effort,
				Reason: fmt.Sprintf("plan role requires claude-opus-5; configured %q is not permitted", name)}
		}
		reason := "plan role pinned to claude-opus-5; lower-reasoning fallbacks disabled"
		if auto {
			reason = "auto: " + reason
		}
		return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary, ReasoningEffort: effort,
			Name: name, Available: name != "", Reason: reason}
	}
	if role == "plan" {
		if !planModelAllowed(name) {
			return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary, ReasoningEffort: effort,
				Reason: fmt.Sprintf("plan role only permits high-reasoning models; configured %q is not permitted", name)}
		}
		for _, fallback := range fallbacks {
			if !planModelAllowed(fallback) {
				return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary, ReasoningEffort: effort,
					Reason: fmt.Sprintf("plan fallback %q is not a permitted high-reasoning model", fallback)}
			}
		}
	}
	candidates := append([]string{name}, fallbacks...)
	ranker := p.Ranker
	if ranker == nil {
		ranker = StaticModelRanker{}
	}
	selected, reason := ranker.SelectModel(repository, role, name, candidates)
	if selected != "" {
		fallbacks = slices.DeleteFunc(slices.Clone(candidates), func(candidate string) bool { return candidate == selected })
		name = selected
	}
	// A decision workflow pins the selected model for every provider. Ranked
	// candidates are planning evidence, not a runtime substitution chain.
	if slices.Contains([]string{"explore", "research", "plan", "implement", "review", "audit"}, role) {
		fallbacks = nil
	}
	available := strings.TrimSpace(name) != ""
	if !available {
		reason = "role has no model configured; execution would be refused"
	}
	if auto {
		reason = "auto: " + reason
	}
	return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary, ReasoningEffort: effort,
		Name: name, Fallbacks: slices.Clone(fallbacks), Available: available, Reason: reason}
}

func autoModelCandidates(role, backend string, declared []string) []string {
	var defaults []string
	switch backend {
	case "claude":
		switch role {
		case "explore", "research":
			defaults = []string{"claude-sonnet-5", "claude-haiku-4-5"}
		case "plan":
			defaults = []string{"claude-opus-5"}
		}
	case "opencode":
		switch role {
		case "explore":
			defaults = []string{"anthropic/claude-sonnet-5", "anthropic/claude-haiku-4-5"}
		case "plan":
			defaults = []string{"anthropic/claude-opus-5", "openai/gpt-5.6-sol", "openai/gpt-5.6-luna"}
		}
	}
	out := make([]string, 0, len(defaults)+len(declared))
	seen := map[string]struct{}{}
	for _, candidate := range append(defaults, declared...) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || strings.EqualFold(candidate, "auto") {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func planModelAllowed(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	switch name {
	case "opus", "claude-opus-5", "anthropic/opus", "anthropic/claude-opus-5", "anthropic/claude-opus-5-fast",
		"gpt-5.6-sol", "gpt-5.6-sol-fast", "openai/gpt-5.6-sol", "openai/gpt-5.6-sol-fast",
		"gpt-5.6-luna", "gpt-5.6-luna-fast", "openai/gpt-5.6-luna", "openai/gpt-5.6-luna-fast":
		return true
	default:
		return strings.Contains(name, "opus")
	}
}

func firstRepository(repositories []string) string {
	if len(repositories) == 0 {
		return ""
	}
	return repositories[0]
}

func (p Planner) toolsFor(agent string, kind Kind, requested string) []ToolChoice {
	var out []ToolChoice
	if agent == "explore" || agent == "reader" {
		out = append(out, ToolChoice{ID: "Read", Kind: "builtin", Source: "agent", Selected: true,
			Reason: "read-only file access for repository inspection"})
		out = append(out, ToolChoice{ID: "Glob", Kind: "builtin", Source: "agent", Selected: true,
			Reason: "resolve files without exposing a shell"})
	}
	if agent == "explore" {
		for _, id := range p.capabilitiesFor(kind) {
			out = append(out, ToolChoice{ID: "atenea_" + strings.ReplaceAll(id, ".", "_"), Kind: "capability", Source: "atenea",
				Capability: id, Selected: true, Reason: "native capability selected for the intent"})
		}
	}
	for _, server := range p.Config.MCPServers {
		if server.Expose != config.ExposeRaw {
			continue
		}
		for _, tool := range server.Tools {
			id := "raw." + server.ID + "." + tool
			selected := requested == id
			reason := "available through allow-list; no semantic mapping is declared"
			if selected {
				reason = "explicitly selected; execution remains permission-gated"
			}
			out = append(out, ToolChoice{ID: id, Kind: "mcp.raw", Source: server.ID,
				Effects: server.EffectsOf(tool), Selected: selected, Reason: reason})
		}
	}
	return out
}

func selectedToolExists(tools []ToolChoice, requested string) bool {
	for _, tool := range tools {
		if tool.ID == requested && tool.Selected {
			return true
		}
	}
	return false
}

func (p Planner) capabilitiesFor(kind Kind) []string {
	wanted := []string{"code.context"}
	if kind == KindUnderstand || kind == KindPlan || kind == KindChange {
		wanted = []string{"symbol.intent_search", "code.context", "symbol.search", "symbol.overview", "symbol.dependencies", "symbol.consumers"}
	}
	if kind == KindSearch || kind == KindUnderstand || kind == KindChange || kind == KindPlan {
		wanted = append(wanted, "code.search")
	}
	if kind == KindPlan || kind == KindChange {
		wanted = append(wanted, "symbol.definition", "symbol.references", "symbol.impact", "symbol.source")
	}
	available := make(map[string]bool, len(p.Config.Capabilities))
	for _, cap := range p.Config.Capabilities {
		available[cap.ID] = true
	}
	out := wanted[:0]
	for _, id := range wanted {
		if available[id] && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func (p Planner) capabilityChoice(id, repo, prefer string) CapabilityChoice {
	choice := CapabilityChoice{ID: id, Repository: repo, Reason: "catalog candidates"}
	for _, impl := range p.Config.Implementations {
		if impl.Capability == id {
			choice.Providers = append(choice.Providers, impl.ID)
		}
	}
	sort.Strings(choice.Providers)
	if len(choice.Providers) == 0 {
		choice.Unavailable = true
		choice.Reason = "no implementation is registered"
		return choice
	}
	if p.Selector != nil {
		decision, err := p.Selector.SelectWithPreference(id, repo, prefer)
		if err != nil {
			choice.Unavailable = true
			choice.Reason = err.Error()
			return choice
		}
		choice.Chosen = decision.Chosen.ID
		choice.Reason = decision.Reason
	}
	return choice
}

func (p Planner) workflowFor(req Request, kind Kind, agent string, repos []string) workflow.Graph {
	grant := req.BudgetUSD
	// The steps compose from the same list plan.Effects is built from, and
	// they have to: what the plan prints is what --confirm asks the operator
	// about, and what the steps carry is what the run may actually do.
	// Composing them from req.Effects alone dropped the standing grant, so a
	// chat running on a floor that allows writing was shown "write" on the
	// plan, stopped for confirmation because of it, and then dispatched steps
	// that could only read.
	//
	// The two layers are not treated alike, and the asymmetry is deliberate.
	// What the commission explicitly asked for travels whole, so an effect
	// the agent type cannot cause refuses the plan out loud rather than being
	// dropped behind the operator's back. The standing grant is a floor
	// somebody set once and is not about this commission, so it is narrowed
	// to what this agent type declares instead of refusing every plan a wide
	// operator line touches.
	type plannedStep struct {
		step     workflow.Step
		estimate BudgetEstimate
	}
	planned := make([]plannedStep, 0, len(repos)*5)
	grantedEffects := mergeEffects(req.StandingEffects, req.Effects)
	if hasAgent(p.Config, "atenea-coordinator") {
		repo := firstRepository(repos)
		estimate := p.estimate(repo, "atenea-coordinator", p.modelChoice("research", repo).Name)
		planned = append(planned, plannedStep{step: workflow.Step{ID: "coordinate", TypeName: "atenea-coordinator",
			Task:       contract.Task{Objective: req.Text, Files: slices.Clone(req.Files), Criterion: criterionOrDefault(req.Criterion, "validate scope and delegate no more than two specialists at once")},
			Permission: contract.Permission{Task: req.Text, Effects: p.agentEffects("atenea-coordinator", grantedEffects)}}, estimate: estimate})
	}
	for repoIndex, repo := range repos {
		id := "explore-" + repo
		estimate := p.estimate(repo, agent, p.modelChoice(modelRole(agent), repo).Name)
		planned = append(planned, plannedStep{step: workflow.Step{ID: id, TypeName: agent,
			Task:       contract.Task{Objective: req.Text, Files: slices.Clone(req.Files), Criterion: "return a complete, evidence-backed repository assessment"},
			Permission: contract.Permission{Task: req.Text, Effects: p.agentEffects(agent, grantedEffects)}}, estimate: estimate})
		if hasAgent(p.Config, "atenea-coordinator") {
			planned[len(planned)-1].step.Needs = []string{"coordinate"}
		}
		if kind == KindPlan || kind == KindChange {
			planAgent := "plan"
			if !hasAgent(p.Config, planAgent) {
				continue
			}
			planID := "plan-" + repo
			estimate := p.estimate(repo, planAgent, p.modelChoice("plan", repo).Name)
			planned = append(planned, plannedStep{step: workflow.Step{ID: planID, TypeName: planAgent,
				Task: contract.Task{Objective: "turn the exploration into an executable plan for: " + req.Text,
					Criterion: "return a valid workflow graph with explicit steps and budgets"},
				Needs: []string{id}, Subject: id,
				Permission: contract.Permission{Task: req.Text, Effects: []contract.Effect{contract.EffectRead}}}, estimate: estimate})
			if kind == KindChange && slices.Contains(grantedEffects, contract.EffectWrite) && hasAgent(p.Config, "implement") && hasAgent(p.Config, "review") && hasAgent(p.Config, "audit") {
				point := fmt.Sprintf("P%02d", repoIndex+1)
				title := "Implementar y verificar " + repo
				implementID := "implement-" + repo
				implementation := workflow.Step{ID: implementID, PointID: point, PointTitle: title, TypeName: "implement",
					Task:  contract.Task{Objective: req.Text, Files: slices.Clone(req.Files), Criterion: criterionOrDefault(req.Criterion, "apply the authorized change completely and provide test evidence")},
					Needs: []string{planID}, Permission: contract.Permission{Task: req.Text, Effects: p.agentEffects("implement", grantedEffects)}}
				reviewID := "review-" + repo
				review := workflow.Step{ID: reviewID, PointID: point, PointTitle: title, TypeName: "review",
					Task:    contract.Task{Objective: "independently review the implementation for: " + req.Text, Criterion: "approve only a complete, tested implementation"},
					Subject: implementID, Permission: contract.Permission{Task: req.Text, Effects: []contract.Effect{contract.EffectRead}}}
				auditID := "audit-" + repo
				audit := workflow.Step{ID: auditID, PointID: point, PointTitle: title, TypeName: "audit",
					Task:    contract.Task{Objective: "perform the final acceptance and security audit for: " + req.Text, Criterion: "approve only when implementation and independent review evidence are complete"},
					Subject: reviewID, Permission: contract.Permission{Task: req.Text, Effects: []contract.Effect{contract.EffectRead}}}
				for _, next := range []workflow.Step{implementation, review, audit} {
					estimate := p.estimate(repo, next.TypeName, p.modelChoice(next.TypeName, repo).Name)
					planned = append(planned, plannedStep{step: next, estimate: estimate})
				}
			}
		}
	}
	total := 0.0
	for _, item := range planned {
		total += item.estimate.EstimatedUSD
	}
	steps := make([]workflow.Step, 0, len(planned))
	for _, item := range planned {
		step := item.step
		step.BudgetEstimateUSD = item.estimate.EstimatedUSD
		step.BudgetMinimumUSD = item.estimate.MinimumUSD
		step.BudgetSource = item.estimate.Source
		if total > 0 {
			step.Permission.BudgetUSD = grant * item.estimate.EstimatedUSD / total
		}
		steps = append(steps, step)
	}
	criterion := strings.TrimSpace(req.Criterion)
	if criterion == "" {
		criterion = "all requested repositories have an evidence-backed answer and every claimed change is verified"
	}
	return workflow.Graph{Task: req.Text, Criterion: criterion, Limits: req.Limits, GrantUSD: grant, Steps: steps}
}

func criterionOrDefault(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func specialistRoles(agent string, kind Kind, cfg config.Config) []string {
	if kind == KindChange && hasAgent(cfg, "implement") && hasAgent(cfg, "review") && hasAgent(cfg, "audit") {
		return []string{"implementation", "verification"}
	}
	roles := []string{agent}
	if (kind == KindPlan || kind == KindChange) && hasAgent(cfg, "plan") && agent != "plan" {
		roles = append(roles, "plan")
	}
	if len(roles) > 2 {
		roles = roles[:2]
	}
	return roles
}

func (p Planner) estimate(repository, agent, model string) BudgetEstimate {
	if p.Estimator != nil {
		if estimate := p.Estimator.Estimate(repository, agent, model); estimate.EstimatedUSD > 0 {
			if estimate.MinimumUSD <= 0 || estimate.MinimumUSD > estimate.EstimatedUSD {
				estimate.MinimumUSD = estimate.EstimatedUSD * 0.80
			}
			return estimate
		}
	}
	return (DefaultBudgetEstimator{}).Estimate(repository, agent, model)
}

func budgetSummary(graph workflow.Graph) BudgetSummary {
	out := BudgetSummary{GrantedUSD: graph.GrantUSD, Sufficient: true}
	for _, step := range graph.Steps {
		out.RequiredUSD += step.BudgetEstimateUSD
		out.MinimumUSD += step.BudgetMinimumUSD
	}
	out.MarginUSD = out.GrantedUSD - out.RequiredUSD
	out.Sufficient = out.GrantedUSD+1e-9 >= out.RequiredUSD
	return out
}

// agentEffects narrows a standing grant to what one agent type can cause.
//
// The narrowing is the reason this takes a name at all, and for a while it
// did not do it: the parameter was ignored and the list was copied through
// whole. That was survivable only because the standing grant never reached
// here either -- once it does, an operator whose line allows spawning
// processes would stamp `process` onto a reader step, and workflow.Compile
// refuses the whole graph over it, because a type's declared effects are the
// ceiling on what a spawn of that type will honor. Refusing to plan at all
// is the wrong answer to "the operator may do more than this agent can": a
// standing grant is a ceiling, not an instruction.
//
// An agent type nobody declared is left alone. Compile names that as the
// missing type it is, which is a far more useful sentence than a step that
// silently ends up with no effects at all.
func (p Planner) agentEffects(name string, granted []contract.Effect) []contract.Effect {
	index := slices.IndexFunc(p.Config.Agents, func(a config.AgentType) bool { return a.Spec.Name == name })
	if index < 0 {
		return slices.Clone(granted)
	}
	declared := p.Config.Agents[index].Effects
	out := make([]contract.Effect, 0, len(granted))
	for _, effect := range granted {
		if slices.Contains(declared, effect) {
			out = append(out, effect)
		}
	}
	return out
}

func mergeEffects(base, extra []contract.Effect) []contract.Effect {
	out := slices.Clone(base)
	for _, effect := range extra {
		if !slices.Contains(out, effect) {
			out = append(out, effect)
		}
	}
	return out
}

func waveCount(graph workflow.Graph) int {
	if len(graph.Steps) == 0 {
		return 0
	}
	depth := make(map[string]int, len(graph.Steps))
	maxDepth := 0
	for _, step := range graph.Steps {
		level := 1
		for _, edge := range step.Edges() {
			if upstream := depth[edge.ID] + 1; upstream > level {
				level = upstream
			}
		}
		depth[step.ID] = level
		if level > maxDepth {
			maxDepth = level
		}
	}
	return maxDepth
}
