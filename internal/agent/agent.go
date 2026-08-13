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
	"strings"
	"time"

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
		verdict contract.Verdict, reason contract.Reason) error
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
	TypeName string
	Task     contract.Task
	// Parent is the assignment handing this work down, nil for a root.
	Parent *contract.Assignment
	// Subject is the run being judged, or the rejected attempt being handed
	// back for a second try.
	Subject *contract.Subject
	// Attempt counts this run within its retry chain, from 1.
	Attempt int
	// RetryOf is the run this one redoes, empty on a first attempt.
	RetryOf string
	// Reviews is the run this one audits, empty when it is not a review.
	Reviews string
}

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
	assignment, err := r.assign(declared, d.Task, d.Parent)
	if err != nil {
		return contract.Report{}, contract.Assignment{}, err
	}
	if d.Subject != nil {
		subject := d.Subject.Clone()
		assignment.Subject = &subject
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
	if runErr != nil {
		death := died(runErr)
		if err := r.store.Complete(ctx, assignment.ID, r.now(),
			contract.VerdictIncomplete, death); err != nil {
			return contract.Report{}, assignment, err
		}
		return contract.Report{Verdict: contract.VerdictIncomplete, Reason: death},
			assignment, contract.Fail(death.Kind, "agent %s (%s): %s",
				assignment.ID, d.TypeName, death.Text)
	}
	if err := r.store.Complete(ctx, assignment.ID, r.now(),
		report.Verdict, report.Reason); err != nil {
		return report, assignment, err
	}
	return report, assignment, nil
}

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
	parent *contract.Assignment) (contract.Assignment, error) {
	id := r.ids()
	if parent == nil {
		out := contract.RootAssignment(id, declared.Spec.Name, declared.Spec.Kind,
			task, declared.Limits)
		out.Context = declared.Context
		out.Effects = declared.Effects
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
			served[level.String()] = map[string]any{
				"repositories": r.workspace.Repositories,
			}
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

func (r *Runner) pastRuns(ctx context.Context, typeName string) ([]map[string]any, error) {
	if r.history == nil {
		return []map[string]any{}, nil
	}
	rows, err := r.history(ctx, typeName, HistoryDepth)
	if err != nil {
		return nil, err
	}
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

func note(stderr string) string {
	if stderr == "" {
		return ""
	}
	return " (stderr: " + firstLine(stderr) + ")"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
