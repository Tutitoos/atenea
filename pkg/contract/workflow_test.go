package contract_test

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func readOnly(task string) contract.Permission {
	return contract.Permission{Task: task, Effects: []contract.Effect{contract.EffectRead}}
}

func step(id string, needs ...string) contract.Step {
	return contract.Step{
		ID:         id,
		Capability: "code.search",
		Repository: "api",
		Payload:    map[string]any{"query": "login"},
		Needs:      needs,
		Permission: readOnly("add login"),
	}
}

func TestPermissionCoversOnlyWhatWasAuthorised(t *testing.T) {
	permission := readOnly("find every TODO")
	if !permission.Allows(contract.EffectRead) {
		t.Fatal("reading was granted and is not allowed")
	}
	// Writing and reaching outside are separate groups precisely so that a
	// read-only commission cannot quietly acquire them.
	if permission.Allows(contract.EffectWrite) {
		t.Fatal("writing was never granted")
	}
	if permission.Allows(contract.EffectExternal) {
		t.Fatal("reaching outside was never granted")
	}
}

// Grant is how a permission grows in layers: read, then whatever the
// standing grant adds, then whatever the caller asked for on top of that.
func TestGrantAddsWithoutDroppingWhatWasAlreadyHeld(t *testing.T) {
	base := readOnly("find every TODO")
	granted := base.Grant([]contract.Effect{contract.EffectProcess})
	if !granted.Allows(contract.EffectRead) || !granted.Allows(contract.EffectProcess) {
		t.Fatalf("effects = %v, want read and process", granted.Effects)
	}
	// Grant returns a copy: the layer it was called on is untouched.
	if base.Allows(contract.EffectProcess) {
		t.Fatal("Grant mutated the permission it was called on")
	}
}

// The same effect can arrive from two layers -- the standing grant and
// --allow both naming process, say -- and must still be held once.
func TestGrantKeepsEachEffectOnce(t *testing.T) {
	base := readOnly("find every TODO").Grant([]contract.Effect{contract.EffectProcess})
	granted := base.Grant([]contract.Effect{contract.EffectProcess, contract.EffectRead})
	want := []contract.Effect{contract.EffectRead, contract.EffectProcess}
	if !slices.Equal(granted.Effects, want) {
		t.Fatalf("effects = %v, want %v", granted.Effects, want)
	}
}

// Two steps granted from the same base permission must not see each other's
// later grant, and the base itself must not acquire either child's.
func TestGrantDoesNotAliasTheOriginalSlice(t *testing.T) {
	base := readOnly("find every TODO")
	loose := base.Grant([]contract.Effect{contract.EffectExternal})
	tight := base.Grant([]contract.Effect{contract.EffectWrite})
	if !loose.Allows(contract.EffectExternal) {
		t.Error("the granted permission lost its own grant")
	}
	if tight.Allows(contract.EffectExternal) {
		t.Error("a permission acquired the reach granted to the one next to it")
	}
	if base.Allows(contract.EffectExternal) || base.Allows(contract.EffectWrite) {
		t.Error("the base permission acquired effects granted to its own children")
	}
}

func TestPermissionNeedsTheCommissionItCameFrom(t *testing.T) {
	if err := (contract.Permission{Effects: []contract.Effect{contract.EffectRead}}).Validate(); err == nil {
		t.Fatal("a permission with no commission behind it must be refused")
	}
}

// Money is a permission, and the question a paid far side asks is the same
// shape as the one it asks about effects: may I. Zero is a no.
func TestAPermissionWithNoMoneyIsNotFunded(t *testing.T) {
	grant := readOnly("find every TODO")
	if grant.Funded() {
		t.Error("a permission nobody put money on reads as funded")
	}
	grant.BudgetUSD = 0.25
	if !grant.Funded() {
		t.Error("a granted quarter reads as unfunded")
	}
}

// Reaching zero is the ordinary end of a grant: the commission spent what it
// had. It must stay valid, because every later step still carries it and a
// permission that failed validation would fail the step for the wrong reason.
func TestASpentGrantIsStillAValidPermission(t *testing.T) {
	grant := readOnly("find every TODO")
	grant.BudgetUSD = 0
	if err := grant.Validate(); err != nil {
		t.Errorf("a spent grant was refused: %v", err)
	}
}

// Below zero is not a spent grant, it is arithmetic that went wrong upstream
// -- and the one shape that would read as a license if it ever reached a far
// side, because "less than nothing" is not a ceiling anybody enforces.
func TestANegativeOrNonsenseGrantIsRefused(t *testing.T) {
	for name, usd := range map[string]float64{
		"negative": -0.01,
		"nan":      math.NaN(),
	} {
		t.Run(name, func(t *testing.T) {
			grant := readOnly("find every TODO")
			grant.BudgetUSD = usd
			if err := grant.Validate(); err == nil {
				t.Fatalf("a budget of %v was accepted", usd)
			}
		})
	}
}

// A step carries the share it was cut, and cloning a plan must not let two
// steps share one figure by accident.
func TestCloningAPermissionCarriesTheGrant(t *testing.T) {
	grant := readOnly("find every TODO")
	grant.BudgetUSD = 0.25
	clone := grant.Clone()
	clone.BudgetUSD = 9
	if grant.BudgetUSD != 0.25 {
		t.Errorf("the original moved to %v", grant.BudgetUSD)
	}
	if clone.Task != grant.Task {
		t.Error("the clone lost the commission it came from")
	}
}

func TestStepValidationRefusesMalformedNodes(t *testing.T) {
	cases := map[string]func(*contract.Step){
		"uppercase id":        func(s *contract.Step) { s.ID = "Explore" },
		"undotted capability": func(s *contract.Step) { s.Capability = "search" },
		"uppercase repo":      func(s *contract.Step) { s.Repository = "API" },
		"unstamped":           func(s *contract.Step) { s.Permission = contract.Permission{} },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			node := step("explore")
			break_(&node)
			if err := node.Validate(); err == nil {
				t.Fatal("expected the step to be refused")
			}
		})
	}
}

// The shape the orchestrator actually builds: a look per repository, then the
// work waiting on it. Everything in a wave is free to run at the same time.
func TestPlanGroupsIndependentStepsIntoOneWave(t *testing.T) {
	plan := contract.Plan{
		Task: "add login",
		Steps: []contract.Step{
			step("search-web", "explore-web"),
			step("explore-api"),
			step("search-api", "explore-api"),
			step("explore-web"),
		},
	}
	waves, err := plan.Layers()
	if err != nil {
		t.Fatalf("Layers: %v", err)
	}
	if len(waves) != 2 {
		t.Fatalf("waves = %d, want 2", len(waves))
	}
	if got := names(waves[0]); got != "explore-api, explore-web" {
		t.Errorf("first wave = %q, want the two looks", got)
	}
	if got := names(waves[1]); got != "search-api, search-web" {
		t.Errorf("second wave = %q, want the two searches", got)
	}
}

// The same plan has to produce the same waves every time, or a dispatch trace
// cannot be compared with the one from yesterday.
func TestPlanWavesAreDeterministic(t *testing.T) {
	plan := contract.Plan{
		Task:  "add login",
		Steps: []contract.Step{step("zeta"), step("alpha"), step("mike")},
	}
	first, err := plan.Layers()
	if err != nil {
		t.Fatalf("Layers: %v", err)
	}
	for range 20 {
		again, err := plan.Layers()
		if err != nil {
			t.Fatalf("Layers: %v", err)
		}
		if names(again[0]) != names(first[0]) {
			t.Fatalf("wave order drifted: %q then %q", names(first[0]), names(again[0]))
		}
	}
	if got := names(first[0]); got != "alpha, mike, zeta" {
		t.Errorf("wave = %q, want sorted", got)
	}
}

func TestPlanRefusesGraphsThatCannotDrain(t *testing.T) {
	cases := map[string]contract.Plan{
		"a cycle": {Task: "t", Steps: []contract.Step{
			step("one", "two"),
			step("two", "one"),
		}},
		"a self dependency": {Task: "t", Steps: []contract.Step{step("one", "one")}},
		"a dangling edge":   {Task: "t", Steps: []contract.Step{step("one", "ghost")}},
		"a duplicate id": {Task: "t", Steps: []contract.Step{
			step("one"),
			step("one"),
		}},
		"the same need twice": {Task: "t", Steps: []contract.Step{
			step("one"),
			step("two", "one", "one"),
		}},
		"no task":  {Steps: []contract.Step{step("one")}},
		"no steps": {Task: "t"},
	}
	for name, plan := range cases {
		t.Run(name, func(t *testing.T) {
			err := plan.Validate()
			if err == nil {
				t.Fatal("expected the plan to be refused")
			}
			if got := contract.KindOf(err); got != contract.FailureInvalidInput {
				t.Fatalf("kind = %v, want invalid_input", got)
			}
		})
	}
}

// A cycle has to name the steps caught in it, or the message sends the reader
// looking through the whole graph by hand.
func TestCycleErrorNamesTheStepsInvolved(t *testing.T) {
	plan := contract.Plan{Task: "t", Steps: []contract.Step{
		step("free"),
		step("one", "two"),
		step("two", "one"),
	}}
	err := plan.Validate()
	if err == nil {
		t.Fatal("expected a cycle to be refused")
	}
	if !strings.Contains(err.Error(), "one") || !strings.Contains(err.Error(), "two") {
		t.Fatalf("error does not name the cycle: %v", err)
	}
	if strings.Contains(err.Error(), "free") {
		t.Fatalf("error blames a step outside the cycle: %v", err)
	}
}

// Dispatching in phases is the whole reason the plan can be asked what is
// left: the work waits on a look that has already happened.
func TestLayersAfterTreatsFinishedStepsAsSatisfied(t *testing.T) {
	plan := contract.Plan{Task: "add login", Steps: []contract.Step{
		step("explore-api"),
		step("search-api", "explore-api"),
	}}
	waves, err := plan.LayersAfter([]string{"explore-api"})
	if err != nil {
		t.Fatalf("LayersAfter: %v", err)
	}
	if len(waves) != 1 {
		t.Fatalf("waves = %d, want only the outstanding one", len(waves))
	}
	if got := names(waves[0]); got != "search-api" {
		t.Fatalf("wave = %q, want search-api", got)
	}
}

func TestLayersAfterRefusesAStepFromAnotherPlan(t *testing.T) {
	plan := contract.Plan{Task: "t", Steps: []contract.Step{step("one")}}
	if _, err := plan.LayersAfter([]string{"ghost"}); err == nil {
		t.Fatal("a finished step that is not in the plan is a bug, not a no-op")
	}
}

// Hybrid planning: the first plan is light, and the rest is drawn once the
// look has said what the project looks like.
func TestAppendValidatesTheWholeGraph(t *testing.T) {
	light := contract.Plan{Task: "add login", Steps: []contract.Step{step("explore-api")}}

	full, err := light.Append(step("search-api", "explore-api"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(full.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(full.Steps))
	}
	if len(light.Steps) != 1 {
		t.Fatal("Append edited the plan it was called on")
	}
	if _, err := light.Append(step("search-api", "ghost")); err == nil {
		t.Fatal("appending a dangling edge has to be refused")
	}
}

func TestPlanCloneDoesNotSharePayloads(t *testing.T) {
	original := contract.Plan{Task: "t", Steps: []contract.Step{step("one", "zero")}}
	original.Steps = append(original.Steps, step("zero"))

	clone := original.Clone()
	clone.Steps[0].Payload["query"] = "changed"
	clone.Steps[0].Needs[0] = "elsewhere"
	clone.Steps[0].Permission.Effects[0] = contract.EffectWrite

	if original.Steps[0].Payload["query"] != "login" {
		t.Error("clone shared the payload map")
	}
	if original.Steps[0].Needs[0] != "zero" {
		t.Error("clone shared the needs slice")
	}
	if original.Steps[0].Permission.Effects[0] != contract.EffectRead {
		t.Error("clone shared the permission effects")
	}
}

func TestPlanStepLookup(t *testing.T) {
	plan := contract.Plan{Task: "t", Steps: []contract.Step{step("one")}}
	if _, ok := plan.Step("one"); !ok {
		t.Fatal("a registered step was not found")
	}
	if _, ok := plan.Step("ghost"); ok {
		t.Fatal("an unregistered step was found")
	}
}

func names(wave []contract.Step) string {
	out := make([]string, 0, len(wave))
	for _, step := range wave {
		out = append(out, step.ID)
	}
	return strings.Join(out, ", ")
}

// The comment on this check names "the one shape that would silently turn a
// ceiling into a license" and the condition then read `< 0 || IsNaN`, which
// lets +Inf past: it is not below zero, and no ceiling downstream can be
// smaller than it, so the far side is held to nothing. NaN loses every
// comparison it is put through and reaches the same place by the other road.
func TestAPermissionCannotCarryABudgetThatIsNotARealNumber(t *testing.T) {
	unreal := map[string]float64{
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
		"NaN":               math.NaN(),
	}
	for name, amount := range unreal {
		t.Run(name, func(t *testing.T) {
			p := contract.Permission{Task: "index the repository", BudgetUSD: amount}
			if err := p.Validate(); err == nil {
				t.Fatalf("a budget of %v was accepted as an authorization", amount)
			}
		})
	}

	// Zero stays valid: a grant that is spent is the normal end of a grant,
	// not a typo, which is why the refusal cannot simply be "not positive".
	spent := contract.Permission{Task: "index the repository"}
	if err := spent.Validate(); err != nil {
		t.Fatalf("a spent grant was refused: %v", err)
	}
}
