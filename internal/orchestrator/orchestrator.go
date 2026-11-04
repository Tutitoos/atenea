// Package orchestrator holds the dedicated agent that dispatches a capability
// call and reviews what comes back.
//
// It is an AGENT, deliberately not part of the core. Atenea decides and
// delegates: the core owns the catalog and the funnel and says who should do a
// thing, while this agent is the one that hands the call out and reviews what
// comes back. Putting the dispatch in the core would make the core do the work
// instead of directing it.
package orchestrator

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/observability"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Catalog is the slice of the registry this agent needs. It is narrow on
// purpose: the orchestrator reads the catalog and reports health, it never
// edits the catalog itself.
type Catalog interface {
	Capability(id string) (contract.Capability, error)
	Repository(id string) (contract.Repository, error)
	Repositories() []contract.Repository
	ImplementationsFor(capabilityID string) ([]contract.Implementation, error)
	Observed(repositoryID string, impls []contract.Implementation) []contract.Implementation
	SetHealth(repositoryID, implementationID string, health contract.Health) error
}

// Chooser is the funnel. The orchestrator asks for a capability on a
// repository and is told which implementation answers it, never the reverse.
type Chooser interface {
	Select(req selector.Request) (selector.Decision, error)
}

type qualityRecorder interface {
	RecordOutcome(capability, repository, implementation, language, toolVersion string, out contract.Outcome, runErr error, observed ...string) error
}

func primaryLanguage(repository contract.Repository) string {
	if len(repository.Languages) == 0 {
		return ""
	}
	langs := slices.Clone(repository.Languages)
	slices.Sort(langs)
	return langs[0]
}

// Meter is where a closed step reports what it cost.
//
// The orchestrator never writes a measurement, it only hands one upwards: the
// core owns the store and is the single writer. Settle is the push to disk at
// the two moments the batch must not be allowed to evaporate.
type Meter interface {
	Record(m metrics.Measurement)
	Settle(ctx context.Context)
}

// unmetered is what an agent runs against when nobody is collecting. Measuring
// is not what makes the work correct, so a core without a store still
// dispatches; it simply learns nothing from it.
type unmetered struct{}

func (unmetered) Record(metrics.Measurement) {}
func (unmetered) Settle(context.Context)     {}

// Base is Meter's twin: one writes down what a step cost, the other reads it
// back so the funnel can rank on it instead of on a guess.
//
// It is asked per repository because cost is not a property of the tool. The
// same provider is cheap against a warm index and expensive without one, so a
// figure borrowed from another repository would be the confident kind of
// wrong.
//
// Like Meter it is optional. A core with the base switched off dispatches
// exactly as well; it simply keeps ranking on the estimates in the settings
// file, and the trace keeps saying so.
type Base interface {
	Baselines(ctx context.Context, capability, repository, subject string) (map[string]metrics.Baseline, error)
}

// Phase names.
const (
	// PhaseAsk is one capability, asked directly. It is the only phase a run
	// reports.
	// PhaseAsk is part of ATENEA's public orchestration contract.
	PhaseAsk = "ask"
)

// searchCapability and contextCapability are the two text-reading capabilities
// the card names first: literal text search, and code.context for a model that
// holds only prose and does not know a string or file yet.
const (
	searchCapability  = "code.search"
	contextCapability = "code.context"
)

// The symbol capabilities, plus the two that read the call graph itself. The
// orchestrator does not plan a commission around any of them -- it plans
// around a text search, which is brick 4's shape and stays -- but it declares
// them because declaring is what makes a step askable at all. A capability
// the card does not name cannot be dispatched even when the catalog, the
// funnel and a runner are all ready for it.
//
// symbol.search is here because it was once not. It shipped complete in every
// other respect -- declared in default.toml, bound to MCP server.symbol_search,
// answered by runSearch, chosen by the funnel -- and every call was refused at
// this list. Warning about the trap in a comment turned out not to be the same
// as closing it, so core.checkAskable now refuses at load a catalog capability
// this card does not name.
const (
	symbolSearchCapability    = "symbol.search"
	intentSearchCapability    = "symbol.intent_search"
	definitionCapability      = "symbol.definition"
	dependenciesCapability    = "symbol.dependencies"
	referencesCapability      = "symbol.references"
	implementationsCapability = "symbol.implementations"
	overviewCapability        = "symbol.overview"
	callsCapability           = "symbol.calls"
	impactCapability          = "code.impact"
	indexCapability           = "repository.index"
)

// code.impact and repository.index are direct, explicit questions. They are
// not planned into ordinary exploration: indexing asks for write and process,
// and impact asks for process in addition to the read that is always free.

// The four Kivgraph answers. Named here for the reason stated above and for
// no other: none of them is planned into a commission either. Three read one
// symbol's cross-repository consumers, its identity by stable key, and the
// references that resolved to nothing; the fourth reports the published
// graph itself. A provider can be declared, attached, holding a ready index
// and answering on the wire, and still be unreachable if this list does not
// name it -- which is how it read from the outside the first time.
const (
	consumersCapability   = "symbol.consumers"
	symbolGetCapability   = "symbol.get"
	unresolvedCapability  = "symbol.unresolved"
	graphStatusCapability = "graph.status"
)

// What is open on this machine's screen. Listed here so a commission can name
// a target instead of guessing, and gated everywhere else: it causes the
// device effect, which no floor grants by default and which the adapter
// refuses outright unless Atenea is the process the system attributes the
// permission to.
const (
	desktopAppsCapability       = "desktop.apps"
	desktopOpenCapability       = "desktop.open"
	desktopInspectCapability    = "desktop.inspect"
	desktopScreenshotCapability = "desktop.screenshot"
	desktopClickCapability      = "desktop.click"
	desktopMoveCapability       = "desktop.move"
	desktopDragCapability       = "desktop.drag"
	desktopScrollCapability     = "desktop.scroll"
	desktopTypeCapability       = "desktop.type"
	desktopKeyCapability        = "desktop.key"
)

const (
	androidDevicesCapability    = "android.devices"
	androidDiagnoseCapability   = "android.diagnose"
	androidInspectCapability    = "android.inspect"
	androidScreenshotCapability = "android.screenshot"
	androidTapCapability        = "android.tap"
	androidSwipeCapability      = "android.swipe"
	androidTypeCapability       = "android.type"
	androidKeyCapability        = "android.key"
	androidMirrorCapability     = "android.mirror"
	androidUnmirrorCapability   = "android.unmirror"
)

// Config wires the agent to the pieces around it.
type Config struct {
	Catalog Catalog
	Chooser Chooser
	Runner  contract.Runner
	// Identity observes the current provider identity before selection. A nil
	// callback leaves quality unknown and never consults historical cost as a
	// substitute.
	Identity func(context.Context, contract.RunRequest) (contract.CacheIdentity, error)
	// IdentityActivity brackets each live identity probe in the durable
	// activity store. It is separate from cache hits: a probe is provider work
	// and must remain visible even when the following dispatch reuses it.
	IdentityActivity func(context.Context, contract.RunRequest) func(error)
	// Reach shares the core's repository-scoped wiring with real dispatch.
	// Nil retains global reach for standalone runners.
	Reach       func(contract.Repository) ([]string, map[string]string)
	Checkpoints *checkpoint.Store
	// Meter collects what each step cost. Nil means nobody is collecting,
	// which is a working core, just one that never learns.
	Meter Meter
	// Base is what the funnel ranks on once real numbers exist. Nil means the
	// funnel keeps ranking on the declared estimates, and break-in turns are
	// off: nothing can be earned when nothing is written down.
	Base Base
	// Notebook is where a panic goes on its way out. Nil records nothing,
	// which is what a hand-assembled test wants; the core always attaches one.
	Notebook *notebook.Notebook
	// Events is an optional metadata-only live stream for observers.
	Events *observability.Hub
	// MaxParallel caps how many steps of one wave run at a time. Zero means no
	// ceiling. The ceiling belongs in the settings because the real limit is
	// the machine, and the machine is not the same one everywhere.
	MaxParallel int
	// BudgetUSD is what one commission may spend when the commission does not
	// say for itself. Zero means no paid provider can run, which is what a
	// machine with none attached wants anyway.
	BudgetUSD float64
	// StandingEffects are granted to every commission and question this
	// agent dispatches, on top of the read that is always free. It is set
	// once, from the settings file, not per call: see config.Orchestrator
	// for why some effects are not a per-request choice.
	StandingEffects []contract.Effect
}

// Agent is the orchestrator. It is safe for concurrent use: two chats can be
// running commissions at the same time against the same catalog.
type Agent struct {
	card             contract.Agent
	catalog          Catalog
	chooser          Chooser
	runner           contract.Runner
	identity         func(context.Context, contract.RunRequest) (contract.CacheIdentity, error)
	identityActivity func(context.Context, contract.RunRequest) func(error)
	reach            func(contract.Repository) ([]string, map[string]string)
	checkpoints      *checkpoint.Store
	meter            Meter
	base             Base
	notebook         *notebook.Notebook
	events           *observability.Hub
	maxParallel      int
	budget           float64
	standingEffects  []contract.Effect
}

// card is what the orchestrator declares about itself. Declaring a context
// level is a permission, not a delivery: nothing here is loaded until the
// agent actually reaches for it.
var card = contract.Agent{
	ID:      "orchestrator",
	Type:    contract.AgentOrchestrator,
	Summary: "Dispatches one capability against one repository, picks who answers it, reviews the answer and writes the receipt.",
	Capabilities: []string{
		contextCapability,
		searchCapability,
		symbolSearchCapability,
		intentSearchCapability,
		definitionCapability,
		dependenciesCapability,
		referencesCapability,
		implementationsCapability,
		overviewCapability,
		callsCapability,
		impactCapability,
		indexCapability,
		consumersCapability,
		symbolGetCapability,
		unresolvedCapability,
		graphStatusCapability,
		"graph.repositories", "graph.ensure_fresh", "symbol.source", "symbol.impact",
		desktopAppsCapability,
		desktopOpenCapability,
		desktopInspectCapability,
		desktopScreenshotCapability,
		desktopClickCapability,
		desktopMoveCapability,
		desktopDragCapability,
		desktopScrollCapability,
		desktopTypeCapability,
		desktopKeyCapability,
		androidDevicesCapability,
		androidDiagnoseCapability,
		androidInspectCapability,
		androidScreenshotCapability,
		androidTapCapability,
		androidSwipeCapability,
		androidTypeCapability,
		androidKeyCapability,
		androidMirrorCapability,
		androidUnmirrorCapability,
	},
	Context: []contract.ContextLevel{
		contract.ContextRepository,
		contract.ContextWorkspace,
		contract.ContextGlobal,
		contract.ContextHistory,
	},
}

// New validates the wiring and returns the agent.
func New(cfg Config) (*Agent, error) {
	if err := card.Validate(); err != nil {
		return nil, err
	}
	if cfg.Catalog == nil || cfg.Chooser == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"orchestrator: catalog and chooser are required")
	}
	if cfg.MaxParallel < 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"orchestrator: max_parallel must not be negative")
	}
	// The standing grant is the ceiling every commission that names no figure
	// of its own inherits, so an unusable number here is worse than an
	// unusable one on a single commission: it is wrong for every commission
	// until somebody edits the settings file.
	if err := validGrant("orchestrator", cfg.BudgetUSD); err != nil {
		return nil, err
	}
	store := cfg.Checkpoints
	if store == nil {
		disabled, err := checkpoint.New("")
		if err != nil {
			return nil, err
		}
		store = disabled
	}
	meter := cfg.Meter
	if meter == nil {
		meter = unmetered{}
	}
	return &Agent{
		card:             card.Clone(),
		catalog:          cfg.Catalog,
		chooser:          cfg.Chooser,
		runner:           cfg.Runner,
		identity:         cfg.Identity,
		identityActivity: cfg.IdentityActivity,
		reach:            cfg.Reach,
		checkpoints:      store,
		meter:            meter,
		base:             cfg.Base,
		notebook:         cfg.Notebook,
		events:           cfg.Events,
		maxParallel:      cfg.MaxParallel,
		budget:           cfg.BudgetUSD,
		standingEffects:  slices.Clone(cfg.StandingEffects),
	}, nil
}

func (a *Agent) emit(event observability.Event) {
	if a != nil && a.events != nil {
		a.events.Publish(event)
	}
}

type observabilityContextKey struct{}

type observabilityContext struct {
	runID, sessionID string
}

func withObservabilityContext(ctx context.Context, runID, sessionID string) context.Context {
	return context.WithValue(ctx, observabilityContextKey{}, observabilityContext{runID: runID, sessionID: sessionID})
}

func (a *Agent) emitContext(ctx context.Context, event observability.Event) {
	if meta, ok := ctx.Value(observabilityContextKey{}).(observabilityContext); ok {
		if event.RunID == "" {
			event.RunID = meta.runID
		}
		if event.SessionID == "" {
			event.SessionID = meta.sessionID
		}
	}
	a.emit(event)
}

// Card returns the agent's contract.
func (a *Agent) Card() contract.Agent { return a.card.Clone() }

// Card is what the orchestrator declares about itself, without an agent to ask.
//
// The wiring checks in internal/core run before anything is dispatched and need
// only the declaration, not a running agent. Building one to read a constant
// would mean a catalog, a chooser and a checkpoint store existing before the
// question "is this catalog even askable" may be put -- which is backwards, and
// which is why the check that needed it did not exist.
func Card() contract.Agent { return card.Clone() }

// Runner reports who is behind the agent, or nil when nothing is wired yet.
func (a *Agent) Runner() contract.Runner { return a.runner }

// MaxParallel reports the configured ceiling; zero means no ceiling.
func (a *Agent) MaxParallel() int { return a.maxParallel }

// Result is what a finished commission looks like, at both heights: the
// summary on top and the trace underneath for when something smells wrong.
type Result struct {
	RunID       string
	Task        string
	Plan        contract.Plan
	Steps       []StepResult
	Phases      []Phase
	Discoveries []contract.Discovery
	Verdict     contract.Verdict
	Spent       contract.Sample
	// Elapsed is how long the commission actually took, which is not what it
	// cost: a wave of four steps charges four durations and spends one. Spent
	// is the sum, this is the wall, and the gap between them is the whole
	// return on running a wave at all. Without it on the report a parallel run
	// and a sequential one are indistinguishable from the outside -- which is
	// how the first wide wave ever dispatched on a real machine went
	// unremarked.
	Elapsed time.Duration
	// SpentUSD is what the commission was charged, over every step. Reported,
	// never ranked: see contract.Outcome.SpentUSD for why money is not one of
	// the measured axes.
	//
	// Read it beside SpentUSDKnown. A total is a measurement only when every
	// step behind it was measured; one step whose provider said nothing makes
	// this a lower bound, and nothing about the number itself shows that.
	SpentUSD float64
	// SpentUSDKnown says whether SpentUSD is the whole bill or only the part
	// somebody reported. See totalUSD.
	SpentUSDKnown bool
}

// Phase is one measured stretch of the run.
type Phase struct {
	Name  string
	Steps int
	Spent contract.Sample
	// Elapsed is the same distinction one height down, and this is where it is
	// most useful: a phase is one or more waves, so the gap between its two
	// figures is what the concurrency in it was worth.
	Elapsed time.Duration
}

// StepResult is one node of the plan after it closed.
type StepResult struct {
	Step     contract.Step
	Phase    string
	Decision selector.Decision
	Outcome  contract.Outcome
	Review   Review
	Failure  string
	// FailureKind is the shared bin the failure was sorted into, kept beside
	// the text because the text is for a human and the bin is what can be
	// counted. Turning the message back into a kind afterwards is not
	// possible: err.Error() is one-way.
	FailureKind contract.FailureKind
	FailureCode string
	// Raw is the provider's own text behind Failure, when whatever adapter
	// raised it kept one. Empty on success, and empty on a failure the core
	// raised itself with nothing to quote.
	Raw string
	// Subject is what this step was about beyond the repository, derived once
	// from the payload by the capability's own declaration. It scopes the
	// health the funnel selects on and is written onto the measurement
	// afterwards, and it has to be the same string in both places -- a step
	// ranked under one subject and filed under another would teach the base
	// something that never happened.
	Subject string
	Spent   contract.Sample
	// Dispatched says the chosen implementation was actually called. It is
	// not the same question as "was somebody chosen": the funnel picks a
	// winner before the request is validated, so a payload missing a required
	// field closes the step with Decision.Chosen filled in and nobody ever
	// asked. Reading the decision as proof of a call filed that caller's typo
	// as the provider's failure, and a run of them would mark down an
	// implementation that was never given the chance to answer.
	Dispatched bool
	// ClosedAt is when this step finished, stamped by the step itself. It has
	// to be: the recorder runs after the whole wave returns, so a clock read
	// there gives every step in the wave the same instant and moves the fast
	// ones to the back. Read beside Spent.Duration this is an interval, and
	// two intervals are how a reader sees that two steps overlapped.
	ClosedAt time.Time
	// These fields are internal correlation metadata and never cross the
	// runner/checkpoint boundary.
	runID     string
	sessionID string
}

// Review is the parent's audit of a child that just finished.
//
// The parent reviews every child, not only the risky ones: it is already
// standing there, and in generated work the harmless-looking failures are the
// ones that slip through. When the two disagree the parent's word is what goes
// on the record, and the disagreement is recorded with it.
type Review struct {
	Child  contract.Verdict
	Parent contract.Verdict
	Reason string
	// Reply is the one answer the child is allowed before the parent closes the
	// matter. It stays empty while the far side of the runner is a tool rather
	// than an agent: a tool has nothing to say for itself.
	Reply     string
	Disagreed bool
}

// Question is one capability asked of one repository, with the payload the
// caller already knows how to build.
//
// This is the one door into dispatch: a workflow is several of these chained
// by a caller. Symbol resolution arrives through here because a position is
// something the caller has and the orchestrator does not -- a text hit is not
// a cursor.
type Question struct {
	// Capability is what to ask for. It has to be on the card: a capability
	// the agent does not declare cannot be dispatched, however ready the rest
	// of the machinery is.
	Capability string
	// Repository is the unit of work. Unlike a commission this one is
	// required: a symbol lives in exactly one repository, and asking every
	// repository the same positional question would answer about files that
	// merely share a path.
	Repository string
	// Prefer selects one implementation for this question when it survives
	// constraints, reach and health. It does not alter standing configuration.
	Prefer string
	// Payload is the capability's declared input. It is checked against the
	// schema by the runner, not here: one gate, at the door it belongs to.
	Payload map[string]any
	// Effects the caller authorized beyond reading.
	Effects []contract.Effect
	// BudgetUSD is what this question may spend. Zero takes the standing
	// grant, exactly as a commission does: one capability is a commission of
	// one step, not a cheaper kind of thing.
	BudgetUSD float64
	// Session is the chat that asked, written to the receipt.
	Session                 string
	SessionClient           string
	SessionName             string
	SessionNameBasis        string
	SessionPrimaryProject   string
	SessionOriginSurface    string
	SessionOriginTransport  string
	SessionExternalObserved bool
	// Floor is the standing grant this question composes from, and carries
	// the same meaning it does on Task: zero is the settings file's own.
	Floor Floor
}

// Ask dispatches a single capability and returns the step that closed.
//
// It shares every mechanism with a commission -- the funnel picks the
// implementation, the parent reviews the child, and the receipt is written
// when the step closes and again when the run does. A second, quieter dispatch
// path would be a second set of rules to keep in step with the first.
func (a *Agent) Ask(ctx context.Context, q Question) (result *Result, err error) {
	capabilityID := strings.TrimSpace(q.Capability)
	if capabilityID == "" {
		return nil, contract.Fail(contract.FailureInvalidInput, "ask: capability is required")
	}
	if strings.TrimSpace(q.Repository) == "" {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"ask: repository is required; a position belongs to exactly one")
	}
	// One capability is a commission of one step, so the same ceiling is
	// refused here by the same rule.
	if err := validGrant("ask", q.BudgetUSD); err != nil {
		return nil, err
	}
	if a.runner == nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"no runner is attached, so nothing can be dispatched")
	}
	repositories, repoErr := a.resolveRepositories([]string{q.Repository})
	if repoErr != nil {
		return nil, repoErr
	}

	// The commission text is what a receipt is read back by, and there is no
	// user sentence here. Saying what was actually asked beats leaving it
	// blank or inventing prose nobody typed.
	text := capabilityID + " in " + repositories[0].ID
	budgetUSD := cmp.Or(q.BudgetUSD, a.budget)
	started := time.Now()
	result = &Result{RunID: checkpoint.NewID(started), Task: text}
	a.emit(observability.Event{Kind: "run.started", RunID: result.RunID, SessionID: q.Session, Capability: q.Capability, Repository: q.Repository})
	ctx = checkpoint.WithRunID(ctx, result.RunID)
	record := checkpoint.Run{
		RequestID: checkpoint.RequestID(ctx),
		ID:        result.RunID, Kind: checkpoint.KindAsk, Session: q.Session, SessionClient: q.SessionClient,
		SessionName: q.SessionName, SessionNameBasis: q.SessionNameBasis,
		SessionPrimaryProject: q.SessionPrimaryProject, SessionOriginSurface: q.SessionOriginSurface,
		SessionOriginTransport: q.SessionOriginTransport, SessionExternalObserved: q.SessionExternalObserved, Task: text,
		Started: started, Repositories: []string{repositories[0].ID}, Effects: q.Effects,
		BudgetUSD: budgetUSD, ContractVersion: contract.Current.String(),
	}
	defer func() {
		result.Spent = totalSpent(result.Steps)
		result.Elapsed = time.Since(started)
		result.SpentUSD, result.SpentUSDKnown = totalUSD(result.Steps)
		result.Verdict = overallVerdict(result.Steps, ctx.Err())
		result.Discoveries = append(result.Discoveries, reported(result.Steps)...)
		record.Closed = true
		record.Verdict = result.Verdict.String()
		record.Updated = time.Now()
		if saveErr := a.checkpoints.Save(record); saveErr != nil && err == nil {
			err = saveErr
		}
		a.emit(observability.Event{Kind: "run.closed", SessionID: q.Session, RunID: result.RunID, State: result.Verdict.String(), DurationMS: result.Elapsed.Milliseconds(), Tokens: int64(result.Spent.Tokens)})
	}()

	plan := contract.Plan{Task: text, Steps: []contract.Step{{
		ID:         "ask-" + repositories[0].ID,
		Capability: capabilityID,
		Repository: repositories[0].ID,
		Payload:    q.Payload,
		// Same layering as Run: read, then the floor this question runs on,
		// then whatever it asked for on top of that. The floor is the standing
		// grant unless a client's chat pinned its own.
		Permission: contract.Permission{
			Task:    text,
			Effects: []contract.Effect{contract.EffectRead},
		}.Grant(q.Floor.Or(a.standingEffects)).Grant(q.Effects),
		Prefer: q.Prefer,
	}}}
	if err := plan.Validate(); err != nil {
		return result, err
	}
	result.Plan = plan
	record.Plan = plan

	_, err = a.dispatch(ctx, plan, PhaseAsk, result, &record, newGrant(budgetUSD), nil)
	return result, err
}

func (a *Agent) resolveRepositories(wanted []string) ([]contract.Repository, error) {
	if len(wanted) == 0 {
		all := a.catalog.Repositories()
		if len(all) == 0 {
			return nil, contract.Fail(contract.FailureNotFound,
				"no repository is registered, so there is no unit of work to act on")
		}
		return all, nil
	}
	out := make([]contract.Repository, 0, len(wanted))
	seen := make(map[string]struct{}, len(wanted))
	for _, id := range wanted {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		repo, err := a.catalog.Repository(id)
		if err != nil {
			return nil, err
		}
		out = append(out, repo)
	}
	return out, nil
}

// dispatch runs whatever of the plan is still outstanding: every wave in
// order, and within a wave as many steps at once as the ceiling allows.
//
// The steps already closed are handed to the plan as finished, so a work step
// waiting on a look that happened in the previous phase is ready rather than
// dangling. alreadyOK adds to that the same way for steps closed in an
// earlier process: a resumed run has no Decision or Outcome to show for
// them, only the id and the fact that it passed review once, so they are
// kept out of result.Steps rather than represented there with blanks.
func (a *Agent) dispatch(ctx context.Context, plan contract.Plan, phase string, result *Result, record *checkpoint.Run, purse *grant, alreadyOK []string) ([]StepResult, error) {
	ctx = withObservabilityContext(ctx, record.ID, record.Session)
	finished := make([]string, 0, len(result.Steps)+len(alreadyOK))
	failed := make(map[string]struct{})
	for _, step := range result.Steps {
		finished = append(finished, step.Step.ID)
		if step.Review.Parent != contract.VerdictOK {
			failed[step.Step.ID] = struct{}{}
		}
	}
	finished = append(finished, alreadyOK...)
	waves, err := plan.LayersAfter(finished)
	if err != nil {
		return nil, err
	}

	phaseSpent := contract.Sample{}
	// What the phase costs is the sum of its steps; what it takes is this
	// clock. On a wave more than one step wide the two are different numbers,
	// and the gap is what the concurrency was worth.
	phaseStarted := time.Now()
	closed := make([]StepResult, 0, len(plan.Steps)-len(finished))
	// The batch lands however this phase ends, not only when it ends well.
	// This is the safety net under batching, and the exits that are not the
	// happy path are exactly the ones it exists for: the caller going away,
	// and a checkpoint that could not be written. Detached from the caller
	// for the same reason -- inheriting a cancellation meant the flush failed
	// with "context canceled", filed an incident saying so, and dropped every
	// measurement the run had already earned.
	defer a.meter.Settle(context.WithoutCancel(ctx))
	// The phase lands however this call ends, for the same reason the batch
	// does. Appended at the end of the loop instead, the two early exits --
	// the caller going away, and a checkpoint that could not be written --
	// took the whole phase off the report, so the commission worth reading
	// back was exactly the one whose accounting was missing: steps ran, money
	// was charged, and result.Phases said the phase never happened.
	defer func() {
		result.Phases = append(result.Phases, Phase{
			Name: phase, Steps: len(closed), Spent: phaseSpent, Elapsed: time.Since(phaseStarted),
		})
	}()
	for _, wave := range waves {
		if err := ctx.Err(); err != nil {
			// Not a timeout unless it really was one. A run the user stopped
			// is not a run that ran out of time, and the bin is what a script
			// reads.
			return closed, contract.Fail(contract.StopKind(err),
				"run %s stopped: %v", result.RunID, err)
		}

		// An edge means "after", so a step whose prerequisite did not pass
		// review has nothing honest to run on. It is blocked rather than
		// failed, and only its own branch stops: work in another repository is
		// none of its business.
		runnable := make([]contract.Step, 0, len(wave))
		done := make([]StepResult, 0, len(wave))
		for _, step := range wave {
			if culprit, stuck := blockedBy(step, failed); stuck {
				blocked := StepResult{Step: step, Review: Review{
					Parent: contract.VerdictFailed,
					Reason: "blocked: " + culprit + " did not pass review",
				}}
				done = append(done, blocked)
				a.emitContext(ctx, observability.Event{Kind: "step.closed", StepID: step.ID, Capability: step.Capability, Repository: step.Repository, State: "blocked", Reason: blocked.Review.Reason})
				continue
			}
			runnable = append(runnable, step)
		}
		done = append(done, a.runWave(ctx, result.RunID, runnable, purse)...)
		slices.SortFunc(done, func(x, y StepResult) int { return strings.Compare(x.Step.ID, y.Step.ID) })

		for _, step := range done {
			step.Phase = phase
			phaseSpent.Duration += step.Spent.Duration
			phaseSpent.Tokens += step.Spent.Tokens
			if step.Review.Parent != contract.VerdictOK {
				failed[step.Step.ID] = struct{}{}
			}
			closed = append(closed, step)
			result.Steps = append(result.Steps, step)
			// Only an attempt that reached a provider is a measurement, and
			// the step itself says whether it did. A blocked step never ran:
			// nobody was chosen, no time was spent, and filing it would put a
			// row under an empty implementation that the selector would later
			// read as a real average. A step refused by the core -- an
			// unreachable capability, a payload missing a required field --
			// did have somebody chosen, and reading that as proof of a call
			// is what used to file the caller's own mistake against the
			// implementation the funnel had just picked.
			//
			// A canceled step is the other kind of non-measurement, and the
			// more dangerous one because it does have numbers attached. They
			// are numbers about the user: how long somebody waited before
			// changing their mind, and a failure nobody's provider committed.
			// Filed, they would price a tool by the patience of whoever ran
			// it and mark it down for being interrupted.
			if step.Dispatched && countsAsProviderSample(step.Outcome, step.FailureKind) {
				a.meter.Record(measure(result.RunID, step))
			}
			record.Steps = append(record.Steps, snapshot(step))
			record.Updated = time.Now()
			// A step closing is one of the two moments the paper copy is
			// written. The other is the run itself closing.
			if err := a.checkpoints.Save(*record); err != nil {
				return closed, err
			}
		}
	}
	// A phase closing is one of the two moments measurements are pushed to
	// disk, the other being the process going down. Between them the batch
	// lives in memory, which is the whole point of batching; these two are
	// what stop a crash from taking the batch with it. The push itself is the
	// deferred Settle above, which covers this exit and every other one.
	return closed, nil
}

// blockedBy names the first prerequisite that did not pass review.
func blockedBy(step contract.Step, failed map[string]struct{}) (string, bool) {
	for _, need := range step.Needs {
		if _, bad := failed[need]; bad {
			return need, true
		}
	}
	return "", false
}

// runWave executes one wave and reviews each child as it finishes.
//
// The wave is also where the commission's grant is cut. Every step is handed
// an equal share of whatever is left, which is what stops a wave of four from
// spending the ceiling four times: four shares of a quarter add up to the one
// grant, however hard each of them tries. What a step is actually charged
// comes off the grant as it closes, so the wave behind this one divides the
// money nobody touched.
func (a *Agent) runWave(ctx context.Context, runID string, wave []contract.Step, purse *grant) []StepResult {
	out := make([]StepResult, len(wave))
	limit := a.maxParallel
	if limit <= 0 || limit > len(wave) {
		limit = len(wave)
	}
	shares := purse.shares(len(wave))
	slots := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, step := range wave {
		wg.Add(1)
		go func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			// Registered last so it runs first. A panic in here would
			// otherwise take the process down from a goroutine whose stack
			// says nothing about which step was being run, on a machine where
			// the operator sees only that Atenea stopped existing.
			defer a.notebook.Catch(notebook.Incident{
				Op:         "orchestrator.step",
				RunID:      runID,
				Step:       step.ID,
				Capability: step.Capability,
				Repository: step.Repository,
				Fields:     notebook.FieldsOf(step.Payload),
				Version:    buildinfo.Version,
			})
			// The share is stamped on the step as dispatched, not on the plan:
			// a plan is written before anything ran and cannot know what is
			// left. Recording it here is what lets the trace say afterwards
			// what each step was actually held to.
			step.Permission.BudgetUSD = shares[i]
			out[i] = a.runStep(ctx, step)
			// Spent down as the step closes, whoever it was charged by. A
			// refusal spends nothing and a free provider spends nothing, so
			// the money simply stays for whoever comes next.
			purse.spend(out[i].Outcome.SpentUSD)
			// Only a step that returned -- not one mid-panic and about to
			// take the process with it -- may tell the wave it is done.
			// A deferred Done would run during the panic's own unwind,
			// before Catch's re-thrown panic actually lands, letting
			// runWave's caller read a zero-value result and race an exit
			// that is supposed to have already happened.
			wg.Done()
		}()
	}
	wg.Wait()
	return out
}

// runStep asks the funnel who should answer, hands the job to the runner, and
// then reviews what came back.
func (a *Agent) runStep(ctx context.Context, step contract.Step) StepResult {
	out := StepResult{Step: step}
	if meta, ok := ctx.Value(observabilityContextKey{}).(observabilityContext); ok {
		out.runID, out.sessionID = meta.runID, meta.sessionID
	}
	a.emitContext(ctx, observability.Event{Kind: "step.started", StepID: step.ID, Capability: step.Capability, Repository: step.Repository, State: "running"})
	if !a.card.CanAsk(step.Capability) {
		return a.close(out, contract.Fail(contract.FailureInvalidInput,
			"agent %s may not ask for %s", a.card.ID, step.Capability))
	}

	capability, err := a.catalog.Capability(step.Capability)
	if err != nil {
		return a.close(out, err)
	}
	repository, err := a.catalog.Repository(step.Repository)
	if err != nil {
		return a.close(out, err)
	}
	candidates, err := a.catalog.ImplementationsFor(step.Capability)
	if err != nil {
		return a.close(out, err)
	}
	candidates = a.catalog.Observed(repository.ID, candidates)
	// Derived before the funnel and carried to the measurement, so the health
	// this step ranks on and the row it writes describe the same thing.
	out.Subject = capability.Subject(step.Payload)
	measuring, notices := a.priced(ctx, step.Capability, repository.ID, out.Subject, candidates)
	// Explicit repair remains callable after a failed repair. Never override
	// an operator-declared outage, and permissions are still checked below.
	if step.Capability == "graph.ensure_fresh" {
		declared, _ := a.catalog.ImplementationsFor(step.Capability)
		for i := range candidates {
			for _, impl := range declared {
				if impl.ID == candidates[i].ID && impl.Provider == "kivgraph" && impl.Health.State != contract.HealthDown {
					candidates[i].Health = contract.Health{State: contract.HealthUnknown, Reason: "explicit graph maintenance may retry"}
				}
			}
		}
	}
	reachable := a.runner.Implementations()
	var unreachable map[string]string
	if a.reach != nil {
		reachable, unreachable = a.reach(repository)
	}
	request := contract.RunRequest{
		Capability: capability,
		Repository: repository,
		Payload:    step.Payload,
		Permission: step.Permission,
	}
	// Validate before identity probes: an invalid request must not cause any
	// provider call merely to prepare the funnel. Candidates all answer the
	// same capability; the selected implementation is stamped later.
	var requestErr error
	if len(candidates) > 0 {
		request.Implementation = candidates[0]
		requestErr = request.Validate()
		if requestErr == nil {
			if missing, allowed := request.Allowed(); !allowed {
				requestErr = contract.Fail(contract.FailurePermissionDenied,
					"%s causes %s, which the request permission does not cover", capability.ID, missing)
			}
		}
	}
	observedVersions := make(map[string]string, len(candidates))
	observedInstances := make(map[string]string, len(candidates))
	observedConfigDigests := make(map[string]string, len(candidates))
	observedIdentities := make(map[string]contract.CacheIdentity, len(candidates))
	if requestErr == nil && a.identity != nil {
		eligibility := func(candidate contract.Implementation) bool {
			selection := selector.Request{
				Capability: step.Capability, Repository: repository, Candidates: candidates,
				Reachable: reachable, Unreachable: unreachable, Payload: step.Payload,
				Language: primaryLanguage(repository),
			}
			if pure, ok := a.chooser.(interface {
				Eligible(selector.Request, contract.Implementation) (string, bool)
			}); ok {
				_, fits := pure.Eligible(selection, candidate)
				return fits
			}
			// A custom chooser still receives the strict reach/health gates;
			// its private constraints cannot be evaluated here.
			if !slices.Contains(reachable, candidate.ID) || !candidate.Health.Usable() {
				return false
			}
			_, inaccessible := unreachable[candidate.ID]
			return !inaccessible
		}
		for _, candidate := range candidates {
			if !eligibility(candidate) {
				continue
			}
			request.Implementation = candidate
			probeStarted := time.Now()
			finishIdentityActivity := func(error) {}
			if a.identityActivity != nil {
				finishIdentityActivity = a.identityActivity(ctx, request)
				if finishIdentityActivity == nil {
					finishIdentityActivity = func(error) {}
				}
			}
			a.emitContext(ctx, observability.Event{
				Kind: "provider.identity.started", StepID: step.ID, Capability: step.Capability,
				Repository: step.Repository, Implementation: candidate.ID, Provider: candidate.Provider,
				State: "running", Reason: "checking current runtime identity before selection",
			})
			identity, identityErr := a.identity(ctx, request)
			finishIdentityActivity(identityErr)
			if identity.DurationNS == 0 {
				identity.DurationNS = time.Since(probeStarted).Nanoseconds()
			}
			identity.Observed = true
			if identity.Provider == "" {
				identity.Provider = candidate.Provider
			}
			if identity.Tool == "" {
				identity.Tool = "graph_status"
			}
			observedIdentities[candidate.ID] = identity
			a.emitContext(ctx, observability.Event{
				Kind: "provider.identity", StepID: step.ID, Capability: step.Capability,
				Repository: step.Repository, Implementation: candidate.ID,
				Provider: candidate.Provider, State: "observed", DurationMS: time.Duration(identity.DurationNS).Milliseconds(),
				Reason: func() string {
					if identityErr != nil {
						return identityErr.Error()
					}
					return "current runtime identity"
				}(),
				CacheValidation: &contract.CacheValidation{
					Called: true, Provider: identity.Provider, Tool: identity.Tool,
					ToolVersion: identity.ToolVersion, Instance: identity.Instance, Generation: identity.Generation,
					Snapshot: identity.Snapshot, Freshness: identity.Freshness, Duration: time.Duration(identity.DurationNS), Error: identity.Error,
				},
			})
			if identityErr != nil {
				continue
			}
			if identity.ToolVersion != "" && identity.Instance != "" {
				observedVersions[candidate.ID] = identity.ToolVersion
				observedInstances[candidate.ID] = identity.Instance
				observedConfigDigests[candidate.ID] = selector.ImplementationConfigDigest(candidate)
			}
		}
	}
	decision, err := a.chooser.Select(selector.Request{
		Capability:            step.Capability,
		Repository:            repository,
		Candidates:            candidates,
		Reachable:             reachable,
		Unreachable:           unreachable,
		Measuring:             measuring,
		Payload:               step.Payload,
		Prefer:                step.Prefer,
		Language:              primaryLanguage(repository),
		ObservedVersions:      observedVersions,
		ObservedInstances:     observedInstances,
		ObservedConfigDigests: observedConfigDigests,
	})
	decision.Notices = append(decision.Notices, notices...)
	out.Decision = decision
	a.emitContext(ctx, observability.Event{Kind: "selector.completed", StepID: step.ID, Capability: step.Capability, Repository: step.Repository, Implementation: decision.Chosen.ID, Provider: decision.Chosen.Provider, Count: len(decision.Stages)})
	if err != nil {
		return a.close(out, err)
	}
	if requestErr != nil {
		return a.close(out, requestErr)
	}

	request.Implementation = decision.Chosen
	if identity, ok := observedIdentities[decision.Chosen.ID]; ok {
		request.ObservedIdentity = &identity
	}
	// Stamped on the way in, not after the call returns: a runner that panics
	// or a context that dies mid-flight still spent the provider's time, and
	// a measurement of that is a real measurement.
	out.Dispatched = true
	started := time.Now()
	a.emitContext(ctx, observability.Event{Kind: "tool.started", StepID: step.ID, Capability: step.Capability, Repository: step.Repository, Implementation: decision.Chosen.ID, Provider: decision.Chosen.Provider, State: "running"})
	a.emitContext(ctx, observability.Event{Kind: "provider.started", StepID: step.ID, Capability: step.Capability, Repository: step.Repository, Implementation: decision.Chosen.ID, Provider: decision.Chosen.Provider, State: "running"})
	outcome, runErr := a.runner.Run(ctx, request)
	if outcome.ToolVersion == "" {
		outcome.ToolVersion = observedVersions[decision.Chosen.ID]
	}
	if outcome.ToolInstance == "" {
		outcome.ToolInstance = observedInstances[decision.Chosen.ID]
	}
	out.Outcome = outcome
	if countsAsProviderSample(outcome, runErr) {
		if recorder, ok := a.chooser.(qualityRecorder); ok {
			languages := repository.Languages
			if len(languages) != 1 {
				// A multi-language outcome has no per-language evidence. Keep one
				// generic sample for diagnostics, but selector requests for a
				// concrete language never consult that generic bucket.
				languages = []string{""}
			}
			for _, language := range languages {
				instance := outcome.ToolInstance
				if instance == "" && outcome.CacheValidation != nil {
					instance = outcome.CacheValidation.Instance
				}
				if persistErr := recorder.RecordOutcome(step.Capability, repository.ID, decision.Chosen.ID, language, outcome.ToolVersion, outcome, runErr, repository.Path, instance, selector.ImplementationConfigDigest(decision.Chosen)); persistErr != nil {
					outcome.Notices = append(outcome.Notices, "quality evidence persistence unavailable: "+persistErr.Error())
				}
			}
		}
	}
	out.Outcome = outcome
	// The core's clock is the one that counts. An adapter sees only its own
	// call and would report the purer figure, but what decides between two
	// implementations is the wait the caller actually sat through, round trip
	// included. Tokens and memory still come from the far side, because they
	// are the two things the core has no way to see.
	out.Spent = outcome.Spent
	out.Spent.Duration = time.Since(started)
	providerState := "ok"
	if runErr != nil {
		providerState = "failed"
	}
	providerEvent := observability.Event{Kind: "provider.completed", StepID: step.ID, Capability: step.Capability, Repository: step.Repository, Implementation: decision.Chosen.ID, Provider: decision.Chosen.Provider, State: providerState, DurationMS: out.Spent.Duration.Milliseconds(), Tokens: int64(out.Spent.Tokens), CacheHit: outcome.CacheHit, Coalesced: outcome.Coalesced, CacheValidation: outcome.CacheValidation}
	if runErr != nil {
		providerEvent.Reason = runErr.Error()
	}
	a.emitContext(ctx, providerEvent)
	toolEvent := providerEvent
	toolEvent.Kind = "tool.completed"
	a.emitContext(ctx, toolEvent)
	if runErr != nil {
		// A provider reporting itself unusable is news the catalog needs: the
		// funnel filters on health, and health is owned by whoever probed last.
		// Running a step is a probe.
		//
		// Unless the capability declares a subject, in which case this mark
		// would be a lie in the other direction. The catalog's health is keyed
		// by repository and nothing else, so a page fetch that a single site
		// refused would mark the implementation down for every site -- which
		// is the exact defect the subject column was added to close, arriving
		// through a second door. Measured: after one page behind Cloudflare,
		// an unrelated host went straight to the stealth level with a drop
		// reason still quoting the first.
		//
		// For those the measurement base is the whole record, and a better
		// one: it carries the subject, it has retention, and Baselines reads
		// it back scoped to the site actually being asked about. Giving the
		// catalog a subject dimension instead was the other option and is
		// worse -- that map is persisted, so it would grow one entry per host
		// ever touched and never shrink.
		if contract.AffectsHealth(runErr) && contract.KindOf(runErr) == contract.FailureUnavailable && out.Subject == "" {
			_ = a.catalog.SetHealth(repository.ID, decision.Chosen.ID, contract.Health{
				State:  contract.HealthDown,
				Reason: runErr.Error(),
				Raw:    contract.RawOf(runErr),
			})
		}
		return a.close(out, runErr)
	}
	// The same probe cuts both ways. A later successful, unscoped call must
	// clear an earlier unavailable verdict; otherwise the registry permanently
	// drops an implementation even after its configuration is repaired.
	if out.Subject == "" {
		observed := a.catalog.Observed(repository.ID, []contract.Implementation{decision.Chosen})
		if len(observed) == 1 {
			if recovered, ok := healthAfterSuccessfulProbe(observed[0].Health); ok {
				_ = a.catalog.SetHealth(repository.ID, decision.Chosen.ID, recovered)
			}
		}
	}
	if decision.Chosen.Provider == "kivgraph" && step.Capability == "graph.ensure_fresh" {
		a.reopenGraphQueries(repository.ID)
	}
	return a.close(out, nil)
}

// countsAsProviderSample is the single gate for provider cost and quality
// accounting. A coalesced waiter received a shared answer but did no physical
// provider work, just like a retained cache hit.
func countsAsProviderSample(out contract.Outcome, runErrOrKind any) bool {
	if out.CacheHit || out.Coalesced {
		return false
	}
	switch value := runErrOrKind.(type) {
	case error:
		return value == nil || contract.KindOf(value) != contract.FailureCanceled
	case contract.FailureKind:
		return value != contract.FailureCanceled
	default:
		return true
	}
}

// Verified maintenance permits a new query probe, not an assertion that every
// operation works. Keep this scoped to this repository and runtime observations.
func (a *Agent) reopenGraphQueries(repository string) {
	for _, capability := range []string{"symbol.search", "symbol.intent_search", "symbol.get", "symbol.source", "symbol.overview", "symbol.references", "symbol.calls", "symbol.definition", "symbol.dependencies", "symbol.consumers", "symbol.impact", "code.context", "code.impact"} {
		impls, err := a.catalog.ImplementationsFor(capability)
		if err != nil {
			continue
		}
		for _, impl := range a.catalog.Observed(repository, impls) {
			if impl.Provider == "kivgraph" && impl.Health.State == contract.HealthDown && !impl.Health.ObservedAt.IsZero() {
				_ = a.catalog.SetHealth(repository, impl.ID, contract.Health{State: contract.HealthUnknown, Reason: "graph maintenance verified; query requires a new probe"})
			}
		}
	}
}

// healthAfterSuccessfulProbe turns a runtime down observation into the
// evidence the successful call just supplied. A zero ObservedAt belongs to
// declarative settings: a call may report newer runtime evidence, but it must
// not silently rewrite the operator's starting verdict through this recovery
// path.
func healthAfterSuccessfulProbe(previous contract.Health) (contract.Health, bool) {
	if previous.State != contract.HealthDown || previous.ObservedAt.IsZero() {
		return contract.Health{}, false
	}
	return contract.Health{
		State:  contract.HealthAlive,
		Reason: "a later call here worked, clearing the earlier unavailable verdict",
	}, true
}

// priced fills the candidates with what the base measured for this repository
// and reports whether the funnel may treat those numbers as live.
//
// A base that cannot be read is not a reason to refuse the work: the funnel
// still has the declared estimates and the commission still gets done. But it
// is a reason to say so out loud, because a decision explained by an estimate
// when a measurement exists on disk is a decision nobody can reproduce. The
// second return carries that admission and anything else the base wants the
// trace to know, and is empty when there is nothing to say.
func (a *Agent) priced(ctx context.Context, capability, repository, subject string,
	candidates []contract.Implementation,
) (bool, []string) {
	if a.base == nil {
		return false, nil
	}
	base, err := a.base.Baselines(ctx, capability, repository, subject)
	if err != nil {
		return false, []string{fmt.Sprintf(
			"the measurement base could not be read (%v); ranking on the declared estimates", err,
		)}
	}
	return true, metrics.Apply(base, candidates, time.Now())
}

// close is the parent's review. It runs for every child, always.
func (a *Agent) close(out StepResult, err error) StepResult {
	defer func() {
		state := out.Review.Parent.String()
		if err != nil && state == contract.VerdictUnspecified.String() {
			state = contract.VerdictFailed.String()
		}
		a.emit(observability.Event{Kind: "step.closed", SessionID: out.sessionID, RunID: out.runID, StepID: out.Step.ID, Capability: out.Step.Capability, Implementation: out.Decision.Chosen.ID, Provider: out.Decision.Chosen.Provider, Repository: out.Step.Repository, State: state, Reason: out.Failure, Attempt: 1, DurationMS: out.Spent.Duration.Milliseconds(), Tokens: int64(out.Spent.Tokens)})
	}()
	// Every exit from a step arrives here, which makes this the one place the
	// clock means what the field says. Read at the recorder instead it would
	// be the wave's end for all of them, and beside a duration that turns the
	// quick steps of a wave into steps that started late.
	out.ClosedAt = time.Now()
	child := out.Outcome.Verdict
	if err != nil {
		out.Failure = err.Error()
		out.FailureKind = contract.KindOf(err)
		out.FailureCode = contract.CodeOf(err)
		out.Raw = contract.RawOf(err)
		child = contract.VerdictFailed
	}
	// A step nobody let finish is not reviewed, because there is nothing to
	// review: no output came back, so neither the child nor the parent has
	// seen anything to have an opinion about. Calling that a failed review
	// invents two opinions and blames the work for the interruption.
	if out.FailureKind == contract.FailureCanceled {
		out.Review = Review{
			Child:  contract.VerdictCanceled,
			Parent: contract.VerdictCanceled,
			Reason: "stopped before there was anything to review",
		}
		return out
	}

	parent, reason := a.judge(out, err)
	out.Review = Review{Child: child, Parent: parent, Reason: reason}
	if child != parent {
		out.Review.Disagreed = true
		// The child gets one reply before the parent closes the matter. A tool
		// on the far side of the runner has no voice, so the record says so
		// rather than pretending a reply happened.
		out.Review.Reply = "no reply: a tool answered this step, not an agent"
	}
	return out
}

// judge is what the parent can actually check today: that the work did not
// fail, and that what came back honors the shape the capability promised. A
// child claiming success with an answer nobody can read is not a success.
func (a *Agent) judge(out StepResult, err error) (contract.Verdict, string) {
	if err != nil {
		return contract.VerdictFailed, contract.KindOf(err).String()
	}
	capability, lookupErr := a.catalog.Capability(out.Step.Capability)
	if lookupErr != nil {
		return contract.VerdictFailed, "capability disappeared from the catalog mid-run"
	}
	if outputErr := capability.ValidateOutput(out.Outcome.Result); outputErr != nil {
		return contract.VerdictFailed, "output does not match the capability: " + outputErr.Error()
	}
	if out.Outcome.Verdict != contract.VerdictOK {
		return contract.VerdictFailed, "the step did not report success"
	}
	return contract.VerdictOK, "output matches the capability"
}

// reported collects what each far side said about its own answer.
//
// The orchestrator can describe what a step found, but only the runner knows
// how it found it, and some of that changes what the answer means: a search
// stopped at a ceiling is not the same fact as a search that ran out of
// matches. Two steps hitting the same wall say so once.
func reported(steps []StepResult) []contract.Discovery {
	var out []contract.Discovery
	seen := make(map[string]struct{}, len(steps))
	for _, step := range steps {
		for _, discovery := range step.Outcome.Discoveries {
			if _, dup := seen[discovery.Note]; dup {
				continue
			}
			seen[discovery.Note] = struct{}{}
			out = append(out, discovery)
		}
	}
	return out
}

func totalSpent(steps []StepResult) contract.Sample {
	var spent contract.Sample
	for _, step := range steps {
		spent.Duration += step.Spent.Duration
		spent.Tokens += step.Spent.Tokens
	}
	return spent
}

// totalUSD is what the commission was charged, summed over every step that
// cost money. It is kept apart from totalSpent because money is not one of
// the measured axes: it never reaches the baseline and it never ranks. It is
// here so a human reading the receipt can see what an answer cost them.
// totalUSD adds what the steps were charged, and says whether the sum is a
// measurement.
//
// The rule is Charge.Plus's, applied to a slice: what was measured is added
// up, and a part nobody priced adds nothing rather than erasing the sum. That
// is what Plus does with an unmeasured operand -- it keeps the measured
// amount -- and the first version of this function did the opposite, returning
// a flat (0, false) the moment one step came back unpriced.
//
// What that cost is the case an operator most wants to read. A commission of
// four steps where three were charged $1.20 by a metered provider and the
// fourth never ran, blocked by a failed prerequisite, has a real bill of
// $1.20; totalUSD called it unknown, so `charged_usd` left the JSON entirely
// and the CLI printed no money line at all. The invoice arrived anyway.
//
// The total is therefore a lower bound whenever some part went unpriced, and
// it is reported as measured because it is: every dollar in it was. A
// commission where nobody priced anything -- and one with no steps at all --
// is unknown, which is the distinction this pair of return values exists for.
func totalUSD(steps []StepResult) (usd float64, known bool) {
	for _, step := range steps {
		if !step.Outcome.SpentUSDKnown {
			continue
		}
		usd += step.Outcome.SpentUSD
		known = true
	}
	return usd, known
}

// overallVerdict is the parent's word for the whole commission: one failed
// step is a failed commission, because half-done work presented as done is
// the thing reviewing exists to prevent.
//
// The precedence between the other two is not symmetric, and both directions
// matter. A real failure outranks a cancellation: if one step failed on its
// own and a later one was stopped, the run failed, and saying "canceled" would
// bury a fault behind the interruption. A cancellation outranks success for
// the opposite reason: a run somebody stopped has not been shown to work,
// whatever the steps that did finish managed to do, so calling it "ok" would
// promise a plan was carried out when part of it never ran.
func overallVerdict(steps []StepResult, stopped error) contract.Verdict {
	canceled := stopped != nil
	for _, step := range steps {
		switch step.Review.Parent {
		case contract.VerdictOK:
		case contract.VerdictCanceled:
			canceled = true
		default:
			return contract.VerdictFailed
		}
	}
	if canceled {
		return contract.VerdictCanceled
	}
	if len(steps) == 0 {
		// Nothing ran and nobody stopped it. That is a bug rather than a
		// verdict -- a plan with no steps should never have been dispatched --
		// and it must not read as success.
		return contract.VerdictFailed
	}
	return contract.VerdictOK
}

// Overspend is how far a step's charge ran past the share it was granted,
// zero when it stayed inside it. A far side's own spending ceiling is
// checked between complete turns, not inside one, so a single expensive
// turn can still finish after the money for it had already run out --
// grant.spend already contains that when it happens, clamping the purse at
// zero rather than going into debt, but containing the damage is not the
// same as saying it happened. This is the number that says so, on the same
// receipt as the charge itself.
func Overspend(step StepResult) float64 {
	over := step.Outcome.SpentUSD - step.Step.Permission.BudgetUSD
	if over < 0 {
		return 0
	}
	return over
}

// snapshot is the paper copy of one closed step.
//
// The close time comes off the step rather than off this clock. A step that
// never ran has none of its own -- nobody dispatched it, so there is no moment
// to record but this one -- and it is the only case where filing the recorder's
// instant is the honest answer.
func snapshot(step StepResult) checkpoint.StepState {
	closed := step.ClosedAt
	if closed.IsZero() {
		closed = time.Now()
	}
	return checkpoint.StepState{
		ID:              step.Step.ID,
		Capability:      step.Step.Capability,
		Repository:      step.Step.Repository,
		Implementation:  step.Decision.Chosen.ID,
		Verdict:         step.Review.Child.String(),
		Review:          step.Review.Parent.String(),
		Failure:         step.Failure,
		Raw:             step.Raw,
		Inputs:          auditableInputs(step.Step),
		Discoveries:     step.Outcome.Discoveries,
		DurationMS:      step.Spent.Duration.Milliseconds(),
		Tokens:          int64(step.Spent.Tokens),
		TokensKnown:     step.Spent.Tokens > 0,
		PeakRSS:         step.Spent.PeakRSS,
		RSSKnown:        step.Spent.PeakRSS > 0,
		ToolVersion:     step.Outcome.ToolVersion,
		ToolInstance:    step.Outcome.ToolInstance,
		CacheHit:        step.Outcome.CacheHit,
		Coalesced:       step.Outcome.Coalesced,
		CacheVersion:    step.Outcome.CacheVersion,
		CacheValidation: step.Outcome.CacheValidation,
		SpentUSD:        step.Outcome.SpentUSD,
		SpentUSDKnown:   step.Outcome.SpentUSDKnown,
		OverspendUSD:    Overspend(step),
		ClosedAt:        closed,
		Funnel:          trace(step.Decision),
	}
}

// auditableInputs records what a step was pointed at, for the steps where the
// capability's name does not already say it.
//
// Kept for the device effect and nothing else, and that is a decision about
// receipts rather than about permissions. Every capability's inputs are
// interesting to somebody; recording them all would double the size of every
// receipt on disk to answer a question nobody asks of a code search, whose
// query is recoverable from the commission anyway. What is NOT recoverable is
// which window somebody's machine was pointed at, and that is exactly what an
// auditor opens a receipt to find.
//
// Redacted rather than trusted: a payload is caller-supplied, a receipt is
// durable, and this is the one field where a secret could otherwise come to
// rest on disk.
func auditableInputs(step contract.Step) string {
	if len(step.Payload) == 0 || !slices.Contains(step.Permission.Effects, contract.EffectDevice) {
		return ""
	}
	body, err := json.Marshal(step.Payload)
	if err != nil {
		// The receipt is worth writing without this field; refusing to write
		// one because its annotation would not serialize is the wrong trade.
		return ""
	}
	return contract.RedactRaw(string(body))
}

// trace copies the funnel onto the receipt, or says why there is nothing to
// copy.
//
// The counts come off the stage rather than being recomputed from the drops:
// a stage that dropped nobody still narrowed nothing for a reason worth
// reading, and deriving `in` from `out` plus drops would quietly turn a
// three-candidate stage that dropped none into an empty line.
//
// A decision with no stages did not happen: the step never reached the
// selector, either because it never dispatched or because it was rebuilt from
// a receipt written before traces were kept. That is FunnelNotKept, and it is
// deliberately not FunnelNone -- none is reserved for a step that could not
// have had a funnel at all.
func trace(decision selector.Decision) checkpoint.Funnel {
	if len(decision.Stages) == 0 {
		return checkpoint.Funnel{State: checkpoint.FunnelNotKept}
	}
	stages := make([]checkpoint.FunnelStage, 0, len(decision.Stages))
	for _, stage := range decision.Stages {
		dropped := make([]checkpoint.FunnelDrop, 0, len(stage.Dropped))
		for _, drop := range stage.Dropped {
			dropped = append(dropped, checkpoint.FunnelDrop{
				Implementation: drop.Implementation,
				Reason:         drop.Reason,
				Raw:            drop.Raw,
			})
		}
		quality := make([]checkpoint.FunnelQuality, 0, len(stage.Quality))
		for _, detail := range stage.Quality {
			quality = append(quality, checkpoint.FunnelQuality{
				Implementation: detail.Implementation, State: detail.State,
				Language: detail.Language, ToolVersion: detail.ToolVersion,
				Instance: detail.Instance, ConfigDigest: detail.ConfigDigest, Samples: detail.Samples, Valid: detail.Valid,
				Accepted: detail.Accepted, Complete: detail.Complete, Partial: detail.Partial,
				Truncated:  detail.Truncated,
				OutOfScope: detail.OutOfScope, Failures: detail.Failures,
				Reason: detail.Reason,
			})
		}
		stages = append(stages, checkpoint.FunnelStage{
			Name:    stage.Name,
			In:      len(stage.In),
			Out:     len(stage.Out),
			Dropped: dropped,
			Quality: quality,
		})
	}
	return checkpoint.Funnel{State: checkpoint.FunnelKept, Stages: stages}
}

// measure turns a closed step into a row for the baseline.
//
// It is snapshot's twin and deliberately looks like it: one is the paper copy
// a resumed run reads back, the other is the figure the selector will one day
// rank on, and both are written at exactly the same moment for the same
// reason. A step that failed is measured like any other -- the whole point of
// recording the bin and the reason is that a provider which fails expensively
// should stop looking cheap.
func measure(runID string, step StepResult) metrics.Measurement {
	m := metrics.Measurement{
		At:             time.Now(),
		RunID:          runID,
		StepID:         step.Step.ID,
		Capability:     step.Step.Capability,
		Implementation: step.Decision.Chosen.ID,
		Provider:       step.Decision.Chosen.Provider,
		Repository:     step.Step.Repository,
		Subject:        step.Subject,
		ToolVersion:    step.Outcome.ToolVersion,
		Spent:          step.Spent,
		OK:             step.Review.Parent == contract.VerdictOK,
		Failure:        step.Failure,
		Raw:            step.Raw,
		OutOfScope:     step.Outcome.OutOfScope,
	}
	if step.Failure != "" {
		m.FailureKind = step.FailureKind.String()
	}
	return m
}
