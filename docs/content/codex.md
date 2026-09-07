---
title: Codex CLI provider
weight: 8
---

# Codex CLI provider

## Native App Server and managed agent profiles

The native App Server adapter is separate from `codex exec`. It keeps a
durable thread (`ephemeral = false`),
starts turns on that same thread, and records requested model/effort/profile
separately from observed model, effort and runtime user agent. A reroute event
or a mismatched start response is a hard failure; Atenea never silently accepts
a substitute model. Interactive Codex work uses this native transport by default and fails
closed when it is disabled or unavailable. `codex exec` remains available
only to a request explicitly marked invisible or CI.

The first native surface is deliberately small: `initialize`, `model/list`,
`modelProvider/capabilities/read`, `thread/start`, `thread/fork`, `turn/start`,
and `thread/list`, plus typed thread, usage and reroute events. The adapter was
tested locally against App Server 0.153.4 wire shapes. The server's initialize response
does not carry a protocol version, so Atenea leaves the observed protocol
unknown rather than inventing one. App Server exposes `thread/fork` to clients,
which Atenea uses to create a durable child thread while preserving its own
specialist roles, concurrency limits, permissions, and parent-child mapping.
This is distinct from Codex's model-internal collaboration tools; Atenea does
not claim direct client access to those tools.

For a visible specialist, the workflow acquires its limits, claims the step and
budget, and publishes the activity notice and progress before any provider
call. It then reserves the fork in its durable route, issues one `thread/fork`,
stores the distinct child id, and only then dispatches the step. The child
process resumes that id and installs its own hook, model, sandbox and role
instructions before its first turn.

If the connection is lost after the external request, the reservation remains
`pending` and automatic resume refuses to fork again. After checking the
provider's thread list, an operator can inspect and bind the verified child,
then resume the workflow:

```sh
atenea workflow native-fork inspect --traces /path/to/traces.db WORKFLOW STEP
atenea workflow native-fork bind --traces /path/to/traces.db \
  --child-thread VERIFIED_CHILD WORKFLOW STEP
atenea workflow resume --traces /path/to/traces.db WORKFLOW
```

Binding refuses the coordinator id and any workflow with an active writer.

Canonical profiles are synchronized without launching Codex:

```sh
atenea codex agents sync --global
atenea codex agents sync --project /path/to/repository
atenea codex agents check --global
```

Global files are written under `$CODEX_HOME/agents/` (or `~/.codex/agents/`),
and project files under `<repository>/.codex/agents/`. Each `atenea-*.toml`
file carries a digest marker and top-level `name`, `description`,
`developer_instructions`, `model`, `model_reasoning_effort`, and `sandbox_mode`
keys. Research, review, and audit use `read-only`; implementation uses
`workspace-write`. Specialist instructions explicitly prohibit delegation.
Files are written through a same-directory atomic rename with restrictive
permissions. Foreign files are preserved. Obsolete Atenea-managed files are
removed only with `--prune`. Tests use temporary homes and project directories;
the real Codex home is never synchronized by the test suite.

These agent files are the managed Codex agent surface; they are not App Server
permission-profile identifiers. Native root turns therefore send the validated
`read-only` or `workspace-write` sandbox mode and do not invent a
`atenea-<role>` permission profile.

Atenea can use the native Codex CLI as the `codex` provider for
`code.search`. The adapter is independent from the Claude Code adapter: it
invokes `codex exec`, consumes Codex JSONL events, and validates the final
structured response against Atenea's capability contract.

## Requirements and login

The Codex executable must be installed and the local Codex account must be
authenticated. The supported macOS installations are the terminal CLI and the
CLI bundled in ChatGPT.app:

```text
/Applications/ChatGPT.app/Contents/Resources/codex
```

Check it with:

```sh
/Applications/ChatGPT.app/Contents/Resources/codex --version
codex --version
```

Login is managed by Codex CLI, not Atenea. Run the normal Codex login flow
once in the same user account that runs Atenea. An unauthenticated Codex
process is reported as an unavailable provider, so `ripgrep` can answer when
it is attached.

## Observable Codex certification

The certification flow proves the contract that Codex exposes through App
Server, real CLI JSONL, and the visible Desktop accessibility tree. It does not
claim provider-internal per-token model telemetry. Start it from a clean,
revision-stamped ATENEA binary on macOS. If the local Go toolchain does not
embed VCS settings, build the certification binary with the full clean commit:

```sh
cert_dir=$(mktemp -d "${TMPDIR:-/tmp}/atenea-certify.XXXXXX")
chmod 700 "$cert_dir"
go build -trimpath -buildvcs=false \
  -ldflags "-buildid= -X github.com/Tutitoos/atenea/internal/buildinfo.certificationRevision=$(git rev-parse HEAD)" \
  -o "$cert_dir/atenea-certify" ./cmd/atenea
```

Then run:

```sh
"$cert_dir/atenea-certify" codex certify start --desktop --ttl 30d
"$cert_dir/atenea-certify" codex certify status CERTIFICATE_ID --markdown
"$cert_dir/atenea-certify" codex certify complete CERTIFICATE_ID
"$cert_dir/atenea-certify" codex certify check --require-valid
```

`start` creates a fresh 0700 sandbox and `CODEX_HOME`, runs device login there,
checks `account/read`, and executes the planning, implementation, review, and
audit model profiles. It then runs two ephemeral, read-only Codex CLI sessions:
one correlated MCP call and one cursor reconnect. The authenticated sandbox is
removed and checked before the Desktop challenge is printed. ATENEA never
copies the normal `auth.json`.

For CLI certification, the canonical Markdown rendering is the payload of the
typed, completed MCP JSONL event. The gate also requires a later
`turn.completed`; a failed or incomplete turn cannot pass.

Paste the printed challenge into a new Codex Desktop chat. The Desktop agent
must run the one-use local challenge command and render its proof, checklist,
and 20-segment progress bar. `complete` reads only the accessibility tree of
`com.openai.codex`; it neither clicks nor types and does not retain the tree,
chat text, or screenshots. Use `cancel` to invalidate an unfinished challenge.

A passed certificate remains valid only for the exact ATENEA commit and
binary, Codex CLI and Desktop binaries, machine, model profiles, App Server
schema, MCP overlay, and presentation contract. Export the sanitized signed
evidence only after every gate passes:

```sh
"$cert_dir/atenea-certify" codex certify export CERTIFICATE_ID \
  --output certifications/codex.json \
  --public-key-output certifications/codex-certifier.pub
```

The release workflow accepts an evidence-only follow-up commit and rejects a
tag when code changed after certification, a gate is missing, the signature is
untrusted, or the certificate is stale or expired. The phrase `Codex
certificado 100 %` appears only for a currently valid certificate.

## Configuration

The adapter block uses the existing runner names and defaults to a 90-second
timeout:

```toml
[orchestrator]
runners = ["omp", "codex"]

  [orchestrator.codex]
  source = "auto"
  terminal_binary = "codex"
  app_binary = "/Applications/ChatGPT.app/Contents/Resources/codex"
  implementations = ["codex.search"]
  timeout = "120s"
```

Codex is not added to the default runner list, so enabling it is explicit.
`auto` tries the terminal CLI first and falls back to the app-bundled CLI;
both surfaces remain one `codex.search` provider and therefore do not create a
second selector choice. Set `source = "terminal"` or `source = "app"` to make
the choice strict. The legacy `binary` key remains an explicit override.
The capability remains declared in the catalog, while `ripgrep` stays the
cheap and preferred provider. The Codex adapter always uses a temporary output
schema, `--json`, `--ephemeral`, `--sandbox read-only`, `--ignore-user-config`,
and `--ignore-rules`; it does not reuse Claude Code flags.

Codex CLI currently does not report a monetary cost in its JSONL completion
event. Atenea therefore gates the dispatch on the commission's budget and
records the absence of a Codex price as a notice; it cannot enforce a native
per-call dollar ceiling that Codex does not expose. The 120-second timeout is
enforced by Atenea and kills the process tree.

## Choosing a provider

For a single command, use `--prefer`:

```sh
atenea select code.search --repo taxiprime-app --prefer ripgrep
atenea select code.search --repo taxiprime-app --prefer codex.search
atenea select code.search --repo taxiprime-app --prefer claude.search

atenea ask code.search --repo taxiprime-app \
  --set query=Firebase --prefer codex.search --trace
```

The one-call preference does not edit settings. If the preferred provider is
not attached, unhealthy, unauthenticated, or otherwise fails the funnel,
Atenea reports the reason and falls back to the surviving provider. A standing
preference can be written instead:

```toml
[[selector.rule]]
capability = "code.search"
repository = "taxiprime-app"
prefer = "codex.search"
```

Omit the rule to keep automatic ranking. With `ripgrep` attached, its lower
declared cost keeps it first by default. To disable Codex, remove `"codex"`
from `orchestrator.runners` (or leave it out); no capability or repository
data is changed.

The adapter never writes the target repository, never reports absolute paths,
filters configured sensitive paths, rejects scopes outside the repository, and
drops returned matches that are outside the requested scope. It intentionally
omits file content from returned `snippet` fields, so Codex cannot expose a
secret or sensitive source fragment through the search result.

## Current audit status

On 2026-08-22 the authenticated `codex-cli 0.149.0` binary was rechecked
against `taxiprime-backend` with the configured `0.25 USD` budget. An isolated
180-second diagnostic completed in `81.8s` with a valid result and no reported
monetary usage. The default timeout is now `120s` to leave measured startup and
provider variance margin; Codex remains optional and the budget was not
increased.
