// Package core wires Atenea together: it turns the settings file into a live
// catalog, owns the selector, and answers "who should do this here".
//
// Atenea decides and delegates. Nothing in this package runs a tool, reads a
// repository or talks to a CLI; that belongs to the adapters, and the adapters
// are dumb translators that do as they are told.
package core

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/adapter/claudecode"
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
	settings config.Config
	catalog  *registry.Registry
	chooser  *selector.Selector
	// runners are the live client adapters, kept as a list because the status
	// screen names each of them; the orchestrator behind them sees one seam.
	runners     []contract.Runner
	checkpoints *checkpoint.Store
	agent       *orchestrator.Agent
	started     time.Time

	mu sync.Mutex
	// sessions is the live chat table. A plain map under the lock the core
	// already holds: it is written twice per chat and read by the status
	// screen, so there is no contention for anything cleverer to relieve.
	sessions map[string]*Session
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
	runners, err := buildRunners(cfg)
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
		Runner:      attach(runners),
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
		runners:     runners,
		checkpoints: checkpoints,
		agent:       agent,
		sessions:    make(map[string]*Session),
		started:     time.Now(),
	}, nil
}

// buildRunners returns the far side of the dispatch seam. A core with no
// runner is still a working core: it can plan and choose, it simply has nobody
// to hand the work to, and the status screen says so out loud.
//
// Which far sides are attached is a settings question, not a code one. More
// than one can be live at a time because omp and Claude Code are both
// first-class clients, and the funnel picks an implementation without caring
// who ends up running it.
func buildRunners(cfg config.Config) ([]contract.Runner, error) {
	out := make([]contract.Runner, 0, len(cfg.Orchestrator.Runners))
	for _, name := range cfg.Orchestrator.Runners {
		runner, err := buildRunner(name, cfg)
		if err != nil {
			return nil, err
		}
		out = append(out, runner)
	}
	if err := checkReach(cfg.Source, out); err != nil {
		return nil, err
	}
	return out, nil
}

func buildRunner(name string, cfg config.Config) (contract.Runner, error) {
	switch name {
	case config.RunnerOMP:
		return omp.New(omp.Options{
			Binary:          cfg.Orchestrator.OMP.Binary,
			Implementations: cfg.Orchestrator.OMP.Implementations,
			Sensitive:       cfg.Security.Sensitive,
			MatchLimit:      cfg.Orchestrator.OMP.MatchLimit,
			Timeout:         cfg.Orchestrator.OMP.Timeout,
		})
	case config.RunnerClaudeCode:
		return claudecode.New(claudecode.Options{
			Binary:          cfg.Orchestrator.ClaudeCode.Binary,
			Implementations: cfg.Orchestrator.ClaudeCode.Implementations,
			Sensitive:       cfg.Security.Sensitive,
			BudgetUSD:       cfg.Orchestrator.ClaudeCode.BudgetUSD,
			Timeout:         cfg.Orchestrator.ClaudeCode.Timeout,
		})
	case config.RunnerLocal:
		return local.New(local.Options{
			Implementations: cfg.Orchestrator.Local.Implementations,
			Sensitive:       cfg.Security.Sensitive,
			SkipDirs:        cfg.Orchestrator.Local.SkipDirs,
		})
	default:
		return nil, contract.Fail(contract.FailureInvalidInput,
			"settings %s: unknown runner %q", cfg.Source, name)
	}
}

// checkReach refuses two live adapters that both claim the same
// implementation.
//
// Dispatch would still work -- the first one asked would answer -- but the
// user would believe something that is not true, and which of two clients ran
// their work is not a detail to decide by declaration order. It is the same
// refusal the selector rules get: a settings file that says something
// impossible is stopped at the door rather than half-honored.
func checkReach(source string, runners []contract.Runner) error {
	owner := map[string]string{}
	for _, runner := range runners {
		for _, id := range runner.Implementations() {
			if first, taken := owner[id]; taken {
				return contract.Fail(contract.FailureInvalidInput,
					"settings %s: %s and %s both serve implementation %s",
					source, first, runner.ID(), id)
			}
			owner[id] = runner.ID()
		}
	}
	return nil
}

// fanOut is the one runner the orchestrator sees when several are attached.
//
// It holds no policy: the funnel has already chosen an implementation, and
// this only carries the request to whoever serves it. checkReach guarantees
// that is at most one, so there is no order to reason about here.
type fanOut []contract.Runner

func (f fanOut) ID() string {
	names := make([]string, len(f))
	for i, runner := range f {
		names[i] = runner.ID()
	}
	return strings.Join(names, "+")
}

func (f fanOut) Serves(implementationID string) bool {
	return slices.ContainsFunc(f, func(r contract.Runner) bool { return r.Serves(implementationID) })
}

func (f fanOut) Implementations() []string {
	var out []string
	for _, runner := range f {
		out = append(out, runner.Implementations()...)
	}
	slices.Sort(out)
	return out
}

func (f fanOut) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	for _, runner := range f {
		if runner.Serves(req.Implementation.ID) {
			return runner.Run(ctx, req)
		}
	}
	// The catalog knows this implementation and no attached client answers for
	// it. That is the unavailable bin, which is what drives fallback to
	// whoever else can do the job.
	return contract.Outcome{}, contract.Fail(contract.FailureUnavailable,
		"no attached runner serves implementation %s", req.Implementation.ID)
}

// attach reduces the live adapters to the single seam the orchestrator takes.
func attach(runners []contract.Runner) contract.Runner {
	switch len(runners) {
	case 0:
		return nil
	case 1:
		// One client needs no routing, and the status screen reads better
		// naming it directly than naming a wrapper around it.
		return runners[0]
	default:
		return fanOut(runners)
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
		Reachable:  c.reach(),
	})
}

// reach is every implementation the attached runners can execute between them.
func (c *Core) reach() []string {
	var out []string
	for _, runner := range c.runners {
		out = append(out, runner.Implementations()...)
	}
	return out
}

// Agent exposes the orchestrator.
func (c *Core) Agent() *orchestrator.Agent { return c.agent }

// Checkpoints exposes the run store.
func (c *Core) Checkpoints() *checkpoint.Store { return c.checkpoints }

// Do hands a commission to the orchestrator on the operator's own behalf.
//
// The core does not explore, split or dispatch: it registers the work so a
// clean stop waits for it, and lets the agent get on with it. Deciding and
// delegating is the whole job.
//
// This is not a way around the chat isolation, it is the console's side of
// it: somebody standing at the terminal is the user, not a chat acting for
// one, and there is nobody above them to ask. A client speaking for a chat
// opens a Session and goes through that.
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
		// Nothing is running and Open is already refusing, so every chat left
		// in the table is inert. Dropping them is what makes the status screen
		// say the true thing: nobody is connected any more.
		c.mu.Lock()
		clear(c.sessions)
		c.mu.Unlock()
		return nil
	case <-time.After(c.settings.Core.ShutdownGrace):
		// The table is left alone on purpose: work is still running, so the
		// chats behind it are still real and saying otherwise would hide it.
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
