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
// only that method set is promoted and every optional one is dropped. For
// contract.IndexProber that is solved by guardedProber below, and it has to
// be: a probe must be bracketed by Acquire/Release like any other question for
// the far side. But an optional interface that needs NO bracketing gets no
// benefit from a wrapper type, and a type per combination is how two optional
// interfaces become four types and three of them get written wrong. Those are
// found through here instead.
func (g guardedRunner) Unwrap() contract.Runner { return g.Runner }

// guardedProber is guardedRunner for a runner that also answers
// contract.IndexProber.
//
// Two types rather than one because the interface is optional and a type
// assertion is how the core finds it. guardedRunner embeds contract.Runner,
// an interface, so only that method set is promoted: ProbeIndex is dropped
// on the floor, and the runner disappears from the detect sweep with no
// error anywhere -- which is how kivgraph, the first supervised runner that
// also probes, silently reported no index for a graph it was holding. The
// alternative, giving guardedRunner a ProbeIndex of its own, is worse: it
// would make every guarded runner satisfy IndexProber, dragging in the ones
// that opted out deliberately (serena's own comment explains why it must
// not answer this). Deciding at construction keeps both truths.
type guardedProber struct {
	guardedRunner
	prober contract.IndexProber
}

// ProbeIndex brackets the probe exactly as Run brackets a call. A probe is a
// question for the far side like any other: on an on_demand process it is
// usually the first thing to wake it, and without Acquire/Release the idle
// reaper is free to stop the server mid-probe and turn "is there an index"
// into "the provider is down".
func (g guardedProber) ProbeIndex(ctx context.Context, root string) (bool, string, error) {
	// Only the path is known here. That is enough for every provider this
	// can reach today: a probing runner under the shared policy ignores the
	// repository entirely, and the one per_repository provider does not
	// implement IndexProber. A per_repository prober added later would need
	// the id, and would get an unknown-instance failure from EnsureReady
	// rather than a wrong answer.
	id := g.instanceID(contract.Repository{Path: root})
	// Acquired first, for the reason guardedRunner.Run's own comment gives:
	// the pair has to bracket EnsureReady, not follow it, or the reaper can
	// stop the server in the gap between the two.
	g.procs.Acquire(id)
	defer g.procs.Release(id)
	if _, err := g.procs.EnsureReady(ctx, id); err != nil {
		return false, "", guardFailure(err, ctx, id)
	}
	return g.prober.ProbeIndex(ctx, root)
}

// guard wraps runner so the supervisor brackets every call, preserving
// contract.IndexProber when the runner answers it.
func guard(runner contract.Runner, procs *supervisor.Supervisor, instanceID func(contract.Repository) string) contract.Runner {
	guarded := guardedRunner{Runner: runner, procs: procs, instanceID: instanceID}
	if prober, ok := runner.(contract.IndexProber); ok {
		return guardedProber{guardedRunner: guarded, prober: prober}
	}
	return guarded
}

// surfaceOf finds a runner's optional surface report, through any wrapper.
//
// Asked of the wrapper first so a wrapper that ever does implement it wins,
// then down the Unwrap chain. Bounded rather than recursive to a fixed point:
// a cycle here would hang a status screen, and nothing in this package nests
// more than twice.
func surfaceOf(runner contract.Runner) (string, bool) {
	for depth := 0; runner != nil && depth < 4; depth++ {
		if reporter, ok := runner.(contract.SurfaceReporter); ok {
			return reporter.Surface(), true
		}
		unwrapper, ok := runner.(interface{ Unwrap() contract.Runner })
		if !ok {
			return "", false
		}
		runner = unwrapper.Unwrap()
	}
	return "", false
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
	if p := cfg.Orchestrator.Serena.Process; p != nil {
		specs = append(specs, serenaSpecs(*p, cfg.Repositories)...)
	}
	if p := cfg.Orchestrator.Kivgraph.Process; p != nil {
		added, err := kivgraphSpecs(cfg.Source, *p)
		if err != nil {
			return nil, err
		}
		specs = append(specs, added...)
	}
	if p := cfg.Orchestrator.Kivgraph.DashboardProcess; p != nil {
		specs = append(specs, kivgraphDashboardSpec(*p))
	}
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

// kivgraphDashboardSpec is deliberately separate from kivgraphSpecs. The MCP
// server speaks stdio and the viewer speaks HTTP; sharing a process id or
// transport would make the dashboard look healthy while the MCP child was
// actually the one being supervised.
func kivgraphDashboardSpec(p config.ManagedProcess) supervisor.Spec {
	return supervisor.Spec{
		ID:           "kivgraph-dashboard",
		Command:      p.Command,
		Args:         p.Args,
		Env:          p.Env,
		Lifecycle:    p.Lifecycle,
		Port:         p.Port,
		EndpointPath: "/",
		Readiness:    supervisor.ReadinessHTTP,
		RestartLimit: p.RestartLimit,
		RestartDelay: p.RestartDelay,
		StableAfter:  p.StableAfter,
		ReadyTimeout: p.ReadyTimeout,
		StopGrace:    p.StopGrace,
	}
}

// serenaSpecs turns one declaration into the servers it means: one, or one per
// repository.
//
// The supervisor is not told which of the two happened, and that is the point
// of doing the expansion here. It has no idea what a repository is; it owns
// processes with ids, ports and lifecycles. A policy that had to be understood
// twice -- once as a declaration and once as a supervisor concept -- is a
// policy with two places to disagree about what it means.
func serenaSpecs(p config.ManagedProcess, repos []contract.Repository) []supervisor.Spec {
	if p.Instance != config.InstancePerRepository {
		return []supervisor.Spec{serenaSpec(config.RunnerSerena, p, p.Args)}
	}
	// A machine that declared this policy and no repositories gets no
	// servers, which is the honest answer: there is nothing to be per. It is
	// not an error, because the repository list is the thing that will change
	// tomorrow, and a settings file that stops loading when the last
	// repository is commented out would be surprising in the wrong direction.
	specs := make([]supervisor.Spec, 0, len(repos))
	for _, repo := range repos {
		args := make([]string, len(p.Args))
		for i, arg := range p.Args {
			if arg == config.ProjectPlaceholder {
				args[i] = repo.Path
				continue
			}
			args[i] = arg
		}
		specs = append(specs, serenaSpec(serenaInstanceID(repo.ID), p, args))
	}
	return specs
}

// serenaInstanceID names one repository's Serena. The separator is one the
// repository id cannot contain -- ids are lowercase slugs -- so the id round
// trips and a status screen listing `serena@atenea` beside `serena@web` says
// which is which without a lookup.
func serenaInstanceID(repoID string) string {
	return config.RunnerSerena + "@" + repoID
}

// serenaSpec turns the settings file's declaration into what the supervisor
// package needs to launch and watch it. ID and EndpointPath are not the
// user's to set: they are what makes this specifically the Serena adapter's
// process rather than a detail the settings file should carry alongside the
// ones that actually vary between installs.
func serenaSpec(id string, p config.ManagedProcess, args []string) supervisor.Spec {
	return supervisor.Spec{
		ID:           id,
		Command:      p.Command,
		Args:         args,
		Env:          p.Env,
		Lifecycle:    p.Lifecycle,
		Port:         p.Port,
		EndpointPath: "/mcp",
		RestartLimit: p.RestartLimit,
		RestartDelay: p.RestartDelay,
		StableAfter:  p.StableAfter,
		ReadyTimeout: p.ReadyTimeout,
		IdleTimeout:  p.IdleTimeout,
		StopGrace:    p.StopGrace,
	}
}

// kivgraphSpecs turns the settings file's kivgraph declaration into the
// one supervisor.Spec it launches. Unlike Serena there is only ever one:
// the graph is one global corpus, published by atomic generation and read
// by every repository alike (see the adapter's own package doc comment),
// so per_repository has no meaning here and is refused loudly rather than
// silently collapsed into the one shared server a caller might not have
// meant.
func kivgraphSpecs(source string, p config.ManagedProcess) ([]supervisor.Spec, error) {
	if p.Instance == config.InstancePerRepository {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.kivgraph.process.instance is %q, but kivgraph publishes one global "+
				"graph -- only %q is meaningful here", source, config.InstancePerRepository, config.InstanceShared)
	}
	return []supervisor.Spec{stdioSpec(config.RunnerKivgraph, p)}, nil
}

// stdioSpec turns the settings file's declaration into what the supervisor
// package needs to launch and watch it. Transport is TransportStdio, and
// Host, Port and EndpointPath are left zero: a stdio server listens on
// nothing, and the supervisor package itself refuses a spec that sets any of
// them rather than silently ignoring a likely config mistake -- the same
// discipline serenaSpec applies to EndpointPath by hand, just enforced one
// layer further in for this transport.
//
// Shared by both stdio far sides (kivgraph and tokensave) because the spec is
// the same shape for either: only the id and the command differ, and those are
// the caller's.
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
// supervisor.Spec it launches. Like kivgraph there is only ever one, and for
// the same reason read the other way round: the graph belongs to the served
// root, and a second copy of a server pointed at the same root would index
// the same files twice into the same database.
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
