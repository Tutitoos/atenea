---
title: Getting started
weight: 1
---

# Getting started

## Requirements

Go 1.24 or newer. Nothing else: Atenea is a single binary and its settings file.

## Build and run

```sh
go build -o bin/atenea ./cmd/atenea
./bin/atenea version
```

A fresh install boots without any setup. When no settings file exists, Atenea
falls back to the built-in defaults, which already carry the P0 capability and
its four candidate providers.

```sh
./bin/atenea status
```

Dispatching real work needs the `omp` CLI on `PATH`: that is the client adapter
the defaults attach. Without it Atenea still plans and chooses — the step fails
as `unavailable`, says which binary it looked for, and the catalog marks that
provider down. On a machine with no client installed, set
`runners = ["local"]` for the stand-in that searches the disk directly.

### Attaching Claude Code

The second client adapter drives the `claude` CLI you are already logged into.
It is off by default because it is the only far side that costs money per call:

```toml
[orchestrator]
runners = ["omp", "claudecode"]
```

No API key is involved. Atenea never sees a credential — it speaks to a client
that is already authenticated, and the session lives inside that client.

With both attached, `ripgrep` still answers an ordinary search: the funnel
ranks on cost, and a flat text search is two orders of magnitude cheaper
through a tool than through a model. Claude Code wins when the cheaper
providers cannot work on that repository or are down — and a `[[selector.rule]]`
will hand it the work outright when you want it.

### Attaching Serena for symbols

Serena answers `symbol.definition`, `symbol.references` and
`symbol.implementations`. It is not a CLI: it runs as an MCP server behind a
local proxy, so the setting is a URL rather than a binary.

```toml
[orchestrator]
runners = ["omp", "serena"]

  [orchestrator.serena]
  endpoint = "http://127.0.0.1:40010/mcp"
```

Two things have to be true for the funnel to reach it. Serena drives one
language server per language, so the repository's `languages` must be among the
ones the implementation declares; and it is useless on a repository it has not
indexed, so name it in `indexed_by`:

```toml
[[repository]]
id = "current"
path = "."
languages = ["go"]
indexed_by = ["serena"]
```

Miss either and the funnel drops it at `reach` or `constraints` and says which
— a provider nobody wired up is not a provider that is broken.

## Write your own settings

```sh
./bin/atenea config path     # where Atenea will look
./bin/atenea config init     # write the built-in defaults there
```

Settings are resolved in this order:

1. `--config PATH`
2. `$ATENEA_CONFIG`
3. `$XDG_CONFIG_HOME/atenea/atenea.toml`, falling back to `~/.config/atenea/atenea.toml`
4. the built-in defaults

A file named explicitly with `--config` must exist. A missing file at the
default location is not an error — that is the fresh-install path — but a
missing file you asked for by name is, because staying quiet there would hide a
typo.

## Ask the funnel a question

```sh
./bin/atenea select code.search --repo current
```

```text
capability  code.search
repository  current
chosen      ripgrep  (cheapest of the healthy ones (estimated))

funnel
  constraints  4 in -> 2 out: claude.search, ripgrep
      dropped codebase-memory.search: needs an index from provider codebase-memory, repository has none
      dropped serena.search: needs an index from provider serena, repository has none
  reach        2 in -> 1 out: ripgrep
      dropped claude.search: no attached runner serves it
  health       1 in -> 1 out: ripgrep
  choice       1 in -> 1 out: ripgrep
```

Every decision carries its trace. A choice nobody can explain is a choice nobody
can trust, and the trace is what later turns into the observability layer.

Each stage answers a different question, and the trace says which one settled
it. `constraints` asks whether a provider can work on this repository at all;
`reach` asks whether anything attached can even invoke it; `health` asks whether
it is well. What is left, `cost` ranks — and the word `estimated` in the reason
is the trace admitting that no measurement exists yet, so the number is the one
the catalog declared rather than one Atenea observed. Hand it some work and
that word changes; [the base](#hand-it-a-commission) is where it comes from.

## Hand it a commission

`select` asks who *would* answer. `task` hands the whole job over: the
orchestrator looks at every repository in scope, splits the work, dispatches it
and reviews what comes back.

```sh
./bin/atenea task "ValidateOutput" --trace
```

```text
run       20260802T003739-e22d82
task      ValidateOutput
verdict   ok
matches   11
spent     12ms over 2 step(s)
  explore  1 step(s), 6ms
  work     1 step(s), 5ms

discovered
  [repository] current: 11 hit(s) for "ValidateOutput", under internal, pkg

plan
  wave 1  explore-current
  wave 2  search-current

steps
  explore-current      explore  ripgrep                  6ms
      review   child=ok parent=ok (output matches the capability)
      dropped  codebase-memory.search: needs an index from provider codebase-memory, repository has none
      dropped  serena.search: needs an index from provider serena, repository has none
  search-current       work     ripgrep                  5ms
      review   child=ok parent=ok (output matches the capability)
      scope    internal, pkg
      dropped  codebase-memory.search: needs an index from provider codebase-memory, repository has none
      dropped  serena.search: needs an index from provider serena, repository has none
```

Two heights, like the status screen: the summary always, the full trace only
when asked for. Drop `--trace` and everything from `plan` down disappears.

The look found hits under `internal` and `pkg` only, so the work that followed
was narrowed to those two areas instead of walking the tree again.

A hit sitting at the repository root is the one case that cannot be narrowed:
there is no directory above it, so the work runs wide rather than quietly
dropping it. That is easy to see for yourself — this page and the README now
quote the search term, so running the example against Atenea's own repository
reports more hits and no `scope` line.

`--repo` narrows the commission; repeat it for several. Every run leaves a
receipt under `$XDG_STATE_HOME/atenea/runs`, including one that was cut short.

What each step cost lands beside it, in `metrics.duckdb`: one row per attempt,
including the ones that failed. That base is what the funnel ranks on. Nothing
has to be switched on for it, and `enabled = false` under `[metrics]` stops it.

Run the same ask a few times and watch the reason on `select` change. It opens
at `(estimated)`, passes through `break-in turn` while each provider is handed
the work often enough to earn its own numbers, and settles at `(measured)` —
at which point the estimates in the settings file stop mattering for that
repository. A provider the file guessed was expensive can win here, and that
is the entire point of handing out those turns.

What a step *charged*, if anything did, is not in there. Money is never one of
the axes the funnel ranks on, so it stays out of the base and goes on the
receipt instead: a `charged` line on the summary when a run cost anything, the
step that incurred it under `--trace`, and `spent_usd` on the run in
`$XDG_STATE_HOME/atenea/runs`.

## Ask for one capability

`task` is a commission: explore, split, dispatch. `ask` is the atom underneath
it — one capability, one repository, no planning:

```sh
./bin/atenea ask symbol.definition --repo current \
  --set file=internal/selector/selector.go --set line=118 --set column=18
```

```text
run       20260802T132043-9f7e98
task      symbol.definition in current
verdict   ok
spent     41ms over 1 step(s)
  ask      1 step(s), 41ms

discovered
  [repository] position internal/selector/selector.go:118:18 names "Select", which is symbol Selector/Select
  [repository] serena answered symbol.definition for current with 1 location(s)

answer
  location
    line     118
    path     internal/selector/selector.go
```

`--set` takes `name=value` and is typed by the capability's own declaration:
`line` is an integer because the capability says so, and a value that is not
one is refused before anything is dispatched. Repeat it for a list field, and
repeat the flag for each entry:

```sh
./bin/atenea ask symbol.references --repo current \
  --set file=pkg/contract/capability.go --set line=140 --set column=6 \
  --set scope=internal --set scope=cmd
```

There is no `matches` line: a commission counts hits across repositories, an
ask has an answer. Printing a zero nobody counted would read as "found
nothing" rather than "did not count".

The trace names the symbol the position resolved to. That is not decoration:
Atenea speaks positions and Serena speaks symbols, so the answer cannot be
checked against the question without it.

## Run it as a service

```sh
./bin/atenea run
```

It boots the catalog and waits. `Ctrl-C` or `SIGTERM` starts a clean stop: new
work is refused immediately, and whatever is already running gets the margin set
by `core.shutdown_grace`.

## Exit codes

A script has to be able to tell a broken settings file from a provider that is
simply down, so the failure bins map onto distinct codes.

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `2` | `invalid_input` — bad arguments or a broken settings file |
| `3` | `not_found` — unknown capability, repository, or nothing fits |
| `4` | `unavailable` / `timeout` |
| `5` | `permission_denied` / `external_denied` |
| `6` | the commission ran and came back `failed` |
| `1` | anything unsorted, which means a bug |

`6` is a different axis from the rest. Nothing about the call was wrong and the
report on stdout is complete — the work is what failed. It cannot borrow `1`,
which means a bug, and it cannot borrow the bin of whichever step failed,
because several steps can fail for different reasons in one run. The reason
lives in the report; the exit code only says that there is one.

## Development

```sh
go test -race ./...     # the suite
lefthook install        # pre-commit: gofmt, go vet, golangci-lint
air                     # hot reload while developing, local only
```

### Publishing these docs

The `docs` workflow builds this site and deploys it to GitHub Pages on every
push that touches `docs/`. It needs Pages to already exist, and the workflow
cannot create it: `GITHUB_TOKEN` can deploy to a site, but creating one is
repository administration and that escalation is deliberately closed. On a
fresh fork it is one command, run once, by an account that administers the
repository:

```sh
gh api -X POST /repos/OWNER/REPO/pages -f build_type=workflow
```
