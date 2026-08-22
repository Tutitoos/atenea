package core

import (
	"context"
	"errors"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/supervisor"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// guardedRunner wraps a Runner whose far side Atenea launches and watches
// itself. Every call ensures the process is up before reaching it and
// brackets the call with Acquire/Release, so the idle reaper can never stop
// the server out from under work that is actually using it -- exactly the
// contract those two methods exist for.
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
	if _, err := g.procs.EnsureReady(ctx, id); err != nil {
		return contract.Outcome{}, guardFailure(err, ctx, id)
	}
	g.procs.Acquire(id)
	defer g.procs.Release(id)
	return g.Runner.Run(ctx, req)
}

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
	if _, err := g.procs.EnsureReady(ctx, id); err != nil {
		return false, "", guardFailure(err, ctx, id)
	}
	g.procs.Acquire(id)
	defer g.procs.Release(id)
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
	return contract.Fail(contract.FailureUnavailable, "%s did not come up", id).WithRaw(err.Error())
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

// stopProcesses stops every server Atenea launched itself. A core with
// nothing managed has nothing to do here, the same way settle has nothing to
// write when measuring is off.
func (c *Core) stopProcesses() {
	if c.processes != nil {
		c.processes.Stop()
	}
}
