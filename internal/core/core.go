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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tutitoos/atenea/internal/adapter/claudecode"
	"github.com/Tutitoos/atenea/internal/adapter/codebasememory"
	"github.com/Tutitoos/atenea/internal/adapter/omp"
	"github.com/Tutitoos/atenea/internal/adapter/serena"
	"github.com/Tutitoos/atenea/internal/backup"
	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/clock"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/ipc"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/internal/runner/local"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/internal/supervisor"
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
	inflight sync.WaitGroup
}

// Job names for the maintenance lane. They are the handle Settle and the
// shutdown path reach for, so they are constants rather than strings typed
// twice.
const (
	jobFlush   = "metrics.flush"
	jobCompact = "metrics.compact"
	jobBackup  = "backup"
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
	copies, err := openCopies(cfg.Backup, platform.StateDir())
	if err != nil {
		return nil, err
	}
	beats, err := buildLanes(cfg, store, copies, book)
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
		Runner:      attach(runners),
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
	built = true
	return &Core{
		settings:     cfg,
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
			Implementations: cfg.Orchestrator.ClaudeCode.Implementations,
			Sensitive:       cfg.Security.Sensitive,
			Timeout:         cfg.Orchestrator.ClaudeCode.Timeout,
		})
	case config.RunnerCodebaseMemory:
		return codebasememory.New(codebasememory.Options{
			Binary:          cfg.Orchestrator.CodebaseMemory.Binary,
			Implementations: cfg.Orchestrator.CodebaseMemory.Implementations,
			Sensitive:       cfg.Security.Sensitive,
			Timeout:         cfg.Orchestrator.CodebaseMemory.Timeout,
		})
	case config.RunnerSerena:
		return buildSerenaRunner(cfg, procs)
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
	if cfg.Orchestrator.Serena.Process != nil {
		// Checked before it is discarded. The supervisor's address is the one
		// that gets dialed, but a written endpoint that could never work is
		// still a mistake, and letting a process table excuse it would mean
		// deleting that table later turns a file that always loaded into one
		// that suddenly does not.
		if err := serena.ValidateEndpoint(endpoint); err != nil {
			return nil, err
		}
		var err error
		if endpoint, err = procs.Endpoint(config.RunnerSerena); err != nil {
			return nil, err
		}
	}
	runner, err := serena.New(serena.Options{
		Endpoint:        endpoint,
		Implementations: cfg.Orchestrator.Serena.Implementations,
		Sensitive:       cfg.Security.Sensitive,
		Timeout:         cfg.Orchestrator.Serena.Timeout,
	})
	if err != nil {
		return nil, err
	}
	if cfg.Orchestrator.Serena.Process == nil {
		return runner, nil
	}
	return guardedRunner{Runner: runner, procs: procs, id: config.RunnerSerena}, nil
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
// behind the permission gate. Nothing dispatched by this core reaches an
// adapter without crossing commissioned.Run first.
func attach(runners []contract.Runner) contract.Runner {
	switch len(runners) {
	case 0:
		return nil
	case 1:
		// One client needs no routing, and the status screen reads better
		// naming it directly than naming a wrapper around it -- which is why
		// the gate delegates ID rather than answering for itself.
		return commissioned{runners[0]}
	default:
		return commissioned{fanOut(runners)}
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
// This is the read half of the pair repository.index is the write half of:
// detection only ever asks, it never builds, and SetIndexed is the same
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
		// WarmUp only touches Persistent servers and does not wait for any
		// of them, so a slow one never holds up the rest of Run starting.
		// Start begins the idle reaper for OnDemand servers. Neither call
		// cares which kind, if either, the settings file actually declared
		// -- both methods already treat "nothing of that kind is registered"
		// as nothing to do; the guard here is only for a Supervisor that
		// does not exist at all, which nil means when nothing was managed.
		c.processes.WarmUp(ctx)
		c.processes.Start(ctx)
	}
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

	// The door shuts first. New work is already refused by the flag above, but
	// a caller mid-question is owed its answer, so this stops new connections
	// and then waits for the ones already inside.
	c.closeSocket()

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
		c.stopProcesses()
		return c.settle()
	case <-time.After(c.settings.Core.ShutdownGrace):
		// The table is left alone on purpose: work is still running, so the
		// chats behind it are still real and saying otherwise would hide it.
		// The batch is still written: measurements of work that did finish are
		// not the thing to throw away because something else overran.
		c.stopProcesses()
		_ = c.settle()
		return contract.Fail(contract.FailureTimeout,
			"in-flight work did not finish within %s", c.settings.Core.ShutdownGrace)
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
	return c.measurements.Close()
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
