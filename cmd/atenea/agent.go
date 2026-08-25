package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/agent/filereader"
	"github.com/Tutitoos/atenea/internal/agent/plancheck"
	"github.com/Tutitoos/atenea/internal/agent/planner"
	"github.com/Tutitoos/atenea/internal/agent/reviewer"
	"github.com/Tutitoos/atenea/internal/agent/semanticreviewer"
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

	flags, opts := agentFlags()
	objective, criterion, tracePath := opts.objective, opts.criterion, opts.tracePath
	repository, quiet, review, confirm := opts.repository, opts.quiet, opts.review, opts.confirm
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

	// Resolved before the trace store is opened, because opening it sweeps
	// orphans and prints about it: a typo in --repository must not leave that
	// behind on its way to being refused.
	workspace, err := workspaceFor(cfg, *repository)
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
		Workspace: workspace,
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
	if requiresAgentConfirmation(declared.Effects) && !*confirm {
		return contract.Fail(contract.FailurePermissionDenied,
			"agent %s may cause write or external effects; pass --confirm before execution", typeName)
	}
	if *confirm {
		if err := confirmTTY(out, "agent "+typeName, 0, declared.Effects); err != nil {
			return err
		}
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

// agentOptions is where `atenea agent` parks what its flags carry.
type agentOptions struct {
	objective  *string
	criterion  *string
	tracePath  *string
	repository *string
	review     *string
	quiet      *bool
	confirm    *bool
}

// agentFlags registers the command's flags on a set of its own.
//
// Separated from cmdAgent so the set can be walked without running anything:
// a flag this command accepts and its help page never mentions is a flag
// nobody finds, and --confirm reached the point of being mandatory for write
// and external types while going undocumented. The test that walks it is what
// keeps the two in step.
func agentFlags() (*flag.FlagSet, agentOptions) {
	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags, agentOptions{
		objective:  flags.String("objective", "", "what the agent is being asked to do"),
		criterion:  flags.String("criterion", "", "what done looks like"),
		tracePath:  flags.String("traces", "", "trace database (default "+trace.DefaultPath()+")"),
		repository: flags.String("repository", "", "repository id to serve at the repository level"),
		quiet:      flags.Bool("quiet", false, "print the verdict line only"),
		review:     flags.String("review", "", "agent type that audits the answer; a refusal relaunches the work once"),
		confirm:    flags.Bool("confirm", false, "require an interactive TTY confirmation for write or external effects"),
	}
}

func requiresAgentConfirmation(effects []contract.Effect) bool {
	for _, effect := range effects {
		if effect == contract.EffectWrite || effect == contract.EffectExternal {
			return true
		}
	}
	return false
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

// workspaceFor picks what the repository level describes.
//
// A named repository must exist, and a name nothing matches is refused rather
// than resolved: the fallback below would otherwise hand the agent the working
// directory under the id "current", so a typo in --repository reads as a
// successful run against a tree nobody named. Every other command that takes a
// repository refuses an unknown one; this one used to be the exception.
//
// An unnamed repository takes the first one the settings file declares, and
// only falls back to the working directory when the file declares none at all.
// That is not "the tree the command was run in": a machine with a catalog gets
// its first entry whatever directory the operator is standing in.
func workspaceFor(cfg config.Config, id string) (agent.Workspace, error) {
	ws := agent.Workspace{AteneaVersion: buildinfo.Version}
	for _, repo := range cfg.Repositories {
		ws.Repositories = append(ws.Repositories, repo.ID)
		if repo.ID == id || (id == "" && ws.RepositoryID == "") {
			ws.RepositoryID, ws.RepositoryRoot = repo.ID, repo.Path
		}
	}
	sort.Strings(ws.Repositories)
	if id != "" && ws.RepositoryID != id {
		return agent.Workspace{}, contract.Fail(contract.FailureNotFound,
			"no repository %q is declared; the settings file declares: %s",
			id, orNone(ws.Repositories))
	}
	if ws.RepositoryRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			ws.RepositoryID, ws.RepositoryRoot = "current", cwd
		}
	}
	return ws, nil
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
		// The same guard truncate documents: wide is a byte budget, so the
		// cut can land inside a multi-byte rune and the ellipsis would then
		// be glued to half a character -- which a terminal draws as U+FFFD
		// in the middle of a result the operator is trying to read.
		return strings.ToValidUTF8(text[:wide], "") + "…"
	}
	return text
}

// cmdAgentRun is the far side of a spawn, not a command a person types.
//
// It is here rather than in its own binary because a second binary is a
// second thing to install, find on PATH and keep in step -- and the settings
// file can name this one by the path it was already started from.
func cmdAgentRun(kind string, stdin io.Reader, stdout io.Writer) error {
	// Validated before anything is written. recordAssignment interpolates the
	// name into a file name, and filepath.Join collapses whatever "../" the
	// name carries into a path outside the log directory -- so a name this
	// binary does not ship must be refused here, while it is still only a
	// string, rather than after a file bearing it has been created.
	if !builtinAgent(kind) {
		return unknownBuiltinAgent(kind)
	}
	stdin = recordAssignment(kind, stdin)
	switch kind {
	case "filereader":
		return filereader.Main(stdin, stdout)
	case "reviewer":
		return reviewer.Main(stdin, stdout)
	case "semantic-reviewer":
		return semanticreviewer.Main(stdin, stdout)
	case "plan-check":
		return plancheck.Main(stdin, stdout)
	case "explore", "reader", "plan":
		// These three call a model, so they are the only built-ins that can
		// be waiting on somebody else when a person gives up. A signal has
		// to reach the turn, or the parent's kill leaves a request billing
		// against a run nobody is reading.
		ctx, stop := interruptible()
		defer stop()
		switch kind {
		case "explore":
			return planner.Explore(ctx, stdin, stdout)
		case "reader":
			// The same half `explore` runs, with none of Atenea's
			// capabilities behind it -- see planner.Surface for what that
			// is worth per turn.
			return planner.Read(ctx, stdin, stdout)
		default:
			return planner.Plan(ctx, stdin, stdout)
		}
	default:
		// Unreachable: builtinAgent above admits exactly the names this
		// switch handles. It stays as the compiler's proof that a name added
		// to one list and not the other cannot fall through to nothing.
		return unknownBuiltinAgent(kind)
	}
}

// builtinAgents is the closed list of agents this binary ships, and the only
// names cmdAgentRun will act on or write to disk.
var builtinAgents = []string{
	"filereader", "reviewer", "semantic-reviewer", "plan-check", "explore", "reader", "plan",
}

func builtinAgent(kind string) bool { return slices.Contains(builtinAgents, kind) }

func unknownBuiltinAgent(kind string) error {
	return contract.Fail(contract.FailureNotFound,
		"no built-in agent %q: this binary ships filereader, reviewer, semantic-reviewer, plan-check, explore, reader and plan", kind)
}

// AssignmentLogEnv names a directory where the assignment each built-in agent
// is handed is written verbatim, before anything parses it.
//
// The sibling of model.PromptLogEnv, at the other end of the spawn. That one
// records what a turn was told; this records what the agent was given, which
// is the input a comparison has to hold fixed. Measured 2026-08-14: five runs
// of one unchanged commission produced five different planner prompts,
// because the planner's own input is another model's answer. A recorded
// assignment is what makes the next comparison a comparison -- replay it and
// the input is identical by construction rather than by hope.
//
// Unset records nothing. The bytes are passed through either way: this reads
// stdin and hands back a reader over the same bytes, so a failure to write
// costs the caller nothing but the record.
func recordAssignment(kind string, stdin io.Reader) io.Reader {
	dir := strings.TrimSpace(os.Getenv("ATENEA_ASSIGNMENT_LOG"))
	if dir == "" {
		return stdin
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		// The read failed, and the agent about to parse this must see the
		// same failure rather than an empty card that looks like a
		// malformed one.
		return io.MultiReader(bytes.NewReader(raw), errReader{err})
	}
	// 0700 and 0600, the modes the notebook and the checkpoint store already
	// use for the same reason: an assignment is a verbatim copy of what a run
	// was told, objectives and file paths included, and it lands wherever the
	// environment variable points -- often a shared temporary directory where
	// the default umask would leave it world-readable.
	if mkErr := os.MkdirAll(dir, 0o700); mkErr == nil {
		name := fmt.Sprintf("%d-%s-%d.json", time.Now().UnixNano(), kind, os.Getpid())
		_ = os.WriteFile(filepath.Join(dir, name), raw, 0o600)
	}
	return bytes.NewReader(raw)
}

// errReader hands one error to whoever reads next. It exists so a stdin that
// failed halfway is still reported as a read failure rather than silently
// becoming a short card.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

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
	// A byte slice at n or n-1 can land inside a multi-byte rune -- the
	// column budget n is a byte count, not a rune count, and neither
	// boundary is guaranteed to fall between two runes. ToValidUTF8 drops
	// exactly the incomplete trailing sequence that cut produces, leaving
	// a shorter but always-valid string underneath the ellipsis.
	if n <= 1 {
		return strings.ToValidUTF8(s[:n], "")
	}
	return strings.ToValidUTF8(s[:n-1], "") + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
