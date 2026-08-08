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
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/internal/wrap"
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
  resume RUN_ID          Pick an interrupted or failed commission back up;
                         --budget USD replaces what remains of the grant.
                         resume --list shows every run still worth it
  catalog                List capabilities, providers and repositories in full
  detect [--repo ID]     Ask attached providers whether they already hold a
                         ready index; corrects indexed_by in memory when they do
  run                    Run as a service until interrupted
  service install        Install atenea as a background service that starts
                         with the system; 'uninstall' undoes it, 'status'
                         says where it stands
  incidents              Read the crash notebook; add 'clear' to mark it read
  metrics                What the base measured, per capability and provider;
                         'clear' empties it, narrowed by --capability,
                         --implementation or --repository, or --all for the lot
  config init            Write the built-in settings file to disk
  config path            Print where settings are read from
  wrap CLIENT [args]     Launch a client with MCP servers Atenea checked a
                         moment ago; dead ones are named and left out
  version                Print the product and contract versions

Global flags:
  --config PATH          Settings file. Falls back to $ATENEA_CONFIG, then
                         $XDG_CONFIG_HOME/atenea/atenea.toml, then the built-in
                         defaults.
`

// commandHelp holds one usage message per subcommand, shown for
// "atenea <command> --help" or "-h" wherever it appears in that command's
// own arguments. Centralizing the check ahead of dispatch is what makes it
// reach every command uniformly: several read a positional argument before
// their flags even see anything -- "atenea ask -h" would otherwise be
// swallowed as the capability id, not recognized as a request for help.
var commandHelp = map[string]string{
	"version": `Usage: atenea version

Print the product and contract versions.
`,
	"status": `Usage: atenea status

Short health screen: one light for Atenea, one per provider it talks to.
`,
	"catalog": `Usage: atenea catalog

List every capability, its providers, and every registered repository.
`,
	"detect": `Usage: atenea detect [flags]

Ask every attached provider that can tell whether it already holds a ready
index, and correct indexed_by in memory with whatever it finds. Read-only
about the repository -- it asks, it never builds; atenea ask repository.index
builds one.

Flags:
  --repo ID   repository to check (default: every repository registered)
  --json      print the result as json instead of prose
`,
	"select": `Usage: atenea select CAPABILITY [flags]

Ask the funnel who would answer a capability, without spending anything.

Flags:
  --repo ID   repository id (defaults to the only one registered)
`,
	"task": `Usage: atenea task "TEXT" [flags]

Hand a commission to the orchestrator in the user's own words.

Flags:
  --repo ID       repository to act on; repeat for several (default: all)
  --allow EFFECT  effect beyond reading to grant this commission; repeat for
                  several (default: none)
  --budget USD    what this commission may spend (default: the settings file)
  --trace         print the plan, the funnel and every review
  --json          print the result as json instead of prose (always
                  complete, ignores --trace)
`,
	"ask": `Usage: atenea ask CAPABILITY [flags]

Dispatch one capability against one repository.

Flags:
  --repo ID       repository to ask about (required when several are
                  registered)
  --set NAME=VAL  payload field; repeat for several
  --allow EFFECT  effect beyond reading to grant this question; repeat for
                  several (default: none)
  --budget USD    what this question may spend (default: the settings file)
  --trace         print the plan, the funnel and every review
  --json          print the result as json instead of prose (always
                  complete, ignores --trace)
`,
	"resume": `Usage: atenea resume RUN_ID [flags]
       atenea resume --list

Pick an interrupted or failed commission back up. --list shows every run
still worth resuming instead of resuming any of them.

Flags:
  --budget USD    replace what remains of the grant instead of adding to it
                  (default: what is left)
  --allow EFFECT  effect to add beyond what the commission already carries;
                  repeat for several (default: none)
  --trace         print the plan, the funnel and every review
  --json          print the result as json instead of prose (always
                  complete, ignores --trace)
`,
	"run": `Usage: atenea run

Run as a service until interrupted (Ctrl-C or SIGTERM).
`,
	"service": `Usage: atenea service install
       atenea service uninstall
       atenea service status

Install atenea as a background service that starts with the system;
'uninstall' undoes it, 'status' says where it stands.
`,
	"incidents": `Usage: atenea incidents [clear] [--all]

Read the crash notebook. With no arguments, shows what has not been read
yet. --all shows the whole notebook, read or not. 'clear' marks the shown
entries read.
`,
	"metrics": `Usage: atenea metrics [clear] [flags]

What the base measured, per capability and provider. 'clear' empties it
instead of reading it.

Flags:
  --capability ID       narrow to one capability
  --implementation ID   narrow to one implementation
  --repository ID       narrow to one repository
  --all                 with clear: confirm emptying the whole base
`,
	"config": `Usage: atenea config init [--force]
       atenea config path

'init' writes the built-in settings file to disk; --force overwrites one
that already exists. 'path' prints where settings are read from.
`,
	"wrap": `Usage: atenea wrap CLIENT [client args...]

Launches CLIENT with MCP configuration Atenea generated from the
[[mcp_server]] blocks in the settings file, having checked every one of
them a moment before.

A server that completes an MCP handshake is declared to the client. One
that does not is named, with the reason, and left out of the payload --
left out rather than switched off, so the client keeps whatever it already
declares under that name.

The check is one handshake, and that is the whole of what "declared" means.
It proves a server is reachable and speaking MCP; it does not prove any of
its tools work. A server can answer initialize perfectly and fail on every
call after it, so an all-green report is a floor, not a warranty.

Nothing is written to disk. The configuration lives in one environment
variable for the lifetime of the child, so a client launched without wrap
is a client with exactly the configuration it had before. There is no
unwrap because there is nothing to undo.

Arguments after CLIENT are passed through untouched.

Supported clients: opencode
`,
}

// helpRequested reports whether -h or --help appears anywhere in args. Most
// subcommands read a positional argument before their own flags, so relying
// on flag.FlagSet's own -h/--help handling would only catch part of
// "atenea <command> --help"; scanning the whole slice catches all of it,
// regardless of where the flag landed.
func helpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

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
		defer book.Catch(notebook.Incident{Op: "atenea.main", Version: buildinfo.Full()})
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
	case contract.FailureCanceled:
		// 128 + SIGINT, which is the number a shell reports for ctrl-c on its
		// own. A script that retries on 4 must not retry this one: nothing is
		// wrong, somebody asked for it to stop.
		return 130
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
	if help, ok := commandHelp[command]; ok && helpRequested(commandArgs) {
		fmt.Fprint(out, help)
		return nil
	}
	switch command {
	case "version":
		return cmdVersion(out)
	case "status":
		return cmdStatus(settingsPath, out)
	case "catalog":
		return cmdCatalog(settingsPath, out)
	case "detect":
		return cmdDetect(settingsPath, commandArgs, out)
	case "select":
		return cmdSelect(settingsPath, commandArgs, out)
	case "task":
		return cmdTask(settingsPath, commandArgs, out)
	case "ask":
		return cmdAsk(settingsPath, commandArgs, out)
	case "resume":
		return cmdResume(settingsPath, commandArgs, out)
	case "run":
		return cmdRun(settingsPath, out)
	case "service":
		return cmdService(settingsPath, commandArgs, out)
	case "incidents":
		return cmdIncidents(settingsPath, commandArgs, out)
	case "metrics":
		return cmdMetrics(settingsPath, commandArgs, out)
	case "config":
		return cmdConfig(settingsPath, commandArgs, out)
	case "wrap":
		return cmdWrap(settingsPath, commandArgs, out)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	default:
		return contract.Fail(contract.FailureInvalidInput, "unknown command %q", command)
	}
}

// load reads the settings file and builds a Core for a one-shot subcommand.
// Every caller must eventually stop what it built: a command that only reads
// status today might still launch a managed process to answer honestly --
// Serena on first use, if the settings file opted it in -- and a bare return
// would leak it as an orphan the moment this process exits. defer Shutdown
// right after checking the error, the same as any other acquired resource.
//
// This is the door for everything except the service, and it is separate from
// loadService rather than a flag on one function so that a subcommand added
// later performs no upkeep unless somebody writes down that it should.
func load(settingsPath string) (*core.Core, error) {
	return build(settingsPath, core.Command)
}

// loadService builds the Core for `atenea run`: the one process that sweeps
// receipts and ticks the clock. Nothing else may use this.
func loadService(settingsPath string) (*core.Core, error) {
	return build(settingsPath, core.Service)
}

func build(settingsPath string, role core.Role) (*core.Core, error) {
	cfg, err := config.Load(settingsPath)
	if err != nil {
		return nil, err
	}
	return core.New(cfg, role)
}

func cmdVersion(out io.Writer) error {
	fmt.Fprintf(out, "atenea   %s\n", buildinfo.Full())
	fmt.Fprintf(out, "contract %s\n", contract.Current)
	return nil
}

func cmdStatus(settingsPath string, out io.Writer) error {
	cfg, err := config.Load(settingsPath)
	if err != nil {
		return err
	}
	// Ask the running service rather than working the screen out from disk.
	//
	// Not an optimization. Half of what is printed below is only true of the
	// process that maintains it -- the uptime, the chats open right now, what
	// the clock has actually run -- and a command maintains none of it. Every
	// Chats table this CLI ever printed was empty, and not because nobody was
	// connected.
	//
	// Only when the service is answering about the same file this command was
	// asked about. Naming a file asks a different question -- "what would this
	// file give me?" -- and a service running a different one is not the
	// answer to it. Comparing the resolved sources rather than the arguments
	// is what makes `--config` on the live file behave like plain `status`
	// instead of quietly falling back to the poorer screen.
	if status, ok := core.Asked(); ok && status.Settings == cfg.Source {
		return printStatus(out, status)
	}
	atenea, err := core.New(cfg, core.Command)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()
	return printStatus(out, atenea.Status())
}

func printStatus(out io.Writer, status core.Status) error {
	fmt.Fprintf(out, "atenea %s  contract %s  %s\n",
		status.Version, status.Contract, strings.ToUpper(status.Light.String()))
	fmt.Fprintf(out, "settings  %s\n", status.Settings)
	// Whose screen this is. It used to mean "the process printing it", and now
	// it means the process the numbers came FROM: a command that reached the
	// service prints the service's, and a command that found nobody prints its
	// own. That is the same line doing the same job -- a reader who sees
	// `service` is looking at a clock that is really ticking, chats that are
	// really open and a `recovered` line that really swept something, and a
	// reader who sees `command` is looking at what could be worked out from
	// disk by a process that maintains none of it.
	fmt.Fprintf(out, "process   %s\n", status.Role)
	if len(status.Missing) > 0 {
		// Beside the settings line, because that file is the thing to edit.
		// Not a light and not an incident: nothing is broken yet, and the
		// symptom when it breaks -- a funnel with one candidate and no
		// fallback -- reads as bad luck unless this line was seen first.
		fmt.Fprintf(out, "catalog   %d shipped implementation(s) this settings file does not declare: %s\n",
			len(status.Missing), strings.Join(status.Missing, ", "))
	}
	fmt.Fprintf(out, "funnel    %s\n", status.Funnel)
	printIncidentLine(out, status.Incidents)
	if summary := status.Recovered.Summary(); summary != "" {
		fmt.Fprintf(out, "recovered %s\n", summary)
	}

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

	printBackground(out, status)
	printProcesses(out, status)

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
			if impl.Health.Raw != "" {
				fmt.Fprintf(out, "             raw: %s\n", clip(oneLine(impl.Health.Raw)))
			}
		}
	}

	fmt.Fprintf(out, "\nrepositories\n")
	for _, repo := range status.Repositories {
		fmt.Fprintf(out, "  %-16s %-28s scale=%-8s vcs=%-8s languages=%s  indexes=%s",
			repo.ID, repo.Path, orDash(repo.Scale), orDash(repo.VCS),
			orDash(strings.Join(repo.Languages, ",")),
			orDash(strings.Join(repo.Indexes, ",")))
		if repo.SerenaEndpoint != "" {
			fmt.Fprintf(out, "  serena=%s", repo.SerenaEndpoint)
		}
		fmt.Fprintln(out)
	}
	return nil
}

// printBackground is Atenea's own house, at the same height as the orchestrator:
// the rhythms that keep the history in shape, and the copies that protect it.
//
// Every fact here has to be true no matter which process the screen came from,
// and this printer now serves two: a service's own view, and a command's view
// when no service answered. That still rules out the running tally each lane
// keeps in memory -- it is real in the first case and meaningless in the
// second, and a number that means two different things depending on a line
// further up is worse than a number nobody prints.
//
// What is left is the pair that answers the question either way -- the rhythms,
// which come from the settings file and are the same everywhere, and the copies
// on disk, which are the same for everybody looking. A lane that fails reaches
// the reader through the crash notebook on the line above.
func printBackground(out io.Writer, status core.Status) {
	fmt.Fprintf(out, "\nbackground\n")
	rhythms := make([]string, 0, len(status.Maintenance))
	for _, lane := range status.Maintenance {
		rhythms = append(rhythms, fmt.Sprintf("%s %s", lane.Name, rhythm(lane.Every)))
	}
	fmt.Fprintf(out, "  %-12s %s\n", "rhythms", orDash(strings.Join(rhythms, ", ")))

	copies := status.Backups
	if !copies.Enabled {
		fmt.Fprintf(out, "  %-12s off\n", "copies")
		return
	}
	line := fmt.Sprintf("  %-12s %d of %d kept in %s", "copies", copies.Count, copies.Keep, copies.Dir)
	switch {
	case copies.Failure != "":
		line += "  FAILED: " + copies.Failure
	case copies.Count == 0:
		// Not a fault on a machine that has only just started, and not a
		// silence either: the one number that matters here is missing.
		line += "  (none taken yet)"
	default:
		line += fmt.Sprintf(", newest %s", copies.Latest.Local().Format("2006-01-02 15:04"))
		if copies.Stale {
			line += "  STALE"
		}
	}
	fmt.Fprintln(out, line)
}

// printProcesses is the section for whatever Atenea itself launched and is
// watching. It stays out of the way entirely when nothing is managed, which
// is the common case: a header over an empty list would be noise on every
// single status call for a setup that never opted in.
func printProcesses(out io.Writer, status core.Status) {
	if len(status.Processes) == 0 {
		return
	}
	fmt.Fprintf(out, "\nprocesses\n")
	for _, p := range status.Processes {
		pid := "-"
		if p.PID != 0 {
			pid = strconv.Itoa(p.PID)
		}
		up := "-"
		if !p.Started.IsZero() {
			up = time.Since(p.Started).Truncate(time.Second).String()
		}
		line := fmt.Sprintf("  %-6s %-10s state=%-11s endpoint=%-34s pid=%-8s up=%-10s restarts=%d",
			p.Light, p.ID, p.State, p.Endpoint, pid, up, p.Restarts)
		if p.LastReason != "" {
			line += "  (" + p.LastReason + ")"
		}
		fmt.Fprintln(out, line)
	}
}

// rhythm drops the zero tail Go prints on round durations: "6h", not "6h0m0s".
// Trimming the text would be the obvious way and the wrong one -- it turns
// "30s" into "3".
func rhythm(every time.Duration) string {
	switch {
	case every >= time.Hour && every%time.Hour == 0:
		return fmt.Sprintf("%dh", every/time.Hour)
	case every >= time.Minute && every%time.Minute == 0:
		return fmt.Sprintf("%dm", every/time.Minute)
	default:
		return every.String()
	}
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
	defer func() { _ = atenea.Shutdown() }()
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

// cmdMetrics prints the measurement base, and with 'clear' empties it.
//
// Reading is the default and touches nothing, the same pairing as the crash
// notebook: emptying is the destructive half and has to be typed.
//
// The base needs a way in because it is the one thing in Atenea that decides
// behavior and cannot be edited. The catalog is a settings file anybody can
// open; health repairs itself the moment a provider answers again. A baseline
// is neither -- it is true by construction, and an afternoon of failures
// stays true long after the machine it describes has been fixed.
func cmdMetrics(settingsPath string, args []string, out io.Writer) error {
	clearing := false
	if len(args) > 0 && args[0] == "clear" {
		clearing, args = true, args[1:]
	}
	var filter metrics.Filter
	flags := flag.NewFlagSet("metrics", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&filter.Capability, "capability", "", "narrow to one capability")
	flags.StringVar(&filter.Implementation, "implementation", "", "narrow to one implementation")
	flags.StringVar(&filter.Repository, "repository", "", "narrow to one repository")
	all := flags.Bool("all", false, "with clear: confirm emptying the whole base")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() > 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"metrics takes 'clear' and flags, got %q", flags.Arg(0))
	}
	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()
	if clearing {
		return clearMetrics(atenea, filter, *all, out)
	}
	if *all {
		return contract.Fail(contract.FailureInvalidInput,
			"--all belongs to 'metrics clear'; reading already shows everything")
	}
	return printMetrics(atenea, filter, out)
}

// clearMetrics empties the base. Emptying all of it needs --all on top of the
// word 'clear': a narrowing flag is itself a statement of intent, but a bare
// 'clear' is as likely to be a misunderstanding as a decision, and this is the
// one command in Atenea that destroys something nothing else can rebuild.
func clearMetrics(atenea *core.Core, filter metrics.Filter, all bool, out io.Writer) error {
	if filter.Empty() && !all {
		return contract.Fail(contract.FailureInvalidInput,
			"clearing the whole base needs --all; "+
				"--capability, --implementation or --repository clear only part of it")
	}
	cleared, err := atenea.ClearMeasurements(filter)
	if err != nil {
		return err
	}
	if cleared.Total() == 0 {
		fmt.Fprintf(out, "nothing to clear for %s\n", filter)
		return nil
	}
	fmt.Fprintf(out, "cleared %s: %d attempt(s) and %d folded bucket(s)\n",
		filter, cleared.Attempts, cleared.Rollups)
	return nil
}

func printMetrics(atenea *core.Core, filter metrics.Filter, out io.Writer) error {
	// Everything, not a window. The store's own retention decides how far back
	// there is anything to see, and a command that quietly hid the older half
	// would be the second place a number can go missing.
	rows, err := atenea.Measurements(time.Time{})
	if err != nil {
		return err
	}
	rows = slices.DeleteFunc(rows, func(r metrics.Row) bool { return !matches(filter, r) })
	if len(rows) == 0 {
		fmt.Fprintf(out, "the measurement base holds nothing for %s\n", filter)
		return nil
	}
	fmt.Fprintf(out, "%-18s %-22s %-12s %8s %8s %8s %10s %10s\n",
		"capability", "implementation", "repository", "tries", "failed", "priced", "each", "worst")
	for _, r := range rows {
		// The three counts sit next to each other because the gap between them
		// is the diagnosis. Attempts with no priced calls is a provider with a
		// long record and no cost at all -- it ranks on the estimate somebody
		// typed, however many times it has run.
		each := "-"
		if r.Successes > 0 {
			each = r.Mean.String()
		}
		fmt.Fprintf(out, "%-18s %-22s %-12s %8d %8d %8d %10s %10s\n",
			r.Capability, r.Implementation, r.Repository,
			r.Attempts, r.Failures, r.Successes, each, r.Slowest)
	}
	fmt.Fprintf(out, "\n'each' is the average of the calls that WORKED. "+
		"A failure is counted, never priced.\n")
	// The strays get their own line rather than a column, because the column
	// would be empty for every provider that confines its own search -- which
	// is all of them but one. It is recorded and never scored: a provider that
	// reports its overreach honestly must not rank below one that hides it, so
	// this is evidence for whoever is deciding where to point a capability,
	// and nothing the funnel reads.
	for _, r := range rows {
		if r.Wandered == 0 {
			continue
		}
		fmt.Fprintf(out, "%s returned %d result(s) outside the scope it was asked for on %s; "+
			"dropped before answering, recorded, never scored.\n",
			r.Implementation, r.Wandered, r.Repository)
	}
	return nil
}

func matches(f metrics.Filter, r metrics.Row) bool {
	return (f.Capability == "" || f.Capability == r.Capability) &&
		(f.Implementation == "" || f.Implementation == r.Implementation) &&
		(f.Repository == "" || f.Repository == r.Repository)
}

func cmdCatalog(settingsPath string, out io.Writer) error {
	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()
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
			fmt.Fprintf(out, "      constraints  languages=%s index=%v vcs=%v scale=%s..%s\n",
				orDash(strings.Join(impl.Constraints.Languages, ",")),
				impl.Constraints.RequiresIndex,
				impl.Constraints.RequiresVCS,
				orDash(impl.Constraints.MinScale.String()),
				orDash(impl.Constraints.MaxScale.String()))
			fmt.Fprintf(out, "      cost         estimated=%s/%dtok measured=%d sample(s)\n",
				impl.Cost.Estimated.Duration, impl.Cost.Estimated.Tokens, impl.Cost.Samples)
			fmt.Fprintf(out, "      health       %s\n", impl.Health.State)
			fmt.Fprintf(out, "      scope        %s\n", orDash(impl.ScopeGuarantee.String()))
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

// cmdDetect asks every attached provider that can tell whether it already
// holds a ready index, and corrects indexed_by in memory with whatever it
// finds. Unlike select and ask it defaults to every repository rather than
// requiring one: sweeping the whole catalog is the common case, a single
// dispatch target is not.
func cmdDetect(settingsPath string, args []string, out io.Writer) error {
	var repository string
	var jsonOut bool
	flags := flag.NewFlagSet("detect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&repository, "repo", "", "repository to check (default: every repository registered)")
	flags.BoolVar(&jsonOut, "json", false, "print the result as json instead of prose")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}

	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reports, err := atenea.DetectIndexes(ctx, repository)
	if err != nil {
		return err
	}
	if jsonOut {
		printIndexReportsJSON(out, reports)
		return nil
	}
	printIndexReports(out, reports)
	return nil
}

func printIndexReports(out io.Writer, reports []core.IndexReport) {
	if len(reports) == 0 {
		fmt.Fprintln(out, "no attached provider can report index readiness")
		return
	}
	for _, report := range reports {
		switch {
		case report.Err != "":
			fmt.Fprintf(out, "%-12s %-16s could not tell: %s\n", report.Repository, report.Provider, report.Err)
		case report.Ready:
			fmt.Fprintf(out, "%-12s %-16s ready\n", report.Repository, report.Provider)
		default:
			fmt.Fprintf(out, "%-12s %-16s not ready: %s\n", report.Repository, report.Provider, orDash(report.Hint))
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
	defer func() { _ = atenea.Shutdown() }()
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
			if dropped.Raw != "" {
				fmt.Fprintf(out, "              raw: %s\n", oneLine(dropped.Raw))
			}
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
	var allow effectList
	var trace bool
	var jsonOut bool
	var budget float64
	flags := flag.NewFlagSet("task", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Var(&repositories, "repo", "repository to act on; repeat for several (default: all)")
	flags.Var(&allow, "allow", "effect beyond reading to grant this commission; repeat for several (default: none)")
	flags.Float64Var(&budget, "budget", 0, "what this commission may spend in usd (default: the settings file)")
	flags.BoolVar(&trace, "trace", false, "print the plan, the funnel and every review")
	flags.BoolVar(&jsonOut, "json", false, "print the result as json instead of prose (always complete, ignores --trace)")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"unexpected argument %q after the commission; quote it if it is one commission",
			flags.Arg(0))
	}
	effects, err := allow.effects()
	if err != nil {
		return err
	}

	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, runErr := atenea.Do(ctx, orchestrator.Task{
		Text: text, Repositories: repositories, Effects: effects, BudgetUSD: budget,
	})
	if result != nil {
		if jsonOut {
			printResultJSON(out, result)
		} else {
			printResult(out, result, trace)
		}
	}
	return commissionError(ctx, result, runErr)
}

// commissionError turns what came back into the invocation's error.
//
// A run the user stopped is not a failed commission, and the difference is
// worth a bin of its own on the way out: nobody's work went wrong, so it must
// not land in the code a script retries on, and the report must not say the
// provider did something. The check comes first because a canceled run also
// leaves a failed verdict behind it, and that verdict is a consequence of the
// interruption rather than a finding about anything.
func commissionError(ctx context.Context, result *orchestrator.Result, runErr error) error {
	// A verdict of canceled is not a failed commission, and the difference has
	// to survive all the way to the shell: nothing about the work went wrong.
	// This reads the verdict rather than only the context, because a caller
	// can stop a run through a context of its own that never reaches here.
	if result != nil && result.Verdict == contract.VerdictCanceled {
		return contract.Fail(contract.FailureCanceled, "stopped before it finished")
	}
	worked := result != nil && result.Verdict == contract.VerdictOK
	if !worked && errors.Is(ctx.Err(), context.Canceled) {
		return contract.Fail(contract.FailureCanceled, "stopped before it finished")
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
	var allow effectList
	var repository string
	var trace bool
	var jsonOut bool
	var budget float64
	flags := flag.NewFlagSet("ask", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&repository, "repo", "", "repository to ask about (required when several are registered)")
	flags.Var(&fields, "set", "payload field as name=value; repeat for several")
	flags.Var(&allow, "allow", "effect beyond reading to grant this question; repeat for several (default: none)")
	flags.Float64Var(&budget, "budget", 0, "what this question may spend in usd (default: the settings file)")
	flags.BoolVar(&trace, "trace", false, "print the plan, the funnel and every review")
	flags.BoolVar(&jsonOut, "json", false, "print the result as json instead of prose (always complete, ignores --trace)")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"unexpected argument %q after the capability", flags.Arg(0))
	}
	effects, err := allow.effects()
	if err != nil {
		return err
	}

	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()
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
	// A missing required field is caught here, before anything is spent: the
	// alternative is finding out from whichever implementation the funnel
	// picked, after it already ran.
	if err := capability.ValidateInput(payload); err != nil {
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
		Effects:    effects,
		BudgetUSD:  budget,
	})
	if result != nil {
		if jsonOut {
			printResultJSON(out, result)
		} else {
			printResult(out, result, trace)
			printAnswer(out, result, trace)
		}
	}
	return commissionError(ctx, result, runErr)
}

// cmdResume picks an interrupted or failed commission back up, or with
// --list shows which receipts still have work to continue instead of
// resuming any of them.
//
// --list is peeled off before the flag set sees anything, the same way
// 'metrics clear' peels its own leading word: that path takes no run id, so
// there is no positional argument on it for flag.Parse to trip over.
func cmdResume(settingsPath string, args []string, out io.Writer) error {
	listing := false
	if len(args) > 0 && args[0] == "--list" {
		listing, args = true, args[1:]
	}
	if listing {
		if len(args) != 0 {
			return contract.Fail(contract.FailureInvalidInput,
				"--list takes no run id; it lists every run still worth resuming")
		}
		return cmdResumeList(settingsPath, out)
	}

	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return contract.Fail(contract.FailureInvalidInput,
			"resume needs a run id, e.g. atenea resume 20260803T125305-92dc90; "+
				"atenea resume --list shows the candidates")
	}
	runID, args := strings.TrimSpace(args[0]), args[1:]

	var allow effectList
	var trace bool
	var jsonOut bool
	var budget float64
	flags := flag.NewFlagSet("resume", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Float64Var(&budget, "budget", 0,
		"replace what remains of the grant instead of adding to it (default: what is left)")
	flags.Var(&allow, "allow", "effect to add beyond what the commission already carries; repeat for several (default: none)")
	flags.BoolVar(&trace, "trace", false, "print the plan, the funnel and every review")
	flags.BoolVar(&jsonOut, "json", false, "print the result as json instead of prose (always complete, ignores --trace)")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if flags.NArg() != 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"unexpected argument %q after the run id", flags.Arg(0))
	}
	effects, err := allow.effects()
	if err != nil {
		return err
	}

	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, runErr := atenea.Resume(ctx, runID, orchestrator.ResumeOptions{BudgetUSD: budget, Effects: effects})
	if result != nil {
		if jsonOut {
			printResultJSON(out, result)
		} else {
			printResult(out, result, trace)
		}
	}
	return commissionError(ctx, result, runErr)
}

// cmdResumeList shows every run still worth resuming, oldest first. It
// dispatches nothing and costs nothing to run: Candidates only reads
// receipts already on disk.
func cmdResumeList(settingsPath string, out io.Writer) error {
	atenea, err := load(settingsPath)
	if err != nil {
		return err
	}
	defer func() { _ = atenea.Shutdown() }()
	if !atenea.Checkpoints().Enabled() {
		fmt.Fprintln(out, "checkpointing is off; there is nothing on disk to resume")
		return nil
	}
	candidates, err := atenea.Checkpoints().Candidates()
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		fmt.Fprintln(out, "nothing to resume")
		return nil
	}
	for _, c := range candidates {
		fmt.Fprintf(out, "%-28s %-8s %2d step(s) remaining  %s\n",
			c.ID, orDash(c.Verdict), c.Remaining, oneLine(c.Task))
	}
	return nil
}

// printAnswer shows what came back. A commission reports how many matches it
// found because that is all a caller can act on across several repositories;
// one capability against one repository has an actual answer, and hiding it
// behind a run receipt would make the verb useless.
func printAnswer(out io.Writer, result *orchestrator.Result, trace bool) {
	if len(result.Steps) != 1 {
		return
	}
	step := result.Steps[0]
	if step.Review.Parent != contract.VerdictOK {
		return
	}
	// Under --trace this step's notices already printed once, inside the
	// per-step loop above; saying them again here would be the same caveat
	// twice on one screen. Without --trace that loop never ran, and this is
	// the only place left to say it -- right beside the answer it qualifies,
	// not behind a flag most callers of a plain `ask` will never pass.
	if !trace {
		for _, notice := range step.Outcome.Notices {
			fmt.Fprintf(out, "\nnotice   %s\n", notice)
		}
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

// effectList collects a repeated --allow flag naming effects beyond reading,
// which is always free and never needs to be named.
type effectList []string

func (e *effectList) String() string     { return strings.Join(*e, ",") }
func (e *effectList) Set(v string) error { *e = append(*e, v); return nil }

// effects parses every collected name against the contract's enum, failing
// on the first one this build does not recognize.
func (e effectList) effects() ([]contract.Effect, error) {
	if len(e) == 0 {
		return nil, nil
	}
	out := make([]contract.Effect, 0, len(e))
	for _, name := range e {
		effect, err := contract.ParseEffect(name)
		if err != nil {
			return nil, contract.Fail(contract.FailureInvalidInput, "--allow: %v", err)
		}
		out = append(out, effect)
	}
	return out, nil
}

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
	everywhere, static := staticDrops(result.Steps)
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
		// A charge past the share is a different fact from the charge
		// itself: the money was spent regardless, but this line is the one
		// that says the far side's own ceiling let a turn finish after the
		// budget for it was already gone.
		if over := orchestrator.Overspend(step); over > 0 {
			fmt.Fprintf(out, "      overspent $%.4f\n", over)
		}
		// A step that was stopped gets one line where three would go. There
		// was no review to report -- printing child and parent verdicts here
		// would dress two opinions nobody holds as findings -- and "failed"
		// would be the screen blaming the work for the reader's own decision.
		// What the funnel did still prints below: that part happened.
		if step.FailureKind == contract.FailureCanceled {
			// The message already opens with the bin, and the label is the bin:
			// printed as they come, the word arrives twice.
			fmt.Fprintf(out, "      canceled %s\n",
				strings.TrimPrefix(step.Failure, contract.FailureCanceled.String()+": "))
		} else {
			fmt.Fprintf(out, "      review   child=%s parent=%s (%s)\n",
				step.Review.Child, step.Review.Parent, step.Review.Reason)
			if step.Review.Disagreed {
				fmt.Fprintf(out, "      disputed %s\n", step.Review.Reply)
			}
			if step.Failure != "" {
				fmt.Fprintf(out, "      failed   %s\n", step.Failure)
			}
			if step.Raw != "" {
				fmt.Fprintf(out, "      raw      %s\n", oneLine(step.Raw))
			}
		}
		for _, notice := range step.Outcome.Notices {
			fmt.Fprintf(out, "      notice   %s\n", notice)
		}
		if scope := scopeOf(step.Step.Payload); len(scope) > 0 {
			fmt.Fprintf(out, "      scope    %s\n", strings.Join(scope, ", "))
		}
		// Where the hits are, not just how many. A commission reports a count
		// because that is all that composes across repositories -- but a count
		// is not something anybody can act on, and the paths were reachable
		// only by re-running the same work as `ask --json`. Measured on the
		// dogfood run: `15 hit(s) for "CANDIDATES"` and a second full
		// dispatch to learn which files. A trace is exactly where this belongs.
		if paths := answerPaths(step.Outcome.Result); len(paths) > 0 {
			shown := paths
			if len(shown) > maxTracePaths {
				shown = shown[:maxTracePaths]
			}
			fmt.Fprintf(out, "      found    %s\n", strings.Join(shown, ", "))
			if len(paths) > len(shown) {
				fmt.Fprintf(out, "               and %d more file(s): atenea ask %s --repo %s --json\n",
					len(paths)-len(shown), step.Step.Capability, step.Step.Repository)
			}
		}
		for _, stage := range step.Decision.Stages {
			for _, dropped := range stage.Dropped {
				if everywhere[dropKey{dropped.Implementation, dropped.Reason, dropped.Raw}] {
					continue
				}
				fmt.Fprintf(out, "      dropped  %s: %s\n", dropped.Implementation, dropped.Reason)
				if dropped.Raw != "" {
					fmt.Fprintf(out, "               raw: %s\n", oneLine(dropped.Raw))
				}
			}
		}
	}

	// A drop that is identical in every step is a fact about the catalog on
	// this machine, not about any step: "no attached runner serves it" does
	// not become truer for being printed six times, and repeating it buries
	// the drops that DID vary, which are the only ones worth reading a trace
	// for. Measured on the dogfood run: four of five trace lines per step were
	// the same three sentences.
	if len(everywhere) > 0 {
		fmt.Fprintf(out, "\ndropped in every step\n")
		for _, d := range static {
			fmt.Fprintf(out, "  %s: %s\n", d.implementation, d.reason)
			if d.raw != "" {
				fmt.Fprintf(out, "      raw: %s\n", oneLine(d.raw))
			}
		}
	}
}

// dropKey identifies one funnel drop by what it actually says. Two steps that
// dropped the same provider for the same reason carry the same key even
// though they are different DroppedImplementation values.
type dropKey struct{ implementation, reason, raw string }

// staticDrops finds the drops every step with a funnel decision shares, in
// the order they were first seen.
//
// One step cannot have a repetition, so nothing is collapsed for a single ask:
// there the drops are the whole story of that one funnel.
func staticDrops(steps []orchestrator.StepResult) (map[dropKey]bool, []dropKey) {
	seen := make(map[dropKey]int)
	var order []dropKey
	decided := 0
	for _, step := range steps {
		if len(step.Decision.Stages) == 0 {
			continue
		}
		decided++
		here := make(map[dropKey]bool)
		for _, stage := range step.Decision.Stages {
			for _, dropped := range stage.Dropped {
				key := dropKey{dropped.Implementation, dropped.Reason, dropped.Raw}
				if here[key] {
					continue
				}
				here[key] = true
				if seen[key] == 0 {
					order = append(order, key)
				}
				seen[key]++
			}
		}
	}
	if decided < 2 {
		return nil, nil
	}
	everywhere := make(map[dropKey]bool)
	static := make([]dropKey, 0, len(order))
	for _, key := range order {
		if seen[key] == decided {
			everywhere[key] = true
			static = append(static, key)
		}
	}
	return everywhere, static
}

// maxTracePaths caps how many files a trace names before it points at the
// command that prints all of them. A trace is detail, not the answer itself,
// and a hundred paths in a step block buries the funnel underneath them.
const maxTracePaths = 8

// answerPaths collects the distinct files an outcome named, in the order it
// named them.
//
// It walks the whole answer rather than knowing any capability's shape:
// code.search returns matches, the symbol capabilities return locations, and
// every one of them calls the field "path" because the contract says so. A
// capability that ever answers without one simply contributes nothing here.
func answerPaths(result map[string]any) []string {
	seen := make(map[string]bool)
	var out []string
	var walk func(value any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if path, ok := typed["path"].(string); ok && path != "" && !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
			for _, name := range slices.Sorted(maps.Keys(typed)) {
				if name == "path" {
					continue
				}
				walk(typed[name])
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(result)
	return out
}

// scopeOf reads the "scope" payload field regardless of how it got there. A
// step just dispatched in this process carries a real []string, put there by
// hint(); a step read back off a receipt decodes the same field as []any,
// because encoding/json never rebuilds a concrete element type for a
// map[string]any -- every array becomes a slice of interfaces, whatever was
// written into it. Printing a trace for a resumed run means reading either.
func scopeOf(payload map[string]any) []string {
	switch v := payload["scope"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func cmdRun(settingsPath string, out io.Writer) error {
	atenea, err := loadService(settingsPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	status := atenea.Status()
	fmt.Fprintf(out, "atenea %s ready  contract %s  %s\n",
		status.Version, status.Contract, strings.ToUpper(status.Light.String()))
	fmt.Fprintf(out, "settings %s\n", status.Settings)
	// A repair is said on the way up, not left for somebody to find by asking
	// for a status screen. This line is the only place the operator learns
	// that the last close was ugly: the service starts on its own after a
	// reboot or a crash, so there is nobody watching a terminal to notice.
	if summary := status.Recovered.Summary(); summary != "" {
		fmt.Fprintf(out, "recovered %s\n", summary)
	}
	fmt.Fprintf(out, "waiting for work; press Ctrl-C to stop\n")

	if err := atenea.Run(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "stopped cleanly\n")
	return nil
}

// cmdService puts Atenea on the machine as a background service, takes it off
// again, and says which of those is currently true.
func cmdService(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"service needs a subcommand: install, uninstall or status")
	}
	switch args[0] {
	case "install":
		return serviceInstall(settingsPath, out)
	case "uninstall":
		return serviceUninstall(out)
	case "status":
		return serviceStatus(out)
	default:
		return contract.Fail(contract.FailureInvalidInput, "unknown service subcommand %q", args[0])
	}
}

// serviceInstall reads the settings for one number and ignores the rest.
//
// core.shutdown_grace is the margin `run` gives in-flight work on the way
// down, and the unit has to be told to wait longer than that. Nothing else in
// the file has anything to do with this command, and an install that failed
// because some provider is misconfigured would be refusing to set up the
// machine over a problem the machine does not have yet.
func serviceInstall(settingsPath string, out io.Writer) error {
	cfg, err := config.Load(settingsPath)
	if err != nil {
		return err
	}
	service, err := runningBinaryService(cfg.Core.ShutdownGrace)
	if err != nil {
		return err
	}
	if err := service.Install(); err != nil {
		return err
	}
	state, err := platform.Query(service.Name)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "unit      %s\n", service.Unit)
	fmt.Fprintf(out, "starts    %s run\n", service.Exec)
	fmt.Fprintf(out, "enabled   %s\n", yesNo(state.Enabled))
	if !state.Linger {
		// Enabled is only half of "starts with the system" on a user manager.
		// Without lingering the unit waits for a login that a machine
		// rebooting unattended never performs. Atenea cannot switch it on for
		// itself, so the operator has to be handed the line.
		fmt.Fprintf(out, "\nlingering is off: the service waits for your next login instead of\n")
		fmt.Fprintf(out, "starting at boot. Run this once to fix that:\n")
		fmt.Fprintf(out, "  %s\n", platform.LingerCommand())
	}
	return nil
}

// serviceUninstall asks for no settings. It is removing a unit rather than
// writing one, and reading the settings file here would let a broken one block
// the command that cleans up after it.
func serviceUninstall(out io.Writer) error {
	// The grace goes nowhere, since no unit is rendered; a Service is built
	// only because it is what carries the name and the path.
	service, err := runningBinaryService(0)
	if err != nil {
		return err
	}
	if err := service.Uninstall(); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed   %s\n", service.Unit)
	return nil
}

// serviceStatus reports and never fails, the same as the health screen. A
// machine with no service manager is a finding the operator needs on the
// screen; turning it into an exit code would hide it on exactly the machine
// where somebody is trying to work out why nothing is running.
func serviceStatus(out io.Writer) error {
	state, err := platform.Query(platform.ServiceName)
	fmt.Fprintf(out, "unit      %s\n", orDash(state.Unit))
	if err != nil {
		fmt.Fprintf(out, "manager   %s\n", oneLine(err.Error()))
		return nil
	}
	fmt.Fprintf(out, "installed %s\n", yesNo(state.Installed))
	fmt.Fprintf(out, "enabled   %s\n", yesNo(state.Enabled))
	fmt.Fprintf(out, "active    %s\n", yesNo(state.Active))
	fmt.Fprintf(out, "linger    %s\n", yesNo(state.Linger))
	fmt.Fprintf(out, "detail    %s\n", oneLine(state.Detail))
	if state.Installed && !state.Linger {
		// The same warning install gives, at the moment somebody is actually
		// looking for why nothing came up after a reboot. A count with no
		// address is a nag, and so is a "no" with no remedy.
		fmt.Fprintf(out, "\nlingering is off: an installed service still waits for your next\n")
		fmt.Fprintf(out, "login instead of starting at boot. Run this once to fix that:\n")
		fmt.Fprintf(out, "  %s\n", platform.LingerCommand())
	}
	return nil
}

// runningBinaryService describes the service around the binary executing right
// now. Pointing the unit anywhere else would install a service that starts a
// copy of Atenea nobody is looking at.
func runningBinaryService(stopGrace time.Duration) (platform.Service, error) {
	binary, err := os.Executable()
	if err != nil {
		return platform.Service{}, contract.Fail(contract.FailureUnavailable,
			"cannot find the running atenea binary to point the service at: %v", err)
	}
	return platform.NewService(binary, stopGrace)
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

// clients maps a client name to the environment variable that client reads
// its MCP configuration from, and the function that renders it.
//
// The map is the whole extension point, and it is deliberately this small:
// every client that can be wrapped is one that takes its configuration from
// the environment. A client that can only be configured by editing a file on
// disk does not belong here, because the guarantee that makes wrap safe to
// try -- run it without wrap and nothing about your setup has changed -- is
// exactly the guarantee a file edit cannot make.
var clients = map[string]struct {
	env    string
	render func(wrap.Plan) (string, error)
}{
	"opencode": {env: "OPENCODE_CONFIG_CONTENT", render: wrap.Plan.OpenCodePayload},
}

func cmdWrap(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"wrap needs a client, e.g. atenea wrap opencode")
	}
	name, rest := args[0], args[1:]
	client, ok := clients[name]
	if !ok {
		return contract.Fail(contract.FailureInvalidInput,
			"cannot wrap %q; supported: %s", name, strings.Join(slices.Sorted(maps.Keys(clients)), ", "))
	}
	// Resolved before anything is probed. Spending eleven handshakes to
	// discover the binary is not installed would be the report arriving
	// after the answer that makes it pointless.
	binary, err := exec.LookPath(name)
	if err != nil {
		return contract.Fail(contract.FailureNotFound,
			"%s is not on PATH: %v", name, err)
	}

	// Settings only. A Core would open the measurement base and may start a
	// managed Serena, and this command holds no lock and asks no provider
	// anything -- it reads a list and launches a process that will outlive
	// it, so holding a DuckDB file open for the length of a chat session is
	// a cost with nothing on the other side of it.
	cfg, err := config.Load(settingsPath)
	if err != nil {
		return err
	}
	plan := wrap.Check(context.Background(), cfg.MCPServers)
	plan.Report(out, name)

	payload, err := client.render(plan)
	if err != nil {
		return err
	}
	return launch(binary, append([]string{name}, rest...), client.env, payload)
}

// launch replaces this process with the client.
//
// Replacing rather than spawning is what makes the wrapper invisible once it
// has done its job. A parent sitting in the middle would have to forward
// signals, copy three streams, and translate an exit status -- three chances
// to get wrong what the kernel gets right for free. It also settles the
// question of what happens to Atenea while a chat session runs for an hour:
// nothing, because Atenea is no longer there.
func launch(binary string, argv []string, key, payload string) error {
	env := append(os.Environ(), key+"="+payload)
	if err := syscall.Exec(binary, argv, env); err != nil {
		return contract.Fail(contract.FailureUnavailable, "cannot start %s: %v", binary, err)
	}
	return nil // unreachable: Exec only returns on failure.
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

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// clip keeps a status line readable when the provider text behind it runs
// long. The full text is never lost -- it is on the receipt, and --trace
// prints it whole -- this only keeps the short screen short.
func clip(s string) string {
	const limit = 160
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}
