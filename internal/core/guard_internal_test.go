package core

import (
	"context"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// fakeRunner is a bare Runner the wrapper tests embed: it answers nothing the
// guard could promote, so what a test finds through the guard is what the
// guard let through.
type fakeRunner struct{}

func (*fakeRunner) ID() string                { return "fake" }
func (*fakeRunner) Serves(id string) bool     { return id == "fake.overview" }
func (*fakeRunner) Implementations() []string { return []string{"fake.overview"} }
func (*fakeRunner) Capabilities() []string    { return []string{"symbol.overview"} }
func (*fakeRunner) Run(context.Context, contract.RunRequest) (contract.Outcome, error) {
	return contract.Outcome{}, nil
}

// surfacingRunner answers contract.SurfaceReporter, which guardedRunner does
// not promote: it embeds contract.Runner, an interface, so only that method
// set survives the wrapper.
type surfacingRunner struct {
	fakeRunner
	surface string
}

func (s *surfacingRunner) Surface() string { return s.surface }

// The defect this closes, stated plainly: a guarded runner's optional surface
// used to vanish, and the status screen printed "surfaces -" for a provider
// that had one. An interface swallowed by a wrapper, with no error anywhere to
// say so, is solved by Unwrap rather than by a wrapper type per interface,
// because a surface needs no bracketing.
func TestAGuardedRunnersSurfaceIsStillFound(t *testing.T) {
	runner := &surfacingRunner{surface: "service:own-permissions"}
	guarded := guard(runner, nil, func(contract.Repository) string { return "fake" })

	// Through the wrapper, the assertion the status screen used to make on
	// its own fails -- which is exactly why it no longer makes it.
	if _, direct := guarded.(contract.SurfaceReporter); direct {
		t.Log("the wrapper now implements SurfaceReporter itself; surfaceOf still has to work")
	}
	got, ok := surfaceOf(guarded)
	if !ok {
		t.Fatal("a guarded runner's surface was not found")
	}
	if got != "service:own-permissions" {
		t.Errorf("surface = %q", got)
	}
}

// A runner with no surface must not grow one, or every guarded provider would
// print an empty line on the status screen.
func TestARunnerWithoutASurfaceReportsNone(t *testing.T) {
	guarded := guard(&fakeRunner{}, nil, func(contract.Repository) string { return "fake" })
	if surface, ok := surfaceOf(guarded); ok {
		t.Errorf("surface = %q, want none reported", surface)
	}
}

// scopedRunner answers contract.RepositoryReacher, the third optional
// interface guardedRunner does not promote and the reason the lookup above is
// generic rather than copied a third time.
type scopedRunner struct {
	fakeRunner
	reaches bool
}

func (s *scopedRunner) ReachesRepository(contract.Repository) (string, bool) {
	if s.reaches {
		return "", true
	}
	return "serves only /somewhere/else, repository is outside it", false
}

// The same swallowing, one interface later. This is the test that has to exist
// before any adapter implements the interface: a scope declared and then eaten
// by the wrapper looks exactly like a scope never declared, so the provider
// would go on being offered everywhere and the bug would read as "the fix did
// nothing" rather than as a wrapper problem.
func TestAGuardedRunnersRepositoryScopeIsStillFound(t *testing.T) {
	runner := &scopedRunner{reaches: false}
	guarded := guard(runner, nil, func(contract.Repository) string { return "fake" })

	reason, reaches := reachesRepository(guarded, contract.Repository{ID: "elsewhere"})
	if reaches {
		t.Fatal("a guarded runner's declared scope was swallowed by the wrapper")
	}
	if reason == "" {
		t.Error("a refused reach must say why; an empty reason reads as no reason")
	}
}

// Absence is the permissive answer, and that is what keeps the interface
// additive: every adapter that says nothing about scope has to go on being
// offered exactly where it was before this existed.
func TestARunnerThatDeclaresNoScopeReachesEverywhere(t *testing.T) {
	guarded := guard(&fakeRunner{}, nil, func(contract.Repository) string { return "fake" })
	if _, reaches := reachesRepository(guarded, contract.Repository{ID: "anywhere"}); !reaches {
		t.Error("a runner that declares no scope was treated as reaching nowhere")
	}
}
