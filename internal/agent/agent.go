// Package agent runs one declared agent as one real process, and writes down
// what happened.
//
// # Finished is not the same question as exited
//
// The whole design of this package turns on one distinction. A process that
// exits has told you nothing: it may have answered, it may have died mid-
// sentence, it may have exited zero without writing a word. So the signal
// Atenea reads is the ANSWER, not the exit status:
//
//   - a parseable report, valid against the type's declared shape -> finished,
//     and the verdict on it is the agent's own to give.
//   - anything else -- no report, half a report, a report in the wrong shape,
//     a non-zero exit, a signal, a deadline, a cancel -> died.
//
// A death is recorded as INCOMPLETE, never as failed. The agent may have done
// most of the work and died on the way to saying so; calling that failure
// invents evidence about work nobody watched. Which kind of death it was
// lands in the reason bin -- timeout, canceled, unavailable -- so the
// distinction survives without pretending to a judgement.
//
// # The trace is written here, not there
//
// The row is opened before the process starts and closed after the answer is
// validated. The agent never touches it. That is what makes a death
// detectable at all: it is the absence of the second write, which survives
// the agent being killed, the machine losing power, and Atenea itself dying.
// Nothing in this package has to notice a crash.
package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/procgroup"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// HistoryDepth is how many past runs of the same type are served to an agent
// that declared the history level. Enough to see a pattern, few enough that
// the context stays cheap.
const HistoryDepth = 5

// Clock is time, injectable so a test can assert on what was written.
type Clock func() time.Time

// Store is the half of the trace store this package uses. An interface
// because the test suite has no business opening a database to check that a
// row was opened before the process was.
type Store interface {
	Begin(ctx context.Context, row trace.Row) error
	Complete(ctx context.Context, id string, at time.Time,
		verdict contract.Verdict, reason contract.Reason, discovered []contract.Discovery) error
}

// Options configure the runner.
type Options struct {
	// Types are the declared agent types, resolved by name.
	Types []config.AgentType
	// Store receives the two writes. Required: an agent run that leaves no
	// trace is the one shape this package exists to prevent.
	Store Store
	// Now is the clock. Defaults to time.Now.
	Now Clock
	// IDs mints execution ids. Defaults to a timestamp plus a counter, which
	// is unique per process and sorts.
	IDs func() string
	// Workspace is what the repository and workspace context levels describe.
	Workspace Workspace
	// Self is the path to this Atenea binary, substituted for the `$atenea`
	// placeholder a declaration may use as its command. Defaults to
	// os.Executable().
	//
	// The placeholder exists because the shipped agent is this binary run
	// with a different first argument, and a settings file cannot know where
	// that binary will live. Hard-coding a path there survives exactly until
	// somebody reinstalls.
	Self string
	// History supplies the history level. Nil means an agent that declared
	// history is served an empty list rather than refused -- a machine with
	// no past is a fact, not an error.
	History func(ctx context.Context, typeName string, limit int) ([]trace.Row, error)
	// Costs supplies what agent types have cost on this machine, served at
	// the workspace level. Nil means the level carries no cost table at all,
	// which reads as "never measured" -- the one thing it must never do is
	// hand back zeros, because a type nobody has priced and a type that
	// costs nothing are different facts.
	Costs func(ctx context.Context) (CostTable, error)
}

// Workspace is what Atenea knows about where the work happens.
type Workspace struct {
	// Repository is the unit of work: its id and its absolute root.
	RepositoryID   string
	RepositoryRoot string
	// Repositories are every declared repository id, which is what the
	// workspace level means: the others exist, and this is their name.
	Repositories []string
	// AteneaVersion is served at the global level.
	AteneaVersion string
}

// Runner spawns declared agents.
type Runner struct {
	types     map[string]config.AgentType
	store     Store
	now       Clock
	ids       func() string
	workspace Workspace
	self      string
	history   func(ctx context.Context, typeName string, limit int) ([]trace.Row, error)
	costs     func(ctx context.Context) (CostTable, error)
}

// SelfPlaceholder is what a declaration writes instead of a path to Atenea.
const SelfPlaceholder = "$atenea"

// New builds a runner over the declared types.
func New(opts Options) (*Runner, error) {
	if opts.Store == nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"agent: a trace store is required")
	}
	types := make(map[string]config.AgentType, len(opts.Types))
	for _, t := range opts.Types {
		types[t.Spec.Name] = t
	}
	r := &Runner{
		types:     types,
		store:     opts.Store,
		now:       opts.Now,
		ids:       opts.IDs,
		workspace: opts.Workspace,
		self:      opts.Self,
		history:   opts.History,
		costs:     opts.Costs,
	}
	if r.self == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, contract.Fail(contract.FailureUnavailable,
				"agent: cannot find this binary, so $atenea cannot be resolved: %v", err)
		}
		r.self = exe
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.ids == nil {
		r.ids = defaultIDs()
	}
	return r, nil
}

// Declared lists the resolvable type names, sorted.
func (r *Runner) Declared() []string {
	out := make([]string, 0, len(r.types))
	for name := range r.types {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

// Dispatch is one run to make: which type, what work, and how this run
// relates to others. Everything past TypeName and Task is a relationship, and
// they are set by the caller that knows about it -- an agent never declares
// itself a retry or a review of anything.
type Dispatch struct {
	// Effects is the explicit grant; nil preserves the declared default for direct callers.
	Effects  []contract.Effect
	TypeName string
	Task     contract.Task
	// Route carries the decision-router's selected execution surface. A child
	// without an explicit route inherits its parent's route in Dispatch.
	Route *contract.Route
	// ID is the execution id to run as. Empty mints one, which is what
	// every caller that does not have to know it in advance does; a caller
	// that recorded the id before the spawn passes the one it wrote down.
	ID string
	// Parent is the assignment handing this work down, nil for a root.
	Parent *contract.Assignment
	// Subject is another run's answer handed to this one: the run a reviewer
	// judges, or the input an agent's work takes.
	Subject *contract.Subject
	// Rejected is this run's own previous attempt, refused by a review. It
	// travels beside Subject rather than replacing it, so an agent that had
	// an input keeps it on the second try.
	Rejected *contract.Subject
	// Attempt counts this run within its retry chain, from 1.
	Attempt int
	// RetryOf is the run this one redoes, empty on a first attempt.
	RetryOf string
	// Reviews is the run this one audits, empty when it is not a review.
	Reviews string
	// BudgetUSD is the share of a grant this run may draw against. Nil is
	// what every caller outside a workflow passes: nobody granted money,
	// which is not the same as granting none.
	BudgetUSD *float64
	// CommissionUSD is the grant of the run this dispatch belongs to, set by
	// the workflow engine and nil everywhere else. An agent that writes a
	// graph divides this; BudgetUSD above is its own share.
	CommissionUSD *float64
}

// NextID mints an execution id without dispatching anything.
//
// It exists for a caller that has to write down what it is about to start
// before starting it -- the workflow engine records a step as running, with
// the id of the run, so that a crash leaves a record pointing at the trace
// instead of a step that looks like it never began. Same reason the trace row
// itself is written before the spawn.
func (r *Runner) NextID() string { return r.ids() }

// Run dispatches one agent and returns what it answered.
//
// The returned Report is always meaningful, including when err is non-nil: a
// death produces an incomplete report carrying the reason, because a caller
// that gets only an error has lost the distinction this package is about.
func (r *Runner) Run(ctx context.Context, typeName string, task contract.Task,
	parent *contract.Assignment) (contract.Report, contract.Assignment, error) {
	return r.Dispatch(ctx, Dispatch{TypeName: typeName, Task: task, Parent: parent})
}

// Dispatch is Run with the relationships spelled out.
func (r *Runner) Dispatch(ctx context.Context, d Dispatch) (contract.Report, contract.Assignment, error) {
	declared, err := r.resolve(d.TypeName)
	if err != nil {
		return contract.Report{}, contract.Assignment{}, err
	}
	if d.Effects != nil {
		for _, effect := range d.Effects {
			if !slices.Contains(declared.Effects, effect) {
				return contract.Report{}, contract.Assignment{}, contract.Fail(contract.FailurePermissionDenied, "dispatch effect %s exceeds declared type", effect)
			}
		}
		declared.Effects = slices.Clone(d.Effects)
	}
	assignment, err := r.assign(declared, d.Task, d.Parent, d.ID, d.BudgetUSD)
	if err != nil {
		return contract.Report{}, contract.Assignment{}, err
	}
	if d.CommissionUSD != nil {
		commission := *d.CommissionUSD
		assignment.CommissionUSD = &commission
	}
	if d.Route != nil {
		route := d.Route.Clone()
		assignment.Route = &route
	} else if d.Parent != nil && d.Parent.Route != nil {
		route := d.Parent.Route.Clone()
		assignment.Route = &route
	}
	if d.Subject != nil {
		subject := d.Subject.Clone()
		assignment.Subject = &subject
	}
	if d.Rejected != nil {
		rejected := d.Rejected.Clone()
		assignment.Rejected = &rejected
	}
	if d.Subject != nil || d.Rejected != nil {
		if err := assignment.Validate(); err != nil {
			return contract.Report{}, assignment, err
		}
	}

	started := r.now()
	row := trace.Row{
		ID:        assignment.ID,
		ParentID:  assignment.ParentID,
		TypeName:  assignment.TypeName,
		Kind:      assignment.Kind,
		Objective: assignment.Task.Objective,
		Depth:     assignment.Depth,
		StartedAt: started,
		Attempt:   d.Attempt,
		RetryOf:   d.RetryOf,
		Reviews:   d.Reviews,
	}
	// Before the spawn, always. A row written afterwards would miss exactly
	// the runs worth tracing.
	if err := r.store.Begin(ctx, row); err != nil {
		return contract.Report{}, assignment, err
	}

	report, runErr := r.execute(ctx, declared, assignment)
	// The closing write does NOT ride the caller's context. Canceling a run
	// is the one case where the record matters most and the caller's context
	// is already dead: a Complete on it fails, the row stays open, and the
	// only thing that would ever close it is a sweep on some later start --
	// after a liveness check against a pid that is still very much alive,
	// which never passes. The agent is cut; the accounting of it is not.
	closing := context.WithoutCancel(ctx)
	if runErr != nil {
		death := died(runErr)
		// execute never returns a populated report alongside an error, so
		// report.Discovered is empty here: there is nothing to persist from
		// a run nobody watched finish.
		if err := r.store.Complete(closing, assignment.ID, r.now(),
			contract.VerdictIncomplete, death, report.Discovered); err != nil {
			return contract.Report{}, assignment, err
		}
		return contract.Report{Verdict: contract.VerdictIncomplete, Reason: death},
			assignment, contract.Fail(death.Kind, "agent %s (%s): %s",
				assignment.ID, d.TypeName, death.Text)
	}
	if err := r.store.Complete(closing, assignment.ID, r.now(),
		report.Verdict, report.Reason, report.Discovered); err != nil {
		return report, assignment, err
	}
	return report, assignment, nil
}

// resolve resolves the requested declared agent type.
func (r *Runner) resolve(name string) (config.AgentType, error) {
	declared, ok := r.types[name]
	if ok {
		return declared, nil
	}
	known := r.Declared()
	if len(known) == 0 {
		return config.AgentType{}, contract.Fail(contract.FailureNotFound,
			"no agent type %q: none is declared", name)
	}
	return config.AgentType{}, contract.Fail(contract.FailureNotFound,
		"no agent type %q: declared are %s", name, strings.Join(known, ", "))
}

// assign builds the card. A child goes through Assignment.Child so the effect
// subset and the depth cap are enforced by the contract rather than here --
// this package must not be a second place those rules are written.
func (r *Runner) assign(declared config.AgentType, task contract.Task,
	parent *contract.Assignment, id string, budgetUSD *float64) (contract.Assignment, error) {
	if id == "" {
		id = r.ids()
	}
	if parent == nil {
		out := contract.RootAssignment(id, declared.Spec.Name, declared.Spec.Kind,
			task, declared.Limits)
		out.Context = declared.Context
		out.Effects = declared.Effects
		out.BudgetUSD = budgetUSD
		if err := out.Validate(); err != nil {
			return contract.Assignment{}, err
		}
		return out, nil
	}
	child, err := parent.Child(id, declared.Spec.Name, declared.Spec.Kind, task,
		declared.Effects, declared.Limits)
	if err != nil {
		return contract.Assignment{}, err
	}
	// The declared levels are this type's, narrowed by what the parent may
	// see: a child cannot be shown a level its parent was never entitled to.
	child.Context = intersectLevels(declared.Context, parent.Context)
	child.BudgetUSD = budgetUSD
	if err := child.Validate(); err != nil {
		return contract.Assignment{}, err
	}
	return child, nil
}

// execute spawns the process and reads its answer. Every error it returns is
// a death.
func (r *Runner) execute(ctx context.Context, declared config.AgentType,
	assignment contract.Assignment) (contract.Report, error) {
	served, err := r.serve(ctx, assignment)
	if err != nil {
		return contract.Report{}, err
	}
	schema, err := declared.Spec.ResultSchema()
	if err != nil {
		return contract.Report{}, err
	}
	payload, err := encodeAssignment(assignment, served, schema)
	if err != nil {
		return contract.Report{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, assignment.Limits.MaxDuration)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.binary(declared.Command), declared.Args...)
	if root := r.workspace.RepositoryRoot; root != "" {
		cmd.Dir = root
	}
	cmd.Env = append(os.Environ(), declared.Env...)
	cmd.Stdin = strings.NewReader(string(payload))
	// An agent spawns tools of its own. Without this, canceling leaves them
	// running and Wait blocks on pipes they still hold.
	procgroup.Contain(cmd)

	stdout, runErr := cmd.Output()
	var stderr string
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		stderr = strings.TrimSpace(string(exit.Stderr))
	}

	// The clock and the cancel are checked before the output, because a
	// truncated answer from a killed process can still parse, and reading it
	// as an answer would turn a death into a verdict.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return contract.Report{}, stopped(ctxErr, assignment.Limits.MaxDuration, stderr)
	}

	report, parseErr := decodeReport(stdout)
	if parseErr != nil {
		return contract.Report{}, deathf(contract.FailureUnavailable,
			"%s%s", contract.MessageOf(parseErr), note(stderr))
	}
	if runErr != nil {
		// A report AND a non-zero exit. The report is discarded: a process
		// that answered and then failed to exit cleanly has not established
		// which of the two to believe.
		return contract.Report{}, deathf(contract.FailureUnavailable,
			"answered and then exited badly: %v%s", runErr, note(stderr))
	}

	report = report.Normalize()
	if err := report.Validate(declared.Spec); err != nil {
		// A well-formed answer in the wrong shape is not a death of the
		// process -- but it is a death of the ANSWER, and the same rule
		// applies: nobody may read a result that was never checked.
		return contract.Report{}, deathf(contract.FailureInvalidInput,
			"the answer does not match the declared shape: %v", err)
	}
	return report, nil
}

// binary resolves the declared command, substituting this binary for the
// placeholder.
func (r *Runner) binary(command string) string {
	if command == SelfPlaceholder {
		return r.self
	}
	return command
}

// serve builds the context payload, one key per declared level and nothing
// else. A level the type did not declare is absent from the map, which is the
// difference between "not offered" and "offered and empty".
func (r *Runner) serve(ctx context.Context, assignment contract.Assignment) (map[string]any, error) {
	served := make(map[string]any, len(assignment.Context))
	for _, level := range assignment.Context {
		switch level {
		case contract.ContextRepository:
			served[level.String()] = map[string]any{
				"id":   r.workspace.RepositoryID,
				"root": r.workspace.RepositoryRoot,
			}
		case contract.ContextWorkspace:
			payload := map[string]any{"repositories": r.workspace.Repositories}
			costs, err := r.costTable(ctx)
			if err != nil {
				return nil, err
			}
			if costs != nil {
				payload["costs"] = costs
			}
			served[level.String()] = payload
		case contract.ContextGlobal:
			served[level.String()] = map[string]any{
				"atenea":   r.workspace.AteneaVersion,
				"contract": contract.Current.String(),
			}
		case contract.ContextHistory:
			past, err := r.pastRuns(ctx, assignment.TypeName)
			if err != nil {
				return nil, err
			}
			served[level.String()] = map[string]any{"runs": past}
		case contract.ContextUnspecified:
			return nil, contract.Fail(contract.FailureInvalidInput,
				"agent %s: empty context level", assignment.ID)
		}
	}
	return served, nil
}

// pastRuns serves up to HistoryDepth rows of this agent type's own trace,
// each carrying what it discovered.
func (r *Runner) pastRuns(ctx context.Context, typeName string) ([]map[string]any, error) {
	if r.history == nil {
		return []map[string]any{}, nil
	}
	rows, err := r.history(ctx, typeName, HistoryDepth)
	if err != nil {
		return nil, err
	}
	// One dedupe pool across every row, not one per row: two runs reporting
	// the same fact are not two facts, and showing it a second time teaches
	// a reader nothing the first copy did not already say.
	seen := make(map[string]struct{}, len(rows))
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		entry := map[string]any{
			"id":        row.ID,
			"objective": row.Objective,
			"verdict":   row.Verdict.String(),
		}
		if !row.EndedAt.IsZero() {
			entry["ended_at"] = row.EndedAt.UTC().Format(time.RFC3339)
		}
		if row.Reason.Kind != contract.FailureUnspecified {
			entry["reason"] = row.Reason.Kind.String()
		}
		// The gate is the discovering row's own verdict, never its
		// parent's: a step that ended ok inside a run that later failed
		// still discovered a fact somebody paid for. A row that answered
		// badly is withheld for the same reason a bad answer is withheld
		// anywhere else -- it is not a source the next run should build on
		// without checking it again.
		if row.Verdict == contract.VerdictOK {
			// "level: note" strings rather than the {level, note} objects
			// the wire uses elsewhere: the reader here is a model-backed
			// agent's prompt, not a second parser, and a line it can drop
			// straight into a prompt as prose is more useful to it than a
			// structure it would only flatten right back down.
			var discovered []string
			for _, d := range row.Discovered {
				if _, dup := seen[d.Note]; dup {
					continue
				}
				seen[d.Note] = struct{}{}
				discovered = append(discovered, d.Level.String()+": "+d.Note)
			}
			if len(discovered) > 0 {
				entry["discovered"] = discovered
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// stopped sorts a context error into the bin that says which death it was.
func stopped(err error, limit time.Duration, stderr string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return deathf(contract.FailureTimeout,
			"no answer within %v%s", limit, note(stderr))
	}
	return deathf(contract.FailureCanceled, "stopped before it answered%s", note(stderr))
}

// note folds what the child printed into the sentence that reports its death,
// which is the one place a person debugging a spawn ever sees it.
//
// One line, and a bounded one. A child's stderr is unbounded -- a tool that
// dies mid-write can emit megabytes without a newline in it -- and this
// sentence ends up in a Failure message, in a report reason, and from there
// in the agent_trace row died writes. Before the bound, a single stderr line
// could carry the whole of that into the message and into the store.
func note(stderr string) string {
	if stderr == "" {
		return ""
	}
	return " (stderr: " + clip(firstLine(stderr)) + ")"
}

// clip bounds one line of child output to what a sentence can carry.
//
// The cut lands on a character boundary rather than a byte index: a child's
// stderr is where non-ASCII actually turns up -- a path, a filename, a message
// in the operator's own language -- and slicing through a multi-byte rune puts
// a replacement character nobody sent into the record. It is the same care
// contract.RedactRaw takes at its own ceiling, for the same reason.
func clip(text string) string {
	const limit = 300
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "..."
}

// firstLine returns the first diagnostic line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// CostTable is what agent types have cost on this machine, as the workspace
// level carries it.
//
// It is a copy rather than a reference to the workflow package's own type:
// this package is below that one, and an agent being told what things cost
// must not drag the engine that measured them into every binary that spawns
// an agent.
type CostTable struct {
	// Repository the figures are scoped to. Empty means machine-wide, and a
	// reader must be told which it is holding -- exploring a six-file tree
	// and exploring this one are not the same act at the same price.
	Repository string
	// Types is keyed by agent type name. A type absent here has never been
	// measured, which every reader must pass on in those words.
	Types map[string]Cost
}

// Cost is one agent type's record.
type Cost struct {
	// MedianUSD, and the range it sits in.
	MedianUSD      float64
	MinUSD, MaxUSD float64
	// N is how many clean runs are behind the median. It travels with the
	// median everywhere, because three samples and thirty are different
	// claims and only one of them is worth planning against.
	N int
	// AtCeiling and Unmeasured are the rows deliberately left out: a run that
	// spent its whole grant is a lower bound rather than a measurement, and a
	// run nobody could price is not a cheap run. Counted out loud so the
	// exclusion is visible to whoever reads the median.
	AtCeiling  int
	Unmeasured int
}

// costTable serves the workspace level's cost figures, or nil when this
// machine has none. Nil and empty differ: nil is "no measurement reached
// here", and the reader prints that rather than inventing a zero.
func (r *Runner) costTable(ctx context.Context) (map[string]any, error) {
	if r.costs == nil {
		return nil, nil
	}
	table, err := r.costs(ctx)
	if err != nil {
		return nil, err
	}
	if len(table.Types) == 0 {
		return nil, nil
	}
	types := make(map[string]any, len(table.Types))
	for name, cost := range table.Types {
		types[name] = map[string]any{
			"median_usd": cost.MedianUSD,
			"min_usd":    cost.MinUSD,
			"max_usd":    cost.MaxUSD,
			"n":          cost.N,
			"at_ceiling": cost.AtCeiling,
			"unmeasured": cost.Unmeasured,
		}
	}
	out := map[string]any{
		"types": types,
		// Said here rather than left to the reader: agent_trace carries no
		// spend column, so a single `atenea agent` run is priced nowhere and
		// this table cannot see one.
		"covers": "workflow steps only",
	}
	if table.Repository != "" {
		out["repository"] = table.Repository
	}
	return out, nil
}
