package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/agent/filereader"
	"github.com/Tutitoos/atenea/internal/agent/plancheck"
	"github.com/Tutitoos/atenea/internal/agent/planner"
	"github.com/Tutitoos/atenea/internal/agent/reviewer"
	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// cmdAgent runs one declared agent, once.
//
// It deliberately does not go through core.New. An agent run needs the
// settings and a trace database and nothing else; booting the funnel would
// start managed processes -- Serena, on a machine that opted in -- for a
// dispatch that never touches them.
func cmdAgent(settingsPath string, args []string, out io.Writer) error {
	// The type name comes off first, before the flag set sees anything.
	// Go's flag parser stops at the first non-flag word, so leaving the
	// name in the list would make `atenea agent filereader --traces X`
	// silently treat `--traces X` as two file names -- a wrong answer that
	// looks like a right one, which is the failure this whole command is
	// about.
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return contract.Fail(contract.FailureInvalidInput,
			"agent needs a type name first, e.g. atenea agent filereader README.md")
	}
	typeName, args := strings.TrimSpace(args[0]), args[1:]

	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	objective := flags.String("objective", "", "what the agent is being asked to do")
	criterion := flags.String("criterion", "", "what done looks like")
	tracePath := flags.String("traces", "", "trace database (default "+trace.DefaultPath()+")")
	repository := flags.String("repository", "", "repository id to serve at the repository level")
	quiet := flags.Bool("quiet", false, "print the verdict line only")
	review := flags.String("review", "", "agent type that audits the answer; a refusal relaunches the work once")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	files := flags.Args()
	// Go's parser stops at the first word that is not a flag, so anything
	// flag-shaped after a file name reached here as a file name. Reading a
	// file called "--traces" is not what anyone meant, and running with the
	// default database while the operator believes they named one is the
	// kind of wrong answer that looks right.
	for _, file := range files {
		if strings.HasPrefix(file, "-") {
			return contract.Fail(contract.FailureInvalidInput,
				"flags come before file names: atenea agent %s %s [files...]",
				typeName, file)
		}
	}

	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}
	declared, err := cfg.AgentTypeByName(typeName)
	if err != nil {
		return err
	}

	ctx := context.Background()
	store, err := openTraces(ctx, *tracePath, out, *quiet)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	runner, err := agent.New(agent.Options{
		Types:     cfg.Agents,
		Store:     store,
		Workspace: workspaceFor(cfg, *repository),
		History: func(ctx context.Context, name string, limit int) ([]trace.Row, error) {
			return store.List(ctx, trace.Filter{TypeName: name, Limit: limit})
		},
	})
	if err != nil {
		return err
	}

	task := contract.Task{
		Objective: strings.TrimSpace(*objective),
		Files:     files,
		Criterion: strings.TrimSpace(*criterion),
	}
	if task.Objective == "" {
		task.Objective = defaultObjective(declared, files)
	}
	if task.Criterion == "" {
		task.Criterion = "the answer matches the shape " + typeName + " declares"
	}

	if *review != "" {
		audited, err := runner.RunReviewed(ctx, typeName, *review, task)
		printReviewed(out, audited, *quiet)
		return err
	}

	report, assignment, runErr := runner.Run(ctx, typeName, task, nil)
	printReport(out, assignment, report, *quiet)
	return runErr
}

func defaultObjective(declared config.AgentType, files []string) string {
	if len(files) > 0 {
		return "read " + files[0] + " and answer"
	}
	if declared.Summary != "" {
		return declared.Summary
	}
	return "run " + declared.Spec.Name
}

// workspaceFor picks what the repository level describes. A named repository
// must exist; an unnamed one takes the working directory, because a command
// run inside a tree is asking about that tree.
func workspaceFor(cfg config.Config, id string) agent.Workspace {
	ws := agent.Workspace{AteneaVersion: buildinfo.Version}
	for _, repo := range cfg.Repositories {
		ws.Repositories = append(ws.Repositories, repo.ID)
		if repo.ID == id || (id == "" && ws.RepositoryID == "") {
			ws.RepositoryID, ws.RepositoryRoot = repo.ID, repo.Path
		}
	}
	sort.Strings(ws.Repositories)
	if ws.RepositoryRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			ws.RepositoryID, ws.RepositoryRoot = "current", cwd
		}
	}
	return ws
}

// openTraces opens the store and closes whatever the last Atenea left open.
//
// The sweep runs here, on the way in, because "Atenea's next start" is the
// only moment that can honestly answer for a run nobody watched end.
func openTraces(ctx context.Context, path string, out io.Writer, quiet bool) (*trace.Store, error) {
	store, err := trace.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	closed, err := store.SweepOrphans(ctx, time.Now())
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if closed > 0 && !quiet {
		fmt.Fprintf(out, "closed %s left open by a previous run, as incomplete\n",
			plural(closed, "trace", "traces"))
	}
	return store, nil
}

func printReport(out io.Writer, assignment contract.Assignment,
	report contract.Report, quiet bool) {
	if assignment.ID == "" {
		return
	}
	fmt.Fprintf(out, "%s  %s  %s", assignment.ID, assignment.TypeName, report.Verdict)
	if !report.Reason.Empty() {
		fmt.Fprintf(out, "  (%s: %s)", report.Reason.Kind, report.Reason.Text)
	}
	fmt.Fprintln(out)
	if quiet {
		return
	}
	for _, key := range sortedKeys(report.Result) {
		fmt.Fprintf(out, "  %s: %s\n", key, resultLine(report.Result[key]))
	}
	for _, d := range report.Discovered {
		fmt.Fprintf(out, "  discovered (%s): %s\n", d.Level, d.Note)
	}
	for _, notice := range report.Notices {
		fmt.Fprintf(out, "  notice: %s\n", notice)
	}
}

// printReviewed prints every attempt and its review, in the order they ran.
//
// All of them, not just the last: "passed on the second try" and "passed" are
// different facts about an agent, and printing only the accepted answer would
// hide the relaunch that produced it -- along with the reason the first one
// was refused, which is the most useful line on the screen.
func printReviewed(out io.Writer, run agent.ReviewedRun, quiet bool) {
	for i, attempt := range run.Attempts {
		fmt.Fprintf(out, "attempt %d/%d\n", i+1, agent.MaxAttempts)
		printReport(out, attempt.Work, attempt.Report, quiet)
		if attempt.Review.ID == "" {
			fmt.Fprintln(out, "  not reviewed: the run did not answer")
			continue
		}
		fmt.Fprint(out, "review ")
		printReport(out, attempt.Review, attempt.ReviewReport, quiet)
	}
	switch {
	case run.Accepted():
		fmt.Fprintln(out, "accepted")
	case len(run.Attempts) >= agent.MaxAttempts:
		fmt.Fprintf(out, "refused on both attempts; no third was run\n")
	default:
		fmt.Fprintln(out, "not accepted")
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// oneLine keeps a result readable on a terminal. A file's contents are a
// legitimate result and printing them raw would bury the verdict.
func resultLine(value any) string {
	text := strings.TrimSpace(fmt.Sprintf("%v", value))
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		lines := strings.Count(text, "\n") + 1
		return fmt.Sprintf("%s… (%d lines)", strings.TrimSpace(text[:i]), lines)
	}
	const wide = 120
	if len(text) > wide {
		return text[:wide] + "…"
	}
	return text
}

// cmdAgentRun is the far side of a spawn, not a command a person types.
//
// It is here rather than in its own binary because a second binary is a
// second thing to install, find on PATH and keep in step -- and the settings
// file can name this one by the path it was already started from.
func cmdAgentRun(kind string, stdin io.Reader, stdout io.Writer) error {
	switch kind {
	case "filereader":
		return filereader.Main(stdin, stdout)
	case "reviewer":
		return reviewer.Main(stdin, stdout)
	case "plan-check":
		return plancheck.Main(stdin, stdout)
	case "explore", "plan":
		// These two call a model, so they are the only built-ins that can
		// be waiting on somebody else when a person gives up. A signal has
		// to reach the turn, or the parent's kill leaves a request billing
		// against a run nobody is reading.
		ctx, stop := interruptible()
		defer stop()
		if kind == "explore" {
			return planner.Explore(ctx, stdin, stdout)
		}
		return planner.Plan(ctx, stdin, stdout)
	default:
		return contract.Fail(contract.FailureNotFound,
			"no built-in agent %q: this binary ships filereader, reviewer, plan-check, explore and plan", kind)
	}
}

// cmdTraces prints the record.
//
// Plain text, because it is read while something is wrong. A binary store
// with no reader means debugging by hand-written SQL under pressure, which is
// how a store stops being consulted at all.
func cmdTraces(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("traces", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var filter trace.Filter
	flags.StringVar(&filter.ID, "id", "", "one execution")
	flags.StringVar(&filter.TypeName, "type", "", "narrow to one agent type")
	verdict := flags.String("verdict", "", "narrow to ok, failed, incomplete or canceled")
	flags.BoolVar(&filter.OpenOnly, "open", false, "only runs with no ending yet")
	since := flags.String("since", "", "only runs started within this window, e.g. 2h")
	flags.IntVar(&filter.Limit, "limit", 0,
		fmt.Sprintf("how many rows (default %d)", trace.DefaultLimit))
	tracePath := flags.String("traces", "", "trace database (default "+trace.DefaultPath()+")")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() > 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"traces takes flags only, got %q", flags.Arg(0))
	}
	if *verdict != "" {
		parsed, err := contract.ParseVerdict(*verdict)
		if err != nil {
			return err
		}
		filter.Verdict = parsed
	}
	if *since != "" {
		window, err := time.ParseDuration(*since)
		if err != nil {
			return contract.Fail(contract.FailureInvalidInput,
				"--since %q: %v", *since, err)
		}
		filter.Since = time.Now().Add(-window)
	}

	ctx := context.Background()
	store, err := trace.Open(ctx, *tracePath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	rows, err := store.List(ctx, filter)
	if err != nil {
		return err
	}
	printTraces(out, rows, store.Path())
	return nil
}

func printTraces(out io.Writer, rows []trace.Row, path string) {
	if len(rows) == 0 {
		fmt.Fprintf(out, "no traces in %s\n", path)
		return
	}
	fmt.Fprintf(out, "%-22s  %-16s  %-10s  %-9s  %-8s  %s\n",
		"STARTED", "TYPE", "VERDICT", "TOOK", "RUN", "OBJECTIVE")
	for _, row := range rows {
		fmt.Fprintf(out, "%-22s  %-16s  %-10s  %-9s  %-8s  %s\n",
			row.StartedAt.Local().Format("2006-01-02 15:04:05"),
			truncate(row.TypeName, 16),
			verdictLabel(row),
			took(row),
			runLabel(row),
			truncate(row.Objective, 50))
		if link := linkLine(row); link != "" {
			fmt.Fprintf(out, "%24s%s\n", "", link)
		}
		if !row.Reason.Empty() {
			fmt.Fprintf(out, "%24s%s: %s\n", "", row.Reason.Kind, truncate(row.Reason.Text, 90))
		}
	}
	fmt.Fprintf(out, "\n%s in %s\n", plural(len(rows), "trace", "traces"), path)
}

// verdictLabel marks a swept row. Both read `incomplete`, and the difference
// between "the agent said it stopped short" and "nobody ever heard back" is
// the first thing a reader needs.
func verdictLabel(row trace.Row) string {
	if row.Open() {
		return "running"
	}
	if row.Swept {
		return row.Verdict.String() + "*"
	}
	return row.Verdict.String()
}

// runLabel says which try this is. A relaunch is a second row on purpose, so
// the column that tells two attempts at one piece of work apart from two
// separate asks has to be on the screen, not derived by the reader.
func runLabel(row trace.Row) string {
	if row.Reviews != "" {
		return "review"
	}
	if row.Attempt > 1 {
		return fmt.Sprintf("try %d", row.Attempt)
	}
	return ""
}

// linkLine names what this row is about: the attempt it redoes, or the run it
// audits. The id is printed in full because it is what --id takes.
func linkLine(row trace.Row) string {
	switch {
	case row.RetryOf != "":
		return "redoes " + row.RetryOf
	case row.Reviews != "":
		return "reviews " + row.Reviews
	}
	return ""
}

func took(row trace.Row) string {
	if row.Open() {
		return "-"
	}
	return row.Duration().Round(time.Millisecond).String()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
