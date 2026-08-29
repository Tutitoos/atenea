// Command atenea is the entry point of the Atenea orchestration core.
//
// Atenea lives outside the CLIs it serves, so this binary is what gets started
// on the machine. `run` is the lifecycle and the rest of the commands are the
// operator's window into the catalog, the selector and the agent.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Tutitoos/atenea/internal/backup"
	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/clientconfig"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/dashboard"
	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/internal/statusline"
	"github.com/Tutitoos/atenea/internal/wrap"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const usage = `atenea - orchestration core

Usage:
  atenea [--config PATH] <command> [flags]

Commands:
  status                 Short health screen: one light for Atenea, one per provider
  doctor                 Check desktop client/profile compatibility and MCP wiring
  select CAPABILITY      Ask the funnel who should answer a capability
  task "TEXT"            Hand a commission to the orchestrator; --budget USD
                         funds this one above the settings file
  decide "TEXT"          Explain model, tool, MCP, provider and workflow choices;
                         --run executes isolated workflows per repository
  ask CAPABILITY         Dispatch one capability against one repository
  resume RUN_ID          Pick an interrupted or failed commission back up;
                         --budget USD replaces what remains of the grant.
                         resume --list shows every run still worth it
  catalog                List capabilities, providers and repositories in full
  dashboard ID            Check and open a dashboard; use 'hosts' for aliases
  intent [--json]        Read the client config this repo carries and say how
                         Atenea answers it; launches nothing from it
  detect [--repo ID]     Ask attached providers whether they already hold a
                         ready index; corrects indexed_by in memory when they do
  run                    Run as a service until interrupted
  desktop ACTION         Do one thing on this machine's screen or keyboard,
                         after showing it and asking. --repo ID --app BUNDLE_ID
                         --set NAME=VAL --confirm. It always asks, and the act
                         itself runs inside the service, which is the process
                         the system grants the permission to
  mcp                    Serve an MCP client over stdin/stdout, bridged to the
                         running service; --check tests the setup without a client
  service install        Install atenea as a background service that starts
                         with the system; 'uninstall' undoes it, 'status'
                         says where it stands
  incidents              Read the crash notebook; add 'clear' to mark it read
  agent TYPE [FILE]      Run one declared agent type as a process, once;
                         --objective/--criterion set the task; --confirm
                         approves write or external effects
  workflow VERB          Draw, launch and steer a graph of agent steps:
                         create, launch, run, propose, approve, reject,
                         resume, redo, list, show. Money is granted per graph
                         and split between its steps; nothing spawns until a
                         person launches it
  traces                 What agents ran and how they ended; filter with
                         --type, --verdict, --open, --since, --id, --limit
  metrics                What the base measured, per capability and provider;
                         'clear' empties it, narrowed by --capability,
                         --implementation or --repository, or --all for the lot
  backup list            List complete state snapshots
  backup restore NAME TARGET [--replace]
                         Restore a snapshot into a new directory, or swap it
                         over an existing directory with --replace
  backup promote TARGET Promote the retained pre-restore directory
  backup discard TARGET --confirm
                         Permanently remove the retained pre-restore directory
  floor                  What starting one turn already costs, measured per
                         repository, per agent and per model; 'measure
                         --repo ID --agent TYPE' spends real money to find
                         out, never invented
  config init            Write the built-in settings file to disk
  config path            Print where settings are read from
  config show            Print the settings that apply here, and from where
  wrap CLIENT [args]     Launch a client with MCP servers Atenea checked a
                         moment ago; dead ones are named and left out
  statusline install [WIDGET]
                         Put Atenea's status line on opencode's screen;
                         'uninstall' takes it off, 'status' says whether the
                         installed copy is the one this binary ships, and
                         'widgets' lists the ones this binary carries
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
	"doctor": `Usage: atenea doctor --client CLIENT [--profile NAME] [--json]

Diagnose desktop client/profile compatibility and MCP wiring without invoking
tools with effects.

Flags:
  --client CLIENT   claude, chatgpt or codex
  --profile NAME    desktop policy profile
  --json            print the diagnostic result as json
`,
	"catalog": `Usage: atenea catalog

List every capability, its providers, and every registered repository.
`,
	"dashboard": `Usage: atenea dashboard ID [--check]
       atenea dashboard hosts [--dry-run] [--check] [--remove-obsolete]

Check and open the configured dashboard for one MCP. Opening is always manual;
starting or probing an MCP never opens a browser.

Flags for an ID:
  --check       check accessibility without opening the browser

The hosts form manages only Atenea's marked block in /etc/hosts. Use --dry-run
to preview changes, and --check to inspect the file without writing it.
`,
	"detect": `Usage: atenea detect [flags]

Ask every attached provider that can tell whether it already holds a ready
index, and correct indexed_by in memory with whatever it finds. Read-only
about the repository -- it asks providers and never builds an index; providers
must be indexed externally.

Flags:
  --repo ID   repository to check (default: every repository registered)
  --json      print the result as json instead of prose
`,
	"select": `Usage: atenea select CAPABILITY [flags]

Ask the funnel who would answer a capability, without spending anything.

Flags:
  --repo ID   repository id (defaults to the only one registered)
  --prefer ID one-call implementation preference (e.g. ripgrep, codex.search, claude.search)
`,
	"task": `Usage: atenea task "TEXT" [flags]

Hand a commission to the orchestrator in the user's own words.

Flags:
  --repo ID       repository to act on; repeat for several (default: all)
  --allow EFFECT  effect beyond reading to grant this commission; repeat for
                  several (default: none)
  --budget USD    what this commission may spend (default: the settings file)
  --confirm       show the execution summary and require an interactive TTY confirmation
  --trace         print the plan, the funnel and every review
  --json          print the result as json instead of prose (always
                  complete, ignores --trace)
`,
	"decide": `Usage: atenea decide "TEXT" [flags]

Build and explain the complete decision plan without executing it. The plan
shows intent, model role, native tools, raw MCP tools, capability providers,
permissions, budget shares and workflow dependencies.

Flags:
  --repo ID       repository to plan for (default: every declared repository)
  --file PATH     named file; repeatable and enables the cheaper reader agent
  --allow EFFECT  effect beyond reading to grant this commission; repeatable
  --prefer ID     one-call provider preference
  --tool ID       explicit raw MCP tool to include
  --budget USD    what this commission may spend (default: the settings file)
  --json          print the complete plan as json
  --trace         include the decision reasons
  --run           execute the compiled plan, one isolated workflow per repository
  --confirm       require a TTY confirmation before --run
  --traces PATH   workflow state database
`,
	"ask": `Usage: atenea ask CAPABILITY [flags]

Dispatch one capability against one repository.

Flags:
  --repo ID       repository to ask about (required when several are
                  registered)
  --set NAME=VAL  payload field; repeat for several
  --payload FILE  the whole payload as json, for inputs --set cannot express
                  (a record or a list of them). Not combinable with --set
  --allow EFFECT  effect beyond reading to grant this question; repeat for
                  several (default: none)
  --budget USD    what this question may spend (default: the settings file)
  --prefer ID     one-call implementation preference (e.g. ripgrep, codex.search, claude.search)
  --confirm       show the execution summary and require an interactive TTY confirmation
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
	"desktop": `Usage: atenea desktop ACTION --repo ID --app BUNDLE_ID [--set NAME=VALUE ...] --confirm

Do one thing on this machine's screen or keyboard.

  atenea desktop click  --repo work --app com.apple.finder --set x=412 --set y=280 --confirm
  atenea desktop type   --repo work --app com.apple.TextEdit --set text="hello" --confirm
  atenea desktop key    --repo work --app com.apple.finder --set key=return --confirm

Actions: click, move, drag, scroll, type, key. Coordinates are in the space of
the image desktop.screenshot returned -- nothing needs to be multiplied by
anything.

It always shows what is about to happen and waits for you to type yes. There is
no flag to skip that, deliberately: a flag to skip it is the whole point of the
command being removed for convenience.

The act itself runs inside the service, not here. macOS attributes a device
permission to the process that is responsible for itself, which is the one
launchd started -- a command run from a shell would be spending the terminal's
permission instead, and the adapter refuses that. So this asks where there is a
terminal to ask in, and dispatches where the permission actually lives.

The application must be in the settings file's [desktop] allow-list, which is
empty by default. Find bundle identifiers with the desktop.apps capability.
`,
	"mcp": `Usage: atenea mcp
       atenea mcp --check

Serve one MCP client over stdin and stdout, relaying to the running service.
Meant to be launched by the client, not by hand: put it in the client's MCP
configuration as the command to run.

Every capability becomes a tool, and every tool takes a repository because
that is Atenea's unit of work. The connection is one chat, named after the
client that opened it and visible in 'atenea status' while it lasts.

A service has to be running -- this bridge decides nothing on its own.
--check says whether one is listening, and what a client would be offered,
without going through a client at all.
`,
	"service": `Usage: atenea service install
       atenea service uninstall
       atenea service status

Install atenea as a background service that starts with the system;
'uninstall' undoes it, 'status' says where it stands.
`,
	"statusline": `Usage: atenea statusline install|uninstall|status [WIDGET]
       atenea statusline widgets

Put Atenea's status line on opencode's screen: one always-visible line
reporting the traffic light, the version actually running, and unread
incidents. It reads the service's own socket, so it needs no key and no
network, and it says "apagado" rather than warning when nothing is running.

More than one widget ships, and each verb takes the name of one:

  atenea             the traffic light, the running version and incidents
  session-share      which model did what share of this session's tokens
  limits             how much of each provider's rate-limit window is used

'widgets' lists them with their summaries. A verb with no name means the
default widget for install and uninstall -- 'atenea' -- and every installed
widget for status, because the question status answers is what is on the
screen, and answering it for one of three would be a partial truth printed
as a whole one.

The plugin source travels inside this binary. 'status' compares what is
installed against what this binary carries, because a line reporting a
version is worth nothing if the file drawing it is a copy nobody updated.

A running TUI loads plugins at startup: after install, restart opencode.
`,
	"intent": `Usage: atenea intent [--json]

Reads the client configuration this repository carries -- .mcp.json and
.claude/ for Claude Code, opencode.json and .opencode/ for opencode -- and
says, for each thing the project asks for, how Atenea answers it:

  funnel     a registered provider answers it; the capabilities are named
  vouched    Atenea declares that backend itself and hands it to clients
             through 'wrap', having checked it first
  unmatched  the project asks for it and nothing here provides it
  prose      a skill: instructions for a client, not a capability

Nothing in those files is launched, and nothing is trusted. A declaration's
command, arguments, environment and URL are dropped when the file is parsed:
they are never carried past the reader, so nothing downstream can run them.
A file that arrives with a git clone may not hand this machine a process.

The unmatched list is printed last and counted. A translator that quietly
dropped what it could not map would produce a report in which absence and
satisfaction look identical.

Reads only. It writes nothing, anywhere, and never into .claude/ or
.opencode/.
`,
	"incidents": `Usage: atenea incidents [clear] [--all]

Read the crash notebook. With no arguments, shows what has not been read
yet. --all shows the whole notebook, read or not. 'clear' marks the shown
entries read.
`,
	"agent": `Usage: atenea agent TYPE [flags] [files...]

Run one declared [[agent]] type as a real process, once. The type is
resolved by name against the settings file; nothing ranks agents and nothing
falls back to another one.

Flags come before file names -- Go's parser stops at the first word that is
not a flag, so a flag after a file name would be read as another file.

Flags:
  --objective TEXT      what the agent is being asked to do
  --criterion TEXT      what done looks like
  --repository ID       repository to serve at the repository context level
  --traces PATH         trace database (default: the one atenea traces reads)
  --quiet               print the verdict line only
  --review TYPE         audit the answer with this agent type
  --confirm             approve write or external effects; required, and
                        asked again on the terminal, for a type that
                        declares either -- without it the run is refused

An agent that exits zero without writing a report has not answered: it is
recorded as incomplete, not as success.

--review hands the answer to a second agent. A refusal relaunches the work
once, carrying the rejected answer and the reason it was rejected; a second
refusal ends it. Each attempt and each review is its own trace row.
`,
	"workflow": `Usage: atenea workflow create|launch|run|propose|approve|reject|resume|redo|list|show

Run a graph of agent steps. The graph comes from a TOML file and is executed
exactly as written: nothing here plans, splits or grows it.

Steps with no unmet dependency run together, up to the ceiling of their lane
(workflow.max_parallel_agent and max_parallel_review). A step that fails takes
only its dependents with it. Two steps that can run at once may not both touch
a file when one writes it -- that is refused before anything spawns.

An edge carries order; a subject carries the answer:

  needs = ["read-a"]    run after read-a, and only if it ended ok
  subject = "read-a"    hand this step read-a's whole report -- implies the edge
  on = "answered"       what the subject must have reached: answered (the
                        default: ok, failed or incomplete) or ok

A subject is for review-pool agent types, one upstream each. A reviewer with
no subject is refused, and so is a subject handed to a type that never reads
one. A step nobody judged clears no bar: what depended on it reads blocked and
says which command would clear it.

A plan is read before it is run, so those are two commands. "run PATH" is both
at once: the person typed the path. Over MCP there is no shortcut -- a plan and
the permission to run it must not arrive in one message.

The graph may grow mid-run, three times at most. While a gate waits nothing new
is dispatched and what is already spawned finishes; waiting never times out. A
proposal may only replace steps that have NOT STARTED, which is what makes a
stale approval impossible to construct rather than a race to catch. The answer
is a row, so a gate outlives the atenea that opened it.

A step's budget_usd is a FORECAST, not a ceiling. Every share is checked against
the grant before anything spawns, and a plan whose steps are funded below what
starting a turn costs is refused -- but nothing stops a turn once it is running.
The provider's own --max-budget-usd is checked between messages, so it decides
whether to start another one and never what the one in flight costs. Measured on
a 23-step run 2026-08-16: a $0.09 share spent $0.41, and a $5.22 grant was
charged $5.88. Budget for the work, then expect a step to exceed its share by up
to the cost of one turn, and the run to exceed its grant by the sum of those.

limits.max_tokens is carried and validated as an advisory declaration. The
model client can use its separate ReadTokens allowance to stop an observed
conversation, but no provider-independent hard token ceiling is promised.

Flags come before the ids -- Go's parser stops at the first word that is not a
flag, so anything after one is read as an id.

  create PATH           write the graph down and print the plan; spawn nothing
  launch ID             commit the grant and run it
  run PATH              create and launch in one command
  propose ID PATH       put an expansion to whoever is running it
  approve ID            let a waiting expansion in
  reject --reason W ID  turn it down; a refused launch stops the run, a refused
                        expansion leaves the approved graph to finish
  resume ID             continue a run that was cut, or whose atenea died
  redo --step S=USD ID  dispatch a step that was cut at its own ceiling, at a
                        raised share; reopens a finished run to do it
  list                  the runs on record
  show ID               one run, step by step, and its gate log

Flags:
  --traces PATH         state database (workflows live beside the traces)
  --repository ID       repository to serve at the repository context level
  --redo STEP           with resume: dispatch a step nobody judged; repeatable
  --step ID=USD         with redo: a step and its raised share; repeatable
  --grant USD           with redo: the run's new total; default leaves it alone
  --replaces STEP       with propose: a step it removes; repeatable
  --reason WHY          with reject: required
  --limit N             with list: how many runs

Ctrl-C cuts the running agents and spawns nothing queued. What was cut is
recorded as interrupted -- nobody judged it -- and resume redoes the read-only
ones by itself. A step that may have written something waits for --redo.

A step cut at its SPENDING ceiling is a different case: it was judged -- it
reported, and the report said incomplete -- so resume leaves it alone and redo
is what dispatches it. Every share redo takes must be higher than the one that
was cut, because the same share buys the same result, and it is never automatic
for the same reason. What the record keeps afterwards is the pair: the attempt
cut at $0.62, and the one that finished at $0.80. Measured 2026-08-16: 150 steps
had been cut at a ceiling and 2 had ever been re-dispatched.
`,
	"traces": `Usage: atenea traces [flags]

What agents ran and how they ended. A row with no ending is a run nobody saw
finish; the next atenea closes it as incomplete once it has checked that the
process which opened it is really gone.

Flags:
  --type NAME           narrow to one agent type
  --verdict V           ok, failed, incomplete or canceled
  --open                only runs still waiting for an ending
  --id ID               one run
  --since DURATION      only runs started within it, e.g. 2h
  --limit N             how many rows at most
  --traces PATH         trace database to read
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
	"backup": `Usage: atenea backup list
       atenea backup restore SNAPSHOT TARGET [--replace]
       atenea backup promote TARGET
       atenea backup discard TARGET --confirm

List complete state snapshots, restore one, or manage the retained state from
an in-place restore. Without --replace, restore requires a new target. With
--replace, the current target is retained as TARGET.atenea-previous. Promote
rolls that state back and retains the current target as TARGET.atenea-current.
Discard is destructive and requires --confirm.
`,
	"floor": `Usage: atenea floor
       atenea floor measure --repo ID --agent TYPE [--dry-run] [--confirm]

What starting a single turn already costs on a repository, before any file
is read: the tokens the CLI spends writing its system prompt and tool
schemas into cache, priced in dollars. It is never invented -- there is no
default and no formula here, only what 'measure' found by spending one real
turn to find out.

A floor is per repository, per agent and per model, never per repository
and model alone: --agent names which declared agent type's tool surface to
measure, any name this settings file's own [[agent]] blocks declare -- an
undeclared name is refused, listing what is declared, rather than silently
falling back to one of two hardcoded choices the way it used to. What
actually drives the cost is the tool surface that type starts a turn with,
not which model answers it. Measured 2026-08-15, same repository and same
model: explore (Atenea's own tools plus Read and Glob) costs $0.27, 26,603
prefix tokens; plan (no tools at all) costs $0.06, 4,991 -- 81% of the
$0.27 floor is the tool definitions written into cache before the model
reads a single file. --agent was called --model until 2026-08-14, which is
exactly why the two measurements used to collide: the flag never selected
a model, only ever the agent.

'measure' prices prefix tokens, not cache-write tokens: probed cold and
warm an hour apart, the same surface wrote 26,603 and read 0 the first
time, then wrote 3,325 and read 23,278 the second -- two splits of the
same 26,603 total, because the split moves with cache state and the total
does not. A probe that lands warm has no receipt of its own to price by,
so 'measure' prices its tokens at the rate of the most recent COLD reading
on the same model, from any repository or agent. Nothing has ever been
measured cold on that model -- refused rather than guessed at: wait for
the warm cache entry to age out (roughly an hour with no probe of that
repository, agent and model; every probe refreshes it) and measure again.

filereader, reviewer and plan-check spend nothing to measure: they are
deterministic Go on the far side of the spawn and call no model at all, so
'measure' stores a zero floor for them instead of pricing a turn that
agent type never runs.

With no subcommand, floor lists every stored measurement: repository,
agent, model, the warm and cold prices, prefix tokens, the block that
arrives with the first tool call, the rescuable share, CLI version and how
long ago it was taken. Warm is what a step pays and cold is what
establishing the cache costs on whichever run of the hour is first: the
prefix and the first-call block are written once per machine per cache
lifetime and read at a twentieth of the price by every step after, so a
plan is refused against the warm figure wherever a row carries one. A dash
means no probe has made a tool call on that row yet, and the cold price is
charged instead. The rescuable share is not a second measurement either --
it is derived from the same row: the smallest share past which half of it,
the read allowance, outweighs the turn's own first assistant event. A step
funded below it clears the floor and still dies with nothing written,
because the model is nudged to answer before it has read anything of its
own -- 'workflow create' refuses a plan on this number the same way it
refuses one on the floor. A row is marked stale when its CLI version does
not match the CLI installed on this machine right now -- the system prompt
and tool schemas ship WITH the CLI, so a new CLI is a new floor even
against the same repository, agent and model. A row measured before
--agent existed prints with no agent and must be re-measured -- see
'floor measure' -- rather than be guessed at.

'measure' spends real money on every agent type except the three named
above: one turn, priced at roughly what it finds. It prints what the last
probe of the same repository, agent and model was billed if anything did,
before it spends anything. It never tops a stored figure up -- a
re-measurement replaces the row outright.

Neither flag has a default, because the only thing a default could do here
is spend a turn on something nobody named. 'atenea floor' with no
subcommand lists every type already measured, and costs nothing.

Flags (measure):
  --repo ID     repository id or path to measure (required)
  --agent TYPE  which declared agent type's tool surface to measure
                (required -- it spends a turn, so there is no default)
  --dry-run     resolve the repository, the agent type and the model, print
                what the turn would be billed, and spend nothing
  --confirm     ask at the terminal before spending it, quoting the estimate;
                refused outright when stdin is not a terminal
`,
	"config": `Usage: atenea config init [--force]
       atenea config path
       atenea config show

'init' writes the built-in settings file to disk; --force overwrites one
that already exists. 'path' prints where settings are read from.

'show' prints the settings that actually apply in this directory, and for
everything a repository is allowed to declare, whether the value came from
the repository's own .atenea/config.toml or from the global file.

A repository may carry .atenea/config.toml at its root. It is a partial
overlay: what it declares wins, what it omits falls back to the global
file, and a repository without one changes nothing. It may declare what
the repository is ([[repository]]: languages, scale, vcs, indexed_by),
which implementation to prefer for it ([[selector.rule]]) and further
files to treat as delicate ([security] sensitive, added to the global list
and never removed from it). Every other key is refused by name: a file
that arrives with a git clone may not hand this machine a command to run.
Set ATENEA_LOCAL_CONFIG=0 to ignore the layer entirely.
`,
	"wrap": `Usage: atenea wrap CLIENT [--profile NAME] [--emit-config] [client args...]
	       atenea wrap --client CLIENT [--profile NAME] [--emit-config]

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

Nothing is written to disk. The configuration rides in one environment
variable or on the client's own command line, for the lifetime of the
child, so a client launched without wrap is a client with exactly the
configuration it had before. There is no unwrap because there is nothing
to undo.

Flags:
  --profile NAME     desktop policy profile
  --emit-config      print the generated client configuration without launching

Arguments after CLIENT are passed through untouched.

Supported clients:

  opencode  OPENCODE_CONFIG_CONTENT, deep-merged over its own config
  claude    --mcp-config <json>, added to every other source it resolves
  codex     one -c mcp_servers.<id>={...} per server, merged into the table
  omp       launched with its existing MCP discovery and configuration

OMP is a pass-through alias for now. Its MCP servers are read from
.omp/mcp.json, mcp.json or .mcp.json, and the current OMP CLI has no
ephemeral MCP overlay that wrap can use without writing one of those files.
The wrapper therefore preserves OMP's own configuration and arguments while
still checking and reporting Atenea's declared servers.
`,
}

// passesArgumentsThrough names the commands whose trailing arguments belong to
// another program. Their arguments are not Atenea's to read, and the help
// interceptor below has to stop before it reaches them.
var passesArgumentsThrough = map[string]bool{"wrap": true}

// helpRequested reports whether -h or --help appears anywhere in args. Most
// subcommands read a positional argument before their own flags, so relying
// on flag.FlagSet's own -h/--help handling would only catch part of
// "atenea <command> --help"; scanning the whole slice catches all of it,
// regardless of where the flag landed.
//
// The scan stops at the first positional argument of a pass-through command,
// because everything after it is handed to the client verbatim: `atenea wrap
// claude --help` is a request for claude's help, and answering it with
// Atenea's left no way to ask a wrapped client anything at all -- the one
// thing wrap promises is that the client sees its own arguments. Help about
// the wrapper is still `atenea wrap --help`, with the flag ahead of the
// client name, which is where the argument list is still Atenea's.
func helpRequested(command string, args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
		if passesArgumentsThrough[command] && !strings.HasPrefix(arg, "-") {
			return false
		}
	}
	return false
}

// noArguments refuses what a command was never going to read.
//
// Four commands take nothing at all, and dropping their extra words in silence
// costs more here than elsewhere because of --config: it is a global flag,
// parsed only ahead of the command name, so `atenea status --config other.toml`
// reads the default settings file and reports confidently on a machine the
// operator did not ask about. Every other command already refuses its leftovers
// through flags.NArg().
func noArguments(command string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if name := strings.TrimLeft(args[0], "-"); name == "config" || strings.HasPrefix(name, "config=") {
		return contract.Fail(contract.FailureInvalidInput,
			"--config is a global flag and belongs before the command: atenea --config PATH %s", command)
	}
	return contract.Fail(contract.FailureInvalidInput,
		"%s takes no arguments: %q", command, args[0])
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
	if help, ok := commandHelp[command]; ok && helpRequested(command, commandArgs) {
		fmt.Fprint(out, help)
		return nil
	}
	switch command {
	case "version":
		if err := noArguments("version", commandArgs); err != nil {
			return err
		}
		return cmdVersion(out)
	case "status":
		if len(commandArgs) > 0 && (commandArgs[0] == "--client" || commandArgs[0] == "--profile") {
			return cmdDesktopStatusCompat(settingsPath, commandArgs, out)
		}
		if err := noArguments("status", commandArgs); err != nil {
			return err
		}
		return cmdStatus(settingsPath, out)
	case "catalog":
		if err := noArguments("catalog", commandArgs); err != nil {
			return err
		}
		return cmdCatalog(settingsPath, out)
	case "dashboard":
		return cmdDashboard(settingsPath, commandArgs, out)
	case "detect":
		return cmdDetect(settingsPath, commandArgs, out)
	case "intent":
		return cmdIntent(settingsPath, commandArgs, out)
	case "select":
		return cmdSelect(settingsPath, commandArgs, out)
	case "task":
		return cmdTask(settingsPath, commandArgs, out)
	case "decide":
		return cmdDecide(settingsPath, commandArgs, out)
	case "ask":
		return cmdAsk(settingsPath, commandArgs, out)
	case "resume":
		return cmdResume(settingsPath, commandArgs, out)
	case "run":
		if err := noArguments("run", commandArgs); err != nil {
			return err
		}
		return cmdRun(settingsPath, out)
	case "desktop":
		if len(commandArgs) > 0 && (commandArgs[0] == "install" || commandArgs[0] == "remove") {
			return cmdDesktopMCP(settingsPath, commandArgs, out)
		}
		return cmdDesktop(commandArgs, os.Stdin, out)
	case "doctor":
		return cmdDoctorCompat(settingsPath, commandArgs, out)
	case "mcp":
		profile, mcpArgs, err := peelDesktopProfile(commandArgs)
		if err != nil {
			return err
		}
		if profile != "" {
			_ = os.Setenv("ATENEA_DESKTOP_PROFILE", profile)
		}
		if len(mcpArgs) == 1 && mcpArgs[0] == "--check" {
			return mcpProbe(out)
		}
		if len(mcpArgs) != 0 {
			return contract.Fail(contract.FailureInvalidInput,
				"mcp takes no arguments (or --check): %q", mcpArgs[0])
		}
		return cmdMCP(os.Stdin, out)
	case "service":
		return cmdService(settingsPath, commandArgs, out)
	case "incidents":
		return cmdIncidents(settingsPath, commandArgs, out)
	case "agent":
		return cmdAgent(settingsPath, commandArgs, out)
	case "agent-exec":
		// The far side of a spawn, not something a person types. Atenea
		// names it in the shipped agent declaration and starts it with an
		// assignment on stdin.
		if len(commandArgs) != 1 {
			return contract.Fail(contract.FailureInvalidInput,
				"agent-exec takes one built-in agent name")
		}
		return cmdAgentRun(commandArgs[0], os.Stdin, out)
	case "workflow":
		return cmdWorkflow(settingsPath, commandArgs, out)
	case "traces":
		return cmdTraces(commandArgs, out)
	case "metrics":
		return cmdMetrics(settingsPath, commandArgs, out)
	case "backup":
		return cmdBackup(settingsPath, commandArgs, out)
	case "floor":
		return cmdFloor(settingsPath, commandArgs, out)
	case "config":
		return cmdConfig(settingsPath, commandArgs, out)
	case "wrap":
		// The report goes to stderr because stdout belongs to the child.
		// wrap replaces this process with the client, so anything printed
		// here lands in front of whatever the client writes: `atenea wrap
		// claude -p query | jq` has to receive claude's JSON and nothing
		// of ours. An operator still sees the report -- stderr is the
		// terminal too -- and a pipeline no longer has to.
		return cmdWrap(settingsPath, commandArgs, os.Stderr)
	case "statusline":
		return cmdStatusLine(commandArgs, out)
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
	// A one-shot command runs in a working tree and answers about it, so the
	// repository's own overlay applies. The service does not: it is one
	// process answering about every repository on the machine, and the
	// overlay of whichever directory it was started in would be wrong for
	// all the others.
	load := config.LoadEffective
	if role == core.Service {
		load = config.Load
	}
	cfg, err := load(settingsPath)
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

// dashboardHostsPath is a seam for tests. The production value is the system
// hosts file, but tests must be able to prove idempotency without touching it.
var dashboardHostsPath = dashboard.HostsPath
var dashboardOpen = func(rawURL string) error {
	return dashboard.DefaultLauncher().Open(rawURL)
}

func cmdDashboard(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"dashboard needs an MCP id or 'hosts'")
	}
	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}
	if args[0] == "hosts" {
		return cmdDashboardHosts(cfg, args[1:], out)
	}
	if args[0] == "--check" || args[0] == "--dry-run" {
		return contract.Fail(contract.FailureInvalidInput,
			"dashboard needs an MCP id before its flags")
	}
	flags := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	check := flags.Bool("check", false, "check without opening")
	if err := flags.Parse(args[1:]); err != nil || len(flags.Args()) != 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"dashboard %s takes only --check", args[0])
	}
	entry, err := dashboard.ResolveConfig(cfg, args[0])
	if err != nil {
		if args[0] == "serena" && (errors.Is(err, dashboard.ErrNotDeclared) || errors.Is(err, dashboard.ErrNotFound)) {
			cwd, cwdErr := os.Getwd()
			if cwdErr == nil {
				if discovered, discoverErr := dashboard.DiscoverSerena(context.Background(), cwd); discoverErr == nil {
					entry, err = discovered, nil
				}
			}
		}
	}
	if err != nil {
		if errors.Is(err, dashboard.ErrNotFound) || errors.Is(err, dashboard.ErrNotDeclared) {
			return contract.Fail(contract.FailureNotFound, "%v", err)
		}
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if err := dashboard.Check(context.Background(), entry.URL); err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"dashboard %s is not accessible at %s: %v", entry.ID, entry.URL, err)
	}
	if *check {
		fmt.Fprintf(out, "dashboard %s accessible at %s\n", entry.ID, entry.URL)
		return nil
	}
	if err := dashboardOpen(entry.URL); err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"could not open dashboard %s at %s: %v", entry.ID, entry.URL, err)
	}
	fmt.Fprintf(out, "opened dashboard %s at %s\n", entry.ID, entry.URL)
	return nil
}

func cmdDashboardHosts(cfg config.Config, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("dashboard hosts", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "show the resulting file without writing")
	check := flags.Bool("check", false, "check the resulting file without writing")
	removeObsolete := flags.Bool("remove-obsolete", false, "remove stale Atenea-managed entries")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"dashboard hosts takes --dry-run, --check or --remove-obsolete")
	}
	entries, err := dashboard.AllConfig(cfg)
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	existing, err := dashboard.ReadHosts(dashboardHostsPath)
	if err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"cannot read %s: %v", dashboardHostsPath, err)
	}
	plan, err := dashboard.PlanHosts(existing, entries, *removeObsolete)
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if *check {
		if plan.Changed {
			fmt.Fprintf(out, "dashboard hosts out of date: %s\n", dashboardHostsPath)
		} else {
			fmt.Fprintf(out, "dashboard hosts up to date: %s\n", dashboardHostsPath)
		}
		return nil
	}
	if *dryRun {
		fmt.Fprint(out, plan.Content)
		return nil
	}
	if !plan.Changed {
		fmt.Fprintf(out, "dashboard hosts already up to date: %s\n", dashboardHostsPath)
		return nil
	}
	if err := dashboard.WriteHosts(dashboardHostsPath, plan.Content); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"cannot write %s: %v", dashboardHostsPath, err)
	}
	fmt.Fprintf(out, "dashboard hosts updated: %s\n", dashboardHostsPath)
	return nil
}

func cmdStatus(settingsPath string, out io.Writer) error {
	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}
	// Ask the running service rather than working the screen out from disk.
	//
	// Not an optimization. Half of what is printed below is only true of the
	// process that maintains it -- the uptime, the chats open right now, what
	// the clock has actually run -- and a command maintains none of it. The
	// chats table is the clearest case: no command could ever have filled one
	// in, and for the whole life of this CLI before the door existed there was
	// nothing to fill it from, so the screen did not print one at all.
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
	// Who answered, not what is printing. The label used to read `process`,
	// which invited "the process running this command" -- and the value has
	// meant the opposite of that since the door existed: a command that
	// reached the service shows the service's numbers, and one that found
	// nobody shows what it could work out from disk alone. A reader who sees
	// `service` is looking at a clock that is really ticking, chats that are
	// really open and a `recovered` line that really swept something.
	//
	// `atenea detect` prints the same claim in the same words, one screen
	// over, because it is the same question about a different kind of answer.
	fmt.Fprintf(out, "answered  %s\n", status.Role)
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
	fmt.Fprintf(out, "  surfaces   %s\n", orDash(strings.Join(agent.Surfaces, ", ")))
	fmt.Fprintf(out, "  serves     %s\n", orDash(strings.Join(agent.Serves, ", ")))
	if len(agent.Unreachable) > 0 {
		fmt.Fprintf(out, "  no runner  %s\n", strings.Join(agent.Unreachable, ", "))
	}
	fmt.Fprintf(out, "  standing   %s\n", orDash(strings.Join(agent.Standing, ", ")))
	// The list prints either way, with a note when it is a copy: two identical
	// lists printed without one would look like two decisions, and only one
	// was made. The copy is the one that moves when the line above does, which
	// is the sharp edge this line exists to keep visible.
	clients := orDash(strings.Join(agent.ClientFloor, ", "))
	if agent.ClientFloorInherited {
		clients += "  (inherited: widening standing widens clients)"
	}
	fmt.Fprintf(out, "  clients    %s\n", clients)
	// Printed only when it is on. A line saying a control is doing its job
	// teaches an operator to skim past it, and this is the one line that must
	// still be read on the day it appears.
	if agent.LookThenAct {
		fmt.Fprintf(out, "  desktop    look-then-act ALLOWED over %s -- a chat may act on what it read\n",
			agent.DesktopScope)
	}
	fmt.Fprintf(out, "  parallel   %s\n", ceiling(agent.MaxParallel))
	fmt.Fprintf(out, "  runs       %s\n", agent.Checkpoints)

	printChats(out, status)
	printBackground(out, status)
	printProcesses(out, status)
	printServers(out, status)

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
		fmt.Fprintf(out, "  %-16s %-28s scale=%-8s vcs=%-8s languages=%s  indexes=%s\n",
			repo.ID, repo.Path, orDash(repo.Scale), orDash(repo.VCS),
			orDash(strings.Join(repo.Languages, ",")),
			orDash(strings.Join(repo.Indexes, ",")))
	}
	return nil
}

// printChats shows who is connected right now.
//
// This table exists because the isolation is otherwise invisible. Two clients
// each get their own chat, their own grant and their own view of what a run
// discovered, and until somebody can see two rows here that is a claim in a
// design document rather than a fact about the machine.
//
// The column is `adds` and not `grant` because that is the honest word. A
// session grant is additive: what a chat's runs may actually do is the
// settings file's standing grant plus this, so a chat showing nothing is not
// a chat that can do nothing -- it is one that asked for nothing on top. The
// two are printed apart rather than summed because they are set by different
// people: the floor by whoever owns the settings file, the addition by
// whoever opened the chat.
//
// Only the service can answer any of it, so it is the one part of this screen
// that depends on having reached one. A command working from disk prints
// nothing here, and correctly: it is not that no client is connected, it is
// that this process has no way to know. The `process` line above already said
// which kind of screen this is.
func printChats(out io.Writer, status core.Status) {
	if len(status.Chats) == 0 {
		return
	}
	fmt.Fprintf(out, "\nchats\n")
	for _, chat := range status.Chats {
		fmt.Fprintf(out, "  %-16s %-10s up %-8s runs %d  adds=%s\n",
			chat.Client, chat.ID, chat.Uptime, chat.Runs,
			orDash(strings.Join(chat.Grant, ",")))
	}
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

// printServers is the section for every [[mcp_server]] the settings file
// declares, whether or not anything has touched it.
//
// Unlike processes it lists a row per declaration rather than per live thing,
// because the fault it exists to catch is a declaration with nothing behind
// it. Three servers were dead for hours on this machine and the only visible
// symptom was that their tools were not in tools/list: an absence, and an
// absence is exactly what a screen cannot show.
//
// The three states are never printed as bare interchangeable words. "ok" is
// the only one that carries an age, "failed" is upper-cased and carries the
// cause, and "unknown" always carries the sentence that says nobody has asked
// -- because a reader taking grey for green is the whole bug, one layer up.
func printServers(out io.Writer, status core.Status) {
	if len(status.Servers) == 0 {
		return
	}
	fmt.Fprintf(out, "\nservers\n")
	for _, s := range status.Servers {
		state := string(s.State)
		if s.State == core.BackendFailed {
			state = strings.ToUpper(state)
		}
		note := ""
		switch {
		case s.State == core.BackendUnknown:
			note = "(nobody has asked yet)"
		case s.Reason != "":
			note = "(" + s.Reason + ")"
		}
		age := "-"
		if !s.LastChecked.IsZero() {
			age = time.Since(s.LastChecked).Truncate(time.Second).String() + " ago"
		}
		line := fmt.Sprintf("  %-7s %-16s %-5s expose=%-4s checked=%-12s %s",
			state, s.ID, s.Transport, s.Expose, age, orDash(s.Where))
		if note != "" {
			line += "  " + note
		}
		if s.Dashboard != "" {
			line += "  dashboard=" + s.Dashboard
		}
		fmt.Fprintln(out, strings.TrimRight(line, " "))
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

// claimStateUnderneath takes the upkeep claim when an operation is about to
// swap the directory a running Atenea is maintaining.
//
// core.claimUpkeep exists so that exactly one process sweeps receipts, ticks
// the clock and drives the flush, the roll-up and the backup. It guards a
// service against a second service; it does nothing about a command that
// renames the whole state root out from under the one holding it. That is what
// `backup restore --replace` and `backup promote` do, and the running service
// would go on writing receipts into a directory that has been moved aside --
// into the copy retained for rollback, where the next start does not look.
//
// So the same claim is taken here, on the same file, and a held one is a
// refusal naming the pid: the operator's answer is to stop the service, which
// they can only do if they are told who it is.
//
// Only when the destination is the state root, or a directory containing it.
// Restoring a snapshot into a scratch directory to look inside it touches
// nothing the service owns, and taking a lock for that would refuse a
// harmless read on behalf of a process it cannot disturb.
func claimStateUnderneath(target string) (func(), error) {
	state := platform.StateDir()
	absolute, err := filepath.Abs(target)
	if err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"backup: cannot resolve %s: %v", target, err)
	}
	if absoluteState, err := filepath.Abs(state); err == nil {
		state = absoluteState
	}
	if state != absolute && !strings.HasPrefix(state, absolute+string(filepath.Separator)) {
		return func() {}, nil
	}
	path := filepath.Join(state, "upkeep.lock")
	release, err := pidlock.Claim(path)
	switch {
	case errors.Is(err, pidlock.ErrHeld):
		return nil, contract.Fail(contract.FailureUnavailable,
			"another atenea has the upkeep of %s (pid %d, %s): stop it before swapping the state it is writing into",
			state, pidlock.Holder(path), path)
	case err != nil:
		return nil, contract.Fail(contract.FailurePermissionDenied,
			"claiming the upkeep at %s: %v", path, err)
	}
	return release, nil
}

// cmdBackup lists or restores state without starting the service or any
// provider. In-place replacement is explicitly opt-in and retains the old
// directory beside the new one for rollback.
func cmdBackup(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"backup needs 'list' or 'restore SNAPSHOT TARGET'")
	}
	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}
	store, err := backup.New(backup.Options{
		Source: platform.StateDir(),
		Dir:    cfg.Backup.Dir,
		Keep:   cfg.Backup.Keep,
	})
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return contract.Fail(contract.FailureInvalidInput, "backup list takes no arguments")
		}
		snapshots, err := store.List()
		if err != nil {
			return err
		}
		if len(snapshots) == 0 {
			fmt.Fprintln(out, "no backups found")
			return nil
		}
		for _, snapshot := range snapshots {
			fmt.Fprintf(out, "%s\t%s\n", snapshot.Name, snapshot.Path)
		}
		return nil
	case "restore":
		replace := len(args) == 4 && args[3] == "--replace"
		if len(args) != 3 && !replace {
			return contract.Fail(contract.FailureInvalidInput,
				"backup restore needs SNAPSHOT and TARGET, with optional --replace")
		}
		if len(args) == 4 && !replace {
			return contract.Fail(contract.FailureInvalidInput,
				"backup restore accepts only --replace after TARGET")
		}
		if replace {
			release, err := claimStateUnderneath(args[2])
			if err != nil {
				return err
			}
			defer release()
			previous, err := store.RestoreInPlace(context.Background(), args[1], args[2])
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "restored %s into %s; previous state retained at %s\n", args[1], args[2], previous)
			return nil
		}
		if err := store.Restore(context.Background(), args[1], args[2]); err != nil {
			return err
		}
		fmt.Fprintf(out, "restored %s into %s\n", args[1], args[2])
		return nil
	case "promote":
		if len(args) != 2 {
			return contract.Fail(contract.FailureInvalidInput,
				"backup promote needs TARGET")
		}
		release, err := claimStateUnderneath(args[1])
		if err != nil {
			return err
		}
		defer release()
		current, err := store.PromotePrevious(context.Background(), args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "promoted previous state into %s; current state retained at %s\n",
			args[1], current)
		return nil
	case "discard":
		if len(args) != 3 || args[2] != "--confirm" {
			return contract.Fail(contract.FailureInvalidInput,
				"backup discard needs TARGET and --confirm")
		}
		if err := store.DiscardPrevious(context.Background(), args[1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "discarded previous state for %s\n", args[1])
		return nil
	default:
		return contract.Fail(contract.FailureInvalidInput,
			"unknown backup action %q; use list, restore, promote or discard", args[0])
	}
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
	// Widths come from the rows rather than from a number guessed before the
	// catalog existed: "symbol.implementations" already outgrows any column a
	// person would pick, and a table that shifts by six characters on the one
	// row with a long name is a table nobody trusts the rest of.
	width := func(header string, of func(metrics.Row) string) int {
		n := len(header)
		for _, r := range rows {
			if got := len(of(r)); got > n {
				n = got
			}
		}
		return n
	}
	wc := width("capability", func(r metrics.Row) string { return r.Capability })
	wi := width("implementation", func(r metrics.Row) string { return r.Implementation })
	wr := width("repository", func(r metrics.Row) string { return r.Repository })
	// The version is a column and not a footnote because the base keys on it:
	// two rows for one implementation are two versions of its tool, and
	// hiding what split them leaves the same capability, implementation and
	// repository on screen twice with no visible reason.
	wv := width("version", func(r metrics.Row) string { return orDash(r.ToolVersion) })
	fmt.Fprintf(out, "%-*s %-*s %-*s %-*s %8s %8s %8s %10s %10s\n",
		wc, "capability", wi, "implementation", wr, "repository", wv, "version",
		"tries", "failed", "priced", "each", "worst")
	for _, r := range rows {
		// The three counts sit next to each other because the gap between them
		// is the diagnosis. Attempts with no priced calls is a provider with a
		// long record and no cost at all -- it ranks on the estimate somebody
		// typed, however many times it has run.
		each := "-"
		if r.Successes > 0 {
			each = r.Mean.String()
		}
		fmt.Fprintf(out, "%-*s %-*s %-*s %-*s %8d %8d %8d %10s %10s\n",
			wc, r.Capability, wi, r.Implementation, wr, r.Repository,
			wv, orDash(r.ToolVersion),
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

// cmdDetect answers two questions an operator has at the same moment: are the
// declared servers actually there, and does any attached provider already hold
// a ready index.
//
// The servers come first because they are underneath. An index that cannot be
// reported because the provider never started is one fact, not two, and
// reading the reachability line first is what turns the second line from a
// puzzle into a consequence.
//
// Unlike select and ask it defaults to every repository rather than requiring
// one: sweeping the whole catalog is the common case, a single dispatch target
// is not.
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	detection, by, err := detectVia(ctx, settingsPath, repository)
	if err != nil {
		return err
	}
	if jsonOut {
		return printDetectionJSON(out, detection, by)
	}
	printServerProbes(out, detection.Servers, by)
	printIndexReports(out, detection.Indexes)
	return nil
}

// answeredBy is who earned the verdicts a detect is about to print.
//
// It is part of the answer rather than a decoration. Two probes of the same
// declaration from two environments disagree honestly -- that is the fault this
// command was rebuilt for -- so a reader who is not told which one ran has been
// handed a verdict they cannot use.
type answeredBy struct {
	Service bool
	// PID is the service that answered, zero when this command did.
	PID int
	// Elsewhere names the settings file a live service is running when that
	// service did not answer this question. Falling back without saying so is
	// how a caller ends up believing no service exists, or that the wrong file
	// was probed.
	Elsewhere string
	// Refused separates "a service answered about another file" from "a
	// service is there and would not answer at all". The second is what an
	// older build looks like from here -- it does not know this method -- and
	// reporting it as "nobody answered" would send the reader looking for a
	// service that is running in front of them.
	Refused bool
}

// detectVia asks the running service to probe and falls back to probing here.
//
// The precedence is the one `atenea status` already uses, including its guard:
// only a service running the same settings file may answer, because naming a
// file asks what that file gives. The fallback is not a nicety -- detect is the
// command you run when things are broken, and needing a healthy service to
// diagnose a sick one would be backwards.
func detectVia(ctx context.Context, settingsPath, repository string) (core.Detection, answeredBy, error) {
	cfg, err := config.Load(settingsPath)
	if err != nil {
		return core.Detection{}, answeredBy{}, err
	}
	var by answeredBy
	switch detection, ok := core.AskedDetect(ctx, repository); {
	case ok && detection.Settings == cfg.Source:
		return detection, answeredBy{Service: true, PID: detection.PID}, nil
	case ok:
		by.Elsewhere = detection.Settings
	default:
		// The door may still be open even though this question came back
		// unanswered: an older service does not know the method. Asking the
		// oldest method there is separates the two, and it is the same call
		// `atenea status` makes.
		if status, alive := core.Asked(); alive {
			by.Elsewhere, by.Refused = status.Settings, true
		}
	}
	atenea, err := core.New(cfg, core.Command)
	if err != nil {
		return core.Detection{}, answeredBy{}, err
	}
	defer func() { _ = atenea.Shutdown() }()
	detection, err := atenea.Detect(ctx, repository)
	if err != nil {
		return core.Detection{}, answeredBy{}, err
	}
	return detection, by, nil
}

// printServerProbes reports every declared server with a verdict and a reason.
//
// Every declaration gets a line, including the ones no capability and no raw
// namespace ever reaches: headroom on this machine is declared, is not raw and
// is named as provider by no implementation, so the status screen can only ever
// call it unknown and this command is the only place it can be given a real
// verdict. That is also the server that was invisible when this started.
//
// The pinned/inherited column is not decoration. A verdict earned under the
// caller's PATH does not transfer to a service started by systemd with a
// minimal one, and that difference is exactly how three servers stayed dead
// for hours while every hand-run check passed.
func printServerProbes(out io.Writer, servers []core.ServerProbe, by answeredBy) {
	if len(servers) == 0 {
		// "Nothing is declared" is a claim about a file, so it owes the same
		// signature as a verdict does: read from the service's declarations it
		// means something different than read from this command's, and a
		// reader cannot tell those apart from the sentence alone.
		fmt.Fprintln(out, "no [[mcp_server]] is declared")
		fmt.Fprintln(out, "  "+answeredLine(by))
		return
	}
	fmt.Fprintf(out, "servers\n")
	for _, s := range servers {
		verdict := "UNREACHABLE"
		detail := s.Reason
		if s.OK {
			verdict = "reachable"
			detail = strings.TrimSpace(s.Name + " " + s.Version)
		}
		path := "inherited PATH"
		if s.PinnedPath {
			path = "own PATH"
		}
		fmt.Fprintf(out, "  %-11s %-16s %-5s expose=%-4s %-14s %6s  %s\n",
			verdict, s.ID, s.Transport, s.Expose, path,
			s.Took.Truncate(time.Millisecond), orDash(detail))
		fmt.Fprintf(out, "  %-11s %-16s where=%s\n", "", "", orDash(s.Where))
		if s.Dashboard != "" {
			fmt.Fprintf(out, "  %-11s %-16s dashboard=%s\n", "", "", s.Dashboard)
		}
	}
	fmt.Fprintln(out, "  "+answeredLine(by))
	// The caveat belongs to one branch only. When the service answered, these
	// verdicts were earned in the environment that matters, and repeating the
	// old warning would teach a reader to ignore warnings. When this command
	// answered, the difference is the whole finding: measured on this machine,
	// a service with a minimal PATH had context7 dead while a shell called the
	// same server reachable in the same minute.
	if !by.Service {
		if inherited := inheritedPATH(servers); inherited > 0 {
			fmt.Fprintf(out, "  %d server(s) marked \"inherited PATH\" can answer differently\n"+
				"  inside the service; start it and ask again to be sure\n", inherited)
		}
	}
	fmt.Fprintln(out)
}

// answeredLine says who probed, in one line, always printed.
//
// Always, including the ordinary case, because the alternative is a reader
// inferring it from the absence of a warning. The pid is there so the claim can
// be checked rather than believed.
func answeredLine(by answeredBy) string {
	if by.Service {
		return fmt.Sprintf("answered by the service (pid %d), in its environment", by.PID)
	}
	const here = "answered by this command, in its own environment"
	switch {
	case by.Refused:
		return here + " (a service is running on " + by.Elsewhere +
			" and did not answer atenea/detect)"
	case by.Elsewhere != "":
		return here + " (a service is running, on " + by.Elsewhere + ")"
	}
	return here + " (no service answered)"
}

// inheritedPATH counts the servers whose verdict depends on who ran the probe.
func inheritedPATH(servers []core.ServerProbe) int {
	count := 0
	for _, s := range servers {
		if !s.PinnedPath {
			count++
		}
	}
	return count
}

// cmdIntent reads the client configuration a team keeps in this repository and
// says how Atenea answers each thing it asks for.
//
// Settings only, no Core, for the reason `wrap` gives: a Core opens the
// measurement base and may start a managed backend, and this command reads
// files and prints. It is also the whole point of the feature -- a command
// that launched something while reporting on a file that arrived by git clone
// would be the failure it exists to prevent.
func cmdIntent(settingsPath string, args []string, out io.Writer) error {
	var jsonOut bool
	flags := flag.NewFlagSet("intent", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&jsonOut, "json", false, "print the result as json instead of prose")
	if err := flags.Parse(args); err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}

	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"cannot read the working directory: %v", err)
	}
	root, ok := config.RepoRoot(cwd)
	if !ok {
		return contract.Fail(contract.FailureNotFound,
			"%s is not inside a repository: the unit of work is the repository, and client config lives at its root", cwd)
	}

	reading, err := clientconfig.Read(root)
	if err != nil {
		return err
	}
	vouched := make([]string, 0, len(cfg.MCPServers))
	for _, server := range cfg.MCPServers {
		vouched = append(vouched, server.ID)
	}
	report := clientconfig.Translate(reading, clientconfig.Catalog{
		Implementations: cfg.Implementations,
		Vouched:         vouched,
	})

	if jsonOut {
		return printIntentJSON(out, report)
	}
	printIntent(out, report)
	return nil
}

func printIntent(out io.Writer, report clientconfig.Report) {
	reading := report.Reading
	fmt.Fprintf(out, "repository %s\n", reading.Root)
	if reading.Empty() {
		fmt.Fprintln(out, "  no client configuration here: nothing is being asked for")
		return
	}
	fmt.Fprintf(out, "read       %s\n", strings.Join(reading.Files, ", "))
	// Before the answers, not after: a file that could not be parsed changes
	// what the list below is worth, and a reader who meets it at the bottom
	// has already believed the list.
	for _, bad := range reading.Unreadable {
		fmt.Fprintf(out, "unreadable %s\n", bad)
	}
	if len(report.Matches) == 0 {
		// The files are here and they ask for nothing. Printing the banner
		// below over an empty space would read as a list that failed to
		// render, which is a worse answer than the true one.
		fmt.Fprintln(out, "  these files declare nothing to answer: no servers, no skills")
		return
	}
	fmt.Fprintln(out, "\nnothing below is launched. Atenea reads these declarations and answers")
	fmt.Fprintln(out, "from its own providers; the commands they carry are dropped when parsed.")

	servers, skills := 0, 0
	for _, match := range report.Matches {
		if match.Request.Kind != clientconfig.KindServer {
			skills++
			continue
		}
		if servers == 0 {
			fmt.Fprintln(out, "\nasked for")
		}
		servers++
		state := ""
		if !match.Request.Enabled {
			state = ", off in the project's settings"
		}
		fmt.Fprintf(out, "  %-10s %s (%s%s)\n",
			match.Answer, fromRepository(match.Request.Name), match.Request.Transport, state)
		if len(match.Sources) > 1 {
			// Two clients asking for one backend. Said out loud, because the
			// row above collapsed them, and a count that quietly differs from
			// the file list is the thing this command exists not to do.
			fmt.Fprintf(out, "             declared in %s\n", strings.Join(match.Sources, " and "))
		}
		if match.Disagreement != "" {
			fmt.Fprintf(out, "             inconsistent: %s\n", match.Disagreement)
		}
		if len(match.Capabilities) > 0 {
			fmt.Fprintf(out, "             -> provider %s: %s\n",
				match.Provider, strings.Join(match.Capabilities, ", "))
			fmt.Fprintf(out, "                via %s\n", strings.Join(match.Implementations, ", "))
		}
		if match.Note != "" {
			fmt.Fprintf(out, "             %s\n", match.Note)
		}
	}

	if skills > 0 {
		fmt.Fprintf(out, "\nalso carried: %d skill(s), prose a client loads\n", skills)
		for _, match := range report.Matches {
			if match.Request.Kind != clientconfig.KindSkill {
				continue
			}
			suffix := ""
			if len(match.Sources) > 1 {
				suffix = fmt.Sprintf(" (in %s)", strings.Join(match.Sources, " and "))
			}
			fmt.Fprintf(out, "  %s%s\n", fromRepository(match.Request.Name), suffix)
		}
	}

	// Last and counted. The whole point of the report is that this list is
	// visible rather than implied by the absence of a line.
	unmatched := report.Unmatched()
	if len(unmatched) == 0 {
		if servers > 0 {
			fmt.Fprintln(out, "\nevery server this project asks for has an answer here.")
		}
		return
	}
	fmt.Fprintf(out, "\n%d unmatched: this project asks for them and nothing here provides them\n", len(unmatched))
	for _, match := range unmatched {
		fmt.Fprintf(out, "  %s (%s)\n", fromRepository(match.Request.Name), strings.Join(match.Sources, ", "))
	}
}

// fromRepository prepares a string that arrived with a git clone for a
// terminal.
//
// clientconfig's own package doc says a repository is untrusted input, and it
// acts on that by dropping the command, arguments, environment and URL of
// every declaration it reads. A Request.Name does not get the same treatment:
// it is the raw key of the mcpServers object, whatever the file put there,
// including escapes that redraw the screen and newlines that forge extra rows
// in this very report. This is the print edge, so this is where it stops
// being a terminal instruction and becomes one bounded line of text.
func fromRepository(value string) string {
	return clip(oneLine(value))
}

func printIntentJSON(out io.Writer, report clientconfig.Report) error {
	type item struct {
		Kind            string   `json:"kind"`
		Name            string   `json:"name"`
		Sources         []string `json:"sources"`
		Enabled         bool     `json:"enabled"`
		Transport       string   `json:"transport,omitempty"`
		Answer          string   `json:"answer"`
		Provider        string   `json:"provider,omitempty"`
		Capabilities    []string `json:"capabilities,omitempty"`
		Implementations []string `json:"implementations,omitempty"`
		Note            string   `json:"note,omitempty"`
		Disagreement    string   `json:"disagreement,omitempty"`
	}
	payload := struct {
		Repository string   `json:"repository"`
		Files      []string `json:"files"`
		Unreadable []string `json:"unreadable,omitempty"`
		Items      []item   `json:"items"`
		Unmatched  int      `json:"unmatched"`
	}{
		Repository: report.Reading.Root,
		Files:      report.Reading.Files,
		Unreadable: report.Reading.Unreadable,
		Items:      make([]item, 0, len(report.Matches)),
		Unmatched:  len(report.Unmatched()),
	}
	for _, match := range report.Matches {
		payload.Items = append(payload.Items, item{
			Kind:            string(match.Request.Kind),
			Name:            match.Request.Name,
			Sources:         match.Sources,
			Enabled:         match.Request.Enabled,
			Transport:       string(match.Request.Transport),
			Answer:          string(match.Answer),
			Provider:        match.Provider,
			Capabilities:    match.Capabilities,
			Implementations: match.Implementations,
			Note:            match.Note,
			Disagreement:    match.Disagreement,
		})
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
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
	var prefer string
	flags := flag.NewFlagSet("select", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&repository, "repo", "", "repository id (defaults to the only one registered)")
	flags.StringVar(&prefer, "prefer", "", "one-call implementation preference")
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

	decision, selectErr := atenea.SelectWithPreference(capabilityID, repository, prefer)
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
	var confirm bool
	var budget float64
	flags := flag.NewFlagSet("task", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Var(&repositories, "repo", "repository to act on; repeat for several (default: all)")
	flags.Var(&allow, "allow", "effect beyond reading to grant this commission; repeat for several (default: none)")
	flags.Float64Var(&budget, "budget", 0, "what this commission may spend in usd (default: the settings file)")
	flags.BoolVar(&confirm, "confirm", false, "require an interactive TTY confirmation before execution")
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
	if confirm {
		if err := confirmTTY(out, "task "+text, budget, effects); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, runErr := atenea.Do(ctx, orchestrator.Task{
		Text: text, Repositories: repositories, Effects: effects, BudgetUSD: budget,
	})
	if result != nil {
		if jsonOut {
			// The write failure wins over the verdict. A caller reading this
			// stream got nothing, so reporting the run's own outcome to it
			// would be answering a question it never heard asked.
			if err := printResultJSON(out, result); err != nil {
				return err
			}
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
	var payloadFile string
	var allow effectList
	var repository string
	var trace bool
	var jsonOut bool
	var confirm bool
	var budget float64
	var prefer string
	flags := flag.NewFlagSet("ask", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&repository, "repo", "", "repository to ask about (required when several are registered)")
	flags.Var(&fields, "set", "payload field as name=value; repeat for several")
	flags.StringVar(&payloadFile, "payload", "", "read the whole payload from a json file, for inputs --set cannot express")
	flags.Var(&allow, "allow", "effect beyond reading to grant this question; repeat for several (default: none)")
	flags.Float64Var(&budget, "budget", 0, "what this question may spend in usd (default: the settings file)")
	flags.StringVar(&prefer, "prefer", "", "one-call implementation preference")
	flags.BoolVar(&confirm, "confirm", false, "require an interactive TTY confirmation before execution")
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
	payload, err := askPayload(capability, fields, payloadFile)
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
	if confirm {
		if err := confirmTTY(out, "ask "+capabilityID+" on "+repository, budget, effects); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, runErr := atenea.Ask(ctx, orchestrator.Question{
		Capability: capabilityID,
		Repository: repository,
		Payload:    payload,
		Effects:    effects,
		BudgetUSD:  budget,
		Prefer:     prefer,
	})
	if result != nil {
		if jsonOut {
			if err := printResultJSON(out, result); err != nil {
				return err
			}
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
			if err := printResultJSON(out, result); err != nil {
				return err
			}
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

// askPayload builds the payload from whichever of the two ways the caller
// used, and refuses both at once.
//
// --set is the right shape for what a shell is good at, and it deliberately
// refuses a record: "Records are a shape a shell cannot express without
// becoming a JSON parser. Refusing is honest; a half-parser would be worse."
// That refusal still stands. What it did not anticipate is a capability whose
// REQUIRED input is a record_list -- web.extract's `fields` -- which made the
// capability reachable from every MCP client and from nothing on the command
// line.
//
// So the second way is a whole payload as JSON, read from a file, and it is
// not a parser bolted onto --set: it is the same document an MCP client would
// have sent. Nothing types it here either, because contract.ValidateInput
// types it a few lines below against the same declaration -- and JSON numbers
// arriving as float64 are already what every adapter reads, since the MCP path
// has always delivered them that way.
//
// The two are mutually exclusive. Merging them would mean deciding which wins
// per field, which is a rule nobody would remember and both sides could
// disagree about.
func askPayload(capability contract.Capability, fields fieldList, file string) (map[string]any, error) {
	if file == "" {
		return fields.payload(capability)
	}
	if len(fields) > 0 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"--payload and --set cannot both be given: one is the whole payload and the other is a field of it")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"--payload %s: %v", file, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"--payload %s is not a json object: %v", file, err)
	}
	if out == nil {
		// `null` decodes without error into a nil map, and a nil payload
		// reaching ValidateInput reads as "no fields given" rather than as the
		// malformed file it is.
		return nil, contract.Fail(contract.FailureInvalidInput,
			"--payload %s is empty", file)
	}
	return out, nil
}

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
		//
		// The refusal predates any capability that could trip it, and it was
		// right then and is right now. What was missing was the other half of
		// the sentence: a caller told only what they cannot do has to go and
		// find out what they can, and web.extract was reachable from every MCP
		// client and from nothing here.
		return nil, contract.Fail(contract.FailureInvalidInput,
			"%s is a %s, which --set cannot express -- pass the whole payload as json with --payload FILE",
			field.Name, field.Type)
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

// confirmTTY is an explicit interactive safety boundary for direct commands.
// Non-interactive callers keep the existing fail-closed behavior and can use
// their deliberate --allow flags without a prompt that would block a script.
// The opt-in flag makes the approval visible and auditable when a human is at
// the terminal.
func confirmTTY(out io.Writer, action string, budget float64, effects []contract.Effect) error {
	info, err := os.Stdin.Stat()
	if err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"confirmation requires an interactive terminal: %v", err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return contract.Fail(contract.FailurePermissionDenied,
			"--confirm requires an interactive terminal; refusing non-interactive execution")
	}
	grant := "settings default"
	if budget > 0 {
		grant = fmt.Sprintf("$%.2f", budget)
	}
	allowed := "read"
	if len(effects) > 0 {
		names := make([]string, 0, len(effects)+1)
		names = append(names, "read")
		for _, effect := range effects {
			names = append(names, effect.String())
		}
		allowed = strings.Join(names, ", ")
	}
	fmt.Fprintf(out, "about to execute %s\n", action)
	fmt.Fprintf(out, "  budget: %s  effects: %s\n", grant, allowed)
	fmt.Fprint(out, "confirm [y/N]: ")
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && len(answer) == 0 {
		return contract.Fail(contract.FailureCanceled, "confirmation was not received")
	}
	if strings.EqualFold(strings.TrimSpace(answer), "y") ||
		strings.EqualFold(strings.TrimSpace(answer), "yes") {
		return nil
	}
	return contract.Fail(contract.FailureCanceled, "execution was not confirmed")
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
	// Two figures, because a wave makes them different: the sum is what the
	// tools charged and the wall is what the operator waited. Printing only the
	// sum reports a parallel run as slower than it was, and leaves the whole
	// return on running a wave invisible.
	fmt.Fprintf(out, "spent     %s of tool time over %d step(s), %s elapsed\n",
		result.Spent.Duration.Round(time.Millisecond), len(result.Steps),
		result.Elapsed.Round(time.Millisecond))
	for _, phase := range result.Phases {
		fmt.Fprintf(out, "  %-8s %d step(s), %s in %s\n",
			phase.Name, phase.Steps, phase.Spent.Duration.Round(time.Millisecond),
			phase.Elapsed.Round(time.Millisecond))
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
	if !state.Linger && platform.LingerCommand() != "" {
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
	if platform.LingerCommand() == "" {
		fmt.Fprintln(out, "linger    n/a")
	} else {
		fmt.Fprintf(out, "linger    %s\n", yesNo(state.Linger))
	}
	fmt.Fprintf(out, "detail    %s\n", oneLine(state.Detail))
	if state.Installed && !state.Linger && platform.LingerCommand() != "" {
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

// cmdStatusLine puts a line on a client's screen, takes it off, and says which of
// those is true -- the same three verbs as `service`, because it is the same kind
// of job: writing something outside Atenea that reports on something.
//
// Three widgets ship -- statusline.Widgets() is the list, and `atenea statusline
// widgets` prints it. `atenea` is the default because it is the one this
// repository is about; `session-share` and `limits` read only what the client or
// the provider already holds and are asked for by name, since installing them as
// a side effect of wanting the traffic light would put readings on the screen
// nobody requested.
func cmdStatusLine(args []string, out io.Writer) error {
	if len(args) == 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"statusline needs a subcommand: install, uninstall, status or widgets")
	}

	verb, rest := args[0], args[1:]
	if verb == "widgets" {
		for _, w := range statusline.Widgets() {
			fmt.Fprintf(out, "%-14s %s\n", w.Name, w.Summary)
		}
		return nil
	}

	// `status` with no name reports every widget: the question it answers is what
	// is on the screen, and answering it for one of three would be a partial
	// truth printed as a whole one.
	if verb == "status" && len(rest) == 0 {
		for i, line := range statusline.All() {
			if i > 0 {
				fmt.Fprintln(out)
			}
			if err := statusLineStatus(line, out); err != nil {
				return err
			}
		}
		return nil
	}

	line, err := widgetLine(rest)
	if err != nil {
		return err
	}
	switch verb {
	case "install":
		return statusLineInstall(line, out)
	case "uninstall":
		return statusLineUninstall(line, out)
	case "status":
		return statusLineStatus(line, out)
	default:
		return contract.Fail(contract.FailureInvalidInput, "unknown statusline subcommand %q", verb)
	}
}

// widgetLine resolves the widget a verb applies to: the default when nothing is
// named, and an error on anything past the first name so a typo is refused
// instead of being installed as the default.
func widgetLine(rest []string) (statusline.Line, error) {
	switch len(rest) {
	case 0:
		return statusline.New(), nil
	case 1:
		return statusline.For(rest[0])
	default:
		return statusline.Line{}, contract.Fail(contract.FailureInvalidInput,
			"statusline takes one widget at a time: %s", strings.Join(statusline.Names(), " or "))
	}
}

func statusLineInstall(line statusline.Line, out io.Writer) error {
	report, err := line.Install()
	if err != nil {
		return err
	}
	// Both screens name the widget before the paths, because with two installed
	// the paths differ by one word and the name is what the other verbs take.
	fmt.Fprintf(out, "widget    %s\n", line.Widget.Name)
	fmt.Fprintf(out, "plugin    %s\n", report.Plugin)
	fmt.Fprintf(out, "declared  %s\n", report.TUIConfig)
	if !report.Declared {
		fmt.Fprintf(out, "          already listed; only the file was replaced\n")
	}
	if !onPath(statusline.Client()) {
		// Not a failure: a config written before the client is installed is
		// waiting, not broken. Saying nothing would leave somebody looking for a
		// line on a screen that does not exist yet.
		fmt.Fprintf(out, "\n%s is not on PATH: the line is installed and will appear\n", statusline.Client())
		fmt.Fprintf(out, "the first time you run it.\n")
		return nil
	}
	// A TUI reads its plugins once, at startup. Every measurement behind this
	// command was taken on a fresh process for that reason, and an operator who
	// looks at the session already open will see nothing and conclude wrongly.
	fmt.Fprintf(out, "\na running %s loads plugins at startup: restart it to see the line.\n", statusline.Client())
	return nil
}

func statusLineUninstall(line statusline.Line, out io.Writer) error {
	report, err := line.Uninstall()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "widget    %s\n", line.Widget.Name)
	fmt.Fprintf(out, "plugin    %s\n", removedOrAbsent(report.Removed, report.Plugin))
	switch {
	case report.ConfigRemoved:
		fmt.Fprintf(out, "config    removed %s (it held nothing else)\n", report.TUIConfig)
	case report.Undeclared:
		fmt.Fprintf(out, "config    %s no longer lists it\n", report.TUIConfig)
	default:
		fmt.Fprintf(out, "config    %s never listed it\n", report.TUIConfig)
	}
	return nil
}

// statusLineStatus reports and never fails, the same as the service screen: this
// output is read on the machine where something is already wrong.
func statusLineStatus(line statusline.Line, out io.Writer) error {
	state := line.Status()
	// The widget is named first and the fix commands carry that name, because this
	// screen is read with two widgets installed and a hint that says `install`
	// without saying which one would repair the wrong line.
	fmt.Fprintf(out, "widget    %s\n", state.Widget.Name)
	fmt.Fprintf(out, "client    %s\n", statusline.Client())
	fmt.Fprintf(out, "plugin    %s\n", state.Plugin)
	fmt.Fprintf(out, "installed %s\n", yesNo(state.Present))
	fmt.Fprintf(out, "declared  %s\n", yesNo(state.Declared))
	fmt.Fprintf(out, "shipped   %s\n", shortDigest(state.Shipped))
	if state.Present {
		fmt.Fprintf(out, "on disk   %s\n", shortDigest(state.Installed))
	}
	if state.Present && !state.Current {
		// The whole reason the source is embedded. A version on the screen drawn
		// by a file this binary never shipped is the same defect as measuring a
		// process that is not the one running: the reading is about a copy.
		fmt.Fprintf(out, "\nthe installed line is not the one this binary carries.\n")
		fmt.Fprintf(out, "run this to replace it:\n")
		fmt.Fprintf(out, "  atenea statusline install %s\n", state.Widget.Name)
	}
	if state.Present && !state.Declared {
		fmt.Fprintf(out, "\nthe file is there but %s does not list it, so nothing loads it.\n", state.TUIConfig)
		fmt.Fprintf(out, "run this to declare it:\n")
		fmt.Fprintf(out, "  atenea statusline install %s\n", state.Widget.Name)
	}
	return nil
}

func removedOrAbsent(removed bool, path string) string {
	if removed {
		return "removed " + path
	}
	return "was not there: " + path
}

// shortDigest keeps a digest readable on a status screen. Twelve hex characters
// are plenty to tell two builds apart by eye, and the full value is one
// sha256sum away for anybody who wants to compare properly.
func shortDigest(sum string) string {
	if sum == "" {
		return "-"
	}
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

// onPath answers whether a client binary is installed. The lookup returns an
// error, but a missing client is a fact about the machine here and not a fault
// to report, so it is turned into the fact the caller actually wants.
func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func cmdConfig(settingsPath string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return contract.Fail(contract.FailureInvalidInput, "config needs a subcommand: init, path or show")
	}
	switch args[0] {
	case "path":
		fmt.Fprintln(out, config.ResolvePath(settingsPath))
		return nil
	case "show":
		return cmdConfigShow(settingsPath, out)
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

// cmdConfigShow prints the effective settings and, for everything a
// repository is allowed to say, where the value came from.
//
// It exists because the overlay is otherwise only visible in its effects. A
// layer that changes an answer without being able to say so is the kind of
// configuration that gets blamed for the wrong thing, and the question it
// answers -- "is this coming from my repository or from the machine?" -- has
// no other way to be asked.
func cmdConfigShow(settingsPath string, out io.Writer) error {
	global, err := config.Load(settingsPath)
	if err != nil {
		return err
	}
	cfg, err := config.LoadEffective(settingsPath)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "global   %s\n", global.Source)
	if cfg.Local == nil {
		fmt.Fprintf(out, "overlay  none (no %s at the root of this repository)\n",
			filepath.Join(config.LocalDir, config.LocalFile))
	} else {
		verb := "patches"
		if cfg.Local.Added {
			verb = "adds"
		}
		fmt.Fprintf(out, "overlay  %s\n", cfg.Local.Path)
		fmt.Fprintf(out, "         root %s, %s repository %s\n",
			cfg.Local.Root, verb, cfg.Local.Repository)
		fmt.Fprintf(out, "         declares %s\n", strings.Join(cfg.Local.Keys, ", "))
		if len(cfg.Local.Types) > 0 {
			// Named, not counted. A type this machine will spawn that
			// arrived with a clone is the line in this output most worth
			// reading, and "2 agent types" is not that line.
			fmt.Fprintf(out, "         adds agent types %s (each runs a type this machine declared)\n",
				strings.Join(cfg.Local.Types, ", "))
		}
	}

	// Only the repository the overlay is about. The other declared ones are
	// not what this command was asked, and printing forty would bury it.
	for _, repo := range cfg.Repositories {
		if cfg.Local == nil || repo.ID != cfg.Local.Repository {
			continue
		}
		fmt.Fprintf(out, "\nrepository %s  %s\n", repo.ID, repo.Path)
		origin := repositoryOrigin(global, cfg.Local)
		fmt.Fprintf(out, "  %-8s languages   %s\n", origin("languages"), orNone(repo.Languages))
		fmt.Fprintf(out, "  %-8s scale       %s\n", origin("scale"), orUnset(repo.Scale.String()))
		fmt.Fprintf(out, "  %-8s vcs         %s\n", origin("vcs"), orUnset(repo.VCS.String()))
		fmt.Fprintf(out, "  %-8s indexed_by  %s\n", origin("indexed_by"), orNone(repo.Indexes()))
	}

	if len(cfg.Selector.Rules) > 0 {
		fmt.Fprintf(out, "\nselector rules\n")
		for _, rule := range cfg.Selector.Rules {
			origin := "global"
			if cfg.Local != nil && rule.Repository == cfg.Local.Repository &&
				slices.Contains(cfg.Local.Keys, "selector.rule") {
				origin = "local"
			}
			fmt.Fprintf(out, "  %-8s %s in %s -> %s\n",
				origin, rule.Capability, orAny(rule.Repository), rule.Prefer)
		}
	}

	added := len(cfg.Security.Sensitive) - len(global.Security.Sensitive)
	origin := "global"
	if added > 0 {
		origin = fmt.Sprintf("+%d local", added)
	}
	fmt.Fprintf(out, "\nsecurity\n  %-8s sensitive   %d pattern(s)\n", origin, len(cfg.Security.Sensitive))

	// Two different questions, so two commands -- but a reader who has just
	// been shown what settings apply here is exactly the reader who wants to
	// know the repository is also asking for things, and would otherwise
	// never learn the second command exists.
	if cwd, err := os.Getwd(); err == nil {
		if root, ok := config.RepoRoot(cwd); ok {
			if reading, err := clientconfig.Read(root); err == nil && !reading.Empty() {
				fmt.Fprintf(out, "\nclient config\n  this repository also asks for %d thing(s), across %d file(s)\n",
					reading.Asks(), len(reading.Files))
				fmt.Fprintln(out, "  `atenea intent` says how each one is answered")
			}
		}
	}
	return nil
}

// repositoryOrigin answers, per field, whether the effective value was
// declared by the overlay or inherited. A field the overlay did not name is
// the global file's answer, or the compiled fallback when the global file did
// not name it either -- and the difference between those two matters to a
// reader deciding where to edit.
func repositoryOrigin(global config.Config, local *config.Local) func(string) string {
	declared := make(map[string]bool, len(local.Keys))
	for _, key := range local.Keys {
		declared[strings.TrimPrefix(key, "repository.")] = true
	}
	return func(field string) string {
		if declared[field] {
			return "local"
		}
		if local.Added {
			// The global file never declared this repository, so an
			// inherited value came from neither: it is what an undeclared
			// field means.
			return "unset"
		}
		for _, repo := range global.Repositories {
			if repo.ID == local.Repository {
				return "global"
			}
		}
		return "unset"
	}
}

func orNone(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}

func orUnset(value string) string {
	if value == "" {
		return "(unspecified)"
	}
	return value
}

func orAny(repository string) string {
	if repository == "" {
		return "every repository"
	}
	return repository
}

// clients maps a client name to how Atenea hands it a configuration that was
// never written to disk.
//
// The map is the whole extension point, and what qualifies a client for it is
// narrow on purpose: it must take its MCP servers from the environment or from
// its own command line. The guarantee that makes wrap safe to try -- run it
// without wrap and nothing about your setup has changed -- is exactly the
// guarantee a file edit cannot make, so a client configurable only by editing
// a file is left out and named as left out rather than quietly served less.
//
// Three clients accept an ephemeral Atenea overlay: OpenCode reads one
// variable and deep-merges it, while Claude Code and codex take theirs as
// arguments. OMP is deliberately included as a pass-through alias: its MCP
// discovery is file-based and it has no equivalent overlay flag, so wrap must
// not write over the user's project or global configuration.
var clients = map[string]struct {
	env    string
	render func(wrap.Plan, wrap.Core) (string, error)
	flags  func(wrap.Plan, wrap.Core) ([]string, error)
	// variadic says the flags end in a value that keeps eating: Claude's
	// `--mcp-config` swallows every following token that does not begin
	// with a dash. Measured against 2.1.220, all three cases:
	//
	//   claude --mcp-config <json> mcp list   -> MCP config file not found: mcp
	//   claude mcp list --mcp-config <json>   -> error: unknown option
	//   claude --mcp-config <json> -- mcp list -> the subcommand runs
	//
	// So the flags go in front, and a `--` is inserted when the user's own
	// arguments begin with a bare word. Putting them last instead reads as
	// the safe choice and is not: a global flag behind a subcommand is not
	// a global flag any more, it is an unknown option, and every
	// `claude mcp list` through the wrapper dies on it.
	variadic bool
}{
	"opencode": {env: "OPENCODE_CONFIG_CONTENT", render: wrap.Plan.OpenCodePayload},
	"claude":   {flags: wrap.Plan.ClaudeArgs, variadic: true},
	"codex":    {flags: wrap.Plan.CodexArgs},
	"omp":      {},
}

func cmdWrap(settingsPath string, args []string, out io.Writer) error {
	options, err := parseWrapOptions(args)
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput, "%v", err)
	}
	if options.Client == "" {
		return contract.Fail(contract.FailureInvalidInput,
			"wrap needs a client, e.g. atenea wrap opencode")
	}
	name, rest := options.Client, options.ClientArgs
	if name == "chatgpt" {
		if options.EmitConfig {
			return emitChatGPTConfig(settingsPath, options.Profile, os.Stdout)
		}
		installArgs := []string{"install", "chatgpt"}
		if options.Profile != "" {
			installArgs = append(installArgs, "--profile", options.Profile)
		}
		return cmdDesktopMCP(settingsPath, installArgs, out)
	}
	client, ok := clients[name]
	if !ok {
		return contract.Fail(contract.FailureInvalidInput,
			"cannot wrap %q; supported: %s", name, strings.Join(slices.Sorted(maps.Keys(clients)), ", "))
	}
	// Resolved before anything is probed. Spending eleven handshakes to
	// discover the binary is not installed would be the report arriving
	// after the answer that makes it pointless.
	binary, err := exec.LookPath(name)
	if err != nil && !options.EmitConfig {
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
	// A backend somebody's implementation runs against is one Atenea
	// answers for, so the client must not be handed it as well. The
	// catalog is the only place that knows; the backend's own block
	// cannot see who points at it.
	served := make(map[string]bool, len(cfg.Implementations))
	for _, impl := range cfg.Implementations {
		served[impl.Provider] = true
	}
	profiles, err := config.DesktopProfilesFromFile(settingsPath)
	if err != nil {
		return err
	}
	if err := config.ValidateDesktopProfiles(profiles, cfg.MCPServers); err != nil {
		return err
	}
	profile, err := config.ResolveDesktopProfile(profiles, options.Profile, name)
	if err != nil {
		return err
	}
	servers := config.FilterDesktopMCPServers(cfg.MCPServers, profile)
	ctx := context.Background()
	if profile.StartupTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, profile.StartupTimeout)
		defer cancel()
	}
	plan := wrap.Check(ctx, servers, served)
	plan.Report(out, name)

	// The door is this binary. Resolving it here rather than trusting
	// argv[0] means a wrap performed by one build cannot point a client at
	// another one that happens to be earlier on PATH.
	self, err := os.Executable()
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "cannot locate the running atenea: %v", err)
	}
	core := wrap.Core{ID: "atenea", Command: []string{self, "mcp", "--desktop-profile", profile.Name}}

	var flags, env []string
	if client.flags != nil {
		if flags, err = client.flags(plan, core); err != nil {
			return err
		}
		flags = append(supportedClientFlags(binary, profile.ClientFlags), flags...)
	}
	if client.env != "" {
		payload, err := client.render(plan, core)
		if err != nil {
			return err
		}
		env = []string{client.env + "=" + payload}
		if options.EmitConfig {
			fmt.Fprintln(os.Stdout, payload)
			return nil
		}
	}
	if options.EmitConfig {
		fmt.Fprintf(os.Stdout, "%s %s\n", name, strings.Join(flags, " "))
		return nil
	}
	env = append(config.DesktopPolicyEnv(profile), env...)
	return launch(binary, clientArgv(name, flags, rest, client.variadic), env)
}

// clientArgv puts Atenea's flags in front of the user's own arguments, with a
// `--` between them when the leading flag would otherwise eat the first one.
//
// The separator is only ever needed against a bare word. A user argument that
// starts with a dash stops a variadic flag by itself, which is why `-p` and
// `--resume` need nothing here, and why `mcp list` does.
func clientArgv(name string, flags, rest []string, variadic bool) []string {
	argv := make([]string, 0, len(flags)+len(rest)+2)
	argv = append(argv, name)
	argv = append(argv, flags...)
	if variadic && len(flags) > 0 && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		argv = append(argv, "--")
	}
	return append(argv, rest...)
}

// launch replaces this process with the client.
//
// Replacing rather than spawning is what makes the wrapper invisible once it
// has done its job. A parent sitting in the middle would have to forward
// signals, copy three streams, and translate an exit status -- three chances
// to get wrong what the kernel gets right for free. It also settles the
// question of what happens to Atenea while a chat session runs for an hour:
// nothing, because Atenea is no longer there.
func launch(binary string, argv, env []string) error {
	if err := syscall.Exec(binary, argv, append(os.Environ(), env...)); err != nil {
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
	return printable(strings.Join(strings.Fields(value), " "))
}

// printable replaces every C0 control character and DEL with a space.
//
// The text that reaches this screen is stdout and stderr from external CLIs
// (Failure.Raw, Health.Raw, FunnelDrop.Raw) and tool descriptions relayed by
// third-party MCP servers -- none of it written by Atenea, all of it printed
// to a terminal that executes what it is sent. strings.Fields does not catch
// it: it splits on Unicode space, and ESC (0x1b) is not one, so "\x1b[2J"
// survives Fields intact and clears the operator's screen. A provider whose
// error message can erase the report about it is a provider that decides what
// its own incident looks like.
//
// A space, not a drop: a run of escapes collapsing to nothing would leave the
// remaining words glued together, which reads as a different message rather
// than as a censored one. Tab and newline are already gone by the time this
// runs, because Fields split on them.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// clip keeps a status line readable when the provider text behind it runs
// long. The full text is never lost -- it is on the receipt, and --trace
// prints it whole -- this only keeps the short screen short.
func clip(s string) string {
	s = printable(s)
	const limit = 160
	if len(s) > limit {
		// limit is a byte budget, so the cut can fall inside a multi-byte
		// rune; ToValidUTF8 drops exactly the incomplete tail it produces,
		// the same guard truncate carries for the same reason.
		return strings.ToValidUTF8(s[:limit], "") + "..."
	}
	return s
}
