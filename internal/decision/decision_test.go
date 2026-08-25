package decision

import (
	"slices"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/selector"
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
	if len(plan.Workflow.Steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(plan.Workflow.Steps))
	}
	var total float64
	for _, step := range plan.Workflow.Steps {
		total += step.Permission.BudgetUSD
	}
	if total > 10+1e-9 {
		t.Fatalf("step shares = %.12f, past grant", total)
	}
	if plan.Workflow.Steps[3].Subject != "explore-two" {
		t.Fatalf("second plan subject = %q, want explore-two", plan.Workflow.Steps[3].Subject)
	}
	if plan.Budget.RequiredUSD <= 0 || !plan.Budget.Sufficient {
		t.Fatalf("budget = %+v, want a sufficient forecast", plan.Budget)
	}
	if plan.Workflow.Steps[0].BudgetEstimateUSD == plan.Workflow.Steps[1].BudgetEstimateUSD {
		t.Fatal("explore and plan received the same forecast; model-aware allocation was not applied")
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

func TestRoutesCarryDeclaredModelFallbacks(t *testing.T) {
	cfg := fixtureConfig("repo")
	cfg.Model.ExploreFallbacks = []string{"claude-haiku-5"}
	cfg.Model.PlanFallbacks = []string{"claude-sonnet-5"}
	plan, err := (Planner{Config: cfg}).Build(Request{
		Text: "preparar un plan", Repository: "repo", BudgetUSD: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Workflow.Steps[0].Route.Fallbacks; len(got) != 1 || got[0] != "claude-haiku-5" {
		t.Fatalf("explore fallbacks = %v", got)
	}
	if got := plan.Workflow.Steps[1].Route; got.Model != "claude-opus-5" || len(got.Fallbacks) != 0 {
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
	route := plan.Workflow.Steps[0].Route
	if route == nil || route.Model != "claude-haiku-4-5" || len(route.Fallbacks) != 1 || route.Fallbacks[0] != "claude-sonnet-5" {
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
	route := plan.Workflow.Steps[1].Route
	if route == nil || route.Model != "claude-opus-5" || len(route.Fallbacks) != 0 {
		t.Fatalf("auto plan route = %+v", route)
	}
	if !strings.Contains(plan.Models[1].Reason, "auto") {
		t.Fatalf("auto plan reason = %q", plan.Models[1].Reason)
	}
}

func TestAutoPlanUsesOpusThenHighReasoningOpenCodeFallbacks(t *testing.T) {
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
	route := plan.Workflow.Steps[1].Route
	if route == nil || route.Model != "anthropic/claude-opus-5" || len(route.Fallbacks) != 2 ||
		route.Fallbacks[0] != "openai/gpt-5.6-sol" || route.Fallbacks[1] != "openai/gpt-5.6-luna" {
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
		"fix the login bug":            KindChange,
		"fixing the login bug":         KindChange,
		"añade un campo al formulario": KindChange,
		"how would you split this":     KindPlan,
		"diseña el flujo de pagos":     KindPlan,
		"buscar autenticación":         KindSearch,
		"explain how the router works": KindUnderstand,
	} {
		if got := infer(text); got != want {
			t.Errorf("infer(%q) = %s, want %s", text, got, want)
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
	// The agent types have to declare write for it to be inside their
	// ceiling; a standing grant wider than the type is narrowed instead, as
	// the reader shape above shows.
	for i := range cfg.Agents {
		cfg.Agents[i].Effects = append(cfg.Agents[i].Effects, contract.EffectWrite)
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
	explored := 0
	for _, step := range plan.Workflow.Steps {
		if !strings.HasPrefix(step.ID, "explore-") {
			continue
		}
		explored++
		if !slices.Contains(step.Permission.Effects, contract.EffectWrite) {
			t.Errorf("step %s carries %v, but the plan promised %v",
				step.ID, step.Permission.Effects, plan.Effects)
		}
	}
	if explored == 0 {
		t.Fatal("no explore step was planned, so this proves nothing")
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
