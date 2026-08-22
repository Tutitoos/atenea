---
title: Codex CLI provider
weight: 8
---

# Codex CLI provider

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
