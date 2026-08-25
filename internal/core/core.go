// Package core wires Atenea together: it turns the settings file into a live
// catalog, owns the selector, and answers "who should do this here".
//
// Atenea decides and delegates. Nothing in this package runs a tool, reads a
// repository or talks to a CLI; that belongs to the adapters, and the adapters
// are dumb translators that do as they are told.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tutitoos/atenea/internal/adapter/claudecode"
	"github.com/Tutitoos/atenea/internal/adapter/codex"
	"github.com/Tutitoos/atenea/internal/adapter/desktop"
	"github.com/Tutitoos/atenea/internal/adapter/kivgraph"
	"github.com/Tutitoos/atenea/internal/adapter/omp"
	"github.com/Tutitoos/atenea/internal/adapter/serena"
	"github.com/Tutitoos/atenea/internal/adapter/tokensave"
	"github.com/Tutitoos/atenea/internal/backup"
	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/clock"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/ipc"
	"github.com/Tutitoos/atenea/internal/mcpprobe"
	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/passthrough"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/internal/runner/local"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/internal/supervisor"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// rawBackend is a live session paired with the declaration that authorized
// it. The two are one lookup on purpose: every call needs the connection and
// the permission together, and holding them in separate maps is how one of
// them eventually gets consulted without the other.
type rawBackend struct {
	passthrough.Backend
	declared config.MCPServer
	spec     passthrough.Spec
	instance config.Instance
}

// Core is safe for concurrent use. Sessions are isolated per chat, but the
// catalog underneath them is shared.
type Core struct {
	settings config.Config
	catalog  *registry.Registry
	// backends are the declared servers whose own tools are re-offered
	// verbatim, keyed by the id that forms the middle segment of their tool
	// names. Empty on a machine that declared none, which is every machine
	// until somebody writes `expose = "raw"`.
	//
	// They are not runners and never reach the orchestrator: nothing here
	// answers a capability, so there is nothing for the funnel to rank and
	// nothing for the measurement base to learn.
	backends map[string]rawBackend
	// readings is what the last exchange with each backend left behind, so a
	// backend that dropped out of tools/list can be named on the status screen
	// instead of just being absent from it. Written from the seams that talk
	// to a backend, never at startup: nothing here probes anything, which is
	// what keeps the three promises documented at the backends map above.
	readings *backendMemory
	chooser  *selector.Selector
	// runners are the live client adapters, kept as a list because the status
	// screen names each of them; the orchestrator behind them sees one seam.
	runners     []contract.Runner
	checkpoints *checkpoint.Store
	// measurements is the baseline, and the core is its only writer. It is nil
	// when the settings turned measuring off.
	measurements *metrics.Store
	// notebook is the crash notebook. Always present: it needs no settings and
	// there is no state of the world in which not having one is better.
	notebook *notebook.Notebook
	// copies protects the history. Nil when the settings turned copying off,
	// and read by the status screen even then so the screen can say so rather
	// than say nothing.
	copies *backup.Store
	// recovered is what the last ugly close cost, assessed before any work
	// was accepted. It is kept so the status screen can say so: a start that
	// had to repair something is a fact about this process, and finding it
	// only in the notebook would mean reading a file to learn why the
	// history looks shorter than it did yesterday.
	recovered Recovery
	// beats serves every background maintenance task in one lane, so the
	// flush, the roll-up and whatever comes next cannot come due at the same
	// second and fight over the same file.
	beats *clock.Clock
	// processes is every MCP server Atenea launches and watches on its own
	// behalf. nil when the settings file managed nothing, which is not a
	// degraded state: every adapter still works exactly as it did before
	// this existed, reached at whatever Endpoint it was given.
	processes *supervisor.Supervisor
	agent     *orchestrator.Agent

	started time.Time
	// role is what this process is allowed to maintain. It is kept so the
	// status screen can say which one it is talking to: a command reporting a
	// clock it is not running would be describing somebody else's process.
	role Role
	// upkeep releases the claim, and is nil for a command -- there is nothing
	// to release when nothing was claimed.
	upkeep func()
	// socket is the door clients knock on, and only the service opens one.
	// Nil in a command, which is what makes "a command never answers for the
	// machine" true rather than merely intended.
	socket *ipc.Listener
	// conns counts answers in flight, so a stop waits for a caller mid-question
	// instead of hanging up on it.
	conns sync.WaitGroup

	mu sync.Mutex
	// sessions is the live chat table. A plain map under the lock the core
	// already holds: it is written twice per chat and read by the status
	// screen, so there is no contention for anything cleverer to relieve.
	sessions map[string]*Session
	stopping bool
	// stopped is closed by whoever ran the teardown, and stopErr is what that
	// teardown returned. They exist so a second Shutdown can wait for the
	// first one and report the same answer, instead of returning nil while
	// the tree is still being written -- see Shutdown.
	stopped  chan struct{}
	stopErr  error
	inflight sync.WaitGroup
}

// Job names for the maintenance lane. They are the handle Settle and the
// shutdown path reach for, so they are constants rather than strings typed
// twice.
const (
	jobFlush     = "metrics.flush"
	jobCompact   = "metrics.compact"
	jobBackup    = "backup"
	jobRetention = "retention"
	jobMCPHealth = "mcp.health"
)

// meter is the orchestrator's end of the measurement seam.
//
// Recording is a memory append on the hot path of real work, so it goes
// straight to the store's buffer. Settling is disk, so it goes through the
// clock: the design puts every maintenance task in one lane, and a phase
// closing is no exception just because something asked for it by hand.
type meter struct {
	store *metrics.Store
	beats *clock.Clock
}

func (m meter) Record(x metrics.Measurement) { m.store.Record(x) }

// Settle pushes the batch and swallows the error on purpose. A flush that
// could not get the file keeps its rows and tries again on the next beat, so
// there is nothing to lose and nothing a caller in the middle of a commission
// could usefully do about it. What did not make it is visible on the status
// screen instead.
func (m meter) Settle(ctx context.Context) { _ = m.beats.Do(ctx, jobFlush) }

// Role says whether this Core is the process responsible for the state on disk
// or one of the many that merely use it. Exactly one of the two is allowed to
// perform upkeep -- the receipt sweep and, once started, the clock's lanes --
// because every one of those tasks coordinates through an in-process lock and
// so cannot be done twice at once without the two passes fighting.
//
// The distinction was true by accident before it was declared: only `atenea
// run` ever called Run, so only the service ever ticked. The sweep had no such
// luck and ran on every construction, which is the bug this closes.
type Role int

const (
	// Service is the long-lived process: it sweeps, it ticks, and it is the
	// one that will hold the socket.
	Service Role = iota
	// Command is every one-shot subcommand. It dispatches and reads freely;
	// it touches nobody's upkeep.
	Command
)

// String names the role for a message a person has to read.
func (r Role) String() string {
	if r == Command {
		return "command"
	}
	return "service"
}

// New builds a core from settings. The role decides whether this process
// performs the upkeep that must happen exactly once; see Role.
func New(cfg config.Config, role Role) (*Core, error) {
	catalog, err := registry.NewWithState(filepath.Join(platform.StateDir(), "registry-state.json"))
	if err != nil {
		return nil, err
	}
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
	runners, procs, err := buildRunners(cfg)
	if err != nil {
		return nil, err
	}
	checkpoints, err := checkpoint.New(cfg.Orchestrator.CheckpointDir)
	if err != nil {
		return nil, err
	}
	// The notebook comes before the store on purpose: everything from here on
	// can fall over, and the point of the file is that the fall gets written
	// down. It needs no settings of its own -- a crash notebook you have to
	// configure before it works is one that is off when you need it.
	book, err := notebook.New(notebook.DefaultPath())
	if err != nil {
		return nil, err
	}
	// The upkeep is claimed before anything is swept, ticked or opened, because
	// the claim is the right to do any of it. A second service is refused here,
	// which is also why it never reaches the measurement base: two services
	// contending for that lock would answer slowly instead of clearly.
	var upkeep func()
	built := false
	if role == Service {
		// Before the claim, so a refusal here does not have to give one back.
		if err := groundedRepositories(cfg); err != nil {
			return nil, err
		}
		upkeep, err = claimUpkeep()
		if err != nil {
			return nil, err
		}
		// A construction that falls over below must not leave the claim behind:
		// the next start would be refused on behalf of nobody.
		defer func() {
			if !built {
				upkeep()
			}
		}()
	}
	// The damage assessment, before anything is served. Receipts first: an
	// interrupted dump is swept and a torn one set aside, so nothing that
	// follows reads a record of a run that never happened that way.
	//
	// Only the service does this. A command cannot tell an abandoned temporary
	// file from one the service has open this instant, and the pass that
	// decides holds a mutex this process does not share with it.
	var found Recovery
	if role == Service {
		found, err = recoverReceipts(checkpoints)
		if err != nil {
			return nil, err
		}
	}
	var store *metrics.Store
	if cfg.Metrics.Enabled {
		store, found.BaseSetAside, err = openBase(cfg.Metrics)
		if err != nil {
			return nil, err
		}
	}
	copies, err := openCopies(cfg.Backup, platform.StateDir(), config.DefaultPath())
	if err != nil {
		return nil, err
	}
	readings, err := newBackendMemory(filepath.Join(platform.StateDir(), "mcp-health.json"))
	if err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"mcp health state: %v", err)
	}
	var health func(context.Context) error
	if role == Service && cfg.Core.HealthProbeEvery > 0 && len(cfg.MCPServers) > 0 {
		health = func(ctx context.Context) error {
			return probeDeclaredServers(ctx, cfg.MCPServers, readings)
		}
	}
	beats, err := buildLanes(cfg, store, copies, checkpoints, book, health)
	if err != nil {
		return nil, err
	}
	fileRecovery(book, found)
	var collector orchestrator.Meter
	// Both seams stay nil together when there is no store. Assigning a nil
	// *Store into an interface would produce a non-nil interface holding
	// nothing, and every "is the base attached?" check downstream would answer
	// yes and then panic on the first read.
	var base orchestrator.Base
	if store != nil {
		collector = meter{store: store, beats: beats}
		base = store
		// The history is put in shape when the database is opened, through the
		// same lane everything else uses. The mark on disk is what keeps this
		// from happening on every command: most passes find nothing due and
		// cost one look at one row. A pass that does fail is not fatal --
		// nothing has been lost, the rows are all still there -- so the core
		// comes up either way and the next one tries again.
		_ = beats.Do(context.Background(), jobCompact)
	}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog:     catalog,
		Chooser:     chooser,
		Runner:      attach(runners, book),
		Checkpoints: checkpoints,
		Meter:       collector,
		// The same store on both seams, which is the point: what a step cost
		// on the way out is what the funnel ranks on next time in. A nil store
		// leaves both off together -- a core that never learns is at least
		// consistent about it.
		Base:            base,
		Notebook:        book,
		MaxParallel:     cfg.Orchestrator.MaxParallel,
		BudgetUSD:       cfg.Orchestrator.BudgetUSD,
		StandingEffects: cfg.Orchestrator.StandingEffects,
	})
	if err != nil {
		return nil, err
	}
	// The declared backends, held for the whole life of the process rather
	// than per chat -- which is the entire point of declaring them. Nothing
	// is dialed and nothing is spawned here: a backend that is down at
	// startup must not stop Atenea from starting, one that comes up later
	// must start working without a restart, and a stdio server nobody has
	// asked for yet is a process nobody should be paying for.
	backends := make(map[string]rawBackend)
	for _, server := range cfg.MCPServers {
		if server.Expose != config.ExposeRaw {
			continue
		}
		instance := server.Instance
		if instance == "" {
			instance = config.InstanceShared
		}
		spec := passthrough.Spec{
			ID:      server.ID,
			URL:     server.URL,
			Command: server.Command,
			Env:     server.Env,
			Timeout: server.Timeout,
			Allowed: server.Tools,
		}
		var backend passthrough.Backend
		if instance != config.InstancePerChat {
			backend = passthrough.New(spec)
		}
		backends[server.ID] = rawBackend{
			Backend:  backend,
			declared: server,
			spec:     spec,
			instance: instance,
		}
	}
	built = true
	return &Core{
		settings:     cfg,
		backends:     backends,
		readings:     readings,
		catalog:      catalog,
		chooser:      chooser,
		runners:      runners,
		checkpoints:  checkpoints,
		measurements: store,
		notebook:     book,
		copies:       copies,
		recovered:    found,
		beats:        beats,
		processes:    procs,
		agent:        agent,
		sessions:     make(map[string]*Session),
		started:      time.Now(),
		role:         role,
		upkeep:       upkeep,
	}, nil
}

// maintenance turns a failed background job into an incident.
//
// It reports two different facts and keeps them apart. A job that failed is
// recoverable: the rows are still in the buffer and the next beat will try
// again. Measurements dropped at the buffer ceiling are not -- that is the
// baseline being quietly falsified, and it is the one thing here worth waking
// up for.
type maintenance struct {
	book  *notebook.Notebook
	store *metrics.Store
	// reported is the drop count already written down, so a ceiling that
	// stays breached does not file the same incident on every beat.
	reported atomic.Int64
}

func (m *maintenance) wrap(op string, run func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		err := run(ctx)
		if err != nil {
			_ = m.book.Record(notebook.Incident{
				Op:      op,
				Detail:  err.Error(),
				Version: buildinfo.Full(),
			})
		}
		m.checkDrops()
		return err
	}
}

// checkDrops files an incident for measurements the store threw away.
//
// The count is cumulative and only ever grows, so the incident says how many
// went since the last one rather than how many there have ever been: a
// notebook read a week later should be able to say when the losses happened,
// not just that they did.
func (m *maintenance) checkDrops() {
	if m.store == nil {
		return
	}
	dropped := int64(m.store.Dropped())
	seen := m.reported.Swap(dropped)
	if dropped <= seen {
		return
	}
	_ = m.book.Record(notebook.Incident{
		Op: "metrics.dropped",
		Detail: fmt.Sprintf(
			"%d measurements were dropped at the buffer ceiling and are gone; the baseline is short by that much",
			dropped-seen),
		Version: buildinfo.Full(),
	})
}

// buildRunners returns the far side of the dispatch seam. A core with no
// runner is still a working core: it can plan and choose, it simply has nobody
// to hand the work to, and the status screen says so out loud.
//
// Which far sides are attached is a settings question, not a code one. More
// than one can be live at a time because omp and Claude Code are both
// first-class clients, and the funnel picks an implementation without caring
// who ends up running it.
func buildRunners(cfg config.Config) ([]contract.Runner, *supervisor.Supervisor, error) {
	procs, err := buildSupervisor(cfg)
	if err != nil {
		return nil, nil, err
	}
	out := make([]contract.Runner, 0, len(cfg.Orchestrator.Runners))
	for _, name := range cfg.Orchestrator.Runners {
		runner, err := buildRunner(name, cfg, procs)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, runner)
	}
	if err := checkReach(cfg.Source, out); err != nil {
		return nil, nil, err
	}
	if err := checkDispatch(cfg.Source, cfg.Implementations, out); err != nil {
		return nil, nil, err
	}
	return out, procs, nil
}

func buildRunner(name string, cfg config.Config, procs *supervisor.Supervisor) (contract.Runner, error) {
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
			Source:          cfg.Orchestrator.ClaudeCode.Source,
			TerminalBinary:  cfg.Orchestrator.ClaudeCode.TerminalBinary,
			AppBinary:       cfg.Orchestrator.ClaudeCode.AppBinary,
			Implementations: cfg.Orchestrator.ClaudeCode.Implementations,
			Sensitive:       cfg.Security.Sensitive,
			Timeout:         cfg.Orchestrator.ClaudeCode.Timeout,
		})
	case config.RunnerCodex:
		return codex.New(codex.Options{
			Binary:          cfg.Orchestrator.Codex.Binary,
			Source:          cfg.Orchestrator.Codex.Source,
			TerminalBinary:  cfg.Orchestrator.Codex.TerminalBinary,
			AppBinary:       cfg.Orchestrator.Codex.AppBinary,
			Implementations: cfg.Orchestrator.Codex.Implementations,
			Sensitive:       cfg.Security.Sensitive,
			Timeout:         cfg.Orchestrator.Codex.Timeout,
		})
	case config.RunnerSerena:
		return buildSerenaRunner(cfg, procs)
	case config.RunnerKivgraph:
		return buildKivgraphRunner(cfg, procs)
	case config.RunnerTokensave:
		return buildTokensaveRunner(cfg, procs)
	case config.RunnerDesktop:
		return buildDesktopRunner(cfg, procs)
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

// buildSerenaRunner builds the Serena adapter. Unmanaged, it is reached at
// whatever Endpoint the settings file declared, unchanged from before
// Process existed. Managed, the real far side is whatever port the
// supervisor actually chose -- never the settings file's Endpoint, which a
// managed process does not need and might not even agree with -- and every
// call is guarded so the process is running before the adapter ever sees it.
func buildSerenaRunner(cfg config.Config, procs *supervisor.Supervisor) (contract.Runner, error) {
	endpoint := cfg.Orchestrator.Serena.Endpoint
	managed := cfg.Orchestrator.Serena.Process
	if managed != nil {
		// Checked before it is discarded. The supervisor's address is the one
		// that gets dialed, but a written endpoint that could never work is
		// still a mistake, and letting a process table excuse it would mean
		// deleting that table later turns a file that always loaded into one
		// that suddenly does not.
		if err := serena.ValidateEndpoint(endpoint); err != nil {
			return nil, err
		}
	}
	// One function answers both halves, because they are one question asked
	// twice: which process serves this repository. The guard wakes it and the
	// adapter dials it, and if those two ever disagreed the adapter would be
	// talking to a server nobody had started.
	instanceID := func(contract.Repository) string { return config.RunnerSerena }
	if managed != nil && managed.Instance == config.InstancePerRepository {
		instanceID = func(repo contract.Repository) string { return serenaInstanceID(repo.ID) }
	}
	opts := serena.Options{
		Endpoint:        endpoint,
		Implementations: cfg.Orchestrator.Serena.Implementations,
		Sensitive:       cfg.Security.Sensitive,
		Timeout:         cfg.Orchestrator.Serena.Timeout,
	}
	if managed != nil {
		if managed.Instance == config.InstancePerRepository {
			// There is no single address to hand over: each repository has
			// its own, and the adapter asks per call.
			opts.EndpointFor = func(repo contract.Repository) (string, error) {
				return procs.Endpoint(instanceID(repo))
			}
		} else {
			resolved, err := procs.Endpoint(config.RunnerSerena)
			if err != nil {
				return nil, err
			}
			opts.Endpoint = resolved
		}
	}
	runner, err := serena.New(opts)
	if err != nil {
		return nil, err
	}
	if managed == nil {
		return runner, nil
	}
	return guard(runner, procs, instanceID), nil
}

// buildKivgraphRunner builds the kivgraph adapter. Unlike Serena there is
// no unmanaged mode to fall back to: a stdio server has no address, only
// two pipes, so a settings file that names this runner without a Process
// block has nothing this adapter could ever dial -- refused here rather
// than reaching Run only to fail on the first call.
func buildKivgraphRunner(cfg config.Config, procs *supervisor.Supervisor) (contract.Runner, error) {
	if cfg.Orchestrator.Kivgraph.Process == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"settings %s: kivgraph has no process to launch -- a stdio server has no address to dial without one",
			cfg.Source)
	}
	runner, err := kivgraph.New(kivgraph.Options{
		Implementations: cfg.Orchestrator.Kivgraph.Implementations,
		Sensitive:       cfg.Security.Sensitive,
		Timeout:         cfg.Orchestrator.Kivgraph.Timeout,
		Session: func(ctx context.Context) (*mcpstdio.Session, error) {
			return procs.Session(config.RunnerKivgraph)
		},
		Index: func(ctx context.Context, root, mode string) (kivgraph.IndexReport, error) {
			process := cfg.Orchestrator.Kivgraph.Process
			if process == nil {
				return kivgraph.IndexReport{}, contract.Fail(contract.FailureUnavailable,
					"settings %s: kivgraph index has no process declaration", cfg.Source)
			}
			return kivgraph.RunConfiguredIndex(ctx, process.Command, process.Env, root, mode)
		},
	})
	if err != nil {
		return nil, err
	}
	// The declaration is instance = "shared" only (see kivgraphSpecs in
	// guard.go, which refuses per_repository before a Supervisor is ever
	// built), so the instance id every call guards against is the one
	// constant name, never a per-repository lookup the way Serena needs.
	instanceID := func(contract.Repository) string { return config.RunnerKivgraph }
	return guard(runner, procs, instanceID), nil
}

// buildTokensaveRunner builds the tokensave adapter. Same refusal as
// Kivgraph's for a missing process table -- a stdio server has no address --
// plus one of its own: the root every path on that wire is relative to has to
// be declared, because this adapter translates repository-relative paths in
// both directions and cannot invent where the served project begins.
func buildTokensaveRunner(cfg config.Config, procs *supervisor.Supervisor) (contract.Runner, error) {
	if cfg.Orchestrator.Tokensave.Process == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"settings %s: tokensave has no process to launch -- a stdio server has no address to dial without one",
			cfg.Source)
	}
	if cfg.Orchestrator.Tokensave.Root == "" {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"settings %s: orchestrator.tokensave.root is required -- every path tokensave reports is relative to it",
			cfg.Source)
	}
	runner, err := tokensave.New(tokensave.Options{
		Root:            cfg.Orchestrator.Tokensave.Root,
		Implementations: cfg.Orchestrator.Tokensave.Implementations,
		Sensitive:       cfg.Security.Sensitive,
		Timeout:         cfg.Orchestrator.Tokensave.Timeout,
		Session: func(ctx context.Context) (*mcpstdio.Session, error) {
			return procs.Session(config.RunnerTokensave)
		},
	})
	if err != nil {
		return nil, err
	}
	// One server for the whole root, so one instance id: the graph is the
	// project's, not a repository's, exactly as with Kivgraph's global corpus.
	instanceID := func(contract.Repository) string { return config.RunnerTokensave }
	return guard(runner, procs, instanceID), nil
}

func buildDesktopRunner(cfg config.Config, procs *supervisor.Supervisor) (contract.Runner, error) {
	if cfg.Orchestrator.Desktop.Process == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"settings %s: desktop has no helper to launch -- everything it does lives behind macOS "+
				"APIs in a separate process, and there is no address to dial without one", cfg.Source)
	}
	runner, err := desktop.New(desktop.Options{
		Implementations: cfg.Orchestrator.Desktop.Implementations,
		Timeout:         cfg.Orchestrator.Desktop.Timeout,
		Session: func(context.Context) (*mcpstdio.Session, error) {
			return procs.Session(config.RunnerDesktop)
		},
	})
	if err != nil {
		return nil, err
	}
	// One desktop, so one instance id, and it does not vary by repository:
	// which project a step belongs to says nothing about whose screen it is.
	instanceID := func(contract.Repository) string { return config.RunnerDesktop }
	return guard(runner, procs, instanceID), nil
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

// checkDispatch refuses a runner told to answer for an implementation whose
// capability its code cannot dispatch.
//
// This is the same door checkReach guards, closed against a quieter lie. A
// runner's served list comes from the settings file and was trusted whole:
// Serves said yes, the status screen printed the implementation as served,
// and the funnel chose it -- then the call reached a switch with no case for
// it and came back not_found, blaming the request for a wiring mistake made
// long before it. That is knowable here, with the catalog and the runners
// both in hand.
//
// An id the catalog does not declare is deliberately NOT an error. It cannot
// be chosen, because the funnel only ever picks from the catalog, so it can
// never produce the failure above -- and refusing it would break the legitimate
// case of a small hand-written catalog attaching a runner whose shipped
// defaults name more than that catalog uses.
func checkDispatch(source string, impls []contract.Implementation, runners []contract.Runner) error {
	capabilityOf := make(map[string]string, len(impls))
	for _, impl := range impls {
		capabilityOf[impl.ID] = impl.Capability
	}
	for _, runner := range runners {
		for _, id := range runner.Implementations() {
			capability, declared := capabilityOf[id]
			if !declared || slices.Contains(runner.Capabilities(), capability) {
				continue
			}
			return contract.Fail(contract.FailureInvalidInput,
				"settings %s: %s is told to serve implementation %s, but it cannot run %s -- it runs %s",
				source, runner.ID(), id, capability,
				strings.Join(runner.Capabilities(), ", "))
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

func (f fanOut) Capabilities() []string {
	var out []string
	for _, runner := range f {
		out = append(out, runner.Capabilities()...)
	}
	slices.Sort(out)
	return slices.Compact(out)
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

// attach reduces the live adapters to the single seam the orchestrator takes,
// behind the two gates. Nothing dispatched by this core reaches an adapter
// without crossing commissioned.Run and then grounded.Run: what the
// commission allows, and whether the repository it names is even here.
func attach(runners []contract.Runner, notes *notebook.Notebook) contract.Runner {
	switch len(runners) {
	case 0:
		return nil
	case 1:
		// One client needs no routing, and the status screen reads better
		// naming it directly than naming a wrapper around it -- which is why
		// the gates delegate ID rather than answering for themselves.
		return commissioned{grounded{runners[0]}, notes}
	default:
		return commissioned{grounded{fanOut(runners)}, notes}
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
	return c.selectWithPreference(capabilityID, repositoryID, "")
}

// SelectWithPreference applies a one-call implementation preference without
// changing the settings file or its standing selector rules.
func (c *Core) SelectWithPreference(capabilityID, repositoryID, prefer string) (selector.Decision, error) {
	return c.selectWithPreference(capabilityID, repositoryID, prefer)
}

func (c *Core) selectWithPreference(capabilityID, repositoryID, prefer string) (selector.Decision, error) {
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
	candidates = c.catalog.Observed(repo.ID, candidates)
	// Select is a one-shot lookup with no caller to cancel it: the CLI asks,
	// prints and exits. The store bounds its own wait on the file lock, so a
	// background context here cannot hang on a second Atenea's flush.
	measuring, notices := c.priced(context.Background(), capabilityID, repo.ID, candidates)
	decision, err := c.chooser.Select(selector.Request{
		Capability: capabilityID,
		Repository: repo,
		Candidates: candidates,
		Reachable:  c.reach(),
		Measuring:  measuring,
		Prefer:     prefer,
	})
	decision.Notices = append(decision.Notices, notices...)
	return decision, err
}

// priced fills the candidates with what the base measured here. It is the same
// seam the orchestrator uses, and for the same reason: the catalog declares
// what a provider is guessed to cost, the store knows what it did cost, and
// the funnel is the one place the two meet.
func (c *Core) priced(ctx context.Context, capability, repository string,
	candidates []contract.Implementation) (bool, []string) {
	if c.measurements == nil {
		return false, nil
	}
	base, err := c.measurements.Baselines(ctx, capability, repository)
	if err != nil {
		return false, []string{fmt.Sprintf(
			"the measurement base could not be read (%v); ranking on the declared estimates", err)}
	}
	return true, metrics.Apply(base, candidates, time.Now())
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

// fileRawReceipt records one passthrough call.
//
// It is written already closed, because a raw call has nothing to resume: no
// plan, no step to redispatch, and nothing a later process could pick up. The
// funnel on it says `none` -- not an empty trace, which is what a step whose
// candidates were never recorded looks like. Those two silences are the pair
// the receipt's Funnel type exists to keep apart.
//
// A failure to write is swallowed on purpose, the same way the orchestrator
// treats its own dumps: the tool already ran on somebody else's server, and
// refusing to hand back its answer because the paperwork failed would turn a
// bookkeeping problem into a broken call. The notebook is where that goes.
func (c *Core) fileRawReceipt(session *Session, name string, effects []contract.Effect, started time.Time, callErr error) {
	if c.checkpoints == nil || !c.checkpoints.Enabled() {
		return
	}
	now := time.Now()
	verdict, failure := contract.VerdictOK.String(), ""
	if callErr != nil {
		verdict, failure = contract.VerdictFailed.String(), callErr.Error()
	}
	chat := ""
	if session != nil {
		chat = session.ID()
	}
	run := checkpoint.Run{
		ID:      checkpoint.NewID(now),
		Kind:    checkpoint.KindRaw,
		Session: chat,
		// The tool's public name is the whole commission: there is no task
		// text behind a raw call, and a reader looking for what happened
		// wants the name a client would have typed.
		Task:            name,
		ContractVersion: contract.Current.String(),
		Started:         started,
		Updated:         now,
		Closed:          true,
		// What the call was authorized to cause, in the operator's own
		// words from the settings file. A raw payload is opaque, so this is
		// the only durable statement of what the machine allowed -- and a
		// refused call keeps it too, which is what makes the refusal
		// auditable rather than merely effective.
		Effects: effects,
		Verdict: verdict,
		Steps: []checkpoint.StepState{{
			ID:             name,
			Implementation: name,
			Verdict:        verdict,
			Review:         verdict,
			Failure:        failure,
			Funnel:         checkpoint.Funnel{State: checkpoint.FunnelNone},
			DurationMS:     now.Sub(started).Milliseconds(),
			ClosedAt:       now,
		}},
	}
	if err := c.checkpoints.Save(run); err != nil {
		c.notebook.Catch(notebook.Incident{
			Op:             "raw-receipt",
			Detail:         err.Error(),
			Implementation: name,
		})
	}
}

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

// Ask dispatches one capability against one repository, with the same
// in-flight bookkeeping a commission gets: a clean stop waits for it too.
func (c *Core) Ask(ctx context.Context, q orchestrator.Question) (*orchestrator.Result, error) {
	if err := c.enter(); err != nil {
		return nil, err
	}
	defer c.inflight.Done()
	return c.agent.Ask(ctx, q)
}

// Resume picks an interrupted or failed commission back up on the
// operator's own behalf, with the same in-flight bookkeeping Do and Ask
// get: a clean stop waits for it too.
func (c *Core) Resume(ctx context.Context, runID string, opts orchestrator.ResumeOptions) (*orchestrator.Result, error) {
	if err := c.enter(); err != nil {
		return nil, err
	}
	defer c.inflight.Done()
	return c.agent.Resume(ctx, runID, opts)
}

// IndexReport is one repository/provider pairing's index-detection result.
type IndexReport struct {
	Repository string
	Provider   string
	Ready      bool
	// Hint explains a false Ready in words a user can act on. Empty when
	// Ready is true.
	Hint string
	// Err is the probe's own failure, in its own words. Empty when the
	// probe ran to completion, whatever it found -- a wire-friendly string
	// rather than an error, the same reason Health.Reason and Drop.Reason
	// already are.
	Err string
}

// DetectIndexes asks every attached runner that can say whether it already
// holds a ready index, for the given repository or every registered one when
// repositoryID is empty, and corrects the catalog's own belief -- indexed_by,
// as the settings file declared it -- with whatever it finds.
//
// Detection only ever asks, it never builds, and SetIndexed is the same
// in-memory correction SetHealth already makes for a provider's own
// liveness, on the same reasoning. It runs on demand rather than on every
// startup because a probe is itself a subprocess call per repository per
// provider, and paying that unconditionally would tax the common case --
// everything already wired correctly -- for the sake of the uncommon one
// this exists to catch.
//
// Not every runner can answer this. One that cannot is left out of the
// report entirely rather than reported as a failure, the same reasoning
// IndexProber being optional documents at its own declaration.
func (c *Core) DetectIndexes(ctx context.Context, repositoryID string) ([]IndexReport, error) {
	if err := c.enter(); err != nil {
		return nil, err
	}
	defer c.inflight.Done()

	var repos []contract.Repository
	if repositoryID == "" {
		repos = c.catalog.Repositories()
	} else {
		repo, err := c.catalog.Repository(repositoryID)
		if err != nil {
			return nil, err
		}
		repos = []contract.Repository{repo}
	}

	var reports []IndexReport
	for _, runner := range c.runners {
		prober, ok := runner.(contract.IndexProber)
		if !ok {
			continue
		}
		for _, repo := range repos {
			if ctx.Err() != nil {
				return reports, ctx.Err()
			}
			ready, hint, err := prober.ProbeIndex(ctx, repo.Path)
			var errText string
			if err != nil {
				errText = err.Error()
			} else if setErr := c.catalog.SetIndexed(repo.ID, runner.ID(), ready); setErr != nil {
				errText = setErr.Error()
			}
			reports = append(reports, IndexReport{
				Repository: repo.ID,
				Provider:   runner.ID(),
				Ready:      ready,
				Hint:       hint,
				Err:        errText,
			})
		}
	}
	slices.SortFunc(reports, func(a, b IndexReport) int {
		if d := strings.Compare(a.Repository, b.Repository); d != 0 {
			return d
		}
		return strings.Compare(a.Provider, b.Provider)
	})
	return reports, nil
}

// ServerProbe is one declared [[mcp_server]] as a probe just found it.
//
// This is the opposite half of ServerStatus and the two must not be confused:
// that one reports what is remembered and costs nothing, this one asks now and
// pays a process per stdio server. Both exist because the operator's question
// and the screen's question are different -- "is it there right now" versus
// "what is the last thing we learned".
type ServerProbe struct {
	ID        string
	Transport string
	Where     string
	Dashboard string
	Expose    string
	OK        bool
	// Name and Version are who answered, from the handshake. Empty when
	// nobody did, which is the only case Reason is set.
	Name    string
	Version string
	Reason  string
	Took    time.Duration
	// PinnedPath says the declaration carries a PATH of its own, so this
	// verdict does not depend on the environment of whoever ran the command.
	//
	// It is on the report because it is the difference between a verdict that
	// transfers to the service and one that does not. Three servers died at
	// boot because they inherited a PATH without their binaries in it; the two
	// that still inherit one can pass here, run from a rich shell, and fail
	// inside a service started by systemd -- and a report that hid that would
	// be a new way to say "everything is fine" about a dead server.
	PinnedPath bool
}

// DetectServers probes every declared server at once and reports a verdict per
// declaration, in declaration order.
//
// Probing is right here and wrong in Status, and the split is deliberate: this
// runs because an operator asked the question, which is the one moment the cost
// of a spawn per server buys something. It reuses mcpprobe.ProbeAll -- the same
// parallel probe `atenea wrap` runs -- rather than a second implementation, so
// two commands cannot disagree about whether one server is up.
//
// It deliberately does not write what it learns into the remembered state the
// screen reads. A command's probe happens in the command's environment, and
// filing it as the service's knowledge would let a verdict earned under one
// PATH be reported later as though the service had earned it.
func (c *Core) DetectServers(ctx context.Context) ([]ServerProbe, error) {
	if err := c.enter(); err != nil {
		return nil, err
	}
	defer c.inflight.Done()

	servers := c.settings.MCPServers
	probes := make([]mcpprobe.Server, len(servers))
	for i, server := range servers {
		probes[i] = server.Probe()
	}
	results := mcpprobe.ProbeAll(ctx, probes)

	out := make([]ServerProbe, 0, len(servers))
	for i, server := range servers {
		_, pinned := server.Env["PATH"]
		entry := ServerProbe{
			ID:         server.ID,
			Transport:  probes[i].Transport(),
			Where:      probes[i].Where(),
			Dashboard:  server.Dashboard,
			Expose:     string(server.Expose),
			OK:         results[i].OK,
			Name:       results[i].Name,
			Version:    results[i].Version,
			Took:       results[i].Took,
			PinnedPath: pinned,
		}
		if results[i].Err != nil {
			entry.Reason = results[i].Err.Error()
		}
		out = append(out, entry)
	}
	return out, nil
}

// Detection is both halves of a detect sweep plus the identity of whoever ran
// it, and the last part is the point.
//
// Both halves travel together because both spawn: the server probes spawn a
// process per stdio declaration, and the index reports spawn the provider's own
// CLI. Fixing one and leaving the other would answer half the command from one
// environment and half from another, which is worse than the fault being fixed.
//
// PID and Settings are what a caller cannot know about a process it only
// reached through a socket: which process earned these verdicts, and which
// declarations it read to earn them.
type Detection struct {
	Servers  []ServerProbe
	Indexes  []IndexReport
	PID      int
	Settings string
}

// Detect runs both halves of a sweep and signs the result.
//
// It exists so the service can answer the whole of `atenea detect` in one
// exchange. A command that probes locally learns whether the servers are
// reachable from a shell, which is not the question when the thing that cannot
// reach them is the service: measured on this machine, a service with a minimal
// PATH had context7 dead while a shell called it reachable in the same minute.
func (c *Core) Detect(ctx context.Context, repositoryID string) (Detection, error) {
	servers, err := c.DetectServers(ctx)
	if err != nil {
		return Detection{}, err
	}
	indexes, err := c.DetectIndexes(ctx, repositoryID)
	if err != nil {
		return Detection{}, err
	}
	return Detection{
		Servers:  servers,
		Indexes:  indexes,
		PID:      os.Getpid(),
		Settings: c.settings.Source,
	}, nil
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
//
// This is the service entry point, and the one place a socket exists: a
// command settles and goes, and a door nobody is behind is worse than no door.
func (c *Core) Run(ctx context.Context) error {
	listener, err := c.listen()
	if err != nil {
		return err
	}
	// The rhythms only exist while something is holding the core up. A CLI
	// command lives for a second and settles its batch on the way out; a
	// service lives for days and needs the beat.
	c.beats.Start(ctx)
	go c.accept(ctx, listener)
	if c.processes != nil {
		// WarmUp only touches Persistent servers, and it waits for some of
		// them. The ordinary ones are started in parallel and not waited on,
		// so a slow one never holds up the rest of Run. The serena@* family
		// is the exception WarmUp documents: those are warmed in declaration
		// order, one at a time, with each ensureReady waited out, because
		// each child claims the first free dashboard port and a parallel
		// start would hand the ports out in whatever order the goroutines
		// happened to run. That makes Run's start time include the readiness
		// of every declared serena instance.
		// Start begins the idle reaper for OnDemand servers. Neither call
		// cares which kind, if either, the settings file actually declared
		// -- both methods already treat "nothing of that kind is registered"
		// as nothing to do; the guard here is only for a Supervisor that
		// does not exist at all, which nil means when nothing was managed.
		c.processes.WarmUp(ctx)
		c.processes.Start(ctx)
	}
	<-ctx.Done()
	// Shutdown is the whole wait. It returns when the teardown is done --
	// including a teardown somebody else started, which is the case that made
	// Run return too early before: a Shutdown finding one already in progress
	// used to return nil at once, so Run reported the service down while a
	// handler was still writing into the state root and the caller who then
	// removed that root failed on a directory that would not stay empty.
	//
	// Waiting for the other teardown rather than draining the connections
	// separately is what keeps the stop inside its budget. A separate drain
	// bounded by its own copy of the grace made a parked handler cost two
	// full graces -- one in Shutdown, one after it -- and the unit files ask
	// the managers to wait grace+5s, so the stop was being SIGKILLed at the
	// margin instead of finishing. One wait, one budget.
	return c.Shutdown()
}

// Shutdown refuses new work and gives whatever is running a bounded margin to
// finish. Cutting a writer off mid-flight can leave files half written; waiting
// forever is not an option either, so the margin is configured, not infinite.
func (c *Core) Shutdown() error {
	c.mu.Lock()
	if c.stopping {
		// Somebody else is already tearing down. Wait for them and report
		// what they got, rather than returning nil: this call's caller is
		// entitled to the same answer, and more importantly to the same
		// guarantee -- that when it returns, the writing has stopped. Bounded
		// by the grace like everything else here, because a teardown that
		// overran is exactly the case this function refuses to wait forever
		// for, and because two callers must not cost two graces.
		waiting := c.stopped
		grace := c.settings.Core.ShutdownGrace
		c.mu.Unlock()
		if waiting == nil {
			return nil
		}
		select {
		case <-waiting:
			c.mu.Lock()
			err := c.stopErr
			c.mu.Unlock()
			return err
		case <-time.After(grace):
			return contract.Fail(contract.FailureTimeout,
				"the shutdown already running did not finish within %s", grace)
		}
	}
	c.stopping = true
	c.stopped = make(chan struct{})
	finished := c.stopped
	c.mu.Unlock()

	// Whatever this teardown concludes is published for the callers waiting
	// above before the channel closes, so a waiter that wakes cannot read a
	// stopErr the teardown had not written yet.
	var stopErr error
	defer func() {
		c.mu.Lock()
		c.stopErr = stopErr
		c.mu.Unlock()
		close(finished)
	}()

	// The door shuts first: the flag above already refuses new work, and this
	// stops new callers from getting as far as being refused.
	c.closeSocket()

	// Connection handlers and in-flight work wait together, under the one
	// margin. They were two waits before this, and only the second of them
	// was bounded: closeSocket ended in c.conns.Wait() with no limit at all,
	// so a handler that did not return hung the stop before the grace timer
	// was even started -- the exact "waiting forever is not an option" this
	// function's own doc comment rules out. Closing the listener and closing
	// a client's socket do not interrupt a dispatch that is already inside
	// talk.dispatch, so that handler is real work that can overrun, and it
	// belongs under the same budget as everything else that can.
	done := make(chan struct{})
	go func() {
		c.conns.Wait()
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
		c.stopProcesses()
		c.closeBackends()
		stopErr = c.settle()
		return stopErr
	case <-time.After(c.settings.Core.ShutdownGrace):
		// The table is left alone on purpose: work is still running, so the
		// chats behind it are still real and saying otherwise would hide it.
		// The batch is still written: measurements of work that did finish are
		// not the thing to throw away because something else overran.
		c.stopProcesses()
		c.closeBackends()
		_ = c.settle()
		stopErr = contract.Fail(contract.FailureTimeout,
			"in-flight work did not finish within %s", c.settings.Core.ShutdownGrace)
		return stopErr
	}
}

// closeBackends releases every declared backend, on both ways out.
//
// It matters for exactly one of the two modes, and that one is new: an HTTP
// backend only forgets a session id, but a stdio backend is a process Atenea
// started, and one left behind is the waste this whole feature exists to
// remove -- reappearing once per restart, which is how a machine ends up
// running six copies of one indexer. Called after the door is shut and the
// in-flight work has been given its margin, so nothing is cut off mid-answer.
func (c *Core) closeBackends() {
	for _, backend := range c.backends {
		if backend.Backend != nil {
			backend.Close()
		}
	}
}

// settle stops the rhythms, releases the upkeep and writes whatever is still in
// memory.
//
// This is the second of the two safety nets around batching, and the last
// chance the batch gets. Unlike the one at a phase close, its error is
// returned: nothing comes after it, so a caller that ignored it would be
// throwing away the only report that measurements were lost.
//
// The claim goes back here rather than at the end of Shutdown because both of
// Shutdown's exits pass through this one function, and because the claim is the
// right to tick: it has no meaning once the clock is stopped, and holding it a
// moment longer would refuse a restart for no reason. A kill that skips this
// path leaves the file behind with a pid that no longer exists, which the next
// claim clears.
func (c *Core) settle() error {
	c.beats.Stop()
	if c.upkeep != nil {
		c.upkeep()
		c.upkeep = nil
	}
	if c.measurements == nil {
		return nil
	}
	return c.flushLast()
}

// settleAttempts and settleBackoff bound the last stand the batch gets.
//
// Three tries rather than one, and spaced rather than immediate, because the
// failure this is for is a transient one: another process holding the DuckDB
// file for its own flush, or a filesystem that has not come back yet. Bounded
// rather than persistent because the process is on its way out and something
// is waiting for it -- the whole budget here is under a second, which is
// nothing against a stop and enough for a lock to clear.
const (
	settleAttempts = 3
	settleBackoff  = 150 * time.Millisecond
)

// flushLast writes the batch, and says how many measurements died with the
// process when it cannot.
//
// Store.Close is a single Flush, and a Flush that fails puts its rows back in
// the in-memory buffer -- which is exactly the right thing for a running
// service, whose next beat will carry them, and exactly the wrong thing here,
// because there is no next beat: the buffer is about to be freed along with
// the process. One failed write was silently the end of that batch, with
// nothing anywhere to say it had happened.
//
// The incident quotes Pending() rather than a guess, because the number is the
// point. A baseline short by rows nobody counted is a baseline nobody can
// trust; a baseline short by seventeen rows, on a named date, is one an
// operator can reason about.
func (c *Core) flushLast() error {
	// Dropped is a lifetime counter, so the settle's own losses are a
	// difference and not a reading. A service that lost five hundred rows to
	// a full buffer at midday would otherwise report those five hundred as
	// having died in the last second of its life.
	before := c.measurements.Dropped()

	var err error
	for attempt := range settleAttempts {
		if attempt > 0 {
			time.Sleep(settleBackoff)
		}
		if err = c.measurements.Flush(context.Background()); err == nil {
			break
		}
	}

	// Sealed after the last attempt, not before the first.
	//
	// On the shutdown that overran, work is still running while this executes
	// and goes on calling Record. Sealing first counted those rows as lost --
	// but this is three flushes separated by 300ms, and Flush drains the whole
	// buffer, so a row arriving during the backoff is one attempt two or three
	// would have written to disk. Sealing first turned recoverable
	// measurements into losses in the very path meant to save them. Sealing
	// here means the retries carry whatever arrives, and only what arrives
	// after the last one is counted as gone -- which it is, because the buffer
	// is about to be freed along with the process.
	c.measurements.Seal()
	if err == nil {
		return nil
	}

	lost := c.measurements.Dropped() - before
	_ = c.notebook.Record(notebook.Incident{
		Op: "metrics.settle",
		Detail: fmt.Sprintf(
			"the final flush failed %d times and %d measurements are gone with the process "+
				"(%d more were refused by a full buffer during the stop): %v",
			settleAttempts, c.measurements.Pending(), lost, err),
		Version: buildinfo.Full(),
	})
	return err
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
func (c *Core) Version() string { return buildinfo.Full() }

// groundedRepositories refuses a service whose catalog names a repository by a
// relative path.
//
// `path = "."` is not a mistake in the shipped settings: it is the mechanism
// by which a fresh install works against whatever tree you are standing in,
// and the CLI is the thing standing somewhere. A daemon stands nowhere. Its
// working directory is whatever its unit file left it -- $HOME before the
// units learned to name the state root, and the state root afterwards, which
// is Atenea's own receipts and measurement base rather than a repository.
// Either way it is a tree nobody chose, searched under a name somebody trusts.
//
// So the CLI keeps the convenience and the service refuses it, by name and
// with the one command that fixes it. Refusing rather than dropping the entry:
// a repository quietly missing from a service's catalog is a capability that
// answers "unknown repository" to a client that can see it in the settings
// file, and this project's own rule about a settings file is that one quietly
// ignored is a machine running settings nobody chose.
func groundedRepositories(cfg config.Config) error {
	for _, repo := range cfg.Repositories {
		if filepath.IsAbs(repo.Path) {
			continue
		}
		// The remedy differs, and saying the wrong one is worse than saying
		// none: on the built-in defaults there is no file to edit, and telling
		// somebody to edit one sends them looking for it.
		remedy := fmt.Sprintf("edit %s and give %s an absolute path", cfg.Source, repo.ID)
		if cfg.Source == config.BuiltIn {
			remedy = "run `atenea config init`, which writes one naming the directory you run it in"
		}
		return contract.Fail(contract.FailureInvalidInput,
			"repository %s is declared at %q, which is relative: a service has no "+
				"working directory anybody chose, so it cannot resolve one. %s",
			repo.ID, repo.Path, remedy)
	}
	return nil
}
