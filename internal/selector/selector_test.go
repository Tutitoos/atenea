package selector_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type implOption func(*contract.Implementation)

func withLanguages(langs ...string) implOption {
	return func(i *contract.Implementation) { i.Constraints.Languages = langs }
}

func needsIndex() implOption {
	return func(i *contract.Implementation) { i.Constraints.RequiresIndex = true }
}

func maxInput(name string, bound int) implOption {
	return func(i *contract.Implementation) {
		if i.Constraints.MaxInput == nil {
			i.Constraints.MaxInput = map[string]int{}
		}
		i.Constraints.MaxInput[name] = bound
	}
}

func needsVCS() implOption {
	return func(i *contract.Implementation) { i.Constraints.RequiresVCS = true }
}

func scaleRange(lo, hi contract.Scale) implOption {
	return func(i *contract.Implementation) {
		i.Constraints.MinScale, i.Constraints.MaxScale = lo, hi
	}
}

func health(state contract.HealthState, score float64) implOption {
	return func(i *contract.Implementation) {
		i.Health = contract.Health{State: state, Score: score}
	}
}

func healthDown(reason, raw string) implOption {
	return func(i *contract.Implementation) {
		i.Health = contract.Health{State: contract.HealthDown, Reason: reason, Raw: raw}
	}
}

func healthDownAt(at time.Time) implOption {
	return func(i *contract.Implementation) {
		i.Health = contract.Health{State: contract.HealthDown, ObservedAt: at}
	}
}

func provider(name string) implOption {
	return func(i *contract.Implementation) { i.Provider = name }
}

func estimated(d time.Duration, tokens int) implOption {
	return func(i *contract.Implementation) {
		i.Cost.Estimated = contract.Sample{Duration: d, Tokens: tokens}
	}
}

func measured(d time.Duration, tokens, samples int) implOption {
	return func(i *contract.Implementation) {
		i.Cost.Measured = contract.Sample{Duration: d, Tokens: tokens}
		i.Cost.Samples = samples
	}
}

func impl(id string, opts ...implOption) contract.Implementation {
	out := contract.Implementation{ID: id, Provider: id, Capability: "code.search"}
	for _, opt := range opts {
		opt(&out)
	}
	return out
}

func smallGoRepo(indexes ...string) contract.Repository {
	return contract.NewRepository("api", "/srv/api", []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, indexes)
}

// funnel wraps the selector so the cases that are not about reach do not have
// to restate it. Reach is required on the real API on purpose -- a caller that
// forgets it is the bug this stage exists to prevent -- but repeating it in
// every case here would bury what each one is actually pinning down. The reach
// tests set it themselves.
type funnel struct{ selector *selector.Selector }

func (f funnel) Select(req selector.Request) (selector.Decision, error) {
	if req.Reachable == nil {
		for _, impl := range req.Candidates {
			req.Reachable = append(req.Reachable, impl.ID)
		}
	}
	return f.selector.Select(req)
}

func mustSelector(t *testing.T, rules ...selector.Rule) funnel {
	t.Helper()
	s, err := selector.New(selector.Config{Rules: rules})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	return funnel{s}
}

func TestExpiredHealthObservationReturnsToUnknown(t *testing.T) {
	s, err := selector.New(selector.Config{HealthStaleAfter: time.Hour})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	observed := time.Now().Add(-2 * time.Hour)
	candidate := impl("stale", healthDownAt(observed))
	decision, err := s.Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{candidate},
		Reachable:  []string{"stale"},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "stale" {
		t.Fatalf("chosen = %q, want stale provider to be retried after expiry", decision.Chosen.ID)
	}
	if decision.Chosen.Health.State != contract.HealthUnknown {
		t.Fatalf("chosen health = %v, want unknown after expiry", decision.Chosen.Health.State)
	}
}

func stage(t *testing.T, decision selector.Decision, name string) selector.Stage {
	t.Helper()
	for _, s := range decision.Stages {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("stage %q missing from %+v", name, decision.Stages)
	return selector.Stage{}
}

// Stage 1 answers "who CAN work here". Each of the three constraints has to be
// able to drop a candidate on its own.
func TestConstraintsDropOnLanguageIndexAndScale(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("ripgrep"),
			impl("dart.analyzer", withLanguages("dart")),
			impl("fixture.search", provider("fixture"), needsIndex()),
			impl("graph.search", scaleRange(contract.ScaleMedium, contract.ScaleUnspecified)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	constraints := stage(t, decision, selector.StageConstraints)
	if !slices.Equal(constraints.Out, []string{"ripgrep"}) {
		t.Fatalf("survivors = %v, want only ripgrep", constraints.Out)
	}
	reasons := map[string]string{}
	for _, drop := range constraints.Dropped {
		reasons[drop.Implementation] = drop.Reason
	}
	for id, want := range map[string]string{
		"dart.analyzer":  "speaks dart",
		"fixture.search": "needs an index from provider fixture",
		"graph.search":   "needs a medium repository or bigger",
	} {
		if !strings.Contains(reasons[id], want) {
			t.Errorf("%s dropped for %q, want it to mention %q", id, reasons[id], want)
		}
	}
}

// Every other constraint reads the repository. This one reads the request:
// an implementation that can only answer a narrower version of the question
// loses to one that can answer the whole of it, and only when the call
// actually asks for the wider thing.
func TestMaxInputDropsOnlyWhenTheCallExceedsIt(t *testing.T) {
	candidates := []contract.Implementation{
		impl("shallow", maxInput("depth", 0)),
		impl("deep"),
	}
	for _, tc := range []struct {
		name    string
		payload map[string]any
		want    []string
	}{
		{"within the bound", map[string]any{"depth": 0}, []string{"deep", "shallow"}},
		{"above the bound", map[string]any{"depth": 1}, []string{"deep"}},
		{"input not named", map[string]any{"file": "a.go"}, []string{"deep", "shallow"}},
		{"no payload at all", nil, []string{"deep", "shallow"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := mustSelector(t).Select(selector.Request{
				Capability: "code.search",
				Repository: smallGoRepo(),
				Candidates: candidates,
				Payload:    tc.payload,
			})
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			got := slices.Clone(stage(t, decision, selector.StageConstraints).Out)
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("survivors = %v, want %v", got, tc.want)
			}
		})
	}
}

// A dropped candidate has to say what it could not be asked for, not merely
// that it lost: the number the caller sent and the number the provider tops
// out at are the two halves of the fix.
func TestMaxInputDropReasonNamesTheInputAndBothNumbers(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{impl("shallow", maxInput("depth", 0)), impl("deep")},
		Payload:    map[string]any{"depth": 3},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	var reason string
	for _, drop := range stage(t, decision, selector.StageConstraints).Dropped {
		if drop.Implementation == "shallow" {
			reason = drop.Reason
		}
	}
	for _, want := range []string{"depth", "3", "0"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not mention %q", reason, want)
		}
	}
}

// A value the bound cannot be compared against is left alone rather than
// treated as a violation. The payload is already invalid and RunRequest's own
// validation says so precisely, whoever is chosen; dropping here would only
// add a second, worse-worded explanation to the trace for a call that was
// never going to run.
func TestMaxInputIgnoresAnUncomparableValue(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{impl("shallow", maxInput("depth", 0)), impl("deep")},
		Payload:    map[string]any{"depth": "deep please"},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	got := slices.Clone(stage(t, decision, selector.StageConstraints).Out)
	slices.Sort(got)
	if !slices.Equal(got, []string{"deep", "shallow"}) {
		t.Fatalf("survivors = %v, want both", got)
	}
}

// A JSON decoder produces float64 for whole numbers, and an adapter speaking
// JSON must not have to pre-convert to be understood.
func TestMaxInputAcceptsAWholeFloat(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{impl("shallow", maxInput("depth", 0))},
		Payload:    map[string]any{"depth": float64(0)},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got := stage(t, decision, selector.StageConstraints).Out; !slices.Equal(got, []string{"shallow"}) {
		t.Fatalf("survivors = %v, want shallow to survive", got)
	}
}

// The drop reason for a missing index names the detection command and leaves
// the actual indexing to the provider's own tooling.
func TestMissingIndexReasonNamesTheFix(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("ripgrep"),
			impl("fixture.search", provider("fixture"), needsIndex()),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	constraints := stage(t, decision, selector.StageConstraints)
	drops := map[string]string{}
	for _, drop := range constraints.Dropped {
		drops[drop.Implementation] = drop.Reason
	}
	reason, ok := drops["fixture.search"]
	if !ok {
		t.Fatalf("fixture.search was not dropped: %+v", constraints.Dropped)
	}
	for _, want := range []string{"atenea detect", "indexed externally"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not mention %q", reason, want)
		}
	}
}

// A warm index belongs to the provider, so an implementation whose provider is
// already indexed here must survive.
func TestIndexConstraintIsSatisfiedByTheProviderIndex(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo("fixture"),
		Candidates: []contract.Implementation{impl("fixture.search", provider("fixture"), needsIndex())},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "fixture.search" {
		t.Fatalf("chosen = %s", decision.Chosen.ID)
	}
}

// An unclassified repository is not a proven mismatch. Dropping candidates for
// a size nobody has measured would silently empty the funnel.
func TestUnspecifiedScaleNeverDisqualifies(t *testing.T) {
	repo := contract.NewRepository("api", "/srv/api", []string{"go"}, contract.ScaleUnspecified, contract.VCSUnspecified, nil)
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: repo,
		Candidates: []contract.Implementation{
			impl("graph.search", scaleRange(contract.ScaleLarge, contract.ScaleUnspecified)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "graph.search" {
		t.Fatalf("chosen = %s", decision.Chosen.ID)
	}
}

// A capability that measures against a point in history has nothing to
// measure against without one: an implementation that requires version
// control must be dropped, cleanly, before it ever reaches the provider.
func TestNoVCSDisqualifiesAnImplementationThatRequiresIt(t *testing.T) {
	repo := contract.NewRepository("api", "/srv/api", []string{"go"}, contract.ScaleSmall, contract.VCSAbsent, nil)
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: repo,
		Candidates: []contract.Implementation{
			impl("ripgrep"),
			impl("graph.impact", needsVCS()),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	constraints := stage(t, decision, selector.StageConstraints)
	if !slices.Equal(constraints.Out, []string{"ripgrep"}) {
		t.Fatalf("survivors = %v, want only ripgrep", constraints.Out)
	}
	reason := constraints.Dropped[0].Reason
	if !strings.Contains(reason, "version control") {
		t.Errorf("reason = %q, want it to mention version control", reason)
	}
}

// Undeclared is not the same as confirmed absent: a repository nobody has
// said either way about must not lose access to every provider that needs
// version control the moment the constraint starts existing.
func TestUnspecifiedVCSNeverDisqualifies(t *testing.T) {
	repo := contract.NewRepository("api", "/srv/api", []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: repo,
		Candidates: []contract.Implementation{impl("graph.impact", needsVCS())},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "graph.impact" {
		t.Fatalf("chosen = %s", decision.Chosen.ID)
	}
}

// Stage 2 answers "who is AVAILABLE". Only a provider reported down leaves;
// degraded and unprobed stay in the running.
func TestHealthDropsOnlyWhatIsDown(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("down.search", health(contract.HealthDown, 0)),
			impl("degraded.search", health(contract.HealthDegraded, 0.4)),
			impl("unknown.search"),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	healthStage := stage(t, decision, selector.StageHealth)
	if !slices.Equal(healthStage.Out, []string{"degraded.search", "unknown.search"}) {
		t.Fatalf("survivors = %v", healthStage.Out)
	}
	if decision.Chosen.ID != "degraded.search" {
		t.Fatalf("chosen = %s, want the degraded one over the unprobed one", decision.Chosen.ID)
	}
}

// A down implementation is dropped without dispatch, which means the drop
// line is the only place its evidence can still show up: whatever raw text
// the record kept has to ride along on the Drop, not just the reason built
// from it.
func TestHealthDropCarriesTheRawText(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("down.search", healthDown("3 unavailable failures in a row", "connection refused")),
			impl("degraded.search", health(contract.HealthDegraded, 0.4)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	healthStage := stage(t, decision, selector.StageHealth)
	if len(healthStage.Dropped) != 1 {
		t.Fatalf("dropped = %d, want exactly the down implementation", len(healthStage.Dropped))
	}
	dropped := healthStage.Dropped[0]
	if dropped.Reason != "3 unavailable failures in a row" {
		t.Errorf("reason = %q", dropped.Reason)
	}
	if dropped.Raw != "connection refused" {
		t.Errorf("raw = %q, want the provider's own text to survive onto the drop", dropped.Raw)
	}
}

func TestRankingPrefersAliveThenScoreThenID(t *testing.T) {
	cases := []struct {
		name       string
		candidates []contract.Implementation
		want       string
	}{
		{
			name: "alive beats degraded",
			candidates: []contract.Implementation{
				impl("a.search", health(contract.HealthDegraded, 1)),
				impl("b.search", health(contract.HealthAlive, 0)),
			},
			want: "b.search",
		},
		{
			name: "score breaks a tie inside one state",
			candidates: []contract.Implementation{
				impl("a.search", health(contract.HealthAlive, 0.2)),
				impl("b.search", health(contract.HealthAlive, 0.9)),
			},
			want: "b.search",
		},
		{
			// Same catalog, same answer, every time: a selector that shuffled
			// would make every measurement below it unreproducible.
			name: "id is the deterministic last resort",
			candidates: []contract.Implementation{
				impl("z.search", health(contract.HealthAlive, 0.5)),
				impl("a.search", health(contract.HealthAlive, 0.5)),
			},
			want: "a.search",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := mustSelector(t).Select(selector.Request{
				Capability: "code.search",
				Repository: smallGoRepo(),
				Candidates: tc.candidates,
			})
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if decision.Chosen.ID != tc.want {
				t.Fatalf("chosen = %s, want %s", decision.Chosen.ID, tc.want)
			}
		})
	}
}

func TestOneCallPreferenceWinsAndFallsBackWhenUnavailable(t *testing.T) {
	candidates := []contract.Implementation{
		impl("ripgrep", health(contract.HealthAlive, 1)),
		impl("codex.search", health(contract.HealthAlive, 0.1)),
	}
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search", Repository: smallGoRepo(), Candidates: candidates,
		Prefer: "codex.search",
	})
	if err != nil || decision.Chosen.ID != "codex.search" {
		t.Fatalf("preferred decision = %s, err=%v", decision.Chosen.ID, err)
	}
	candidates[1].Health = contract.Health{State: contract.HealthDown, Reason: "not authenticated"}
	decision, err = mustSelector(t).Select(selector.Request{
		Capability: "code.search", Repository: smallGoRepo(), Candidates: candidates,
		Prefer: "codex.search",
	})
	if err != nil || decision.Chosen.ID != "ripgrep" {
		t.Fatalf("fallback decision = %s, err=%v", decision.Chosen.ID, err)
	}
	if len(decision.Notices) == 0 || !strings.Contains(decision.Notices[0], "falling back") {
		t.Fatalf("notices = %v, want explicit fallback notice", decision.Notices)
	}
}

// The user's word outranks Atenea's own ranking, even against a healthier
// provider.
func TestUserRuleOutranksTheAutomaticChoice(t *testing.T) {
	s := mustSelector(t, selector.Rule{Capability: "code.search", Prefer: "ripgrep"})
	decision, err := s.Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("ripgrep", health(contract.HealthDegraded, 0)),
			impl("fixture.search", health(contract.HealthAlive, 1)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "ripgrep" {
		t.Fatalf("chosen = %s, want the rule to win", decision.Chosen.ID)
	}
	if !strings.Contains(decision.Reason, "user rule") {
		t.Fatalf("reason = %q", decision.Reason)
	}
}

func TestRepositoryScopedRuleBeatsTheGlobalOne(t *testing.T) {
	s := mustSelector(t,
		selector.Rule{Capability: "code.search", Prefer: "ripgrep"},
		selector.Rule{Capability: "code.search", Repository: "api", Prefer: "fixture.search"},
	)
	decision, err := s.Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{impl("ripgrep"), impl("fixture.search")},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "fixture.search" {
		t.Fatalf("chosen = %s, want the repository-scoped rule to win", decision.Chosen.ID)
	}
}

// The manual choice is scaffolding, not dogma: Atenea moves on rather than
// stopping. Changing it in silence would betray what the user asked for, so it
// has to be announced.
func TestFallbackFromADeadPreferenceIsAnnounced(t *testing.T) {
	s := mustSelector(t, selector.Rule{Capability: "code.search", Prefer: "fixture.search"})
	decision, err := s.Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("ripgrep", health(contract.HealthAlive, 1)),
			impl("fixture.search", health(contract.HealthDown, 0)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "ripgrep" {
		t.Fatalf("chosen = %s", decision.Chosen.ID)
	}
	if len(decision.Notices) != 1 || !strings.Contains(decision.Notices[0], "fixture.search") {
		t.Fatalf("notices = %v, want the skipped preference announced", decision.Notices)
	}
}

// Every failure lands in the right bin, because the bin is what the caller
// reacts to: nothing to try is not the same as everything is down.
func TestFailuresLandInTheRightBin(t *testing.T) {
	cases := []struct {
		name       string
		candidates []contract.Implementation
		want       contract.FailureKind
	}{
		{
			name:       "no provider registered",
			candidates: nil,
			want:       contract.FailureNotFound,
		},
		{
			name:       "nothing fits this repository",
			candidates: []contract.Implementation{impl("dart.analyzer", withLanguages("dart"))},
			want:       contract.FailureNotFound,
		},
		{
			name: "everything that fits is down",
			candidates: []contract.Implementation{
				impl("ripgrep", health(contract.HealthDown, 0)),
			},
			want: contract.FailureUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := mustSelector(t).Select(selector.Request{
				Capability: "code.search",
				Repository: smallGoRepo(),
				Candidates: tc.candidates,
			})
			if got := contract.KindOf(err); got != tc.want {
				t.Fatalf("kind = %v, want %v (err %v)", got, tc.want, err)
			}
			// Even a failed selection has to explain itself.
			if tc.candidates != nil && len(decision.Stages) == 0 {
				t.Fatal("a failed selection must still carry its trace")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Reach
// ---------------------------------------------------------------------------

// The funnel is cost- and health-aware but blind to wiring, so without this
// stage it would happily pick a provider nothing on this machine can invoke.
// The step would then die on dispatch instead of falling back to whoever could
// have answered -- which is exactly what happened when the second adapter was
// added to the catalog before this stage existed.
func TestAProviderNobodyServesNeverWins(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("claude.search", provider("claude-code"), health(contract.HealthAlive, 1)),
			impl("ripgrep", health(contract.HealthAlive, 1)),
		},
		Reachable: []string{"ripgrep"},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "ripgrep" {
		t.Fatalf("chose %q, want the only one anything can run", decision.Chosen.ID)
	}

	// And it has to say why, because "down" would send someone to debug a
	// provider that is perfectly healthy.
	dropped := stage(t, decision, selector.StageReach).Dropped
	if len(dropped) != 1 || dropped[0].Implementation != "claude.search" {
		t.Fatalf("reach dropped %+v", dropped)
	}
	if !strings.Contains(dropped[0].Reason, "no attached runner") {
		t.Errorf("reason = %q, want it to name the wiring", dropped[0].Reason)
	}
}

// Reach runs after constraints so the trace carries the most useful reason it
// can. A provider that both needs a missing index and has nothing wired up
// should be reported for the index: that is the fact the user can act on, and
// the one that would still block it after the wiring was fixed.
func TestTheMostUsefulReasonWins(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("ripgrep"),
			impl("fixture.search", provider("fixture"), needsIndex()),
		},
		Reachable: []string{"ripgrep"},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if names := stageNames(decision); !slices.Equal(names, []string{
		selector.StageConstraints, selector.StageReach, selector.StageHealth, selector.StageChoice,
	}) {
		t.Fatalf("funnel order = %v", names)
	}

	dropped := stage(t, decision, selector.StageConstraints).Dropped
	if len(dropped) != 1 || !strings.Contains(dropped[0].Reason, "index") {
		t.Errorf("constraints dropped %+v, want the index reason", dropped)
	}
	if len(stage(t, decision, selector.StageReach).Dropped) != 0 {
		t.Error("reach repeated a drop that constraints had already explained")
	}
}

func stageNames(decision selector.Decision) []string {
	out := make([]string, len(decision.Stages))
	for i, s := range decision.Stages {
		out[i] = s.Name
	}
	return out
}

// A catalog full of providers and nothing wired up is unavailable, not
// not_found: the implementations exist, there is just no way to ask them.
func TestNothingReachableIsUnavailable(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{impl("ripgrep"), impl("fixture.search")},
		Reachable:  []string{},
	})
	if got := contract.KindOf(err); got != contract.FailureUnavailable {
		t.Fatalf("kind = %v, want unavailable", got)
	}
	if len(decision.Stages) == 0 {
		t.Fatal("a failed selection must still carry its trace")
	}
}

// A provider that is attached and serves the implementation, and still cannot
// answer for this repository, must not be reported as unwired. The default
// reason is true of the ordinary miss and false of this one, and the whole
// point of a trace is that somebody acts on what it says: "no attached runner
// serves it" sends them to check wiring that is already correct.
func TestAProviderOutOfScopeSaysSoRatherThanReadingAsUnwired(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{impl("ripgrep"), impl("fixture.search")},
		Reachable:  []string{"ripgrep"},
		Unreachable: map[string]string{
			"fixture.search": "serves only /elsewhere, repository is outside it",
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	var reason string
	for _, stage := range decision.Stages {
		for _, drop := range stage.Dropped {
			if drop.Implementation == "fixture.search" {
				reason = drop.Reason
			}
		}
	}
	if reason == "" {
		t.Fatal("the out-of-scope provider was dropped without a reason")
	}
	if !strings.Contains(reason, "outside it") {
		t.Errorf("reason = %q, want the declared scope rather than the wiring default", reason)
	}
}

// The ordinary miss keeps the ordinary words. Nothing declared a scope here,
// so the default has to survive -- a reason that grew vaguer for every
// provider would be a worse trace, not a better one.
func TestAnOrdinaryUnreachableKeepsTheWiringReason(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{impl("ripgrep"), impl("fixture.search")},
		Reachable:  []string{"ripgrep"},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	for _, stage := range decision.Stages {
		for _, drop := range stage.Dropped {
			if drop.Implementation == "fixture.search" && !strings.Contains(drop.Reason, "no attached runner") {
				t.Errorf("reason = %q, want the wiring default", drop.Reason)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Cost
// ---------------------------------------------------------------------------

// Two providers, equally healthy, wildly different prices. Before cost was
// wired the id decided, so attaching a second client made a model answer every
// search because "claude" sorts before "ripgrep".
func TestTheCheaperOfTwoEqualsWins(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("claude.search", estimated(12*time.Second, 20000)),
			impl("ripgrep", estimated(80*time.Millisecond, 400)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "ripgrep" {
		t.Fatalf("chose %q, want the cheap one", decision.Chosen.ID)
	}
	// And the trace has to admit the number is a guess, because on day one it
	// is: nothing has been measured yet.
	if !strings.Contains(decision.Reason, "estimated") {
		t.Errorf("reason = %q, want it to say the figure is an estimate", decision.Reason)
	}
}

// Health outranks cost: a cheap provider that is degraded loses to a healthy
// expensive one. Cost decides between equals, never over them.
func TestHealthStillOutranksCost(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("cheap.but.sick", health(contract.HealthDegraded, 1), estimated(time.Millisecond, 1)),
			impl("dear.but.well", health(contract.HealthAlive, 1), estimated(time.Minute, 99999)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "dear.but.well" {
		t.Fatalf("chose %q, want the healthy one", decision.Chosen.ID)
	}
}

// Once an implementation has been measured enough, its own numbers replace the
// declared guess -- which is the hybrid cost the design asked for.
func TestMeasurementsReplaceTheEstimate(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			// Declared cheap, measured expensive. The measurement wins.
			impl("optimist", estimated(time.Millisecond, 1),
				measured(30*time.Second, 50000, selector.BreakInSamples)),
			impl("realist", estimated(time.Second, 900),
				measured(time.Second, 900, selector.BreakInSamples)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "realist" {
		t.Fatalf("chose %q, want the one the measurements favor", decision.Chosen.ID)
	}
	if !strings.Contains(decision.Reason, "measured") {
		t.Errorf("reason = %q, want it to say the figure is measured", decision.Reason)
	}
}

// One measurement is not enough: a single call can be a cold cache. Until an
// implementation is out of break-in its declared estimate is what is believed.
func TestOneMeasurementIsStillBreakIn(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("unlucky", estimated(time.Millisecond, 1), measured(time.Hour, 99999, 1)),
			impl("steady", estimated(time.Second, 900)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "unlucky" {
		t.Fatalf("chose %q: one bad sample was believed over the estimate", decision.Chosen.ID)
	}
}

// Nobody has decided what a second of wall clock is worth in tokens, so a
// genuine trade-off is declared a tie instead of being settled by a weighting
// this package invented. That is the point at which a user rule belongs, and
// the trace says so.
func TestATradeOffIsNotPretendedToBeADecision(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("chatty.but.quick", estimated(time.Millisecond, 9000)),
			impl("terse.but.slow", estimated(time.Minute, 10)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if !strings.Contains(decision.Reason, "settled by id") {
		t.Errorf("reason = %q, want it to admit no cost decided this", decision.Reason)
	}
}

// Health is the first ranking stage, so when it separates two providers it is
// what settled the choice - and the trace has to say so even when the two
// carry the same score. A score is a number inside a state, not a substitute
// for it: `degraded 0.5` and `alive 0.5` are not a tie, and a reason naming
// cost here would send a reader looking for a price that decided nothing.
func TestDifferentHealthStatesAreSettledByHealthNotCost(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("limping", health(contract.HealthDegraded, 0.5), estimated(time.Millisecond, 1)),
			impl("well", health(contract.HealthAlive, 0.5), estimated(time.Hour, 99999)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "well" {
		t.Fatalf("chose %q, want the alive provider over the degraded one", decision.Chosen.ID)
	}
	if !strings.Contains(decision.Reason, "healthiest") {
		t.Errorf("reason = %q, want health named: the states differ, only the scores match", decision.Reason)
	}
}

// Cost ranks, it never filters. An expensive provider that is the only one
// left still answers -- and it has to, or it would never earn the measurements
// that could correct its estimate.
func TestBeingExpensiveNeverRemovesAProvider(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("claude.search", estimated(12*time.Second, 20000)),
			impl("ripgrep", estimated(80*time.Millisecond, 400)),
		},
		Reachable: []string{"claude.search"},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "claude.search" {
		t.Fatalf("chose %q, want the expensive survivor", decision.Chosen.ID)
	}
	for _, stage := range decision.Stages {
		for _, drop := range stage.Dropped {
			if drop.Implementation == "claude.search" {
				t.Fatalf("cost acted as a filter: %+v", drop)
			}
		}
	}
}

func TestNewRejectsBrokenRules(t *testing.T) {
	cases := map[string][]selector.Rule{
		"missing capability": {{Prefer: "ripgrep"}},
		"missing preference": {{Capability: "code.search"}},
		"two rules for the same scope": {
			{Capability: "code.search", Prefer: "ripgrep"},
			{Capability: "code.search", Prefer: "fixture.search"},
		},
	}
	for name, rules := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := selector.New(selector.Config{Rules: rules}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
