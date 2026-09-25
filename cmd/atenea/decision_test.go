package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/decision"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestCmdDecideValidatesCommissionBeforeLoadingSettings(t *testing.T) {
	if err := cmdDecide("", nil, &bytes.Buffer{}); err == nil {
		t.Fatal("cmdDecide accepted an empty commission")
	}
}

func TestCmdDecideBuildsJSONDryRunFromSettings(t *testing.T) {
	settingsPath := settingsFile(t)
	body, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte("\n[model]\nbackend = \"claude\"\nbinary = \"claude\"\nexplore = \"sonnet\"\nplan = \"claude-opus-5\"\n")...)
	if err := os.WriteFile(settingsPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(t.TempDir(), "workflow.db")
	var out bytes.Buffer
	err = cmdDecide(settingsPath, []string{
		"find authentication", "--repo", "api", "--traces", tracePath, "--budget", "5", "--json",
	}, &out)
	if err != nil {
		t.Fatalf("cmdDecide dry run: %v; output=%s", err, out.String())
	}
	if !strings.Contains(out.String(), `"intent": "search"`) || !strings.Contains(out.String(), `"valid": true`) {
		t.Fatalf("decision json = %q, want a valid search plan", out.String())
	}
}

func TestCmdDecideRejectsMalformedFlagsAndTrailingArguments(t *testing.T) {
	settingsPath := settingsFile(t)
	for name, args := range map[string][]string{
		"malformed flag":    {"find authentication", "--budget=not-money"},
		"trailing argument": {"find authentication", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := cmdDecide(settingsPath, args, &bytes.Buffer{}); err == nil {
				t.Fatalf("cmdDecide accepted %v", args)
			}
		})
	}
}

func TestParseDecisionContextRequiresOneStrictVersionedObject(t *testing.T) {
	valid := `{"version":1,"repository":"api","accepted_plan_id":"plan-2","accepted_plan_revision":"r4","accepted_plan_current":true,"active_objective":"add search","scope_files":["internal/search.go"]}`
	context, err := parseDecisionContext(valid)
	if err != nil || context == nil || context.Version != 1 || context.Repository != "api" {
		t.Fatalf("parsed context = %+v, err=%v", context, err)
	}
	for _, raw := range []string{
		`{"version":1} {"version":1}`,
		`{"version":1,"unknown":true}`,
		`null`,
		`{"repository":"api"}`,
	} {
		if _, err := parseDecisionContext(raw); err == nil {
			t.Errorf("parseDecisionContext(%q) succeeded; want strict JSON/version rejection", raw)
		}
	}
}

func TestCmdDecideRequiresContextForContinuationAndUsesContextRepository(t *testing.T) {
	settingsPath := settingsFile(t)
	body, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte("\n[model]\nbackend = \"claude\"\nbinary = \"claude\"\nexplore = \"sonnet\"\nplan = \"claude-opus-5\"\n")...)
	if err := os.WriteFile(settingsPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	var unresolvedOut bytes.Buffer
	err = cmdDecide(settingsPath, []string{
		"hazlo", "--budget", "5", "--traces", filepath.Join(t.TempDir(), "unresolved.db"), "--json",
	}, &unresolvedOut)
	if err == nil {
		t.Fatal("cmdDecide accepted a bare continuation")
	}
	var unresolved decision.Plan
	if err := json.Unmarshal(unresolvedOut.Bytes(), &unresolved); err != nil {
		t.Fatalf("unresolved JSON = %q, err=%v", unresolvedOut.String(), err)
	}
	if unresolved.Resolution != decision.ResolutionNeedsContext || unresolved.ResolutionReason != decision.ResolutionReasonMissingAcceptedPlan || len(unresolved.Workflow.Steps) != 0 {
		t.Fatalf("unresolved plan = %+v", unresolved)
	}

	unverifiedContextJSON := `{"version":1,"repository":"api","accepted_plan_id":"plan-4","accepted_plan_revision":"r2","active_objective":"añadir búsqueda"}`
	var unverifiedOut bytes.Buffer
	err = cmdDecide(settingsPath, []string{
		"hazlo", "--budget", "5", "--traces", filepath.Join(t.TempDir(), "unverified.db"), "--json", "--decision-context", unverifiedContextJSON,
	}, &unverifiedOut)
	if err == nil {
		t.Fatal("cmdDecide accepted a continuation with an unverified plan revision")
	}
	var unverified decision.Plan
	if err := json.Unmarshal(unverifiedOut.Bytes(), &unverified); err != nil {
		t.Fatalf("unverified JSON = %q, err=%v", unverifiedOut.String(), err)
	}
	if unverified.ResolutionReason != decision.ResolutionReasonPlanFreshnessUnverified || len(unverified.Workflow.Steps) != 0 {
		t.Fatalf("unverified decision = %+v", unverified)
	}

	contextJSON := `{"version":1,"repository":"api","accepted_plan_id":"plan-4","accepted_plan_revision":"r2","accepted_plan_current":true,"active_objective":"añadir búsqueda"}`
	var resolvedOut bytes.Buffer
	err = cmdDecide(settingsPath, []string{
		"hazlo", "--budget", "5", "--traces", filepath.Join(t.TempDir(), "resolved.db"), "--json", "--decision-context", contextJSON,
	}, &resolvedOut)
	if err != nil {
		t.Fatalf("cmdDecide contextual dry run: %v; output=%s", err, resolvedOut.String())
	}
	var resolved decision.Plan
	if err := json.Unmarshal(resolvedOut.Bytes(), &resolved); err != nil {
		t.Fatalf("resolved JSON = %q, err=%v", resolvedOut.String(), err)
	}
	if resolved.Resolution != decision.ResolutionResolved || !resolved.ContextUsed || resolved.Intent != decision.KindChange || len(resolved.Repositories) != 1 || resolved.Repositories[0] != "api" {
		t.Fatalf("resolved plan = %+v", resolved)
	}
}

func TestGraphForRepositoryKeepsOnlyItsIsolatedSubgraph(t *testing.T) {
	graph := workflow.Graph{Task: "inspect", GrantUSD: 4, Steps: []workflow.Step{
		{ID: "explore-api", TypeName: "explore", Permission: contract.Permission{BudgetUSD: 1}},
		{ID: "plan-api", TypeName: "plan", Needs: []string{"explore-api"}, Subject: "explore-api", Permission: contract.Permission{BudgetUSD: 1}},
		{ID: "explore-web", TypeName: "explore", Permission: contract.Permission{BudgetUSD: 1}},
		{ID: "plan-web", TypeName: "plan", Needs: []string{"explore-web"}, Subject: "explore-web", Permission: contract.Permission{BudgetUSD: 1}},
	}}

	got := graphForRepository(graph, "api")
	if len(got.Steps) != 2 || got.Steps[1].Needs[0] != "explore-api" || got.Steps[1].Subject != "explore-api" {
		t.Fatalf("api graph = %+v, want its dependent pair", got)
	}
	if got.GrantUSD != 2 {
		t.Fatalf("api grant = %.2f, want 2", got.GrantUSD)
	}
}

func TestGraphForRepositoryDropsForeignDependenciesAndSubjects(t *testing.T) {
	graph := workflow.Graph{GrantUSD: 3, Steps: []workflow.Step{
		{ID: "explore-api", TypeName: "explore", Permission: contract.Permission{BudgetUSD: 1}},
		{ID: "plan-api", TypeName: "plan", Needs: []string{"explore-api", "explore-web", "missing"}, Subject: "explore-web", Permission: contract.Permission{BudgetUSD: 1}},
	}}

	got := graphForRepository(graph, "api")
	if len(got.Steps) != 2 || len(got.Steps[1].Needs) != 1 || got.Steps[1].Needs[0] != "explore-api" {
		t.Fatalf("filtered graph = %+v, want only the local dependency", got)
	}
	if got.Steps[1].Subject != "" || got.GrantUSD != 2 {
		t.Fatalf("filtered graph subject/grant = %q/%.2f, want empty/2", got.Steps[1].Subject, got.GrantUSD)
	}
}

func TestDecisionPresentationAndConfirmationGuards(t *testing.T) {
	plan := decision.Plan{
		Intent:       decision.KindPlan,
		Agent:        "plan",
		Repositories: []string{"repo"},
		Effects:      []contract.Effect{contract.EffectRead, contract.EffectWrite},
		Models:       []decision.ModelChoice{{Role: "plan", Backend: "claude", Name: "opus", Fallbacks: []string{"fallback"}, Reason: "missing"}},
		Tools:        []decision.ToolChoice{{ID: "raw.docs.query", Kind: "raw", Selected: false, Reason: "not selected"}},
		Capabilities: []decision.CapabilityChoice{
			{ID: "code.search", Repository: "repo", Providers: []string{"ripgrep"}, Reason: "candidate"},
			{ID: "symbol.definition", Repository: "repo", Unavailable: true, Reason: "offline"},
		},
		Budget: decision.BudgetSummary{GrantedUSD: 1, RequiredUSD: 2, MinimumUSD: 1, MarginUSD: -1},
		Workflow: workflow.Graph{Steps: []workflow.Step{{
			ID: "explore-repo", TypeName: "explore", Permission: contract.Permission{BudgetUSD: 1, Effects: []contract.Effect{contract.EffectRead}},
		}}},
		Reasons: []decision.Reason{{Stage: "intent", Message: "classified"}},
	}

	var out bytes.Buffer
	printDecisionPlan(&out, plan, true)
	if !strings.Contains(out.String(), "unavailable") || !strings.Contains(out.String(), "reasons") || !strings.Contains(out.String(), "insufficient") {
		t.Fatalf("decision output = %q, want complete diagnostic sections", out.String())
	}
	var jsonOut bytes.Buffer
	if err := printDecisionJSON(&jsonOut, plan); err != nil || !strings.Contains(jsonOut.String(), `"intent": "plan"`) {
		t.Fatalf("decision json = %q, err=%v", jsonOut.String(), err)
	}

	if requiresDecisionConfirmation(decision.Plan{}, "") {
		t.Fatal("empty plan should not require confirmation")
	}
	if !requiresDecisionConfirmation(plan, "") || !requiresDecisionConfirmation(decision.Plan{}, "raw.docs.query") {
		t.Fatal("write effects and raw tools should require confirmation")
	}
	if got := effectNames([]contract.Effect{contract.EffectRead, contract.EffectWrite}); len(got) != 2 || got[0] != "read" {
		t.Fatalf("effect names = %v", got)
	}
}

func TestCmdDecideAcceptPlanAndRejectStaleReference(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	settingsPath := settingsFile(t)
	body, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `path = "/srv/api"`, fmt.Sprintf("path = %q", root), 1) + "\n[model]\nbackend = \"claude\"\nbinary = \"claude\"\nexplore = \"sonnet\"\nplan = \"claude-opus-5\"\n")
	if err := os.WriteFile(settingsPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cmdDecide(settingsPath, []string{"accept-plan", "test", "--decision-context", `{"version":1,"repository":"api","active_objective":"Improve validation","scope_files":["handler.go"]}`}, &out); err != nil {
		t.Fatal(err)
	}
	var ref decision.AcceptedPlanReference
	if err := json.Unmarshal(out.Bytes(), &ref); err != nil {
		t.Fatal(err)
	}
	args := []string{"hazlo", "--repo", "api", "--accepted-plan", ref.ID, "--accepted-revision", ref.Revision, "--budget", "5", "--json", "--traces", filepath.Join(t.TempDir(), "workflow.db")}
	out.Reset()
	if err := cmdDecide(settingsPath, args, &out); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	var plan decision.Plan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Resolution != decision.ResolutionResolved || plan.Intent != decision.KindChange || !plan.Valid {
		t.Fatalf("plan: %+v", plan)
	}
	if err := os.WriteFile(filepath.Join(root, "handler.go"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := cmdDecide(settingsPath, args, &out); err == nil {
		t.Fatal("stale CLI reference succeeded")
	}
	if !strings.Contains(out.String(), `"resolution": "needs_context"`) {
		t.Fatalf("stale output: %s", out.String())
	}
}
