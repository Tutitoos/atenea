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
its three candidate providers.

```sh
./bin/atenea status
```

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
chosen      ripgrep  (healthiest surviving implementation)

funnel
  constraints  3 in -> 1 out: ripgrep
      dropped codebase-memory.search: needs an index from provider codebase-memory, repository has none
      dropped serena.search: needs an index from provider serena, repository has none
  health       1 in -> 1 out: ripgrep
  choice       1 in -> 1 out: ripgrep
```

Every decision carries its trace. A choice nobody can explain is a choice nobody
can trust, and the trace is what later turns into the observability layer.

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
| `1` | anything unsorted, which means a bug |

## Development

```sh
go test -race ./...     # the suite
lefthook install        # pre-commit: gofmt, go vet, golangci-lint
air                     # hot reload while developing, local only
```
