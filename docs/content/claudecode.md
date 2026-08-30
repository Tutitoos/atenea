---
title: Claude Code provider
weight: 9
---

# Claude Code provider

Atenea can drive Claude Code's non-interactive CLI as the `claude-code`
provider for `code.search`. The terminal executable is the runner surface;
Claude.app remains a client surface and should connect to Atenea through MCP,
not be launched as a GUI subprocess by an adapter.

```toml
[orchestrator]
runners = ["omp", "claudecode"]

  [orchestrator.claudecode]
  source = "auto"
  terminal_binary = "/opt/homebrew/bin/claude"
  implementations = ["claude.search"]
  timeout = "90s"
```

`auto` resolves the Claude Code executable on PATH. Its user authentication
state is kept outside Atenea, so the terminal CLI and Claude Code client
surfaces can use the same account without placing credentials in TOML. Do not
configure the GUI binary from `/Applications/Claude.app` as a runner: it is
not the headless `claude -p` contract.

Use `atenea wrap claude ...` when Claude Code should consume Atenea's checked
MCP configuration. The wrapper is ephemeral and does not rewrite the client's
configuration files.

Use `atenea wrap --via-headroom claude ...` to compose that MCP overlay with
Headroom's HTTP proxy. Headroom handles model traffic and Atenea remains the
single MCP bridge; the command fails closed if Headroom is unavailable.

## Current audit status

On 2026-08-22 the authenticated CLI was rechecked against `taxiprime-backend`
with the configured `0.25 USD` budget and `90s` timeout. Claude Code stopped at
its spending ceiling and returned no accepted search result; the provider
reported `0.310367 USD` observed usage. It remains an optional provider, and
the audit did not increase either the budget or the timeout. Atenea now keeps
that observed charge on the receipt and rejects any successful-looking answer
whose reported cost exceeds the permission, as `permission_denied`.

This is a safety classification, not a billing guarantee. Claude Code's
`--max-budget-usd` is a provider-side stopping threshold and its final ledger
can exceed the requested value. A run that must have more room should receive
an explicitly larger budget, for example through the caller's existing
`--budget` option; removing the limit globally is not required and is not the
default.
