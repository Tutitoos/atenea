package selector_test

import (
	"slices"
	"strings"
	"testing"

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

func provider(name string) implOption {
	return func(i *contract.Implementation) { i.Provider = name }
}

func impl(id string, opts ...implOption) contract.Implementation {
	out := contract.Implementation{ID: id, Provider: id, Capability: "code.search"}
	for _, opt := range opts {
		opt(&out)
	}
	return out
}

func smallGoRepo(indexes ...string) contract.Repository {
	return contract.NewRepository("api", "/srv/api", []string{"go"}, contract.ScaleSmall, indexes)
}

func mustSelector(t *testing.T, rules ...selector.Rule) *selector.Selector {
	t.Helper()
	s, err := selector.New(selector.Config{Rules: rules})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	return s
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
			impl("serena.search", provider("serena"), needsIndex()),
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
		"dart.analyzer": "speaks dart",
		"serena.search": "needs an index from provider serena",
		"graph.search":  "needs a medium repository or bigger",
	} {
		if !strings.Contains(reasons[id], want) {
			t.Errorf("%s dropped for %q, want it to mention %q", id, reasons[id], want)
		}
	}
}

// A warm index belongs to the provider, so an implementation whose provider is
// already indexed here must survive.
func TestIndexConstraintIsSatisfiedByTheProviderIndex(t *testing.T) {
	decision, err := mustSelector(t).Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo("serena"),
		Candidates: []contract.Implementation{impl("serena.search", provider("serena"), needsIndex())},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "serena.search" {
		t.Fatalf("chosen = %s", decision.Chosen.ID)
	}
}

// An unclassified repository is not a proven mismatch. Dropping candidates for
// a size nobody has measured would silently empty the funnel.
func TestUnspecifiedScaleNeverDisqualifies(t *testing.T) {
	repo := contract.NewRepository("api", "/srv/api", []string{"go"}, contract.ScaleUnspecified, nil)
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

// The user's word outranks Atenea's own ranking, even against a healthier
// provider.
func TestUserRuleOutranksTheAutomaticChoice(t *testing.T) {
	s := mustSelector(t, selector.Rule{Capability: "code.search", Prefer: "ripgrep"})
	decision, err := s.Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("ripgrep", health(contract.HealthDegraded, 0)),
			impl("serena.search", health(contract.HealthAlive, 1)),
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
		selector.Rule{Capability: "code.search", Repository: "api", Prefer: "serena.search"},
	)
	decision, err := s.Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{impl("ripgrep"), impl("serena.search")},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "serena.search" {
		t.Fatalf("chosen = %s, want the repository-scoped rule to win", decision.Chosen.ID)
	}
}

// The manual choice is scaffolding, not dogma: Atenea moves on rather than
// stopping. Changing it in silence would betray what the user asked for, so it
// has to be announced.
func TestFallbackFromADeadPreferenceIsAnnounced(t *testing.T) {
	s := mustSelector(t, selector.Rule{Capability: "code.search", Prefer: "serena.search"})
	decision, err := s.Select(selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: []contract.Implementation{
			impl("ripgrep", health(contract.HealthAlive, 1)),
			impl("serena.search", health(contract.HealthDown, 0)),
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if decision.Chosen.ID != "ripgrep" {
		t.Fatalf("chosen = %s", decision.Chosen.ID)
	}
	if len(decision.Notices) != 1 || !strings.Contains(decision.Notices[0], "serena.search") {
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

func TestNewRejectsBrokenRules(t *testing.T) {
	cases := map[string][]selector.Rule{
		"missing capability": {{Prefer: "ripgrep"}},
		"missing preference": {{Capability: "code.search"}},
		"two rules for the same scope": {
			{Capability: "code.search", Prefer: "ripgrep"},
			{Capability: "code.search", Prefer: "serena.search"},
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
