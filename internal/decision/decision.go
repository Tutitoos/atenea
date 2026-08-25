// Package decision turns a natural-language commission into an explainable
// execution plan. It deliberately sits above the capability selector and the
// workflow engine: it chooses the shape of the work, while those packages
// continue to own provider selection and graph validation.
package decision

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

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
	KindUnderstand Kind = "understand"
	KindSearch     Kind = "search"
	KindPlan       Kind = "plan"
	KindChange     Kind = "change"
)

// Request is the input to the decision layer.
type Request struct {
	Text            string
	Repository      string
	Files           []string
	BudgetUSD       float64
	Effects         []contract.Effect
	StandingEffects []contract.Effect
	Prefer          string
	Tool            string
}

// ModelChoice describes the model role selected for one agent type.
type ModelChoice struct {
	Role      string   `json:"role"`
	Backend   string   `json:"backend"`
	Binary    string   `json:"binary"`
	Name      string   `json:"name"`
	Fallbacks []string `json:"fallbacks,omitempty"`
	Available bool     `json:"available"`
	Reason    string   `json:"reason"`
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
	Text         string             `json:"text"`
	Intent       Kind               `json:"intent"`
	Repositories []string           `json:"repositories"`
	Effects      []contract.Effect  `json:"effects"`
	Agent        string             `json:"agent"`
	Models       []ModelChoice      `json:"models"`
	Tools        []ToolChoice       `json:"tools"`
	Capabilities []CapabilityChoice `json:"capabilities"`
	Budget       BudgetSummary      `json:"budget"`
	Workflow     workflow.Graph     `json:"workflow"`
	Valid        bool               `json:"valid"`
	Reasons      []Reason           `json:"reasons"`
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
	Config    config.Config
	Selector  Selector
	Estimator BudgetEstimator
	Ranker    ModelRanker
}

// Build creates and validates one decision plan.
func (p Planner) Build(req Request) (Plan, error) {
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

	repos, err := p.repositories(req.Repository)
	if err != nil {
		return Plan{}, err
	}
	intent := infer(text)
	agent := p.agentFor(intent, req.Files)
	plan := Plan{
		Text:         text,
		Intent:       intent,
		Repositories: repos,
		Effects:      mergeEffects(req.StandingEffects, req.Effects),
		Agent:        agent,
		Reasons: []Reason{
			{Stage: "intent", Message: fmt.Sprintf("classified as %s from the request text", intent)},
			{Stage: "policy", Message: "user constraints and declared effects are applied before provider choice"},
		},
	}
	plan.Models = p.modelsFor(agent, intent, firstRepository(repos))
	plan.Tools = p.toolsFor(agent, intent, req.Tool)
	if req.Tool != "" && !selectedToolExists(plan.Tools, req.Tool) {
		plan.Reasons = append(plan.Reasons, Reason{Stage: "tool", Message: "requested tool is not declared or allow-listed: " + req.Tool})
	}
	modelsReady := true
	toolsReady := req.Tool == "" || selectedToolExists(plan.Tools, req.Tool)
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
	plan.Valid = modelsReady && toolsReady && plan.Budget.Sufficient
	plan.Reasons = append(plan.Reasons, Reason{Stage: "workflow",
		Message: fmt.Sprintf("compiled %d step(s) into %d wave(s)", len(compiled.Graph.Steps), waveCount(compiled.Graph))})
	return plan, nil
}

func (p Planner) stampRoutes(plan *Plan, agent string, kind Kind) {
	for i := range plan.Workflow.Steps {
		step := &plan.Workflow.Steps[i]
		role := modelRole(agent)
		if step.TypeName == "plan" {
			role = "plan"
		}
		route := contract.Route{}
		if role != "" {
			model := p.modelChoice(role, repositoryFromStep(step.ID, plan.Repositories))
			route.Model, route.Fallbacks, route.Backend, route.Binary = model.Name, slices.Clone(model.Fallbacks), model.Backend, model.Binary
		}
		if step.TypeName == "explore" {
			route.Capabilities = p.capabilitiesFor(kind)
		}
		if step.TypeName == "explore" {
			for _, choice := range plan.Capabilities {
				if choice.Repository == repositoryFromStep(step.ID, plan.Repositories) && choice.Chosen != "" {
					if route.Providers == nil {
						route.Providers = make(map[string]string)
					}
					route.Providers[choice.ID] = choice.Chosen
				}
			}
		}
		if step.TypeName != "plan" {
			for _, tool := range plan.Tools {
				if tool.Selected {
					route.Tools = append(route.Tools, tool.ID)
				}
			}
		}
		step.Route = &route
	}
}

func repositoryFromStep(id string, repositories []string) string {
	for _, repo := range repositories {
		if id == "explore-"+repo || id == "plan-"+repo {
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

// The vocabulary each intent is recognised by, as whole words rather than as
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
	changeWords = []string{
		"implementar", "implementa", "implement", "implements", "implementing",
		"cambiar", "cambia", "change", "changes", "changing",
		"fix", "fixes", "fixed", "fixing",
		"refactorizar", "refactoriza", "refactor", "refactors", "refactoring",
		"construir", "construye", "build", "builds", "building",
		"añadir", "añade", "add", "adds", "adding",
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
	words := wordsOf(text)
	if containsAny(words, changeWords...) {
		return KindChange
	}
	if containsAny(words, planWords...) {
		return KindPlan
	}
	if containsAny(words, searchWords...) {
		return KindSearch
	}
	return KindUnderstand
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

func (p Planner) modelsFor(agent string, kind Kind, repository string) []ModelChoice {
	role := modelRole(agent)
	if role == "" {
		return nil
	}
	out := []ModelChoice{p.modelChoice(role, repository)}
	if (kind == KindPlan || kind == KindChange) && role != "plan" && hasAgent(p.Config, "plan") {
		out = append(out, p.modelChoice("plan", repository))
	}
	return out
}

func modelRole(agent string) string {
	switch agent {
	case "explore", "reader":
		return "explore"
	case "plan":
		return "plan"
	default:
		return ""
	}
}

func (p Planner) modelChoice(role, repository string) ModelChoice {
	name := p.Config.Model.Explore
	fallbacks := p.Config.Model.ExploreFallbacks
	if role == "plan" {
		name = p.Config.Model.Plan
		fallbacks = p.Config.Model.PlanFallbacks
	}
	backend := p.Config.Model.Backend
	if backend == "" {
		backend = "claude"
	}
	auto := strings.EqualFold(strings.TrimSpace(name), "auto")
	if auto {
		candidates := autoModelCandidates(role, backend, fallbacks)
		if len(candidates) == 0 {
			return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary,
				Reason: fmt.Sprintf("%s=auto needs declared model candidates for backend %s", role, backend)}
		}
		name = candidates[0]
		fallbacks = candidates[1:]
	}
	if role == "plan" && backend == "claude" {
		if name != "" && name != "claude-opus-5" {
			return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary,
				Reason: fmt.Sprintf("plan role requires claude-opus-5; configured %q is not permitted", name)}
		}
		reason := "plan role pinned to claude-opus-5; lower-reasoning fallbacks disabled"
		if auto {
			reason = "auto: " + reason
		}
		return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary,
			Name: name, Available: name != "", Reason: reason}
	}
	if role == "plan" {
		if !planModelAllowed(name) {
			return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary,
				Reason: fmt.Sprintf("plan role only permits high-reasoning models; configured %q is not permitted", name)}
		}
		for _, fallback := range fallbacks {
			if !planModelAllowed(fallback) {
				return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary,
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
	if role == "plan" && backend == "claude" {
		fallbacks = nil
	}
	available := strings.TrimSpace(name) != ""
	if !available {
		reason = "role has no model configured; execution would be refused"
	}
	if auto {
		reason = "auto: " + reason
	}
	return ModelChoice{Role: role, Backend: backend, Binary: p.Config.Model.Binary,
		Name: name, Fallbacks: slices.Clone(fallbacks), Available: available, Reason: reason}
}

func autoModelCandidates(role, backend string, declared []string) []string {
	var defaults []string
	switch backend {
	case "claude":
		switch role {
		case "explore":
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
	if kind == KindSearch || kind == KindUnderstand || kind == KindChange || kind == KindPlan {
		wanted = append(wanted, "code.search")
	}
	if kind == KindPlan || kind == KindChange {
		wanted = append(wanted, "symbol.definition", "symbol.references")
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
	effects := mergeEffects(p.agentEffects(agent, req.StandingEffects), req.Effects)
	type plannedStep struct {
		step     workflow.Step
		estimate BudgetEstimate
	}
	planned := make([]plannedStep, 0, len(repos)*2)
	for _, repo := range repos {
		id := "explore-" + repo
		estimate := p.estimate(repo, agent, p.modelChoice(modelRole(agent), repo).Name)
		planned = append(planned, plannedStep{step: workflow.Step{ID: id, TypeName: agent,
			Task:       contract.Task{Objective: req.Text, Files: slices.Clone(req.Files), Criterion: "return a complete, evidence-backed repository assessment"},
			Permission: contract.Permission{Task: req.Text, Effects: effects}}, estimate: estimate})
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
	return workflow.Graph{Task: req.Text, GrantUSD: grant, Steps: steps}
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
// ceiling on what a spawn of that type will honour. Refusing to plan at all
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
