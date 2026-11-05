package core

import (
	"context"
	"errors"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/supervisor"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// guardedRunner wraps a Runner whose far side Atenea launches and watches
// itself. Every call brackets the whole of ensuring-and-reaching with
// Acquire/Release, so the idle reaper can never stop the server out from
// under work that is actually using it -- exactly the contract those two
// methods exist for. The bracket has to open before EnsureReady rather than
// after it: EnsureReady holds nothing when it returns, so an Acquire on the
// next line is a gap the reaper can fit a stop into.
//
// Everything but Run is the wrapped Runner's own: an adapter still decides
// what it Serves and what it answers for, the same as an unmanaged one. Only
// reaching the far side changes.
type guardedRunner struct {
	contract.Runner
	procs *supervisor.Supervisor
	// instanceID names the process this request needs. Under a shared policy
	// it ignores the request and answers the one id; under per_repository it
	// is the whole difference between waking the server holding this
	// repository and waking somebody else's.
	instanceID func(repo contract.Repository) string
}

func (g guardedRunner) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	id := g.instanceID(req.Repository)
	// Acquired before the server is ensured, not after, and the order is the
	// whole guarantee. EnsureReady returns without holding anything: between
	// its return and an Acquire placed after it, the idle reaper's stopIfIdle
	// sees state==StateReady, inflight==0 and a lastUsed old enough, and asks
	// the very server this call is about to use to stop -- so the call lands
	// on a process shutting down and the caller is told the provider is
	// unavailable, on a machine where nothing was wrong. Claiming the count
	// first closes the window: inflight is non-zero for the whole of
	// EnsureReady, so no tick of the reaper in between can find it idle.
	// Acquire on an id the supervisor does not know is a no-op, so doing it
	// before the id has been validated costs nothing.
	g.procs.Acquire(id)
	defer g.procs.Release(id)
	if _, err := g.procs.EnsureReady(ctx, id); err != nil {
		return contract.Outcome{}, guardFailure(err, ctx, id)
	}
	return g.Runner.Run(ctx, req)
}

// Unwrap returns the runner underneath, so an optional interface the wrapper
// does not itself implement can still be found.
//
// It exists because guardedRunner embeds contract.Runner -- an interface -- so
// only that method set is promoted and every optional one is dropped. An
// optional interface that needs no bracketing gets no benefit from a wrapper
// type, and a type per combination is how two optional interfaces become four
// types and three of them get written wrong. Those are found through here
// instead.
func (g guardedRunner) Unwrap() contract.Runner { return g.Runner }

// guard wraps runner so the supervisor brackets every call.
func guard(runner contract.Runner, procs *supervisor.Supervisor, instanceID func(contract.Repository) string) contract.Runner {
	return guardedRunner{Runner: runner, procs: procs, instanceID: instanceID}
}

// optional finds a runner's optional interface, through any wrapper.
//
// Asked of the wrapper first so a wrapper that ever does implement it wins,
// then down the Unwrap chain. Bounded rather than recursive to a fixed point:
// a cycle here would hang a status screen. Production dispatch can include
// stats, cache, commission, filesystem and supervisor wrappers before the
// provider, so the bound leaves room for those layers.
//
// Generic on the third caller rather than the second. guardedRunner embeds
// contract.Runner, an interface, so only that method set is promoted and every
// optional one is swallowed -- the bug that printed `surfaces -` for a
// provider that had one. Written once for SurfaceReporter, it would have been
// copied twice by now; a lookup that each caller reimplements is a lookup
// where one of them eventually forgets the depth bound.
func optional[T any](runner contract.Runner) (T, bool) {
	var zero T
	for depth := 0; runner != nil && depth < 8; depth++ {
		if found, ok := runner.(T); ok {
			return found, true
		}
		unwrapper, ok := runner.(interface{ Unwrap() contract.Runner })
		if !ok {
			return zero, false
		}
		runner = unwrapper.Unwrap()
	}
	return zero, false
}

// surfaceOf finds a runner's optional surface report, through any wrapper.
func surfaceOf(runner contract.Runner) (string, bool) {
	reporter, ok := optional[contract.SurfaceReporter](runner)
	if !ok {
		return "", false
	}
	return reporter.Surface(), true
}

// reachesRepository asks a runner whether it can answer for repo at all.
//
// A runner that does not implement contract.RepositoryReacher reaches
// everywhere it is attached, so absence answers yes. That default is what
// keeps this additive: every provider but the rooted shape says nothing here
// and is offered exactly as before.
func reachesRepository(runner contract.Runner, repo contract.Repository) (string, bool) {
	reacher, ok := optional[contract.RepositoryReacher](runner)
	if !ok {
		return "", true
	}
	return reacher.ReachesRepository(repo)
}

// guardFailure sorts an EnsureReady error into the shared bins, mirroring
// each adapter's own failureFor: whatever the supervisor says, the core only
// ever sees one of the six, with the untranslated text kept beside it.
func guardFailure(err error, ctx context.Context, id string) *contract.Failure {
	var known *contract.Failure
	if errors.As(err, &known) {
		return known
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return contract.Fail(contract.StopKind(ctxErr),
			"%s did not come up before the call ended", id).WithRaw(err.Error())
	}
	// The reason in the message and not only in Raw. A failure traveling up
	// through the funnel is re-summarized -- "every implementation of X is
	// down" -- and Raw does not survive that trip, so a message that said
	// only "did not come up" left the operator with nothing to act on for the
	// commonest cause of all: a provider whose binary is not installed.
	return contract.Fail(contract.FailureUnavailable,
		"%s did not come up: %v", id, err).WithRaw(err.Error())
}

// buildSupervisor collects every managed-process spec the settings file
// declared and returns the one Supervisor that owns all of them. nil means
// nothing is managed, which is not an error -- the same way an empty runners
// list plans and chooses with nobody to dispatch to instead of refusing to
// boot.
func buildSupervisor(cfg config.Config) (*supervisor.Supervisor, error) {
	var specs []supervisor.Spec
	if p := cfg.Orchestrator.Tokensave.Process; p != nil {
		added, err := tokensaveSpecs(cfg.Source, *p)
		if err != nil {
			return nil, err
		}
		specs = append(specs, added...)
	}
	if p := cfg.Orchestrator.Desktop.Process; p != nil {
		added, err := desktopSpecs(cfg.Source, *p)
		if err != nil {
			return nil, err
		}
		specs = append(specs, added...)
	}
	if len(specs) == 0 {
		return nil, nil
	}
	return supervisor.New(specs...)
}

// stdioSpec turns the settings file's declaration into what the supervisor
// package needs to launch and watch it. Transport is TransportStdio, and
// Host, Port and EndpointPath are left zero: a stdio server listens on
// nothing, and the supervisor package itself refuses a spec that sets any of
// them rather than silently ignoring a likely config mistake.
//
// Shared by the stdio far sides (tokensave and the desktop helper) because the
// spec is the same shape for either: only the id and the command differ, and
// those are the caller's.
func stdioSpec(id string, p config.ManagedProcess) supervisor.Spec {
	return supervisor.Spec{
		ID:           id,
		Transport:    supervisor.TransportStdio,
		Command:      p.Command,
		Args:         p.Args,
		Env:          p.Env,
		Lifecycle:    p.Lifecycle,
		RestartLimit: p.RestartLimit,
		RestartDelay: p.RestartDelay,
		StableAfter:  p.StableAfter,
		ReadyTimeout: p.ReadyTimeout,
		IdleTimeout:  p.IdleTimeout,
		StopGrace:    p.StopGrace,
	}
}

// tokensaveSpecs turns the settings file's tokensave declaration into the one
// supervisor.Spec it launches. There is only ever one: the graph belongs to
// the served root, and a second copy of a server pointed at the same root
// would index the same files twice into the same database.
func tokensaveSpecs(source string, p config.ManagedProcess) ([]supervisor.Spec, error) {
	if p.Instance == config.InstancePerRepository {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.tokensave.process.instance is %q, but tokensave serves one rooted "+
				"project -- only %q is meaningful here", source, config.InstancePerRepository, config.InstanceShared)
	}
	return []supervisor.Spec{stdioSpec(config.RunnerTokensave, p)}, nil
}

// desktopSpecs registers the helper. There is one screen, one pointer and one
// keyboard on this machine, so `shared` is not a default here but the only
// coherent answer: a second helper would be a second thing moving the same
// mouse, and per_repository would make that depend on how many projects
// happen to be open.
func desktopSpecs(source string, p config.ManagedProcess) ([]supervisor.Spec, error) {
	if p.Instance == config.InstancePerRepository {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.desktop.process.instance is %q, but there is one desktop on "+
				"this machine -- only %q is meaningful here", source, config.InstancePerRepository, config.InstanceShared)
	}
	return []supervisor.Spec{stdioSpec(config.RunnerDesktop, p)}, nil
}

// stopProcesses stops every server Atenea launched itself. A core with
// nothing managed has nothing to do here, the same way settle has nothing to
// write when measuring is off.
func (c *Core) stopProcesses() {
	if c.processes != nil {
		c.processes.Stop()
	}
}
