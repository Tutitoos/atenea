package decision

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type selectorStub struct{}

func (selectorStub) SelectWithPreference(capabilityID, repositoryID, prefer string) (selector.Decision, error) {
	return selector.Decision{
		Capability: capabilityID,
		Repository: repositoryID,
		Chosen:     contract.Implementation{ID: prefer},
		Reason:     "stub preference",
	}, nil
}

func TestBuildClassifiesSearchAndUsesTheReaderShape(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Implementations = []contract.Implementation{
		{ID: "ripgrep", Provider: "local", Capability: "code.search"},
		{ID: "kivgraph.search", Provider: "kivgraph", Capability: "code.search"},
	}
	cfg.MCPServers = []config.MCPServer{
		{ID: "docs", Expose: config.ExposeRaw, Tools: []string{"query"}, Effects: []contract.Effect{contract.EffectRead}},
	}

	plan, err := (Planner{Config: cfg, Selector: selectorStub{}}).Build(Request{
		Text: "buscar autenticación", Repository: "repo", Files: []string{"internal/auth.go"}, BudgetUSD: 2, Prefer: "ripgrep",
		StandingEffects: []contract.Effect{contract.EffectProcess},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid {
		t.Fatalf("plan invalid: %+v", plan.Reasons)
	}
	if plan.Intent != KindSearch || plan.Agent != "reader" {
		t.Fatalf("intent/agent = %s/%s, want search/reader", plan.Intent, plan.Agent)
	}
	if plan.Workflow.Steps[0].Route == nil || plan.Workflow.Steps[0].Route.Model != "sonnet" {
		t.Fatalf("route = %+v, want the selected explore model", plan.Workflow.Steps[0].Route)
	}
	if got := plan.Capabilities[1].Chosen; got != "ripgrep" {
		t.Fatalf("chosen provider = %q, want ripgrep", got)
	}
	if plan.Effects[0] != contract.EffectProcess {
		t.Fatalf("policy effects = %v, want standing process", plan.Effects)
	}
	if len(plan.Tools) != 3 || plan.Tools[2].Selected {
		t.Fatalf("tools = %+v, want Read, Glob and an unselected raw MCP tool", plan.Tools)
	}
}

func TestBuildSplitsBudgetAcrossExploreAndPlanSteps(t *testing.T) {
	cfg := fixtureConfig("one", "two")
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "diseña el flujo de pagos", BudgetUSD: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid {
		t.Fatalf("plan invalid: %+v", plan.Reasons)
	}
	if len(plan.Models) != 2 || plan.Models[0].Role != "explore" || plan.Models[1].Role != "plan" {
		t.Fatalf("models = %+v, want explore and plan roles", plan.Models)
	}
	if len(plan.Workflow.Steps) != 5 {
		t.Fatalf("steps = %d, want coordinator plus four specialists", len(plan.Workflow.Steps))
	}
	var total float64
	for _, step := range plan.Workflow.Steps {
		total += step.Permission.BudgetUSD
	}
	if total > 10+1e-9 {
		t.Fatalf("step shares = %.12f, past grant", total)
	}
	if plan.Workflow.Steps[4].Subject != "explore-two" {
		t.Fatalf("second plan subject = %q, want explore-two", plan.Workflow.Steps[4].Subject)
	}
	if plan.Budget.RequiredUSD <= 0 || !plan.Budget.Sufficient {
		t.Fatalf("budget = %+v, want a sufficient forecast", plan.Budget)
	}
	if plan.Workflow.Steps[1].BudgetEstimateUSD == plan.Workflow.Steps[2].BudgetEstimateUSD {
		t.Fatal("explore and plan received the same forecast; model-aware allocation was not applied")
	}
}

func TestBuildRoutesCodexResearchAndPlanProfilesWithoutFallbacks(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Model = config.Model{
		Backend: "codex", Binary: "codex",
		CodexNative: true,
		Research:    "gpt-5.6-sol", Plan: "gpt-5.6-sol",
		Implement: "gpt-5.6-luna", Review: "gpt-5.6-sol", Audit: "gpt-6-astra",
		ResearchReasoningEffort: "medium", PlanReasoningEffort: "medium",
		ImplementReasoningEffort: "xhigh", ReviewReasoningEffort: "medium", AuditReasoningEffort: "medium",
	}
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "preparar un plan", Repository: "repo", BudgetUSD: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid {
		t.Fatalf("plan invalid: %+v", plan.Reasons)
	}
	if len(plan.Workflow.Steps) != 3 {
		t.Fatalf("steps = %d, want coordinator, research and plan", len(plan.Workflow.Steps))
	}
	research, planned := plan.Workflow.Steps[1].Route, plan.Workflow.Steps[2].Route
	if research == nil || research.Role != "research" || research.RequestedModel != "gpt-5.6-sol" || research.RequestedReasoningEffort != "medium" || len(research.Fallbacks) != 0 {
		t.Fatalf("research route = %+v", research)
	}
	if planned == nil || planned.Role != "plan" || planned.RequestedModel != "gpt-5.6-sol" || planned.RequestedReasoningEffort != "medium" || len(planned.Fallbacks) != 0 {
		t.Fatalf("plan route = %+v", planned)
	}
	if !research.VisibilityRequired || !planned.VisibilityRequired {
		t.Fatal("codex visible routes must require the native App Server")
	}
}

func TestBuildPersistsCoordinatorCriterionLimitsAndTwoSpecialistCeiling(t *testing.T) {
	cfg := fixtureConfig("repo")
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "preparar un plan", Repository: "repo", BudgetUSD: 10,
		Criterion: "all findings have source and verification", Limits: contract.Limits{MaxDuration: time.Minute, MaxTokens: 400},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Criterion == "" || plan.Workflow.Criterion != plan.Criterion || plan.Workflow.Limits.MaxTokens != 400 {
		t.Fatalf("plan metadata = %+v workflow=%+v", plan, plan.Workflow)
	}
	if plan.Coordinator != "atenea-coordinator" || len(plan.Specialists) != 2 {
		t.Fatalf("topology = coordinator %q specialists %v", plan.Coordinator, plan.Specialists)
	}
}

func TestBuildRejectsACommissionBelowTheModelAwareForecast(t *testing.T) {
	cfg := fixtureConfig("repo")
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "preparar un plan", Repository: "repo", BudgetUSD: 0.25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Valid || plan.Budget.Sufficient {
		t.Fatalf("plan = %+v, want budget preflight refusal", plan.Budget)
	}
	found := false
	for _, reason := range plan.Reasons {
		if reason.Stage == "budget" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons = %+v, want budget reason", plan.Reasons)
	}
}

func TestRoutesPinSelectedModelsWithoutRuntimeFallbacks(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Model.ExploreFallbacks = []string{"claude-haiku-5"}
	cfg.Model.PlanFallbacks = []string{"claude-sonnet-5"}
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "preparar un plan", Repository: "repo", BudgetUSD: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Workflow.Steps[1].Route.Fallbacks; len(got) != 0 {
		t.Fatalf("explore runtime fallbacks = %v", got)
	}
	if got := plan.Workflow.Steps[2].Route; got.Model != "claude-opus-5" || len(got.Fallbacks) != 0 {
		t.Fatalf("plan route = %+v, want pinned Opus without fallback", got)
	}
}

func TestAutoExploreChoosesFromSafeCandidatesUsingHistory(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Model.Explore = "auto"
	ranker := AdaptiveModelRanker{History: modelHistoryStub{
		"explore/claude-sonnet-5":  {Samples: 3, MedianUSD: 1.00},
		"explore/claude-haiku-4-5": {Samples: 3, MedianUSD: 0.50},
	}}
	plan, err := (Planner{Config: cfg, Ranker: ranker}).Build(Request{
		Text: "entender el router", Repository: "repo", BudgetUSD: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	route := plan.Workflow.Steps[1].Route
	if route == nil || route.Model != "claude-haiku-4-5" || len(route.Fallbacks) != 0 {
		t.Fatalf("auto explore route = %+v", route)
	}
	if plan.Models[0].Reason == "" || !strings.Contains(plan.Models[0].Reason, "auto") {
		t.Fatalf("auto explore reason = %q", plan.Models[0].Reason)
	}
}

func TestAutoPlanPinsClaudeToOpusWithoutDowngrade(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Model.Plan = "auto"
	cfg.Model.PlanFallbacks = []string{"claude-sonnet-5"}
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "preparar un plan", Repository: "repo", BudgetUSD: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	route := plan.Workflow.Steps[2].Route
	if route == nil || route.Model != "claude-opus-5" || len(route.Fallbacks) != 0 {
		t.Fatalf("auto plan route = %+v", route)
	}
	if !strings.Contains(plan.Models[1].Reason, "auto") {
		t.Fatalf("auto plan reason = %q", plan.Models[1].Reason)
	}
}

func TestAutoPlanSelectsOpusAndPinsItForOpenCode(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Model.Backend = "opencode"
	cfg.Model.Binary = "opencode"
	cfg.Model.Explore = "auto"
	cfg.Model.Plan = "auto"
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "preparar un plan", Repository: "repo", BudgetUSD: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	route := plan.Workflow.Steps[2].Route
	if route == nil || route.Model != "anthropic/claude-opus-5" || len(route.Fallbacks) != 0 {
		t.Fatalf("OpenCode auto plan route = %+v", route)
	}
}

func TestOpenCodePlanRejectsLowerReasoningFallback(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Model.Backend = "opencode"
	cfg.Model.Plan = "anthropic/claude-opus-5"
	cfg.Model.PlanFallbacks = []string{"anthropic/claude-sonnet-5"}
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "preparar un plan", Repository: "repo", BudgetUSD: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Valid || !strings.Contains(plan.Models[1].Reason, "high-reasoning") {
		t.Fatalf("plan = valid %v, model reason %q; want lower-reasoning fallback refusal", plan.Valid, plan.Models[1].Reason)
	}
}

func TestBuildRejectsAnEffectOutsideTheAgentCeiling(t *testing.T) {
	cfg := fixtureConfig("repo")
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "implementar el cambio", Repository: "repo", BudgetUSD: 1,
		Effects: []contract.Effect{contract.EffectWrite},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Valid {
		t.Fatal("plan became executable after requesting an undeclared write effect")
	}
	if len(plan.Reasons) == 0 || plan.Reasons[len(plan.Reasons)-1].Stage != "workflow" {
		t.Fatalf("reasons = %+v, want workflow refusal", plan.Reasons)
	}
}

func fixtureConfig(repositories ...string) config.Config {
	cfg := config.Config{
		Orchestrator: config.Orchestrator{BudgetUSD: 5},
		Model:        config.Model{Backend: "claude", Binary: "claude", Explore: "sonnet", Plan: "claude-opus-5"},
		Capabilities: []contract.Capability{
			{ID: "code.context"}, {ID: "code.search"}, {ID: "symbol.definition"}, {ID: "symbol.references"},
		},
	}
	for _, id := range repositories {
		cfg.Repositories = append(cfg.Repositories, contract.Repository{ID: id, Path: "/tmp/" + id})
	}
	cfg.Agents = append(cfg.Agents, config.AgentType{Spec: contract.AgentTypeSpec{
		Name: "atenea-coordinator", Kind: contract.AgentOrchestrator,
		Result: []contract.Field{{Name: "result", Type: contract.TypeString, Required: true}},
	}, Effects: []contract.Effect{contract.EffectRead, contract.EffectWrite}})
	for _, name := range []string{"reader", "explore", "plan"} {
		typeDef := config.AgentType{Spec: contract.AgentTypeSpec{
			Name: name, Kind: contract.AgentSpecialized,
			Result: []contract.Field{{Name: "result", Type: contract.TypeString, Required: true}},
		}, Effects: []contract.Effect{contract.EffectRead}}
		if name == "plan" {
			typeDef.ReadsSubject = true
		}
		cfg.Agents = append(cfg.Agents, typeDef)
	}
	return cfg
}

// Intent used to be classified with strings.Contains against a vocabulary
// that includes three-letter words, so ordinary technical English matched the
// wrong branch: "prefix" contains "fix", "address" contains "add" and
// "explanation" contains "plan". KindChange is tested first, so every one of
// them turned a question into a change -- the classification that decides
// whether the plan asks for write effects and whether the CLI stops for
// --confirm.
func TestIntentIsClassifiedOnWholeWordsNotSubstrings(t *testing.T) {
	for text, want := range map[string]Kind{
		// The three traps, one per branch.
		"dónde está el prefix handler":             KindSearch,
		"where is the address parser":              KindSearch,
		"give me an explanation of the retry loop": KindUnderstand,
		// And the words themselves still classify, inflected or not.
		"fix the login bug":                                        KindChange,
		"fixing the login bug":                                     KindChange,
		"añade un campo al formulario":                             KindChange,
		"how would you split this":                                 KindPlan,
		"diseña el flujo de pagos":                                 KindPlan,
		"buscar autenticación":                                     KindSearch,
		"explain how the router works":                             KindUnderstand,
		"Do not make changes yet. Tell me how you would add Laya.": KindPlan,
		"Only explain how to fix the login bug.":                   KindUnderstand,
		"Do not only explain; implement the fix.":                  KindChange,
		"Search for the quoted instruction \"implement now\".":     KindSearch,
		"Find the literal 'implement now' string.":                 KindSearch,
		"Busca la frase “añade ahora”.":                            KindSearch,
		"Outline a refactoring approach but do not apply it.":      KindPlan,
		"Design how to migrate the database.":                      KindPlan,
		"Migrate the database now.":                                KindChange,
		"Search for the failing path, then fix it.":                KindChange,
		"Only explain; do not edit.":                               KindUnderstand,
		"Don't fix it; explain the bug.":                           KindUnderstand,
		"How to migrate the database safely.":                      KindPlan,
		"Corrige el error de acceso.":                              KindChange,
		"Cómo corregir el error de acceso.":                        KindPlan,
		"No corrijas el error; explica la causa.":                  KindUnderstand,
		"We will migrate the database next week.":                  KindPlan,
		"We should fix the login bug later.":                       KindPlan,
		"Añade el campo mañana.":                                   KindPlan,
		"Should we implement this now?":                            KindPlan,
		"Implement this now.":                                      KindChange,
		"Do not only explain; implement next week.":                KindPlan,
	} {
		if got := infer(text); got != want {
			t.Errorf("infer(%q) = %s, want %s", text, got, want)
		}
	}
}

func TestBuildRequiresRepositoryBoundContextForBareContinuation(t *testing.T) {
	cfg := fixtureConfig("repo")
	for _, text := range []string{"hazlo", "do it now", "fix it", "add it", "implement that", "ejecuta el plan", "run it"} {
		t.Run(text, func(t *testing.T) {
			plan, err := (Planner{Config: cfg}).Build(Request{
				Text: text, Repository: "repo", BudgetUSD: 10,
				StandingEffects: []contract.Effect{contract.EffectWrite},
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Resolution != ResolutionNeedsContext || plan.ResolutionReason != ResolutionReasonMissingAcceptedPlan || plan.Valid ||
				len(plan.Workflow.Steps) != 0 || len(plan.Effects) != 0 {
				t.Fatalf("continuation plan = %+v, want non-executable needs_context result", plan)
			}
			if plan.Intent != "" || plan.Workflow.Task != text {
				t.Fatalf("ambiguous continuation was expanded: intent=%q task=%q", plan.Intent, plan.Workflow.Task)
			}
		})
	}
}

func TestBuildUsesAcceptedPlanContextWithoutWideningFilesOrEffects(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Model.Implement, cfg.Model.Review, cfg.Model.Audit = "sonnet", "sonnet", "claude-opus-5"
	for _, name := range []string{"implement", "review", "audit"} {
		typeDef := config.AgentType{Spec: contract.AgentTypeSpec{Name: name, Kind: contract.AgentSpecialized,
			Result: []contract.Field{{Name: "result", Type: contract.TypeString, Required: true}}},
			Effects: []contract.Effect{contract.EffectRead}}
		if name == "implement" {
			typeDef.Effects = append(typeDef.Effects, contract.EffectWrite)
		} else {
			typeDef.Pool, typeDef.ReadsSubject = config.PoolReview, true
		}
		cfg.Agents = append(cfg.Agents, typeDef)
	}

	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "fix it now", Files: []string{"internal/trips/search.go", "outside.go"}, BudgetUSD: 20,
		StandingEffects: []contract.Effect{contract.EffectWrite},
		Context: &IntentContext{Version: 1, Repository: "repo", AcceptedPlanID: "plan-7", AcceptedPlanRevision: "r3", AcceptedPlanCurrent: true,
			ActiveObjective: "añadir búsqueda de viajes", ScopeFiles: []string{"internal/trips/search.go"},
			Constraints: []string{"mantener compatibilidad"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid || plan.Resolution != ResolutionResolved || !plan.ContextUsed || plan.Intent != KindChange {
		t.Fatalf("contextual continuation = valid:%t resolution:%s context:%t intent:%s reasons:%+v",
			plan.Valid, plan.Resolution, plan.ContextUsed, plan.Intent, plan.Reasons)
	}
	if plan.Text != "fix it now" || !slices.Contains(plan.Repositories, "repo") {
		t.Fatalf("current request/repository = %q/%v", plan.Text, plan.Repositories)
	}
	if len(plan.Workflow.Steps) == 0 {
		t.Fatal("contextual continuation produced no workflow")
	}
	var implementation *workflow.Step
	for i := range plan.Workflow.Steps {
		step := &plan.Workflow.Steps[i]
		if step.TypeName == "implement" {
			implementation = step
		}
	}
	if implementation == nil {
		t.Fatal("accepted change context did not plan an implementation step")
	}
	if len(implementation.Task.Files) != 1 || implementation.Task.Files[0] != "internal/trips/search.go" {
		t.Fatalf("implementation files = %v, want the accepted scope only", implementation.Task.Files)
	}
	if !slices.Contains(implementation.Permission.Effects, contract.EffectWrite) {
		t.Fatalf("implementation effects = %v, want the independently granted standing write", implementation.Permission.Effects)
	}
}

func TestBuildRejectsMismatchedRepositoryAndDisjointScopeContext(t *testing.T) {
	context := &IntentContext{Version: 1, Repository: "repo", AcceptedPlanID: "plan-1", AcceptedPlanRevision: "r1", AcceptedPlanCurrent: true,
		ActiveObjective: "add search", ScopeFiles: []string{"internal/search.go"}}
	for name, request := range map[string]Request{
		"repository mismatch": {Text: "hazlo", Repository: "other", Context: context, BudgetUSD: 10},
		"disjoint files":      {Text: "hazlo", Repository: "repo", Files: []string{"internal/login.go"}, Context: context, BudgetUSD: 10},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := fixtureConfig("repo", "other")
			plan, err := (Planner{Config: cfg}).Build(request)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Resolution != ResolutionNeedsContext || plan.Valid || len(plan.Workflow.Steps) != 0 {
				t.Fatalf("plan = resolution:%s reason:%s valid:%t steps:%d", plan.Resolution, plan.ResolutionReason, plan.Valid, len(plan.Workflow.Steps))
			}
		})
	}
}

func TestBuildRequiresRepositoryBindingForAcceptedPlanContinuation(t *testing.T) {
	plan, err := (Planner{Config: fixtureConfig("repo")}).Build(Request{
		Text: "do it", Repository: "repo", BudgetUSD: 10,
		Context: &IntentContext{Version: 1, AcceptedPlanID: "plan-4", AcceptedPlanRevision: "r2", AcceptedPlanCurrent: true, ActiveObjective: "add search"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Resolution != ResolutionNeedsContext || plan.ResolutionReason != ResolutionReasonUnboundContinuation || len(plan.Workflow.Steps) != 0 {
		t.Fatalf("unbound plan context = resolution:%s reason:%s steps:%d", plan.Resolution, plan.ResolutionReason, len(plan.Workflow.Steps))
	}
}

func TestBuildRequiresCallerToVerifyAcceptedPlanRevisionIsCurrent(t *testing.T) {
	plan, err := (Planner{Config: fixtureConfig("repo")}).Build(Request{
		Text: "do it", Repository: "repo", BudgetUSD: 10,
		Context: &IntentContext{Version: 1, Repository: "repo", AcceptedPlanID: "plan-4", AcceptedPlanRevision: "r2",
			ActiveObjective: "add search"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Resolution != ResolutionNeedsContext || plan.ResolutionReason != ResolutionReasonPlanFreshnessUnverified || plan.Valid ||
		len(plan.Workflow.Steps) != 0 || len(plan.Effects) != 0 {
		t.Fatalf("unverified plan context = resolution:%s reason:%s valid:%t effects:%v steps:%d",
			plan.Resolution, plan.ResolutionReason, plan.Valid, plan.Effects, len(plan.Workflow.Steps))
	}
}

func TestAcceptedPlanContextDoesNotGrantWriteEffect(t *testing.T) {
	cfg := fixtureConfig("repo")
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "hazlo", BudgetUSD: 10,
		Context: &IntentContext{Version: 1, Repository: "repo", AcceptedPlanID: "plan-3", AcceptedPlanRevision: "r2", AcceptedPlanCurrent: true,
			ActiveObjective: "añadir búsqueda", ScopeFiles: []string{"internal/search.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != KindChange || slices.Contains(plan.Effects, contract.EffectWrite) {
		t.Fatalf("contextual intent/effects = %s/%v; context must not add write", plan.Intent, plan.Effects)
	}
	for _, step := range plan.Workflow.Steps {
		if step.TypeName == "implement" || slices.Contains(step.Permission.Effects, contract.EffectWrite) {
			t.Fatalf("context granted an implementation effect: %+v", step)
		}
	}
}

func TestBuildDoesNotTurnNegatedChangeRequestIntoImplementationOnWriteFloor(t *testing.T) {
	cfg := fixtureConfig("repo")
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "Do not make changes yet. Tell me how you would add Laya.", Repository: "repo", BudgetUSD: 10,
		StandingEffects: []contract.Effect{contract.EffectWrite},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != KindPlan || !plan.Valid {
		t.Fatalf("intent/valid = %s/%t, want a valid plan-only workflow", plan.Intent, plan.Valid)
	}
	for _, step := range plan.Workflow.Steps {
		if step.TypeName == "implement" {
			t.Fatalf("negated plan request acquired implementation step: %+v", step)
		}
	}
}

func TestBuildDoesNotTurnFutureChangeDiscussionIntoImplementationOnWriteFloor(t *testing.T) {
	plan, err := (Planner{Config: fixtureConfig("repo")}).Build(Request{
		Text: "We will migrate the database next week.", Repository: "repo", BudgetUSD: 10,
		StandingEffects: []contract.Effect{contract.EffectWrite},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Intent != KindPlan || !plan.Valid {
		t.Fatalf("future intent/valid = %s/%t, want a valid plan-only workflow", plan.Intent, plan.Valid)
	}
	for _, step := range plan.Workflow.Steps {
		if step.TypeName == "implement" {
			t.Fatalf("future discussion acquired implementation step: %+v", step)
		}
	}
}

// The plan a person reads and confirms is the plan that runs. plan.Effects is
// what --confirm shows and what the operator agrees to; the step permissions
// are what the workflow may actually do, and they were built from the
// commission's own effects alone -- the standing grant reached the printed
// list and nothing else, so a chat on a floor that allows writing was stopped
// for confirmation over a permission its steps never received.
func TestStandingEffectsReachTheStepsAndNotOnlyThePrintedPlan(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Model.Implement, cfg.Model.Review, cfg.Model.Audit = "luna", "sol", "astra"
	for _, name := range []string{"implement", "review", "audit"} {
		typeDef := config.AgentType{Spec: contract.AgentTypeSpec{Name: name, Kind: contract.AgentSpecialized,
			Result: []contract.Field{{Name: "result", Type: contract.TypeString, Required: true}}},
			Effects: []contract.Effect{contract.EffectRead}}
		if name == "implement" {
			typeDef.Effects = append(typeDef.Effects, contract.EffectWrite)
		} else {
			typeDef.Pool, typeDef.ReadsSubject = config.PoolReview, true
		}
		cfg.Agents = append(cfg.Agents, typeDef)
	}

	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "implementar el cambio", Repository: "repo", BudgetUSD: 3,
		StandingEffects: []contract.Effect{contract.EffectWrite},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid {
		t.Fatalf("plan invalid: %+v", plan.Reasons)
	}
	if !slices.Contains(plan.Effects, contract.EffectWrite) {
		t.Fatalf("plan effects = %v, want the standing write the operator granted", plan.Effects)
	}
	implemented := 0
	for _, step := range plan.Workflow.Steps {
		if !strings.HasPrefix(step.ID, "implement-") {
			continue
		}
		implemented++
		if !slices.Contains(step.Permission.Effects, contract.EffectWrite) {
			t.Errorf("step %s carries %v, but the plan promised %v",
				step.ID, step.Permission.Effects, plan.Effects)
		}
	}
	if implemented == 0 {
		t.Fatal("no implementation step was planned, so this proves nothing")
	}
	for _, step := range plan.Workflow.Steps {
		if strings.HasPrefix(step.ID, "explore-") && slices.Contains(step.Permission.Effects, contract.EffectWrite) {
			t.Fatal("exploration received the implementation write grant")
		}
	}
	// And never the other way round: a step may not carry an effect the plan
	// did not print, because the printed list is what was agreed to.
	for _, step := range plan.Workflow.Steps {
		for _, effect := range step.Permission.Effects {
			if effect != contract.EffectRead && !slices.Contains(plan.Effects, effect) {
				t.Errorf("step %s carries %s, which the plan never showed anybody", step.ID, effect)
			}
		}
	}
}
