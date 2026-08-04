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
	id    string
}

func (g guardedRunner) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	if _, err := g.procs.EnsureReady(ctx, g.id); err != nil {
		return contract.Outcome{}, guardFailure(err, ctx, g.id)
	}
	g.procs.Acquire(g.id)
	defer g.procs.Release(g.id)
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
		specs = append(specs, serenaSpec(*p))
	}
	if len(specs) == 0 {
		return nil, nil
	}
	return supervisor.New(specs...)
}

// serenaSpec turns the settings file's declaration into what the supervisor
// package needs to launch and watch it. ID and EndpointPath are not the
// user's to set: they are what makes this specifically the Serena adapter's
// process rather than a detail the settings file should carry alongside the
// ones that actually vary between installs.
func serenaSpec(p config.ManagedProcess) supervisor.Spec {
	return supervisor.Spec{
		ID:           config.RunnerSerena,
		Command:      p.Command,
		Args:         p.Args,
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
