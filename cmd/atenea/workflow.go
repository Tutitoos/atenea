package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// cmdWorkflow runs a graph of agent steps, resumes one, or reads the record.
//
// The graph comes from a file. Nothing here builds one: there is no
// orchestrator yet, and a command that quietly invented a plan would be the
// first place a graph nobody wrote could come from.
func cmdWorkflow(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow needs a subcommand: create, launch, run, propose, approve, reject, resume, redo, list or show")
	}
	sub, rest := strings.TrimSpace(args[0]), args[1:]
	switch sub {
	case "create":
		return workflowCreate(settingsPath, rest, out)
	case "launch":
		return workflowLaunch(settingsPath, rest, out)
	case "run":
		return workflowRun(settingsPath, rest, out)
	case "propose":
		return workflowPropose(rest, out)
	case "approve":
		return workflowAnswer(rest, out, workflow.DecisionApproved)
	case "reject":
		return workflowAnswer(rest, out, workflow.DecisionRejected)
	case "resume":
		return workflowResume(settingsPath, rest, out)
	case "redo":
		return workflowRedo(settingsPath, rest, out)
	case "list":
		return workflowList(rest, out)
	case "show":
		return workflowShow(rest, out)
	default:
		return contract.Fail(contract.FailureInvalidInput,
			"unknown workflow subcommand %q: create, launch, run, propose, approve, reject, resume, redo, list or show", sub)
	}
}

func workflowCreate(settingsPath string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("workflow create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tracePath := flags.String("traces", "", "state database (default "+workflow.DefaultPath()+")")
	repository := flags.String("repository", "", "repository id to serve at the repository level")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 1 {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow create takes one graph file, e.g. atenea workflow create plan.toml")
	}
	graph, err := workflow.ReadFile(flags.Arg(0))
	if err != nil {
		return err
	}
	ctx := context.Background()
	engine, closers, err := openWorkflow(ctx, settingsPath, *tracePath, *repository, out)
	if err != nil {
		return err
	}
	defer closers()

	run, gate, err := engine.Create(ctx, graph)
	if err != nil {
		return err
	}
	printGate(out, run, gate)
	return nil
}

func workflowLaunch(settingsPath string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("workflow launch", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tracePath := flags.String("traces", "", "state database (default "+workflow.DefaultPath()+")")
	repository := flags.String("repository", "", "repository id to serve at the repository level")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 1 {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow launch takes one workflow id, e.g. atenea workflow launch wf1786-1")
	}
	ctx, stop := interruptible()
	defer stop()

	engine, closers, err := openWorkflow(ctx, settingsPath, *tracePath, *repository, out)
	if err != nil {
		return err
	}
	defer closers()

	run, runErr := engine.Launch(ctx, flags.Arg(0))
	if run.ID != "" {
		printRun(out, run)
	}
	return runErr
}

// workflowPropose puts an expansion to the person running the workflow.
//
// It writes the question and returns. Nothing here proposes anything on its
// own: the graph comes from a file, the same as the first one did, and until
// there is an orchestrator this is where a proposal enters the system.
func workflowPropose(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("workflow propose", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tracePath := flags.String("traces", "", "state database (default "+workflow.DefaultPath()+")")
	var replaces stringList
	flags.Var(&replaces, "replaces", "step this proposal removes; repeatable, and only steps that have not started")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 2 {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow propose takes a workflow id and a graph file, e.g. atenea workflow propose wf1786-1 next.toml")
	}
	graph, err := workflow.ReadFile(flags.Arg(1))
	if err != nil {
		return err
	}
	ctx := context.Background()
	store, err := workflow.Open(ctx, *tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	id := flags.Arg(0)
	gate, err := store.Ask(ctx, id, workflow.KindApprove,
		workflow.Proposal{Steps: graph.Steps, Replaces: replaces}, time.Now())
	if err != nil {
		return err
	}
	run, err := store.Load(ctx, id)
	if err != nil {
		return err
	}
	printGate(out, run, gate)
	return nil
}

// workflowAnswer records a decision on whatever gate is open.
//
// The answer is a row, not a reply on a channel: the Atenea that asked may be
// gone, and the next one to take the run over reads the same record.
func workflowAnswer(args []string, out io.Writer, decision workflow.Decision) error {
	name := "workflow approve"
	if decision == workflow.DecisionRejected {
		name = "workflow reject"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tracePath := flags.String("traces", "", "state database (default "+workflow.DefaultPath()+")")
	reason := flags.String("reason", "", "why, on a rejection: required")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 1 {
		return contract.Fail(contract.FailureInvalidInput,
			"%s takes one workflow id", name)
	}
	ctx := context.Background()
	store, err := workflow.Open(ctx, *tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	id := flags.Arg(0)
	gate, ok, err := store.OpenGate(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return contract.Fail(contract.FailureNotFound,
			"workflow %s has nothing waiting", id)
	}
	if gate.Kind == workflow.KindLaunch && decision == workflow.DecisionApproved {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow %s is waiting to be launched, not approved: `atenea workflow launch %s` reads the plan "+
				"and runs it, because whoever commits the grant is whoever spends it", id, id)
	}
	answered, err := store.Answer(ctx, id, gate.Ordinal, decision,
		workflow.Hand("cli"), *reason, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "gate %d %s  %s  %s\n", answered.Ordinal, answered.Kind,
		answered.Decision, workflow.Short(answered.Digest))
	fmt.Fprintf(out, "by %s\n", answered.Hand)
	if answered.Reason != "" {
		fmt.Fprintf(out, "%s\n", answered.Reason)
	}
	return nil
}

func workflowRun(settingsPath string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("workflow run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tracePath := flags.String("traces", "", "state database (default "+workflow.DefaultPath()+")")
	repository := flags.String("repository", "", "repository id to serve at the repository level")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 1 {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow run takes one graph file, e.g. atenea workflow run plan.toml")
	}
	graph, err := workflow.ReadFile(flags.Arg(0))
	if err != nil {
		return err
	}

	ctx, stop := interruptible()
	defer stop()

	engine, closers, err := openWorkflow(ctx, settingsPath, *tracePath, *repository, out)
	if err != nil {
		return err
	}
	defer closers()

	run, runErr := engine.Start(ctx, graph)
	if run.ID != "" {
		printRun(out, run)
	}
	return runErr
}

func workflowResume(settingsPath string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("workflow resume", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tracePath := flags.String("traces", "", "state database (default "+workflow.DefaultPath()+")")
	repository := flags.String("repository", "", "repository id to serve at the repository level")
	var redo stringList
	flags.Var(&redo, "redo", "step to dispatch again although nobody judged it; repeatable")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 1 {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow resume takes one workflow id, e.g. atenea workflow resume wf1786-1")
	}

	ctx, stop := interruptible()
	defer stop()

	engine, closers, err := openWorkflow(ctx, settingsPath, *tracePath, *repository, out)
	if err != nil {
		return err
	}
	defer closers()

	run, runErr := engine.Resume(ctx, flags.Arg(0), redo)
	if run.ID != "" {
		printRun(out, run)
	}
	return runErr
}

func workflowList(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("workflow list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tracePath := flags.String("traces", "", "state database to read")
	limit := flags.Int("limit", 20, "how many runs to show; 0 for all")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	ctx := context.Background()
	store, err := workflow.Open(ctx, *tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	runs, err := store.List(ctx, *limit)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Fprintf(out, "no workflows in %s\n", store.Path())
		return nil
	}
	fmt.Fprintf(out, "%-20s %-22s %-12s %s\n", "ID", "STARTED", "STATE", "TASK")
	for _, run := range runs {
		full, err := store.Load(ctx, run.ID)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%-20s %-22s %-12s %s\n",
			run.ID, run.Started.Local().Format("2006-01-02 15:04:05"),
			runState(full), truncate(run.Task, 50))
	}
	fmt.Fprintf(out, "\n%s in %s\n", plural(len(runs), "workflow", "workflows"), store.Path())
	return nil
}

func workflowShow(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("workflow show", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tracePath := flags.String("traces", "", "state database to read")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 1 {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow show takes one workflow id")
	}
	ctx := context.Background()
	store, err := workflow.Open(ctx, *tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	run, err := store.Load(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	printRun(out, run)
	gates, err := store.Gates(ctx, run.ID)
	if err != nil {
		return err
	}
	printGates(out, gates)
	return nil
}

// openWorkflow builds the engine over the same trace store the agents write
// to, and hands back the closer for both.
func openWorkflow(ctx context.Context, settingsPath, tracePath, repository string,
	out io.Writer) (*workflow.Engine, func(), error) {
	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return nil, nil, err
	}
	return workflow.Serve(ctx, cfg, tracePath, repository, "cli", out)
}

// interruptible cuts the run on the first ctrl-c and leaves the second one to
// the runtime. A workflow that ignored the second would be a process the
// operator cannot get rid of, which is worse than an unclean stop.
func interruptible() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// runState is the one word for a whole run: what it is doing, or why it is not.
func runState(run workflow.Run) string {
	switch {
	case run.Closed:
		return "finished"
	case run.Stop != workflow.StopNone:
		return string(run.Stop)
	case run.WriterPID != 0 && pidlock.Alive(run.WriterPID):
		return "running"
	case run.WriterPID != 0:
		// The record names an owner and that owner is gone. Printing
		// "running" here would be the same unmeasured claim the resume path
		// refuses to make: a record saying running is not evidence that
		// anything runs, and the pid is what settles it.
		return "orphaned"
	default:
		// Closed runs and stopped runs are caught above, and End zeroes the
		// pid, so what is left is a run nobody has ever owned: a plan
		// waiting to be launched. Calling that interrupted would put a
		// casualty on the record where there is a question.
		return "unlaunched"
	}
}

func printRun(out io.Writer, run workflow.Run) {
	fmt.Fprintf(out, "%s  %s\n", run.ID, run.Task)
	fmt.Fprintf(out, "%s  %s\n", runState(run), run.Summary())
	fmt.Fprintf(out, "%s\n", run.Budget())
	fmt.Fprintln(out)

	fmt.Fprintf(out, "%-16s %-14s %-8s %-12s %-12s %-12s %s\n",
		"STEP", "AGENT", "LANE", "STATE", "FORECAST", "COST", "DETAIL")
	for _, step := range run.Steps {
		forecast := "-"
		if step.Step.BudgetEstimateUSD > 0 {
			forecast = fmt.Sprintf("~$%.2f", step.Step.BudgetEstimateUSD)
		}
		fmt.Fprintf(out, "%-16s %-14s %-8s %-12s %-12s %-12s %s\n",
			truncate(step.Step.ID, 16),
			truncate(step.Step.TypeName, 14),
			step.Pool,
			run.State(step),
			forecast,
			stepCost(run, step),
			stepDetail(run, step))
	}

	if interrupted := run.Interrupted(); len(interrupted) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "%s nobody judged. Resume redoes the read-only ones; "+
			"name the others with --redo to run them again:\n",
			plural(len(interrupted), "step", "steps"))
		for _, step := range interrupted {
			fmt.Fprintf(out, "  %s (%s)\n", step.Step.ID, step.Reason.Text)
		}
	}
}

// printGate shows a plan somebody has to read before it runs.
//
// It prints the digest, because the digest is what the approval binds to: the
// engine recomputes it over what it is about to apply and refuses on any
// difference. Two printed plans with the same short digest are the same plan.
func printGate(out io.Writer, run workflow.Run, gate workflow.Gate) {
	fmt.Fprintf(out, "%s  %s\n", run.ID, run.Task)
	fmt.Fprintf(out, "gate %d %s  waiting  %s\n", gate.Ordinal, gate.Kind, workflow.Short(gate.Digest))
	// Allocated, never spent. Nothing on this machine can report a charge
	// yet, so this line is what the plan claims of the grant and not what
	// running it will cost.
	fmt.Fprintf(out, "$%.2f of $%.2f allocated by this plan\n",
		gate.Proposal.AllocatedUSD(), run.GrantUSD)
	fmt.Fprintln(out)

	fmt.Fprintf(out, "%-16s %-14s %-8s %s\n", "STEP", "AGENT", "SHARE", "OBJECTIVE")
	for _, step := range gate.Proposal.Steps {
		fmt.Fprintf(out, "%-16s %-14s $%-7.2f %s\n",
			truncate(step.ID, 16), truncate(step.TypeName, 14),
			step.Permission.BudgetUSD, truncate(step.Task.Objective, 44))
	}
	for _, id := range gate.Proposal.Replaces {
		fmt.Fprintf(out, "  replaces %s\n", id)
	}
	fmt.Fprintln(out)

	verb := "launch"
	if gate.Kind == workflow.KindApprove {
		verb = "approve"
	}
	fmt.Fprintf(out, "atenea workflow %s %s\n", verb, run.ID)
}

// printGates is the log: which questions were put, and how each was answered.
func printGates(out io.Writer, gates []workflow.Gate) {
	if len(gates) == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%-4s %-8s %-9s %-14s %-24s %s\n",
		"GATE", "KIND", "DECISION", "DIGEST", "WHEN", "HAND")
	for _, gate := range gates {
		when := gate.Asked
		if !gate.Answered.IsZero() {
			when = gate.Answered
		}
		fmt.Fprintf(out, "%-4d %-8s %-9s %-14s %-24s %s\n",
			gate.Ordinal, gate.Kind, gate.Decision, workflow.Short(gate.Digest),
			when.Local().Format("2006-01-02 15:04:05"), gate.Hand)
		if gate.Reason != "" {
			fmt.Fprintf(out, "     %s\n", gate.Reason)
		}
	}
}

// stepCost is the one word for what a step was charged: the dollar figure
// once one exists, the tokens when something measured but nobody priced it,
// and `unmeasured` when nothing could say. Never $0.00 or a dash for that
// last case -- either would print a measurement nothing took.
//
// EVERY attempt, not the live one. `workflow_step` holds exactly one, so a
// redo overwrites the row and files the previous dispatch in the archive: this
// column showed $0.68 for a step that had cost $1.29 (measured 2026-08-16 on
// the first real redo), and it did so under a run header that had just been
// fixed to say $7.32 -- so the column no longer summed to the total two lines
// above it. The run line names the superseded portion, which is where a reader
// goes for the split; this column is the step's whole bill.
//
// Dollars are totalled and tokens are not, for the reason [workflow.Spend]
// gives: a cut attempt's token record is the one the killed-turn accounting
// fix exists to correct, and importing it here would understate a total that
// is already correct in dollars.
func stepCost(run workflow.Run, step workflow.StepRow) string {
	usd, priced := 0.0, false
	if step.Spent.USD != nil {
		usd, priced = *step.Spent.USD, true
	}
	for _, attempt := range run.Superseded {
		if attempt.StepID == step.Step.ID && attempt.Spent.USD != nil {
			usd += *attempt.Spent.USD
			priced = true
		}
	}
	if priced {
		return fmt.Sprintf("$%.2f", usd)
	}
	if !step.Spent.Measured() {
		return "unmeasured"
	}
	return fmt.Sprintf("%d tok", step.Spent.Tokens())
}

// stepDetail is the most useful sentence about one step: why it ended badly,
// what it found, or what it is waiting for. A partial answer's stopped_at is
// folded into whichever of those it is -- the STATE column already says the
// answer is partial, and DETAIL is where a reader learns what was cut short.
func stepDetail(run workflow.Run, step workflow.StepRow) string {
	if !step.Reason.Empty() {
		return truncate(withStopped(step.Reason.Kind.String()+": "+step.Reason.Text, step), 60)
	}
	switch step.Status {
	case workflow.StatusOK:
		var detail string
		if len(step.Result) > 0 {
			detail = resultLine(step.Result[firstKey(step.Result)])
		} else {
			detail = took(traceLike(step))
		}
		return truncate(withStopped(detail, step), 60)
	case workflow.StatusPending:
		// Not truncated. The subject form of this line carries the command
		// that clears it, and a cure cut off at sixty columns is not one.
		if reason := run.BlockReason(step.Step.ID); reason != "" {
			return reason
		}
		if len(step.Needs()) > 0 {
			return "after " + strings.Join(step.Needs(), ", ")
		}
	}
	return ""
}

// withStopped appends what a partial answer did not reach, when the report
// says so. A coverage figure in the STATE column with no explanation beside
// it tells a reader something is missing without telling them what.
func withStopped(detail string, step workflow.StepRow) string {
	stopped := step.Report().StoppedAt
	if stopped == "" {
		return detail
	}
	if detail == "" {
		return "stopped at " + stopped
	}
	return detail + " (stopped at " + stopped + ")"
}

func firstKey(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// traceLike reuses the trace printer's duration wording, so a step reads the
// same in both listings.
func traceLike(step workflow.StepRow) trace.Row {
	return trace.Row{StartedAt: step.Started, EndedAt: step.Ended}
}

// stringList collects a repeatable flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return contract.Fail(contract.FailureInvalidInput, "--redo needs a step id")
	}
	*s = append(*s, value)
	return nil
}

// raiseList collects `--step ID=USD`, the pair a redo is.
//
// One flag carrying both halves, rather than a --step and a matching --share:
// two repeatable lists that have to be zipped by position is a shape where a
// mistyped pair reads as a valid one, and the mistake it makes is spending the
// wrong step's money.
type raiseList []workflow.Raise

func (r *raiseList) String() string {
	parts := make([]string, 0, len(*r))
	for _, raise := range *r {
		parts = append(parts, fmt.Sprintf("%s=%.2f", raise.StepID, raise.USD))
	}
	return strings.Join(parts, ",")
}

func (r *raiseList) Set(value string) error {
	id, share, ok := strings.Cut(strings.TrimSpace(value), "=")
	id, share = strings.TrimSpace(id), strings.TrimSpace(share)
	if !ok || id == "" || share == "" {
		return contract.Fail(contract.FailureInvalidInput,
			"--step takes ID=USD, e.g. --step admin-config=0.80")
	}
	usd, err := strconv.ParseFloat(strings.TrimPrefix(share, "$"), 64)
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput,
			"--step %s: %q is not a dollar figure", id, share)
	}
	if usd <= 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"--step %s: a share is money, and $%.2f is not", id, usd)
	}
	*r = append(*r, workflow.Raise{StepID: id, USD: usd})
	return nil
}

// workflowRedo dispatches steps cut at their own ceiling, at raised shares.
//
// Separate from resume on purpose: resume runs work nobody judged, as it was.
// This spends more money on work that was judged and ran out -- see
// [workflow.Engine.Redo] for why that is an operator's act and never
// automatic.
func workflowRedo(settingsPath string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("workflow redo", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tracePath := flags.String("traces", "", "state database (default "+workflow.DefaultPath()+")")
	repository := flags.String("repository", "", "repository id to serve at the repository level")
	grant := flags.Float64("grant", 0, "the run's new total grant; default leaves it alone")
	var raises raiseList
	flags.Var(&raises, "step", "ID=USD: a step cut at its ceiling, and its raised share; repeatable")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 1 {
		// Flags before the id, because Go's parser stops at the first word
		// that is not a flag -- so the id-first form this example used to
		// print could not parse, and every flag after it was read as another
		// id. Found by typing it.
		return contract.Fail(contract.FailureInvalidInput,
			"workflow redo takes one workflow id, with its flags before it, e.g. "+
				"atenea workflow redo --step admin-config=0.80 --grant 8.90 wf1786-1")
	}
	if len(raises) == 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow redo needs at least one --step ID=USD: a step is only redone "+
				"at a share somebody raised")
	}

	ctx, stop := interruptible()
	defer stop()

	engine, closers, err := openWorkflow(ctx, settingsPath, *tracePath, *repository, out)
	if err != nil {
		return err
	}
	defer closers()

	run, runErr := engine.Redo(ctx, flags.Arg(0), raises, *grant)
	if run.ID != "" {
		printRun(out, run)
	}
	return runErr
}
