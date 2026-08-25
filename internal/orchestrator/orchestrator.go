// Package orchestrator holds the dedicated agent that turns a commission into
// finished work.
//
// It is an AGENT, deliberately not part of the core. Atenea decides and
// delegates: the core owns the catalog and the funnel and says who should do a
// thing, while this agent is the one that explores, splits the work, hands the
// pieces out and reviews what comes back. Putting the splitting in the core
// would make the core do the work instead of directing it.
//
// The order is fixed: explore first, split second. A microservice has a
// predictable shape, but a large front end does not, and splitting before
// looking produces a plan for a project that is not there. Exploring is not
// free, so it counts as one more measured phase rather than as unbilled
// preparation.
package orchestrator

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/notebook"
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
	Baselines(ctx context.Context, capability, repository string) (map[string]metrics.Baseline, error)
}

// Phase names, in the order they run.
const (
	// PhaseExplore is the look before the split. It is measured like any other
	// phase: a task that hides what exploring cost reports a total that is
	// quietly too low, and the selector then compares against a number that
	// never happened.
	PhaseExplore = "explore"
	// PhaseWork is the split-up commission itself.
	PhaseWork = "work"
	// PhaseAsk is one capability, asked directly. Hoja 15 calls this the
	// atomic base that workflows are built out of; Run is one such workflow,
	// hard-wired. It is a phase of its own rather than a borrowed name so a
	// receipt never claims a run explored or split when it did neither.
	PhaseAsk = "ask"
)

// searchCapability is what a commission's mechanical fallback is planned
// around: literal text search is how the orchestrator splits work when no
// model-backed explorer is attached. A model holding only prose starts with
// code.context instead (contextCapability below), because it does not know a
// string or file yet.
const searchCapability = "code.search"
const contextCapability = "code.context"

// The symbol capabilities, plus the two that read the call graph itself. The
// orchestrator does not plan a commission around any of them -- it plans
// around a text search, which is brick 4's shape and stays -- but it declares
// them because declaring is what makes a step askable at all. A capability
// the card does not name cannot be dispatched even when the catalog, the
// funnel and a runner are all ready for it.
const (
	definitionCapability      = "symbol.definition"
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
	desktopInspectCapability    = "desktop.inspect"
	desktopScreenshotCapability = "desktop.screenshot"
)

// probeContextLines is what exploring asks for: the hit and nothing around it.
// The look is meant to find out WHERE the commission lands, not to read it.
const probeContextLines = 0

// Config wires the agent to the pieces around it.
type Config struct {
	Catalog     Catalog
	Chooser     Chooser
	Runner      contract.Runner
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
	card            contract.Agent
	catalog         Catalog
	chooser         Chooser
	runner          contract.Runner
	checkpoints     *checkpoint.Store
	meter           Meter
	base            Base
	notebook        *notebook.Notebook
	maxParallel     int
	budget          float64
	standingEffects []contract.Effect
}

// card is what the orchestrator declares about itself. Declaring a context
// level is a permission, not a delivery: nothing here is loaded until the
// agent actually reaches for it.
var card = contract.Agent{
	ID:      "orchestrator",
	Type:    contract.AgentOrchestrator,
	Summary: "Explores the repositories in scope, splits the commission into a graph of steps and hands them out.",
	Capabilities: []string{
		contextCapability,
		searchCapability,
		definitionCapability,
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
		desktopAppsCapability,
		desktopInspectCapability,
		desktopScreenshotCapability,
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
		card:            card.Clone(),
		catalog:         cfg.Catalog,
		chooser:         cfg.Chooser,
		runner:          cfg.Runner,
		checkpoints:     store,
		meter:           meter,
		base:            cfg.Base,
		notebook:        cfg.Notebook,
		maxParallel:     cfg.MaxParallel,
		budget:          cfg.BudgetUSD,
		standingEffects: slices.Clone(cfg.StandingEffects),
	}, nil
}

// Card returns the agent's contract.
func (a *Agent) Card() contract.Agent { return a.card.Clone() }

// Runner reports who is behind the agent, or nil when nothing is wired yet.
func (a *Agent) Runner() contract.Runner { return a.runner }

// MaxParallel reports the configured ceiling; zero means no ceiling.
func (a *Agent) MaxParallel() int { return a.maxParallel }

// Task is a commission as it arrives from the user.
type Task struct {
	// Text is the commission in the user's own words. With a single capability
	// wired there is no model in the loop to read intent out of it, so the text
	// is also what gets searched for. That is a property of this brick, not of
	// the design: the moment an adapter brings a model, reading the intent is
	// its job and this field stays exactly as it is.
	Text string
	// Repositories narrows the commission. Empty means every repository the
	// catalog knows, because the unit of work is the repository and the user
	// did not exclude any.
	Repositories []string
	// Effects the user authorized beyond reading. Reading is free by default,
	// so an ordinary search needs nothing here.
	Effects []contract.Effect
	// BudgetUSD is what this commission may spend, across every step. Zero
	// takes the standing grant from the settings file, which is the usual
	// case: the operator granted it once by writing the number down.
	BudgetUSD float64
	// Session is the chat that commissioned this, when there is one. It buys
	// the run nothing: it is written to the receipt so a shared history stays
	// attributable to the isolated chat that produced it.
	Session string
	// Floor is the standing grant this commission composes from. The zero
	// value is the settings file's, which is what a command at a terminal
	// runs on; a chat opened by a client fills it in with the client floor
	// so the operator's own line stays the operator's.
	Floor Floor
}

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
	Matches       int
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
	// Raw is the provider's own text behind Failure, when whatever adapter
	// raised it kept one. Empty on success, and empty on a failure the core
	// raised itself with nothing to quote.
	Raw   string
	Spent contract.Sample
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

// Run carries out a commission from end to end.
func (a *Agent) Run(ctx context.Context, task Task) (result *Result, err error) {
	if strings.TrimSpace(task.Text) == "" {
		return nil, contract.Fail(contract.FailureInvalidInput, "task: text is required")
	}
	if err := validGrant("task", task.BudgetUSD); err != nil {
		return nil, err
	}
	if a.runner == nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"no runner is attached, so nothing can be dispatched")
	}
	repositories, repoErr := a.resolveRepositories(task.Repositories)
	if repoErr != nil {
		return nil, repoErr
	}
	// Reading is free by default. The floor adds whatever was pre-authorized
	// for a commission of this kind -- the settings file's standing grant, or
	// a connected client's own line where the commission came from a chat --
	// and the commission's own Effects adds what this one asked for on top.
	permission := contract.Permission{
		Task:    task.Text,
		Effects: []contract.Effect{contract.EffectRead},
	}.Grant(task.Floor.Or(a.standingEffects)).Grant(task.Effects)
	// The grant is opened once, here, and spent down by every wave. It is the
	// commission that holds it -- not the step, not the adapter -- which is
	// what makes four steps cost one ceiling instead of four.
	budgetUSD := cmp.Or(task.BudgetUSD, a.budget)
	purse := newGrant(budgetUSD)

	started := time.Now()
	result = &Result{RunID: checkpoint.NewID(started), Task: task.Text}
	record := checkpoint.Run{
		ID: result.RunID, Kind: checkpoint.KindTask, Session: task.Session, Task: task.Text,
		Started: started, Repositories: task.Repositories, Effects: task.Effects,
		BudgetUSD: budgetUSD, ContractVersion: contract.Current.String(),
	}

	// The run closing is the second of the two moments the paper copy is
	// written, and it has to happen whether the run finished or was cut short:
	// a commission interrupted halfway is exactly the one worth reading back.
	defer func() {
		result.Spent = totalSpent(result.Steps)
		// Summed above, walled here, and both on the report: a commission that
		// ran its steps in waves costs more time than it takes, and the report
		// is the only place an operator can see that it did.
		result.Elapsed = time.Since(started)
		result.SpentUSD, result.SpentUSDKnown = totalUSD(result.Steps)
		result.Verdict = overallVerdict(result.Steps, ctx.Err())
		result.Matches = countMatches(result.Steps)
		// What the far side said for itself travels too. The summary the
		// orchestrator writes below is what it worked out by looking; this is
		// what the runner reported, and it is the only way something like a
		// search cut short at a ceiling ever reaches the screen.
		result.Discoveries = append(result.Discoveries, reported(result.Steps)...)
		record.Closed = true
		record.Verdict = result.Verdict.String()
		record.Updated = time.Now()
		if saveErr := a.checkpoints.Save(record); saveErr != nil && err == nil {
			err = saveErr
		}
	}()

	// The first plan is light on purpose: look, and decide the rest afterwards.
	plan := contract.Plan{Task: task.Text}
	for _, repo := range repositories {
		payload := map[string]any{"query": task.Text}
		a.hint(payload, "context_lines", probeContextLines)
		plan.Steps = append(plan.Steps, contract.Step{
			ID:         "explore-" + repo.ID,
			Capability: searchCapability,
			Repository: repo.ID,
			Payload:    payload,
			Permission: permission,
		})
	}
	if err := plan.Validate(); err != nil {
		return result, err
	}
	result.Plan = plan
	record.Plan = plan

	explored, err := a.dispatch(ctx, plan, PhaseExplore, result, &record, purse, nil)
	if err != nil {
		return result, err
	}
	result.Discoveries = discoveriesFrom(explored)

	// Now that the shape of each repository is known, split the commission.
	plan, err = plan.Append(a.split(permission, repositories, explored)...)
	if err != nil {
		return result, err
	}
	result.Plan = plan
	record.Plan = plan

	_, err = a.dispatch(ctx, plan, PhaseWork, result, &record, purse, nil)
	return result, err
}

// RunPlan executes a caller-supplied multi-capability DAG.
//
// Run is the opinionated commission flow: explore first, then derive work
// from what was found. Workflows and integrations that already have a
// reviewed graph should not have to fake an exploration phase just to reach
// the same dispatcher. This method is the narrow bridge: contract.Plan owns
// validation and dependency ordering, while the orchestrator still owns
// selection, permissions, review, checkpoints and metering.
//
// The graph is copied before execution. A caller may reuse or inspect its
// plan after this method returns without racing the per-step budget stamps
// that dispatch adds while the run is active.
func (a *Agent) RunPlan(ctx context.Context, plan contract.Plan, budgetUSD float64) (result *Result, err error) {
	if strings.TrimSpace(plan.Task) == "" {
		return nil, contract.Fail(contract.FailureInvalidInput, "plan: task is required")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if err := validGrant("plan", budgetUSD); err != nil {
		return nil, err
	}
	if a.runner == nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"no runner is attached, so nothing can be dispatched")
	}
	if budgetUSD == 0 {
		budgetUSD = a.budget
	}

	plan = plan.Clone()
	started := time.Now()
	result = &Result{RunID: checkpoint.NewID(started), Task: plan.Task, Plan: plan}
	record := checkpoint.Run{
		ID: result.RunID, Kind: checkpoint.KindPlan, Task: plan.Task,
		Started: started, BudgetUSD: budgetUSD,
		ContractVersion: contract.Current.String(), Plan: plan,
	}
	seenRepos := make(map[string]struct{}, len(plan.Steps))
	for _, step := range plan.Steps {
		seenRepos[step.Repository] = struct{}{}
	}
	for repo := range seenRepos {
		record.Repositories = append(record.Repositories, repo)
	}
	slices.Sort(record.Repositories)

	defer func() {
		result.Spent = totalSpent(result.Steps)
		result.Elapsed = time.Since(started)
		result.SpentUSD, result.SpentUSDKnown = totalUSD(result.Steps)
		result.Verdict = overallVerdict(result.Steps, ctx.Err())
		result.Matches = countMatches(result.Steps)
		result.Discoveries = append(result.Discoveries, reported(result.Steps)...)
		record.Closed = true
		record.Verdict = result.Verdict.String()
		record.Updated = time.Now()
		if saveErr := a.checkpoints.Save(record); saveErr != nil && err == nil {
			err = saveErr
		}
	}()

	_, err = a.dispatch(ctx, plan, PhaseWork, result, &record, newGrant(budgetUSD), nil)
	return result, err
}

// Question is one capability asked of one repository, with the payload the
// caller already knows how to build.
//
// This is the atomic base of hoja 15: a workflow is several of these chained,
// and Run is one such chain with its shape written into the code. Symbol
// resolution arrives through here because a position is something the caller
// has and the orchestrator does not -- exploring finds text, and a text hit is
// not a cursor.
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
	Session string
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
	record := checkpoint.Run{
		ID: result.RunID, Kind: checkpoint.KindAsk, Session: q.Session, Task: text,
		Started: started, Repositories: []string{repositories[0].ID}, Effects: q.Effects,
		BudgetUSD: budgetUSD, ContractVersion: contract.Current.String(),
	}
	defer func() {
		result.Spent = totalSpent(result.Steps)
		result.Elapsed = time.Since(started)
		result.SpentUSD, result.SpentUSDKnown = totalUSD(result.Steps)
		result.Verdict = overallVerdict(result.Steps, ctx.Err())
		result.Matches = countMatches(result.Steps)
		result.Discoveries = append(result.Discoveries, reported(result.Steps)...)
		record.Closed = true
		record.Verdict = result.Verdict.String()
		record.Updated = time.Now()
		if saveErr := a.checkpoints.Save(record); saveErr != nil && err == nil {
			err = saveErr
		}
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

// ResumeOptions narrows how a resumed commission continues.
type ResumeOptions struct {
	// BudgetUSD, when set, replaces what remains of the original grant
	// instead of adding to it -- the same rule --budget already follows on a
	// fresh commission.
	BudgetUSD float64
	// Effects adds to what the commission already carries, on top of read,
	// the standing grant and whatever it already held -- it never replaces
	// any of them. Unlike BudgetUSD above, an effect already granted is
	// never worth losing by accident: --allow answers "what else may this
	// do now", not "forget what it already could".
	Effects []contract.Effect
}

// Resume picks an interrupted or failed commission back up.
//
// A step that already passed review is left alone: it is never
// redispatched, re-measured or re-charged, because it already was when it
// first closed. Everything else -- failed, canceled, or never reached at
// all -- is retried exactly as if it were being asked for the first time.
//
// The one thing Resume cannot recover is the shape a splitting decision was
// based on. If the process went away before exploring finished, what it
// found only ever lived in that process's memory, and there is nothing
// honest to split on without it. Resume redoes exploring whole in that case
// rather than guess: exploring is one wave, one step per repository, and
// usually free, so redoing it is cheap where guessing would be wrong. Once
// splitting already ran, the plan on the receipt already carries every
// step's payload, and none of it is recomputed here.
func (a *Agent) Resume(ctx context.Context, runID string, opts ResumeOptions) (result *Result, err error) {
	if err := validGrant("resume", opts.BudgetUSD); err != nil {
		return nil, err
	}
	if a.runner == nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"no runner is attached, so nothing can be dispatched")
	}
	if !a.checkpoints.Enabled() {
		return nil, contract.Fail(contract.FailureUnavailable,
			"checkpointing is off, so there is nothing on file to resume")
	}

	// Locked before anything else is even read: two attempts racing the same
	// receipt would each see the same steps missing and each redispatch
	// them, and neither side could tell from its own view that this had
	// happened.
	release, err := a.checkpoints.Lock(runID)
	if err != nil {
		return nil, err
	}
	defer release()

	record, err := a.checkpoints.Load(runID)
	if err != nil {
		return nil, err
	}
	if err := a.checkResumable(record); err != nil {
		return nil, err
	}

	// A step already on the plan keeps exactly the permission it was stamped
	// with, plus whatever --allow adds now. The operator's standing grant is
	// deliberately NOT re-applied: the floor a commission actually ran on is
	// not on the receipt, and a commission opened by a connected client ran
	// on that client's floor, not on the operator's line. Composing from
	// a.standingEffects here handed such a run every effect the operator ever
	// granted themselves, which is a widening the person who resumed it never
	// asked for and could not see. Until the floor is persisted beside
	// Effects, the only honest reconstruction is the one already on file.
	//
	// A resumed commission can therefore be narrower than the first attempt
	// where the floor did the granting, and that is the safe direction: the
	// step refuses and says which effect it lacked, where the other mistake
	// runs it.
	for i := range record.Plan.Steps {
		record.Plan.Steps[i].Permission = record.Plan.Steps[i].Permission.Grant(opts.Effects)
	}

	budgetUSD := record.BudgetUSD - chargedSoFar(record.Steps)
	if opts.BudgetUSD > 0 {
		budgetUSD = opts.BudgetUSD
	}
	purse := newGrant(budgetUSD)

	// This attempt's own clock, not the interrupted one's. The receipt keeps
	// Started from the first try on purpose -- it is the same commission -- but
	// what this process took is what the screen in front of somebody is about.
	started := time.Now()
	result = &Result{RunID: record.ID, Task: record.Task, Plan: record.Plan}
	// Captured before anything below can add to record.Steps, so this is
	// exactly what passed review in an earlier process: the steps this
	// attempt will never redispatch, and whose only surviving discoveries
	// are on the receipt rather than in memory.
	priorOK := record.OK()
	// The receipt may already read closed -- a clean failure closes it
	// exactly like success does, and a crash may not have gotten that far.
	// Either way this attempt reopens it: if it is interrupted too, the
	// receipt has to say so rather than keep repeating the previous
	// attempt's word.
	record.Closed = false
	defer func() {
		result.Spent = totalSpent(result.Steps)
		result.Elapsed = time.Since(started)
		result.SpentUSD, result.SpentUSDKnown = totalUSD(result.Steps)
		result.Verdict = resumeVerdict(result.Steps, ctx.Err())
		result.Matches = countMatches(result.Steps)
		result.Discoveries = append(result.Discoveries, reported(result.Steps)...)
		result.Discoveries = append(result.Discoveries, discoveriesFromReceipt(record.Steps, priorOK)...)
		record.Closed = true
		record.Verdict = result.Verdict.String()
		record.Updated = time.Now()
		if saveErr := a.checkpoints.Save(record); saveErr != nil && err == nil {
			err = saveErr
		}
	}()

	// A single ask has one step and no split to redo: dispatch it exactly as
	// it was dispatched the first time, and let alreadyOK decide whether
	// there is anything left to do at all.
	if record.Kind == checkpoint.KindAsk {
		_, err = a.dispatch(ctx, record.Plan, PhaseAsk, result, &record, purse, record.OK())
		return result, err
	}
	if record.Kind == checkpoint.KindPlan {
		_, err = a.dispatch(ctx, record.Plan, PhaseWork, result, &record, purse, record.OK())
		return result, err
	}

	// Splitting never ran: redo the look whole rather than trust a shape
	// nothing left on file can justify.
	if !hasWork(record.Plan) {
		repositories, repoErr := a.resolveRepositories(record.Repositories)
		if repoErr != nil {
			return result, repoErr
		}
		// record.Effects never included the implicit read -- Run and Ask
		// both start every permission from it and never store it back, so
		// rebuilding from record.Effects alone would silently drop it here.
		// The floor the commission ran on is the one layer the receipt does
		// not carry, and the standing grant is not a stand-in for it: see the
		// stamped-permission loop above for why re-applying it would widen a
		// client's chat to the operator's own line.
		permission := contract.Permission{
			Task:    record.Task,
			Effects: []contract.Effect{contract.EffectRead},
		}.Grant(record.Effects).Grant(opts.Effects)

		explorePlan := contract.Plan{Task: record.Task}
		for _, repo := range repositories {
			payload := map[string]any{"query": record.Task}
			a.hint(payload, "context_lines", probeContextLines)
			explorePlan.Steps = append(explorePlan.Steps, contract.Step{
				ID:         "explore-" + repo.ID,
				Capability: searchCapability,
				Repository: repo.ID,
				Payload:    payload,
				Permission: permission,
			})
		}
		if err := explorePlan.Validate(); err != nil {
			return result, err
		}
		result.Plan = explorePlan
		record.Plan = explorePlan

		explored, exploreErr := a.dispatch(ctx, explorePlan, PhaseExplore, result, &record, purse, nil)
		if exploreErr != nil {
			return result, exploreErr
		}
		result.Discoveries = discoveriesFrom(explored)

		plan, appendErr := explorePlan.Append(a.split(permission, repositories, explored)...)
		if appendErr != nil {
			return result, appendErr
		}
		result.Plan = plan
		record.Plan = plan

		_, err = a.dispatch(ctx, plan, PhaseWork, result, &record, purse, nil)
		return result, err
	}

	// Splitting already ran, so the plan on file already carries every
	// step's payload. The look is dispatched first and on its own, the same
	// way it would be inside Run, so a step still waiting on it sees it
	// finished rather than dangling once the second call reads the graph.
	alreadyOK := record.OK()
	explorePlan := contract.Plan{Task: record.Plan.Task}
	for _, step := range record.Plan.Steps {
		if len(step.Needs) == 0 {
			explorePlan.Steps = append(explorePlan.Steps, step)
		}
	}
	if err := explorePlan.Validate(); err != nil {
		return result, err
	}
	explored, exploreErr := a.dispatch(ctx, explorePlan, PhaseExplore, result, &record, purse, onlyIn(explorePlan, alreadyOK))
	if exploreErr != nil {
		return result, exploreErr
	}
	result.Discoveries = discoveriesFrom(explored)

	_, err = a.dispatch(ctx, record.Plan, PhaseWork, result, &record, purse, alreadyOK)
	return result, err
}

// checkResumable gates a receipt before anything is touched: a contract
// version this core no longer understands, or a repository or capability the
// catalog no longer has, is refused whole, rather than discovered halfway
// through dispatching with some steps already redone and others not.
func (a *Agent) checkResumable(record checkpoint.Run) error {
	if record.ContractVersion == "" {
		return contract.Fail(contract.FailureInvalidInput,
			"run %s predates resume support and cannot be resumed", record.ID)
	}
	peer, err := contract.ParseVersion(record.ContractVersion)
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput, "run %s: %v", record.ID, err)
	}
	if !contract.Current.Supports(peer) {
		return contract.Fail(contract.FailureInvalidInput,
			"run %s: contract %s is not supported by this core (%s)", record.ID, peer, contract.Current)
	}
	for _, step := range record.Plan.Steps {
		if _, err := a.catalog.Capability(step.Capability); err != nil {
			return err
		}
		if _, err := a.catalog.Repository(step.Repository); err != nil {
			return err
		}
	}
	return nil
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

// split turns what exploring found into the work graph: one step per
// repository in scope, each waiting on its own look, and each narrowed to the
// areas where the commission actually landed.
func (a *Agent) split(permission contract.Permission, repositories []contract.Repository, explored []StepResult) []contract.Step {
	areas := make(map[string][]string, len(explored))
	for _, step := range explored {
		areas[step.Step.Repository] = topLevelAreas(step.Outcome.Result)
	}
	steps := make([]contract.Step, 0, len(repositories))
	for _, repo := range repositories {
		payload := map[string]any{"query": permission.Task}
		// An empty scope means the whole repository. Narrowing to the areas the
		// look found is the whole return on having looked.
		if scope := areas[repo.ID]; len(scope) > 0 {
			a.hint(payload, "scope", scope)
		}
		steps = append(steps, contract.Step{
			ID:         "search-" + repo.ID,
			Capability: searchCapability,
			Repository: repo.ID,
			Payload:    payload,
			Needs:      []string{"explore-" + repo.ID},
			Permission: permission,
		})
	}
	return steps
}

// hint fills an OPTIONAL field, and only when the capability declares it.
//
// The orchestrator is one caller among many: it knows the shape of the job it
// is asking for, not the shape of the schema that answers it. Sending a field
// the capability never declared is a broken request, and it would make the
// agent depend on a nicety that a perfectly valid settings file may leave out.
func (a *Agent) hint(payload map[string]any, field string, value any) {
	capability, err := a.catalog.Capability(searchCapability)
	if err != nil {
		return
	}
	for _, input := range capability.Inputs {
		if input.Name == field {
			payload[field] = value
			return
		}
	}
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
				done = append(done, StepResult{Step: step, Review: Review{
					Parent: contract.VerdictFailed,
					Reason: "blocked: " + culprit + " did not pass review",
				}})
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
			if step.Dispatched && step.FailureKind != contract.FailureCanceled {
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
	measuring, notices := a.priced(ctx, step.Capability, repository.ID, candidates)
	decision, err := a.chooser.Select(selector.Request{
		Capability: step.Capability,
		Repository: repository,
		Candidates: candidates,
		Reachable:  a.runner.Implementations(),
		Measuring:  measuring,
		Payload:    step.Payload,
		Prefer:     step.Prefer,
	})
	decision.Notices = append(decision.Notices, notices...)
	out.Decision = decision
	if err != nil {
		return a.close(out, err)
	}

	request := contract.RunRequest{
		Capability:     capability,
		Implementation: decision.Chosen,
		Repository:     repository,
		Payload:        step.Payload,
		Permission:     step.Permission,
	}
	// A payload missing a required field is a fact the request itself already
	// carries; catching it here means the funnel's own work above -- pricing
	// candidates, choosing among them -- was not spent finding out.
	if err := request.Validate(); err != nil {
		return a.close(out, err)
	}
	// Stamped on the way in, not after the call returns: a runner that panics
	// or a context that dies mid-flight still spent the provider's time, and
	// a measurement of that is a real measurement.
	out.Dispatched = true
	started := time.Now()
	outcome, runErr := a.runner.Run(ctx, request)
	out.Outcome = outcome
	// The core's clock is the one that counts. An adapter sees only its own
	// call and would report the purer figure, but what decides between two
	// implementations is the wait the caller actually sat through, round trip
	// included. Tokens and memory still come from the far side, because they
	// are the two things the core has no way to see.
	out.Spent = outcome.Spent
	out.Spent.Duration = time.Since(started)
	if runErr != nil {
		// A provider reporting itself unusable is news the catalog needs: the
		// funnel filters on health, and health is owned by whoever probed last.
		// Running a step is a probe.
		if contract.KindOf(runErr) == contract.FailureUnavailable {
			_ = a.catalog.SetHealth(repository.ID, decision.Chosen.ID, contract.Health{
				State:  contract.HealthDown,
				Reason: runErr.Error(),
				Raw:    contract.RawOf(runErr),
			})
		}
		return a.close(out, runErr)
	}
	return a.close(out, nil)
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
func (a *Agent) priced(ctx context.Context, capability, repository string,
	candidates []contract.Implementation) (bool, []string) {
	if a.base == nil {
		return false, nil
	}
	base, err := a.base.Baselines(ctx, capability, repository)
	if err != nil {
		return false, []string{fmt.Sprintf(
			"the measurement base could not be read (%v); ranking on the declared estimates", err)}
	}
	return true, metrics.Apply(base, candidates, time.Now())
}

// close is the parent's review. It runs for every child, always.
func (a *Agent) close(out StepResult, err error) StepResult {
	// Every exit from a step arrives here, which makes this the one place the
	// clock means what the field says. Read at the recorder instead it would
	// be the wave's end for all of them, and beside a duration that turns the
	// quick steps of a wave into steps that started late.
	out.ClosedAt = time.Now()
	child := out.Outcome.Verdict
	if err != nil {
		out.Failure = err.Error()
		out.FailureKind = contract.KindOf(err)
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

// topLevelAreas reads the probe's hits and returns the top-level directories
// they landed in, sorted. That is the narrowing the work pass gets for free.
func topLevelAreas(result map[string]any) []string {
	raw, ok := result["matches"].([]any)
	if !ok {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		record, isRecord := item.(map[string]any)
		if !isRecord {
			continue
		}
		where, isText := record["path"].(string)
		if !isText {
			continue
		}
		head, _, nested := strings.Cut(path.Clean(where), "/")
		if !nested {
			// A hit at the repository root cannot be narrowed to a directory,
			// so narrowing at all would silently drop it.
			return nil
		}
		seen[head] = struct{}{}
	}
	areas := make([]string, 0, len(seen))
	for area := range seen {
		areas = append(areas, area)
	}
	slices.Sort(areas)
	return areas
}

// hasWork reports whether plan already carries the split-up commission, not
// only the look that came before it.
func hasWork(plan contract.Plan) bool {
	for _, step := range plan.Steps {
		if len(step.Needs) > 0 {
			return true
		}
	}
	return false
}

// chargedSoFar sums what a receipt's steps were actually charged. Every
// entry on file, including a failed retry, represents a dispatch that really
// happened and really spent whatever it reports -- summing them is what
// tells a resume how much of the original grant is left to work with.
func chargedSoFar(steps []checkpoint.StepState) float64 {
	var usd float64
	for _, step := range steps {
		usd += step.SpentUSD
	}
	return usd
}

// onlyIn filters ids down to the ones that name a step of plan. alreadyOK
// carries every id a receipt closed with, but a sub-plan dispatched on its
// own only recognizes the ids that are actually its own steps -- anything
// else is a step LayersAfter has never heard of.
func onlyIn(plan contract.Plan, ids []string) []string {
	known := make(map[string]struct{}, len(plan.Steps))
	for _, step := range plan.Steps {
		known[step.ID] = struct{}{}
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := known[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// discoveriesFrom turns what the look saw into facts worth keeping, so the
// next commission does not pay to learn the same thing twice.
func discoveriesFrom(explored []StepResult) []contract.Discovery {
	out := make([]contract.Discovery, 0, len(explored))
	for _, step := range explored {
		if step.Review.Parent != contract.VerdictOK {
			continue
		}
		areas := topLevelAreas(step.Outcome.Result)
		note := fmt.Sprintf("%s: %d hit(s) for %q", step.Step.Repository,
			matchCount(step.Outcome.Result), step.Step.Permission.Task)
		if len(areas) > 0 {
			note += ", under " + strings.Join(areas, ", ")
		}
		out = append(out, contract.Discovery{Level: contract.ContextRepository, Note: note})
	}
	return out
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

// discoveriesFromReceipt recovers what steps closed in an earlier process
// found. They are never redispatched on resume, so reported -- which only
// looks at result.Steps -- cannot see them; this reads the same facts back
// from the receipt instead, the one place they survived the process that
// discovered them.
func discoveriesFromReceipt(steps []checkpoint.StepState, ids []string) []contract.Discovery {
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []contract.Discovery
	seen := make(map[string]struct{})
	for _, step := range steps {
		if _, ok := want[step.ID]; !ok {
			continue
		}
		for _, discovery := range step.Discoveries {
			if _, dup := seen[discovery.Note]; dup {
				continue
			}
			seen[discovery.Note] = struct{}{}
			out = append(out, discovery)
		}
	}
	return out
}

func matchCount(result map[string]any) int {
	raw, _ := result["matches"].([]any)
	return len(raw)
}

func countMatches(steps []StepResult) int {
	total := 0
	for _, step := range steps {
		if step.Phase == PhaseWork {
			total += matchCount(step.Outcome.Result)
		}
	}
	return total
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

// resumeVerdict is overallVerdict adjusted for one case only Resume can
// reach: a call that redispatched nothing at all. For Run and Ask that is a
// bug -- a plan with no steps should never have been dispatched -- but a
// resumed commission reaches it legitimately whenever every step was
// already on file with review ok and dispatch had nothing left to
// schedule. Nothing new ran and nobody stopped it is not a bug here; it is
// the receipt already saying done.
func resumeVerdict(steps []StepResult, stopped error) contract.Verdict {
	if len(steps) == 0 && stopped == nil {
		return contract.VerdictOK
	}
	return overallVerdict(steps, stopped)
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
		ID:             step.Step.ID,
		Capability:     step.Step.Capability,
		Repository:     step.Step.Repository,
		Implementation: step.Decision.Chosen.ID,
		Verdict:        step.Review.Child.String(),
		Review:         step.Review.Parent.String(),
		Failure:        step.Failure,
		Raw:            step.Raw,
		Discoveries:    step.Outcome.Discoveries,
		DurationMS:     step.Spent.Duration.Milliseconds(),
		SpentUSD:       step.Outcome.SpentUSD,
		SpentUSDKnown:  step.Outcome.SpentUSDKnown,
		OverspendUSD:   Overspend(step),
		ClosedAt:       closed,
		Funnel:         trace(step.Decision),
	}
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
		stages = append(stages, checkpoint.FunnelStage{
			Name:    stage.Name,
			In:      len(stage.In),
			Out:     len(stage.Out),
			Dropped: dropped,
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
