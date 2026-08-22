# Atenea

An orchestration core for agents and MCP tooling that lives **outside** the CLIs
it serves. omp, Claude Code and OpenCode all connect to the same core.

> **Atenea decides and delegates, it does not execute.**
> The flow is always `goal -> capability -> implementation`.

**Documentation: https://tutitoos.github.io/atenea/** — architecture, settings
reference and getting started. The sources live in [`docs/`](docs/) and travel
in the same pull request as the code.

Version `1.0.0`, speaking contract `3.1.0` — stable core with optional external
providers. What landed is in the
[changelog](CHANGELOG.md). The core, the Capability Registry and the funnel
selector are in place, and so is the orchestrator: it takes one sentence, looks
at the repositories in scope, splits the work into a graph of steps, dispatches
them in waves and reviews every answer. Four adapters ship: two client CLIs
(`omp`, Claude Code) for text search, Serena over MCP for symbols, and
graph providers for symbol navigation. Every
attempt is measured — time, tokens and peak memory, per capability and per
implementation — into an embedded DuckDB base, and the funnel ranks on it:
what a step cost on the way out is what decides who answers next time in.
When Atenea itself breaks — a panic in a step, a background job failing where
nobody was listening — the fault is on disk before the process dies, and
`atenea incidents` reads it back.

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
fails as `unavailable`, the report says which binary it looked for, and the
command exits `6` — a commission that failed, not a crash. On a machine with no
client installed, set `runners = ["local"]` for the stand-in that searches the
disk directly.

Claude Code is the second client adapter. It is off by default because it is
the only far side that costs money per call; `runners = ["omp", "claudecode"]`
attaches both, and the funnel then ranks on cost, so a flat text search still
goes to `ripgrep` and the model is kept for what only a model can answer.

Money is a permission, granted per commission and split between its steps:
`budget_usd` under `[orchestrator]` is what one `task` may spend in total, not
per call, so four steps share one quarter rather than spending four. `--budget`
funds one commission above it. A commission that runs out gets
`permission_denied` on the paid steps and keeps going through whoever charges
nothing.

Effects work the same way: `code.search` causes `read` and `process` at once,
because every implementation of it is a binary. `[orchestrator] effects =
["process"]` grants that standing to every commission and question, on by
default so the P0 capability works out of the box; `--allow EFFECT` grants
one more to a single commission.

Symbols are the second family of capabilities: `symbol.definition`,
`symbol.references`, `symbol.implementations` and `symbol.overview`. They are
answered by Serena, which is not a CLI at all but an MCP server behind a local
proxy, so the third adapter speaks JSON-RPC over HTTP instead of spawning a
process.

```sh
./bin/atenea ask symbol.definition --repo current \
  --set file=internal/selector/selector.go --set line=161 --set column=20
```

`ask` is one capability against one repository — the atomic unit a workflow is
built out of, and the way a client that already has a cursor hands it over.
Atenea's contract names a *position* because that is what an editor has;
Serena's API names a *symbol*. Reading the word under the cursor is the
adapter's job, and the trace says which name it resolved to.

```text
run       20260808T172133-fcb614
task      ValidateOutput
verdict   ok
matches   30
spent     1.205s of tool time over 2 step(s), 1.276s elapsed
  explore  1 step(s), 778ms in 797ms
  work     1 step(s), 426ms in 442ms

discovered
  [repository] current: 30 hit(s) for "ValidateOutput"

run with --trace for the plan, the funnel and every review
```

Look before you split: the light first pass finds *where* the commission lands,
and the work that follows is narrowed to those areas. Every decision and every
review carries its trace — a choice nobody can explain is a choice nobody can
trust.

That run is this repository, and it shows the one case that cannot be narrowed:
this README quotes the search term, a hit with no directory above it has no area
to narrow to, and so the work ran wide rather than quietly dropping it. The
timings are omp's, the runner the shipped settings attach.

## Leave it running

Atenea is a core, not a command: the commands are how you talk to it, and the
background rhythms are what keep it worth talking to. Install it and it is
there after a reboot.

```sh
go build -o ~/.local/bin/atenea ./cmd/atenea
~/.local/bin/atenea service install     # systemd user unit or macOS launchd agent
# Linux:
systemctl --user start atenea.service
# macOS:
launchctl kickstart -k gui/$(id -u)/com.tutitoos.atenea
```

A per-user service, never a system one, and no port: the only thing it listens
on is a Unix socket in your own state root, `0600` in a `0700` directory, with every
caller checked against the kernel's answer for who they are before a byte is
read. Atenea holds no privilege worth borrowing, so `sudo` would only widen
what a bug could reach. `atenea status` asks the running service, because the
uptime and the chats open right now are only true of the process that keeps
them; with nothing running it falls back to disk and says so.

### Install a release

Published Linux and macOS releases can be installed with the checksum-verified installer:

```sh
curl -fsSL https://github.com/Tutitoos/atenea/releases/download/v1.0.0/atenea-install.sh \
  -o /tmp/atenea-install.sh
bash /tmp/atenea-install.sh --version 1.0.0
```

The installer supports Linux and macOS on `amd64` and `arm64`, writes to
`~/.local/bin`, and never enables a service unless `--service` is passed
explicitly. Linux uses a systemd user unit; macOS uses a per-user launchd
agent. Other systems require a source build.

Re-running it with a different pinned version updates the binary and keeps the
previous one as `~/.local/bin/atenea.previous`. Use
`bash /tmp/atenea-install.sh --rollback` to restore that copy, or
`--uninstall --service` to remove the binary and its background service.

What runs on its own: the measurement batch reaches disk every 30s, the history
is folded hourly, and every six hours a hard-linked copy of everything Atenea
has learned is taken, five kept in rotation. A start after a power cut repairs
what the cut left half-written *before* accepting any work, and says so.

```text
background
  rhythms      metrics.flush 30s, metrics.compact 1h, backup 6h
  copies       1 of 5 kept in /home/tutitoos/.local/state/atenea-backups, newest 2026-08-02 21:26
```

## Layout

```text
cmd/atenea/         entry point: the service and the operator commands
internal/           the brain, not importable from outside
  adapter/claudecode/      the client adapter: translates for the Claude Code CLI
  adapter/omp/             the client adapter: translates for the omp CLI
  adapter/serena/          the symbol adapter: MCP over HTTP, positions to names and back
  checkpoint/              run receipts on disk
  clock/                   the one lane every background rhythm runs in
  config/                  the single settings file
  core/                    wiring, status and clean shutdown
  metrics/                 the measurement base: DuckDB, batched, one writer
  notebook/                the crash notebook: Atenea's own faults, synced on write
  orchestrator/            the agent: explore, split, dispatch, review
  procstat/                weighing a finished child process, per platform
  registry/                the Capability Registry
  runner/local/            stand-in far side, for a machine with no client installed
  selector/                the funnel
  toolversion/             asking a tool who it is, once per process
pkg/contract/       the contract shared by the core and its adapters
docs/               documentation sources, served by Hugo on GitHub Pages
```

## Development

```sh
lefthook install        # do this first: installs the pre-commit and pre-push hooks
go test -race ./...     # the suite
air                     # hot reload, local development only
```

**Run `lefthook install` after cloning.** Git never clones hooks, so a fresh
checkout has none and unformatted code commits without complaint. One command
installs both: pre-commit runs `gofmt`, `go vet` and `golangci-lint`;
pre-push runs the suite with `-race`.

The hooks are a convenience, not the guarantee. **The enforced gate is the
release workflow**, which re-runs the linter and the full suite at tag time and
refuses to publish if either fails — it is the only check nobody can skip by
forgetting a setup step. `v0.6.0` is a tag with no release behind it for
exactly that reason; the [changelog](CHANGELOG.md) says why.

## Credits

Atenea leans on work other people did first. A thank-you, with a link to each:

- [ripgrep](https://github.com/BurntSushi/ripgrep) — the search engine behind the first capability
- [Serena](https://github.com/oraios/serena) — symbol-level navigation and editing
- Graph providers — repository symbol and relationship indexes exposed through MCP
- [Semgrep](https://github.com/semgrep/semgrep) — static analysis
- [Context7](https://github.com/upstash/context7) — version-accurate library documentation
- [ToolHive](https://github.com/stacklok/toolhive) — isolation and lifecycle for shared MCP servers
- [Chrome DevTools MCP](https://github.com/ChromeDevTools/chrome-devtools-mcp) — browser diagnostics
- [claude-mem](https://github.com/thedotmack/claude-mem) — persistent memory across sessions
- [DuckDB](https://github.com/duckdb/duckdb) — the analytical store the measurement base runs on
- [Hugo](https://github.com/gohugoio/hugo) and [hugo-book](https://github.com/alex-shpak/hugo-book) — these docs
- [lefthook](https://github.com/evilmartians/lefthook) and [Air](https://github.com/air-verse/air) — the development loop

That list is a human thank-you. What Atenea actually imports is in
[`go.mod`](go.mod), which is the technical answer to the same question.
