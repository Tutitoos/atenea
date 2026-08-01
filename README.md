# Atenea

An orchestration core for agents and MCP tooling that lives **outside** the CLIs
it serves. omp, Claude Code and OpenCode all connect to the same core.

> **Atenea decides and delegates, it does not execute.**
> The flow is always `goal -> capability -> implementation`.

**Documentation: https://tutitoos.github.io/atenea/** — architecture, settings
reference and getting started. The sources live in [`docs/`](docs/) and travel
in the same pull request as the code.

Status: alpha (`0.x.y`). The first brick is in place — the core, the Capability
Registry and the funnel selector running on constraints and health.

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
```

No setup needed: with no settings file present, Atenea boots on its built-in
defaults. `atenea config init` writes them out so you can edit them.

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
can trust.

## Layout

```text
cmd/atenea/        entry point: the service and the operator commands
internal/          the brain, not importable from outside
  config/            the single settings file
  core/              wiring, status and clean shutdown
  registry/          the Capability Registry
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
