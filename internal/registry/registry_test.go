package registry_test

import (
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

func mustErr[T any](_ T, err error) error { return err }

// Health is the one block that moves while Atenea runs. Everything else is
// declarative, so the registry only exposes this mutator.
func TestSetHealthReplacesTheBlock(t *testing.T) {
	reg := seeded(t)
	want := contract.Health{State: contract.HealthDown, Reason: "container exited"}
	if err := reg.SetHealth("serena.search", want); err != nil {
		t.Fatalf("SetHealth: %v", err)
	}
	got, err := reg.Implementation("serena.search")
	if err != nil {
		t.Fatalf("Implementation: %v", err)
	}
	if got.Health.State != contract.HealthDown || got.Health.Reason != "container exited" {
		t.Fatalf("health = %+v", got.Health)
	}
	if err := reg.SetHealth("nope", want); contract.KindOf(err) != contract.FailureNotFound {
		t.Errorf("unknown implementation: kind = %v", contract.KindOf(err))
	}
	if err := reg.SetHealth("ripgrep", contract.Health{Score: 2}); err == nil {
		t.Error("out-of-range score should fail")
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 500 {
			state := contract.HealthAlive
			if i%2 == 0 {
				state = contract.HealthDown
			}
			if err := reg.SetHealth("ripgrep", contract.Health{State: state}); err != nil {
				t.Errorf("SetHealth: %v", err)
				return
			}
		}
	}()
	for range 500 {
		if _, err := reg.ImplementationsFor("code.search"); err != nil {
			t.Fatalf("ImplementationsFor: %v", err)
		}
	}
	<-done
}
