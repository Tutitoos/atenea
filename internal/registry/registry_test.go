package registry_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func codeSearch() contract.Capability {
	return contract.Capability{
		ID:      "code.search",
		Version: contract.Version{Major: 1},
		Summary: "Find literal text in a repository.",
		Effects: []contract.Effect{contract.EffectRead},
		Inputs:  []contract.Field{{Name: "query", Type: contract.TypeString, Required: true}},
	}
}

func impl(id, provider string) contract.Implementation {
	return contract.Implementation{
		ID:         id,
		Provider:   provider,
		Capability: "code.search",
	}
}

func seeded(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := reg.AddCapability(codeSearch()); err != nil {
		t.Fatalf("AddCapability: %v", err)
	}
	for _, i := range []contract.Implementation{impl("serena.search", "serena"), impl("ripgrep", "ripgrep")} {
		if err := reg.AddImplementation(i); err != nil {
			t.Fatalf("AddImplementation %s: %v", i.ID, err)
		}
	}
	return reg
}

// A bound that names nothing the capability sends is refused at load. The
// alternative is worse than an error: the funnel reads a name no request
// carries, every implementation survives, and the narrowing the settings
// file asked for is silently absent.
func TestMaxInputMustNameADeclaredIntInput(t *testing.T) {
	capability := codeSearch()
	capability.Inputs = append(capability.Inputs,
		contract.Field{Name: "depth", Type: contract.TypeInt},
		contract.Field{Name: "scope", Type: contract.TypeString})

	for _, tc := range []struct {
		name, input, want string
	}{
		{"undeclared input", "dpeth", "does not declare"},
		{"declared but not an int", "scope", "not int"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := registry.New()
			if err := reg.AddCapability(capability); err != nil {
				t.Fatalf("AddCapability: %v", err)
			}
			bounded := impl("ripgrep", "ripgrep")
			bounded.Constraints.MaxInput = map[string]int{tc.input: 0}
			err := reg.AddImplementation(bounded)
			if contract.KindOf(err) != contract.FailureInvalidInput {
				t.Fatalf("kind = %v, want invalid_input (err = %v)", contract.KindOf(err), err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}

	reg := registry.New()
	if err := reg.AddCapability(capability); err != nil {
		t.Fatalf("AddCapability: %v", err)
	}
	bounded := impl("ripgrep", "ripgrep")
	bounded.Constraints.MaxInput = map[string]int{"depth": 0}
	if err := reg.AddImplementation(bounded); err != nil {
		t.Fatalf("a bound on a declared int input was refused: %v", err)
	}
}

func TestImplementationsForIsSortedAndComplete(t *testing.T) {
	impls, err := seeded(t).ImplementationsFor("code.search")
	if err != nil {
		t.Fatalf("ImplementationsFor: %v", err)
	}
	if len(impls) != 2 || impls[0].ID != "ripgrep" || impls[1].ID != "serena.search" {
		t.Fatalf("got %v", impls)
	}
}

// An implementation pointing at a capability nobody registered is a typo, and a
// typo that resolves to nothing only surfaces as "the selector found no
// candidates" hours later.
func TestOrphanImplementationIsRefused(t *testing.T) {
	reg := registry.New()
	if err := reg.AddImplementation(impl("ripgrep", "ripgrep")); err == nil {
		t.Fatal("expected an error")
	} else if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v", contract.KindOf(err))
	}
}

func TestDuplicatesAreRefused(t *testing.T) {
	reg := seeded(t)
	if err := reg.AddCapability(codeSearch()); err == nil {
		t.Error("duplicate capability should fail")
	}
	if err := reg.AddImplementation(impl("ripgrep", "ripgrep")); err == nil {
		t.Error("duplicate implementation should fail")
	}
	repo := contract.NewRepository("web", "/srv/web", nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	if err := reg.AddRepository(repo); err != nil {
		t.Fatalf("AddRepository: %v", err)
	}
	if err := reg.AddRepository(repo); err == nil {
		t.Error("duplicate repository should fail")
	}
}

func TestUnknownLookupsAreNotFound(t *testing.T) {
	reg := seeded(t)
	for name, err := range map[string]error{
		"capability":     mustErr(reg.Capability("code.impact")),
		"implementation": mustErr(reg.Implementation("grep")),
		"repository":     mustErr(reg.Repository("web")),
	} {
		if contract.KindOf(err) != contract.FailureNotFound {
			t.Errorf("%s: kind = %v, want not_found", name, contract.KindOf(err))
		}
	}
	if _, err := reg.ImplementationsFor("code.impact"); contract.KindOf(err) != contract.FailureNotFound {
		t.Errorf("ImplementationsFor: kind = %v", contract.KindOf(err))
	}
}

// A caller knows where it is working, not what the operator called that place
// in the settings file. An absolute path resolves to the repository holding
// it, and the longest configured path wins so a repository nested inside
// another is not swallowed by its parent. A registered id is still matched
// first: a name is never guessed at as a path.
func TestRepositoryResolvesAnAbsolutePathToItsRepository(t *testing.T) {
	reg := seeded(t)
	for _, r := range []contract.Repository{
		contract.NewRepository("web", "/srv/web", nil, contract.ScaleSmall, contract.VCSUnspecified, nil),
		contract.NewRepository("frontend", "/srv/web/frontend", nil, contract.ScaleSmall, contract.VCSUnspecified, nil),
		contract.NewRepository("sibling", "/srv/web-two", nil, contract.ScaleSmall, contract.VCSUnspecified, nil),
	} {
		if err := reg.AddRepository(r); err != nil {
			t.Fatalf("AddRepository %s: %v", r.ID, err)
		}
	}

	for _, tc := range []struct{ ask, want string }{
		{"web", "web"},                        // a registered id, not a path
		{"/srv/web", "web"},                   // the root itself
		{"/srv/web/internal/core", "web"},     // a directory under it
		{"/srv/web/frontend", "frontend"},     // the nested repository, not its parent
		{"/srv/web/frontend/src", "frontend"}, // longest prefix wins
		{"/srv/web-two/app", "sibling"},       // a prefix in text is not a prefix in path
		{"/srv/web/../web/internal", "web"},   // cleaned before it is compared
	} {
		repo, err := reg.Repository(tc.ask)
		if err != nil {
			t.Fatalf("Repository(%q): %v", tc.ask, err)
		}
		if repo.ID != tc.want {
			t.Errorf("Repository(%q) = %s, want %s", tc.ask, repo.ID, tc.want)
		}
	}

	// Outside every configured path, and a relative path that happens to look
	// like one, are both still not_found: the fallback widens what resolves,
	// not what is accepted.
	for _, ask := range []string{"/srv/other", "/", "srv/web"} {
		if _, err := reg.Repository(ask); contract.KindOf(err) != contract.FailureNotFound {
			t.Errorf("Repository(%q): kind = %v, want not_found", ask, contract.KindOf(err))
		}
	}
}

// A typo close enough to a real id is named in the error, so the second
// attempt does not have to be another guess.
func TestUnknownCapabilitySuggestsTheClosestMatch(t *testing.T) {
	reg := seeded(t)
	_, err := reg.Capability("code.serach")
	if contract.KindOf(err) != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", contract.KindOf(err))
	}
	if !strings.Contains(err.Error(), "did you mean code.search?") {
		t.Errorf("err = %v, want a suggestion", err)
	}
}

// Past a real typo's reach a suggestion is a guess dressed as help. An
// unrelated id gets the plain refusal, nothing invented to fill the gap.
func TestUnknownCapabilityFarFromAnythingSuggestsNothing(t *testing.T) {
	reg := seeded(t)
	_, err := reg.Capability("totally.unrelated")
	if contract.KindOf(err) != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", contract.KindOf(err))
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("err = %v, want no suggestion for an unrelated id", err)
	}
}

func mustErr[T any](_ T, err error) error { return err }

// Health is the one block that moves while Atenea runs. Everything else is
// declarative, so the registry only exposes this mutator -- and it is keyed
// by repository, because a provider that cannot serve one repository is not
// thereby down for the rest.
func TestSetHealthRecordsWhatOneRepositoryFound(t *testing.T) {
	reg := seeded(t)
	web := contract.NewRepository("web", "/srv/web", nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	api := contract.NewRepository("api", "/srv/api", nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	for _, r := range []contract.Repository{web, api} {
		if err := reg.AddRepository(r); err != nil {
			t.Fatalf("AddRepository %s: %v", r.ID, err)
		}
	}
	want := contract.Health{State: contract.HealthDown, Reason: "no language server"}
	if err := reg.SetHealth("web", "serena.search", want); err != nil {
		t.Fatalf("SetHealth: %v", err)
	}

	// The declaration itself never moves: it is what the operator wrote, and
	// the status screen still has to be able to show it.
	declared, err := reg.Implementation("serena.search")
	if err != nil {
		t.Fatalf("Implementation: %v", err)
	}
	if declared.Health.State == contract.HealthDown {
		t.Error("a probe against one repository rewrote the declaration")
	}

	impls, err := reg.ImplementationsFor("code.search")
	if err != nil {
		t.Fatalf("ImplementationsFor: %v", err)
	}
	for _, tc := range []struct {
		repository string
		want       contract.HealthState
	}{
		{"web", contract.HealthDown},
		{"api", declared.Health.State},
	} {
		for _, got := range reg.Observed(tc.repository, slices.Clone(impls)) {
			if got.ID != "serena.search" {
				continue
			}
			if got.Health.State != tc.want {
				t.Errorf("%s: health = %v, want %v", tc.repository, got.Health.State, tc.want)
			}
		}
	}

	if err := reg.SetHealth("web", "nope", want); contract.KindOf(err) != contract.FailureNotFound {
		t.Errorf("unknown implementation: kind = %v", contract.KindOf(err))
	}
	if err := reg.SetHealth("nope", "serena.search", want); contract.KindOf(err) != contract.FailureNotFound {
		t.Errorf("unknown repository: kind = %v", contract.KindOf(err))
	}
	if err := reg.SetHealth("web", "ripgrep", contract.Health{Score: 2}); err == nil {
		t.Error("out-of-range score should fail")
	}
}

// SetIndexed is the repository's equivalent of SetHealth: indexed_by starts
// as the settings file's declared guess, and a real probe corrects it.
func TestSetIndexedCorrectsRepository(t *testing.T) {
	reg := registry.New()
	repo := contract.NewRepository("web", "/srv/web", nil, contract.ScaleSmall, contract.VCSUnspecified, nil)
	if err := reg.AddRepository(repo); err != nil {
		t.Fatalf("AddRepository: %v", err)
	}
	if err := reg.SetIndexed("web", "codebase-memory", true); err != nil {
		t.Fatalf("SetIndexed: %v", err)
	}
	got, err := reg.Repository("web")
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	if !got.IndexedBy("codebase-memory") {
		t.Fatal("SetIndexed(true) did not stick")
	}
	if err := reg.SetIndexed("nope", "codebase-memory", true); contract.KindOf(err) != contract.FailureNotFound {
		t.Errorf("unknown repository: kind = %v", contract.KindOf(err))
	}
}

// The catalog is shared between every open chat, so handing a caller a
// pointer into it would let one session corrupt another.
func TestReadsAreDefensiveCopies(t *testing.T) {
	reg := registry.New()
	if err := reg.AddCapability(codeSearch()); err != nil {
		t.Fatalf("AddCapability: %v", err)
	}
	serena := impl("serena.search", "serena")
	serena.Constraints.Languages = []string{"go"}
	if err := reg.AddImplementation(serena); err != nil {
		t.Fatalf("AddImplementation: %v", err)
	}

	// Mutating the value we registered must not reach the registry either.
	serena.Constraints.Languages[0] = "rust"

	got, err := reg.Implementation("serena.search")
	if err != nil {
		t.Fatalf("Implementation: %v", err)
	}
	if got.Constraints.Languages[0] != "go" {
		t.Fatal("the registry aliased the caller's slice on write")
	}
	got.Constraints.Languages[0] = "php"

	again, err := reg.Implementation("serena.search")
	if err != nil {
		t.Fatalf("Implementation: %v", err)
	}
	if again.Constraints.Languages[0] != "go" {
		t.Fatal("the registry aliased its own slice on read")
	}
}

func TestConcurrentReadsAndHealthWrites(t *testing.T) {
	reg := seeded(t)
	if err := reg.AddRepository(contract.NewRepository(
		"web", "/srv/web", nil, contract.ScaleSmall, contract.VCSUnspecified, nil)); err != nil {
		t.Fatalf("AddRepository: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 500 {
			state := contract.HealthAlive
			if i%2 == 0 {
				state = contract.HealthDown
			}
			if err := reg.SetHealth("web", "ripgrep", contract.Health{State: state}); err != nil {
				t.Errorf("SetHealth: %v", err)
				return
			}
		}
	}()
	for range 500 {
		impls, err := reg.ImplementationsFor("code.search")
		if err != nil {
			t.Fatalf("ImplementationsFor: %v", err)
		}
		reg.Observed("web", impls)
		reg.Observations("ripgrep")
	}
	<-done
}
