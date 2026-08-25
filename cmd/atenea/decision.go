package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Tutitoos/atenea/internal/decision"
	"github.com/Tutitoos/atenea/internal/floor"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// cmdDecide is the safe front door to the decision layer. It always builds a
// complete, explainable plan first; execution is an explicit second choice so
// a caller can inspect model, tool, provider, permission and workflow choices
// before anything spends money or starts a process.
func cmdDecide(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return contract.Fail(contract.FailureInvalidInput,
			`decide needs the commission first, e.g. atenea decide "find the auth flow"`)
	}
	text, args := strings.TrimSpace(args[0]), args[1:]
	var repository string
	files := newStringList("file")
	var allow effectList
	var prefer string
	var tool string
	var budget float64
	var jsonOut bool
	var run bool
	var confirm bool
	var trace bool
	var tracePath string
	flags := flag.NewFlagSet("decide", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&repository, "repo", "", "repository id (default: every declared repository)")
	flags.Var(&files, "file", "file already named by the request; repeatable and enables the cheaper reader agent")
	flags.Var(&allow, "allow", "effect beyond reading to grant this commission; repeat for several")
	flags.StringVar(&prefer, "prefer", "", "one-call implementation preference")
	flags.StringVar(&tool, "tool", "", "explicit raw MCP tool, e.g. raw.semgrep.semgrep_scan")
	flags.Float64Var(&budget, "budget", 0, "what this commission may spend in usd (default: the settings file)")
	flags.BoolVar(&jsonOut, "json", false, "print the complete decision plan as json")
	flags.BoolVar(&run, "run", false, "execute the compiled plan after deciding it")
	flags.BoolVar(&confirm, "confirm", false, "require an interactive TTY confirmation before --run")
	flags.BoolVar(&trace, "trace", false, "print every decision and workflow step")
	flags.StringVar(&tracePath, "traces", "", "workflow state database (default: the workflow path)")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput, "unexpected argument %q after the commission", flags.Arg(0))
	}
	effects, err := allow.effects()
	if err != nil {
		return err
	}

	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()
	cfg := atenea.Settings()
	estimator := decision.BudgetEstimator(decision.DefaultBudgetEstimator{})
	if floors, floorErr := floor.Open(""); floorErr == nil {
		estimator = decision.MeasuredBudgetEstimator{Store: floors}
	}
	var historyStore *workflow.Store
	ranker := decision.ModelRanker(decision.StaticModelRanker{})
	if store, storeErr := workflow.Open(context.Background(), tracePath); storeErr == nil {
		historyStore = store
		ranker = decision.AdaptiveModelRanker{History: &workflowDecisionHistory{store: store}}
	}
	planner := decision.Planner{Config: cfg, Selector: atenea, Estimator: estimator, Ranker: ranker}
	plan, err := planner.Build(decision.Request{
		Text: text, Repository: repository, Files: files.values, BudgetUSD: budget,
		Effects: effects, StandingEffects: cfg.Orchestrator.StandingEffects, Prefer: prefer, Tool: tool,
	})
	if err != nil {
		if historyStore != nil {
			_ = historyStore.Close()
		}
		return err
	}
	if historyStore != nil {
		_ = historyStore.Close()
	}
	if jsonOut {
		if err := printDecisionJSON(out, plan); err != nil {
			return err
		}
	} else {
		printDecisionPlan(out, plan, trace)
	}
	if !run {
		if !plan.Valid {
			return contract.Fail(contract.FailureInvalidInput, "decision plan is not executable")
		}
		return nil
	}
	if !plan.Valid {
		return contract.Fail(contract.FailureInvalidInput, "refusing to run an invalid decision plan")
	}
	if run && requiresDecisionConfirmation(plan, tool) && !confirm {
		return contract.Fail(contract.FailurePermissionDenied,
			"--run requires --confirm for write/external effects or an explicit raw MCP tool")
	}
	if confirm {
		if err := confirmTTY(out, "decide --run "+text, plan.Workflow.GrantUSD, effects); err != nil {
			return err
		}
	}
	ctx, stop := interruptible()
	defer stop()
	for _, repository := range plan.Repositories {
		graph := graphForRepository(plan.Workflow, repository)
		engine, closeEngine, err := workflow.Serve(ctx, cfg, tracePath, repository, "cli", out)
		if err != nil {
			return err
		}
		result, runErr := engine.Start(ctx, graph)
		closeEngine()
		if result.ID != "" {
			fmt.Fprintf(out, "repository %s\n", repository)
			printRun(out, result)
		}
		if runErr != nil {
			return runErr
		}
	}
	return nil
}

// workflowDecisionHistory adapts persisted workflow model observations to the
// decision package without making the planner depend on a database.
type workflowDecisionHistory struct {
	store *workflow.Store
	table map[string]workflow.ModelCostTable
}

func (h *workflowDecisionHistory) Performance(repository, role, model string) (decision.ModelPerformance, bool) {
	if h.store == nil {
		return decision.ModelPerformance{}, false
	}
	if h.table == nil {
		h.table = make(map[string]workflow.ModelCostTable)
	}
	table, ok := h.table[repository]
	if !ok {
		var err error
		table, err = h.store.CostByModel(context.Background(), repository)
		if err != nil {
			return decision.ModelPerformance{}, false
		}
		h.table[repository] = table
	}
	seen, ok := table.Performance(role, model)
	if !ok {
		return decision.ModelPerformance{}, false
	}
	return decision.ModelPerformance{Samples: seen.N, MedianUSD: seen.MedianUSD,
		MedianLatency: seen.MedianDuration.Milliseconds()}, true
}

func requiresDecisionConfirmation(plan decision.Plan, requestedTool string) bool {
	if requestedTool != "" {
		return true
	}
	for _, effect := range plan.Effects {
		if effect == contract.EffectWrite || effect == contract.EffectExternal {
			return true
		}
	}
	return false
}

func graphForRepository(graph workflow.Graph, repository string) workflow.Graph {
	allowed := make(map[string]bool)
	for _, step := range graph.Steps {
		if step.ID == "explore-"+repository || step.ID == "plan-"+repository {
			allowed[step.ID] = true
		}
	}
	out := graph.Clone()
	out.Steps = out.Steps[:0]
	out.GrantUSD = 0
	for _, step := range graph.Steps {
		if !allowed[step.ID] {
			continue
		}
		step.Needs = filterStepIDs(step.Needs, allowed)
		if step.Subject != "" && !allowed[step.Subject] {
			step.Subject = ""
		}
		out.Steps = append(out.Steps, step)
		out.GrantUSD += step.Permission.BudgetUSD
	}
	return out
}

func filterStepIDs(ids []string, allowed map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if allowed[id] {
			out = append(out, id)
		}
	}
	return out
}

func printDecisionJSON(out io.Writer, plan decision.Plan) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(plan)
}

func printDecisionPlan(out io.Writer, plan decision.Plan, trace bool) {
	fmt.Fprintf(out, "intent      %s\n", plan.Intent)
	fmt.Fprintf(out, "agent       %s\n", orDash(plan.Agent))
	fmt.Fprintf(out, "repositories %s\n", orDash(strings.Join(plan.Repositories, ", ")))
	fmt.Fprintf(out, "effects     %s\n", orDash(strings.Join(effectNames(plan.Effects), ", ")))
	fmt.Fprintf(out, "valid       %t\n", plan.Valid)

	fmt.Fprintln(out, "\nmodels")
	for _, model := range plan.Models {
		status := "ready"
		if !model.Available {
			status = "missing"
		}
		fallback := ""
		if len(model.Fallbacks) > 0 {
			fallback = " fallback=" + strings.Join(model.Fallbacks, ",")
		}
		fmt.Fprintf(out, "  %-10s %-16s %-10s %s%s (%s)\n", model.Role, orDash(model.Name), status, orDash(model.Backend), fallback, model.Reason)
	}
	fmt.Fprintln(out, "\ntools")
	for _, tool := range plan.Tools {
		selected := "available"
		if !tool.Selected {
			selected = "not selected"
		}
		fmt.Fprintf(out, "  %-30s %-10s %-12s %s\n", tool.ID, tool.Kind, selected, tool.Reason)
	}
	fmt.Fprintln(out, "\ncapabilities")
	for _, capability := range plan.Capabilities {
		provider := orDash(capability.Chosen)
		if len(capability.Providers) > 0 && capability.Chosen == "" {
			provider = "candidates: " + strings.Join(capability.Providers, ", ")
		}
		if capability.Unavailable {
			provider = "unavailable"
		}
		fmt.Fprintf(out, "  %-28s %-18s %s\n", capability.Repository+":"+capability.ID, provider, capability.Reason)
	}
	fundingStatus := "sufficient"
	if !plan.Budget.Sufficient {
		fundingStatus = "insufficient"
	}
	fmt.Fprintf(out, "\nbudget\n  granted=$%.2f required~=$%.2f minimum~=$%.2f margin=$%.2f %s\n",
		plan.Budget.GrantedUSD, plan.Budget.RequiredUSD, plan.Budget.MinimumUSD,
		plan.Budget.MarginUSD, fundingStatus)
	fmt.Fprintln(out, "\nworkflow")
	for _, step := range plan.Workflow.Steps {
		needs := strings.Join(step.Needs, ",")
		if step.Subject != "" {
			needs = needs + " subject=" + step.Subject
		}
		fmt.Fprintf(out, "  %-24s %-10s budget=$%.2f forecast~=$%.2f min~=$%.2f effects=%s needs=%s\n",
			step.ID, step.TypeName, step.Permission.BudgetUSD, step.BudgetEstimateUSD,
			step.BudgetMinimumUSD, orDash(strings.Join(effectNames(step.Permission.Effects), ",")), orDash(needs))
	}
	if trace {
		fmt.Fprintln(out, "\nreasons")
		for _, reason := range plan.Reasons {
			fmt.Fprintf(out, "  %-10s %s\n", reason.Stage, reason.Message)
		}
	}
}

func effectNames(effects []contract.Effect) []string {
	out := make([]string, 0, len(effects))
	for _, effect := range effects {
		out = append(out, effect.String())
	}
	return out
}
