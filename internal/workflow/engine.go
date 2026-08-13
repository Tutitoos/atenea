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
}

// Engine runs graphs.
type Engine struct {
	runner Dispatcher
	store  *Store
	types  []config.AgentType
	lanes  config.Workflow
	now    func() time.Time
	ids    func() string
	pid    int
	alive  func(pid int) bool
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
		runner: opts.Runner,
		store:  opts.Store,
		types:  opts.Types,
		lanes:  opts.Lanes,
		now:    opts.Now,
		ids:    opts.IDs,
		pid:    opts.PID,
		alive:  opts.Alive,
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
	return e, nil
}

// Start compiles a graph, writes it down, and runs it.
//
// The returned Run is the record as it stands when the loop ends, whatever
// happened -- including when err is non-nil. A caller that gets only an error
// has lost every step that did work before the one that did not.
func (e *Engine) Start(ctx context.Context, graph Graph) (Run, error) {
	plan, err := Compile(graph, e.types)
	if err != nil {
		return Run{}, err
	}
	id := e.ids()
	if err := e.store.Create(ctx, id, plan, e.now(), e.pid); err != nil {
		return Run{}, err
	}
	return e.execute(ctx, id, plan)
}

// Resume continues a run that was cut or whose Atenea died.
//
// redo names steps to dispatch again even though this would not otherwise
// touch them: the interrupted ones that may have written something. Naming a
// step that is not interrupted is refused rather than ignored, because
// silently doing nothing to a step somebody asked about reads as having
// redone it.
func (e *Engine) Resume(ctx context.Context, id string, redo []string) (Run, error) {
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
	// step still in flight.
	if run.WriterPID != 0 && run.WriterPID != e.pid && e.alive(run.WriterPID) {
		return run, contract.Fail(contract.FailureUnavailable,
			"workflow %s is running under pid %d", id, run.WriterPID)
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
	for _, step := range run.Steps {
		status[step.Step.ID] = step.Status
		attempts[step.Step.ID] = step.Attempt
		traces[step.Step.ID] = step.TraceID
	}

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
	for {
		if ctx.Err() != nil {
			aborted = true
		}
		if !aborted {
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
		if err := e.store.Finish(write, id, finished.stepID, finished.status,
			finished.report, e.now()); err != nil {
			return run, err
		}
		status[finished.stepID] = finished.status
	}
	wg.Wait()

	stop := StopNone
	switch {
	case aborted:
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

// ready reports whether every step this one waits on has finished OK.
//
// OK, not merely finished: a step whose input failed has nothing to work from,
// and running it anyway would produce an answer about a state that never
// existed. Its siblings are untouched -- that is the whole point of the graph
// being a graph.
func ready(step Step, status map[string]Status) bool {
	for _, need := range step.Needs {
		if status[need] != StatusOK {
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
