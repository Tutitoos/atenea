package workflow

import (
	"context"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
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
}

// Engine runs graphs.
type Engine struct {
	runner  Dispatcher
	store   *Store
	types   []config.AgentType
	lanes   config.Workflow
	now     func() time.Time
	ids     func() string
	pid     int
	alive   func(pid int) bool
	poll    time.Duration
	surface string
	repo    string
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
		runner:  opts.Runner,
		store:   opts.Store,
		types:   opts.Types,
		lanes:   opts.Lanes,
		now:     opts.Now,
		ids:     opts.IDs,
		pid:     opts.PID,
		alive:   opts.Alive,
		poll:    opts.Poll,
		repo:    opts.Repository,
		surface: opts.Surface,
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
// The returned Gate carries the digest the launch will be checked against.
func (e *Engine) Create(ctx context.Context, graph Graph) (Run, Gate, error) {
	plan, err := Compile(graph, e.types)
	if err != nil {
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
		return Run{}, Gate{}, err
	}
	run, err := e.store.Load(ctx, id)
	if err != nil {
		return Run{}, Gate{}, err
	}
	return run, gate, nil
}

// Launch answers a run's launch gate and drives it.
//
// The two halves are one call because they are one act: the person saying yes
// is the person committing the grant, and a launch recorded by somebody who
// then did not run it would leave an approval with nothing behind it.
func (e *Engine) Launch(ctx context.Context, id string) (Run, error) {
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
	if err := e.store.Own(ctx, id, e.pid); err != nil {
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

// takeOver refuses a run that is finished or that another live Atenea holds.
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
	return run, nil
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
		if err := e.store.Reset(ctx, id, step.Step.ID); err != nil {
			return run, err
		}
	}
	if err := e.store.Own(ctx, id, e.pid); err != nil {
		return run, err
	}
	return e.execute(ctx, id, plan)
}

// touchesTheWorld reports whether repeating this step could land an effect
// twice. Process is not one of them: every agent spawns a process to answer at
// all, and a second spawn of a read-only agent changes nothing outside itself.
func touchesTheWorld(effects []contract.Effect) bool {
	return slices.Contains(effects, contract.EffectWrite) ||
		slices.Contains(effects, contract.EffectExternal)
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
	// carried is what an abandoned attempt already cost. A step's own row
	// holds one attempt -- Claim clears it on every re-claim, so a redo does
	// not inherit a charge it did not incur -- but the run paid for both,
	// and a receipt that drops the refused half understates the bill by
	// exactly the amount the correction cost.
	carried := make(map[string]contract.Charge, len(run.Steps))
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
	defer cancel()
	write := context.WithoutCancel(ctx)

	aborted := false
	// A refused launch is refused for good, and not only in the process that
	// heard the refusal. Read off gate 0 rather than off the wait below: the
	// answer may have arrived while nothing was running, and an execute that
	// only consulted OPEN gates would find none and dispatch the graph
	// somebody had just turned down.
	rejected := false
	if launch, err := e.store.Gate(write, id, 0); err == nil &&
		launch.Kind == KindLaunch && launch.Decision == DecisionRejected {
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
			gate, waiting, err := e.store.OpenGate(write, id)
			if err != nil {
				cancel()
				wg.Wait()
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
					ID:       traceID,
					TypeName: step.TypeName,
					Task:     step.Task,
					Attempt:  attempts[step.ID],
					RetryOf:  redoOf(traces[step.ID], attempts[step.ID]),
					// The share the plan cut for this step, which Compile
					// already checked against the grant. An agent that spends
					// has to be told its ceiling, or the only thing bounding
					// it is the provider's patience.
					BudgetUSD: &step.Permission.BudgetUSD,
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
					attempts[step.ID], e.now(), e.pid); err != nil {
					cancel()
					wg.Wait()
					return run, err
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
			aborted = true
			if err := e.store.Interrupt(write, id, finished.stepID, "cut by abort", e.now()); err != nil {
				return run, err
			}
			status[finished.stepID] = StatusInterrupted
			continue
		}
		if before, ok := carried[finished.stepID]; ok {
			finished.report.Spent = finished.report.Spent.Plus(before)
		}
		if err := e.store.Finish(write, id, finished.stepID, finished.status,
			finished.report, e.now()); err != nil {
			return run, err
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
			carried[redo] = answers[redo].Spent.Plus(carried[redo])
			status[redo] = StatusPending
			status[finished.stepID] = StatusPending
			delete(answers, redo)
			delete(answers, finished.stepID)
		}
	}
	wg.Wait()

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

func stepRow(run Run, id string) (StepRow, bool) {
	for _, step := range run.Steps {
		if step.Step.ID == id {
			return step, true
		}
	}
	return StepRow{}, false
}

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
