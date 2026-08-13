package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
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
			"workflow needs a subcommand: run, resume, list or show")
	}
	sub, rest := strings.TrimSpace(args[0]), args[1:]
	switch sub {
	case "run":
		return workflowRun(settingsPath, rest, out)
	case "resume":
		return workflowResume(settingsPath, rest, out)
	case "list":
		return workflowList(rest, out)
	case "show":
		return workflowShow(rest, out)
	default:
		return contract.Fail(contract.FailureInvalidInput,
			"unknown workflow subcommand %q: run, resume, list or show", sub)
	}
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
	traces, err := openTraces(ctx, tracePath, out, false)
	if err != nil {
		return nil, nil, err
	}
	state, err := workflow.Open(ctx, traces.Path())
	if err != nil {
		_ = traces.Close()
		return nil, nil, err
	}
	runner, err := agent.New(agent.Options{
		Types:     cfg.Agents,
		Store:     traces,
		Workspace: workspaceFor(cfg, repository),
		History: func(ctx context.Context, name string, limit int) ([]trace.Row, error) {
			return traces.List(ctx, trace.Filter{TypeName: name, Limit: limit})
		},
	})
	if err != nil {
		_ = state.Close()
		_ = traces.Close()
		return nil, nil, err
	}
	engine, err := workflow.New(workflow.Options{
		Runner: runner,
		Store:  state,
		Types:  cfg.Agents,
		Lanes:  cfg.Workflow,
	})
	if err != nil {
		_ = state.Close()
		_ = traces.Close()
		return nil, nil, err
	}
	return engine, func() {
		_ = state.Close()
		_ = traces.Close()
	}, nil
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
	case run.WriterPID != 0:
		return "running"
	default:
		return "interrupted"
	}
}

func printRun(out io.Writer, run workflow.Run) {
	fmt.Fprintf(out, "%s  %s\n", run.ID, run.Task)
	fmt.Fprintf(out, "%s  %s\n", runState(run), run.Summary())
	fmt.Fprintf(out, "%s\n", run.Budget())
	fmt.Fprintln(out)

	fmt.Fprintf(out, "%-16s %-14s %-8s %-12s %s\n", "STEP", "AGENT", "LANE", "STATE", "DETAIL")
	for _, step := range run.Steps {
		fmt.Fprintf(out, "%-16s %-14s %-8s %-12s %s\n",
			truncate(step.Step.ID, 16),
			truncate(step.Step.TypeName, 14),
			step.Pool,
			run.Label(step),
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

// stepDetail is the most useful sentence about one step: why it ended badly,
// what it found, or what it is waiting for.
func stepDetail(run workflow.Run, step workflow.StepRow) string {
	if !step.Reason.Empty() {
		return truncate(step.Reason.Kind.String()+": "+step.Reason.Text, 60)
	}
	switch step.Status {
	case workflow.StatusOK:
		if len(step.Result) > 0 {
			return truncate(resultLine(step.Result[firstKey(step.Result)]), 60)
		}
		return took(traceLike(step))
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
