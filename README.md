# Atenea

An orchestration core for agents and MCP tooling that lives **outside** the CLIs
it serves. omp, Claude Code, Codex and OpenCode all connect to the same core.

> **Atenea decides and delegates, it does not execute.**
> The flow is always `goal -> capability -> implementation`.

**Documentation: https://tutitoos.github.io/atenea/** — architecture, settings
reference and getting started. The sources live in [`docs/`](docs/) and travel
in the same pull request as the code.

Version `1.1.0`, speaking contract `3.6.0` — stable core with optional external
providers. The release is published with checksum-verified installers for
Linux and macOS on `amd64` and `arm64`; the [final audit](docs/content/v1-final-audit.md)
records the evidence and the remaining provider-dependent limits. What landed is in the
[changelog](CHANGELOG.md). The core, the Capability Registry and the funnel
selector are in place, and so is the orchestrator: it takes one sentence, looks
at the repositories in scope, splits the work into a graph of steps, dispatches
them in waves and reviews every answer. Six native adapters ship: `omp`, Claude
Code and Codex for client CLIs, Serena over MCP for symbols, and Kivgraph and
Tokensave for graph/context operations. OpenCode is an optional model backend,
not a native adapter. Every
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

Claude Code and Codex are optional client adapters and are off by default because
they may incur provider-side cost; `runners = ["omp", "claudecode", "codex"]`
attaches them, and the funnel ranks the available implementations on cost and
health, so a flat text search can still go to `ripgrep` when that is the best
choice.

Money is a permission, granted per commission and split between its steps:
`budget_usd` under `[orchestrator]` is what one `task` may spend in total, not
per call, so four steps share one quarter rather than spending four. `--budget`
funds one commission above it. A commission that runs out gets
`permission_denied` on the paid steps and keeps going through whoever charges
nothing.

The core also verifies the provider's reported charge at the boundary, so an
adapter cannot return a successful result above its stamped share. This is a
postcondition, not a promise that every provider can cancel an already-running
turn at the exact cent. Use `--confirm` on `task` or `ask` when a human
should review the budget and effects in a TTY before dispatch.

Effects work the same way: `code.search` causes `read` and `process` at once,
because every implementation of it is a binary. `[orchestrator] effects =
["process"]` grants that standing to every commission and question, on by
default so the P0 capability works out of the box; `--allow EFFECT` grants
one more to a single commission.

Symbols include `symbol.search`, `symbol.definition`, `symbol.references`,
`symbol.implementations`, `symbol.overview`, `symbol.calls`,
`symbol.consumers`, `symbol.get` and `symbol.unresolved`. Serena answers the
language-server symbol operations over MCP; Kivgraph and Tokensave provide the
graph-backed operations where an indexed repository is required.

```sh
./bin/atenea ask symbol.definition --repo current \
  --set file=internal/selector/selector.go --set line=167 --set column=20
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
curl -fsSL https://github.com/Tutitoos/atenea/releases/download/v1.1.0/atenea-install.sh \
  -o /tmp/atenea-install.sh
bash /tmp/atenea-install.sh --version 1.1.0
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
  copies       1 of 5 kept in ~/.local/state/atenea-backups
```

## Layout

```text
cmd/atenea/             entry point: the service and the operator commands
cmd/atenea-benchmark/   the evidence run: profiles, raw output and summaries
internal/               the brain, not importable from outside
  adapter/claudecode/       the client adapter: translates for the Claude Code CLI
  adapter/codex/            the client adapter: translates for the Codex CLI
  adapter/kivgraph/         graph adapter: impact, indexing and structural queries
  adapter/omp/              the client adapter: translates for the omp CLI
  adapter/serena/           the symbol adapter: MCP over HTTP, positions to names and back
  adapter/tokensave/        context and call adapter for the indexed repository
  agent/                    one declared agent as one real process
  agent/filereader/         the minimal agent: one file, no model, no key
  agent/model/              the seam an agent calls its own model through
  agent/opencode/           the isolated model runner for OpenCode's CLI
  agent/plancheck/          the planner's TOML checked against the engine
  agent/planner/            explore and plan, two runs so a retry is cheap
  agent/reviewer/           the auditor: only claims it can prove on the spot
  agent/semanticreviewer/   does the conclusion follow from the evidence
  allowance/                money-to-reading arithmetic, in one place
  backup/                   five complete copies in rotation, never a chain
  benchmark/                the evidence format for reproducible runs
  buildinfo/                the version of the running binary
  checkpoint/               run receipts on disk
  clientconfig/             reads a repository's .mcp.json, never runs it
  clock/                    the one lane every background rhythm runs in
  config/                   the single settings file
  core/                     wiring, status and clean shutdown
  dashboard/                the optional web UI beside an MCP declaration
  decision/                 a commission in prose to an explainable plan
  floor/                    what a turn costs before it has done anything
  ipc/                      the door a client knocks on: one unix socket
  mcpprobe/                 asks an MCP server if it is really there
  mcpstdio/                 JSON-RPC over a child's own stdin and stdout
  metrics/                  the measurement base: DuckDB, batched, one writer
  notebook/                 the crash notebook: Atenea's own faults, synced on write
  orchestrator/             the agent: explore, split, dispatch, review
  passthrough/              somebody else's tools, re-offered under a raw. id
  pidlock/                  one named right at a time, held by a pid file
  platform/                 where data lives and how Atenea starts, per OS
  procgroup/                making a canceled child and its helpers stop
  procstat/                 weighing a finished child process, per platform
  registry/                 the Capability Registry
  runner/local/             stand-in far side, for a machine with no client installed
  selector/                 the funnel
  statusline/               Atenea's traffic light on a client's screen
  supervisor/               the MCP servers Atenea launches and keeps alive
  testroot/                 short test paths, so a unix socket still fits
  toolpath/                 a client's binary, not one installation path
  toolversion/              asking a tool who it is, once per process
  trace/                    which agents ran, when, and how they ended
  workflow/                 a DAG of agent steps: waves, ceilings, failure
  wrap/                     launching a client on configuration Atenea checked
pkg/contract/           the contract shared by the core and its adapters
docs/                   documentation sources, served by Hugo on GitHub Pages
benchmarks/             the recorded evidence a report is audited against
tools/                  standalone scripts: MCP drift check, loopback recorder
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
forgetting a setup step. The historical `v0.6.0` tag has no release behind it
for exactly that reason; the [changelog](CHANGELOG.md) says why. The current
published release is `v1.1.0`.

## Credits

Atenea leans on work other people did first. A thank-you, with a link to each:

- [ripgrep](https://github.com/BurntSushi/ripgrep) — the search engine behind the first capability
- [Serena](https://github.com/oraios/serena) — symbol-level navigation and editing
- Graph providers — repository symbol and relationship indexes exposed through MCP
- [Semgrep](https://github.com/semgrep/semgrep) — static analysis
- [Context7](https://github.com/upstash/context7) — version-accurate library documentation
- [Chrome DevTools MCP](https://github.com/ChromeDevTools/chrome-devtools-mcp) — browser diagnostics
- [claude-mem](https://github.com/thedotmack/claude-mem) — persistent memory across sessions
- [DuckDB](https://github.com/duckdb/duckdb) — the analytical store the measurement base runs on
- [Hugo](https://github.com/gohugoio/hugo) and [hugo-book](https://github.com/alex-shpak/hugo-book) — these docs
- [lefthook](https://github.com/evilmartians/lefthook) and [Air](https://github.com/air-verse/air) — the development loop

That list is a human thank-you. The Go dependencies imported by Atenea are in
[`go.mod`](go.mod); external CLIs, MCP servers and documentation tools are
configured or installed separately.
