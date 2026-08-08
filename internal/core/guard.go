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
	if len(specs) == 0 {
		return nil, nil
	}
	return supervisor.New(specs...)
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

// stopProcesses stops every server Atenea launched itself. A core with
// nothing managed has nothing to do here, the same way settle has nothing to
// write when measuring is off.
func (c *Core) stopProcesses() {
	if c.processes != nil {
		c.processes.Stop()
	}
}
