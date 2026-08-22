package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func codeSearch() contract.Capability {
	return contract.Capability{
		ID:      "code.search",
		Version: contract.Version{Major: 1},
		Summary: "Find literal text in a repository.",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "query", Type: contract.TypeString, Required: true},
			{Name: "scope", Type: contract.TypeStringList},
			{Name: "context_lines", Type: contract.TypeInt},
		},
		Outputs: []contract.Field{{
			Name: "matches", Type: contract.TypeRecordList, Required: true,
			Fields: []contract.Field{
				{Name: "path", Type: contract.TypeString, Required: true},
			},
		}},
	}
}

type testHelper interface {
	Helper()
	Fatalf(string, ...any)
}

// catalog holds one capability, two providers and two repositories, which is
// the smallest shape where the funnel has a real decision to make.
func catalog(t testHelper) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := reg.AddCapability(codeSearch()); err != nil {
		t.Fatalf("AddCapability: %v", err)
	}
	impls := []contract.Implementation{
		{
			ID: "ripgrep", Provider: "ripgrep", Capability: "code.search",
			Health: contract.Health{State: contract.HealthAlive, Score: 0.8},
		},
		{
			ID: "serena.search", Provider: "serena", Capability: "code.search",
			Constraints: contract.Constraints{RequiresIndex: true},
			Health:      contract.Health{State: contract.HealthAlive, Score: 1},
		},
	}
	for _, impl := range impls {
		if err := reg.AddImplementation(impl); err != nil {
			t.Fatalf("AddImplementation: %v", err)
		}
	}
	repos := []contract.Repository{
		contract.NewRepository("api", "/srv/api", []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, []string{"serena"}),
		contract.NewRepository("web", "/srv/web", []string{"typescript"}, contract.ScaleSmall, contract.VCSUnspecified, nil),
	}
	for _, repo := range repos {
		if err := reg.AddRepository(repo); err != nil {
			t.Fatalf("AddRepository: %v", err)
		}
	}
	return reg
}

// fakeRunner stands in for the far side of the dispatch seam so the tests can
// exercise the agent without touching a disk or a tool.
type fakeRunner struct {
	mu sync.Mutex
	// seen records every request, in the order they arrived.
	seen []contract.RunRequest
	// answer decides what comes back for one step id.
	answer func(req contract.RunRequest) (contract.Outcome, error)
	// live and peak track how many steps overlap.
	live, peak atomic.Int64
	// serves limits which implementations this runner can execute.
	serves []string
	delay  time.Duration
}

func (f *fakeRunner) ID() string { return "fake" }

func (f *fakeRunner) Serves(id string) bool {
	if f.serves == nil {
		return true
	}
	for _, served := range f.serves {
		if served == id {
			return true
		}
	}
	return false
}

// Implementations is the same declaration Serves answers one id at a time, and
// it has to agree with it: a double that claimed everything through one and
// nothing through the other would fail the reach stage for reasons no
// production runner ever could. build fills it from the fixture catalog.
func (f *fakeRunner) Implementations() []string { return f.serves }

// Capabilities is deliberately wide: these fakes stand in for every provider
// the fixture catalogs name, and the wiring check that reads this lives in
// core, not here.
func (f *fakeRunner) Capabilities() []string {
	return []string{"code.context", "code.search", "symbol.definition", "symbol.references",
		"symbol.implementations", "symbol.overview", "symbol.calls",
		"code.impact", "repository.index"}
}

func (f *fakeRunner) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	now := f.live.Add(1)
	for {
		peak := f.peak.Load()
		if now <= peak || f.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	defer f.live.Add(-1)

	if !f.Serves(req.Implementation.ID) {
		// A runner that cannot reach a provider says so, exactly as the
		// contract requires. Faking that away would test a runner that cannot
		// exist.
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
			"fake runner does not serve %s", req.Implementation.ID)
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			// What every real adapter does, and the reason it matters here: a
			// double that binned both context errors as `timeout` would model
			// the defect instead of the contract, and every test built on it
			// would agree that stopping a run is a provider running slow.
			return contract.Outcome{}, contract.Stopped(ctx.Err(), "fake runner", f.delay)
		}
	}

	f.mu.Lock()
	f.seen = append(f.seen, req)
	f.mu.Unlock()

	if f.answer != nil {
		return f.answer(req)
	}
	return hits("cmd/main.go"), nil
}

func (f *fakeRunner) requests() []contract.RunRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.RunRequest(nil), f.seen...)
}

func hits(paths ...string) contract.Outcome {
	matches := make([]any, 0, len(paths))
	for _, where := range paths {
		matches = append(matches, map[string]any{"path": where})
	}
	return contract.Outcome{
		Result:  map[string]any{"matches": matches},
		Verdict: contract.VerdictOK,
		Spent:   contract.Sample{Duration: time.Millisecond, Tokens: 10},
	}
}

func build(t testHelper, runner contract.Runner, maxParallel int, dir string) (*orchestrator.Agent, *registry.Registry) {
	t.Helper()
	reg := catalog(t)
	if fake, ok := runner.(*fakeRunner); ok && fake.serves == nil {
		// The default double answers for whatever the fixture registered.
		for _, capability := range reg.Capabilities() {
			impls, err := reg.ImplementationsFor(capability.ID)
			if err != nil {
				t.Fatalf("ImplementationsFor: %v", err)
			}
			for _, impl := range impls {
				fake.serves = append(fake.serves, impl.ID)
			}
		}
	}
	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog:     reg,
		Chooser:     chooser,
		Runner:      runner,
		Checkpoints: store,
		MaxParallel: maxParallel,
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	return agent, reg
}

// ---------------------------------------------------------------------------
// The card
// ---------------------------------------------------------------------------

func TestTheOrchestratorDeclaresItselfAsAnOrchestrator(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	card := agent.Card()
	if err := card.Validate(); err != nil {
		t.Fatalf("the agent's own card is invalid: %v", err)
	}
	if card.Type != contract.AgentOrchestrator {
		t.Errorf("type = %v, want orchestrator", card.Type)
	}
	// It explores repositories and builds the map of who calls whom, and what
	// it learns goes to the history, so it is entitled to all four levels.
	for _, level := range []contract.ContextLevel{
		contract.ContextRepository, contract.ContextWorkspace,
		contract.ContextGlobal, contract.ContextHistory,
	} {
		if !card.Sees(level) {
			t.Errorf("the orchestrator does not declare %v", level)
		}
	}
}

func TestTheCardHandedOutIsACopy(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	agent.Card().Capabilities[0] = "file.write"
	if agent.Card().Capabilities[0] != "code.context" {
		t.Fatal("Card handed out a pointer into the agent")
	}
}

// ---------------------------------------------------------------------------
// Explore, then split
// ---------------------------------------------------------------------------

func TestTheLookHappensBeforeTheSplit(t *testing.T) {
	runner := &fakeRunner{}
	agent, _ := build(t, runner, 0, "")

	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Phases) != 2 {
		t.Fatalf("phases = %d, want explore then work", len(result.Phases))
	}
	if result.Phases[0].Name != orchestrator.PhaseExplore {
		t.Errorf("first phase = %q, want %q", result.Phases[0].Name, orchestrator.PhaseExplore)
	}
	if result.Phases[1].Name != orchestrator.PhaseWork {
		t.Errorf("second phase = %q, want %q", result.Phases[1].Name, orchestrator.PhaseWork)
	}
	// Every look closes before the first piece of work starts.
	for i, step := range result.Steps {
		if i < 2 && step.Phase != orchestrator.PhaseExplore {
			t.Fatalf("step %d is %s, want a look first", i, step.Phase)
		}
		if i >= 2 && step.Phase != orchestrator.PhaseWork {
			t.Fatalf("step %d is %s, want work after the looks", i, step.Phase)
		}
	}
}

// Exploring is not unbilled preparation: hiding what it cost would make every
// task report a total that never happened.
func TestExploringIsMeasuredLikeAnyOtherPhase(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	explore := result.Phases[0]
	if explore.Steps != 2 {
		t.Errorf("explore steps = %d, want one per repository", explore.Steps)
	}
	if explore.Spent.Duration <= 0 {
		t.Error("the look cost time and the phase does not say so")
	}
	if result.Spent.Duration < explore.Spent.Duration {
		t.Error("the total leaves the look out")
	}
}

func TestOneWorkStepPerRepositoryInScope(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := map[string]bool{"search-api": false, "search-web": false}
	for _, step := range result.Steps {
		if step.Phase != orchestrator.PhaseWork {
			continue
		}
		if _, expected := want[step.Step.ID]; !expected {
			t.Fatalf("unexpected work step %s", step.Step.ID)
		}
		want[step.Step.ID] = true
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("%s never ran", id)
		}
	}
}

func TestScopeNarrowsTheCommission(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	result, err := agent.Run(t.Context(), orchestrator.Task{
		Text:         "login",
		Repositories: []string{"api"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, step := range result.Steps {
		if step.Step.Repository != "api" {
			t.Fatalf("%s ran against %s, which was out of scope", step.Step.ID, step.Step.Repository)
		}
	}
}

// The whole return on having looked: the work pass only walks the areas where
// the commission actually landed.
func TestTheLookNarrowsTheWorkThatFollows(t *testing.T) {
	runner := &fakeRunner{
		answer: func(req contract.RunRequest) (contract.Outcome, error) {
			return hits("internal/auth/login.go", "internal/http/route.go", "cmd/main.go"), nil
		},
	}
	agent, _ := build(t, runner, 0, "")

	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login", Repositories: []string{"api"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var work contract.Step
	for _, step := range result.Steps {
		if step.Phase == orchestrator.PhaseWork {
			work = step.Step
		}
	}
	scope, ok := work.Payload["scope"].([]string)
	if !ok {
		t.Fatalf("the work step was not narrowed: %v", work.Payload)
	}
	if strings.Join(scope, ",") != "cmd,internal" {
		t.Fatalf("scope = %v, want the top-level areas the look found", scope)
	}
	// The look itself asks for no context: it is finding out WHERE the
	// commission lands, not reading it.
	for _, req := range runner.requests() {
		if lines, isSet := req.Payload["context_lines"]; isSet && lines != 0 {
			t.Errorf("the look asked for %v context lines", lines)
		}
	}
}

// Narrowing must never silently drop a hit that sits at the repository root,
// because a root file has no directory to narrow to.
func TestAHitAtTheRootLeavesTheWorkUnnarrowed(t *testing.T) {
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return hits("internal/auth.go", "README.md"), nil
		},
	}
	agent, _ := build(t, runner, 0, "")
	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login", Repositories: []string{"api"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, step := range result.Steps {
		if step.Phase != orchestrator.PhaseWork {
			continue
		}
		if _, narrowed := step.Step.Payload["scope"]; narrowed {
			t.Fatalf("a root hit cannot be narrowed away: %v", step.Step.Payload)
		}
	}
}

func TestWhatTheLookFoundBecomesADiscovery(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Discoveries) != 2 {
		t.Fatalf("discoveries = %d, want one per repository looked at", len(result.Discoveries))
	}
	for _, found := range result.Discoveries {
		if found.Level != contract.ContextRepository {
			t.Errorf("a fact about one repository was filed under %v", found.Level)
		}
		if !strings.Contains(found.Note, "login") {
			t.Errorf("the note does not say what was looked for: %q", found.Note)
		}
	}
}

// Only the runner knows how it reached its answer, and some of that changes
// what the answer means. A search stopped at a ceiling is not a search that
// ran out of matches, so what the far side says has to survive the trip.
func TestWhatTheFarSideReportsSurvivesTheTrip(t *testing.T) {
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			out := hits("internal/auth/login.go")
			out.Discoveries = append(out.Discoveries, contract.Discovery{
				Level: contract.ContextRepository,
				Note:  "the answer is partial",
			})
			return out, nil
		},
	}
	agent, _ := build(t, runner, 0, "")
	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	partial := 0
	for _, found := range result.Discoveries {
		if found.Note == "the answer is partial" {
			partial++
		}
	}
	if partial == 0 {
		t.Fatalf("the runner's own discovery was dropped: %+v", result.Discoveries)
	}
	// Every step reported it, and one wall is one fact.
	if partial != 1 {
		t.Errorf("the same note was recorded %d times, want once", partial)
	}
	// It travels alongside what the orchestrator worked out, not instead.
	summary := 0
	for _, found := range result.Discoveries {
		if strings.Contains(found.Note, "hit(s)") {
			summary++
		}
	}
	if summary == 0 {
		t.Error("carrying the runner's note lost the orchestrator's own summary")
	}
}

// ---------------------------------------------------------------------------
// The permission traveling down
// ---------------------------------------------------------------------------

func TestEveryStepCarriesThePermissionItInherited(t *testing.T) {
	runner := &fakeRunner{}
	agent, _ := build(t, runner, 0, "")
	if _, err := agent.Run(t.Context(), orchestrator.Task{Text: "find every TODO"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	requests := runner.requests()
	if len(requests) == 0 {
		t.Fatal("nothing was dispatched")
	}
	for _, req := range requests {
		if req.Permission.Task != "find every TODO" {
			t.Errorf("a step carried %q instead of the commission", req.Permission.Task)
		}
		if !req.Permission.Allows(contract.EffectRead) {
			t.Error("reading is free by default and was not granted")
		}
		// Nothing granted itself anything heavier on the way down.
		if req.Permission.Allows(contract.EffectWrite) {
			t.Error("a read-only commission acquired write on the way down")
		}
		if req.Permission.Allows(contract.EffectExternal) {
			t.Error("a read-only commission acquired external on the way down")
		}
	}
}

func TestAHeavierCommissionPassesItsGrantDown(t *testing.T) {
	runner := &fakeRunner{}
	agent, _ := build(t, runner, 0, "")
	_, err := agent.Run(t.Context(), orchestrator.Task{
		Text:    "rewrite the login form",
		Effects: []contract.Effect{contract.EffectWrite},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, req := range runner.requests() {
		if !req.Permission.Allows(contract.EffectWrite) {
			t.Fatal("the grant did not reach the child")
		}
	}
}

// The standing grant is the settings file's own layer, added beneath
// whatever a commission or question asks for on its own -- Run and Ask both
// carry it down the same way they already carry the free read.
func TestStandingEffectsReachEveryDispatch(t *testing.T) {
	runner := &fakeRunner{}
	reg := catalog(t)
	for _, capability := range reg.Capabilities() {
		impls, err := reg.ImplementationsFor(capability.ID)
		if err != nil {
			t.Fatalf("ImplementationsFor: %v", err)
		}
		for _, impl := range impls {
			runner.serves = append(runner.serves, impl.ID)
		}
	}
	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	store, err := checkpoint.New("")
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog:         reg,
		Chooser:         chooser,
		Runner:          runner,
		Checkpoints:     store,
		StandingEffects: []contract.Effect{contract.EffectProcess},
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}

	if _, err := agent.Run(t.Context(), orchestrator.Task{Text: "find every TODO"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := agent.Ask(t.Context(), orchestrator.Question{
		Capability: "code.search", Repository: "api", Payload: map[string]any{"query": "login"},
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	for _, req := range runner.requests() {
		if !req.Permission.Allows(contract.EffectProcess) {
			t.Errorf("%s did not carry the standing grant", req.Capability.ID)
		}
		// Neither the commission nor the question asked for anything beyond
		// the standing grant, so nothing heavier should have arrived either.
		if req.Permission.Allows(contract.EffectWrite) {
			t.Error("a step acquired write, which nothing granted it")
		}
	}
}

// ---------------------------------------------------------------------------
// The review
// ---------------------------------------------------------------------------

func TestEveryChildIsReviewed(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Steps) != 4 {
		t.Fatalf("steps = %d, want two looks and two searches", len(result.Steps))
	}
	for _, step := range result.Steps {
		if step.Review.Parent == contract.VerdictUnspecified {
			t.Errorf("%s finished without being reviewed", step.Step.ID)
		}
		if step.Review.Reason == "" {
			t.Errorf("%s was judged without a reason on the record", step.Step.ID)
		}
	}
}

// The reviewer exists for exactly this: a child that reports success with an
// answer nobody can read has not succeeded.
func TestTheParentOverrulesAChildThatLiesAboutSucceeding(t *testing.T) {
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{
				// The capability promises `matches`, and this is not it.
				Result:  map[string]any{"results": []any{}},
				Verdict: contract.VerdictOK,
			}, nil
		},
	}
	agent, _ := build(t, runner, 0, "")

	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login", Repositories: []string{"api"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	step := result.Steps[0]
	if step.Review.Child != contract.VerdictOK {
		t.Fatalf("the child claimed %v, want ok", step.Review.Child)
	}
	if step.Review.Parent != contract.VerdictFailed {
		t.Fatalf("the parent accepted an unreadable answer: %v", step.Review.Parent)
	}
	if !step.Review.Disagreed {
		t.Error("the disagreement was not recorded")
	}
	if step.Review.Reply == "" {
		t.Error("the child's one reply has to be on the record, even when it is silence")
	}
	// The parent's word is what goes on the record for the whole commission.
	if result.Verdict != contract.VerdictFailed {
		t.Errorf("commission verdict = %v, want the parent's word", result.Verdict)
	}
}

func TestAgreementIsNotRecordedAsADispute(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, step := range result.Steps {
		if step.Review.Disagreed {
			t.Errorf("%s agreed and was recorded as a dispute", step.Step.ID)
		}
		if step.Review.Reply != "" {
			t.Errorf("%s agreed and got a reply anyway: %q", step.Step.ID, step.Review.Reply)
		}
	}
	if result.Verdict != contract.VerdictOK {
		t.Errorf("verdict = %v, want ok", result.Verdict)
	}
}

// ---------------------------------------------------------------------------
// Health, the funnel and failure
// ---------------------------------------------------------------------------

// Running a step is a probe, and a provider that reports itself unusable is
// news the catalog needs: the funnel filters on health.
//
// The provider is reachable here and fails anyway. That is the only shape this
// case can take now: one nothing serves never reaches dispatch, so it could
// never report anything about itself.
func TestAProviderThatReportsItselfDownIsMarkedDown(t *testing.T) {
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
				"the index is not built")
		},
	}
	agent, reg := build(t, runner, 0, "")

	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login", Repositories: []string{"api"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Verdict != contract.VerdictFailed {
		t.Fatalf("verdict = %v, want failed", result.Verdict)
	}
	chosen := result.Steps[0].Decision.Chosen.ID
	health, where, ok := reg.Observations(chosen)
	if !ok {
		t.Fatalf("%s failed as unavailable and nothing was recorded against it", chosen)
	}
	if where != "api" {
		t.Errorf("recorded against %q, want the repository the call ran on", where)
	}
	if health.State != contract.HealthDown {
		t.Fatalf("%s failed as unavailable and its health is still %v", chosen, health.State)
	}
	if health.Reason == "" {
		t.Error("health went down without saying why")
	}
	// And the declaration is untouched: it is what the operator wrote, and a
	// probe on one repository is not a correction to it.
	declared, err := reg.Implementation(chosen)
	if err != nil {
		t.Fatalf("Implementation: %v", err)
	}
	if declared.Health.State == contract.HealthDown {
		t.Error("a probe rewrote the declared health")
	}
}

// The generic bin an unrecognized provider error falls into used to say
// nothing but its own catch-all sentence, on the step and on the catalog
// mark it left behind for the next call. Both are where a human actually
// looks after a failure, so both have to carry the provider's own words, not
// just Atenea's summary of them.
func TestAFailureCarriesItsRawTextOntoTheStepAndTheCatalog(t *testing.T) {
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
				"serena did not answer").WithRaw("no symbol matching 'Frame/consistent' found")
		},
	}
	agent, reg := build(t, runner, 0, "")

	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login", Repositories: []string{"api"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Steps[0].Raw != "no symbol matching 'Frame/consistent' found" {
		t.Errorf("step.Raw = %q, want the provider's own text", result.Steps[0].Raw)
	}
	chosen := result.Steps[0].Decision.Chosen.ID
	health, _, ok := reg.Observations(chosen)
	if !ok {
		t.Fatalf("nothing was recorded against %s", chosen)
	}
	if health.Raw != "no symbol matching 'Frame/consistent' found" {
		t.Errorf("health.Raw = %q, want it on the mark the next call will read", health.Raw)
	}
}

// One repository finding a provider unusable must not refuse the next
// repository.
//
// Found live: Serena has no TypeScript language server on this machine, so a
// call on a TypeScript repository came back unavailable -- and the Go
// repository next door, whose own Serena process had answered three seconds
// earlier, was then refused with "every implementation is down". A provider
// is not up or down in the abstract, and under a per-repository instance
// policy the two repositories are not even talking to the same process.
func TestOneRepositorysFailureDoesNotBlindAnother(t *testing.T) {
	runner := &fakeRunner{
		serves: []string{"serena.search"},
		answer: func(req contract.RunRequest) (contract.Outcome, error) {
			if req.Repository.ID == "web" {
				return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
					"serena has no working language server for this request")
			}
			return hits("cmd/api/main.go"), nil
		},
	}
	agent, reg := build(t, runner, 0, "")
	// Both repositories must reach the same implementation, or the failure on
	// one could never have reached the other and the test proves nothing.
	if err := reg.SetIndexed("web", "serena", true); err != nil {
		t.Fatalf("SetIndexed: %v", err)
	}
	// web first, so its verdict is on the books before api is ever asked.
	for _, tc := range []struct {
		repository string
		want       contract.Verdict
	}{
		{"web", contract.VerdictFailed},
		{"api", contract.VerdictOK},
	} {
		result, err := agent.Run(t.Context(), orchestrator.Task{
			Text: "login", Repositories: []string{tc.repository},
		})
		if err != nil {
			t.Fatalf("%s: Run: %v", tc.repository, err)
		}
		if result.Verdict != tc.want {
			t.Fatalf("%s: verdict = %v, want %v (steps=%+v)",
				tc.repository, result.Verdict, tc.want, result.Steps)
		}
	}

	// And the record says which repository found it, so the status screen can
	// name the one that is actually broken.
	if _, where, ok := reg.Observations("serena.search"); !ok || where != "web" {
		t.Errorf("observation = %q (recorded=%v), want it against web", where, ok)
	}

	// The other half of remembering: asked again, web is refused by the funnel
	// on what the last call found, instead of dispatching into the same wall.
	// Without that, a recorded verdict is a note nobody reads.
	before := len(runner.requests())
	again, err := agent.Run(t.Context(), orchestrator.Task{Text: "login", Repositories: []string{"web"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if again.Verdict != contract.VerdictFailed {
		t.Fatalf("verdict = %v, want failed", again.Verdict)
	}
	if got := len(runner.requests()); got != before {
		t.Errorf("%d call(s) reached the provider after it was recorded down", got-before)
	}
	if failure := again.Steps[0].Failure; !strings.Contains(failure, "is down for repository web") {
		t.Errorf("failure = %q, want the funnel's own refusal", failure)
	}
}

// The funnel is asked per repository, so the same commission can be answered
// by different providers in different units of work.
func TestTheFunnelIsConsultedPerRepository(t *testing.T) {
	runner := &fakeRunner{}
	agent, _ := build(t, runner, 0, "")
	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	chosen := map[string]string{}
	for _, step := range result.Steps {
		chosen[step.Step.Repository] = step.Decision.Chosen.ID
	}
	// api has a warm Serena index and Serena scores higher; web has none, so
	// Serena is dropped on constraints and ripgrep is left.
	if chosen["api"] != "serena.search" {
		t.Errorf("api chose %s, want serena.search", chosen["api"])
	}
	if chosen["web"] != "ripgrep" {
		t.Errorf("web chose %s, want ripgrep", chosen["web"])
	}
}

// An edge in the graph means "after". A look that failed leaves the work that
// waited on it with nothing honest to stand on, so that branch is blocked --
// never dispatched, but still on the record, because a step that silently
// vanishes is worse than one that says why it did not run.
func TestWorkIsBlockedWhenTheLookItWaitedOnFailed(t *testing.T) {
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{}, contract.Fail(contract.FailureNotFound, "no such repository on disk")
		},
	}
	agent, _ := build(t, runner, 0, "")
	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	looks := 0
	for _, step := range result.Steps {
		if step.Phase == orchestrator.PhaseExplore {
			looks++
			continue
		}
		if step.Decision.Chosen.ID != "" {
			t.Fatalf("%s was dispatched even though %v failed", step.Step.ID, step.Step.Needs)
		}
		if step.Review.Parent != contract.VerdictFailed {
			t.Errorf("%s was blocked and not recorded as failed", step.Step.ID)
		}
		if !strings.Contains(step.Review.Reason, "blocked") {
			t.Errorf("%s does not say it was blocked: %q", step.Step.ID, step.Review.Reason)
		}
	}
	// Only the two looks reached the runner; neither search did.
	if got := len(runner.requests()); got != looks {
		t.Fatalf("%d requests reached the runner, want only the %d looks", got, looks)
	}
	if result.Verdict != contract.VerdictFailed {
		t.Errorf("verdict = %v, want failed", result.Verdict)
	}
	if result.Steps[0].Failure == "" {
		t.Error("the failure was not kept on the step")
	}
}

func TestAnUnknownRepositoryIsRefusedBeforeAnythingRuns(t *testing.T) {
	runner := &fakeRunner{}
	agent, _ := build(t, runner, 0, "")
	_, err := agent.Run(t.Context(), orchestrator.Task{Text: "login", Repositories: []string{"ghost"}})
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", got)
	}
	if len(runner.requests()) != 0 {
		t.Error("something was dispatched for a repository that does not exist")
	}
}

func TestACommissionWithNoTextIsRefused(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	_, err := agent.Run(t.Context(), orchestrator.Task{Text: "   "})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// An agent with nobody behind it can plan and choose; it cannot dispatch, and
// saying so is better than failing halfway through.
func TestWithNoRunnerNothingIsDispatched(t *testing.T) {
	agent, _ := build(t, nil, 0, "")
	_, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", got)
	}
}

// ---------------------------------------------------------------------------
// Waves and the ceiling
// ---------------------------------------------------------------------------

func TestStepsInOneWaveRunAtTheSameTime(t *testing.T) {
	runner := &fakeRunner{delay: 30 * time.Millisecond}
	agent, _ := build(t, runner, 0, "")
	if _, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if runner.peak.Load() < 2 {
		t.Fatalf("peak overlap = %d, want the two repositories to run together", runner.peak.Load())
	}
}

func TestTheCeilingLimitsHowManyRunAtOnce(t *testing.T) {
	runner := &fakeRunner{delay: 20 * time.Millisecond}
	agent, _ := build(t, runner, 1, "")
	if _, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := runner.peak.Load(); got != 1 {
		t.Fatalf("peak overlap = %d, want the configured ceiling of 1", got)
	}
}

func TestANegativeCeilingIsRefused(t *testing.T) {
	reg := catalog(t)
	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	_, err = orchestrator.New(orchestrator.Config{Catalog: reg, Chooser: chooser, MaxParallel: -1})
	if err == nil {
		t.Fatal("a negative ceiling has to be refused")
	}
}

func TestWiringWithoutACatalogIsRefused(t *testing.T) {
	if _, err := orchestrator.New(orchestrator.Config{}); err == nil {
		t.Fatal("an agent with no catalog can never answer anything")
	}
}

// A cancellation stops the run, hands back what it got to, and says so. The
// verdict used to be `failed` here, which was this defect written down as a
// test: nothing failed, and a reader sent looking for a fault finds none.
func TestCancellationStopsTheRun(t *testing.T) {
	runner := &fakeRunner{delay: time.Second}
	agent, _ := build(t, runner, 0, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result, err := agent.Run(ctx, orchestrator.Task{Text: "login"})
	if err == nil {
		t.Fatal("a canceled commission has to stop")
	}
	if result == nil {
		t.Fatal("a cut-short run still has to hand back what it got to")
	}
	if result.Verdict != contract.VerdictCanceled {
		t.Errorf("verdict = %v, want canceled", result.Verdict)
	}
	// And it must not read as success either: part of the plan never ran.
	if result.Verdict == contract.VerdictOK {
		t.Error("a run that was stopped cannot report that it worked")
	}
}

// ---------------------------------------------------------------------------
// The paper copy
// ---------------------------------------------------------------------------

func TestTheRunIsWrittenAsEachStepClosesAndWhenItCloses(t *testing.T) {
	dir := t.TempDir()
	agent, _ := build(t, &fakeRunner{}, 1, dir)

	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	record, err := store.Load(result.RunID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !record.Closed {
		t.Error("the run closed and the paper copy does not say so")
	}
	if record.Verdict != result.Verdict.String() {
		t.Errorf("recorded verdict = %q, want %q", record.Verdict, result.Verdict)
	}
	if len(record.Steps) != len(result.Steps) {
		t.Fatalf("recorded steps = %d, want %d", len(record.Steps), len(result.Steps))
	}
	for i, step := range record.Steps {
		if step.ID != result.Steps[i].Step.ID {
			t.Errorf("step %d recorded as %s, want %s", i, step.ID, result.Steps[i].Step.ID)
		}
		if step.Implementation == "" {
			t.Errorf("%s was recorded without the implementation that ran it", step.ID)
		}
	}
}

// A commission cut short is exactly the one worth reading back, so the dump
// has to happen on the way out too.
func TestACutShortRunStillLeavesAPaperCopy(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{}, contract.Fail(contract.FailureNotFound, "gone")
		},
	}
	agent, _ := build(t, runner, 0, dir)

	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	store, _ := checkpoint.New(dir)
	record, err := store.Load(result.RunID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if record.Verdict != contract.VerdictFailed.String() {
		t.Errorf("recorded verdict = %q, want failed", record.Verdict)
	}
	if len(record.Steps) == 0 {
		t.Error("the failed looks were not recorded")
	}
}

func TestCheckpointingOffLeavesNothingBehind(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	if _, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"}); err != nil {
		t.Fatalf("a run with checkpointing off still has to work: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Ask: one capability, directly
// ---------------------------------------------------------------------------

// The atomic base of hoja 15. It shares every mechanism with a commission --
// the funnel picks, the parent reviews, the receipt is written -- and these
// tests are about the two rules that are its own.

// A commission with no repository means every repository, because the user
// excluded none. A direct ask cannot borrow that: a position belongs to
// exactly one unit of work, and running it against the rest would answer about
// files that merely share a path.
func TestAskNeedsToKnowWhichRepository(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")

	_, err := agent.Ask(t.Context(), orchestrator.Question{
		Capability: "code.search",
		Payload:    map[string]any{"query": "x"},
	})
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input; err = %v", contract.KindOf(err), err)
	}
}

// The card is the gate, not the catalog. A capability the agent does not
// declare is refused even when the registry knows it and a runner serves it,
// because the card is what a client reads to find out what it may ask for.
func TestAskRefusesWhatTheCardDoesNotDeclare(t *testing.T) {
	agent, reg := build(t, &fakeRunner{}, 0, "")
	undeclared := codeSearch()
	undeclared.ID = "code.rewrite"
	if err := reg.AddCapability(undeclared); err != nil {
		t.Fatalf("AddCapability: %v", err)
	}
	if err := reg.AddImplementation(contract.Implementation{
		ID: "rewriter", Provider: "rewriter", Capability: "code.rewrite",
		Health: contract.Health{State: contract.HealthAlive, Score: 1},
	}); err != nil {
		t.Fatalf("AddImplementation: %v", err)
	}
	if agent.Card().CanAsk("code.rewrite") {
		t.Fatal("the fixture card already declares the capability; the gate cannot be tested")
	}

	result, err := agent.Ask(t.Context(), orchestrator.Question{
		Capability: "code.rewrite",
		Repository: "api",
		Payload:    map[string]any{"query": "x"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	// The step closes failed rather than the call erroring: an undeclared
	// capability is a bad step, and a bad step is reviewed like any other so
	// the receipt records what was attempted.
	if result.Verdict != contract.VerdictFailed {
		t.Fatalf("verdict = %v, want failed", result.Verdict)
	}
	if len(result.Steps) != 1 || !strings.Contains(result.Steps[0].Failure, "code.rewrite") {
		t.Fatalf("steps = %+v, want one naming the refused capability", result.Steps)
	}
}

// symbol.overview is the newest capability the card lists, and the
// mechanism the test above refuses code.rewrite with is exactly what has to
// let this one through instead. The fixture registry never stocks it, so
// the ask still fails -- but failing at "unknown capability" rather than
// "may not ask for" is what proves the card gate, not the registry, is what
// changed.
func TestAskSymbolOverviewPassesTheCardGate(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	if !agent.Card().CanAsk("symbol.overview") {
		t.Fatal("the card does not declare symbol.overview")
	}

	result, err := agent.Ask(t.Context(), orchestrator.Question{
		Capability: "symbol.overview",
		Repository: "api",
		Payload:    map[string]any{"file": "main.go"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result.Verdict != contract.VerdictFailed {
		t.Fatalf("verdict = %v, want failed: the fixture registry does not stock this capability", result.Verdict)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %+v, want exactly one", result.Steps)
	}
	if strings.Contains(result.Steps[0].Failure, "may not ask for") {
		t.Fatalf("failure = %q, the card gate rejected it; want it past the gate and failing on the registry instead", result.Steps[0].Failure)
	}
	if !strings.Contains(result.Steps[0].Failure, "unknown capability") {
		t.Fatalf("failure = %q, want it to fail because the fixture registry does not stock symbol.overview", result.Steps[0].Failure)
	}
}

// The receipt says what happened, and what happened was an ask. Borrowing
// "explore" would make a run claim it looked around when it did not.
func TestAskIsItsOwnPhaseOnTheReceipt(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")

	result, err := agent.Ask(t.Context(), orchestrator.Question{
		Capability: "code.search",
		Repository: "api",
		Payload:    map[string]any{"query": "x"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v, want ok", result.Verdict)
	}
	if len(result.Phases) != 1 || result.Phases[0].Name != orchestrator.PhaseAsk {
		t.Fatalf("phases = %+v, want one named %q", result.Phases, orchestrator.PhaseAsk)
	}
	if len(result.Steps) != 1 || result.Steps[0].Step.Repository != "api" {
		t.Fatalf("steps = %+v, want one against api", result.Steps)
	}
	// One repository was asked, so the other one was left alone.
	if got := result.Steps[0].Decision.Repository; got != "api" {
		t.Errorf("the funnel was consulted for %q, want api", got)
	}
}

// A payload missing a required field is a fact the request already carries;
// the funnel's own work -- pricing candidates, choosing among them -- must
// not be spent finding that out, and the runner must never be asked at all.
func TestAskRejectsAMissingRequiredFieldBeforeDispatch(t *testing.T) {
	runner := &fakeRunner{}
	agent, _ := build(t, runner, 0, "")

	result, err := agent.Ask(t.Context(), orchestrator.Question{
		Capability: "code.search",
		Repository: "api",
		Payload:    map[string]any{},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result.Verdict != contract.VerdictFailed {
		t.Fatalf("verdict = %v, want failed", result.Verdict)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %+v, want 1", result.Steps)
	}
	step := result.Steps[0]
	if step.FailureKind != contract.FailureInvalidInput {
		t.Errorf("failure kind = %v, want invalid_input", step.FailureKind)
	}
	if !strings.Contains(step.Failure, `"query" is required`) {
		t.Errorf("failure = %q, want it to name the missing field", step.Failure)
	}
	if got := runner.requests(); len(got) != 0 {
		t.Errorf("runner saw %d request(s), want 0 -- dispatched before validating", len(got))
	}
}

// ---------------------------------------------------------------------------
// Resume
// ---------------------------------------------------------------------------

// A crash between the look closing and the split running leaves a receipt
// whose plan never grew past the look: hasWork reads false, and there is no
// honest shape to split on without redoing the look that would have decided
// it. Resume redoes it whole, ignoring that the old attempt's look already
// passed review, and then splits and works exactly as a fresh commission
// would.
func TestResumeRedoesExploreWhenSplittingNeverRan(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return hits("internal/auth/login.go"), nil
		},
	}
	agent, _ := build(t, runner, 0, dir)
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}

	permission := contract.Permission{Task: "login", Effects: []contract.Effect{contract.EffectRead}}
	runID := checkpoint.NewID(time.Now())
	if err := store.Save(checkpoint.Run{
		ID: runID, Kind: checkpoint.KindTask, Task: "login",
		Repositories: []string{"api"}, BudgetUSD: 1,
		ContractVersion: contract.Current.String(), Started: time.Now(),
		Plan: contract.Plan{Task: "login", Steps: []contract.Step{{
			ID: "explore-api", Capability: "code.search", Repository: "api",
			Payload: map[string]any{"query": "login"}, Permission: permission,
		}}},
		Steps: []checkpoint.StepState{{
			ID: "explore-api", Capability: "code.search", Repository: "api",
			Implementation: "ripgrep", Verdict: "ok", Review: "ok",
		}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	result, err := agent.Resume(t.Context(), runID, orchestrator.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(runner.requests()) != 2 {
		t.Fatalf("requests = %d, want 2 -- the look is redone, not trusted from the receipt", len(runner.requests()))
	}
	// Regression: the checkpoint record above never sets Effects, exactly as
	// checkpoint.Run.Effects is documented to hold only what a commission
	// authorized BEYOND reading. Rebuilding the permission from that field
	// alone, with nothing prepended, would silently drop the free read, and
	// every real adapter would refuse the redone look with permission
	// denied -- read has to be added back here the same way Run adds it.
	if !runner.requests()[0].Permission.Allows(contract.EffectRead) {
		t.Fatal("the redone look was not granted the read every commission gets for free")
	}
	if result.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v, want ok", result.Verdict)
	}
	if len(result.Plan.Steps) != 2 {
		t.Fatalf("plan steps = %d, want 2 (redone look + work)", len(result.Plan.Steps))
	}
	if len(result.Steps) != 2 {
		t.Fatalf("steps = %d, want 2 -- both the redone look and the work it enabled", len(result.Steps))
	}
}

// Every layer a resumed commission can carry effects from -- the free read,
// the standing grant, what the original commission held, and what --allow
// adds now -- has to reach the redone look, not just some of them.
func TestResumeComposesEveryEffectLayerOnTheRedoneExplore(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return hits("internal/auth/login.go"), nil
		},
	}
	reg := catalog(t)
	for _, capability := range reg.Capabilities() {
		impls, err := reg.ImplementationsFor(capability.ID)
		if err != nil {
			t.Fatalf("ImplementationsFor: %v", err)
		}
		for _, impl := range impls {
			runner.serves = append(runner.serves, impl.ID)
		}
	}
	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog:         reg,
		Chooser:         chooser,
		Runner:          runner,
		Checkpoints:     store,
		StandingEffects: []contract.Effect{contract.EffectProcess},
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}

	runID := checkpoint.NewID(time.Now())
	if err := store.Save(checkpoint.Run{
		ID: runID, Kind: checkpoint.KindTask, Task: "login",
		Repositories: []string{"api"}, BudgetUSD: 1,
		Effects:         []contract.Effect{contract.EffectWrite},
		ContractVersion: contract.Current.String(), Started: time.Now(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := agent.Resume(t.Context(), runID, orchestrator.ResumeOptions{
		Effects: []contract.Effect{contract.EffectExternal},
	}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	requests := runner.requests()
	if len(requests) == 0 {
		t.Fatal("nothing was dispatched")
	}
	permission := requests[0].Permission
	for _, want := range []contract.Effect{
		contract.EffectRead,     // free by default
		contract.EffectProcess,  // the standing grant
		contract.EffectWrite,    // what the original commission held
		contract.EffectExternal, // what --allow added on this resume
	} {
		if !permission.Allows(want) {
			t.Errorf("redone look permission = %v, missing %s", permission.Effects, want)
		}
	}
}

// Once splitting already ran, the plan on the receipt already carries every
// step's payload. A step that already passed review is left alone; a step
// that never closed -- failed, canceled, or the process died mid-wave -- is
// retried.
func TestResumeContinuesFromASplitPlanAlreadyOnFile(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			out := hits("internal/auth/login.go")
			out.Discoveries = append(out.Discoveries, contract.Discovery{
				Level: contract.ContextRepository, Note: "search-api: found it fresh",
			})
			return out, nil
		},
	}
	agent, _ := build(t, runner, 0, dir)
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}

	permission := contract.Permission{Task: "login", Effects: []contract.Effect{contract.EffectRead}}
	runID := checkpoint.NewID(time.Now())
	if err := store.Save(checkpoint.Run{
		ID: runID, Kind: checkpoint.KindTask, Task: "login",
		Repositories: []string{"api"}, BudgetUSD: 1,
		ContractVersion: contract.Current.String(), Started: time.Now(),
		Plan: contract.Plan{Task: "login", Steps: []contract.Step{
			{ID: "explore-api", Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "login"}, Permission: permission},
			{ID: "search-api", Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "login"}, Needs: []string{"explore-api"},
				Permission: permission},
		}},
		Steps: []checkpoint.StepState{{
			// search-api never closed -- the process died mid-work.
			ID: "explore-api", Capability: "code.search", Repository: "api",
			Implementation: "ripgrep", Verdict: "ok", Review: "ok",
			Discoveries: []contract.Discovery{
				{Level: contract.ContextRepository, Note: "explore-api: already found this before the crash"},
			},
		}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	result, err := agent.Resume(t.Context(), runID, orchestrator.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(runner.requests()) != 1 {
		t.Fatalf("requests = %d, want 1 -- explore-api already passed review and must not be redispatched", len(runner.requests()))
	}
	if result.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v, want ok", result.Verdict)
	}
	if len(result.Steps) != 1 || result.Steps[0].Step.ID != "search-api" {
		t.Fatalf("steps = %+v, want only the redispatched search-api", result.Steps)
	}
	// explore-api was never redispatched, so its discovery can only have
	// reached the result by being read back off the receipt.
	var notes []string
	for _, d := range result.Discoveries {
		notes = append(notes, d.Note)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "explore-api: already found this before the crash") {
		t.Errorf("discoveries = %v, want explore-api's recovered from the receipt", notes)
	}
	if !strings.Contains(joined, "search-api: found it fresh") {
		t.Errorf("discoveries = %v, want search-api's from this dispatch", notes)
	}

	updated, err := store.Load(runID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(updated.Steps) != 2 {
		t.Fatalf("persisted steps = %d, want 2 -- the old entry kept, the new one added", len(updated.Steps))
	}
	if !updated.Closed || updated.Verdict != "ok" {
		t.Fatalf("closed = %v verdict = %q, want closed ok", updated.Closed, updated.Verdict)
	}
}

// A step already on a split plan keeps its own stamped permission, but the
// standing grant and --allow still have to reach it when it is retried --
// the same way they reach a redone look, just through the other branch.
func TestResumeGrantsStandingAndAllowToARetriedSplitStep(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return hits("internal/auth/login.go"), nil
		},
	}
	reg := catalog(t)
	for _, capability := range reg.Capabilities() {
		impls, err := reg.ImplementationsFor(capability.ID)
		if err != nil {
			t.Fatalf("ImplementationsFor: %v", err)
		}
		for _, impl := range impls {
			runner.serves = append(runner.serves, impl.ID)
		}
	}
	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog:         reg,
		Chooser:         chooser,
		Runner:          runner,
		Checkpoints:     store,
		StandingEffects: []contract.Effect{contract.EffectProcess},
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}

	original := contract.Permission{Task: "login", Effects: []contract.Effect{contract.EffectRead}}
	runID := checkpoint.NewID(time.Now())
	if err := store.Save(checkpoint.Run{
		ID: runID, Kind: checkpoint.KindTask, Task: "login",
		Repositories: []string{"api"}, BudgetUSD: 1,
		ContractVersion: contract.Current.String(), Started: time.Now(),
		Plan: contract.Plan{Task: "login", Steps: []contract.Step{
			{ID: "explore-api", Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "login"}, Permission: original},
			{ID: "search-api", Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "login"}, Needs: []string{"explore-api"},
				Permission: original},
		}},
		Steps: []checkpoint.StepState{{
			// search-api never closed -- the process died mid-work.
			ID: "explore-api", Capability: "code.search", Repository: "api",
			Implementation: "ripgrep", Verdict: "ok", Review: "ok",
		}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := agent.Resume(t.Context(), runID, orchestrator.ResumeOptions{
		Effects: []contract.Effect{contract.EffectWrite},
	}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	requests := runner.requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1 -- only search-api needed retrying", len(requests))
	}
	permission := requests[0].Permission
	for _, want := range []contract.Effect{contract.EffectRead, contract.EffectProcess, contract.EffectWrite} {
		if !permission.Allows(want) {
			t.Errorf("retried step permission = %v, missing %s", permission.Effects, want)
		}
	}
}

// A run that already finished with nothing outstanding is a no-op: every
// step is already in alreadyOK, so no wave has anything left to schedule.
func TestResumeOnAClosedRunWithNothingLeftIsANoOp(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}
	agent, _ := build(t, runner, 0, dir)
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}

	permission := contract.Permission{Task: "login", Effects: []contract.Effect{contract.EffectRead}}
	runID := checkpoint.NewID(time.Now())
	if err := store.Save(checkpoint.Run{
		ID: runID, Kind: checkpoint.KindTask, Task: "login",
		Repositories: []string{"api"}, BudgetUSD: 1,
		ContractVersion: contract.Current.String(), Started: time.Now(),
		Closed: true, Verdict: "ok",
		Plan: contract.Plan{Task: "login", Steps: []contract.Step{
			{ID: "explore-api", Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "login"}, Permission: permission},
			{ID: "search-api", Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "login"}, Needs: []string{"explore-api"},
				Permission: permission},
		}},
		Steps: []checkpoint.StepState{
			{ID: "explore-api", Capability: "code.search", Repository: "api", Verdict: "ok", Review: "ok"},
			{ID: "search-api", Capability: "code.search", Repository: "api", Verdict: "ok", Review: "ok"},
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	result, err := agent.Resume(t.Context(), runID, orchestrator.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(runner.requests()) != 0 {
		t.Fatalf("requests = %d, want 0 -- nothing was left to do", len(runner.requests()))
	}
	if len(result.Steps) != 0 {
		t.Fatalf("steps = %d, want 0 -- every step was already on file", len(result.Steps))
	}
	if result.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v, want ok", result.Verdict)
	}
}

// Nothing is redispatched here -- every step is already OK -- so this is the
// purest form of the gap: if discoveries did not survive on the receipt,
// there would be nothing anywhere left to report them from.
func TestResumeRecoversDiscoveriesFromStepsClosedInAnEarlierProcess(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}
	agent, _ := build(t, runner, 0, dir)
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}

	permission := contract.Permission{Task: "login", Effects: []contract.Effect{contract.EffectRead}}
	runID := checkpoint.NewID(time.Now())
	if err := store.Save(checkpoint.Run{
		ID: runID, Kind: checkpoint.KindTask, Task: "login",
		Repositories: []string{"api"}, BudgetUSD: 1,
		ContractVersion: contract.Current.String(), Started: time.Now(),
		Closed: true, Verdict: "ok",
		Plan: contract.Plan{Task: "login", Steps: []contract.Step{
			{ID: "explore-api", Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "login"}, Permission: permission},
			{ID: "search-api", Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "login"}, Needs: []string{"explore-api"},
				Permission: permission},
		}},
		Steps: []checkpoint.StepState{
			{ID: "explore-api", Capability: "code.search", Repository: "api", Verdict: "ok", Review: "ok",
				Discoveries: []contract.Discovery{
					{Level: contract.ContextRepository, Note: "explore-api: 4 hit(s)"},
				}},
			{ID: "search-api", Capability: "code.search", Repository: "api", Verdict: "ok", Review: "ok",
				Discoveries: []contract.Discovery{
					{Level: contract.ContextRepository, Note: "search-api: answered for free"},
				}},
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	result, err := agent.Resume(t.Context(), runID, orchestrator.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(runner.requests()) != 0 {
		t.Fatalf("requests = %d, want 0 -- nothing was left to do", len(runner.requests()))
	}
	if len(result.Discoveries) != 2 {
		t.Fatalf("discoveries = %d, want both steps' -- nothing ran, so both can only have come off the receipt: %+v",
			len(result.Discoveries), result.Discoveries)
	}
	var notes []string
	for _, d := range result.Discoveries {
		notes = append(notes, d.Note)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "explore-api: 4 hit(s)") || !strings.Contains(joined, "search-api: answered for free") {
		t.Errorf("discoveries = %v, want both steps' recovered from the receipt", notes)
	}
}

// A peer ahead of this core is refused, because the core cannot honor a
// field it has never heard of -- the same rule Version.Supports already
// applies to a live connection applies here to a receipt written by a peer
// version that has since moved on.
func TestResumeRefusesAStaleContractVersion(t *testing.T) {
	dir := t.TempDir()
	agent, _ := build(t, &fakeRunner{}, 0, dir)
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	permission := contract.Permission{Task: "x", Effects: []contract.Effect{contract.EffectRead}}
	runID := checkpoint.NewID(time.Now())
	if err := store.Save(checkpoint.Run{
		ID: runID, Kind: checkpoint.KindAsk, Task: "x",
		Repositories: []string{"api"}, BudgetUSD: 1,
		ContractVersion: "1.0.0", Started: time.Now(),
		Plan: contract.Plan{Task: "x", Steps: []contract.Step{{
			ID: "ask-api", Capability: "code.search", Repository: "api",
			Payload: map[string]any{"query": "x"}, Permission: permission,
		}}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err = agent.Resume(t.Context(), runID, orchestrator.ResumeOptions{})
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// A repository the catalog no longer has is refused whole, before any step
// is touched -- discovering it halfway through would leave some steps
// retried and others not, for a receipt that was never resumable at all.
func TestResumeRefusesARepositoryNoLongerInTheCatalog(t *testing.T) {
	dir := t.TempDir()
	agent, _ := build(t, &fakeRunner{}, 0, dir)
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	permission := contract.Permission{Task: "x", Effects: []contract.Effect{contract.EffectRead}}
	runID := checkpoint.NewID(time.Now())
	if err := store.Save(checkpoint.Run{
		ID: runID, Kind: checkpoint.KindAsk, Task: "x",
		Repositories: []string{"ghost"}, BudgetUSD: 1,
		ContractVersion: contract.Current.String(), Started: time.Now(),
		Plan: contract.Plan{Task: "x", Steps: []contract.Step{{
			ID: "ask-ghost", Capability: "code.search", Repository: "ghost",
			Payload: map[string]any{"query": "x"}, Permission: permission,
		}}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err = agent.Resume(t.Context(), runID, orchestrator.ResumeOptions{})
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", got)
	}
}

// BudgetUSD replaces what remains of the grant instead of adding to it, the
// same rule a fresh commission's --budget already follows. The share a
// single-step wave is handed is the whole grant, so it reads the override
// straight off the request the runner receives.
func TestResumeReplacesTheRemainingBudgetRatherThanAddingToIt(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}
	agent, _ := build(t, runner, 0, dir)
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	permission := contract.Permission{Task: "x", Effects: []contract.Effect{contract.EffectRead}}
	save := func(id string) {
		t.Helper()
		if err := store.Save(checkpoint.Run{
			ID: id, Kind: checkpoint.KindAsk, Task: "x",
			Repositories: []string{"api"}, BudgetUSD: 3,
			ContractVersion: contract.Current.String(), Started: time.Now(),
			Plan: contract.Plan{Task: "x", Steps: []contract.Step{{
				ID: "ask-api", Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "x"}, Permission: permission,
			}}},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	plainID := checkpoint.NewID(time.Now())
	save(plainID)
	if _, err := agent.Resume(t.Context(), plainID, orchestrator.ResumeOptions{}); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	overrideID := checkpoint.NewID(time.Now())
	save(overrideID)
	if _, err := agent.Resume(t.Context(), overrideID, orchestrator.ResumeOptions{BudgetUSD: 50}); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	reqs := runner.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if got := reqs[0].Permission.BudgetUSD; got != 3 {
		t.Errorf("plain resume share = %v, want the remaining grant (3)", got)
	}
	if got := reqs[1].Permission.BudgetUSD; got != 50 {
		t.Errorf("overridden resume share = %v, want the override (50), not 3+50", got)
	}
}

// A single ask is one step and no split to redo: it dispatches exactly as it
// did the first time.
func TestResumeOfASingleAskRedispatchesWhatIsLeft(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{}
	agent, _ := build(t, runner, 0, dir)
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	permission := contract.Permission{Task: "code.search in api", Effects: []contract.Effect{contract.EffectRead}}
	runID := checkpoint.NewID(time.Now())
	if err := store.Save(checkpoint.Run{
		ID: runID, Kind: checkpoint.KindAsk, Task: "code.search in api",
		Repositories: []string{"api"}, BudgetUSD: 1,
		ContractVersion: contract.Current.String(), Started: time.Now(),
		Plan: contract.Plan{Task: "code.search in api", Steps: []contract.Step{{
			ID: "ask-api", Capability: "code.search", Repository: "api",
			Payload: map[string]any{"query": "x"}, Permission: permission,
		}}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	result, err := agent.Resume(t.Context(), runID, orchestrator.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if result.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v, want ok", result.Verdict)
	}
	if len(result.Phases) != 1 || result.Phases[0].Name != orchestrator.PhaseAsk {
		t.Fatalf("phases = %+v, want one named %q", result.Phases, orchestrator.PhaseAsk)
	}
	if len(runner.requests()) != 1 {
		t.Fatalf("requests = %d, want 1", len(runner.requests()))
	}
}

// Checkpointing off means there is no disk to read a receipt back from, so
// Resume is refused before it ever tries.
func TestResumeIsRefusedWhenCheckpointingIsOff(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, "")
	_, err := agent.Resume(t.Context(), "20260802T120000-abc123", orchestrator.ResumeOptions{})
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", got)
	}
}

func TestResumeOfAnUnknownRunIsRefused(t *testing.T) {
	agent, _ := build(t, &fakeRunner{}, 0, t.TempDir())
	_, err := agent.Resume(t.Context(), checkpoint.NewID(time.Now()), orchestrator.ResumeOptions{})
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", got)
	}
}
