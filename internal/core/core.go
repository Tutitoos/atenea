// Package core wires Atenea together: it turns the settings file into a live
// catalog, owns the selector, and answers "who should do this here".
//
// Atenea decides and delegates. Nothing in this package runs a tool, reads a
// repository or talks to a CLI; that belongs to the adapters, and the adapters
// are dumb translators that do as they are told.
package core

import (
	"context"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/adapter/omp"
	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/internal/runner/local"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Core is safe for concurrent use. Sessions are isolated per chat, but the
// catalog underneath them is shared.
type Core struct {
	settings    config.Config
	catalog     *registry.Registry
	chooser     *selector.Selector
	runner      contract.Runner
	checkpoints *checkpoint.Store
	agent       *orchestrator.Agent
	started     time.Time

	mu       sync.Mutex
	stopping bool
	inflight sync.WaitGroup
}

// New builds a core from settings.
func New(cfg config.Config) (*Core, error) {
	catalog := registry.New()
	for _, capability := range cfg.Capabilities {
		if err := catalog.AddCapability(capability); err != nil {
			return nil, err
		}
	}
	for _, impl := range cfg.Implementations {
		if err := catalog.AddImplementation(impl); err != nil {
			return nil, err
		}
	}
	for _, repo := range cfg.Repositories {
		if err := catalog.AddRepository(repo); err != nil {
			return nil, err
		}
	}
	chooser, err := selector.New(cfg.Selector)
	if err != nil {
		return nil, err
	}
	if err := checkRules(catalog, chooser.Rules()); err != nil {
		return nil, err
	}
	runner, err := buildRunner(cfg)
	if err != nil {
		return nil, err
	}
	checkpoints, err := checkpoint.New(cfg.Orchestrator.CheckpointDir)
	if err != nil {
		return nil, err
	}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog:     catalog,
		Chooser:     chooser,
		Runner:      runner,
		Checkpoints: checkpoints,
		MaxParallel: cfg.Orchestrator.MaxParallel,
	})
	if err != nil {
		return nil, err
	}
	return &Core{
		settings:    cfg,
		catalog:     catalog,
		chooser:     chooser,
		runner:      runner,
		checkpoints: checkpoints,
		agent:       agent,
		started:     time.Now(),
	}, nil
}

// buildRunner returns the far side of the dispatch seam. A core with no runner
// is still a working core: it can plan and choose, it simply has nobody to
// hand the work to, and the status screen says so out loud.
//
// Which far side is a settings question, not a code one. The omp adapter is
// the one that ships; the stand-in is what answers on a machine where no
// client is installed.
func buildRunner(cfg config.Config) (contract.Runner, error) {
	switch cfg.Orchestrator.Runner {
	case config.RunnerNone:
		return nil, nil
	case config.RunnerOMP:
		return omp.New(omp.Options{
			Binary:          cfg.Orchestrator.OMP.Binary,
			Implementations: cfg.Orchestrator.OMP.Implementations,
			Sensitive:       cfg.Security.Sensitive,
			MatchLimit:      cfg.Orchestrator.OMP.MatchLimit,
			Timeout:         cfg.Orchestrator.OMP.Timeout,
		})
	case config.RunnerLocal:
		return local.New(local.Options{
			Implementations: cfg.Orchestrator.Local.Implementations,
			Sensitive:       cfg.Security.Sensitive,
			SkipDirs:        cfg.Orchestrator.Local.SkipDirs,
		})
	default:
		return nil, contract.Fail(contract.FailureInvalidInput,
			"settings %s: unknown runner %q", cfg.Source, cfg.Orchestrator.Runner)
	}
}

// checkRules refuses a rule that points at something the catalog does not
// have. A rule silently matching nothing is a preference the user believes is
// in force and is not.
func checkRules(catalog *registry.Registry, rules []selector.Rule) error {
	for _, rule := range rules {
		if _, err := catalog.Capability(rule.Capability); err != nil {
			return contract.Fail(contract.FailureInvalidInput,
				"selector rule prefers %s for unknown capability %s", rule.Prefer, rule.Capability)
		}
		impl, err := catalog.Implementation(rule.Prefer)
		if err != nil {
			return contract.Fail(contract.FailureInvalidInput,
				"selector rule for %s prefers unknown implementation %s", rule.Capability, rule.Prefer)
		}
		if impl.Capability != rule.Capability {
			return contract.Fail(contract.FailureInvalidInput,
				"selector rule for %s prefers %s, which answers %s instead",
				rule.Capability, rule.Prefer, impl.Capability)
		}
		if rule.Repository != "" {
			if _, err := catalog.Repository(rule.Repository); err != nil {
				return contract.Fail(contract.FailureInvalidInput,
					"selector rule for %s names unknown repository %s", rule.Capability, rule.Repository)
			}
		}
	}
	return nil
}

// Registry exposes the catalog.
func (c *Core) Registry() *registry.Registry { return c.catalog }

// Settings exposes the settings the core was built from.
func (c *Core) Settings() config.Config { return c.settings }

// Select answers which implementation should serve a capability on a
// repository, along with the trace that justifies it.
func (c *Core) Select(capabilityID, repositoryID string) (selector.Decision, error) {
	if err := c.enter(); err != nil {
		return selector.Decision{}, err
	}
	defer c.inflight.Done()

	repo, err := c.catalog.Repository(repositoryID)
	if err != nil {
		return selector.Decision{}, err
	}
	candidates, err := c.catalog.ImplementationsFor(capabilityID)
	if err != nil {
		return selector.Decision{}, err
	}
	return c.chooser.Select(selector.Request{
		Capability: capabilityID,
		Repository: repo,
		Candidates: candidates,
	})
}

// Agent exposes the orchestrator.
func (c *Core) Agent() *orchestrator.Agent { return c.agent }

// Checkpoints exposes the run store.
func (c *Core) Checkpoints() *checkpoint.Store { return c.checkpoints }

// Do hands a commission to the orchestrator.
//
// The core does not explore, split or dispatch: it registers the work so a
// clean stop waits for it, and lets the agent get on with it. Deciding and
// delegating is the whole job.
func (c *Core) Do(ctx context.Context, task orchestrator.Task) (*orchestrator.Result, error) {
	if err := c.enter(); err != nil {
		return nil, err
	}
	defer c.inflight.Done()
	return c.agent.Run(ctx, task)
}

// enter registers a unit of in-flight work, refusing it once a stop is under
// way. Taking the count under the same lock that flips the flag is what makes
// the clean stop actually clean: no work can slip in behind it.
func (c *Core) enter() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopping {
		return contract.Fail(contract.FailureUnavailable, "atenea is shutting down")
	}
	c.inflight.Add(1)
	return nil
}

// Run holds the core up until the context is canceled, then stops cleanly.
// This is the service entry point; there is nothing to serve until the first
// adapter exists, so for now it is the lifecycle and nothing more.
func (c *Core) Run(ctx context.Context) error {
	<-ctx.Done()
	return c.Shutdown()
}

// Shutdown refuses new work and gives whatever is running a bounded margin to
// finish. Cutting a writer off mid-flight can leave files half written; waiting
// forever is not an option either, so the margin is configured, not infinite.
func (c *Core) Shutdown() error {
	c.mu.Lock()
	if c.stopping {
		c.mu.Unlock()
		return nil
	}
	c.stopping = true
	c.mu.Unlock()

	done := make(chan struct{})
	go func() {
		c.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(c.settings.Core.ShutdownGrace):
		return contract.Fail(contract.FailureTimeout,
			"in-flight work did not finish within %s", c.settings.Core.ShutdownGrace)
	}
}

// Stopping reports whether a shutdown has begun.
func (c *Core) Stopping() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopping
}

// Uptime since the core was built.
func (c *Core) Uptime() time.Duration { return time.Since(c.started) }

// Version of the running binary.
func (c *Core) Version() string { return buildinfo.Version }
