package workflow

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/allowance"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Dispatcher spawns one agent and answers what it said. It is the whole of
// what this package needs from the runner, and [agent.Runner] is it.
type Dispatcher interface {
	Dispatch(ctx context.Context, d agent.Dispatch) (contract.Report, contract.Assignment, error)
	// NextID mints an execution id ahead of the spawn, so the step can be
	// recorded as running before it is.
	NextID() string
}

// Floor is what starting a turn costs before any work happens: the cache write
// of a system prompt and a tool catalog, priced.
//
// Measured, never declared. The figure belongs to one repository and one model
// together -- the tool schemas that repository serves and that model's prices
// are both inside it -- and it moves whenever the client's system prompt or
// that catalog changes. So every figure carries when it was taken and what
// took it, and there is no constant in Go anybody could read as the answer.
type Floor struct {
	// USD is the cold-equivalent price of the prefix alone: what
	// establishing this turn's cache costs, once, on whichever run of the
	// hour is first. It is reported and no longer refused against -- see
	// WarmUSD, and checkFunding for which of the two binds.
	USD float64
	// WarmUSD is what starting a step costs once this machine's cache holds
	// the prefix AND the block its first tool call brings with it: the
	// ordinary case for every step but one, and the figure the floor rule
	// refuses a share against. Zero means no probe has priced it, and the
	// rule falls back to USD, saying so in the refusal -- an overcharge a
	// person can see beats a rule that silently stops applying.
	WarmUSD float64
	// StartTokens is the evidence under WarmUSD: prefix plus first-call
	// block, in tokens, cache-state invariant. Zero when nothing measured
	// the first call.
	StartTokens int
	// MeasuredAt is when this figure was taken. A floor with no date is a
	// claim, not a measurement.
	MeasuredAt time.Time
	// CLIVersion is the client that produced it, bare ("2.1.227"). Empty
	// means the probe could not read one, which is reported as silence
	// rather than as a version.
	CLIVersion string
	// CacheWriteTokens is what the turn pays for before it reads a file: the
	// system prompt and the tool definitions. It is the evidence half of the
	// figure -- a dollar amount is a number to argue with, a token count that
	// bought nothing is not -- and zero means the measurement did not carry
	// one, never that a turn was free.
	CacheWriteTokens int
	// StartWeight is the input-equivalent weight of establishing this turn's
	// whole start from cold -- its prefix and the block its first tool call
	// brings -- see allowance.StartWeight. Measured 2026-08-15, it is paid
	// ONCE per machine per cache lifetime, not once per step, so it is no
	// longer what a step is refused against. It is reported so a person can
	// see what the first run of the hour carries. Zero means this measurement
	// carries no token count at all, so neither weight can be answered --
	// never that starting is free.
	StartWeight int
	// WarmStartWeight is the same start once both blocks are cached: the
	// per-step reading, twenty times smaller, and the number the allowance
	// rule actually refuses a step against. Half of a share must buy more
	// input-equivalent tokens of reading than this, or the reserved-answer
	// nudge fires by the time the step's first tool call returns and it
	// answers having read one thing. Zero has the same meaning as above.
	WarmStartWeight int
}

// Floors answers what starting a turn costs, for a repository, an agent
// type, and the model that agent type spends against.
//
// Keyed by agent because the tool surface is most of the cost, not the
// model. Measured 2026-08-14, same repository (taxiprime-backend) and same
// model (claude-opus-5), two agent types: explore -- Atenea MCP tools, Read,
// Glob -- cost $0.28 and 27,666 tokens of cache write; plan -- no tools at
// all -- cost $0.06 and 4,991 tokens of cache write. 81% of the floor is the
// tool schemas, not the system prompt. A key of (repository, model) alone
// conflates the two: measuring plan overwrote the row explore reads, and the
// cheap $0.06 figure then governed the expensive explore steps -- the check
// waving through exactly the steps it exists to refuse.
//
// An interface because the measurements live in a file this package does not
// own, and because a caller who has measured nothing must be able to hand over
// nothing at all. The bool is "nobody has measured this triple yet", which is
// not an error: it is the state every machine starts in.
type Floors interface {
	Floor(ctx context.Context, repository, agent, model string) (Floor, bool, error)
}

// Options configure the engine.
type Options struct {
	// Runner spawns the agents. Required.
	Runner Dispatcher
	// Store is the record. Required: a workflow that leaves nothing behind
	// cannot be resumed, and resumption is half of why this exists.
	Store *Store
	// Types are the declared agent types, used to compile a graph and to
	// resolve each step's lane.
	Types []config.AgentType
	// Lanes are the ceilings, one per pool. Zero means no ceiling.
	Lanes config.Workflow
	// Now is the clock. Defaults to time.Now.
	Now func() time.Time
	// IDs mints workflow ids. Defaults to a timestamp plus a counter.
	IDs func() string
	// PID is the process that owns what this engine runs. Defaults to this
	// one; a test uses it to write a run that belongs to a process which no
	// longer exists.
	PID int
	// Alive reports whether a pid is still running. Defaults to the real
	// check; the resume path is the only caller and it must not guess.
	Alive func(pid int) bool
	// Poll is how often a gate's row is re-read while waiting. Defaults to a
	// quarter second. It is a poll and not a subscription because the answer
	// may come from another process entirely, and a channel only reaches the
	// one holding it.
	Poll time.Duration
	// Surface names where this engine's own answers come from, for the gate
	// log. Defaults to "cli".
	Surface string
	// Repository is which repository these runs are about, resolved by
	// WorkspaceFor the same way every agent resolves it. Recorded on the run
	// so what a step cost can later be read back scoped to the tree it was
	// spent on -- exploring a six-file repository and this one are not the
	// same act at the same price. Empty is honest and means machine-wide.
	Repository string
	// RepositoryRoot is the directory those runs would actually happen in,
	// which is a different fact from the id above and the one that decides
	// where a step reads. Both are needed in a refusal because an id can be
	// uninformative while still being real: the shipped settings declare a
	// repository `current` at `path = "."` (config/default.toml), so "would
	// serve current" is a true statement that names no tree. Empty means the
	// caller did not say; nothing is inferred from it.
	RepositoryRoot string
	// Floors answers what starting a turn costs on a repository with a
	// model, from what somebody measured. Nil turns the check off, which is
	// what a caller that has measured nothing has: no measurement, no claim.
	Floors Floors
	// ModelFor resolves which model an agent type spends against, so a
	// step's share can be held against the floor for the model it would
	// actually pay. Empty means the type runs no model turn this engine can
	// price, and such a step is never refused. Nil turns the check off with
	// Floors.
	ModelFor func(agentType string) string
}

// Engine runs graphs.
type Engine struct {
	runner   Dispatcher
	store    *Store
	types    []config.AgentType
	lanes    config.Workflow
	now      func() time.Time
	ids      func() string
	pid      int
	alive    func(pid int) bool
	poll     time.Duration
	surface  string
	repo     string
	repoRoot string
	floors   Floors
	modelFor func(agentType string) string
}

// New builds an engine.
func New(opts Options) (*Engine, error) {
	if opts.Runner == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"workflow: a runner is required")
	}
	if opts.Store == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"workflow: a store is required: a run nobody wrote down cannot be resumed")
	}
	e := &Engine{
		runner:   opts.Runner,
		store:    opts.Store,
		types:    opts.Types,
		lanes:    opts.Lanes,
		now:      opts.Now,
		ids:      opts.IDs,
		pid:      opts.PID,
		alive:    opts.Alive,
		poll:     opts.Poll,
		repo:     opts.Repository,
		repoRoot: opts.RepositoryRoot,
		surface:  opts.Surface,
		floors:   opts.Floors,
		modelFor: opts.ModelFor,
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.ids == nil {
		e.ids = defaultIDs()
	}
	if e.pid == 0 {
		e.pid = os.Getpid()
	}
	if e.alive == nil {
		e.alive = pidlock.Alive
	}
	if e.poll <= 0 {
		e.poll = 250 * time.Millisecond
	}
	if e.surface == "" {
		e.surface = "cli"
	}
	return e, nil
}

// Create compiles a graph, writes it down, and stops.
//
// Nothing spawns. The run is left holding an unanswered launch gate, which is
// the whole point of the split: a plan is a thing to read before it is a
// thing to run, and the reading is a separate act from the commissioning.
//
// A plan that funds a step below what starting a turn costs is refused here,
// before the record exists, so the person who would have approved it reads
// the arithmetic instead of the receipt.
//
// The returned Gate carries the digest the launch will be checked against.
func (e *Engine) Create(ctx context.Context, graph Graph) (Run, Gate, error) {
	plan, err := Compile(graph, e.types)
	if err != nil {
		return Run{}, Gate{}, err
	}
	if err := e.checkFunding(ctx, "", plan, nil); err != nil {
		return Run{}, Gate{}, err
	}
	id := e.ids()
	at := e.now()
	if err := e.store.Create(ctx, id, plan, e.repo, at, 0); err != nil {
		return Run{}, Gate{}, err
	}
	gate, err := e.store.Ask(ctx, id, KindLaunch,
		Proposal{Steps: plan.Graph.Steps}, at)
	if err != nil {
		// The run exists and nothing authorized it. Leaving it there is what
		// produced runs with every step pending and no gate: `list` showed
		// them and `resume` executed them. The discard is best effort because
		// the refusal above is the news -- a cleanup that failed must not
		// replace the reason the plan was turned down.
		if discardErr := e.store.Discard(ctx, id); discardErr != nil {
			return Run{}, Gate{}, contract.Fail(contract.KindOf(err),
				"%v (and the unauthorized run %s could not be discarded: %v)", err, id, discardErr)
		}
		return Run{}, Gate{}, err
	}
	run, err := e.store.Load(ctx, id)
	if err != nil {
		return Run{}, Gate{}, err
	}
	return run, gate, nil
}

// observedMinRows is how many clean, completed rows a median needs before it
// is allowed to refuse a plan.
//
// Three, because Observed's own doc says a median of two is a rumor, and the
// middle of two rows is just an endpoint. It is not a measured constant and
// does not pretend to be: it is the point where this project agreed a median
// starts meaning something, written once instead of in each caller.
const observedMinRows = 3

// deadSpendRatio converts what a step already spent dying at its own
// ceiling, once, in the SAME run, into a prediction of what it needs to
// finish -- see Redo's own doc for why every row workflow_attempt holds got
// there by dying first.
//
// Measured 2026-08-16 on wf1786845363956-1: three steps redone at a raised
// share, after conversation.charge was fixed, finished at 0.92x, 1.08x and
// 1.09x of what they had already spent dying -- median 1.08x, n=3
// (deadSpendRatioN). It is a median, not a floor: the lowest of the three
// finished for LESS than it had already burned, so this rule can ask for
// less than a step already spent, and that is not a defect in it.
//
// It is preferred over the type median (below) whenever it exists, because
// it is a narrower and stronger claim: what THIS step needed, not what the
// rest of its type needed. Averaging a step's own receipt into fifteen
// others' would throw the more specific number away.
const (
	deadSpendRatio  = 1.08
	deadSpendRatioN = 3
)

// underfunded is one step, the share it was handed, and why that share does
// not clear this repository's requirements for the agent type and model it
// runs against.
type underfunded struct {
	step  string
	usd   float64
	agent string
	model string
	floor Floor
	// needUSD is the largest of what the floor requires, what the allowance
	// rule requires, and what this step needs to finish -- either its OWN
	// dead spend, redone, when it died once already in this run, or (when
	// it never has) the type median. The number a person raises the share
	// to. Zero when the measurement carries no token count at all, so the
	// second requirement cannot be computed and bound is "unmeasured".
	needUSD float64
	// bound names which requirement needUSD came from: "floor", "allowance",
	// "dead-spend", "observed", or "unmeasured" when the row carries no
	// token count to check either probe rule against.
	bound string
	// weight is the row's StartWeight: the input-equivalent weight of
	// the turn's own first assistant event, cold-equivalent. Zero only when
	// bound is "unmeasured".
	weight int
	// tokens is what the funded share actually buys, in the same
	// input-equivalent unit as weight -- allowance.Tokens(usd).
	tokens int
	// deadSpendUSD is what this exact step already spent the one time it
	// died at its own ceiling in this run, when bound is "dead-spend". Zero
	// otherwise -- including when the step DID die but a different rule won,
	// which is why this is not read as "has this step ever died".
	deadSpendUSD float64
	// observed is the median of what CLEAN, completed steps of this agent
	// type have cost, with n behind it and the scope those rows were read
	// at. Zero when nothing has been measured, which is not zero cost: it is
	// no claim, and then only the two probe rules apply.
	//
	// Population, not this step: it is superseded by deadSpendUSD for any
	// step that has its own dead-spend figure. Measured 2026-08-16: the
	// probe rules asked $0.06 where a real reader step cost $0.30-$0.44,
	// and eighteen of twenty-three steps died in the gap.
	observed      float64
	observedN     int
	observedScope string
}

// checkFunding refuses a plan that funds a step below any of four measured
// requirements, for the agent type that step runs and the model that agent
// type spends against on this repository: the floor, what starting a turn
// costs before any file is read; and the rescuable threshold, the point past
// which half the step's own share -- the read allowance, see
// internal/allowance -- outweighs the turn's own first assistant event,
// cold-equivalent. A step funded above the floor but below the threshold
// still spawns, still spends its whole ceiling reading, and still dies with
// nothing written -- only later, and after its money is gone.
//
// The floor half of this: measured 2026-08-14, on one real 18-step plan run
// twice, 17 of its 18 steps were funded $0.12-$0.28 against a turn that cost
// ~$0.35 before a single file was read -- 25,340 tokens of cache write for
// the system prompt and the tool definitions. All twelve steps that got as
// far as spawning died at their ceiling with result_len = 0; $3.78 and $3.57
// spent across the two runs, nothing answered.
//
// The threshold half: the arithmetic -- half of a share, 83,000
// input-equivalent tokens to the dollar -- was measured the same day and
// written into a doc comment, and this rule did not exist yet to read it. A
// real turn's own first assistant event already weighed 65,625 of them
// (~$0.20), which is why a step must clear ~$0.84 on that repository and
// model before its allowance buys any reading at all where the floor alone
// asks only ~$0.27. Run the next night, thirteen model-backed steps --
// funded above the floor -- died empty anyway: $5.24 spent against a $3.52
// grant. A measurement that stays a comment is documentation, not a control.
//
// The observed half, added 2026-08-16: both rules above price a PROBE -- one
// turn that starts, makes at most one tool call, and stops. A step that does
// the work costs several times that, and the two probe rules cannot see it.
// Twenty-three steps on taxiprime-backend, every share cleared by both rules
// on the day: five finished at $0.30-$0.44 and eighteen died at their
// ceilings having read real files and written nothing, for $4.41 and no
// deliverable. The third rule is the median of the rows that FINISHED, which
// is the only number in this system measured on the whole act. It is also
// why the shares were wrong in a shape no probe could have caught: they were
// scaled by file bytes, and the receipts put a 35x byte range inside a 2x
// cost range. A step costs about what a step costs.
//
// The fourth rule, added 2026-08-16, is the only one keyed to a single step
// rather than a population: a step that already died once in THIS run
// carries its own receipt for what finishing it needs, and that figure
// supersedes the type median for that step alone -- see deadSpendRatio for
// the arithmetic and its provenance. What the type median asks of everyone
// else, this step answers for itself.
//
// All four rules are evaluated in one pass, and every underfunded step is
// reported once, against whichever requirement is largest for its own
// measured row -- see underfunded and refusal -- so a person raises a share
// once rather than clearing one gate and hitting the next on the following
// attempt.
//
// A row with no token count at all cannot answer the threshold question, and
// is refused outright rather than checked against the floor alone: falling
// back to the weaker rule whenever the stronger one cannot be evaluated is
// the exact defect this whole rule exists to end.
//
// A type NO PROBE has priced is not skipped any more. It has no floor and no
// threshold, and the observed rule needs neither: finished rows are finished
// rows. Exempting the types this machine has the most evidence about, because
// the one thing missing was a probe, was backwards.
//
// THE ENGINE NEVER TOPS A STEP UP TO ANY OF THE FOUR REQUIREMENTS. Quietly
// raising a share to make a plan runnable is Atenea spending money nobody
// approved, and it is worse than the defect above because it is silent: the
// reader of the receipt would have no way to tell the figure they authorized
// from the one the engine preferred. Refusing is the honest move. The shares
// in a plan are its author's, and they stay its author's.
// deaths, when given, supplies what a step already spent dying instead of
// reading it back from the archive. Redo needs that: the number lives on the
// live row until Reset files it, and Reset is a write -- so a check that had to
// read the archive could only run after the writes it exists to prevent.
func (e *Engine) checkFunding(ctx context.Context, id string, plan Plan, deaths map[string]float64) error {
	if e.floors == nil || e.modelFor == nil {
		return nil
	}
	// One lookup per (agent, model) rather than per step: the measured fact
	// is per (repository, agent, model) -- see [Floors] -- and the store
	// behind this re-reads its file on every call.
	type key struct{ agent, model string }
	type lookup struct {
		floor Floor
		known bool
	}
	asked := make(map[key]lookup, 2)
	// What a completed step of each type has actually cost, read once. A
	// failure here is not a refusal: the two probe rules still apply, and a
	// store that cannot answer what things cost is not evidence that a share
	// is adequate.
	costs := CostTable{Types: map[string]Observed{}}
	if got, err := e.store.CostByType(ctx, e.repo); err == nil {
		costs = got
	}
	var under []underfunded
	for i := range plan.Graph.Steps {
		step := &plan.Graph.Steps[i]
		// No model, no claim: an agent type this engine cannot price is one
		// nobody measured a turn for, and inventing a floor for it would be
		// the written-down constant this whole thing exists to avoid.
		model := e.modelFor(step.TypeName)
		if step.Route != nil && step.Route.Model != "" {
			model = step.Route.Model
		}
		if model == "" {
			continue
		}
		k := key{agent: step.TypeName, model: model}
		got, seen := asked[k]
		if !seen {
			floor, known, err := e.floors.Floor(ctx, e.repo, step.TypeName, model)
			if err != nil {
				// A measurement that cannot be read is not a measurement
				// that says yes. Reading a corrupt cache as "no floor known"
				// would launch exactly the plan this check exists to stop.
				return err
			}
			got = lookup{floor: floor, known: known}
			asked[k] = got
		}
		usd := step.Permission.BudgetUSD
		// This exact step's own history, first: id is empty at Create --
		// a brand-new plan has no run to look an attempt up against -- and
		// non-empty at Redo, where it is the one place this can ever be
		// non-zero, on the one step being raised.
		deadUSD, hadDeath := e.deadSpend(ctx, id, step.ID)
		if usd, given := deaths[step.ID]; given {
			deadUSD, hadDeath = usd, true
		}
		// What completed steps of this type have cost, population-wide.
		// Read before the probe gating below, because it is the one rule
		// that does not need a probe to have run: a type nobody measured a
		// turn for can still have five finished rows on the record, and
		// skipping the check there would exempt exactly the types this
		// machine knows the most about.
		obs := costs.Types[step.TypeName]
		predictedUSD, predictedBound := 0.0, ""
		switch {
		case hadDeath:
			predictedUSD, predictedBound = deadSpendRatio*deadUSD, "dead-spend"
		case obs.N >= observedMinRows:
			predictedUSD, predictedBound = obs.MedianUSD, "observed"
		}
		if !got.known {
			if predictedUSD > 0 && usd+moneyEpsilon < predictedUSD {
				under = append(under, underfunded{
					step:          step.ID,
					usd:           usd,
					agent:         step.TypeName,
					model:         model,
					needUSD:       predictedUSD,
					bound:         predictedBound,
					deadSpendUSD:  deadUSD,
					observed:      obs.MedianUSD,
					observedN:     obs.N,
					observedScope: costs.Repository,
				})
			}
			continue
		}
		if got.floor.WarmStartWeight == 0 {
			// The row cannot answer the threshold question at all. Refuse
			// rather than fall back to the floor alone, whatever usd is --
			// see checkFunding's own doc.
			under = append(under, underfunded{
				step:  step.ID,
				usd:   usd,
				agent: step.TypeName,
				model: model,
				floor: got.floor,
				bound: "unmeasured",
			})
			continue
		}
		// Below is below, and zero is not exempt: a step handed nothing is
		// the extreme of this defect, not an exception to it. The epsilon is
		// the one Compile divides shares with, so a share that equals the
		// floor is not refused over the last bit of binary floating point.
		// Tokens is compared strictly: a share that buys exactly the first
		// event and no more still nudges the model to answer before it has
		// read anything of its own.
		//
		// Against the WARM weight, not the cold one. A step is one of many
		// on a machine that has already established this prefix, and
		// measured 2026-08-15 the cold write happens once and is read by
		// everything after it -- see allowance.WarmStartWeight. The
		// cold figure still travels on the row, and refusal prints it, so
		// the person sizing a grant can see what the hour's first run adds.
		tokens := allowance.Tokens(usd)
		// The floor charged per step is the warm one when a probe has priced
		// this row's first tool call, and the cold prefix price only while
		// nothing has. Measured 2026-08-15: the cold figure is ~8x the warm
		// one on a real row, so which of the two a rule reads is not a
		// rounding question.
		floorUSD, floorKind := got.floor.USD, "floor"
		if got.floor.WarmUSD > 0 {
			floorUSD, floorKind = got.floor.WarmUSD, "warm-floor"
		}
		// The third and fourth rules, and the only ones measured on work
		// rather than on a probe: what this step needed to finish after its
		// own death, or -- absent that -- what a step of this type has cost
		// to FINISH. See deadSpendRatio and observedMinRows.
		okFloor := usd+moneyEpsilon >= floorUSD
		okAllowance := tokens > got.floor.WarmStartWeight
		okPredicted := usd+moneyEpsilon >= predictedUSD
		if okFloor && okAllowance && okPredicted {
			continue
		}
		needUSD, bound := floorUSD, floorKind
		if rescuable := allowance.MinShareUSD(got.floor.WarmStartWeight); rescuable > needUSD {
			needUSD, bound = rescuable, "allowance"
		}
		if predictedUSD > needUSD {
			needUSD, bound = predictedUSD, predictedBound
		}
		under = append(under, underfunded{
			step:    step.ID,
			usd:     usd,
			agent:   step.TypeName,
			model:   model,
			floor:   got.floor,
			needUSD: needUSD,
			bound:   bound,
			weight:  got.floor.WarmStartWeight,
			tokens:  tokens,
			// Carried whatever bound won, and whatever else is known: a
			// share refused by a probe rule while real rows say something
			// larger is a person's next question, not a footnote.
			deadSpendUSD:  deadUSD,
			observed:      obs.MedianUSD,
			observedN:     obs.N,
			observedScope: costs.Repository,
		})
	}
	if len(under) == 0 {
		return nil
	}
	return contract.Fail(contract.FailureInvalidInput, "%s",
		refusal(e.repo, len(plan.Graph.Steps), under))
}

// deadSpend returns what step stepID already spent the one time it was cut
// at its own ceiling within run id, and whether it has one.
//
// id empty (Create, where no run exists yet to hold an attempt) or the
// lookup itself failing both read as "no death" rather than an error: this
// predictor is an upgrade over the type median, never the only source of
// truth, so a store that cannot answer falls back to the population rule
// exactly as CostByType's own failure does above.
//
// Every row workflow_attempt holds arrived through Redo, and Redo only
// archives a step CutAtItsCeiling -- see that table's own doc -- so the
// figure this returns, when it returns one, is always a killed attempt,
// never a live one. The last one, when a step has died more than once: the
// most recent dead spend is the best available estimate of what it needs.
func (e *Engine) deadSpend(ctx context.Context, id, stepID string) (float64, bool) {
	if id == "" {
		return 0, false
	}
	attempts, err := e.store.Attempts(ctx, id, stepID)
	if err != nil || len(attempts) == 0 {
		return 0, false
	}
	last := attempts[len(attempts)-1]
	if last.Spent.USD == nil {
		return 0, false
	}
	return *last.Spent.USD, true
}

// refusal writes what a person needs in order to act: the arithmetic for
// every step that is under, against whichever of the two rules requires
// more, and where the underlying measurement came from.
//
// Every underfunded step is named, however many there are. A person about to
// approve a plan is reading this to decide whether to raise seventeen shares or
// throw the plan away, and a list that stopped at five would hide the size of
// the decision. They are aligned for the same reason: the columns are meant to
// be compared down the page, not read as sentences.
func refusal(repository string, steps int, under []underfunded) string {
	// An empty repository id is honest and means machine-wide -- see
	// Options.Repository -- so it is said in words rather than left as a gap
	// in a sentence, and the re-measure line drops a flag it cannot fill.
	where := repository
	if where == "" {
		where = "this machine"
	}
	verb := "are"
	if len(under) == 1 {
		verb = "is"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "workflow create refused: %d of %s %s funded below what a step needs to answer\n\n",
		len(under), plural(steps, "step", "steps"), verb)

	// needs is precomputed per step so the column can be aligned like the
	// others -- funded $ and needs $ are meant to be compared down the page.
	wide, money, need := 0, 0, 0
	needs := make([]string, len(under))
	for i, u := range under {
		wide = max(wide, len(u.step))
		money = max(money, len(strconv.FormatFloat(u.usd, 'f', 2, 64)))
		needs[i] = "?"
		if u.bound != "unmeasured" {
			needs[i] = fmt.Sprintf("$%.2f", centsUp(u.needUSD))
		}
		need = max(need, len(needs[i]))
	}
	for i, u := range under {
		fmt.Fprintf(&b, "  %-*s   funded $%*.2f   needs %-*s   %s\n",
			wide, u.step, money, u.usd, need, needs[i], clause(u))
	}

	// The provenance once per (agent, model), not once per step: seventeen
	// copies of the same measurement would bury the list it is there to
	// support. In the order the plan named them, so the same refusal reads
	// the same way twice.
	type said struct{ agent, model string }
	told := make([]said, 0, 2)
	for _, u := range under {
		row := said{agent: u.agent, model: u.model}
		if slices.Contains(told, row) {
			continue
		}
		told = append(told, row)
		// No probe ever priced this row, so there is no floor to date or
		// version. Saying so is the provenance: the number that refused this
		// step came from finished runs, and a reader who goes looking for a
		// floor measurement must not be sent after one that does not exist.
		if u.floor.MeasuredAt.IsZero() {
			fmt.Fprintf(&b, "\nno probe has priced %s as %s with %s on this machine; the "+
				"requirement above is what %d finished runs of it cost.\n",
				where, u.agent, u.model, u.observedN)
			continue
		}
		line := fmt.Sprintf("\nthe floor for %s as %s with %s was measured %s",
			where, u.agent, u.model, u.floor.MeasuredAt.Local().Format("2006-01-02 15:04"))
		if u.floor.CLIVersion != "" {
			line += " on claude code " + u.floor.CLIVersion
		}
		switch {
		case u.floor.CacheWriteTokens == 0:
			// A measurement that carried no token count ends its sentence
			// instead of promising evidence it does not have.
			b.WriteString(line + ".\n")
		case u.floor.StartTokens > u.floor.CacheWriteTokens:
			// A probed row: name the two blocks separately, because the
			// second one is 7x the first and a reader sizing a share has to
			// see which number is doing the work.
			b.WriteString(line + ":\n")
			fmt.Fprintf(&b, "%s tokens for the system prompt and tool definitions, and %s more "+
				"arriving with the step's first tool call -- %s before it has read a second "+
				"thing.\n",
				grouped(u.floor.CacheWriteTokens),
				grouped(u.floor.StartTokens-u.floor.CacheWriteTokens),
				grouped(u.floor.StartTokens))
		default:
			b.WriteString(line + ":\n")
			fmt.Fprintf(&b, "%s tokens of cache write -- system prompt and tool definitions -- "+
				"before any file is read. No probe has priced this row's first tool call.\n",
				grouped(u.floor.CacheWriteTokens))
		}
		if u.floor.WarmStartWeight != 0 {
			fmt.Fprintf(&b, "half of a share is the read allowance, %s input-equivalent tokens "+
				"to the dollar, so a share must exceed $%.2f before it buys any reading at "+
				"all.\n", grouped(allowance.Tokens(1)),
				centsUp(allowance.MinShareUSD(u.floor.WarmStartWeight)))
			fmt.Fprintf(&b, "that is the warm figure, which is what a step pays: those bytes are "+
				"written to cache once and read by everything after them. Establishing them "+
				"cold weighs %s input-equivalent tokens, paid by whichever run of the hour is "+
				"first, and by no other.\n",
				grouped(u.floor.StartWeight))
		}
	}

	b.WriteString("\nnothing was written and no budget was changed. " +
		"Raise the shares in the plan, or re-measure with\n`atenea floor measure")
	if repository != "" {
		b.WriteString(" --repo " + repository)
	}
	b.WriteString("`.\n")
	return b.String()
}

// clause names, in prose, the requirement that bound for one step -- what a
// person reads after the columns to know which rule to satisfy and by how
// much.
func clause(u underfunded) string {
	switch u.bound {
	case "unmeasured":
		return "the measurement carries no token count, so what a share buys cannot be checked"
	case "allowance":
		return fmt.Sprintf("half a share buys %s tokens of reading; its prompt and first "+
			"tool call weigh %s, read from cache",
			grouped(u.tokens), grouped(u.weight))
	case "dead-spend":
		return fmt.Sprintf("this step already spent $%.2f dying at its own ceiling once in "+
			"this run -- a step-specific figure, not a population one: steps redone after a "+
			"death needed a median %.2fx more to finish (n=%d measured pairs, not a floor -- "+
			"the lowest of the three finished for less than it had already spent)",
			u.deadSpendUSD, deadSpendRatio, deadSpendRatioN)
	case "observed":
		scope := "on this machine"
		if u.observedScope != "" {
			scope = "on " + u.observedScope
		}
		return fmt.Sprintf("a step of this type has cost $%.2f to finish, median of %d "+
			"completed runs %s -- the probes price starting a turn, these priced doing "+
			"the work", u.observed, u.observedN, scope)
	case "warm-floor":
		return fmt.Sprintf("starting a step costs ~$%.2f warm -- %s tokens of prefix and "+
			"first tool call, read from cache",
			u.floor.WarmUSD, grouped(u.floor.StartTokens))
	default: // "floor"
		return fmt.Sprintf("starting a turn costs ~$%.2f, and no probe has priced this "+
			"row's first tool call", u.floor.USD)
	}
}

// centsUp rounds usd up to the nearest cent. The number in a refusal is one a
// person may type back as a share; rounding down would print a figure that
// still refuses the person who typed it.
//
// The epsilon is the one the rest of this check compares money with, and it is
// load-bearing here rather than decorative: a requirement of exactly $0.55
// arrives as 0.5500000000000000444, and a bare ceiling turns that into $0.56 --
// a cent nobody measured, printed as if it were the measurement. Found by a
// test on an observed median 2026-08-16.
func centsUp(usd float64) float64 {
	return math.Ceil(usd*100-moneyEpsilon) / 100
}

// grouped writes an integer with thousands separators. The token count in a
// refusal is evidence somebody has to weigh at a glance, and 25,340 reads as a
// quantity where 25340 reads as a serial number.
func grouped(n int) string {
	digits := strconv.Itoa(n)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	if len(digits) <= 3 {
		return sign + digits
	}
	var b strings.Builder
	b.Grow(len(sign) + len(digits) + (len(digits)-1)/3)
	b.WriteString(sign)
	head := len(digits) % 3
	if head == 0 {
		head = 3
	}
	b.WriteString(digits[:head])
	for i := head; i < len(digits); i += 3 {
		b.WriteByte(',')
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// Launch answers a run's launch gate and drives it.
//
// The two halves are one call because they are one act: the person saying yes
// is the person committing the grant, and a launch recorded by somebody who
// then did not run it would leave an approval with nothing behind it.
func (e *Engine) Launch(ctx context.Context, id string) (Run, error) {
	// Before the gate, not after it. sameRepository is also checked in
	// takeOver, which is what covers `run` and `resume` -- but reaching it
	// through Launch would answer the gate first, and a run whose grant was
	// committed and whose execution then refused reads as "launched already"
	// on the retry. The mistake this refuses is one somebody fixes by typing
	// the command again with a flag, so the gate has to survive it.
	recorded, err := e.store.Load(ctx, id)
	if err != nil {
		return Run{}, err
	}
	if err := e.sameRepository(recorded); err != nil {
		return recorded, err
	}
	gate, ok, err := e.store.OpenGate(ctx, id)
	if err != nil {
		return Run{}, err
	}
	if !ok {
		// Nothing waiting has two causes, and they are not the same news.
		// Reading gate 0 rather than assuming: a plan somebody refused is
		// not a plan that already ran, and telling its author it was
		// launched would send them looking for work that never happened.
		launch, gateErr := e.store.Gate(ctx, id, 0)
		if gateErr == nil && launch.Decision == DecisionRejected {
			return Run{}, contract.Fail(contract.FailureInvalidInput,
				"workflow %s was rejected at %s by %s: %s",
				id, launch.Answered.Local().Format(time.RFC3339), launch.Hand, launch.Reason)
		}
		return Run{}, contract.Fail(contract.FailureInvalidInput,
			"workflow %s has nothing waiting: it was launched already", id)
	}
	if gate.Kind != KindLaunch {
		return Run{}, contract.Fail(contract.FailureInvalidInput,
			"workflow %s is waiting on an expansion, not a launch: approve it with `atenea workflow approve %s`",
			id, id)
	}
	if _, err := e.store.Answer(ctx, id, gate.Ordinal, DecisionApproved,
		Hand(e.surface), "", e.now()); err != nil {
		return Run{}, err
	}
	return e.Run(ctx, id)
}

// Effects reports every effect the steps of a recorded workflow may cause.
//
// It reads and only reads, which is the point: a surface that has to authorize
// a launch has to know what it is authorizing BEFORE it commits anything.
// Launching and checking afterwards authorizes by doing.
//
// Separate from Graph.Effects because the two answer about different things: a
// graph is a file somebody may still edit, a run is what was written down. A
// launch must be held to the second.
func (e *Engine) Effects(ctx context.Context, id string) ([]contract.Effect, error) {
	run, err := e.store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	seen := make(map[contract.Effect]struct{}, 4)
	for _, row := range run.Steps {
		for _, effect := range row.Step.Permission.Effects {
			seen[effect] = struct{}{}
		}
	}
	out := make([]contract.Effect, 0, len(seen))
	for effect := range seen {
		out = append(out, effect)
	}
	slices.Sort(out)
	return out, nil
}

// Run drives a workflow that is already on disk: it takes the run over and
// executes whatever the graph and the gates say to do next.
func (e *Engine) Run(ctx context.Context, id string) (Run, error) {
	run, err := e.takeOver(ctx, id)
	if err != nil {
		return run, err
	}
	plan, err := e.replan(run)
	if err != nil {
		return run, err
	}
	// run.WriterPID is what takeOver saw and decided on. Handing it back makes
	// the claim conditional on that observation still holding, so an Atenea
	// that slipped in between the two loses the UPDATE instead of sharing the
	// run.
	if err := e.store.Own(ctx, id, e.pid, run.WriterPID); err != nil {
		return run, err
	}
	return e.execute(ctx, id, plan)
}

// Start compiles a graph, launches it, and runs it, as one command from one
// hand.
//
// This is `atenea workflow run PATH`: the person typed the path, so the
// reading and the commissioning are the same act and the gate log says so.
// The MCP surface does not have this -- there a model must call create, and a
// person must call launch.
func (e *Engine) Start(ctx context.Context, graph Graph) (Run, error) {
	_, gate, err := e.Create(ctx, graph)
	if err != nil {
		return Run{}, err
	}
	return e.Launch(ctx, gate.RunID)
}

// takeOver refuses a run that is finished, that another live Atenea holds, or
// that would be executed against a repository other than the one it was
// created for.
func (e *Engine) takeOver(ctx context.Context, id string) (Run, error) {
	run, err := e.store.Load(ctx, id)
	if err != nil {
		return Run{}, err
	}
	if run.Closed {
		return run, contract.Fail(contract.FailureInvalidInput,
			"workflow %s already finished at %s", id, run.Ended.Local().Format(time.RFC3339))
	}
	// The two-Ateneas case. A record saying running is not evidence that
	// anything runs; the pid is. Taking a live run over would double every
	// step still in flight -- and a run parked on a gate holds its pid the
	// same way, so this is also what keeps two processes from racing to
	// apply one answer.
	if run.WriterPID != 0 && run.WriterPID != e.pid && e.alive(run.WriterPID) {
		return run, contract.Fail(contract.FailureUnavailable,
			"workflow %s is running under pid %d", id, run.WriterPID)
	}
	if err := e.sameRepository(run); err != nil {
		return run, err
	}
	return run, nil
}

// sameRepository refuses to execute a run against a repository other than the
// one it was created for.
//
// Measured 2026-08-16: a 23-step plan created with `--repository
// taxiprime-backend` was launched WITHOUT the flag, from a shell sitting in
// Atenea's own checkout. `create` priced every floor against taxiprime-backend
// and wrote that id on the run. `launch`, given no flag, resolved the
// repository the ordinary way -- and resolved it to something real: the
// settings declare `current` at Atenea's own path, so WorkspaceFor matched it
// by directory and served that. Twenty-three readers went looking for a Fastify
// surface in a Go orchestrator. Eight answered that the files did not exist,
// fifteen died searching, $5.88 of a $5.22 grant went, and not one route was
// inventoried.
//
// The shape worth keeping: NEITHER side was invalid. Two declared repositories,
// one priced and one served, and no code path compared them -- the run's
// repository was written by Create and read only by checkFunding. This is not a
// missing fallback, it is a missing comparison, which is why the fix is a
// refusal and not a default.
//
// The refusal names BOTH sides, because either one alone sends the reader to
// the wrong place: told only what the run recorded, they re-run the same
// command; told only what would be served, they doubt the plan.
//
// A run that recorded nothing is not refused. Those rows predate the column
// (see the schema's own note on repository) and cannot say what they were
// created for; refusing them would break resuming work nobody can re-create.
//
// The comparison is on ids -- what `create` recorded, what the floor is keyed
// on, what the operator types. The MESSAGE also carries the root, because a
// real id can name no tree: "would serve current" is true and useless, and the
// directory is the fact the whole failure turned on.
func (e *Engine) sameRepository(run Run) error {
	if run.Repository == "" || run.Repository == e.repo {
		return nil
	}
	serving := strconv.Quote(e.repo)
	if e.repo == "" {
		// Said as a sentence rather than printed as "": no repository served
		// is not a repository named badly, and what the steps then get is
		// wherever the launching process happens to be standing.
		serving = "no repository at all"
	}
	if e.repoRoot != "" {
		serving += " in " + e.repoRoot
	}
	return contract.Fail(contract.FailureInvalidInput,
		"workflow %s was created for repository %s and this launch would serve %s;"+
			" its funding was priced against %s, so re-run it with --repository %s",
		run.ID, strconv.Quote(run.Repository), serving,
		strconv.Quote(run.Repository), run.Repository)
}

// Resume continues a run that was cut or whose Atenea died.
//
// redo names steps to dispatch again even though this would not otherwise
// touch them: the interrupted ones that may have written something. Naming a
// step that is not interrupted is refused rather than ignored, because
// silently doing nothing to a step somebody asked about reads as having
// redone it.
func (e *Engine) Resume(ctx context.Context, id string, redo []string) (Run, error) {
	run, err := e.takeOver(ctx, id)
	if err != nil {
		return run, err
	}

	plan, err := e.replan(run)
	if err != nil {
		return run, err
	}

	// Anything still marked running belonged to a process that is gone.
	// Nobody read its report, so nobody judged it.
	at := e.now()
	for _, step := range run.Steps {
		if step.Status != StatusRunning {
			continue
		}
		why := "the atenea running it died"
		if run.Stop == StopAborted {
			why = "cut by abort"
		}
		if err := e.store.Interrupt(ctx, id, step.Step.ID, why, at); err != nil {
			return run, err
		}
	}
	if run, err = e.store.Load(ctx, id); err != nil {
		return Run{}, err
	}

	forced := make(map[string]bool, len(redo))
	for _, name := range redo {
		forced[name] = true
	}
	for _, name := range sortedKeys(forced) {
		step, ok := stepRow(run, name)
		if !ok {
			return run, contract.Fail(contract.FailureNotFound,
				"workflow %s has no step %s", id, name)
		}
		if step.Status != StatusInterrupted {
			return run, contract.Fail(contract.FailureInvalidInput,
				"workflow %s step %s is %s, not interrupted: --redo is for steps "+
					"nobody judged", id, name, step.Status)
		}
	}

	// An interrupted step is re-dispatched only when re-running it cannot
	// land twice. Read-only work is free to repeat; a step that may write or
	// reach outside was stopped without anybody seeing how far it got, so
	// repeating it could duplicate an effect that already happened. Those
	// wait for somebody to say --redo.
	for _, step := range run.Steps {
		if step.Status != StatusInterrupted {
			continue
		}
		if !forced[step.Step.ID] && touchesTheWorld(step.Step.Permission.Effects) {
			continue
		}
		if err := e.store.Reset(ctx, id, step.Step.ID, e.now()); err != nil {
			return run, err
		}
	}
	// run.WriterPID is what takeOver saw and decided on. Handing it back makes
	// the claim conditional on that observation still holding, so an Atenea
	// that slipped in between the two loses the UPDATE instead of sharing the
	// run.
	if err := e.store.Own(ctx, id, e.pid, run.WriterPID); err != nil {
		return run, err
	}
	return e.execute(ctx, id, plan)
}

// Raise is one step named for re-dispatch, and the share it is to run under.
type Raise struct {
	StepID string
	USD    float64
}

// Redo dispatches steps that were cut at their own spending ceiling, at shares
// somebody raised, and reopens a finished run to do it.
//
// This is deliberately not part of Resume, and it is deliberately not
// automatic.
//
// Not Resume, because Resume is for work nobody judged -- a step cut by Ctrl-C
// or by a dead Atenea, which may simply be run again as it was. A step cut at
// its ceiling WAS judged: it reported, the report said incomplete, and the
// record is complete. What it lacks is not a verdict but money, and reopening
// a finished run to spend more of it is a different act with a different
// receipt.
//
// Not automatic, because a step that died at its ceiling dies again at the
// same share. Retrying it unchanged would burn real money to reproduce a
// result already on the record -- which is why every share here must be a
// RAISE, refused otherwise, naming both figures. Measured 2026-08-16: of 150
// steps cut at a ceiling, 2 were ever re-dispatched, and the only path was to
// write a new plan.
//
// What it leaves behind is the pair the admission rule now reads directly.
// Reset files the dead attempt into workflow_attempt with the share it ran
// under before anything is rewritten, then Reshare writes the new one -- so
// the record holds "cut at $0.62" and "finished at $0.80" as two rows of one
// step, and the second is a measurement of what the first needed. Measured
// 2026-08-16: checkFunding reads this pair for the exact step being raised
// and prefers it over CostByType's population median, which still excludes
// ceiling deaths because their spend is a lower bound -- see deadSpendRatio.
//
// grant is the run's new total, or zero to leave it alone. A raise that no
// longer fits under the grant is refused by [Store.Ask] naming what is left,
// rather than quietly extending it: the grant is the figure somebody
// authorized, and a redo that moved it by itself would make the check that
// exists to catch unapproved spend unable to fail.
func (e *Engine) Redo(ctx context.Context, id string, raises []Raise, grant float64) (Run, error) {
	if len(raises) == 0 {
		return Run{}, contract.Fail(contract.FailureInvalidInput,
			"workflow redo needs at least one step to dispatch again")
	}
	run, err := e.store.Load(ctx, id)
	if err != nil {
		return Run{}, err
	}
	// Everything a live Atenea would race on is refused first. Closed is NOT
	// checked -- reopening is the whole point -- but a run somebody else is
	// executing right now is the same hazard here as anywhere.
	if run.WriterPID != 0 && run.WriterPID != e.pid && e.alive(run.WriterPID) {
		return run, contract.Fail(contract.FailureUnavailable,
			"workflow %s is running under pid %d", id, run.WriterPID)
	}
	if err := e.sameRepository(run); err != nil {
		return run, err
	}

	// Validated in full before a single write. A refusal on the third of three
	// steps must not leave the first two resharded and the run reopened.
	seen := make(map[string]bool, len(raises))
	steps := make([]Step, 0, len(raises))
	deaths := make(map[string]float64, len(raises))
	for _, raise := range raises {
		if seen[raise.StepID] {
			return run, contract.Fail(contract.FailureInvalidInput,
				"workflow %s: step %s named twice", id, raise.StepID)
		}
		seen[raise.StepID] = true
		row, ok := stepRow(run, raise.StepID)
		if !ok {
			return run, contract.Fail(contract.FailureNotFound,
				"workflow %s has no step %s", id, raise.StepID)
		}
		if !row.CutAtItsCeiling() {
			return run, contract.Fail(contract.FailureInvalidInput,
				"workflow %s step %s is %s and was not cut at its ceiling: "+
					"redo is for a step that ran out of its own share%s",
				id, raise.StepID, row.Status, elsewhere(row))
		}
		was := row.Step.Permission.BudgetUSD
		if raise.USD <= was+moneyEpsilon {
			return run, contract.Fail(contract.FailureInvalidInput,
				"workflow %s step %s was cut at its ceiling of $%.2f: "+
					"$%.2f is not a raise, and the same share buys the same result",
				id, raise.StepID, was, raise.USD)
		}
		step := row.Step.Clone()
		step.Permission.BudgetUSD = raise.USD
		steps = append(steps, step)
		// What this attempt spent before it was cut, taken from the live row.
		// Reset is what files this into the archive, and Reset is a write; a
		// funding check that could only read the archive was therefore a
		// check that could only run after the writes.
		if row.Spent.USD != nil {
			deaths[raise.StepID] = *row.Spent.USD
		}
	}

	// Before a single write, which is what the paragraph above always claimed
	// and the code did not do.
	//
	// checkFunding used to run last: after Regrant, after the gate was asked
	// and approved, and after every named step had been Reset and resharded.
	// A refusal there left the step `pending` -- so CutAtItsCeiling() was
	// false and a second redo refused it -- on a run still closed, so resume
	// refused it too. The step was unreachable by any command, and the only
	// evidence of why was a gate answered "approved" for work that never ran.
	prospective, err := e.replan(withShares(run, steps, grant))
	if err != nil {
		return run, err
	}
	if err := e.checkFunding(ctx, id, prospective, deaths); err != nil {
		return run, err
	}

	totalGrant := run.GrantUSD
	if grant > 0 {
		totalGrant = grant
	}
	required := 0.0
	for _, row := range run.Steps {
		if row.TraceID != "" {
			if row.Spent.USD != nil {
				required += *row.Spent.USD
			} else {
				required += row.Step.Permission.BudgetUSD
			}
		}
	}
	for _, row := range run.Superseded {
		if row.Spent.USD != nil {
			required += *row.Spent.USD
		} else {
			required += row.GrantUSD
		}
	}
	for _, raise := range raises {
		required += raise.USD
	}
	if required > totalGrant+moneyEpsilon {
		return run, contract.Fail(contract.FailurePermissionDenied, "redo requires $%.2f including previous attempts; grant is $%.2f", required, totalGrant)
	}

	if grant > 0 {
		if err := e.store.Regrant(ctx, id, grant); err != nil {
			return run, err
		}
		if run, err = e.store.Load(ctx, id); err != nil {
			return Run{}, err
		}
	}

	// The gate first, while the run is still closed. Ask checks the raises
	// against what the grant has left, so a redo that does not fit refuses
	// here with nothing written and the run still finished.
	at := e.now()
	gate, err := e.store.Ask(ctx, id, KindRedo, Proposal{Steps: steps}, at)
	if err != nil {
		return run, err
	}
	// Asked and answered in one act, the same way Start commits a launch: the
	// person who typed the shares is the person authorizing them, and a
	// question they would answer themselves is not a question.
	if _, err := e.store.Answer(ctx, id, gate.Ordinal, DecisionApproved,
		Hand(e.surface), "", at); err != nil {
		return run, err
	}

	// Reset before Reshare, always. Reset files the dead attempt with the
	// share it actually ran under; doing it after would archive the new figure
	// and lose the half of the pair that was measured.
	for _, raise := range raises {
		if err := e.store.Reset(ctx, id, raise.StepID, at); err != nil {
			return run, err
		}
		if err := e.store.Reshare(ctx, id, raise.StepID, raise.USD); err != nil {
			return run, err
		}
	}
	if run, err = e.store.Load(ctx, id); err != nil {
		return Run{}, err
	}
	plan, err := e.replan(run)
	if err != nil {
		return run, err
	}
	// run.WriterPID is what takeOver saw and decided on. Handing it back makes
	// the claim conditional on that observation still holding, so an Atenea
	// that slipped in between the two loses the UPDATE instead of sharing the
	// run.
	if err := e.store.Own(ctx, id, e.pid, run.WriterPID); err != nil {
		return run, err
	}
	return e.execute(ctx, id, plan)
}

// elsewhere names the path that does serve a step redo just refused, when
// there is one. A refusal that only says no leaves the operator guessing
// between resume, a new plan, and a typo.
func elsewhere(row StepRow) string {
	if row.Status == StatusInterrupted {
		return ": nobody judged this one, so `atenea workflow resume --redo` runs it as it was"
	}
	return ""
}

// touchesTheWorld reports whether repeating this step could land an effect
// twice. Process is not one of them: every agent spawns a process to answer at
// all, and a second spawn of a read-only agent changes nothing outside itself.
func touchesTheWorld(effects []contract.Effect) bool {
	return slices.Contains(effects, contract.EffectWrite) ||
		slices.Contains(effects, contract.EffectExternal)
}

// withShares copies a run with some steps replaced and, when one is given, a
// new grant -- so a plan can be compiled from what a change WOULD produce
// without the change having happened.
//
// The grant belongs here rather than being left at its old value: a redo that
// raises the grant and the shares together is one act, and compiling the new
// shares against the old ceiling would refuse it for a limit it is in the
// middle of lifting.
func withShares(run Run, steps []Step, grant float64) Run {
	replaced := make(map[string]Step, len(steps))
	for _, step := range steps {
		replaced[step.ID] = step
	}
	out := run
	if grant > 0 {
		out.GrantUSD = grant
	}
	out.Steps = make([]StepRow, len(run.Steps))
	copy(out.Steps, run.Steps)
	for i := range out.Steps {
		if step, ok := replaced[out.Steps[i].Step.ID]; ok {
			out.Steps[i].Step = step
		}
	}
	return out
}

// replan rebuilds the compiled plan from what is on disk. The graph is read
// back rather than re-derived: a resume must run the graph that was dispatched,
// not the one today's settings would produce.
func (e *Engine) replan(run Run) (Plan, error) {
	graph := Graph{Task: run.Task, GrantUSD: run.GrantUSD}
	for _, step := range run.Steps {
		graph.Steps = append(graph.Steps, step.Step)
	}
	return Compile(graph, e.types)
}

// grow compiles the graph an approved proposal would produce, without writing
// anything.
//
// Compiled before it is applied, so a proposal that would make an
// uncompilable graph -- a cycle, two concurrent writers of one file, shares
// past the grant -- is refused with nothing half-written behind it.
func (e *Engine) grow(run Run, p Proposal) (Plan, error) {
	replaced := make(map[string]bool, len(p.Replaces))
	for _, id := range p.Replaces {
		replaced[id] = true
	}
	graph := Graph{Task: run.Task, GrantUSD: run.GrantUSD}
	for _, step := range run.Steps {
		if replaced[step.Step.ID] {
			continue
		}
		graph.Steps = append(graph.Steps, step.Step)
	}
	graph.Steps = append(graph.Steps, p.Steps...)
	return Compile(graph, e.types)
}

// await blocks until a gate is answered, however long that takes.
//
// Nothing times out. A question that expires into a default is not a
// question, and the whole point of the gate is that a person decided.
//
// It polls the row rather than waiting on a channel because the answer
// arrives from another process -- the CLI, or a second Atenea serving MCP --
// and because a process that dies here must leave the question standing. The
// record is the only thing both of those have in common.
func (e *Engine) await(ctx context.Context, id string, ordinal int) (Gate, error) {
	read := context.WithoutCancel(ctx)
	ticker := time.NewTicker(e.poll)
	defer ticker.Stop()
	for {
		gate, err := e.store.Gate(read, id, ordinal)
		if err != nil {
			return Gate{}, err
		}
		if !gate.Waiting() {
			return gate, nil
		}
		select {
		case <-ctx.Done():
			return Gate{}, contract.Fail(contract.FailureCanceled,
				"workflow %s: cut while waiting on gate %d", id, ordinal)
		case <-ticker.C:
		}
	}
}

// unwind stops a dispatch loop that is leaving, whatever the reason.
//
// Both halves are load-bearing and neither used to happen reliably. `results`
// is unbuffered, so a goroutine whose step has finished blocks on its send
// until somebody reads it -- and canceling the context does not release a
// blocked send. Two exits called cancel() and wg.Wait() with nobody left
// reading and deadlocked outright, hanging the command forever; the other
// exits returned without either, stranding those goroutines and the agent
// processes they hold, in a service that does not exit between runs.
//
// The drain is complete when the WaitGroup is: each goroutine sends and only
// then defers its Done, so a returned Wait means every send was received.
func unwind(cancel context.CancelFunc, wg *sync.WaitGroup, results <-chan done) {
	cancel()
	landed := make(chan struct{})
	go func() {
		wg.Wait()
		close(landed)
	}()
	for {
		select {
		case <-results:
		case <-landed:
			return
		}
	}
}

// done is one finished dispatch, handed back to the single writer.
type done struct {
	stepID string
	status Status
	report contract.Report
}

// execute is the loop: launch everything ready that has a free slot in its
// lane, wait for one to finish, write it down, look again.
//
// One goroutine writes. The steps run in parallel and answer on a channel, and
// every database write happens here between two waits -- so a status flip is
// never half-applied and never races another.
func (e *Engine) execute(ctx context.Context, id string, plan Plan) (Run, error) {
	run, err := e.store.Load(ctx, id)
	if err != nil {
		return Run{}, err
	}

	status := make(map[string]Status, len(run.Steps))
	attempts := make(map[string]int, len(run.Steps))
	traces := make(map[string]string, len(run.Steps))
	// answers is what a subject edge hands over. Seeded from the record
	// rather than only from this process's own results: a resumed run
	// dispatches reviewers of steps that finished under an Atenea that is
	// gone, and the answer they audit has to be the one that was given.
	answers := make(map[string]StepRow, len(run.Steps))
	// rejections holds the card for a step a review refused and this loop is
	// about to run again. It is process-local on purpose: it exists between
	// a refusal and the relaunch it causes, and a run resumed after that
	// window is a run a person is steering, not one still mid-correction.
	rejections := make(map[string]contract.Subject, len(run.Steps))
	// seed rebuilds the four maps from the record. It runs again after a
	// graph grows, so an expansion's steps arrive the same way a resumed
	// run's do -- from disk, not from a second construction path that could
	// disagree with it.
	seed := func(from Run) {
		clear(status)
		clear(attempts)
		clear(traces)
		clear(answers)
		for _, step := range from.Steps {
			status[step.Step.ID] = step.Status
			attempts[step.Step.ID] = step.Attempt
			traces[step.Step.ID] = step.TraceID
			answers[step.Step.ID] = step
		}
	}
	seed(run)

	lanes := make(map[config.Pool]int)
	running := make(map[string]bool)
	results := make(chan done)
	var wg sync.WaitGroup

	// Canceling this cuts the agents without cutting the writes that record
	// what happened to them. A store call on the caller's context would fail
	// exactly when the record matters most.
	runCtx, cancel := context.WithCancel(ctx)
	// Every exit from this function unwinds the same way, including the ones
	// that used not to unwind at all. See unwind: `results` is unbuffered, so
	// leaving without draining it strands every goroutine whose step has
	// already finished, and waiting without draining it deadlocks outright.
	defer unwind(cancel, &wg, results)
	write := context.WithoutCancel(ctx)

	accountingFailure := func(cause error, first ...done) (Run, error) {
		cancel()
		completed := append([]done(nil), first...)
		landed := make(chan struct{})
		go func() { wg.Wait(); close(landed) }()
	collect:
		for {
			select {
			case item := <-results:
				completed = append(completed, item)
			case <-landed:
				break collect
			}
		}
		if err := e.store.reconcileAccountingFailure(write, id, completed, e.now()); err != nil {
			return run, errors.Join(cause, err)
		}
		out, err := e.store.Load(write, id)
		return out, errors.Join(cause, err)
	}

	aborted := false
	// A refused launch is refused for good, and not only in the process that
	// heard the refusal. Read off gate 0 rather than off the wait below: the
	// answer may have arrived while nothing was running, and an execute that
	// only consulted OPEN gates would find none and dispatch the graph
	// somebody had just turned down.
	//
	// The absence of gate 0 is now a refusal too, and it used to be silence.
	// `err == nil &&` meant a run with no launch gate at all fell through to
	// the loop and dispatched -- and such runs existed: Create wrote the run
	// before Ask could refuse it, so a proposal Ask turned down left a
	// workflow row with every step pending and nothing blessing the money.
	// `list` showed it and `resume` ran it. Ask can no longer be reached in
	// that state, and this is the second lock on the same door: the launch
	// gate IS the authorization this loop runs under, so running without one
	// cannot be a thing the code is able to do.
	rejected := false
	launch, err := e.store.Gate(write, id, 0)
	switch {
	case err != nil:
		return run, contract.Fail(contract.FailureInvalidInput,
			"workflow %s has no launch gate: nothing authorized it, so there is nothing to run", id)
	case launch.Kind != KindLaunch:
		return run, contract.Fail(contract.FailureInvalidInput,
			"workflow %s: gate 0 is a %s, not a launch: the record does not say who authorized this run",
			id, launch.Kind)
	case launch.Decision == DecisionRejected:
		rejected = true
	}
	for {
		if ctx.Err() != nil {
			aborted = true
		}
		// The freeze. While a gate is open nothing new is dispatched: what
		// is already spawned runs to completion and is not replaced. This is
		// what makes the scope rule hold rather than merely state it -- a
		// proposal may only touch steps that have not started, and with
		// dispatch stopped no step it names can start while somebody reads
		// it. Staleness stops being a race to detect.
		frozen := false
		if !aborted {
			gate, waiting, err := e.store.PendingGate(write, id)
			if err != nil {
				return run, err
			}
			switch {
			case !waiting:
			case len(running) > 0:
				// Let the running steps land first. Asking now would be
				// asking about a graph that is still moving.
				frozen = true
			default:
				answered, err := e.await(ctx, id, gate.Ordinal)
				if err != nil {
					aborted = true
					break
				}
				if answered.Decision == DecisionRejected {
					// A refused launch never ran. A refused expansion
					// leaves the run with the graph it already has, which
					// is the graph somebody did approve, so the loop goes
					// on and finishes it.
					if answered.Kind == KindLaunch {
						rejected = true
					}
					continue
				}
				if answered.Kind == KindLaunch {
					continue
				}
				if answered.applied == nil {
					applied, err := e.reconcileLegacyApproval(write, run, answered)
					if err != nil {
						return run, err
					}
					if applied {
						continue
					}
				}
				grown, err := e.grow(run, answered.Proposal)
				if err != nil {
					return run, err
				}
				if err := e.store.Apply(write, id, answered, grown); err != nil {
					return run, err
				}
				if run, err = e.store.Load(write, id); err != nil {
					return run, err
				}
				plan = grown
				seed(run)
				continue
			}
		}
		if rejected {
			break
		}
		if !aborted && !frozen {
			for _, step := range plan.Graph.Steps {
				if status[step.ID] != StatusPending || running[step.ID] {
					continue
				}
				if !ready(step, status) {
					continue
				}
				pool := plan.Pool(step.ID)
				if ceiling := e.lanes.Cap(pool); ceiling > 0 && lanes[pool] >= ceiling {
					// Ready, but the lane is full. It stays pending and is
					// looked at again the moment a slot frees: the queue is
					// this set plus the graph order, not a second list that
					// could disagree with it.
					continue
				}
				attempts[step.ID]++
				// The id is minted here, before anything spawns, so the
				// record of a step that is about to run points at the trace
				// of the run it is about to be. Written down after the
				// spawn, a crash in between would leave a step that looks
				// like it never started beside an agent that certainly did.
				traceID := e.runner.NextID()
				dispatch := agent.Dispatch{
					Effects:  append([]contract.Effect{}, step.Permission.Effects...),
					ID:       traceID,
					TypeName: step.TypeName,
					Task:     step.Task,
					Route:    step.Route,
					Attempt:  attempts[step.ID],
					RetryOf:  redoOf(traces[step.ID], attempts[step.ID]),
					// The share the plan cut for this step, which Compile
					// already checked against the grant. An agent that spends
					// has to be told its ceiling, or the only thing bounding
					// it is the provider's patience.
					BudgetUSD: &step.Permission.BudgetUSD,
					// What the whole run was granted, which is a different
					// fact from the line above and the one an agent writing
					// a graph has to divide. Measured 2026-08-14: without
					// it the shipped planner divided its own share under
					// the name "the grant for the whole graph", and eleven
					// runs allocated the same $0.90 whatever the
					// commission said.
					CommissionUSD: &plan.Graph.GrantUSD,
				}
				if step.Subject != "" {
					subject, err := subjectFrom(answers[step.Subject])
					if err != nil {
						return run, err
					}
					dispatch.Subject = &subject
					// The same link `atenea agent --review` writes, so the
					// chain walks from either end: recording the
					// relationship in the workflow tables only would make
					// the graph's reviews invisible to every reader of the
					// traces.
					//
					// Only for a step that actually audits. A planner reads
					// its input; recording that as a review would put an
					// audit on the record nobody performed, and the
					// exploration would read as judged by the graph built
					// out of it.
					if plan.Pool(step.ID) == config.PoolReview {
						dispatch.Reviews = subject.RunID
					}
				}
				if card, ok := rejections[step.ID]; ok {
					// Its own refused answer, beside the input it keeps. The
					// same card `atenea agent --review` writes, built by the
					// same constructor so the two callers cannot drift.
					dispatch.Rejected = &card
				}
				if err := e.store.Claim(write, id, step.ID, traceID,
					attempts[step.ID], e.now(), e.pid, step.Permission.BudgetUSD); err != nil {
					return accountingFailure(err)
				}
				traces[step.ID] = traceID
				lanes[pool]++
				running[step.ID] = true
				status[step.ID] = StatusRunning
				wg.Add(1)
				go func(stepID string, d agent.Dispatch) {
					defer wg.Done()
					report, _, runErr := e.runner.Dispatch(runCtx, d)
					results <- done{
						stepID: stepID,
						status: outcome(report, runErr),
						report: reportOf(report, runErr),
					}
				}(step.ID, dispatch)
			}
		}
		if len(running) == 0 {
			break
		}

		finished := <-results
		delete(running, finished.stepID)
		lanes[plan.Pool(finished.stepID)]--

		// A step cut mid-flight was never judged. Which steps those are is
		// read off their own death -- a canceled run comes back with
		// `canceled` on it -- and not off a flag sampled at the top of the
		// loop: the cancel usually lands while this loop is blocked here,
		// which is exactly when that flag is still false.
		if finished.status == StatusInterrupted {
			if err := e.store.Finish(write, id, finished.stepID, StatusInterrupted, finished.report, e.now()); err != nil {
				return accountingFailure(err, finished)
			}
			aborted = true
			if err := e.store.Interrupt(write, id, finished.stepID, "cut by abort", e.now()); err != nil {
				return run, err
			}
			status[finished.stepID] = StatusInterrupted
			continue
		}
		if err := e.store.Finish(write, id, finished.stepID, finished.status,
			finished.report, e.now()); err != nil {
			return accountingFailure(err, finished)
		}
		status[finished.stepID] = finished.status
		// Kept for whoever reads this answer next. The same fields the store
		// just wrote, so a subject built here and one built after a resume
		// are the same card.
		step, _ := plan.Step(finished.stepID)
		answers[finished.stepID] = StepRow{
			Step:       step,
			Status:     finished.status,
			TraceID:    traces[finished.stepID],
			Attempt:    attempts[finished.stepID],
			Verdict:    finished.report.Verdict,
			Reason:     finished.report.Reason,
			Result:     finished.report.Result,
			Discovered: finished.report.Discovered,
			Spent:      finished.report.Spent,
		}

		// A review that judged and said no sends the work back, once. Same
		// rule as `atenea agent --review`: the second attempt is handed the
		// sentence that refused the first, and a third is not offered --
		// an agent told the same thing twice writes the same answer twice.
		//
		// Only a `failed` review relaunches. A review that came back
		// `incomplete` did not judge -- it timed out, it could not read the
		// file, the service was down -- and re-running the work because its
		// auditor broke spends money on somebody else's outage.
		if redo, ok := e.refused(plan, finished, attempts, answers); ok {
			rejections[redo] = agent.RejectedCard(
				mustSubject(answers[redo]), traces[finished.stepID], finished.report.Reason)
			// Written down, not merely remembered.
			//
			// These two statuses used to exist only in this process's map. An
			// Atenea that died between a review's refusal and the re-dispatch
			// it causes left a run where nothing was pending and nothing was
			// interrupted -- so Run.Done() said true, End wrote StopNone, and
			// the record claimed a finished run whose correction never
			// happened. Reset also files the refused attempt, which is where
			// its money belongs: the run paid for both halves, and the
			// archive is the one place that says so. A second in-memory tally
			// of the same charge -- which is what this used to keep -- made
			// the live row carry it as well, and the balance counted it twice.
			for _, stepID := range [...]string{redo, finished.stepID} {
				if err := e.store.Reset(write, id, stepID, e.now()); err != nil {
					return run, err
				}
				status[stepID] = StatusPending
				delete(answers, stepID)
			}
		}
	}
	// The loop only breaks with nothing running, so this waits on nobody
	// today. It goes through unwind anyway: a future break that left a step
	// in flight would otherwise write the run's ending while it was still
	// happening, and the version of this that was a bare wg.Wait() would have
	// hung there instead of saying so.
	unwind(cancel, &wg, results)

	stop := StopNone
	switch {
	case rejected:
		stop = StopRejected
	case aborted:
		// A gate left open by an abort stays open. The question was never
		// answered, and closing it on the way out would answer it.
		stop = StopAborted
	case anyInterrupted(status):
		// Nothing is running, nothing is runnable, and what is left was
		// never judged. Saying "finished" here would be a receipt claiming
		// work that no report was ever read for.
		stop = StopUnjudged
	}
	if err := e.store.End(write, id, stop, e.now()); err != nil {
		return run, err
	}
	out, err := e.store.Load(write, id)
	if err != nil {
		return run, err
	}
	if aborted {
		return out, contract.Fail(contract.FailureCanceled,
			"workflow %s was cut: %s", id, out.Summary())
	}
	if rejected {
		return out, contract.Fail(contract.FailureInvalidInput,
			"workflow %s was not launched: the plan was rejected", id)
	}
	return out, nil
}

// redoOf links a re-dispatch to the run it repeats. A first attempt redoes
// nothing, and saying otherwise would put a retry chain on the record where
// there was none.
func redoOf(previous string, attempt int) string {
	if attempt <= 1 {
		return ""
	}
	return previous
}

// refused reports whether a finished step was a review that judged its
// subject and said no, and names the step to run again.
//
// Three things have to hold, and each is a bug someone would otherwise pay
// for: the finished step must actually be a review of something (a step with
// a subject edge), its verdict must be `failed` rather than any of the ways
// a review can fail to happen, and the work must not already have had its
// second attempt.
func (e *Engine) refused(plan Plan, finished done, attempts map[string]int,
	answers map[string]StepRow) (string, bool) {
	step, ok := plan.Step(finished.stepID)
	if !ok || step.Subject == "" {
		return "", false
	}
	if finished.status != StatusFailed || finished.report.Verdict != contract.VerdictFailed {
		return "", false
	}
	if strings.TrimSpace(finished.report.Reason.Text) == "" {
		// A refusal with no sentence cannot be answered, and handing it back
		// would be the "try again" this design refuses to send.
		return "", false
	}
	if attempts[step.Subject] >= agent.MaxAttempts {
		return "", false
	}
	if _, ok := answers[step.Subject]; !ok {
		return "", false
	}
	return step.Subject, true
}

// mustSubject packs a finished step for the relaunch card. The row came off
// this engine's own answers map, so it is a report that already validated;
// an error here would be a bug in this file rather than bad input, and the
// zero card is refused by the assignment's own Validate before it can spawn.
func mustSubject(row StepRow) contract.Subject {
	subject, err := subjectFrom(row)
	if err != nil {
		return contract.Subject{}
	}
	return subject
}

// outcome sorts what came back into a status.
//
// A death is incomplete, the same as an agent saying it stopped short: both
// are runs that reached no judgement, and the trace row carries which kind it
// was. A CUT is neither -- it is the one death nobody can read as being about
// the agent, so it gets its own state and keeps `incomplete` meaning what it
// meant before.
func outcome(report contract.Report, err error) Status {
	if err != nil {
		if contract.KindOf(err) == contract.FailureCanceled {
			return StatusInterrupted
		}
		return StatusIncomplete
	}
	switch report.Verdict {
	case contract.VerdictOK:
		// A partial answer (Completeness < 1) is still VerdictOK -- see
		// contract.Report.Validate, which refuses anything else for a
		// partial -- so it lands here with a full ok, not in the
		// VerdictIncomplete branch below. That is deliberate: Requirement's
		// satisfiedBy and downstream gates key on StatusOK (both
		// OnAnswered and OnOK clear on it), and a partial answer IS an
		// answer. Measured 2026-08-14: twelve of twelve steps that hit
		// their spending ceiling came back with an empty result and
		// VerdictIncomplete, and six reviewers keyed on OnOK never ran
		// against subjects that had, in fact, said something. Splitting
		// the ceiling into a read allowance and a reserved answer pass
		// exists so a step that ran out of read budget still reports
		// VerdictOK with less than 1 -- and that only pays off if a
		// partial answer clears the same gate a whole one does.
		return StatusOK
	case contract.VerdictFailed:
		return StatusFailed
	default:
		return StatusIncomplete
	}
}

// reportOf keeps the answer even when the dispatch failed: the runner returns
// a meaningful report on a death, and a record that dropped it would lose the
// reason the step ended.
func reportOf(report contract.Report, err error) contract.Report {
	if err == nil {
		return report
	}
	if report.Verdict == contract.VerdictUnspecified {
		report.Verdict = contract.VerdictIncomplete
	}
	if report.Reason.Empty() {
		report.Reason = contract.Reason{
			Kind: contract.KindOf(err),
			Text: contract.MessageOf(err),
		}
	}
	return report
}

// ready reports whether everything this step waits on has cleared the bar it
// set.
//
// An ordering edge demands OK, and merely finished will not do: a step whose
// input failed has nothing to work from, and running it anyway would produce
// an answer about a state that never existed. A subject edge demands what the
// step declared -- by default an answer of any verdict, because a failure is
// an answer and auditing it is the point. Neither is cleared by a step nobody
// judged.
func ready(step Step, status map[string]Status) bool {
	for _, edge := range step.Edges() {
		if !edge.On.satisfiedBy(status[edge.ID]) {
			return false
		}
	}
	return true
}

// anyInterrupted reports whether the record still holds a step nobody judged.
func anyInterrupted(status map[string]Status) bool {
	for _, s := range status {
		if s == StatusInterrupted {
			return true
		}
	}
	return false
}

// stepRow finds a persisted step by identifier.
func stepRow(run Run, id string) (StepRow, bool) {
	for _, step := range run.Steps {
		if step.Step.ID == id {
			return step, true
		}
	}
	return StepRow{}, false
}

// sortedKeys returns deterministic key ordering.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// defaultIDs mints workflow ids that sort by time and do not collide within a
// process.
func defaultIDs() func() string {
	var (
		mu      sync.Mutex
		counter int
	)
	return func() string {
		mu.Lock()
		counter++
		n := counter
		mu.Unlock()
		return "wf" + strings.ToLower(itoa(int(time.Now().UTC().UnixMilli()))) + "-" + itoa(n)
	}
}
