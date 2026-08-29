---
title: Settings
weight: 4
---

# Settings

Atenea is a declarative engine. The catalog of capabilities, the
implementations behind them, the repositories they run against and the user's
selector rules all live in one TOML file. Changing behavior means editing that
file, not the core.

One file on purpose: ceilings, rhythms and catalog in a single place, so
nothing ends up baked into the code or scattered across three configs.

Unknown keys are refused. A typo that is silently ignored is a setting the user
believes is in force and is not.

## What the file replaces, and what it only overrides

Atenea ships a full settings file compiled into the binary, and that is what
runs when no file exists on disk. The moment a file does exist it is used
instead. What *instead* means depends on which part of the file you are looking
at, and the difference is the thing to know before you delete a line.

**The catalog is replaced.** The `[[capability]]`, `[[implementation]]`,
`[[repository]]` and `[[selector.rule]]` blocks are not merged with the shipped
ones: what your file lists is the whole list. `[[mcp_server]]` is the same kind
of list without being part of the catalog, and for it the rule costs nothing:
the shipped file declares none, so there is nothing there to lose. A settings
file containing only

```toml
contract = "3.6.0"

[orchestrator]
runners = ["omp", "claudecode"]
```

is a complete, valid file describing an Atenea that knows no capabilities at
all. It boots, `atenea status` shows the orchestrator red and `serves -`, and
every command answers `unknown capability`. Nothing is hidden and nothing
crashed; you asked for an empty catalog and got one.

**The knobs are overridden.** The ceilings and rhythms — `[core]`,
`[orchestrator]`, `[metrics]`, `[backup]`, and the adapter blocks under them —
are applied key by key on top of a compiled default. The file above sets no
step ceiling and no rhythm at all, yet that same `atenea status` prints
`parallel 4`, `metrics.flush 30s`, `metrics.compact 1h`, `backup 6h` and
`5 of 5 kept`. None of that is in the file; all of it is in force.

So a knob you leave out is not absent, it is whatever the binary falls back to.
Today those constants say exactly what the shipped file says — a test holds
them to it — which is what makes the difference easy to miss. It stops being
invisible on the upgrade that changes one.

**A list you leave out is not an empty list.** `runners`, `skip_dirs`,
`sensitive` and every adapter's `implementations` tell an omitted list apart
from an explicitly empty one, deliberately:

| Written | Means |
| --- | --- |
| nothing | the built-in list stands |
| `[]` | genuinely nothing: dispatch nowhere, skip no directory, treat no file as sensitive |

Strip `implementations` out of `[orchestrator.serena]` and Serena still answers
for all five symbol capabilities. Write `implementations = []` in the same
place and it answers for none, while every other key in the block keeps working.

`effects` is the one list that does not play by this rule, because a grant
nobody wrote down is a grant nobody made. Leave it out and you have none, not
the shipped `["process"]` — and since every implementation of `code.search` is
a binary, a file that omits it answers `permission_denied: code.search causes
process, which the commission does not cover`.

So the way to change one setting is still to start from the whole file:

```sh
atenea config init          # writes the built-in file, catalog and all
atenea config init --force  # overwrites one that is already there
atenea config path          # says where that is
```

then edit it. A bare `init` refuses when a file is already there and names
`--force`, so writing the defaults can never silently discard settings you had.
A full file states every value where you can read it, which is
the only version of this that survives an upgrade. Merging the catalog was
considered and refused: a half-file whose meaning depends on what a particular
binary happened to ship is a file nobody can read on its own. The knobs that do
fall back are precisely where that objection still bites, which is the argument
for pinning them rather than trusting them.

A full rewrite is still the only way to *have* everything a release shipped. It
is no longer the only way to find out you do not: `atenea status` compares your
catalog against the built-in one and prints a `catalog` line beside `settings`
naming the implementations it is missing. It stays quiet about capabilities you
dropped outright, because dropping one is a decision rather than drift. The
warning exists because the drift is otherwise invisible until it bites: a file
written before `0.6.0` kept registering one implementation of `symbol.overview`
after the binary began shipping two, so the funnel ran with a single candidate,
and the day that candidate died there was nothing behind it.

## Skeleton

```toml
contract = "3.6.0"          # required: the contract version this file targets

[core]
shutdown_grace = "10s"      # margin a clean stop gives in-flight work
health_probe_every = "15m"  # background MCP reachability probe; "0s" disables
```

The `contract` line is the one field with no default: a file must say which
core it was written for, and a core refuses a file from a different major
version by name rather than reading it and hoping. Minor lag is supported, so
a file targeting `3.0.0` remains readable by the current `3.6.0` core because
every 3.x addition since has been backward-compatible. A file from a newer contract
is refused and must be reviewed before use:

```text
settings ~/.config/atenea/atenea.toml: contract 4.0.0 is not supported by
this core (3.6.0): change the contract line to "3.6.0"; no other key moves
```

Do that and you are done. The refusal is deliberately not a fallback to the
defaults: a file quietly ignored is a machine running settings nobody chose.
A file from a *newer* core than the binary is told to upgrade the binary
instead, because no edit to the file can fix that one.

## The orchestrator

```toml
[orchestrator]
max_parallel = 4            # steps of one wave at a time; 0 lifts the ceiling
budget_usd = 0.90           # what ONE COMMISSION may spend, across every step
effects = ["process"]       # granted standing to every commission and question
client_effects = ["process"] # the same, for a chat a client opened; also its ceiling
client_denied_capabilities = ["desktop.move", "desktop.click"] # MCP kill switch
  runners = ["omp"]           # any of omp, claudecode, codex, serena, kivgraph, tokensave, local; [] dispatches nowhere
checkpoints = true          # false is the only way to stop writing run receipts
checkpoint_dir = ""         # "" uses $XDG_STATE_HOME/atenea/runs

  [orchestrator.omp]
  binary = "omp"                       # bare name is looked up on PATH
  implementations = ["ripgrep"]        # what the adapter answers for
  match_limit = 10000                  # matches one search asks omp for
  timeout = "30s"                      # after this, omp is stuck, not slow

  [orchestrator.local]
  implementations = ["ripgrep"]        # what the stand-in can actually execute
  # never walked; the stand-in reads no .gitignore, so it is told instead
  skip_dirs = [".git", "node_modules", "vendor", "dist", "build", ".venv", "target"]

  [orchestrator.claudecode]
  source = "auto"                      # terminal or auto; Claude.app is a GUI client
  terminal_binary = "claude"            # Claude Code headless executable
  implementations = ["claude.search"]  # a different id from ripgrep's, on purpose
  # no ceiling here: what a call may spend arrives with the commission
  timeout = "90s"                      # ~13 model turns; the grant bites first

  [orchestrator.codex]
  source = "auto"                      # terminal, app or auto
  terminal_binary = "codex"             # Codex CLI on PATH
  app_binary = "/Applications/ChatGPT.app/Contents/Resources/codex"
  implementations = ["codex.search"]
  timeout = "90s"

  [orchestrator.serena]
  endpoint = "http://127.0.0.1:40010/mcp"   # a server, not a binary
  implementations = ["serena.definition", "serena.references", "serena.implementations",
                     "serena.overview", "serena.symbol_search"]
  timeout = "90s"                      # a language server indexing cold is slow, not stuck

  # Optional. Present means Atenea launches and watches the server itself,
  # instead of expecting one already listening at the `endpoint` above.
  [orchestrator.serena.process]
  command = "serena"                   # required once this table exists
  args = ["start-mcp-server", "--transport", "streamable-http",
          "--host", "127.0.0.1", "--port", "{{port}}", "--project", "{{project}}"]
  env = ["SERENA_LOG_LEVEL=WARNING"]   # added to the inherited environment
  lifecycle = "on_demand"              # required: "persistent" or "on_demand"
  instance = "per_repository"          # "shared" (default) or one copy per repository
  port = 0                             # 0 lets the OS choose; {{port}} receives it
  ready_timeout = "15s"                # one spawn's window to answer the handshake
  restart_limit = 2                    # retries after a crash; 0 never retries
  restart_delay = "2s"                 # pause between a crash and the next try
  stable_after = "30s"                 # uptime that earns a fresh restart budget
  idle_timeout = "5m"                  # on_demand only; refused beside persistent
  stop_grace = "5s"                    # SIGTERM, then SIGKILL

  [orchestrator.tokensave]
  root = "/srv/workspace"              # required, and ABSOLUTE: every path is relative to it
  implementations = ["tokensave.context", "tokensave.calls", "tokensave.overview"]
  timeout = "90s"                      # re-syncs its own index first: kivgraph's class, not omp's

    # Not optional the way Serena's is: a stdio child has no address to dial.
    [orchestrator.tokensave.process]
    command = "tokensave"              # required once this table exists
    args = ["serve", "--path", "/srv/workspace"]
    lifecycle = "on_demand"            # required: "persistent" or "on_demand"
    instance = "shared"                # the only value here; per_repository is refused
    idle_timeout = "10m"               # on_demand only, same as every process table

```

`max_parallel` is the real brake on total memory: four steps of one wave run at
a time so a laptop stays responsive. `0` means no ceiling, which is a choice
for a build machine, not a default.

It only ever comes up when a wave has more than one step in it, and what puts
steps in a wave is **repositories**. A commission looks at each repository in
scope and splits the work per repository, so a settings file with one
`[[repository]]` produces waves one step wide however high this is set, and
`atenea task` on a file with two runs both explores at once and then both
searches at once. The run report is where that shows: it prints what the
commission cost and what it took, and on a wide wave the second number is the
smaller one.

`runners` names the far sides of the contract, and it is a list because several
can be attached at once. `omp` is the client adapter that ships attached.
`claudecode` drives the Claude Code CLI and is off by default, because it is
the only far side that costs money per call. `serena` is not a CLI at all: it
is an MCP server, which is why its block takes a URL instead of a binary, and
it answers the five symbol capabilities rather than a text search.
`kivgraph` and `tokensave` are graph-backed MCP providers. `local` is a
stand-in that searches the disk directly, for a machine with no client
installed. An empty list leaves the core able to plan and choose but unable to
dispatch — a working core with nobody attached, and the status screen says so
rather than failing halfway through a commission.

`local` and `omp` both answer for `ripgrep`, so attaching both is refused by the
rule below and the choice between them is exclusive. It is worth making
deliberately, because the stand-in is not a slower omp — it is a faster one that
answers a slightly different question. Spawning nothing makes it 28–65× quicker
on the repositories this was measured against, with identical results on all
three. But it reads no `.gitignore`, and against a probe repository built to
separate the two rules the answers diverged in both directions: the stand-in
skipped a tracked file under `build/` because the name is on its fixed list, and
returned two `.gitignore`'d files — one of them a private note — that omp did
not. Neither answer is wrong for the rule it applies. Only one of them applies
the rule the repository itself declares, which is why `omp` ships attached and
the speed is not the reason to switch.

Each runner answers for its own implementations, and two of them claiming the
same one is refused at load. Dispatch would still work — whoever was asked
first would answer — but which client ran your work is not a detail to settle
by declaration order.

`match_limit` cannot be `0`. Zero reads like "no limit" and is precisely the
value omp treats as "use a small default", after which it reports the short
answer as complete. Atenea states the number instead, and a search that reaches
it comes back with the ceiling named in `discovered` — a partial answer that
does not say so is a wrong answer.

`budget_usd` is what **one commission** may spend, added up across every step
and every paid provider it dispatches to. It lives under `[orchestrator]` and
not on an adapter because money is a permission, and permissions are granted
per commission: an adapter with a ceiling of its own could only ever cap one
call, so a run of four steps would spend it four times over.

Zero is not a typo and not "no limit": it is a commission that may not spend,
which is exactly what a machine with no paid provider attached wants. Free
providers keep working. A negative number *is* a typo and is refused at load,
because clamping it would silently switch off every paid provider and the
refusals that followed would read as an outage.

A wave hands each of its steps an equal share of whatever is left, so even if
every step spends its share to the last cent the wave cannot draw more than the
commission had. The next wave divides what the last one did not touch. What
each step was actually held to, and what it actually charged, are both on the
receipt — `charged` on the summary, and per step under `--trace`.

One order beats the standing grant, in both directions: `atenea task "..."
--budget 3` funds that commission and nothing else.

Running out is reported as `permission_denied`, not as slowness: the far side
was not slow, the grant was spent. It does not mark the provider down, so the
next step can still go to it — and a step that costs nothing still runs.

`effects` is granted the same way, standing to every commission and question
this core dispatches, on top of the read that is always free. It exists
because not every effect is a per-request choice: `code.search`'s only
implementations today are both a binary, so requiring every caller to grant
`process` by hand on every single call would just move the same yes to one
place instead of saying it once, here. The shipped default turns it on for
exactly that reason — refusing it by default would not make the spawn
auditable, it would make the one P0 capability unusable out of the box.

One order beats the standing grant here too: `atenea task "..." --allow
write` grants one more effect to that one commission, repeatable for
several. Unlike `--budget` on a resume, which replaces what remains of the
grant, `--allow` only ever adds — an effect already held is never worth
losing by accident.

`client_effects` is that same grant for a chat opened by a connected MCP
client, rather than by the person at this terminal. Until it existed there was
one line for both: widening `effects` to `write` for an afternoon's work
widened every client that connected, permanently and silently, and this file
had no way to say otherwise. That is the gap it closes.

It ships equal to `effects` and separate from it, and separate is the part
that matters — after the day you widen your own line, the two are different
numbers. Deleting the key does not turn the rule off: it hands clients
whatever `effects` says, which is what every settings file written before the
key existed already does, so nothing changes by upgrading. `atenea status`
prints both on its `standing` and `clients` lines, and marks the second
`inherited` when it is a copy, because two identical lists with nothing
between them look like two decisions when only one was made.

An empty list is a real answer and not an omission — that is why this key is a
list that may be empty while `effects` is one that may not. `client_effects =
[]` means a connected client may read and nothing else, which on this machine
refuses `code.search`: every implementation of it is a binary and so needs
`process`. That is why it is not the default. A default that refuses the
headline capability on day one does not teach caution, it teaches people to
turn the default off.


The provider behind it, `[orchestrator.desktop]`, is also the one whose helper
is **built on the machine that runs it** and ships in no release. Distributing
a macOS binary that drives the screen needs a Developer ID signature and
notarization; measured on macOS 26.6, an Apple Development signature is
rejected by Gatekeeper on another machine exactly as an unsigned one is, so the
weaker options do not substitute. Code compiled locally carries no quarantine
attribute, so Gatekeeper never enters and no certificate is needed by anybody:

    swift build -c release --package-path helper

A certificate would buy one thing and it is not the right to run: that the
permission survives a rebuild. See `helper/README.md` for the measurements.

`device` is the one effect that argues the other way, and it is on neither
floor as shipped. It marks a capability that reaches the pointer, the keyboard
or the screen, and the permission behind it is not Atenea's to spend: measured
on macOS 26.6, the system attributes its device grant to the responsible
ancestor rather than to the process asking, so an executable whose own
identifier was never authorized reports full access merely for having been
launched from a terminal that has it. Granting `device` on `client_effects`
would therefore hand a connected client something nobody granted Atenea. It
belongs on `effects` alone, typed deliberately, and only where Atenea is that
responsible ancestor.

A chat opened by a client holds this list. That sentence is younger than the
key: in the historical `0.10.0` behavior a chat opened holding nothing at all, so
`client_effects` was a ceiling with no way to reach it, and a raw tool
declaring anything beyond `read` was refused on every machine whatever this
file said. Deleting the key still hands clients whatever `effects` says — the
two readings agree on a file that never wrote the line, which is why upgrading
changes nothing for anyone who never wrote it.

It is also a ceiling, and now that a chat can reach it, the ceiling is a rule
rather than a statement. A client may ask at `initialize` to hold *less* than
this list:

```json
{"capabilities": {"experimental": {"atenea": {"grant": ["read"]}}}}
```

A client that needs `write` for one call in fifty can spend the other
forty-nine unable to make a mistake. Asking for *more* is refused at the door
rather than at the first `tools/call` — a client told at the door can say so,
one told mid-conversation has already promised somebody the work. So is an
effect name nobody recognizes: a client that misspells `write` asked to be
restrained, and handing it the full grant would be the opposite of what it
asked for.

The handshake answers with what the chat actually ended up holding, in the
same block. Before that the only way for a client to learn its own permissions
was to make a call and read the refusal — and a client that has to provoke an
error to find out what it may do will provoke it on the user's work.

A capability declares the effects running it has, and a step is refused when
its permission does not carry one of them — `code.search causes process, which
the commission does not cover`, as a verdict on the step rather than a
transport error. Both grants rest on that one check, which sits in the core
wrapping whichever runner is attached rather than in the adapters.

An implementation no attached runner can execute is not removed from the
catalog. It is dropped by the funnel's `reach` stage, which says so in the
trace — `no attached runner serves it`, not `down`, because a provider nobody
wired up is not a provider that is broken. The status screen lists them up
front, under `no runner`.

`checkpoints` is what turns run receipts off, and the only thing that does.
`checkpoint_dir` follows the ordinary override rule, where an empty string is
indistinguishable from a key nobody wrote and so inherits the default rather
than blanking it: `checkpoint_dir = ""` keeps writing, to
`$XDG_STATE_HOME/atenea/runs`. Only `checkpoints = false` stops it, and it
wins over an explicit path written beside it. `atenea status` reports which
way it went on its `runs` line — a directory, or `off`.

### A far side Atenea launches itself

`serena` is the one far side that is a server rather than a command, so it is
the only one that can be already running, or not running at all, before Atenea
starts. Leaving `[orchestrator.serena.process]` out keeps that arrangement: an
externally managed server, taken on faith at whatever `endpoint` names.
Declaring the table hands the job to Atenea instead — which is what retires a
hand-written systemd unit or a terminal nobody may close.

Declaring it also settles the address. `endpoint` is not what gets dialed and
not what the status screen reports — that is the supervisor's own address:
`127.0.0.1`, the port below, and Serena's MCP path. The written one is still
checked, though, so a file means the same thing whether or not a process table
happens to sit beside it: an `endpoint` that could never work is refused
either way, and deleting the table later cannot turn a file that always loaded
into one that suddenly does not.

`command` and `lifecycle` are the two required keys once the table exists;
every other knob has a default. `persistent` is launched at startup and kept
alive. `on_demand` waits for the first call that needs it and is stopped again
after `idle_timeout` — which is why writing `idle_timeout` beside
`persistent` is refused rather than ignored. The idle reaper skips persistent
servers, so the key would sit there doing nothing with nothing anywhere to say
so. That is the test the whole file is held to: a knob that stops applying has
to have something visible explaining why. `enabled = false` and
`restart_limit = 0` pass it — they sit in the same table as what they switch
off, and `atenea status` reports the result as `off`. This one had no such
witness, so it is a load error instead.

An omitted `port`, or `0`, has the OS pick a free one — and Atenea then has to
tell the server which port it picked, which is what `{{port}}` is for: every
occurrence in `args` becomes the chosen port, the same one the readiness probe
then dials. Pin a port instead when something outside Atenea also has to find
the server; `{{port}}` receives it either way. `env` is added to the
environment Atenea already has rather than replacing it, so `PATH` survives
and a named key overrides.

`restart_limit` counts retries after a crash, not attempts: `2`, the default,
means three spawns before the server is marked down for good, and `0` means
the first crash is final. It is the one knob here where zero is a real
setting rather than a key nobody wrote, which is why leaving it out gives you
`2` and not nothing.

A server that never comes up fails at the guard rather than at the adapter,
because the guard is what waits for it: the call comes back `unavailable`
saying `serena did not come up`, with the reason it did not carried alongside
untranslated.

### One Serena, or one per repository

`instance` says how many copies of the server should exist. `shared`, the
default and what every managed server did before the key existed, is one
process for the machine: whichever repository calls first activates its
project, and a call for a second repository retargets it with
`activate_project`. That retarget is the cost — a language server torn down
and reindexed — and it is paid on every alternation, so work that moves
between two repositories pays it twice per round trip.

`per_repository` spends memory to remove it: one process per `[[repository]]`,
each started against its own project and never retargeted. They are named
after the repository they serve, so `atenea status` lists `serena@api` beside
`serena@web` rather than one row that could be either. Each gets its own port
and its own session, and the funnel reaches whichever one belongs to the
repository in the request.

Two things are refused rather than accepted and quietly broken. `{{project}}`
has to appear in `args` — it is replaced with the repository's path, and
without it every copy would be launched against the same project, which is N
identical servers wearing different names. And `port` cannot be fixed: the
second copy would fail to bind, so the port is Atenea's to pick per process.
The same two rules run the other way too — `{{project}}` under `shared` is
refused, because there is no repository to substitute and the placeholder
would reach the server literally.

Health follows the same split. A provider is not up or down in the abstract:
Serena with no TypeScript language server is down for a TypeScript repository
and alive for a Go one, and under this policy the two are not even the same
process. So what probing finds is recorded per repository, the funnel filters
on what the repository in front of it found, and `atenea status` names the
repository that found the failure — `health=down (on web: ...)`. A verdict
from one repository never refuses work on another.

### The far side that serves one root

`tokensave` is a stdio server rather than an HTTP one, so there is no
`endpoint` for it to fall back on and `[orchestrator.tokensave.process]` is
not the optional table Serena's is. Naming `tokensave` in `runners` without
one is refused at startup — `tokensave has no process to launch -- a stdio
server has no address to dial without one` — because there is nothing else in
the block that could say where the far side is.

`root` is the key no other adapter has, and it is required for the same
reason it exists. `kivgraph` publishes one corpus addressed by repository
*name*, while tokensave serves **one project rooted at a directory** and
speaks paths relative to that root, so every path crossing this adapter is
translated in both directions and the translation has to know where the root
begins. Leave it out and the runner is refused by name:

```text
settings ~/.config/atenea/atenea.toml: orchestrator.tokensave.root is
required -- every path tokensave reports is relative to it
```

It has to be absolute, and a relative one is refused rather than resolved:
"resolved against what" has no honest answer here, because the process that
loads this file is not the one that reads the path six months later from a
systemd unit with a working directory of its own. It is declared rather than
derived from the `[[repository]]` list for the matching reason — a root
guessed from that list is silently wrong the day a repository is added
outside it.

Every repository this provider answers for lives under that root, and the
graph does not stop at a repository boundary: a symbol in one repository can
have callers in another. Those rows have nowhere to travel, since
`symbol.calls` declares repository-relative paths and no field for "another
repository", so they are dropped and the drop comes back as a discovery.
Renaming a foreign path into a repository-relative one would be a lie the
caller could not detect.

The three implementations are what the far side can answer honestly:
`tokensave.context` for `code.context`, `tokensave.calls` for `symbol.calls`
and `tokensave.overview` for `symbol.overview`. Three things are deliberately
absent. No `code.search`, because tokensave has no text engine and a flat
search answered out of a symbol index is a different answer wearing the same
name. No `symbol.definition`, because resolving the symbol under a *position*
back to its declaration needs a type checker and tokensave resolves by name.
And no indexing implementation at all, unlike kivgraph: this far side
re-syncs its index on every call, so there is nothing for the funnel to
trigger.

That re-sync is also what `timeout` is sized for. `90s` is kivgraph's ceiling
rather than omp's because an index brought back into step before the answer
is slow long before it is stuck.

`instance` may only be `shared`, and `per_repository` is refused rather than
accepted: a second copy pointed at the same root would index the same files
twice into the same database. The whole graph belongs to the root, not to a
repository, which is why one process is the honest number.

## The model client

```toml
[model]
backend = "claude"  # protocol: claude or opencode; omitted keeps claude
binary = "claude"    # the CLI that answers a turn; a bare name is looked up on PATH
timeout = "180s"     # per turn; long enough for repository explore and plan turns
explore = ""         # model, or "auto", for repository exploration
plan = ""            # model, or "auto", for the high-reasoning plan role
explore_fallbacks = [] # explicit Claude fallbacks, in declaration order
plan_fallbacks = []    # ignored by Claude: plan is pinned to claude-opus-5
```

This is the seam the two model-backed built-in agents, `explore` and `plan`,
call a model of their own through. It is not a capability provider: nothing
in the catalog dispatches to it, and `[orchestrator]`'s adapters are a
different kind of far side entirely — theirs answer a search, this one
thinks.

`explore` and `plan` name the model for each role, and both ship empty. They
also accept `auto`: Claude exploration considers `claude-sonnet-5` and
`claude-haiku-4-5`, while Claude planning is pinned to `claude-opus-5`.
With OpenCode, plan auto uses `anthropic/claude-opus-5` first and permits the
observed high-reasoning fallbacks `openai/gpt-5.6-sol` and
`openai/gpt-5.6-luna`. A
fresh install spending real money the first time either agent is dispatched
would be exactly the surprise `orchestrator.claudecode` ships off the runner
list to avoid, so a role with no model configured is refused at dispatch
rather than defaulted to whichever model looks cheapest today — there is no
cost data yet to choose one by, and a knob nobody can tune correctly is
worse than a fixed default that is visible in this file and changed by hand
once it is wrong. Fill in a model name or an alias your CLI resolves on its
own (`sonnet`, `opus`, a full name, or `auto`) to turn either agent on.

Both roles read from the same `backend`, `binary` and `timeout`: one protocol,
one CLI and one ceiling, whichever role is asking. `timeout` matches
`orchestrator.claudecode.timeout` for the same reason it is set there — it
is the same client, on the same machine, so the same measured ceiling
applies.

Fallback lists are opt-in and are passed to the selected backend as its
provider-side
fallback chain. In `auto` mode, the safe candidates are explicit in the
router; OpenCode requires declared candidates because Atenea cannot invent
provider/model identifiers. Atenea never silently downgrades the `plan` role:
Claude planning remains pinned to Opus 5. The adaptive ranker only compares
names already in the candidate set, requires three successful workflow
samples, and requires a 15% median-cost saving. The decision trace records the
primary, the declared fallbacks and whether history changed the choice. Raw MCP tools remain
explicit-only because they have no semantic capability contract to rank.
Timeout/unavailable retries are budget-gated: reported dollars are preferred,
and unpriced token usage is bounded conservatively without being presented as
a provider invoice.

The default backend is Claude Code. To opt into the isolated OpenCode event
protocol, set `backend = "opencode"` and use a provider/model identifier such
as `anthropic/claude-sonnet-5`. OpenCode receives an explicit `--format json`
run, never `--auto`; Atenea requires a completed `step_finish` event and fails
closed when OpenCode exits before producing one. Tool-enabled turns stop after
four intermediate tool steps and resume the persisted session with a final,
tool-free answer request. OpenCode has no equivalent of Claude Code's
`--json-schema` or `--max-budget-usd`: structured answers are requested in the
prompt, and the reported cost is observational. The Claude cache floor probes
do not apply to OpenCode.

The real-provider smoke test is deliberately opt-in because it can consume a
provider allowance. From a clean checkout, set the exact provider/model and
run:

```sh
ATENEA_OPENCODE_SMOKE=1 \
ATENEA_OPENCODE_MODEL=provider/model-id \
bash scripts/opencode-smoke.sh
```

The smoke uses `--format json`, no Atenea MCP tools and no `--auto`; it checks a
real completed event stream and the structured answer against the requested
schema. It intentionally does not use `--pure`, which the current authenticated
OpenCode server rejects. Ordinary unit, race and CI suites use fake CLIs and
never incur provider cost.

`orchestrator.claudecode.source` and `terminal_binary` keep Claude Code as a
headless runner while Claude.app remains an MCP client. Do not configure the
GUI binary from `/Applications/Claude.app` as a provider executable. Codex
`source = "auto"` tries the terminal CLI first and then the CLI bundled in
ChatGPT.app; these are two executable surfaces of one provider, not two
selector candidates. The legacy `binary` key remains an explicit override.

## The measurement base

```toml
[metrics]
enabled = true              # off leaves a working core that never learns
path = ""                   # "" -> $XDG_STATE_HOME/atenea/metrics.duckdb
flush = "30s"               # how often the batch reaches disk
compact = "1h"              # how often the retention ladder is walked
buffer_limit = 10000        # measurements held in memory when flushes fail
```

What every attempt cost — time, tokens, memory — filed under the capability
asked for and the implementation that answered. It is the fuel the funnel ranks
on; until it has real figures the funnel uses the estimates written further
down the file, which is guesswork wearing a decimal point.

`enabled = false` is a bigger switch than it looks. Nothing is written, so
nothing is ever read back: the funnel keeps ranking on those estimates forever,
and break-in mode is off with it — there is no point handing a provider a turn
to earn numbers nobody will record. A core with the base switched off works
exactly as well on its first day and never gets better.

With the base on, the turn it hands out is credit rather than a standing
entitlement, because only a call that *works* leaves a measurement. A provider
that cannot answer here would otherwise sit at zero samples forever and be
handed every dispatch on the strength of it. Four attempts with nothing to show
for any of them and it ranks on its declared estimate instead — ranked lower,
never dropped, so it can still earn its first number the day it starts working.
`atenea select` shows the gap for free: `7 attempts here, none of them
successful`.

It also costs the health screen half its evidence. A provider is marked alive
by having worked recently and marked down by a run of failures that are its own
fault — a clean `not_found` or a spent budget is a fact about the request and
never counts — and both facts come out of this base — so with it off, every
provider that nothing has probed
in *this* process reads `health=unknown` forever. `atenea status` says which of
the two it is showing, on the funnel line: `measuring is off: ranking on
declared estimates for good`, rather than the `nothing measured yet` of a base
that is simply empty. The word `yet` is a promise, and it should not be made on
behalf of a base that is never coming.

`path = ""` is not "nowhere", it is the default location, beside the run
receipts and under the same state root: both are the same kind of thing, what
Atenea remembers about work it has done.

Neither rhythm can be set to zero. A beat of zero reads like "never", and a
maintenance task that silently stops happening is worse than one that is
switched off in a place you can see. Off is spelled `enabled = false`.

`buffer_limit` is the ceiling on how many measurements wait in memory while
flushes are failing. Past it the oldest are dropped and counted, because the
only thing worse than losing a measurement is not knowing that you did — the
count lands in the crash notebook, where `atenea incidents` will show it.

## What protects the history

```toml
[backup]
enabled = true              # off means no copies at all
dir = ""                    # "" -> a folder BESIDE the state root
every = "6h"                # how often a copy is taken
keep = 5                    # how many survive; the sixth arrives, the oldest leaves
```

The measurement base, the run receipts and the crash notebook are everything
Atenea has learned. A disk that loses them loses the reason the funnel picks
anybody, and unlike a provider going down there is no bin for it and no
recovering afterwards.

`dir = ""` is a folder of its own beside the state root, never inside it: a
copy under the tree it copies recurses into itself, and dies with the tree it
exists to survive. A path inside the state root is refused rather than
corrected. Point this at another disk and it stops being a copy and starts
being a backup.

Copies are hard-linked against the one before, so a snapshot of an unchanged
base costs a directory entry rather than a database. What changed is copied,
and the older snapshot keeps the older bytes — dropping the oldest can never
take a file another copy still needs.

`keep = 0` is refused. It reads as "copy, then delete the copy", and the way to
say what it looks like it means is `enabled = false`. Every rhythm in this file
refuses zero for the same reason.

`every` is a floor, not an alarm clock. Whether a copy is due is read from the
newest one on disk rather than from an in-memory timer, so a machine restarted
more often than the rhythm still copies: without that, a six-hour rhythm on a
laptop shut every evening would never take a single one.

## The crash notebook

It has none, deliberately. A notebook you have to switch on before it works is
one that is off on the day you need it, so there is no `[notebook]` table and
no way to move it: it is always on, at `$XDG_STATE_HOME/atenea/incidents.jsonl`,
beside the receipts and the base.

Nothing about it is tunable because nothing about it should be traded away. It
writes one line per fault and flushes before returning — a rhythm setting would
only ever be used to make it lose the entry it exists to keep.

## The desktop allow-list

```toml
[desktop]
applications = []           # bundle identifiers that may be looked at; EMPTY DENIES ALL
denied = ["com.apple.keychainaccess", "com.1password.1password"]  # always wins
look_then_act = false       # may a chat act on the screen it just read?
```

`applications` is the one list in this file where empty means *nothing* rather
than *everything*, and the inversion is deliberate. `desktop.inspect` and
`desktop.screenshot` can read any window on the machine, and a capability like
that must not be switched on by a settings file that forgot to mention it. Find
the identifiers with `desktop.apps`, which needs no entry here because it
returns names and identifiers and nothing about what any window contains.

`denied` always wins, and it is seeded rather than empty. Two lists rather than
one, because a single list would make "never look at my password manager" a
thing you state by omission — and omission is what happens when somebody adds
an entry in a hurry. The shipped refusals are the applications where one
screenshot is a credential. Deleting the block restores them; writing an
explicitly empty list is a statement and is honored.

The single entry `"*"` means every application `denied` does not name. It has to
be typed, and that is the point: the rule above is that omission is not a
statement, so the widest allow-list this file can express must not be reachable
by leaving something blank. Writing it beside named identifiers is refused at
load — "everything, and also these two" is two sentences that disagree about
which is in force. `denied` outranks it, which is what keeps it survivable: even
`"*"` cannot reach a password manager.

Bundle identifiers rather than display names throughout: a name is localized,
changes under the reader's feet, and two applications may share one.

`look_then_act` decides whether one chat may act on the screen it has already
read, and `false` is the security control rather than a cautious guess about
one. `desktop.inspect` and `desktop.screenshot` return whatever a window chose
to display, written by whoever controls it. With this off, a chat handed that
content may no longer move the pointer or type — so a sentence inside somebody
else's email cannot reach this machine's input. Acting is still available:
`atenea desktop` shows what will happen and waits for a person to agree.

Turning it on is the only way to get the continuous loop — look, click, look
again — which is what driving a desktop actually requires. What it costs, said
plainly: Atenea runs no classifier over what it captured, so with this on there
is nothing left between a window's text and the pointer. What still stands is
`denied`, the hard refusal to type into a secure field, credential redaction in
`desktop.type`, and the receipts. `atenea status` prints a line whenever it is
on, because a control that is off gets remembered and one that is on gets
forgotten.

Neither list is a permission on its own. The capabilities behind them cause the
`device` effect, which no floor grants by default, and the adapter refuses them
outright unless Atenea is the process macOS attributes the permission to — see
the effect's own section above.

## Reaching the desktop from a client

Three things have to line up, and they are separate on purpose.

**Atenea has to be the service.** macOS attributes a device permission to the
process responsible for itself, which is the one launchd started. Run from a
shell, Atenea borrows the terminal's screen and input access instead, and the
adapter refuses rather than spending a permission nobody granted it and which
Atenea's own settings could not switch off. `atenea service install`, then grant
Accessibility — and Screen Recording, for captures — to `atenea` itself in
System Settings.

Use `scripts/install-dev.sh` if you build your own: it builds, signs and
installs both binaries and restarts the service, which is the whole of what has
to happen in the right order. `atenea status` says `(ad-hoc: grant dies on next
build)` beside the desktop surface when it did not.

Sign the binary first if you build your own. An unsigned binary's TCC grant is
pinned to a hash that changes on every build, so it dies the next time you
compile; signed with any certificate, the grant follows the identifier instead.
An entry can look enabled in System Settings while pointing at an identity that
no longer exists — remove it and add it again if a permission you granted stops
being seen.

**The floor has to allow it.** `device` is on neither floor as shipped. A client
that only reads wants it on `client_effects`; the acting capabilities also cause
`write` and `external`, and granting those reaches every client, not only the
one you had in mind.

**The application has to be on the list.** See `[desktop]` above. Empty denies
everything.

`atenea desktop ACTION` adds a confirmation to all of this: it shows what is
about to happen, waits for a yes, and then dispatches through the service, which
is the only process that can perform it. Note that it dispatches over the same
socket a client uses, so it is governed by `client_effects` too — the
confirmation is a control on top of the floor, never instead of it.

## The web allow-list

```toml
[web]
domains = []              # empty: any PUBLIC host. Non-empty: only these.
denied  = ["127.0.0.0/8", "169.254.0.0/16", "10.0.0.0/8", "*.lan", "*.local"]
```

`domains` empty means *any public host*, and that is deliberately not how
`[desktop] applications` reads. There, empty denies everything, because any
window on this machine can be a credential. Here it does not, because an
arbitrary public web page is not the hazard — the hazard is the inside of this
network, and refusing that is `denied`'s job. Inverting this list too would put
one settings edit per site exactly where the risk is not, and the predictable
end of that trade is somebody emptying `denied` to make the nuisance stop.

A non-empty `domains` narrows to the hosts it names. A bare domain covers its
subdomains, so `example.com` reaches `api.example.com`.

`denied` always wins, takes CIDR blocks and host patterns in one list — the
thing being refused is one idea, "the inside of this network", that is spelled
two ways — and is seeded rather than empty. The shipped entries are loopback,
link-local, the RFC1918 ranges and the mDNS suffixes. Deleting the block
restores them; writing an explicitly empty list is a statement and is honored,
and it is the one setting on this page that hands out an unrestricted HTTP
client. A malformed entry is refused at load rather than skipped at call time:
a denial rule nobody can parse is a denial that silently does not apply.

`169.254.0.0/16` is the entry worth reading twice. `169.254.169.254` is the
cloud metadata endpoint on every major provider, it answers over plain HTTP
with no authentication at all, and reaching it is the single most valuable
thing anybody can do with somebody else's fetcher.

**The check runs against the resolved address, never the name.** A hostname is
somebody else's claim about where it points, and there are as many public names
with an `A` record to `127.0.0.1` as anybody cares to publish — `localtest.me`
is one. Every address a name resolves to is judged, not the first, so a name
with one public and one private record does not pass on resolver ordering.

One hole is left open and named rather than papered over: the far side follows
redirects inside its own process, so a URL that passes the gate can still end
somewhere that would not have. What Atenea can do it does — the destination the
server reports having landed on goes back through the same gate before the
answer is handed over, and an answer from a refused address is a failure rather
than a result. What it cannot do is stop the request from having been made.
`scrapling.request` could be made airtight (its far side can be told not to
follow redirects at all), but the two browser levels cannot, so the limit is
documented as one that applies everywhere rather than fixed in one of three
places — see [what is not built yet](not-built-yet.md).

## Reaching the web

```toml
[orchestrator.scrapling]
implementations = ["scrapling.fetch", "scrapling.request", "scrapling.stealth"]
timeout = "30s"

  [orchestrator.scrapling.process]
  command = "/absolute/path/to/scrapling-mcp"
  instance = "shared"       # a fetcher holds no per-repository state
  lifecycle = "on_demand"
  ready_timeout = "30s"
```

Off unless `runners` names it. `web.fetch` causes the `external` effect, which
no floor grants by default.

Three implementations answer the one capability, because they are the same
question at very different prices: `scrapling.request` is a plain HTTP request
with browser impersonation, `scrapling.fetch` renders in a real browser, and
`scrapling.stealth` defeats anti-bot interstitials. Ranking equivalent work by
measured cost is the whole job of the funnel, so the level is not an input the
caller picks.

An interstitial has to be a failure rather than a result. A challenge page
arrives as a successful `200` carrying a page about how much the site cares
about security; reported as an answer, the funnel would learn that the cheapest
level works every time and hand back challenge pages as content forever. The
two cheaper levels report it as `unavailable`. `scrapling.stealth` does not,
because it is the last level there is and there is nobody to fall back to.

**The escalation happens across calls, not inside one.** There is one dispatch
per commission: an `unavailable` marks that implementation unhealthy, and it is
the next call whose funnel drops it at the health stage and reaches for the
level above. A page behind Cloudflare therefore takes three calls to come back
the first time, after which the funnel goes straight to stealth for as long as
the health record stands — `health_stale_after` under `[selector]`, 24 hours as
shipped.

That record is per implementation and not per host, which is the sharp edge:
one protected site marks `scrapling.request` down for *every* site, so the
cheap path stays skipped even for the pages that would have answered over plain
HTTP. Shorten `health_stale_after` to shrink the window. It is written up in
full under "One blocked site downgrades every site" on the [what is not built
yet](not-built-yet.md) page.

`timeout` is sized for that last level rather than for the average: a plain
request is milliseconds, and a stealth render starts a browser and waits a
challenge out on purpose. `instance` accepts only `shared`; a fetcher holds no
per-repository state, so a server per repository would be several browsers
idling to answer questions none of them holds anything about.

The server is installed on this machine and ships in no release:

```sh
pip install "scrapling[fetchers,ai]"
scrapling install     # downloads Chromium; several hundred MB
```

## Reading named fields off a page

`web.extract` takes a list of `{name, selector}` and answers with one row per
match: `{field, index, value}`. Same gate, same three levels, same escalation
as `web.fetch` — the implementations are `scrapling.extract_request`,
`scrapling.extract_fetch` and `scrapling.extract_stealth`.

**The answer is long, not wide.** Rows keyed by field name rather than one
record with a column per field, and that is forced rather than chosen: output
fields are declared statically in the catalog, so a shape that depends on the
selectors a caller passes cannot be named in advance. The alternative is an
untyped bag, which is a promise that says nothing.

The long shape also survives ragged data, which the wide one does not. Measured
against the Hacker News front page: 30 titles, 30 ages, and 29 scores — one job
posting carries no score. Long format simply has one fewer row for that field;
a wide one would have to invent a null or misalign the column.

**It costs one request per field.** The far side takes one selector per call,
so three named fields are three fetches of the same page, about 0.75s each once
the server is warm. Two consequences worth knowing before reaching for it: the
origin sees N requests for one page, and the fields are read seconds apart, so
a page changing underneath yields a record that was never true all at once. For
anything that moves, fetch once with `web.fetch` and read the whole thing.

Fetching once and applying the selectors inside Atenea was the alternative and
was refused: it needs a CSS engine in Go, which would mean `.foo` matching
differently in `web.extract` than in `web.fetch`. A selector that means two
things in one system is worse than a capability that costs more.

There is no per-field `attribute` input. The far side renders text, html or
markdown and does not extract attributes, so declaring one would promise what
nothing here can honor. `format = "html"` returns matched elements whole, hrefs
included, for a caller that needs them.

**From the command line it needs `--payload`.** `--set NAME=VAL` cannot express
a `record_list` and refuses rather than half-parsing JSON — a deliberate limit
that `web.extract` was simply the first capability to meet. `--payload FILE`
takes the whole payload as JSON instead, which is the same document an MCP
client would have sent:

```sh
atenea ask web.extract --repo current --allow external --payload fields.json
```

The two are mutually exclusive: one is the whole payload and the other is a
field of it, and merging them would mean a rule about which wins per field.

## Walking a site

```toml
[orchestrator.scrapling.spider]
command = "~/.local/share/uv/tools/scrapling/bin/python"
args = ["/absolute/path/to/helper/scrapling-spider/atenea_spider.py"]
instance = "shared"
lifecycle = "on_demand"
ready_timeout = "30s"
```

`web.crawl` takes a `start_url` and returns the pages it reached, with the
depth each one was found at. Its far side is **not** `scrapling-mcp`: those
thirteen tools do not include a crawl, so the Spider API is reached through a
helper this repository ships at
[`helper/scrapling-spider/`](https://github.com/Tutitoos/atenea/tree/main/helper/scrapling-spider).

Omit the block and the capability is simply not served. The adapter does not
claim implementations it has no far side for — claiming them and failing at
dispatch would have the funnel rank one, choose it, and learn at the far side
that it was never there.

Two levels rather than three: `scrapling.crawl` and `scrapling.crawl_stealth`.
A crawl is already many requests, and a middle tier would multiply the cost
that is the whole reason to reach for `web.fetch` when one page will do.
Neither escalates — with two levels the cheap one moving up *is* the ladder,
and a walk that spent its page budget being challenged has spent it.

**A crawl cannot leave the host it started on**, and that is a property rather
than an option. The destination gate resolves a hostname and judges the
address, and it lives in Go; a crawler's frontier is discovered as it goes, and
it is discovered in the helper. Gating it there would mean a second copy of the
gate in Python, and a security control with two implementations has two
behaviors. So the frontier is pinned to the host the gate already approved.

**`robots.txt` is always obeyed** and is not an argument. Scrapling defaults it
off, so the helper turns it on deliberately. A caller who could turn it back
off per call would make "does Atenea respect robots.txt" a question with no
answer.

Bounded twice: `max_depth` is what the caller asked for and `max_pages` is the
ceiling that makes a mistake in the first one survivable, because a depth of
three on a site with a calendar is not a small number. Why the walk *ended*
comes back as a discovery, because a budget that ran out and a site with no
more links produce the same rows and only one of them means there is more here.

## Security

```toml
[security]
sensitive = [".env", "*.pem", "*.key", "id_rsa", "*credentials*.json"]
```

A single list of paths and patterns governs what is never read, matched against
both the file name and its repository-relative path. Sensitive files are
skipped in silence: a search that reported "1 match in .env" would leak the
very thing the list exists to protect.

There is one exception, and it is the opposite of silence. A symbol lookup
points at one exact position, so the adapter has to open that file to read the
word under the cursor. Skipping a search hit costs the caller nothing; telling
someone who named a line that there is nothing there would be a lie. That is
refused out loud, as `permission_denied`.

Declaring it in one place is the point. The moment the answer to "is this
sensitive?" lives in several files, it starts differing between them.

## Capabilities

```toml
[[capability]]
id = "code.search"          # dotted lowercase
version = "1.0.0"
summary = "Find literal text in a repository."
semantics = "Flat text search. Options are stated as intent, never as an order."
effects = ["read", "process"]  # read | write | external | process | device

  [[capability.input]]
  name = "query"            # lowercase snake_case
  type = "string"
  required = true
  summary = "The text to look for."

  [[capability.input]]
  name = "direction"
  type = "string"
  required = true
  enum = ["incoming", "outgoing", "both"]   # optional: closes the set
  summary = "Which way to walk."

  [[capability.output]]
  name = "matches"
  type = "record_list"
  required = true

    [[capability.output.field]]
    name = "path"
    type = "string"
    required = true
```

Field types: `string`, `string_list`, `int`, `bool`, `record`, `record_list`.
The set is small on purpose — a contract has to be checkable, not expressive.
`record` and `record_list` take nested `field` entries; the others must not.

`enum` closes a `string` or `string_list` to a fixed set of values. Omit it and
the field stays open; declare it and anything else is refused, with the list
named in the refusal. On a `string_list` it constrains each element — which
words may appear, never how many.

It is there for the caller that cannot be asked. A summary saying *"incoming",
"outgoing" or "both"* is enough for a person; a machine filling the field from
a generated schema has to be told, or it finds the edge by being refused. Which
is also why it is opt-in: `kind` in `symbol.overview` deliberately has no enum,
because a provider names symbol kinds in its own vocabulary and closing that
set would refuse honest answers.

Numeric bounds are not part of a field. A range in the contract is a range
every implementation must honour, and a line number is bounded by the file, not
by the capability — `max_input` under an implementation is where a *particular*
tool says what it can be asked.

### What a call is about, beyond the repository

```toml
[[capability]]
id = "web.fetch"
subject_from = "url"        # which input says what this call is about
subject_kind = "url_host"   # how to read it
```

Both keys or neither, and most capabilities declare neither. Health and cost
are recorded per repository, which is the only dimension a code capability
has — and a capability that ignores the repository entirely has nowhere to hang
them. `web.fetch` reaches the open web and never looks at a checkout, so before
this existed every site landed in one bucket.

What that cost was measured, not imagined: one page behind Cloudflare marked
the cheapest implementation of `web.fetch` unhealthy, and the next fetch of an
unrelated site skipped that implementation too — with a drop reason still
quoting the first site's url. One protected page paid for a browser on every
page after it, for as long as `health_stale_after` stood.

**It scopes health and deliberately not cost.** Whether a site refuses a
provider is a fact about that site. What a fetch *costs* is a fact about the
machinery — the far side and its browser dominate, not the host — so merging
subjects there gives a larger sample of one thing rather than mixing two.

`subject_kind` is a kind rather than a free string because the subject is a
grouping key: two calls meaning the same place must produce the same key, and
whatever the caller happened to type does not. `url_host` reads the input as a
URL and takes its host, lowercased and without a port, so `https://Example.COM/a`
and `http://example.com/b` are one subject.

The whole declaration is checked when settings load — an input that is not
declared, an input that is not a string, one key without the other. A subject
key that means nothing does not fail at call time; it files measurements under
nonsense and lets the funnel rank as if that were fine, so it is refused at the
door instead.

`atenea select` names no subject and says so in its notices: it asks who
*would* answer a capability, which is a question about the capability rather
than about a call, and the health it shows is what was recorded for no subject
in particular.

## Implementations

```toml
[[implementation]]
id = "serena.search"
provider = "serena"         # who owns the index; several implementations may share one
capability = "code.search"
scope_guarantee = ""        # "", filtered, confined -- see below

  [implementation.constraints]
  languages = ["go", "typescript"]   # empty means language-agnostic
  requires_index = true
  requires_vcs = false               # needs the repository under version control (e.g. a git diff against a baseline)
  min_scale = ""                     # "", small, medium, large
  max_scale = ""
  max_input = { depth = 0 }          # bounds what this one may be ASKED, not where it may run

  [implementation.cost]
  estimated_duration = "600ms"
  estimated_tokens = 900
  tool_version = ""                  # the version these estimates belong to

  [implementation.health]
  state = "unknown"                  # unknown | alive | degraded | down
  score = 0.0                        # 0..1, breaks ties inside one state
  reason = ""
```

`scope_guarantee` declares how strongly this implementation keeps a call inside
the `scope` it was asked to search. Two mechanisms answer honestly and a caller
deserves to know which one it got:

| Value | Means |
| --- | --- |
| `confined` | The provider physically cannot see outside scope. `ripgrep` ships this: `targets()` refuses any path that leaves the requested scope before the search ever runs. |
| `filtered` | The provider may read anywhere, but every returned match is checked afterwards and anything outside is dropped and reported through a `Notice`. `claude.search` ships this: a model is asked nicely, then verified. |
| `""` | Nobody has declared anything. Read as the weakest claim, never as `confined` — the same convention `min_scale` and `vcs` use for a fact nobody has stated. |

It is disclosure, not selection: the funnel never ranks on it and it never
disqualifies a candidate. `atenea catalog` prints it per implementation so the
promise is queryable rather than assumed.

`max_input` is the one constraint that reads the **request** instead of the
repository. Every other entry in the block answers "can this provider work
*here*"; this one answers "can this provider be asked *this*". It bounds a
declared integer input by name, inclusive, and it binds only when the call
actually names that input — an omitted value is the capability's own default,
which every implementation must honour, so nothing is dropped for it.

The graph provider ships `{ depth = 0 }`: its graph holds what a file
declares at its own top level and nothing nested inside those declarations, so
a deeper ask would return the same list and read as a complete answer.
At `depth = 0` both providers are candidates and the funnel ranks them; at
`depth = 1` it is dropped in the constraints stage and Serena, which descends
properly, is the one left. The drop names both numbers.

A bound on an input the capability does not declare, or declares as anything
other than `int`, is refused when the settings file loads. The alternative is
silent: the funnel would read a name no request carries and the narrowing you
asked for would simply not exist.

Two things are **not** declarable, deliberately:

- **Measurements.** They are earned by running. A hand-written measurement would
  poison the very baseline the selector is meant to learn from.
- **Live health.** The `state` here is a starting point and an operator
  override; once the health checker exists, live probes own that value.

`unknown` health is not the same as `down`. An unprobed provider is still a
candidate, just a less trustworthy one, and it ranks below anything alive.

## Repositories

```toml
[[repository]]
id = "api"
path = "/srv/api"
languages = ["go"]
scale = "small"             # "", small, medium, large
vcs = "present"             # "", present, absent -- whether the root sits under version control
indexed_by = ["serena"]     # providers with a ready index HERE
```

An unclassified `scale` or an unspecified `vcs` never disqualifies anyone: an
unknown fact is not a proven mismatch, and dropping candidates over it would
silently empty the funnel.

There is no per-repository Serena URL here. Pinning one repository to its own
Serena is a question about how many copies of that server should exist, and it
is answered once for the machine by `[orchestrator.serena.process].instance`
rather than one repository at a time -- see [Serena](#serena). The key that
used to sit here named a process Atenea did not start, watch or stop, so a
repository could point at an address that had nothing behind it.

`path` has to be a directory that is really there. Every adapter makes it the
working directory of the tool it launches, and a missing one used to come back
as `omp could not be started: fork/exec /home/me/.local/bin/omp: no such file
or directory` -- Go names the binary when it cannot enter `cmd.Dir`, so the one
thing that was fine got the blame. A step against a repository that is not on
the disk is now refused before anything is spawned, named at the directory, and
filed as `invalid_input`: a bin the measurement base ignores, so a typo here
cannot mark a working provider down for every other repository on the machine.

The example above is a repository somebody classified. The shipped file is not:
it leaves `scale` empty, because a fresh install has measured nothing and a
guess is not free. Writing `small` there drops every implementation that asks
for a medium repository or bigger, which then looks unimplemented rather than
unclassified. Set it once you know, and the implementation comes back.

`indexed_by` is a starting point the operator typed by hand, not a live
fact -- a settings file does not watch the disk, so it can drift the moment
an index is built or lost after the file was last edited. `atenea detect
[--repo ID]` asks every attached provider that can answer whether it
already holds a ready index and corrects the belief in memory for the rest
of that process's run, the same one-place-a-catalog-entry-changes-while-
running exception `SetHealth` already is for a provider's health -- it
writes nothing back to this file, so a later invocation starts again from
what is declared here. When a provider genuinely has nothing to detect,
the provider's own indexing tool must build one instead. Atenea only records
the resulting provider state and does not build indexes itself.

## Agents

```toml
[[agent]]
name = "filereader"       # what `atenea agent <name>` resolves
kind = "specialized"      # specialized executes one objective; orchestrator splits work
summary = "Reads one file and reports its size, its line count and its contents"
command = "$atenea"       # the one placeholder: this binary
args = ["agent-exec", "filereader"]
context = ["repository"]  # the levels the assignment may carry
effects = ["read"]        # the ceiling on what it may cause
max_duration = "30s"      # wall clock for one run
max_tokens = 1            # what one run may spend
pool = "agent"            # which parallel lane this type belongs to: agent or review

  [[agent.result]]
  name = "path"
  type = "string"         # string, string_list, int, bool, record, record_list
  required = true
  summary = "The file that was read, as it was asked for"
```

An agent is not a provider. A provider answers a capability and the funnel
picks between several of them on cost, health and what the repository is; an
agent is asked for by name, runs once as its own process, and answers in the
shape declared here. Nothing ranks agents and nothing falls back between them:
`atenea agent filereader` runs that one or fails saying it is not declared.

The wire is two JSON objects and it is the whole interface. Atenea writes the
assignment to the agent's stdin -- ids, task, the declared context levels, the
limits, the effects, and the result schema built from `[[agent.result]]` -- and
reads one report from stdout: `result`, `verdict`, and for anything that is not
a plain `ok`, a `reason` of `{kind, text}`. Stderr is diagnostics and is kept
only on a failure. **Exit status is not the channel.** An agent that exits zero
without writing a report has not answered, and is recorded as having died
rather than as having succeeded -- the distinction the whole thing exists for,
because those two are the same number to anything counting exit codes.

`command = "$atenea"` means this binary. It is the only placeholder and it
exists because the shipped agent is Atenea run with a different first argument;
a hard-coded path there survives exactly until the next reinstall. Any other
`command` is used literally, so an agent of your own -- a script, a model
harness -- differs only in what it does between reading stdin and writing
stdout.

Seven agents ship declared. `filereader` reads one file, `reviewer` audits an
answer against the files it named, and `plan-check` compiles a plan and says
whether the engine accepts it -- none of the three spends a token, and their
`max_tokens = 1` is the honest ceiling of an agent that never calls a model.
`semantic-reviewer` calls the configured `explore` model to assess whether a
conclusion follows from its evidence. It returns a structured semantic
verdict and is explicit about confidence (0-100) and scope. `explore`, `reader`,
`plan` and `semantic-reviewer` call a model through `[model]` above, and they
are off until you name a model for each role.

`explore` and `reader` are one agent declared twice, and the only difference
is the tools their turn is handed: `explore` gets Atenea's own capabilities,
`reader` gets Read and Glob alone. That is not a detail. Measured 2026-08-15,
cold, on one repository against one model: starting an `explore` turn costs
$0.27 and 26,603 tokens of prefix before the model has read a line, and the
identical probe with no tools at all costs $0.06 and 4,991 -- 81% of the floor
is the definitions of tools most steps never call. Give a step that already
knows which files it is about to `reader`, and pay `explore` only when
something has to be searched for.

`context` is a permission, not a delivery. Only the levels named here are ever
sent, and a level nobody asked for is absent from the payload rather than
present and empty, so an agent cannot read a blank field as an answer about
the world. `effects` is a ceiling a child may only narrow. Both limits are
required: an unset ceiling is not "no ceiling", it is a ceiling nobody decided,
and `max_duration` is what turns a hung agent into a `timeout` verdict instead
of a process nobody is waiting on.

The `history` level serves past runs of the same agent type, and each of those
rows now carries what that run **discovered**: the short facts a report marked
as worth outliving its task. Only rows whose own verdict was `ok` contribute,
because a fact found by a run that then fell short was never checked, and the
same note found twice is served once. This is why `explore` declares
`history`: those facts were paid for, and a commission that rediscovers them
pays for them again.

The `workspace` level names the repositories this machine knows, and carries
**what each agent type has actually cost here**. This is why `plan` declares
it: a planner dividing a grant with no idea what exploring costs divides it
evenly, and every step it writes stops at a ceiling it could not have known
was too low. Measured 2026-08-14: a plan allocated `$0.10` to a step whose type
had never once finished under `$1.26`.

The figures are read back from the workflow record — `median`, the range, and
`n`, which travels with every median because three samples and thirty are
different claims. Three exclusions are deliberate and are printed rather than
hidden:

- **A run that spent its whole grant is excluded and counted.** It is a lower
  bound — "at least this much" — and averaging those in is exactly how a
  measured table becomes the under-estimate it was built to replace.
- **A run nobody could price is excluded and counted.** A turn killed at its
  timeout is not a cheap turn.
- **A type with no rows is reported as `never measured`, in those words.** Not
  a zero, not a dash, not an omitted line: "nobody has priced this" and "this
  is cheap" are different facts, and only one of them is safe to plan against.

Two limits ship with it. The figures cover **workflow steps only** — a single
`atenea agent` run is priced nowhere, because `agent_trace` has no spend
column — and the table says so on every render. And the scope is the
repository the run was recorded against, falling back to **machine-wide** when
that repository has no rows yet, which it says in those words rather than
presenting another tree's numbers as this one's.

None of it is enforced. A share below the measured median compiles and runs:
a machine with history refusing plans that a fresh install accepts is a worse
failure than an under-allocated grant, and the grant is the operator's call.

`[[agent.result]]` compiles to a strict JSON schema -- `additionalProperties`
is false -- so an answer carrying a field nobody declared is refused at the
boundary rather than believed. A refused answer is `incomplete`, not `failed`:
the run got somewhere and stopped short, which is a different fact from a run
that reached a judgement and the judgement was no.

Every run is written to the trace database at `~/.local/state/atenea/traces.db`
before the spawn and closed after the report validates. `atenea traces` reads
it. A row left open is a run nobody saw finish; the next Atenea closes it as
`incomplete` with reason `unavailable`, but only after checking that the
process that opened it is really gone -- another Atenea may be mid-run right
now, and closing a live run would be the sweep inventing the very thing it
exists to record honestly.

### Review

```
atenea agent filereader --review reviewer sample.txt
```

A reviewer is an agent like any other -- same wire, same declaration, same
trace row. What makes it a reviewer is being named in `--review`: it is handed
the answer another agent gave, the task that produced it, and the same files,
and it answers `ok`, `failed` or `incomplete` about that answer.

A refusal relaunches the work **once**, and the relaunch is handed the rejected
answer and the sentence that rejected it, because an agent told only "try
again" reruns the same mistake. A second refusal ends it: the caller gets an
error carrying the reviewer's own reason kind, and no third attempt is made.

Every attempt is its own trace row, and the rows say which is which. The
relaunch carries `retry_of` naming the attempt it redoes and `attempt = 2`; a
review carries `reviews` naming the run it audits. `atenea traces` prints both
columns, so "passed" and "passed on the second try" never read the same.

Two things are deliberately not reviewed. A run that **died** -- no report, a
timeout, a crash -- is not handed to a reviewer, because there is no answer to
judge and re-running a crashed process is a retry policy, not an audit. And a
reviewer that dies itself does not accept: the answer is returned unjudged with
the reviewer's death as the error, never quietly passed off as reviewed.

`reviewer` proves deterministic facts such as file contents, line counts and
citation locations. It does not infer that a prose conclusion follows from
those facts. Use `--review semantic-reviewer` when that second question is
needed. `supported` is an explicit model judgement, `unsupported` is rejected,
and `indeterminate` remains incomplete rather than being promoted to approval.

`pool` is which parallel lane a type belongs to, `agent` or `review`, and it
defaults to `agent`. Reviews are separated because one lane holding both would
starve auditing exactly when the machine is busiest -- every slot full of
agents, the reviewer queued behind them, answers piling up unjudged. The
scheduler that honours it is the workflow engine below; `atenea agent` runs one
agent at a time and has no cap to compete for, so the lane only starts to
matter once a graph is running.

`reads_subject` says a type is handed another step's whole answer. It is a
separate word from `pool` because reviewing and reading a subject are two
different facts that only looked like one while reviewers were the only
consumers: the shipped `plan` agent reads the exploration `explore` produced,
and it is ordinary work, not an audit. Declaring it as a lane would have put a
planner in the pool sized for auditing, competing with the reviews. A
`review`-pool type has it whether or not it says so -- a review with nothing
to review is refused anyway -- so it is only ever written on an agent-lane
type. Hand a subject to a type that declares neither and the graph is refused
before anything spawns.

## Workflows

```toml
[workflow]
max_parallel_agent = 4    # steps in the agent lane at once; 0 lifts the ceiling
max_parallel_review = 4   # the review lane, sized apart from it
```

A workflow is a DAG of agent steps handed to Atenea whole:

```
atenea workflow run plan.toml
atenea workflow list
atenea workflow show <id>
atenea workflow resume [--redo STEP] <id>
```

Every flag goes **before** the id. Go's flag parser stops at the first word
that is not a flag, so `resume <id> --redo STEP` hands the subcommand three
arguments where it expects one and exits with `workflow resume takes one
workflow id` before it has read anything.

```toml
task = "count what the docs say"          # the commission every step is a slice of
budget_usd = 0.50                         # the grant; step shares divide it

[[step]]
id = "read-readme"
agent = "filereader"                      # a declared [[agent]], not a capability
objective = "read README.md and answer"
files = ["README.md"]
criterion = "the counts match the file"
effects = ["read"]                        # ceiling for this step, within its type's
budget_usd = 0.25                         # this step's share

[[step]]
id = "audit-readme"
agent = "reviewer"                        # a review-pool type; it needs a subject
subject = "read-readme"                   # hand it that step's answer -- implies the edge
objective = "audit what the reader answered"
files = ["README.md"]
criterion = "the answer holds"
effects = ["read"]
budget_usd = 0.25
```

Atenea can now write one of these, and it is still the same file. Two shipped
agents do it: `explore` looks at the project through Atenea's own capabilities
and reports what it found, and `plan` reads that exploration and answers with
a graph as TOML text. What comes back is an artifact a person can read, edit
and keep -- not a hidden structure -- and the engine executes it exactly as
written afterwards, through `atenea workflow create` like any other plan.

There is still no orchestrator inside a run: no model picks steps while the
engine works, and a graph does not grow mid-run. Planning happens before the
run, in a run of its own that is recorded, judged and paid for like any other
work. That is why every refusal below happens before anything spawns -- and
why a plan a model wrote is compiled by `plan-check`, the shipped reviewer
that hands the planner the engine's own refusal sentence to correct.

Steps with no unmet dependency run together, up to the ceiling of their lane.
Ready steps beyond it wait: the queue is the pending set in declaration order,
not a second list, so the same graph makes the same choices on a slower
machine. A step whose dependency did not end `ok` never runs and reads as
`blocked`; its siblings are untouched, which is the point of the graph being a
graph.

### What an edge carries

`needs` carries order and nothing else: a step is handed the task written in
the file, never what the step before it found.

`subject` carries the answer. It names **one** upstream step, and it is an
edge as well as a pipe -- a step reading another's answer runs after it, and
declaring that twice would let the two halves disagree. What travels is the
whole validated report: the result, the verdict, the reason, and the task the
upstream was actually asked, criterion included. Never a projection of it. A
subject narrowed to the interesting fields is a summary, and a review of a
summary is a review of something the parent never consumed.

It is the same card `atenea agent --review` hands over, built by the same
constructor, so one answer cannot get two different reviews depending on which
door the caller came through. The reviewer's trace row records `reviews <id>`
either way.

A review inside a graph carries the same correction rule as `atenea agent
--review`: a `failed` verdict sends its subject back **once**, and the second
attempt is handed its own refused answer beside the input it was working
from, plus the sentence that refused it. A second refusal stands. Only a
judgement relaunches -- a reviewer that could not judge (`incomplete`, a
death, a timeout) has said nothing about the work, and re-running it because
its auditor broke spends the commission on somebody else's outage.

Both attempts are on the receipt. The refused run cost real money, and a
total that keeps only the accepted half understates the bill by exactly what
the correction cost.

Two refusals at compile time, both naming the step:

- a **review-pool type with no `subject`** -- it would spawn, read an empty
  card and answer `incomplete` saying so, after the earlier steps had run
- a **`subject` on a type that does not read one** -- neither a review-pool
  type nor one declaring `reads_subject`

### What an edge demands

```toml
on = "answered"   # default: ok, failed or incomplete -- anything judged
on = "ok"         # only a success
```

The line is **judged versus unjudged, not good versus bad**:

| the subject ended | the dependent |
|---|---|
| `ok` | runs |
| `failed` | runs -- a failure is an answer, and "it says it failed" is the claim most worth auditing |
| `incomplete` | runs -- the agent's own word for stopping short is still an answer |
| `interrupted` | **blocked**: nobody judged it, so there is no verdict to hand over |
| `blocked` / `skipped` | blocked, transitively |

Never a partial subject. Either the whole validated report or nothing: a card
claiming an answer that was never given is the same class of mistake as a
green verdict on an unmeasured surface -- it would look fine and be wrong.
That one is not a rule layered on top, either. A subject with no verdict is
refused by the contract itself, so it cannot be built even if a gate above it
were wrong.

A step blocked by an unjudged subject prints the command that clears it, which
is `atenea workflow resume <id>` when the subject only read, and the same with
`--redo <step>` when it may have written -- the one a resume deliberately
leaves alone.

`on` is a declared word rather than a second `needs` line naming the same
step. Those two lines look redundant, and a reader tidying one away would
leave a graph that still runs and quietly reviews things it was meant to
skip: silent, and clean-looking. `on` with no `subject` is refused rather than
ignored.

**Two steps that can run at once may not both touch a file when one of them
writes it.** That is refused when the graph compiles, naming both steps and the
path -- not serialized quietly at run time, which would make the order depend
on how busy the machine was and hide the conflict until the day it lands
differently. "Can run at once" means neither waits on the other, whatever the
ceilings happen to be; order them with `needs` or give them different files.

### What survives

The record lives in two tables in the trace database -- the graph, each step's
status, its answer, and the trace row it ran as -- because a resumed workflow
has to report steps it did not re-run, and because the only honest answer to
"was this step running?" is a pid to check. Everything derivable stays in
memory: the ready set, the queue, the lane counts and the write claims are all
functions of the graph and the statuses, and a stored copy is a second truth
that can disagree with the first.

A step is `pending`, `running`, `ok`, `failed`, `incomplete` or `interrupted`.
The last is a step **nobody judged**: it was running when the operator cut it
or when Atenea died, and no report was ever read. It is not `failed` -- that is
a judgement, and nothing here made one -- and not `incomplete`, which is the
agent's own word for stopping short.

`atenea workflow resume` continues a run that was cut or orphaned. It refuses
while the pid on the record is still alive, because a record saying `running`
is not evidence that anything runs and taking over a live run would double
every step in flight. Interrupted steps that only read are dispatched again as
a second attempt whose trace row says which run it redoes; interrupted steps
that may `write` or reach `external` are **left alone**, because nobody saw how
far they got and repeating them could land the same effect twice. Those wait
for `--redo <step>`, and a run holding one is `unjudged` rather than finished.

A subject survives all of this. The card handed to a reviewer on a resume is
rebuilt from the record, so it is the answer the upstream really gave, even
when the Atenea that heard it is gone -- and if that upstream was itself
redone, the review audits the attempt that stands rather than the one that was
cut.

### Money

`budget_usd` on the graph is the grant; the shares on the steps divide it and
are refused if they add up to more, because money is split rather than copied.

What was **spent** reads as `unmeasured`, and will until something can report a
charge: the agent report wire carries no cost and no token count, so there is
no number to add up. That is why the column is empty rather than zero. A
receipt printing `$0.00 spent` for a real run would be the same lie as list
price on subscription traffic, and worse for looking audited.

### Gates

A plan is a thing to read before it is a thing to run, so those are two acts
and two commands.

```
atenea workflow create plan.toml   # writes it down, spawns nothing
atenea workflow launch <id>        # commits the grant and runs it
```

`atenea workflow run plan.toml` is both at once, and honest about it: the
person typed the path, so the reading and the commissioning are the same act.
Over MCP there is no such shortcut -- `workflow.create` and `workflow.launch`
are separate tools, because a plan and the permission to run it arriving in
one message is the arrangement a gate exists to prevent.

The graph may grow mid-run, three times at most. Nothing proposes an
expansion yet: it comes from a file, the same as the first graph did.

```
atenea workflow propose --replaces old-step <id> next.toml
atenea workflow approve <id>
atenea workflow reject --reason "reads two files it should not" <id>
```

**Launch** and **approve** are the same mechanism and different words.
Launching commits a grant that nothing had claimed; approving an expansion
extends one already in flight. `atenea workflow show` prints the log, and a
reader can tell which happened without counting ordinals.

#### What a waiting gate does to the run

**The queue freezes; what is already spawned finishes.** No step is cut and no
new step is dispatched. A gate waiting overnight holds no processes at all --
the running ones end on their own and are not replaced. Waiting has no
deadline: nothing times out, because a question that expires into a default is
not a question.

A proposal may only replace steps that **have not started**. Not "not
executed" -- a running step has begun to touch the world, and replanning it
would be a decision about work already underway.

Those two rules are one design. The only thing that moves a step out of
`pending` is a dispatch, and dispatch is frozen while a gate is open, so no
step a proposal names can change state while somebody reads it. **Staleness
stops being a race to detect and becomes impossible to construct.** The digest
below is the check on that reasoning, not a substitute for it: if the freeze
ever had a hole, the digest is what refuses the plan that fell through it.

Ctrl-C during a gate cuts the running steps and records the run as `aborted`,
and **the gate stays open**. The question was never answered, and closing it
on the way out would answer it.

#### How the answer arrives

**The gate is a row, not a reply.** It lives in the trace database beside the
graph, which is what lets it outlive the Atenea that asked: if that process
dies mid-wait, the record still says `waiting`, the pid on it is dead, and
`atenea workflow resume <id>` takes the run over and finds the question
exactly where it was left. The CLI and the MCP surface write the same row.

#### What the record keeps

The proposal verbatim, a digest of it, when it was asked, when it was
answered, the decision, the reason on a rejection, and the hand.

The digest is what an approval is an approval **of**. The engine recomputes it
over what it is about to apply, immediately before applying, and refuses on
any difference: an approval names an artifact, not a moment. Reordering the
steps of a proposal is a different plan; listing the replaced steps in another
order is not.

**The hand is what this machine can tell, and it is not an identity.** It
records an operating-system user and the surface the answer arrived through --
`tutitoos via cli`, `tutitoos via mcp session 0da8`. Nothing here
authenticates anybody, so a field named for a person holding only `$USER`
would be a claim backed by nothing, the same shape as a receipt reporting a
charge nobody measured. A real identity needs something that verifies one, and
there is nothing on this machine that does.

#### Where it stops

Two ceilings, and they run out for different reasons, so they say so
differently:

```
expansions exhausted (3 of 3)
grant fully allocated ($0.50 of $0.50); this proposal asks for $0.25 more
this proposal asks for $0.80 and the grant has $0.50 left ($0.50 of $1.00 allocated)
```

The last two are the same refusal and different facts: a grant with room left
that this proposal overruns is not a grant that is spent, and saying the
stronger sentence would send a reader looking for steps that do not exist.

**Allocated, never spent.** The second is the sum of what the steps claim,
refused when it passes the grant. Nothing on this machine can report a charge
-- see Money above -- so there is no spend to compare against and there will
not be one until an agent can measure what it used. An expansion past the
grant is refused when it is proposed, not when it is approved: a person should
not be asked to bless a plan that cannot run.

A rejected **launch** stops the run: nothing ran, and it reads `rejected`
rather than `aborted` (nobody cut anything) or `finished`. A rejected
**expansion** is not a rejected run -- the graph already approved carries on
and finishes, because that graph is one somebody did approve.


## MCP servers

```toml
[[mcp_server]]
id = "serena"                        # the name the client will see
url = "http://127.0.0.1:40010/mcp"   # http endpoint
dashboard = "http://127.0.0.1:40010/dashboard"  # optional web UI; opened only by `atenea dashboard <id>`
timeout = "5s"                       # bounds the check; omitted takes the default
expose = "off"                       # off (default) points the client here; raw is a passthrough

[[mcp_server]]
id = "graph"
command = ["graph-backend"]           # stdio; started once per check, then killed
[mcp_server.env]
GRAPH_BACKEND_UI = "false"
```

This list is not the catalog and nothing dispatches against it. Atenea reaches
its own providers through adapters; these are endpoints `atenea wrap` hands to
*someone else's* client so that client stops spawning a private copy of a
server that is already running.

`dashboard` is optional metadata for a real HTTP(S) web UI belonging to the
MCP. Atenea validates its scheme, host and port, reports the URL in `status`
and `detect`, and never opens a browser when the MCP starts or is probed. To
open one explicitly, use `atenea dashboard <id>`. The command checks that the
URL is reachable first. `atenea dashboard hosts --dry-run` previews the
idempotent, Atenea-managed block in `/etc/hosts`; writing that file requires
the explicit `atenea dashboard hosts` command and appropriate permissions.

Serena is the exception to a static `dashboard` URL: Atenea launches one
instance per repository and Serena assigns each web dashboard a dynamic port.
`atenea dashboard serena` discovers the dashboard whose active project matches
the current working directory. Serena instances are warmed sequentially so
their dashboard port selection cannot collide; their browser opening remains
manual.

Kivgraph has two separate processes: its stdio MCP server and its optional
read-only graph viewer. The viewer must be built with Kivgraph's `webassets`
tag and can be supervised independently:

```toml
[orchestrator.kivgraph]
dashboard = "http://127.0.0.1:7777"

[orchestrator.kivgraph.dashboard_process]
command = "/Users/gtrave/.local/opt/kivgraph-ui/bin/kivgraph"
args = ["ui", "--addr", "127.0.0.1:7777"]
env = ["HOME=/Users/gtrave"]
lifecycle = "persistent"
port = 7777
```

This process is checked with a normal HTTP GET, not an MCP handshake. It is
kept bound to loopback and does not open a browser. Use `atenea dashboard
kivgraph` when the page should be opened explicitly. A binary built without
the web bundle is not a valid dashboard provider: adding its URL would claim
a UI that cannot serve one.

Each block sets `url` or `command`, never both; repeating an `id` is refused
rather than resolved; and an `id` may not contain a dot. Those refusals are
the same rule: the payload is keyed by `id`, so a block that cannot be turned
into exactly one endpoint would end up either ignored or silently overwritten
by the block after it, and a declaration nobody can act on is worse than no
declaration -- a client is told the server exists before anyone finds out it
does not. The dot is refused for the same reason one step later: a passthrough
tool is named `raw.<id>.<tool>`, which can only be split back into a server
and a tool while the server is one segment.

`expose` is the field that separates the two things this list can mean. `off`,
the default and everything described above, makes the entry a pointer: the
client is told where the shared server is and talks to it directly. `raw`
means Atenea holds the connection and re-offers that server's tools verbatim
under `raw.<id>.<tool>`, with no funnel, no capability, and no health record
of its own.

A backend declared `raw` is dialed on the first call that needs it, not at
startup: one that is down when Atenea starts must not stop it starting, and one
that comes up later must start working without a restart. The session is opened
once and shared by every chat, which is the whole reason for declaring it here
rather than in five client configs. `tools/list` then carries that server's
tools after Atenea's own capabilities, named `raw.<id>.<tool>`, with the
backend's own input schema forwarded unedited -- no `repository` argument is
added, because a raw tool has no idea what a repository is. A backend that does
not answer is left out of the list rather than listed as broken.

What a raw call does *not* touch is the point of keeping it separate: no
funnel, because there is nobody to choose between; no capability, so no schema
of Atenea's is checked against it; and no row in the measurement base, because
latency with no competitor is not evidence in a decision. It still leaves a
receipt -- `kind = "raw"`, written closed, with the step's `funnel.state` set
to `none`. That is not the same as a step whose funnel went unrecorded, which
reads `not_kept`, and telling those two silences apart is why the field has
three states rather than being absent.

### What a raw backend may offer, and what it costs

A `raw` block must declare both, and neither has a default:

```toml
[[mcp_server]]
id = "semgrep"
url = "http://127.0.0.1:40020/mcp"
expose = "raw"
instance = "shared"                                  # shared | per_chat
tools = ["get_supported_languages", "semgrep_scan"]   # the allow list
effects = ["read"]                                    # what they may cause

  [[mcp_server.tool]]
  name = "semgrep_scan"      # narrower than the server's, for one tool
  effects = ["read", "process"]
```

`tools` is the budget. Only the names on it are offered, and only they can be
called: the list is enforced at the backend, so a name that never appeared on
any tool list is still refused when a client sends it anyway. Both readings of
an *absent* list are defensible -- offer everything, offer nothing -- which is
exactly why neither is guessed. A block declaring `raw` with no `tools` is
refused, and so is an empty list.

`effects` is what those tools are authorized to cause, in the same five names
capabilities use: `read`, `write`, `external`, `process`, `device`. Atenea cannot infer
them; a backend's own list can hold `execute_shell_command` beside
`find_symbol`, and nothing in a name or a schema says which is which. A
`[[mcp_server.tool]]` block narrows the declaration for one tool, and a tool
with no block of its own causes what the server declared. A per-tool block
naming a tool outside `tools` is refused -- it describes a call that is already
refused a layer earlier, and left in it reads as coverage nobody has.

At call time the declared effects are held against what the chat may
authorize, through the same `Session.entitled` a capability crosses. Reading
is free; anything else needs a grant. Captured against the live `semgrep` on
the author's machine, with the block above:

```text
get_supported_languages  ok       supported languages are: apex, bash, c, c#, …
semgrep_scan             refused  permission_denied: session … may not authorize process
semgrep_rule_schema      refused  permission_denied: semgrep: tool "semgrep_rule_schema"
                                  is not in this backend's tools
```

All three left a receipt, and each carries the effects it was measured
against -- `["read"]`, `["read","process"]`, `["read"]`. A refused call is
recorded exactly like one that ran, because an attempt that was stopped is
what an audit is looking for.

That middle line was captured against a chat holding nothing beyond `read`,
which in the historical `0.10.0` behavior was every chat there was. The same call on a machine
whose `client_effects` grants `process` now runs: the refusal was the ceiling
being unreachable, not `semgrep_scan` being forbidden.

A raw backend is also **held back from `atenea wrap`**. Every other entry is
handed to the client so it can talk to the shared server directly; doing that
for a raw one would point the client past the allow list and the effects
check, so the budget would be bypassed by the very command meant to apply it.
It is still probed and still reported, under `held`.

`instance = "shared"` is the default and keeps one upstream MCP session for
the whole Atenea service. Use `instance = "per_chat"` when the backend's
session state belongs to one client conversation: Atenea creates that backend
on the first `tools/list` or `tools/call` for the connection, reuses it for the
rest of the chat, and closes it when the connection ends. The setting applies
to both HTTP (`url`) and stdio (`command`) raw backends; it is refused on a
pointer (`expose = "off"`) because Atenea does not own that lifecycle.

`raw` is reserved as the first segment of any capability or implementation id,
so nothing in the catalogue can ever collide with a passthrough name -- refused
in `Capability.Validate` and `Implementation.Validate`.

**Both transports are served.** A `url` entry is one HTTP session shared by
every chat. A `command` entry is one *process*: Atenea spawns it on the first
call that needs it, replays the MCP handshake once for the process rather than
once per chat, holds its stdin open for as long as it lives -- a stdio server
reads EOF on stdin as its client leaving -- and routes each answer back to the
chat that asked by request id. It is stopped when Atenea stops, because Atenea
started it. This is the case worth declaring: an HTTP server can be pointed at
by five clients, but a stdio server has no address, so every client that wants
one has no choice but to spawn its own. On the machine this was written for
that meant five private copies of one indexer, each holding its own index of
the same repositories.

One rule is stricter over a pipe than over HTTP: **a call that dies is not
retried**. A `tools/list` that died is asked again of the replacement, because
nothing happened; a `tools/call` is not, because the server may have run the
tool and died carrying the answer back, and re-sending it would run a declared
`write` twice. The chat is told instead, and the next call gets a fresh
process.

`atenea wrap opencode` handshakes every entry here and passes on only the ones
that answered -- minus the raw ones, which Atenea serves itself. Captured on a
real machine:

```text
wrap opencode  4 checked: 3 declared, 0 refused, 1 held

  declared  chrome-devtools  http   http://127.0.0.1:40021/mcp  chrome_devtools 1.6.0 (2ms)
  declared  context7         http   http://127.0.0.1:40011/mcp  Context7 4.0.0 (478ms)
  declared  serena           http   http://127.0.0.1:40010/mcp  Serena 1.28.1 (27ms)
  held      semgrep          http   http://127.0.0.1:40020/mcp  Semgrep 1.23.3 (3ms)
                             served as raw.semgrep.<tool>; opencode is not pointed at it

  Declared means it answered an MCP handshake, not that its tools work.
```

## What `declared` promises, and what it does not

The check is one MCP handshake per server. That is a real measurement and it
is a narrow one: it proves the server is reachable, starts, and speaks the
protocol. It proves nothing about whether its tools work.

The gap is not theoretical. On the machine above, `semgrep` answers the
handshake in under two seconds and reports version 1.23.3 -- and every call to
its primary tool fails, because the tool builds its ruleset from a registry
the machine is configured not to contact. It had been failing for days. Wrap
declares it, correctly, and the report says in one line what that word covers,
which is why the line is printed on an all-green run too: `5 declared, 0
refused` is exactly the moment the word is trusted furthest and examined
least.

Checking further would mean calling tools, and calling a tool is doing work --
with side effects, a bill, and no way to tell a broken server from a slow one.
The handshake is the last thing that is free. Wrap narrows the distance
between *declared* and *working*; it does not close it, and it says so.

A refused server is **absent** from what the client receives, not disabled in
it. The client merges Atenea's configuration over its own key by key, so an
absent key leaves the user's entry alone: Atenea declining to vouch for a
server must never be the reason a working one disappears. The corollary is
visible above -- `chrome-devtools` stays broken in exactly the way it was
already broken, and wrap's contribution is that somebody now knows.

Nothing is written to disk. The configuration rides in one environment
variable or on the client's own command line, for the lifetime of the child
process, so a client launched without `wrap` is a client with exactly the
configuration it had before, and there is no `unwrap` because there is nothing
to undo. Three clients are wired today, and they take it in two ways:

| client | how it arrives | against the client's own |
|---|---|---|
| `opencode` | `OPENCODE_CONFIG_CONTENT` | deep-merged, key by key |
| `claude` | `--mcp-config <json>` | added to every other source it resolves |
| `codex` | one `-c mcp_servers.<id>={...}` per server | one key each, rest of the table untouched |

The codex shape is one server per override rather than one for the whole
table on purpose: `-c mcp_servers={...}` replaces the map, and every server
the user declared in their own `config.toml` would be gone for the length of
the session.

`omp` is not wired, and it is the interesting one. Its MCP servers are read
from `mcp.json` files and nothing else -- there is no config-content variable,
and its `--config` overlay feeds a settings tree whose schema has no
`mcpServers` key in it at all, so even an inline overlay could not carry a
server. Wrapping it would mean writing one of those files. That is the
guarantee above being traded away, so it is not done, and `atenea wrap --help`
says so by name rather than leaving a reader to discover the omission.

## Arguments handed to the client, and `--auto`

Everything after the client name is passed to the client untouched. For
`opencode` wrap adds nothing of its own: verified by replacing the binary with
a stub that printed its argv, which received exactly `--auto`. For the two
command-line clients it adds exactly the flags in the table above, and where
they go is not cosmetic. `--mcp-config` is variadic -- it swallows every
following token that does not begin with a dash -- and it is also a global
flag, which a subcommand will not accept. All three placements were measured
against claude 2.1.220 and only one survives both facts:

| placement | result |
|---|---|
| `claude --mcp-config <json> mcp list` | `MCP config file not found: mcp` |
| `claude mcp list --mcp-config <json>` | `error: unknown option '--mcp-config'` |
| `claude --mcp-config <json> -- mcp list` | the subcommand runs |

So the flags go in front, and a `--` is inserted when the user's first
argument is a bare word. Appending them behind the user's arguments reads as
the safe choice and was shipped that way first: it works for a session and
kills every subcommand, because a global flag behind a subcommand is not a
global flag any more.

That a session honours the injected config is measured rather than read: a
server whose command leaves a file behind on start left one. It cannot be
read back with `claude mcp list`, which reports the servers on disk and
ignores the flag entirely -- even under `--strict-mcp-config`, where the list
came back identical to the unflagged one.

That flag is worth naming here rather than leaving it in a shell alias,
because it is the one part of the launch line Atenea does not govern. It
belongs to opencode, whose own help describes it as *auto-approve permissions
that are not explicitly denied (dangerous!)*. It is not a `wrap` option and
never was; on the machine this was written on it arrived by inheritance, from
an older `headroom wrap opencode --auto` alias, and rode along unexamined when
the alias moved to `atenea wrap` on 2026-08-09.

It does not widen anything Atenea enforces. The commission check refuses a
step whose effects its capability does not declare no matter which client
asked, so `--auto` cannot buy a caller an effect the settings file withholds.
What it governs is the other half: what the client approves on its own behalf,
with its own tools, before Atenea is ever consulted. The two are independent,
which is exactly why one of them being a default nobody chose is worth writing
down.

Recorded as a standing posture decision to revisit, not a recommendation.

## Selector rules

```toml
[selector]
health_stale_after = "24h"
```

`health_stale_after` is how long a runtime provider observation remains
trusted. When it expires, Atenea treats the provider as `unknown` and lets the
next dispatch re-probe it; the setting does not age declarative health written
in the catalog.

The service also probes declared MCP servers every `core.health_probe_every`
and persists the last result under the private state directory. A successful
probe is `ok`, a failed handshake is `failed`, and an unconfigured interval of
`0s` keeps probing on demand through `atenea detect` only. The probe checks the
MCP handshake; it does not execute tools or claim semantic availability.

```toml
[[selector.rule]]
capability = "code.search"
repository = "api"          # omit to apply everywhere
prefer = "ripgrep"
```

The most specific rule wins: one scoped to a repository beats a global one for
the same capability. Two rules for the same capability and repository are
refused rather than resolved by file order.

A rule pointing at something the catalog does not have stops the boot. A rule
that quietly matches nothing is a preference the user believes is in force and
is not.

## A repository's own settings

A repository may carry `.atenea/config.toml` at its root. It is a partial
overlay on the global file: what it declares wins, what it leaves out falls
back, and a repository without one changes nothing at all. Nothing in it is
required.

```toml
# /home/you/work/api/.atenea/config.toml
[[repository]]
scale = "medium"           # this repository is medium here, whatever the global file says
                           # languages, vcs and indexed_by are not named, so they inherit

[[selector.rule]]
capability = "code.search"
prefer = "ripgrep"         # in this repository only
```

`atenea config show` prints the result with the origin of every field, which is
the only way to tell an inherited value from a declared one that happens to
match:

```
global   /home/you/.config/atenea/atenea.toml
overlay  /home/you/work/api/.atenea/config.toml
         root /home/you/work/api, patches repository api
         declares repository, repository.scale

repository api  /home/you/work/api
  global   languages   go
  local    scale       medium
  global   vcs         present
  global   indexed_by  serena
```

### Which file applies

The active repository is found by walking up from the working directory to the
first `.git`, and the overlay is the one at that root. The walk stops there: a
repository nested inside a workspace does **not** inherit the workspace's
overlay. A nested repository is cloned and published on its own, so inheriting
would make it behave differently depending on where it happens to be checked
out — and it is the same boundary the client harnesses on this machine already
stop at, so there is one rule to remember rather than two.

Measured in a workspace holding a nested repository, with an overlay in each:

| working directory | overlay that applies |
| --- | --- |
| `kena-workspace` | `kena-workspace/.atenea/` |
| `kena-workspace/cli` | `kena-workspace/.atenea/` |
| `kena-workspace/libraries` (own `.git`) | `libraries/.atenea/` |
| `kena-workspace/libraries/packages/env` | `libraries/.atenea/` |

From inside `libraries`, the workspace's own declared scale is still what the
global file says. The outer overlay is not consulted, not merged and not
partially applied.

A `[[repository]]` block is matched to the global catalog **by path**, not by
id: the subject of the file is the directory it was found in. When the global
file already declares that path, the block patches it field by field. When it
does not, the repository is added, taking its id from the directory name — and
a clash with an id the global file already uses for a different path is refused
rather than resolved.

### What a repository may not declare

The file travels inside the repository, so cloning somebody's repository is
accepting their settings. That is a different trust question from the global
file, where the machine's owner is the only author, and most of this schema
answers it badly: `[[mcp_server]]` and every `process` block carry a command to
launch, `[[implementation]]` decides what runs behind a capability, and a
shortened `[security] sensitive` disarms the skip that keeps secrets out of a
search.

So the overlay accepts four things and refuses the rest **by name**, with the
reason:

| allowed | what it says |
| --- | --- |
| `[[repository]]` — `languages`, `scale`, `vcs`, `indexed_by` | what this repository is |
| `[[selector.rule]]` | which implementation to prefer, for this repository only |
| `[security] sensitive` | further files to treat as delicate |
| `[[agent]]` — with `runs` | a type of its own, reusing a spawn this machine already declared |

`sensitive` is **unioned** with the global list, never replaced: a repository
may tighten the guard and may not loosen it, so an empty local list adds
nothing rather than clearing anything.

A selector rule is scoped to the overlay's own repository whether or not it
says so, and naming a different one is refused. `prefer` must name an
implementation the global settings declare — a rule pointing at something this
binary does not have would be a preference the reader believes is in force and
is not, which is the same refusal the global file already makes.

`repository.path` and `repository.id` are refused too. The path is the
directory the file was found in; letting the file name another one is the only
way this layer could reach outside its own tree.

#### `[[agent]]`, and why it is not a hole

A repository may declare an agent type, and it must name `runs`: the type it
borrows the spawn from. What it may not name is the spawn itself. `command`,
`args` and `env` are refused by name, for the reason each refusal gives —
the command is what this machine launches, the arguments are the other half of
it, and a `PATH` set in `env` redirects even a command the machine did choose:

```
atenea: invalid_input: local settings /tmp/repo/.atenea/config.toml:
  agent.command: the command is the binary this machine spawns; a repository
  choosing it is a cloned file deciding what runs here
```

So the shape a repository can declare is a narrower use of something already
on this machine: a different objective, a smaller share, a tighter result
schema. Anything it declares is held to two ceilings at once — the type it
runs, and `[local_agents]` below — and the tighter of the two wins. Saying
nothing inherits that same tighter figure, because saying nothing must not be
a way to hold more than saying something.

#### `[local_agents]`

The global file's ceiling on everything the section above allows. It belongs to
the machine's owner and a repository cannot touch it:

```toml
[local_agents]
effects = ["read"]              # the most a locally declared type may cause
context = ["repository"]        # the most it may be served
# max_tokens = 20000            # unset: the ceiling is the type being run
# max_duration = "5m"           # the same, for wall clock
```

The two shipped keys are also the defaults, and they are what every type this
project ships already holds, so the floor is useful rather than a gesture.
Deleting the whole block does **not** lift the ceiling: it restores those
values, which is also what a settings file written before the feature existed
gets. An absent ceiling that read as no ceiling would be the same mistake as an
unmeasured cost that reads as free.

`effects = []` turns the feature off entirely: a type that may cause nothing is
refused, so no repository-declared type loads at all. `context` matters more
than it looks — `workspace` is the catalog of every repository on this machine
and `history` is what other runs of the same type were told, so `repository` is
the only level that cannot carry one project's file into another's prompt.

The two limits are commented out on purpose. A local type is already bounded by
the limits of the type it runs, which it may lower and never raise, so state a
number here only to hold a cloned repository below what a generous shipped type
would otherwise allow it.

A repository cannot set this block. `[local_agents]` is refused in an overlay
by name — *"it is the ceiling on what a repository's own types may do, so a
repository setting it would be granting itself the permissions it is being held
to."*

Everything else — `contract`, `[core]`, `[orchestrator]`, `[metrics]`,
`[backup]`, `[local_agents]`, `[[capability]]`, `[[implementation]]`,
`[[mcp_server]]` — is refused naming the block and why:

```
atenea: invalid_input: local settings /tmp/repo/.atenea/config.toml:
  mcp_server: it carries a command to launch, so a cloned repository would be
  handing this machine a process to run; repository.path: the path is the
  directory this file was found in; naming another one is the one way this
  layer could reach outside its own tree
```

Set `ATENEA_LOCAL_CONFIG=0` to ignore the layer entirely.

### Where it does not apply

`atenea run` — the service — reads the global file alone. It is one process
answering about every repository on the machine, and the overlay of whichever
directory it was started in would be wrong for all the others. `service
install` is the same, for the same reason.

One consequence is worth knowing rather than discovering: when an overlay
applies, `settings` names both files, joined by ` + `. `atenea status` compares
that string against the running service's before trusting its answer, so a
command run inside a repository with an overlay computes its own screen instead
of printing one built from settings that are not the ones in force here.

### A directory that declares nothing

`.atenea/` present with no `config.toml` inside is refused rather than ignored.
The mistake it catches is `.atenea/atenea.toml`, and left quiet it is a whole
settings file that never takes effect and never says so.

## What the repository asks for

The layer above is the settings a repository carries *for Atenea*. This one is
different in every way that matters: it is the configuration a team keeps for
**its own clients** — `.mcp.json` and `.claude/` for Claude Code, `opencode.json`
and `.opencode/` for opencode — and Atenea has no authority over it at all.

`atenea intent` reads it and answers one question: for each thing this project
asks for, what happens here?

```
asked for
  funnel     serena (local)
             -> provider serena: code.search, symbol.definition, symbol.references
                via serena.definition, serena.references, serena.search
  vouched    context7 (local)
             declared by Atenea and handed to clients by `atenea wrap`
  unmatched  dart (local, off in the project's settings)
             nothing registered here provides it

1 unmatched: this project asks for them and nothing here provides them
  dart (.mcp.json)
```

### It reads and it never runs

A `.mcp.json` is a list of commands somebody else wrote, and it arrives with a
git clone. The whole value of Atenea sitting between a client and its backends
disappears the moment reading such a file can start a process.

So the guarantee is not a policy, it is the shape of the code: a declaration's
command, arguments, environment and URL are **dropped at the parse boundary**.
They are not stored, not printed, not passed on. Nothing downstream can run
them because nothing downstream has them. A test walks every field of the
parsed result and fails if any of those strings is reachable from it.

That is the same trust model as the `.atenea/config.toml` allow-list one
section up, arrived at from the other side: there, a cloned repository may
adjust settings but may never name a command to launch; here, a cloned
repository may say what it wants but may never be the reason something starts.

### The unmatched list is the point

It is printed last, counted, and never abbreviated. A translator that quietly
dropped what it could not map would produce a report where "everything is
answered" and "half of it vanished" look identical, and the reader would learn
only that the command ran.

### One backend, two clients, one ask

A project that uses both clients declares the same backend twice. Those
collapse into one row, with both files named on it, because the count is what
the report is read for and a two-client repository does not want twice as much
as a one-client one.

Two declarations of one name that **disagree** — different transports — are
reported as inconsistent rather than resolved. A convenience that hides a
contradiction in somebody's own configuration has stopped being one. Enabled is
the exception and is a union: switched off for one client and on for another is
on, because reporting it as off would describe a machine nobody is running.

### It writes nothing

Not to `.claude/`, not to `.opencode/`, not anywhere. Measured the way the
overlay layer was: 30,956 files under the clients' own directories, snapshotted
before and after six invocations — nothing created, nothing removed, nothing
modified.
