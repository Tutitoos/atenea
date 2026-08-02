---
title: Settings
weight: 3
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
runners = ["omp"]           # any of omp, claudecode, serena, local; [] dispatches nowhere
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
  budget_usd = 0.25                    # what one call may spend before it is cut
  timeout = "5m"                       # a model turn is slower than a tool call

  [orchestrator.serena]
  endpoint = "http://127.0.0.1:40010/mcp"   # a server, not a binary
  implementations = ["serena.definition", "serena.references", "serena.implementations"]
  timeout = "90s"                      # a language server indexing cold is slow, not stuck
```

`max_parallel` is the real brake on total memory: four steps of one wave run at
a time so a laptop stays responsive. `0` means no ceiling, which is a choice
for a build machine, not a default.

`runners` names the far sides of the contract, and it is a list because several
can be attached at once. `omp` is the client adapter that ships attached.
`claudecode` drives the Claude Code CLI and is off by default, because it is
the only far side that costs money per call. `serena` is not a CLI at all: it
is an MCP server, which is why its block takes a URL instead of a binary, and
it answers the three symbol capabilities rather than a text search. `local` is
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

`budget_usd` cannot be `0` either, for the same reason and with worse
consequences: a model turn with no ceiling is a runaway.

An implementation no attached runner can execute is not removed from the
catalog. It is dropped by the funnel's `reach` stage, which says so in the
trace — `no attached runner serves it`, not `down`, because a provider nobody
wired up is not a provider that is broken. The status screen lists them up
front, under `no runner`.

Setting `checkpoint_dir = ""` after an explicit path does not disable dumps; it
falls back to the default location. To turn checkpointing off, point the
orchestrator at no store at all — the directory is created on first write, so a
core that never receives a commission leaves nothing behind.

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
effects = ["read"]          # read | write | external

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
indexed_by = ["serena"]     # providers with a ready index HERE
```

An unclassified `scale` never disqualifies anyone: an unknown size is not a
proven mismatch, and dropping candidates over it would silently empty the
funnel.

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
