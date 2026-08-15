package planner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/agent/model"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// stub is a model that answers whatever the test wrote down. The point of the
// deps seam is that everything worth asserting here -- an answer in the wrong
// shape, an empty one, a turn that died holding a charge -- is reachable
// without a key, a network or a subscription.
type stub struct {
	answer model.Answer
	err    error
	seen   model.Request
	calls  int
}

func (s *stub) Turn(_ context.Context, req model.Request) (model.Answer, error) {
	s.calls++
	s.seen = req
	return s.answer, s.err
}

func structured(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func withTools(c caller) deps {
	return deps{client: c, tools: func() (string, error) { return "/tmp/mcp.json", nil }}
}

func usd(v float64) *float64 { return &v }

// charged is a measured charge: the number a real turn comes back with.
func charged() contract.Charge {
	return contract.Charge{InputTokens: 100, OutputTokens: 20, USD: usd(0.03), PricedBy: "claude-code"}
}

func exploreAssignment() assignment {
	return assignment{
		ID:   "a1",
		Type: "explore",
		Task: task{
			Objective: "find where the settings are read",
			Criterion: "the answer names a file",
		},
	}
}

func planAssignment(verdict string, result map[string]any) assignment {
	in := exploreAssignment()
	in.Type = "plan"
	in.Subject = &subject{
		RunID:   "r1",
		Type:    "explore",
		Verdict: verdict,
		Result:  result,
		Task:    in.Task,
	}
	return in
}

func exploration() map[string]any {
	return map[string]any{
		SummaryField:  "settings load in internal/config",
		FindingsField: "config.Load reads atenea.toml. ReadFile parses the graph. Nothing else touches it.",
	}
}

// ---- the exploration half -------------------------------------------------

func TestAnExplorationComesBackAsSummaryAndFindings(t *testing.T) {
	c := &stub{answer: model.Answer{
		Structured: structured(t, map[string]string{
			"summary":  "the settings load in one place",
			"findings": "config.Load reads the file. ReadFile parses the graph.",
		}),
		Spent: charged(),
	}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict != "ok" {
		t.Fatalf("verdict = %q (%s), want ok", got.Verdict, reasonOf(got))
	}
	if got.Result[SummaryField] != "the settings load in one place" {
		t.Errorf("summary = %v", got.Result[SummaryField])
	}
	if got.Spent == nil || got.Spent.USD == nil || *got.Spent.USD != 0.03 {
		t.Errorf("spent = %+v, want the turn's charge", got.Spent)
	}
}

func TestAnExplorationsFindingsBecomeDiscoveries(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary":  "one place",
		"findings": "First fact here. Second fact here. Third fact here. Fourth fact here.",
	})}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if len(got.Discovered) == 0 {
		t.Fatal("an exploration that found things discovered nothing")
	}
	if len(got.Discovered) > 3 {
		t.Errorf("discovered %d facts, want at most 3", len(got.Discovered))
	}
	for _, d := range got.Discovered {
		if d.Level != "repository" {
			t.Errorf("level = %q, want repository", d.Level)
		}
		if strings.TrimSpace(d.Note) == "" {
			t.Error("an empty discovery was recorded")
		}
	}
}

func TestAnExplorerWithNoServiceIsUnavailableNotFailed(t *testing.T) {
	c := &stub{}
	d := deps{client: c, tools: func() (string, error) {
		return "", contract.Fail(contract.FailureUnavailable, "no atenea service is listening at /tmp/atenea.sock")
	}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, d)

	if got.Verdict != "incomplete" {
		t.Errorf("verdict = %q, want incomplete: the explorer did not do badly, it could not run", got.Verdict)
	}
	if got.Reason == nil || got.Reason.Kind != "unavailable" {
		t.Errorf("reason = %+v, want kind unavailable", got.Reason)
	}
	if c.calls != 0 {
		t.Error("the model was called with no capabilities to reach")
	}
}

func TestAnEmptyExplorationIsRefused(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary": "", "findings": "",
	})}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", got.Verdict)
	}
}

func TestAnAnswerInTheWrongShapeIsRefusedAndKeepsItsCharge(t *testing.T) {
	c := &stub{answer: model.Answer{
		Structured: json.RawMessage(`{"summary": 3}`),
		Spent:      charged(),
	}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", got.Verdict)
	}
	if got.Spent == nil {
		t.Error("a refused answer threw away the charge the turn really cost")
	}
}

func TestATurnThatDiedStillReportsWhatItSpent(t *testing.T) {
	c := &stub{
		answer: model.Answer{Spent: charged()},
		err:    contract.Fail(contract.FailureTimeout, "the model did not answer within 30s"),
	}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict == "ok" {
		t.Fatal("a died turn answered ok")
	}
	if got.Spent == nil || got.Spent.USD == nil || *got.Spent.USD != 0.03 {
		t.Errorf("spent = %+v: a turn that occupied the machine for a minute cost that money", got.Spent)
	}
}

// ---- the planning half ----------------------------------------------------

func TestAPlanIsReturnedAsTOMLText(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"plan": "[[step]]\nid = \"read-a\"\n",
	})}}
	got := plan(context.Background(), planAssignment("ok", exploration()), config.Config{}, withTools(c))

	if got.Verdict != "ok" {
		t.Fatalf("verdict = %q (%s), want ok", got.Verdict, reasonOf(got))
	}
	if !strings.Contains(got.Result[PlanField].(string), "[[step]]") {
		t.Errorf("plan = %v", got.Result[PlanField])
	}
}

func TestPlanningWithoutAnExplorationIsUnavailable(t *testing.T) {
	c := &stub{}
	in := planAssignment("ok", exploration())
	in.Subject = nil
	got := plan(context.Background(), in, config.Config{}, withTools(c))

	if got.Verdict != "incomplete" {
		t.Errorf("verdict = %q, want incomplete", got.Verdict)
	}
	if c.calls != 0 {
		t.Error("the model was asked to plan from nothing")
	}
}

func TestPlanningOnARejectedExplorationIsRefusedBeforeTheModel(t *testing.T) {
	c := &stub{}
	got := plan(context.Background(), planAssignment("failed", nil), config.Config{}, withTools(c))

	if got.Verdict == "ok" {
		t.Fatal("a plan was built on an exploration that said it fell short")
	}
	if c.calls != 0 {
		t.Error("the model was paid to plan from a finding nobody accepted")
	}
	if got.Reason == nil || !strings.Contains(got.Reason.Text, "failed") {
		t.Errorf("reason = %+v, want it to name the exploration's verdict", got.Reason)
	}
}

func TestAnEmptyPlanIsRefused(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{"plan": "   "})}}
	got := plan(context.Background(), planAssignment("ok", exploration()), config.Config{}, withTools(c))

	if got.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", got.Verdict)
	}
}

func TestThePlannerIsToldWhatTheExplorationFound(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{"plan": "[[step]]"})}}
	plan(context.Background(), planAssignment("ok", exploration()), config.Config{}, withTools(c))

	if !strings.Contains(c.seen.Prompt, "config.Load reads atenea.toml") {
		t.Error("the planner's prompt does not carry the exploration it is planning from")
	}
}

// ---- the grant ------------------------------------------------------------

func TestTheGrantedShareReachesTheModel(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary": "s", "findings": "f",
	})}}
	in := exploreAssignment()
	in.BudgetUSD = usd(0.75)
	explore(context.Background(), in, config.Config{}, withTools(c))

	if c.seen.BudgetUSD != 0.75 {
		t.Errorf("budget = %v, want the step's share", c.seen.BudgetUSD)
	}
}

// Measured 2026-08-14: twelve of twelve real steps spent their whole ceiling
// reading and hit --max-budget-usd before writing an answer, and a same-
// evening probe showed the CLI has no mid-turn cost signal -- see readShare
// and tokensPerUSD's own comments. ReadTokens is how the client is told to
// hold back the rest, denominated in tokens because those are what the CLI
// reports as it goes.
func TestTheReadShareIsReservedFromTheGrant(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary": "s", "findings": "f",
	})}}
	in := exploreAssignment()
	grant := 0.80
	in.BudgetUSD = usd(grant)
	explore(context.Background(), in, config.Config{}, withTools(c))

	if want := int(readShare * grant * tokensPerUSD); c.seen.ReadTokens != want {
		t.Errorf("read tokens = %v, want %v", c.seen.ReadTokens, want)
	}
}

func TestAnUngrantedRunPassesNoCeiling(t *testing.T) {
	c := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary": "s", "findings": "f",
	})}}
	in := exploreAssignment() // BudgetUSD nil: nobody granted money
	explore(context.Background(), in, config.Config{}, withTools(c))

	if c.seen.BudgetUSD != 0 {
		t.Errorf("budget = %v, want 0 meaning no ceiling", c.seen.BudgetUSD)
	}
}

// A share of nothing is not freedom. run refuses it before it builds a client,
// so the two readings of zero never collapse into one.
func TestAShareOfNothingIsRefusedAndNoModelIsCalled(t *testing.T) {
	in := exploreAssignment()
	in.BudgetUSD = usd(0)
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	c := &stub{}
	var out strings.Builder
	if err := run(context.Background(), strings.NewReader(string(raw)), &out,
		func(_ context.Context, _ assignment, _ config.Config, _ deps) report {
			t.Fatal("a turn ran on a grant of nothing")
			return report{}
		}); err != nil {
		t.Fatalf("run: %v", err)
	}

	var got report
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("the answer is not readable: %v", err)
	}
	if got.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", got.Verdict)
	}
	if got.Reason == nil || got.Reason.Kind != "permission_denied" {
		t.Errorf("reason = %+v, want permission_denied", got.Reason)
	}
	if c.calls != 0 {
		t.Error("the model was called on a grant of nothing")
	}
}

// ---- the reserved answer ---------------------------------------------------

func TestAPartialAnswerKeepsVerdictOkAndCarriesCompletenessAndANotice(t *testing.T) {
	c := &stub{answer: model.Answer{
		Structured: structured(t, map[string]string{
			"summary": "the settings load in one place", "findings": "config.Load reads the file.",
		}),
		Completeness: usd(0.55),
		StoppedAt:    "internal/config/loader.go and everything downstream of it",
		Spent:        charged(),
	}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict != "ok" {
		t.Fatalf("verdict = %q (%s), want ok: a partial that answered is still an answer", got.Verdict, reasonOf(got))
	}
	if got.Completeness == nil || *got.Completeness != 0.55 {
		t.Errorf("completeness = %v, want 0.55", got.Completeness)
	}
	if got.StoppedAt == "" {
		t.Error("stopped_at was dropped")
	}
	if len(got.Notices) != 1 {
		t.Errorf("notices = %v, want exactly one naming what was not reached", got.Notices)
	}
}

// A partial that will not say where it stopped is not auditable: `ok` with an
// unnamed gap would read as whole to everything downstream that counts it.
func TestAPartialAnswerWithNoStoppedAtIsRefused(t *testing.T) {
	c := &stub{answer: model.Answer{
		Structured:   structured(t, map[string]string{"summary": "s", "findings": "f"}),
		Completeness: usd(0.55),
		Spent:        charged(),
	}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", got.Verdict)
	}
	if got.Spent == nil {
		t.Error("a refused partial threw away the charge the turn really cost")
	}
}

func TestAWholeAnswerIsAPlainOKWithNoCompletenessAndNoNotice(t *testing.T) {
	c := &stub{answer: model.Answer{
		Structured:   structured(t, map[string]string{"summary": "s", "findings": "f"}),
		Completeness: usd(1),
	}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict != "ok" {
		t.Fatalf("verdict = %q (%s), want ok", got.Verdict, reasonOf(got))
	}
	if got.Completeness != nil {
		t.Errorf("completeness = %v, want nil on a whole answer", got.Completeness)
	}
	if len(got.Notices) != 0 {
		t.Errorf("notices = %v, want none", got.Notices)
	}
}

// The empty-answer refusals apply to a partial the same as to a whole one: a
// partial still has to contain an answer, not just a completeness figure.
func TestAnEmptyPartialExplorationIsStillRefused(t *testing.T) {
	c := &stub{answer: model.Answer{
		Structured:   structured(t, map[string]string{"summary": "", "findings": ""}),
		Completeness: usd(0.55),
		StoppedAt:    "everything",
	}}
	got := explore(context.Background(), exploreAssignment(), config.Config{}, withTools(c))

	if got.Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", got.Verdict)
	}
}

func reasonOf(r report) string {
	if r.Reason == nil {
		return ""
	}
	return r.Reason.Text
}

// Which built-ins the explorer is given is a design decision with a price
// attached, not an incidental. Measured 2026-08-14, before this list existed:
// three explorations of a real repository spent $1.87 and 1.05M tokens and
// dispatched zero capabilities, because Grep and Bash sat beside them.
func TestTheExplorerIsGivenReadingToolsAndNotSearchingOnes(t *testing.T) {
	s := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary":  "the settings are read in internal/config",
		"findings": "config.Load reads the file named by ATENEA_CONFIG.",
	})}}

	if got := explore(t.Context(), exploreAssignment(), config.Config{}, withTools(s)); got.Verdict != "ok" {
		t.Fatalf("verdict = %q, want ok: %+v", got.Verdict, got)
	}

	// Read, because no capability hands back a file's body; Glob, because a
	// tree with no index has to be learned somehow.
	for _, want := range []string{"Read", "Glob"} {
		if !slices.Contains(s.seen.Builtins, want) {
			t.Errorf("builtins = %v, want %s: the explorer cannot read the code", s.seen.Builtins, want)
		}
	}
	// Grep is code.search under another name, and an explorer holding both
	// never dispatches the capability. Bash makes read-only a hope.
	for _, unwanted := range []string{"Grep", "Bash", "Write", "Edit"} {
		if slices.Contains(s.seen.Builtins, unwanted) {
			t.Errorf("builtins = %v, want %s absent", s.seen.Builtins, unwanted)
		}
	}
	if s.seen.Tools == "" {
		t.Error("the explorer was handed no atenea tools at all")
	}
}

// The planner reads the exploration it was handed. Giving it the repository
// would let it plan from a second, unrecorded look at the code -- and the
// exploration on the record would no longer be what the graph came from.
func TestThePlannerIsGivenNoToolsAtAll(t *testing.T) {
	s := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"plan": "task = \"x\"\nbudget_usd = 1.0\n",
	})}}
	in := exploreAssignment()
	in.Type = "plan"
	in.Subject = &subject{Verdict: "ok", Result: map[string]any{
		"summary": "s", "findings": "f",
	}}

	_ = plan(t.Context(), in, config.Config{}, withTools(s))
	if len(s.seen.Builtins) != 0 {
		t.Errorf("builtins = %v, want none: the planner plans from the exploration", s.seen.Builtins)
	}
}

// ---- the reader surface ----------------------------------------------------

// `reader` is `explore` with the capability catalog taken off, and that
// catalog is most of what starting such a turn costs. Measured 2026-08-15 on
// a real repository against claude-opus-5, cold: $0.27 and 26,603 tokens of
// prefix with it, $0.06 and 4,991 without, which is 81% of the floor spent on
// the definitions of tools most steps never call. The whole saving is the
// --mcp-config that is not there, so its absence is the assertion.
func TestAReaderIsGivenNoCapabilitiesAndTheSameReadingTools(t *testing.T) {
	s := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary":  "the settings are read in internal/config",
		"findings": "config.Load reads the file named by ATENEA_CONFIG.",
	})}}

	if got := reader(t.Context(), exploreAssignment(), config.Config{}, withTools(s)); got.Verdict != "ok" {
		t.Fatalf("verdict = %q, want ok: %+v", got.Verdict, got)
	}

	if s.seen.Tools != "" {
		t.Errorf("tools = %q, want none: a reader is the surface with no --mcp-config", s.seen.Tools)
	}
	// The built-ins are the ones explore gets, unchanged: a reader that
	// cannot open a file is not a cheaper agent, it is no agent.
	for _, want := range []string{"Read", "Glob"} {
		if !slices.Contains(s.seen.Builtins, want) {
			t.Errorf("builtins = %v, want %s", s.seen.Builtins, want)
		}
	}
	for _, unwanted := range []string{"Grep", "Bash", "Write", "Edit"} {
		if slices.Contains(s.seen.Builtins, unwanted) {
			t.Errorf("builtins = %v, want %s absent", s.seen.Builtins, unwanted)
		}
	}
	if s.seen.Role != model.RoleExplore {
		t.Errorf("role = %q, want %q: it is the exploring half, on a cheaper surface",
			s.seen.Role, model.RoleExplore)
	}
}

// The service is not a dependency of a step that reads files somebody already
// named. A reader that dialed it anyway would refuse to run whenever the core
// is down -- for a socket whose only purpose is the catalog this type
// deliberately does not carry.
func TestAReaderNeverAsksForTheCapabilitiesItDoesNotCarry(t *testing.T) {
	s := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
		"summary": "one place", "findings": "config.Load reads it.",
	})}}
	d := deps{client: s, tools: func() (string, error) {
		t.Error("the reader dialed the service for tools it is not given")
		return "", contract.Fail(contract.FailureUnavailable, "no atenea service is listening")
	}}

	if got := reader(t.Context(), exploreAssignment(), config.Config{}, d); got.Verdict != "ok" {
		t.Fatalf("verdict = %q, want ok with no service at all: %+v", got.Verdict, got)
	}
}

// The floor probe rebuilds a turn out of SurfaceOf and prices it, and its
// whole claim is that what it priced is the turn a real step gets. That holds
// only while the table and the turns agree, so here they are compared: every
// model-backed type, run for real against a stub, against what the table says
// it is.
func TestEveryTurnAsksForTheSurfaceItsTypeDeclares(t *testing.T) {
	for name, run := range map[string]turn{"explore": explore, "reader": reader, "plan": plan} {
		t.Run(name, func(t *testing.T) {
			s := &stub{answer: model.Answer{Structured: structured(t, map[string]string{
				"summary": "s", "findings": "f", "plan": "task = \"x\"\n",
			})}}
			in := planAssignment("ok", exploration())
			in.Type = name
			if got := run(t.Context(), in, config.Config{}, withTools(s)); got.Verdict != "ok" {
				t.Fatalf("verdict = %q, want ok: %+v", got.Verdict, got)
			}

			want, ok := SurfaceOf(name)
			if !ok {
				t.Fatalf("SurfaceOf says %q calls no model, and a turn of it just did", name)
			}
			if s.seen.Role != want.Role {
				t.Errorf("role = %q, want %q", s.seen.Role, want.Role)
			}
			if carries := s.seen.Tools != ""; carries != want.Capabilities {
				t.Errorf("carries an --mcp-config = %v, want %v", carries, want.Capabilities)
			}
			if !slices.Equal(s.seen.Builtins, want.Builtins) {
				t.Errorf("builtins = %v, want %v", s.seen.Builtins, want.Builtins)
			}
		})
	}
}

// The three deterministic built-ins answer in Go and call no model, so there
// is no turn of theirs to shape and no floor of theirs to price. A surface
// invented for them would have the floor command spend a real turn measuring
// an agent that never spends one.
//
// The key is the name `agent-exec` is given, not the declared type's own: a
// repository's `spec-reader` that borrows this command with `runs` is keyed
// by what it runs, and a type whose command is not this binary has no answer
// here at all.
func TestOnlyTheTypesThatCallAModelHaveASurface(t *testing.T) {
	for _, name := range []string{"filereader", "reviewer", "plan-check", "spec-reader", ""} {
		if got, ok := SurfaceOf(name); ok {
			t.Errorf("SurfaceOf(%q) = %+v, want none", name, got)
		}
	}
}

// A turn is told what it can do, and these two can do different things.
// Naming code.search to an agent holding no such tool spends the commission
// hunting for it, and the reverse -- an explorer never told the capabilities
// are the point -- is what cost $1.87 across three explorations that
// dispatched none of them.
func TestEachSurfaceIsToldWhichToolsItActuallyHas(t *testing.T) {
	withCatalog := explorePrompt(exploreAssignment(), exploreSurface())
	for _, want := range []string{"code.search", "symbol.definition", "catalog.repositories"} {
		if !strings.Contains(withCatalog, want) {
			t.Errorf("the explorer is never told about %s", want)
		}
	}

	filesOnly := explorePrompt(exploreAssignment(), readerSurface())
	for _, unwanted := range []string{"code.search", "symbol.", "catalog.repositories"} {
		if strings.Contains(filesOnly, unwanted) {
			t.Errorf("a reader is pointed at %s, which it does not have:\n%s", unwanted, filesOnly)
		}
	}
	for _, want := range []string{"Read and Glob", "no other tools"} {
		if !strings.Contains(filesOnly, want) {
			t.Errorf("a reader is never told what it does have: %q is missing", want)
		}
	}
}

// Both plans a real model wrote on 2026-08-14 used `needs` as a data pipe:
// four steps read files so that seven later steps could "have" them, which is
// not what needs does. The prompt had never said so, and worse, it forbade the
// one edge that does carry an answer to a step outside the review pool.

func TestThePromptSaysWhatAnEdgeCarries(t *testing.T) {
	got := planPrompt(planAssignment("ok", exploration()), config.Config{})
	for _, want := range []string{
		"needs carries order and NOTHING ELSE",
		"subject is the only edge an answer travels along",
		"write the fetching into the objective of the step that needs it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan prompt never says %q", want)
		}
	}
}

// A planner cannot choose the right edge without knowing which types can
// receive one, and the catalog is the only place that fact lives.
func TestEveryDeclaredTypeSaysWhetherItReadsASubject(t *testing.T) {
	cfg := config.Config{Agents: []config.AgentType{
		{Spec: contract.AgentTypeSpec{Name: "critic"}, Pool: config.PoolReview},
		{Spec: contract.AgentTypeSpec{Name: "planner"}, Pool: config.PoolAgent, ReadsSubject: true},
		{Spec: contract.AgentTypeSpec{Name: "reader"}, Pool: config.PoolAgent},
	}}
	got := declaredTypes(cfg)

	for name, want := range map[string]string{
		"critic":  "Review pool: every step of this type needs",
		"planner": "Reads a subject:",
		"reader":  "Reads no subject:",
	} {
		var found bool
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "- "+name+" ") && strings.Contains(line, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not described as %q:\n%s", name, want, got)
		}
	}
}

// A type the repository declared is a fact about the project, not only a
// capability, and the planner cannot read it as one without being told which
// is which. The marker is Atenea's own word for it: the merge sets the flag,
// and nothing a repository writes can set or clear it.
func TestTheMenuSaysWhichTypesAreTheRepositorysOwn(t *testing.T) {
	cfg := config.Config{Agents: []config.AgentType{
		{Spec: contract.AgentTypeSpec{Name: "reviewer"}, Pool: config.PoolReview},
		{Spec: contract.AgentTypeSpec{Name: "migrations-reviewer"}, Pool: config.PoolReview,
			Summary: "audits a migration", Local: true},
	}}
	for _, line := range strings.Split(declaredTypes(cfg), "\n") {
		own := strings.Contains(line, "this repository's own")
		switch {
		case strings.HasPrefix(line, "- migrations-reviewer ") && !own:
			t.Errorf("a local type is not marked as one: %s", line)
		case strings.HasPrefix(line, "- reviewer ") && own:
			t.Errorf("a shipped type is marked as the repository's: %s", line)
		}
	}
}

// The menu is built from settings this process reads for itself, so reading
// the global file alone hides every type a repository declared. Measured on a
// real run on 2026-08-14: a repository declared `spec-reader`, `config show`
// listed it, `atenea agent spec-reader` spawned it, and the planner was handed
// a menu of five types that did not include it -- because this path called
// Load rather than LoadEffectiveIn. The repository comes from the assignment:
// this test's own working directory is the package, not the repository, so a
// menu built from `os.Getwd` fails it too.
func TestThePlannerSeesTypesTheAssignedRepositoryDeclared(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "atenea.toml")
	if err := config.WriteDefault(settings, false); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	t.Setenv("ATENEA_CONFIG", settings)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".atenea"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	overlay := "[[agent]]\nname = \"spec-reader\"\nruns = \"filereader\"\nsummary = \"reads SPEC.md verbatim\"\n"
	if err := os.WriteFile(filepath.Join(root, ".atenea", "config.toml"), []byte(overlay), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}

	in := planAssignment("ok", map[string]any{"summary": "s", "findings": []any{"f"}})
	level, err := json.Marshal(map[string]string{"root": root})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	in.Context = map[string]json.RawMessage{"repository": level}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var seen config.Config
	var out strings.Builder
	if err := run(context.Background(), strings.NewReader(string(raw)), &out,
		func(_ context.Context, _ assignment, cfg config.Config, _ deps) report {
			seen = cfg
			return report{Result: map[string]any{"plan": "x"}, Verdict: "ok"}
		}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := seen.AgentTypeByName("spec-reader")
	if err != nil {
		t.Fatalf("the planner's settings do not carry the repository's own type: %v", err)
	}
	if !got.Local {
		t.Error("the type is not marked as the repository's own, so the menu cannot say so")
	}
	if menu := declaredTypes(seen); !strings.Contains(menu, "spec-reader") {
		t.Errorf("the menu handed to the model does not name it:\n%s", menu)
	}
}
