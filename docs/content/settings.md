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
contract = "3.0.0"

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
for all four symbol capabilities. Write `implementations = []` in the same
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
contract = "3.0.0"          # required: the contract version this file targets

[core]
shutdown_grace = "10s"      # margin a clean stop gives in-flight work
```

The `contract` line is the one field with no default: a file must say which
core it was written for, and a core refuses a file from a different major
version by name rather than reading it and hoping. Minor lag is fine and
always has been - a file targeting `2.0.0` keeps working against this `2.3.0`
core, because every minor bump only adds - so in practice this line moves once
per breaking release. `0.7.0` is the first one, and a file written for any
`1.x` core is refused on sight:

```text
settings ~/.config/atenea/atenea.toml: contract 1.0.0 is not supported by
this core (2.3.0): change the contract line to "2.3.0"; no other key moves
```

Do that and you are done. The refusal is deliberately not a fallback to the
defaults: a file quietly ignored is a machine running settings nobody chose.
A file from a *newer* core than the binary is told to upgrade the binary
instead, because no edit to the file can fix that one.

## The orchestrator

```toml
[orchestrator]
max_parallel = 4            # steps of one wave at a time; 0 lifts the ceiling
budget_usd = 0.25           # what ONE COMMISSION may spend, across every step
effects = ["process"]       # granted standing to every commission and question
client_effects = ["process"] # the same, for a chat a client opened; also its ceiling
runners = ["omp"]           # any of omp, claudecode, serena, codebasememory, local; [] dispatches nowhere
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
  binary = "claude"                    # bare name is looked up on PATH
  implementations = ["claude.search"]  # a different id from ripgrep's, on purpose
  # no ceiling here: what a call may spend arrives with the commission
  timeout = "90s"                      # ~13 model turns; the grant bites first

  [orchestrator.serena]
  endpoint = "http://127.0.0.1:40010/mcp"   # a server, not a binary
  implementations = ["serena.definition", "serena.references", "serena.implementations", "serena.overview"]
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

  [orchestrator.codebasememory]
  binary = "codebase-memory-mcp"       # bare name is looked up on PATH
  implementations = ["codebase-memory.calls", "codebase-memory.overview", "codebase-memory.impact", "codebase-memory.index"]
  timeout = "90s"                      # opening an index cold is slow, not stuck
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
it answers the four symbol capabilities rather than a text search.
`codebasememory` is a CLI again, like `omp`, but answers from a call graph it
keeps on disk instead of searching or parsing anything live. `local` is
a stand-in that searches the disk directly, for a machine with no client
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

A chat opened by a client holds this list. That sentence is younger than the
key: through `0.10.0` a chat opened holding nothing at all, so
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
effects = ["read", "process"]  # read | write | external | process

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

`codebase-memory.overview` ships `{ depth = 0 }`: its graph holds what a file
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
for a medium repository or bigger -- on this catalog that is `symbol.calls` and
`code.impact`, which then look unimplemented rather than unclassified. Set it
once you know, and the two come back.

`indexed_by` is a starting point the operator typed by hand, not a live
fact -- a settings file does not watch the disk, so it can drift the moment
an index is built or lost after the file was last edited. `atenea detect
[--repo ID]` asks every attached provider that can answer whether it
already holds a ready index and corrects the belief in memory for the rest
of that process's run, the same one-place-a-catalog-entry-changes-while-
running exception `SetHealth` already is for a provider's health -- it
writes nothing back to this file, so a later invocation starts again from
what is declared here. When a provider genuinely has nothing to detect,
`atenea ask repository.index --repo ID` builds one instead: `write` and
`process` effects, gated the same as any other capability that touches the
machine rather than only answering from it.

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

`context` is a permission, not a delivery. Only the levels named here are ever
sent, and a level nobody asked for is absent from the payload rather than
present and empty, so an agent cannot read a blank field as an answer about
the world. `effects` is a ceiling a child may only narrow. Both limits are
required: an unset ceiling is not "no ceiling", it is a ceiling nobody decided,
and `max_duration` is what turns a hung agent into a `timeout` verdict instead
of a process nobody is waiting on.

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

`pool` is which parallel lane a type belongs to, `agent` or `review`, and it
defaults to `agent`. Reviews are separated because one lane holding both would
starve auditing exactly when the machine is busiest -- every slot full of
agents, the reviewer queued behind them, answers piling up unjudged. The
scheduler that honours it is the workflow engine below; `atenea agent` runs one
agent at a time and has no cap to compete for, so the lane only starts to
matter once a graph is running.

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
atenea workflow resume <id> [--redo STEP]
```

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
id = "read-changelog"
agent = "filereader"
needs = ["read-readme"]                   # an edge means AFTER
objective = "read CHANGELOG.md and answer"
files = ["CHANGELOG.md"]
criterion = "the counts match the file"
effects = ["read"]
budget_usd = 0.25
```

Nothing in Atenea writes one of these yet. There is no orchestrator, no model
picking steps and no growth mid-run: the graph arrives complete and is executed
exactly as written, which is why every refusal below happens before anything
spawns.

Nothing passes between steps either. An edge is an order, not a pipe: a step is
handed the task written in the file and nothing the step before it found. One
consequence worth knowing before you try it -- **`reviewer` is not usable as a
graph step yet.** A reviewer reads its case from the `subject` on its
assignment, a graph step has no subject to give it, and it answers `incomplete`
saying exactly that. Auditing today goes through `atenea agent --review`, which
hands over the answer itself.

Steps with no unmet dependency run together, up to the ceiling of their lane.
Ready steps beyond it wait: the queue is the pending set in declaration order,
not a second list, so the same graph makes the same choices on a slower
machine. A step whose dependency did not end `ok` never runs and reads as
`blocked`; its siblings are untouched, which is the point of the graph being a
graph.

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

### Money

`budget_usd` on the graph is the grant; the shares on the steps divide it and
are refused if they add up to more, because money is split rather than copied.

What was **spent** reads as `unmeasured`, and will until something can report a
charge: the agent report wire carries no cost and no token count, so there is
no number to add up. That is why the column is empty rather than zero. A
receipt printing `$0.00 spent` for a real run would be the same lie as list
price on subscription traffic, and worse for looking audited.


## MCP servers

```toml
[[mcp_server]]
id = "serena"                        # the name the client will see
url = "http://127.0.0.1:40010/mcp"   # http endpoint
timeout = "5s"                       # bounds the check; omitted takes the default
expose = "off"                       # off (default) points the client here; raw is a passthrough

[[mcp_server]]
id = "codebase-memory"
command = ["codebase-memory-mcp"]    # stdio; started once per check, then killed
[mcp_server.env]
CODEBASE_MEMORY_UI = "false"
```

This list is not the catalog and nothing dispatches against it. Atenea reaches
its own providers through adapters; these are endpoints `atenea wrap` hands to
*someone else's* client so that client stops spawning a private copy of a
server that is already running.

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

`effects` is what those tools are authorized to cause, in the same four names
capabilities use: `read`, `write`, `external`, `process`. Atenea cannot infer
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
which through `0.10.0` was every chat there was. The same call on a machine
whose `client_effects` grants `process` now runs: the refusal was the ceiling
being unreachable, not `semgrep_scan` being forbidden.

A raw backend is also **held back from `atenea wrap`**. Every other entry is
handed to the client so it can talk to the shared server directly; doing that
for a raw one would point the client past the allow list and the effects
check, so the budget would be bypassed by the very command meant to apply it.
It is still probed and still reported, under `held`.

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

So the overlay accepts three things and refuses the rest **by name**, with the
reason:

| allowed | what it says |
| --- | --- |
| `[[repository]]` — `languages`, `scale`, `vcs`, `indexed_by` | what this repository is |
| `[[selector.rule]]` | which implementation to prefer, for this repository only |
| `[security] sensitive` | further files to treat as delicate |

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

Everything else — `contract`, `[core]`, `[orchestrator]`, `[metrics]`,
`[backup]`, `[[capability]]`, `[[implementation]]`, `[[mcp_server]]` — is
refused naming the block and why:

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
