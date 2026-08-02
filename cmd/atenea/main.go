// Command atenea is the entry point of the Atenea orchestration core.
//
// Atenea lives outside the CLIs it serves, so this binary is what gets started
// on the machine. Until the first adapter exists there is nothing to serve, so
// `run` is the lifecycle and the rest of the commands are the operator's window
// into the catalog and the selector.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
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
  task "TEXT"            Hand a commission to the orchestrator
  catalog                List capabilities, providers and repositories in full
  run                    Run as a service until interrupted
  config init            Write the built-in settings file to disk
  config path            Print where settings are read from
  version                Print the product and contract versions

Global flags:
  --config PATH          Settings file. Falls back to $ATENEA_CONFIG, then
                         $XDG_CONFIG_HOME/atenea/atenea.toml, then the built-in
                         defaults.
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "atenea: %v\n", err)
		os.Exit(exitCode(err))
	}
}

// exitCode maps the failure bins onto shell exit codes so a script can tell a
// broken settings file from a provider that is simply down.
func exitCode(err error) int {
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
	case "run":
		return cmdRun(settingsPath, out)
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

	agent := status.Orchestrator
	fmt.Fprintf(out, "\norchestrator %s\n", strings.ToUpper(agent.Light.String()))
	fmt.Fprintf(out, "  agent      %s (%s)\n", agent.Agent, agent.Type)
	fmt.Fprintf(out, "  asks for   %s\n", strings.Join(agent.Capabilities, ", "))
	fmt.Fprintf(out, "  context    %s\n", strings.Join(agent.Context, ", "))
	fmt.Fprintf(out, "  runner     %s\n", orDash(agent.Runner))
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
	flags := flag.NewFlagSet("task", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Var(&repositories, "repo", "repository to act on; repeat for several (default: all)")
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

	result, runErr := atenea.Do(ctx, orchestrator.Task{Text: text, Repositories: repositories})
	if result != nil {
		printResult(out, result, trace)
	}
	return runErr
}

// repoList collects a repeated --repo flag.
type repoList []string

func (r *repoList) String() string     { return strings.Join(*r, ",") }
func (r *repoList) Set(v string) error { *r = append(*r, v); return nil }

func printResult(out io.Writer, result *orchestrator.Result, trace bool) {
	fmt.Fprintf(out, "run       %s\n", result.RunID)
	fmt.Fprintf(out, "task      %s\n", result.Task)
	fmt.Fprintf(out, "verdict   %s\n", result.Verdict)
	fmt.Fprintf(out, "matches   %d\n", result.Matches)
	fmt.Fprintf(out, "spent     %s over %d step(s)\n",
		result.Spent.Duration.Round(time.Millisecond), len(result.Steps))
	for _, phase := range result.Phases {
		fmt.Fprintf(out, "  %-8s %d step(s), %s\n",
			phase.Name, phase.Steps, phase.Spent.Duration.Round(time.Millisecond))
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
