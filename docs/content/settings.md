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

## The file replaces the defaults, it does not patch them

Atenea ships a full settings file compiled into the binary, and that is what
runs when no file exists on disk. The moment a file does exist it is used
*instead* — not merged on top. There is no layering: a setting you leave out is
not inherited from the built-in copy, it is absent.

That matters most for the catalog, because the catalog is the largest thing in
the file and the easiest to forget. A settings file containing only

```toml
contract = "1.0.0"

[orchestrator]
runners = ["omp", "claudecode"]
```

is a complete, valid file describing an Atenea that knows no capabilities at
all. It boots, `atenea status` shows the orchestrator red and `serves -`, and
every command answers `unknown capability`. Nothing is hidden and nothing
crashed; you asked for an empty catalog and got one.

So the way to change one setting is to start from the whole file:

```sh
atenea config init          # writes the built-in file, catalog and all
atenea config path          # says where that is
```

then edit it. Merging was considered and refused: a half-file whose meaning
depends on what a particular binary happened to ship is a file nobody can read
on its own, and an upgrade that changed a default would silently change a
machine whose settings file never moved.

## Skeleton

```toml
contract = "1.0.0"          # required: the contract version this file targets

[core]
shutdown_grace = "10s"      # margin a clean stop gives in-flight work
```

## The orchestrator

```toml
[orchestrator]
max_parallel = 4            # steps of one wave at a time; 0 lifts the ceiling
budget_usd = 0.25           # what ONE COMMISSION may spend, across every step
effects = ["process"]       # granted standing to every commission and question
runners = ["omp"]           # any of omp, claudecode, serena, codebasememory, local; [] dispatches nowhere
checkpoint_dir = ""         # "" uses $XDG_STATE_HOME/atenea/runs

  [orchestrator.omp]
  binary = "omp"                       # bare name is looked up on PATH
  implementations = ["ripgrep"]        # what the adapter answers for
  match_limit = 10000                  # matches one search asks omp for
  timeout = "30s"                      # after this, omp is stuck, not slow

  [orchestrator.local]
  implementations = ["ripgrep"]        # what the stand-in can actually execute
  skip_dirs = [".git", "node_modules"] # never walked

  [orchestrator.claudecode]
  binary = "claude"                    # bare name is looked up on PATH
  implementations = ["claude.search"]  # a different id from ripgrep's, on purpose
  # no ceiling here: what a call may spend arrives with the commission
  timeout = "90s"                      # ~13 model turns; the grant bites first

  [orchestrator.serena]
  endpoint = "http://127.0.0.1:40010/mcp"   # a server, not a binary
  implementations = ["serena.definition", "serena.references", "serena.implementations"]
  timeout = "90s"                      # a language server indexing cold is slow, not stuck

  [orchestrator.codebasememory]
  binary = "codebase-memory-mcp"       # bare name is looked up on PATH
  implementations = ["codebase-memory.calls", "codebase-memory.impact"]
  timeout = "90s"                      # opening an index cold is slow, not stuck
```

`max_parallel` is the real brake on total memory: four steps of one wave run at
a time so a laptop stays responsive. `0` means no ceiling, which is a choice
for a build machine, not a default.

`runners` names the far sides of the contract, and it is a list because several
can be attached at once. `omp` is the client adapter that ships attached.
`claudecode` drives the Claude Code CLI and is off by default, because it is
the only far side that costs money per call. `serena` is not a CLI at all: it
is an MCP server, which is why its block takes a URL instead of a binary, and
it answers the three symbol capabilities rather than a text search.
`codebasememory` is a CLI again, like `omp`, but answers from a call graph it
keeps on disk instead of searching or parsing anything live. `local` is
a stand-in that searches the disk directly, for a machine with no client
installed. An empty list leaves the core able to plan and choose but unable to
dispatch — a working core with nobody attached, and the status screen says so
rather than failing halfway through a commission.

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

An implementation no attached runner can execute is not removed from the
catalog. It is dropped by the funnel's `reach` stage, which says so in the
trace — `no attached runner serves it`, not `down`, because a provider nobody
wired up is not a provider that is broken. The status screen lists them up
front, under `no runner`.

Setting `checkpoint_dir = ""` after an explicit path does not disable dumps; it
falls back to the default location. To turn checkpointing off, point the
orchestrator at no store at all — the directory is created on first write, so a
core that never receives a commission leaves nothing behind.

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
by having worked recently and marked down by a run of failures, and both facts
come out of this base — so with it off, every provider that nothing has probed
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

## Implementations

```toml
[[implementation]]
id = "serena.search"
provider = "serena"         # who owns the index; several implementations may share one
capability = "code.search"

  [implementation.constraints]
  languages = ["go", "typescript"]   # empty means language-agnostic
  requires_index = true
  requires_vcs = false               # needs the repository under version control (e.g. a git diff against a baseline)
  min_scale = ""                     # "", small, medium, large
  max_scale = ""

  [implementation.cost]
  estimated_duration = "600ms"
  estimated_tokens = 900
  tool_version = ""                  # the version these estimates belong to

  [implementation.health]
  state = "unknown"                  # unknown | alive | degraded | down
  score = 0.0                        # 0..1, breaks ties inside one state
  reason = ""
```

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
