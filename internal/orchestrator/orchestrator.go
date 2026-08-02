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
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
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
	SetHealth(implementationID string, health contract.Health) error
}

// Chooser is the funnel. The orchestrator asks for a capability on a
// repository and is told which implementation answers it, never the reverse.
type Chooser interface {
	Select(req selector.Request) (selector.Decision, error)
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

// searchCapability is what a commission is planned around: a text search is
// how the orchestrator explores a repository and how it splits the work.
const searchCapability = "code.search"

// The symbol capabilities. The orchestrator does not plan a commission around
// them -- it plans around a text search, which is brick 4's shape and stays --
// but it declares them because declaring is what makes a step askable at all.
// A capability the card does not name cannot be dispatched even when the
// catalog, the funnel and a runner are all ready for it.
const (
	definitionCapability      = "symbol.definition"
	referencesCapability      = "symbol.references"
	implementationsCapability = "symbol.implementations"
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
	// MaxParallel caps how many steps of one wave run at a time. Zero means no
	// ceiling. The ceiling belongs in the settings because the real limit is
	// the machine, and the machine is not the same one everywhere.
	MaxParallel int
}

// Agent is the orchestrator. It is safe for concurrent use: two chats can be
// running commissions at the same time against the same catalog.
type Agent struct {
	card        contract.Agent
	catalog     Catalog
	chooser     Chooser
	runner      contract.Runner
	checkpoints *checkpoint.Store
	maxParallel int
}

// card is what the orchestrator declares about itself. Declaring a context
// level is a permission, not a delivery: nothing here is loaded until the
// agent actually reaches for it.
var card = contract.Agent{
	ID:      "orchestrator",
	Type:    contract.AgentOrchestrator,
	Summary: "Explores the repositories in scope, splits the commission into a graph of steps and hands them out.",
	Capabilities: []string{
		searchCapability,
		definitionCapability,
		referencesCapability,
		implementationsCapability,
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
	store := cfg.Checkpoints
	if store == nil {
		disabled, err := checkpoint.New("")
		if err != nil {
			return nil, err
		}
		store = disabled
	}
	return &Agent{
		card:        card.Clone(),
		catalog:     cfg.Catalog,
		chooser:     cfg.Chooser,
		runner:      cfg.Runner,
		checkpoints: store,
		maxParallel: cfg.MaxParallel,
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
	// Session is the chat that commissioned this, when there is one. It buys
	// the run nothing: it is written to the receipt so a shared history stays
	// attributable to the isolated chat that produced it.
	Session string
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
	Matches     int
}

// Phase is one measured stretch of the run.
type Phase struct {
	Name  string
	Steps int
	Spent contract.Sample
}

// StepResult is one node of the plan after it closed.
type StepResult struct {
	Step     contract.Step
	Phase    string
	Decision selector.Decision
	Outcome  contract.Outcome
	Review   Review
	Failure  string
	Spent    contract.Sample
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
	if a.runner == nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"no runner is attached, so nothing can be dispatched")
	}
	repositories, repoErr := a.resolveRepositories(task.Repositories)
	if repoErr != nil {
		return nil, repoErr
	}
	permission := contract.Permission{
		Task: task.Text,
		// Reading is free by default. Anything heavier has to be granted by the
		// commission itself, which is what the caller passes in.
		Effects: append([]contract.Effect{contract.EffectRead}, task.Effects...),
	}

	started := time.Now()
	result = &Result{RunID: checkpoint.NewID(started), Task: task.Text}
	record := checkpoint.Run{
		ID: result.RunID, Session: task.Session, Task: task.Text, Started: started,
	}

	// The run closing is the second of the two moments the paper copy is
	// written, and it has to happen whether the run finished or was cut short:
	// a commission interrupted halfway is exactly the one worth reading back.
	defer func() {
		result.Spent = totalSpent(result.Steps)
		result.Verdict = overallVerdict(result.Steps)
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

	explored, err := a.dispatch(ctx, plan, PhaseExplore, result, &record)
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

	_, err = a.dispatch(ctx, plan, PhaseWork, result, &record)
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
	// Payload is the capability's declared input. It is checked against the
	// schema by the runner, not here: one gate, at the door it belongs to.
	Payload map[string]any
	// Effects the caller authorized beyond reading.
	Effects []contract.Effect
	// Session is the chat that asked, written to the receipt.
	Session string
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
	started := time.Now()
	result = &Result{RunID: checkpoint.NewID(started), Task: text}
	record := checkpoint.Run{
		ID: result.RunID, Session: q.Session, Task: text, Started: started,
	}
	defer func() {
		result.Spent = totalSpent(result.Steps)
		result.Verdict = overallVerdict(result.Steps)
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
		Permission: contract.Permission{
			Task:    text,
			Effects: append([]contract.Effect{contract.EffectRead}, q.Effects...),
		},
	}}}
	if err := plan.Validate(); err != nil {
		return result, err
	}
	result.Plan = plan

	_, err = a.dispatch(ctx, plan, PhaseAsk, result, &record)
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
// dangling.
func (a *Agent) dispatch(ctx context.Context, plan contract.Plan, phase string, result *Result, record *checkpoint.Run) ([]StepResult, error) {
	finished := make([]string, 0, len(result.Steps))
	failed := make(map[string]struct{})
	for _, step := range result.Steps {
		finished = append(finished, step.Step.ID)
		if step.Review.Parent != contract.VerdictOK {
			failed[step.Step.ID] = struct{}{}
		}
	}
	waves, err := plan.LayersAfter(finished)
	if err != nil {
		return nil, err
	}

	phaseSpent := contract.Sample{}
	closed := make([]StepResult, 0, len(plan.Steps)-len(finished))
	for _, wave := range waves {
		if err := ctx.Err(); err != nil {
			return closed, contract.Fail(contract.FailureTimeout, "run %s stopped: %v", result.RunID, err)
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
		done = append(done, a.runWave(ctx, runnable)...)
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
			record.Steps = append(record.Steps, snapshot(step))
			record.Updated = time.Now()
			// A step closing is one of the two moments the paper copy is
			// written. The other is the run itself closing.
			if err := a.checkpoints.Save(*record); err != nil {
				return closed, err
			}
		}
	}
	result.Phases = append(result.Phases, Phase{Name: phase, Steps: len(closed), Spent: phaseSpent})
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
func (a *Agent) runWave(ctx context.Context, wave []contract.Step) []StepResult {
	out := make([]StepResult, len(wave))
	limit := a.maxParallel
	if limit <= 0 || limit > len(wave) {
		limit = len(wave)
	}
	slots := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, step := range wave {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			out[i] = a.runStep(ctx, step)
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
	decision, err := a.chooser.Select(selector.Request{
		Capability: step.Capability,
		Repository: repository,
		Candidates: candidates,
		Reachable:  a.runner.Implementations(),
	})
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
	started := time.Now()
	outcome, runErr := a.runner.Run(ctx, request)
	out.Outcome = outcome
	out.Spent = outcome.Spent
	if out.Spent.Duration == 0 {
		out.Spent.Duration = time.Since(started)
	}
	if runErr != nil {
		// A provider reporting itself unusable is news the catalog needs: the
		// funnel filters on health, and health is owned by whoever probed last.
		// Running a step is a probe.
		if contract.KindOf(runErr) == contract.FailureUnavailable {
			_ = a.catalog.SetHealth(decision.Chosen.ID, contract.Health{
				State:      contract.HealthDown,
				Reason:     runErr.Error(),
				ObservedAt: time.Now(),
			})
		}
		return a.close(out, runErr)
	}
	return a.close(out, nil)
}

// close is the parent's review. It runs for every child, always.
func (a *Agent) close(out StepResult, err error) StepResult {
	child := out.Outcome.Verdict
	if err != nil {
		out.Failure = err.Error()
		child = contract.VerdictFailed
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

// overallVerdict is the parent's word for the whole commission: one failed
// step is a failed commission, because half-done work presented as done is
// the thing reviewing exists to prevent.
func overallVerdict(steps []StepResult) contract.Verdict {
	if len(steps) == 0 {
		return contract.VerdictFailed
	}
	for _, step := range steps {
		if step.Review.Parent != contract.VerdictOK {
			return contract.VerdictFailed
		}
	}
	return contract.VerdictOK
}

func snapshot(step StepResult) checkpoint.StepState {
	return checkpoint.StepState{
		ID:             step.Step.ID,
		Capability:     step.Step.Capability,
		Repository:     step.Step.Repository,
		Implementation: step.Decision.Chosen.ID,
		Verdict:        step.Review.Child.String(),
		Review:         step.Review.Parent.String(),
		Failure:        step.Failure,
		DurationMS:     step.Spent.Duration.Milliseconds(),
		ClosedAt:       time.Now(),
	}
}
