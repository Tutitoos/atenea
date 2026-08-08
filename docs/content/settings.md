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
contract = "2.2.0"

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
contract = "2.2.0"          # required: the contract version this file targets

[core]
shutdown_grace = "10s"      # margin a clean stop gives in-flight work
```

The `contract` line is the one field with no default: a file must say which
core it was written for, and a core refuses a file from a different major
version by name rather than reading it and hoping. Minor lag is fine and
always has been - a file targeting `2.0.0` keeps working against this `2.2.0`
core, because every minor bump only adds - so in practice this line moves once
per breaking release. `0.7.0` is the first one, and a file written for any
`1.x` core is refused on sight:

```text
settings ~/.config/atenea/atenea.toml: contract 1.0.0 is not supported by
this core (2.2.0): change the contract line to "2.2.0"; no other key moves
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
          "--host", "127.0.0.1", "--port", "{{port}}", "--project", "/srv/api"]
  env = ["SERENA_LOG_LEVEL=WARNING"]   # added to the inherited environment
  lifecycle = "on_demand"              # required: "persistent" or "on_demand"
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

It is also a ceiling. A client cannot open a chat claiming more than this,
and one that tries is refused at `initialize` rather than at its first
`tools/call` — a client told at the door can say so, one told mid-conversation
has already promised somebody the work. The ceiling arrives with the line: a
file that never wrote one has no ceiling, exactly as before.

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
serena_endpoint = ""        # optional: pin this repo to its own Serena URL
```

An unclassified `scale` or an unspecified `vcs` never disqualifies anyone: an
unknown fact is not a proven mismatch, and dropping candidates over it would
silently empty the funnel. `serena_endpoint` empty means the adapter's default
(`[orchestrator.serena].endpoint`) and a retarget via `activate_project` when
the previous call was on a different project; a set URL keeps that repository
on its own warm Serena process so alternating repos does not tear language
servers down.

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

## MCP servers

```toml
[[mcp_server]]
id = "serena"                        # the name the client will see
url = "http://127.0.0.1:40010/mcp"   # http endpoint
timeout = "5s"                       # bounds the check; omitted takes the default

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

Each block sets `url` or `command`, never both, and repeating an `id` is
refused rather than resolved. All three refusals are the same rule: the
payload is keyed by `id`, so a block that cannot be turned into exactly one
endpoint would end up either ignored or silently overwritten by the block
after it, and a declaration nobody can act on is worse than no declaration --
a client is told the server exists before anyone finds out it does not.

`atenea wrap opencode` handshakes every entry here and passes on only the ones
that answered. Captured on a real machine, both transports, one real failure:

```text
wrap opencode  6 checked: 5 declared, 1 refused

  declared  codebase-memory  stdio  codebase-memory-mcp --ui=true  codebase-memory-mcp 0.9.0 (9ms)
  declared  context7         stdio  context7-mcp --transport stdio  Context7 3.2.5 (315ms)
  declared  headroom         stdio  headroom mcp serve  headroom 1.29.0 (406ms)
  declared  semgrep          stdio  semgrep mcp --transport stdio  Semgrep 1.23.3 (1.953s)
  declared  serena           http   http://127.0.0.1:40010/mcp  Serena 1.28.1 (32ms)
  refused   chrome-devtools  http   http://127.0.0.1:40021/mcp
                             dial tcp 127.0.0.1:40021: connect: connection refused

  Declared means it answered an MCP handshake, not that its tools work.
  A refused server is left out of the payload, not switched off:
  opencode keeps whatever it already declares under that name.
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

Nothing is written to disk. The configuration lives in one environment
variable for the lifetime of the child process, so a client launched without
`wrap` is a client with exactly the configuration it had before, and there is
no `unwrap` because there is nothing to undo. `opencode` is the one client
wired today; a client configurable only by editing a file on disk cannot be
added here, because that guarantee is the one a file edit cannot make.

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
