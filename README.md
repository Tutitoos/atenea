# Atenea

A local core that connects agents, CLI clients and MCP tools through one
capability catalog. omp, Claude Code, Codex and OpenCode can share the same
service, provider configuration and execution history.

> **Atenea decides and delegates.** A request names a capability; Atenea selects
> an implementation, dispatches it through an adapter and reviews the result.

```text
goal → capability → provider selection → implementation → reviewed result
```

[Documentation](https://tutitoos.github.io/atenea/) ·
[Releases](https://github.com/Tutitoos/atenea/releases) ·
[Changelog](CHANGELOG.md) ·
[Configuration reference](docs/content/settings.md)

**Source version:** `1.1.0` · **Adapter contract:** `4.1.0`

This README describes the current checkout, including unreleased changes.
The product version in the source does not establish that a matching release
has been published. For an installed binary, check `atenea version` and use the
documentation at its release tag. Contract 3.x configurations require the
[4.0 migration](#migrating-from-contract-3x) before this source version can load them.

## What it does

- Routes capabilities such as `code.search` through repository constraints,
  attached runners, provider health and measured time, tokens and memory.
- Answers one capability call at a time, from the CLI with `ask` or from an MCP
  client. Each call is dispatched to the selected implementation, reviewed
  and written to a receipt.
- Exposes configured capabilities and explicitly declared raw MCP tools through
  one MCP connection. Clients discover the actual tool surface at runtime.
- Applies effect permissions and spending grants, records attempts and run
  receipts, and supports diagnostics and state backups.
- Provides terminal activity reports.

Atenea no longer plans multi-step work itself: `atenea task` and
`atenea resume` were removed. Callers that need several steps chain `ask` or
MCP calls.

The [architecture guide](docs/content/architecture.md) explains the selector,
orchestrator and adapter boundaries. [Current limits](docs/content/not-built-yet.md)
describe behavior that is still unavailable or depends on external providers.

## Install

### Build the current source

Use Linux or macOS with Go **1.26.10 or newer** and a C/C++ toolchain for DuckDB's
cgo bindings. The supported release targets are `amd64` and `arm64` on both
systems. External providers are installed separately.

```sh
git clone https://github.com/Tutitoos/atenea.git
cd atenea
go build -o bin/atenea ./cmd/atenea
export PATH="$PWD/bin:$PATH"
atenea version
```

Swift and the macOS permissions setup are needed for the optional desktop
helper.

### Install a published release

Choose an existing version from [Releases](https://github.com/Tutitoos/atenea/releases).
Enter its number without the leading `v` below. The installer downloads that
specific artifact and verifies it against the release's `SHA256SUMS`.

```sh
printf 'Published version (without v): '
read -r atenea_release
curl -fsSL "https://github.com/Tutitoos/atenea/releases/download/v${atenea_release}/atenea-install.sh" \
  -o /tmp/atenea-install.sh &&
bash /tmp/atenea-install.sh --version "$atenea_release"
export PATH="$HOME/.local/bin:$PATH"
atenea version
```

Installation writes `~/.local/bin/atenea`. Add `--service` to register the
background service. Updating keeps the previous binary at
`~/.local/bin/atenea.previous`; `bash /tmp/atenea-install.sh --rollback` restores
it. Service recovery and removal are covered in the
[operations guide](docs/content/operations.md).

The examples below target the current source and its contract. An older release
may have different commands, configuration and available capabilities.

## First run

From the repository you want Atenea to work on, inspect and initialize settings:

```sh
atenea config path
atenea config init
```

`config init` writes the built-in catalog and records that directory as the
absolute path of repository `current`. It refuses to overwrite an existing file;
use `atenea config show` to inspect your current settings instead.

The default runner is `omp`, which requires its CLI on `PATH`. For a first run
without an external client or model, edit the **existing** `[orchestrator]`
table in the generated file, keeping the rest of the catalog:

```toml
[orchestrator]
runners = ["local"]
```

The local runner provides filesystem text search. It is a development stand-in:
it skips configured sensitive paths and directories, but does not interpret
`.gitignore` like ripgrep.

From the Atenea checkout, try:

```sh
atenea status
atenea select code.search --repo current
atenea ask code.search --repo current \
  --set query=ValidateOutput --set scope=pkg
```

`select` explains who would answer without dispatching the capability. `ask`
executes one request. Change the query and scope for a different repository.
Add `--json` to `ask` for structured output; `--trace` prints the funnel and
the review of the step.

### Configuration

Global settings are resolved in this order:

1. `--config PATH`
2. `$ATENEA_CONFIG`
3. `$XDG_CONFIG_HOME/atenea/atenea.toml`, or `~/.config/atenea/atenea.toml`
4. Built-in defaults when no default settings file exists

An explicitly requested file must exist. Without a file, repository `current`
refers to the working directory. A background service requires absolute
repository paths, so initialize settings before starting it.

A global file replaces the capability and implementation catalog; it is not a
small patch to the embedded catalog. Edit the generated file rather than using
the runner fragment above as a complete configuration. Repository-local
`.atenea/config.toml` files provide a restricted overlay.

See the [settings reference](docs/content/settings.md) and
[embedded defaults](internal/config/default.toml) for repository declarations,
provider processes, selector rules and permission settings.

## Providers and capabilities

Only configured runners and their reachable implementations can answer work.
The source includes these adapters and the local stand-in:

| Runner | Role | Setup |
| --- | --- | --- |
| `omp` | Text search through the omp CLI | Default runner; requires `omp` on `PATH` |
| `claudecode` | `code.search` through Claude Code | Optional; authenticated `claude` CLI and a spending grant |
| `codex` | `code.search` through Codex | Optional; authenticated `codex` CLI and a spending grant |
| `kivgraph` | Structural search, source, references, dependencies, impact, context and graph maintenance | Configured MCP transport and registered, indexed repositories |
| `tokensave` | Context, symbol calls and overview | Configured stdio process and repository scope |
| `desktop` | macOS application inspection, screenshots and interaction | Local Swift helper and macOS permissions |
| `local` | Filesystem `code.search` | No external client; intended for local development and smoke tests |

OpenCode is a supported client; it is not
a native capability adapter. Generic MCP servers can also be declared for
supervision and raw tool exposure through `[[mcp_server]]` settings.

Use `atenea catalog` for declared contracts, implementations and repositories.
MCP clients should read `catalog.repositories` and `tools/list` for their actual
surface, which also depends on attached runners and client policy.

Kivgraph provides **`symbol.search`** and **`symbol.implementations`**. The latter
requires a locally maintained Kivgraph build exposing `find_implementations`;
these provider changes are not included in an upstream release. Install its
complete matching bundle and rebuild the graph before using the capability.
**`symbol.unresolved` has no implementation**: it remains declared but is absent
from `tools/list`; direct calls return `not_offered`.
See [maintenance, compatibility and diagnostic statistics](docs/content/maintenance-and-stats.md)
for provider requirements and evidence limits.

Graph queries require verified content freshness. Automatic rebuilding is off
by default; `graph.ensure_fresh` is an explicit maintenance capability with
read/write/process effects. `atenea detect` probes providers and indexes without
building an index. See [graph freshness](docs/content/kivgraph-freshness.md) and
[routing and usage receipts](docs/content/tool-visibility.md).

### Permissions and budgets

`orchestrator.effects` controls the standing grant for CLI work;
`orchestrator.client_effects` controls connected clients. The defaults grant
`process` in addition to read access. Interactive desktop capabilities are also
blocked for MCP clients by the default `client_denied_capabilities` list.

`budget_usd` is a grant for one `ask` or MCP call. `--budget` sets a grant for
one run, `--allow EFFECT` adds an effect to one CLI request, and `--confirm`
requests human review in a TTY before `ask` dispatches. Provider-reported
spending is checked after execution; this is not a guarantee that every
external provider can stop at the exact monetary limit.

For desktop setup and interaction rules, see
[Computer Use](docs/content/computer-use.md) and the [helper guide](helper/README.md).

## Run the service and connect clients

After initializing settings, run the service in a terminal:

```sh
atenea run
```

In another terminal, check the bridge:

```sh
atenea mcp --check
```

The bridge speaks MCP over stdin/stdout and forwards requests to the running
core through a private Unix socket. It does not start the service itself.

### Background service

Install the binary at a stable path first. For a source build:

```sh
mkdir -p "$HOME/.local/bin"
go build -o "$HOME/.local/bin/atenea" ./cmd/atenea
"$HOME/.local/bin/atenea" service install
```

Start it using the command for your system:

```sh
# Linux: systemd user service
systemctl --user start atenea.service

# macOS: per-user launchd agent
launchctl kickstart -k "gui/$(id -u)/com.tutitoos.atenea"
```

Use `atenea service status` and `atenea mcp --check` to inspect it. The service
runs as your user. Its MCP socket is mode `0600` inside a `0700` directory.

For an installation using macOS desktop control, use
`bash scripts/install-dev.sh` from the checkout. It builds Atenea and
the Swift helper, signs them when an identity is available,
installs them and restarts the service. See the
[helper guide](helper/README.md) for signing and permission requirements.

### MCP clients

For clients using the `mcpServers` JSON format, configure an absolute path to
the installed binary, replacing `/absolute/path/to/atenea`:

```json
{
  "mcpServers": {
    "atenea": {
      "command": "/absolute/path/to/atenea",
      "args": ["mcp"]
    }
  }
}
```

For CLI clients, `atenea wrap claude`, `atenea wrap codex`,
`atenea wrap opencode` and `atenea wrap omp` launch the client with checked MCP
configuration. Persistent desktop setup, profiles, Codex configuration and
Claude Desktop packaging are covered in the
[desktop client guide](docs/content/desktop-clients.md).

Reconnect clients after changing the offered tool surface. Atenea provides
per-call routing receipts and asks the client to display tool activity; whether
the notice is rendered depends on the client/model. See
[tool visibility](docs/content/tool-visibility.md) and
[chat commands](docs/content/commands.md).

## Observe and troubleshoot

```sh
atenea status
atenea detect --repo current
atenea stats --today
atenea stats --week --provider kivgraph
atenea stats --today --used --watch
atenea metrics
atenea incidents
atenea backup list
```

`status` reports service state or a labeled disk fallback. `detect` performs
provider probes; a successful handshake does not prove every tool works.
`stats` separates requests, provider attempts, refusals and failures.
Calendar periods, filters and historical limits are in the
[stats guide](docs/content/stats.md).

`atenea dashboard ID` checks and opens the web UI that a declared provider or MCP
server publishes in its own `dashboard` setting; see the
[settings reference](docs/content/settings.md).

Measurements are stored in DuckDB; run receipts and incidents
provide separate execution evidence. State normally lives under
`$XDG_STATE_HOME/atenea` or `~/.local/state/atenea`. See
[operations](docs/content/operations.md) for recovery and backups, and
[provider diagnosis](docs/content/diagnosing-providers.md) for dispatch failures.

## Migrating from contract 3.x

Contract `4.0.0` retires Serena and rejects 3.x configuration files. Remove its
runner, adapter/process tables, MCP declaration, implementation blocks,
selector references and repository `indexed_by` entries before setting
`contract = "4.0.0"`. Changing only the header is insufficient.

Validate the result with `atenea config show`. Follow the
[migration guide](docs/content/migration-4.md) for the complete checklist and
the distinction between retired configuration and historical records.

## Development

The main Go module builds the core and CLI. Install
[Lefthook](https://github.com/evilmartians/lefthook) and the repository's linter
version before enabling the Git hooks:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
lefthook install
go build ./...
go vet ./...
go test -race ./...
```

Pre-commit checks Go formatting, vet, lint and host-footer compatibility;
pre-push runs the race suite. [CI](.github/workflows/ci.yml) also checks native
platform builds, dependencies, provider contracts and helper
code. The [release workflow](.github/workflows/release.yml) validates a tagged
tree before publishing its artifacts. Passing local checks does not publish a
release.

Optional Go hot reload uses [Air](https://github.com/air-verse/air)
and the checked-in [.air.toml](.air.toml), which starts `atenea run`.

### Repository layout

| Path | Contents |
| --- | --- |
| [`cmd/`](cmd/) | CLI and benchmark entry points |
| [`internal/`](internal/) | Core, adapters, runners, storage and transports |
| [`pkg/contract/`](pkg/contract/) | Versioned capability and adapter contracts |
| [`helper/`](helper/) | Swift desktop helper |
| [`docs/`](docs/) | Hugo documentation sources and configuration |
| [`benchmarks/`](benchmarks/) | Recorded runs and reproducible benchmark evidence |
| [`scripts/`](scripts/) | Build, installation, validation and smoke-test scripts |
| [`packaging/`](packaging/) | Client extension packaging |
| [`tools/`](tools/) | Standalone MCP auditing and diagnostic tools |

## Credits

Atenea builds on [ripgrep](https://github.com/BurntSushi/ripgrep),
[Kivgraph](https://github.com/Luqueee/kivgraph),
[DuckDB](https://github.com/duckdb/duckdb), along with the CLIs and MCP providers
configured by its users. Documentation uses [Hugo](https://github.com/gohugoio/hugo)
and [hugo-book](https://github.com/alex-shpak/hugo-book).

Imported Go dependencies are listed in [go.mod](go.mod). External CLIs, MCP
servers and helper runtimes are installed separately.
