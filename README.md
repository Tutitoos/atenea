# Atenea

An orchestration core for agents and MCP tooling that lives **outside** the CLIs
it serves. omp, Claude Code and OpenCode all connect to the same core.

> **Atenea decides and delegates, it does not execute.**
> The flow is always `goal -> capability -> implementation`.

**Documentation: https://tutitoos.github.io/atenea/** — architecture, settings
reference and getting started. The sources live in [`docs/`](docs/) and travel
in the same pull request as the code.

Status: alpha (`0.x.y`). The core, the Capability Registry and the funnel
selector are in place, and so is the orchestrator: it takes one sentence, looks
at the repositories in scope, splits the work into a graph of steps, dispatches
them in waves and reviews every answer. The far side of that dispatch is now a
real client adapter driving the `omp` CLI, not a stand-in.

## Why

Tooling changes constantly. A workflow written against `ripgrep` breaks the day
`ripgrep` is replaced. A workflow written against `code.search` does not: the
capability is stable, and which tool answers it is a decision Atenea makes from
what it knows about the repository in front of it.

## Try it

```sh
go build -o bin/atenea ./cmd/atenea
./bin/atenea status
./bin/atenea select code.search --repo current
./bin/atenea task "ValidateOutput"
```

No setup needed: with no settings file present, Atenea boots on its built-in
defaults. `atenea config init` writes them out so you can edit them.

`task` dispatches to `omp`, so it needs that CLI on `PATH`. Without it the step
fails as `unavailable` and says so — nothing crashes. On a machine with no
client installed, set `runner = "local"` for the stand-in that searches the
disk directly.

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

run with --trace for the plan, the funnel and every review
```

Look before you split: the light first pass finds *where* the commission lands,
and the work that follows is narrowed to those areas. Every decision and every
review carries its trace — a choice nobody can explain is a choice nobody can
trust.

(Run it against this repository and the numbers will differ: this README and
the docs now quote the search term themselves. The README is a root file, and
a hit with no directory above it cannot be narrowed away — so the work runs
wide rather than quietly dropping it.)

## Layout

```text
cmd/atenea/        entry point: the service and the operator commands
internal/          the brain, not importable from outside
  adapter/omp/       the client adapter: translates for the omp CLI
  checkpoint/        run receipts on disk
  config/            the single settings file
  core/              wiring, status and clean shutdown
  orchestrator/      the agent: explore, split, dispatch, review
  registry/          the Capability Registry
  runner/local/      stand-in far side, for a machine with no client installed
  selector/          the funnel
pkg/contract/      the contract shared by the core and its adapters
docs/              documentation sources, served by Hugo on GitHub Pages
```

## Development

```sh
go test -race ./...     # the suite
lefthook install        # pre-commit: gofmt, go vet, golangci-lint
air                     # hot reload, local development only
```

## Credits

Atenea leans on work other people did first. A thank-you, with a link to each:

- [ripgrep](https://github.com/BurntSushi/ripgrep) — the search engine behind the first capability
- [Serena](https://github.com/oraios/serena) — symbol-level navigation and editing
- [Codebase Memory](https://github.com/entrepeneur4lyf/codegraph-rust) — the code knowledge graph
- [Semgrep](https://github.com/semgrep/semgrep) — static analysis
- [Context7](https://github.com/upstash/context7) — version-accurate library documentation
- [ToolHive](https://github.com/stacklok/toolhive) — isolation and lifecycle for shared MCP servers
- [Chrome DevTools MCP](https://github.com/ChromeDevTools/chrome-devtools-mcp) — browser diagnostics
- [claude-mem](https://github.com/thedotmack/claude-mem) — persistent memory across sessions
- [DuckDB](https://github.com/duckdb/duckdb) — the analytical store the metrics base will run on
- [Hugo](https://github.com/gohugoio/hugo) and [hugo-book](https://github.com/alex-shpak/hugo-book) — these docs
- [lefthook](https://github.com/evilmartians/lefthook) and [Air](https://github.com/air-verse/air) — the development loop

That list is a human thank-you. What Atenea actually imports is in
[`go.mod`](go.mod), which is the technical answer to the same question.
