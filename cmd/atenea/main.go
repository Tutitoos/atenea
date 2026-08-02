// Command atenea is the entry point of the Atenea orchestration core.
//
// Atenea lives outside the CLIs it serves, so this binary is what gets started
// on the machine. `run` is the lifecycle and the rest of the commands are the
// operator's window into the catalog, the selector and the agent.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const usage = `atenea - orchestration core

Usage:
  atenea [--config PATH] <command> [flags]

Commands:
  status                 Short health screen: one light for Atenea, one per provider
  select CAPABILITY      Ask the funnel who should answer a capability
  task "TEXT"            Hand a commission to the orchestrator; --budget USD
                         funds this one above the settings file
  ask CAPABILITY         Dispatch one capability against one repository
  catalog                List capabilities, providers and repositories in full
  run                    Run as a service until interrupted
  incidents              Read the crash notebook; add 'clear' to mark it read
  config init            Write the built-in settings file to disk
  config path            Print where settings are read from
  version                Print the product and contract versions

Global flags:
  --config PATH          Settings file. Falls back to $ATENEA_CONFIG, then
                         $XDG_CONFIG_HOME/atenea/atenea.toml, then the built-in
                         defaults.
`

func main() {
	// The outermost net. A panic anywhere below here would otherwise print a
	// stack to a terminal nobody was watching and take the evidence with it
	// when the window closed. This runs before the settings are even read, so
	// a crash while loading them is recorded too -- which is why the notebook
	// takes its path from the environment and not from the file it is about
	// to fail to parse.
	//
	// A notebook that cannot even be prepared is not worth refusing to start
	// over: the command still works, it just has nowhere to fall.
	if book, err := notebook.New(notebook.DefaultPath()); err == nil {
		defer book.Catch(notebook.Incident{Op: "atenea.main", Version: buildinfo.Version})
	}
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "atenea: %v\n", err)
		os.Exit(exitCode(err))
	}
}

// errCommissionFailed marks a run that was carried out cleanly and came back
// with a failed verdict. The invocation was not broken -- the report on stdout
// is the whole story, step by step -- but a script has to be able to tell a
// commission that worked from one that did not, and the exit code is the only
// channel it can read without parsing the screen.
var errCommissionFailed = errors.New(
	"the commission failed; the verdict above says which step and why")

// exitCode maps the failure bins onto shell exit codes so a script can tell a
// broken settings file from a provider that is simply down.
func exitCode(err error) int {
	// A verdict is a different axis from a failure bin: the work failed, the
	// invocation did not. It cannot borrow 1, which means a bug.
	if errors.Is(err, errCommissionFailed) {
		return 6
	}
	switch contract.KindOf(err) {
	case contract.FailureInvalidInput:
		return 2
	case contract.FailureNotFound:
		return 3
	case contract.FailureUnavailable, contract.FailureTimeout:
		return 4
	case contract.FailurePermissionDenied, contract.FailureExternalDenied:
		return 5
	default:
		return 1
	}
}

func run(args []string, out io.Writer) error {
	var settingsPath string
	global := flag.NewFlagSet("atenea", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.StringVar(&settingsPath, "config", "", "settings file")
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(out, usage)
			return nil
		}
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}

	rest := global.Args()
	if len(rest) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}

	command, commandArgs := rest[0], rest[1:]
	switch command {
	case "version":
		return cmdVersion(out)
	case "status":
		return cmdStatus(settingsPath, out)
	case "catalog":
		return cmdCatalog(settingsPath, out)
	case "select":
		return cmdSelect(settingsPath, commandArgs, out)
	case "task":
		return cmdTask(settingsPath, commandArgs, out)
	case "ask":
		return cmdAsk(settingsPath, commandArgs, out)
	case "run":
		return cmdRun(settingsPath, out)
	case "incidents":
		return cmdIncidents(settingsPath, commandArgs, out)
	case "config":
		return cmdConfig(settingsPath, commandArgs, out)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	default:
		return contract.Fail(contract.FailureInvalidInput, "unknown command %q", command)
	}
}

func load(settingsPath string) (*core.Core, error) {
	cfg, err := config.Load(settingsPath)
	if err != nil {
		return nil, err
	}
	return core.New(cfg)
}

func cmdVersion(out io.Writer) error {
	fmt.Fprintf(out, "atenea   %s\n", buildinfo.Version)
	fmt.Fprintf(out, "contract %s\n", contract.Current)
	return nil
}

func cmdStatus(settingsPath string, out io.Writer) error {
	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	status := atenea.Status()

	fmt.Fprintf(out, "atenea %s  contract %s  %s\n",
		status.Version, status.Contract, strings.ToUpper(status.Light.String()))
	fmt.Fprintf(out, "settings  %s\n", status.Settings)
	fmt.Fprintf(out, "funnel    %s\n", status.Funnel)
	printIncidentLine(out, status.Incidents)

	agent := status.Orchestrator
	fmt.Fprintf(out, "\norchestrator %s\n", strings.ToUpper(agent.Light.String()))
	fmt.Fprintf(out, "  agent      %s (%s)\n", agent.Agent, agent.Type)
	fmt.Fprintf(out, "  asks for   %s\n", strings.Join(agent.Capabilities, ", "))
	fmt.Fprintf(out, "  context    %s\n", strings.Join(agent.Context, ", "))
	fmt.Fprintf(out, "  runners    %s\n", orDash(strings.Join(agent.Runners, ", ")))
	fmt.Fprintf(out, "  serves     %s\n", orDash(strings.Join(agent.Serves, ", ")))
	if len(agent.Unreachable) > 0 {
		fmt.Fprintf(out, "  no runner  %s\n", strings.Join(agent.Unreachable, ", "))
	}
	fmt.Fprintf(out, "  parallel   %s\n", ceiling(agent.MaxParallel))
	fmt.Fprintf(out, "  runs       %s\n", agent.Checkpoints)

	fmt.Fprintf(out, "\ncapabilities\n")
	for _, capability := range status.Capabilities {
		fmt.Fprintf(out, "  %-24s [%s]\n", capability.ID, strings.Join(capability.Effects, " "))
		if len(capability.Implementations) == 0 {
			fmt.Fprintf(out, "      (no provider registered)\n")
		}
		for _, impl := range capability.Implementations {
			line := fmt.Sprintf("      %-6s %-24s provider=%-18s health=%s",
				impl.Light, impl.ID, impl.Provider, impl.Health.State)
			if impl.Health.Reason != "" {
				line += "  (" + impl.Health.Reason + ")"
			}
			fmt.Fprintln(out, line)
		}
	}

	fmt.Fprintf(out, "\nrepositories\n")
	for _, repo := range status.Repositories {
		fmt.Fprintf(out, "  %-16s %-28s scale=%-8s languages=%s  indexes=%s\n",
			repo.ID, repo.Path, orDash(repo.Scale),
			orDash(strings.Join(repo.Languages, ",")),
			orDash(strings.Join(repo.Indexes, ",")))
	}
	return nil
}

// printIncidentLine is the fourth thing the short screen owes the design,
// after the light, the providers and the work in flight.
//
// It prints nothing when there is nothing, which is the normal state and the
// one that should cost no attention. A screen carrying a permanent "incidents
// 0" trains the eye to skip the line it exists to catch.
func printIncidentLine(out io.Writer, in core.IncidentStatus) {
	if in.Unread == 0 && in.Unreadable == 0 {
		return
	}
	line := fmt.Sprintf("incidents %d unread", in.Unread)
	if !in.Latest.IsZero() {
		line += fmt.Sprintf(", latest %s", in.Latest.Format("2006-01-02 15:04:05"))
	}
	if in.Unreadable > 0 {
		line += fmt.Sprintf(", %d unreadable", in.Unreadable)
	}
	fmt.Fprintf(out, "%s  (atenea incidents)\n", line)
}

// cmdIncidents reads the crash notebook out, and with 'clear' marks it read.
//
// Reading is the default and it changes nothing on disk: the same command run
// twice prints the same thing, and one person looking never alters what the
// next person finds. Marking read is deliberately a separate word, because it
// is the only destructive-looking act in the pair and it should have to be
// typed.
func cmdIncidents(settingsPath string, args []string, out io.Writer) error {
	all := false
	marking := false
	for _, arg := range args {
		switch arg {
		case "clear":
			marking = true
		case "--all":
			all = true
		default:
			return contract.Fail(contract.FailureInvalidInput,
				"incidents takes 'clear' or --all, got %q", arg)
		}
	}
	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	if marking {
		cleared, err := atenea.ClearIncidents()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "marked %d incident(s) read\n", cleared)
		return nil
	}
	book, err := atenea.Incidents()
	if err != nil {
		return err
	}
	show := book.New()
	if all {
		show = book.Incidents
	}
	if len(show) == 0 {
		if len(book.Incidents) > 0 {
			fmt.Fprintf(out, "nothing new; %d incident(s) already read (--all to see them)\n",
				len(book.Incidents))
			return nil
		}
		fmt.Fprintln(out, "the crash notebook is empty")
		return nil
	}
	for i, in := range show {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, in.Line())
		// The stack is why the entry exists, so a read prints it whole. This
		// is the long height of the two the design asks for; the status line
		// is the short one.
		if in.Stack != "" {
			for _, line := range strings.Split(strings.TrimRight(in.Stack, "\n"), "\n") {
				fmt.Fprintf(out, "    %s\n", line)
			}
		}
	}
	if book.Unreadable > 0 {
		fmt.Fprintf(out, "\n%d line(s) could not be read; the notebook was torn mid-entry\n",
			book.Unreadable)
	}
	if !all && book.Unread > 0 {
		fmt.Fprintf(out, "\n%d shown. 'atenea incidents clear' marks them read.\n", book.Unread)
	}
	return nil
}

func cmdCatalog(settingsPath string, out io.Writer) error {
	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	registry := atenea.Registry()
	for _, capability := range registry.Capabilities() {
		fmt.Fprintf(out, "capability %s %s\n", capability.ID, capability.Version)
		fmt.Fprintf(out, "  summary   %s\n", capability.Summary)
		if capability.Semantics != "" {
			fmt.Fprintf(out, "  semantics %s\n", oneLine(capability.Semantics))
		}
		fmt.Fprintf(out, "  inputs\n")
		printFields(out, "    ", capability.Inputs)
		fmt.Fprintf(out, "  outputs\n")
		printFields(out, "    ", capability.Outputs)

		impls, err := registry.ImplementationsFor(capability.ID)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "  implementations\n")
		for _, impl := range impls {
			fmt.Fprintf(out, "    %s (provider %s)\n", impl.ID, impl.Provider)
			fmt.Fprintf(out, "      constraints  languages=%s index=%v scale=%s..%s\n",
				orDash(strings.Join(impl.Constraints.Languages, ",")),
				impl.Constraints.RequiresIndex,
				orDash(impl.Constraints.MinScale.String()),
				orDash(impl.Constraints.MaxScale.String()))
			fmt.Fprintf(out, "      cost         estimated=%s/%dtok measured=%d sample(s)\n",
				impl.Cost.Estimated.Duration, impl.Cost.Estimated.Tokens, impl.Cost.Samples)
			fmt.Fprintf(out, "      health       %s\n", impl.Health.State)
		}
		fmt.Fprintln(out)
	}
	return nil
}

func printFields(out io.Writer, indent string, fields []contract.Field) {
	if len(fields) == 0 {
		fmt.Fprintf(out, "%s(none)\n", indent)
		return
	}
	for _, field := range fields {
		required := "optional"
		if field.Required {
			required = "required"
		}
		fmt.Fprintf(out, "%s%-16s %-12s %-8s %s\n", indent, field.Name, field.Type, required, field.Summary)
		if len(field.Fields) > 0 {
			printFields(out, indent+"  ", field.Fields)
		}
	}
}

func cmdSelect(settingsPath string, args []string, out io.Writer) error {
	// The capability comes first and the flags after it: Go's flag package
	// stops at the first positional argument, so accepting them in any order
	// would mean hand-rolling a parser for no gain.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return contract.Fail(contract.FailureInvalidInput,
			"select needs a capability first, e.g. atenea select code.search --repo current")
	}
	capabilityID, args := args[0], args[1:]

	var repository string
	flags := flag.NewFlagSet("select", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&repository, "repo", "", "repository id (defaults to the only one registered)")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"unexpected argument %q after the capability", flags.Arg(0))
	}

	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	if repository == "" {
		repos := atenea.Registry().Repositories()
		if len(repos) != 1 {
			return contract.Fail(contract.FailureInvalidInput,
				"--repo is required: %d repositories are registered", len(repos))
		}
		repository = repos[0].ID
	}

	decision, selectErr := atenea.Select(capabilityID, repository)
	printDecision(out, decision, selectErr)
	return selectErr
}

func printDecision(out io.Writer, decision selector.Decision, selectErr error) {
	if decision.Capability == "" {
		return
	}
	fmt.Fprintf(out, "capability  %s\n", decision.Capability)
	fmt.Fprintf(out, "repository  %s\n", decision.Repository)
	if selectErr == nil {
		fmt.Fprintf(out, "chosen      %s  (%s)\n", decision.Chosen.ID, decision.Reason)
	}
	for _, notice := range decision.Notices {
		fmt.Fprintf(out, "notice      %s\n", notice)
	}
	if len(decision.Stages) == 0 {
		return
	}
	fmt.Fprintf(out, "\nfunnel\n")
	for _, stage := range decision.Stages {
		fmt.Fprintf(out, "  %-12s %d in -> %d out: %s\n",
			stage.Name, len(stage.In), len(stage.Out), orDash(strings.Join(stage.Out, ", ")))
		for _, dropped := range stage.Dropped {
			fmt.Fprintf(out, "      dropped %s: %s\n", dropped.Implementation, dropped.Reason)
		}
	}
}

// cmdTask hands a commission to the orchestrator and prints the answer at two
// heights: the summary always, the trace only when asked for. Keeping the full
// record and showing a short one is the same idea as the status screen.
func cmdTask(settingsPath string, args []string, out io.Writer) error {
	// The commission comes first and the flags after it. Go's flag package
	// stops at the first positional argument, so accepting them in any order
	// would mean hand-rolling the parser for no gain.
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return contract.Fail(contract.FailureInvalidInput,
			`task needs the commission first, e.g. atenea task "find every TODO" --trace`)
	}
	text, args := strings.TrimSpace(args[0]), args[1:]

	var repositories repoList
	var trace bool
	var budget float64
	flags := flag.NewFlagSet("task", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Var(&repositories, "repo", "repository to act on; repeat for several (default: all)")
	flags.Float64Var(&budget, "budget", 0, "what this commission may spend in usd (default: the settings file)")
	flags.BoolVar(&trace, "trace", false, "print the plan, the funnel and every review")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"unexpected argument %q after the commission; quote it if it is one commission",
			flags.Arg(0))
	}

	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, runErr := atenea.Do(ctx, orchestrator.Task{
		Text: text, Repositories: repositories, BudgetUSD: budget,
	})
	if result != nil {
		printResult(out, result, trace)
	}
	if runErr != nil {
		// A run that could not be carried out at all is the more specific
		// answer, and it already carries the bin that says why.
		return runErr
	}
	if result != nil && result.Verdict != contract.VerdictOK {
		// Only "ok" leaves quietly. Anything else -- today "failed", tomorrow
		// whatever else review learns to say -- has to reach the shell.
		return errCommissionFailed
	}
	return nil
}

// cmdAsk dispatches one capability. It is the atomic base a workflow is built
// out of, and the only way a caller who already has a position -- an editor,
// a client with a cursor -- can hand it over: exploring finds text, and a text
// hit is not a cursor.
func cmdAsk(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return contract.Fail(contract.FailureInvalidInput,
			`ask needs the capability first, e.g. atenea ask symbol.definition --repo current --set file=main.go --set line=12 --set column=6`)
	}
	capabilityID, args := strings.TrimSpace(args[0]), args[1:]

	var fields fieldList
	var repository string
	var trace bool
	var budget float64
	flags := flag.NewFlagSet("ask", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&repository, "repo", "", "repository to ask about (required when several are registered)")
	flags.Var(&fields, "set", "payload field as name=value; repeat for several")
	flags.Float64Var(&budget, "budget", 0, "what this question may spend in usd (default: the settings file)")
	flags.BoolVar(&trace, "trace", false, "print the plan, the funnel and every review")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"unexpected argument %q after the capability", flags.Arg(0))
	}

	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	capability, err := atenea.Registry().Capability(capabilityID)
	if err != nil {
		return err
	}
	// The capability's own declaration is what types the payload. A parser of
	// its own here would be a second schema to keep in step with the first,
	// and it would be wrong the moment a capability gains a field.
	payload, err := fields.payload(capability)
	if err != nil {
		return err
	}
	if repository == "" {
		repos := atenea.Registry().Repositories()
		if len(repos) != 1 {
			return contract.Fail(contract.FailureInvalidInput,
				"--repo is required: %d repositories are registered", len(repos))
		}
		repository = repos[0].ID
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, runErr := atenea.Ask(ctx, orchestrator.Question{
		Capability: capabilityID,
		Repository: repository,
		Payload:    payload,
		BudgetUSD:  budget,
	})
	if result != nil {
		printResult(out, result, trace)
		printAnswer(out, result)
	}
	if runErr != nil {
		return runErr
	}
	if result != nil && result.Verdict != contract.VerdictOK {
		return errCommissionFailed
	}
	return nil
}

// printAnswer shows what came back. A commission reports how many matches it
// found because that is all a caller can act on across several repositories;
// one capability against one repository has an actual answer, and hiding it
// behind a run receipt would make the verb useless.
func printAnswer(out io.Writer, result *orchestrator.Result) {
	if len(result.Steps) != 1 {
		return
	}
	step := result.Steps[0]
	if step.Review.Parent != contract.VerdictOK {
		return
	}
	fmt.Fprintf(out, "\nanswer\n")
	for _, name := range slices.Sorted(maps.Keys(step.Outcome.Result)) {
		printValue(out, "  ", name, step.Outcome.Result[name])
	}
}

func printValue(out io.Writer, indent, name string, value any) {
	switch typed := value.(type) {
	case map[string]any:
		fmt.Fprintf(out, "%s%s\n", indent, name)
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			printValue(out, indent+"  ", key, typed[key])
		}
	case []any:
		fmt.Fprintf(out, "%s%s (%d)\n", indent, name, len(typed))
		for i, item := range typed {
			printValue(out, indent+"  ", strconv.Itoa(i+1), item)
		}
	default:
		fmt.Fprintf(out, "%s%-8s %v\n", indent, name, value)
	}
}

// fieldList collects a repeated --set flag and types it against a capability.
type fieldList []string

func (f *fieldList) String() string     { return strings.Join(*f, ",") }
func (f *fieldList) Set(v string) error { *f = append(*f, v); return nil }

func (f fieldList) payload(capability contract.Capability) (map[string]any, error) {
	declared := make(map[string]contract.Field, len(capability.Inputs))
	for _, field := range capability.Inputs {
		declared[field.Name] = field
	}
	out := make(map[string]any, len(f))
	for _, entry := range f {
		name, raw, found := strings.Cut(entry, "=")
		if !found {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"--set %q is not name=value", entry)
		}
		name = strings.TrimSpace(name)
		field, known := declared[name]
		if !known {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"%s has no input named %q", capability.ID, name)
		}
		value, err := coerce(field, raw)
		if err != nil {
			return nil, err
		}
		// A repeated name builds a list rather than overwriting: that is the
		// only way --set scope=a --set scope=b can mean both.
		if previous, seen := out[name]; seen {
			if field.Type != contract.TypeStringList {
				return nil, contract.Fail(contract.FailureInvalidInput,
					"%s: %s was given twice and is not a list", capability.ID, name)
			}
			out[name] = append(previous.([]any), value.([]any)...)
			continue
		}
		out[name] = value
	}
	return out, nil
}

func coerce(field contract.Field, raw string) (any, error) {
	switch field.Type {
	case contract.TypeString:
		return raw, nil
	case contract.TypeInt:
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"%s must be a whole number, got %q", field.Name, raw)
		}
		return n, nil
	case contract.TypeBool:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"%s must be true or false, got %q", field.Name, raw)
		}
		return b, nil
	case contract.TypeStringList:
		return []any{raw}, nil
	default:
		// Records are a shape a shell cannot express without becoming a JSON
		// parser. Refusing is honest; a half-parser would be worse.
		return nil, contract.Fail(contract.FailureInvalidInput,
			"%s is a %s, which --set cannot express", field.Name, field.Type)
	}
}

// repoList collects a repeated --repo flag.
type repoList []string

func (r *repoList) String() string     { return strings.Join(*r, ",") }
func (r *repoList) Set(v string) error { *r = append(*r, v); return nil }

func printResult(out io.Writer, result *orchestrator.Result, trace bool) {
	fmt.Fprintf(out, "run       %s\n", result.RunID)
	fmt.Fprintf(out, "task      %s\n", result.Task)
	fmt.Fprintf(out, "verdict   %s\n", result.Verdict)
	// Matches are counted out of the split-up commission. A direct ask has an
	// answer rather than a tally, and printing a zero nobody measured would
	// read as "found nothing" instead of "did not count".
	if slices.ContainsFunc(result.Phases, func(p orchestrator.Phase) bool {
		return p.Name == orchestrator.PhaseWork
	}) {
		fmt.Fprintf(out, "matches   %d\n", result.Matches)
	}
	fmt.Fprintf(out, "spent     %s over %d step(s)\n",
		result.Spent.Duration.Round(time.Millisecond), len(result.Steps))
	for _, phase := range result.Phases {
		fmt.Fprintf(out, "  %-8s %d step(s), %s\n",
			phase.Name, phase.Steps, phase.Spent.Duration.Round(time.Millisecond))
	}
	// Money is only mentioned when money changed hands. A "$0.0000" line on
	// every run of a free tool would train the eye to skip the one line that
	// matters on the run where it is not zero.
	if result.SpentUSD > 0 {
		fmt.Fprintf(out, "charged   $%.4f\n", result.SpentUSD)
	}

	if len(result.Discoveries) > 0 {
		fmt.Fprintf(out, "\ndiscovered\n")
		for _, found := range result.Discoveries {
			fmt.Fprintf(out, "  [%s] %s\n", found.Level, found.Note)
		}
	}

	if !trace {
		fmt.Fprintf(out, "\nrun with --trace for the plan, the funnel and every review\n")
		return
	}

	fmt.Fprintf(out, "\nplan\n")
	waves, err := result.Plan.Layers()
	if err != nil {
		fmt.Fprintf(out, "  (unplanned: %v)\n", err)
	}
	for i, wave := range waves {
		names := make([]string, 0, len(wave))
		for _, step := range wave {
			names = append(names, step.ID)
		}
		fmt.Fprintf(out, "  wave %d  %s\n", i+1, strings.Join(names, ", "))
	}

	fmt.Fprintf(out, "\nsteps\n")
	for _, step := range result.Steps {
		fmt.Fprintf(out, "  %-20s %-8s %-24s %s\n",
			step.Step.ID, step.Phase, orDash(step.Decision.Chosen.ID),
			step.Spent.Duration.Round(time.Millisecond))
		// The charge sits on its own line, and only on the steps that had
		// one. Money is broken out per step rather than per phase because a
		// phase tally says how much went, and this says who to go and look
		// at. Phases stay a duration count.
		if step.Outcome.SpentUSD > 0 {
			fmt.Fprintf(out, "      charged  $%.4f\n", step.Outcome.SpentUSD)
		}
		fmt.Fprintf(out, "      review   child=%s parent=%s (%s)\n",
			step.Review.Child, step.Review.Parent, step.Review.Reason)
		if step.Review.Disagreed {
			fmt.Fprintf(out, "      disputed %s\n", step.Review.Reply)
		}
		if step.Failure != "" {
			fmt.Fprintf(out, "      failed   %s\n", step.Failure)
		}
		if scope, ok := step.Step.Payload["scope"].([]string); ok && len(scope) > 0 {
			fmt.Fprintf(out, "      scope    %s\n", strings.Join(scope, ", "))
		}
		for _, stage := range step.Decision.Stages {
			for _, dropped := range stage.Dropped {
				fmt.Fprintf(out, "      dropped  %s: %s\n", dropped.Implementation, dropped.Reason)
			}
		}
	}
}

func cmdRun(settingsPath string, out io.Writer) error {
	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	status := atenea.Status()
	fmt.Fprintf(out, "atenea %s ready  contract %s  %s\n",
		status.Version, status.Contract, strings.ToUpper(status.Light.String()))
	fmt.Fprintf(out, "settings %s\n", status.Settings)
	fmt.Fprintf(out, "waiting for work; press Ctrl-C to stop\n")

	if err := atenea.Run(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "stopped cleanly\n")
	return nil
}

func cmdConfig(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return contract.Fail(contract.FailureInvalidInput, "config needs a subcommand: init or path")
	}
	switch args[0] {
	case "path":
		fmt.Fprintln(out, config.ResolvePath(settingsPath))
		return nil
	case "init":
		var force bool
		flags := flag.NewFlagSet("config init", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		flags.BoolVar(&force, "force", false, "overwrite an existing settings file")
		if err := flags.Parse(args[1:]); err != nil {
			return contract.Fail(contract.FailureInvalidInput, "%v", err)
		}
		path := config.ResolvePath(settingsPath)
		if err := config.WriteDefault(path, force); err != nil {
			return err
		}
		fmt.Fprintf(out, "wrote %s\n", path)
		return nil
	default:
		return contract.Fail(contract.FailureInvalidInput, "unknown config subcommand %q", args[0])
	}
}

// ceiling renders a limit where zero means there is none.
func ceiling(value int) string {
	if value <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d step(s) at a time", value)
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
