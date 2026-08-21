---
title: v1 readiness
weight: 7
---

# v1 readiness

This page is the acceptance record for the v1.0 candidate. It separates what is
implemented and tested in this repository from capabilities that would require
a new contract or an external provider decision. The normative policy is in
[`v1.0 policy`](v1-policy.md).

## Code-backed and shipped

| Area | Evidence | Status |
| --- | --- | --- |
| Core, registry, selector and orchestrator | `pkg/contract/`, `internal/registry/`, `internal/selector/`, `internal/orchestrator/` | Implemented and covered |
| Persistent history | `internal/agent/agent.go`, `internal/trace/`, `internal/metrics/` | Lazy history loading is wired; tests cover prior-run discoveries |
| Permission effects | `pkg/contract/workflow.go`, `internal/core/`, `cmd/atenea/main.go` | Explicit grants and `--allow` are enforced |
| Measurements and wandering | `internal/metrics/summary.go`, `internal/metrics/compact.go`, migrations `0006` and `0007` | `out_of_scope` is persisted, rolled up and reported |
| Completeness reporting | `internal/agent/model/model.go`, `internal/agent/planner/` | Missing or partial coverage is represented and tested |
| Ranked structural search | `symbol.search`, `internal/adapter/serena/serena.go`, `internal/adapter/serena/symbols.go` | Serena declarations are filtered, ranked deterministically and returned with qualified name, kind and source range |
| MCP lifecycle and passthrough | `internal/mcpstdio/`, `internal/passthrough/`, `internal/supervisor/` | Shared lifecycle, raw allow-list and effects are validated |
| Supported adapters | `internal/adapter/omp/`, `claudecode/`, `codex/`, `serena/`, `kivgraph/` | Native adapters compile and are tested |
| OpenCode model backend | `internal/agent/opencode/`, `internal/agent/model/`, `scripts/opencode-smoke.sh` | Opt-in adapter, local structured-schema enforcement, protocol fixtures and safe real-provider smoke command |
| Installation and operations | `scripts/install.sh`, `scripts/release-smoke.sh`, `docs/content/operations.md` | Install, update, rollback, uninstall and release smoke are verified |
| Release gate | `.github/workflows/ci.yml`, `release.yml`, `postrelease.yml`, `v1-readiness.yml` | Linux/macOS amd64/arm64 validation is automated |
| v1.0 policy gate | `scripts/v1-policy-check.sh`, `docs/content/v1-policy.md` | The declared guarantees and deferred contracts have stable anchors |

## Acceptance command

Run from a clean checkout:

```sh
bash scripts/v1-readiness.sh
```

The gate checks repository hygiene, removal of active `codebase-memory`
references, formatting, module tidiness, vet, native build, the complete race
suite and shell entry points. It does not publish a release.

## Latest real-provider smoke

On 2026-08-21, `scripts/opencode-smoke.sh` passed against the installed
OpenCode `1.18.15` CLI and the `opencode/hy3-free` model. The run completed a
real `--format json --pure` turn in 80.03 seconds, produced text plus a
structured `{ "ok": true }` answer, and passed Atenea's local schema check.
It supplied no Atenea MCP tools and did not use `--auto`.

This is evidence for one real model and one local CLI version, not a guarantee
for every OpenCode provider or future release. The smoke remains opt-in and
may create the provider client's normal local session state.

## Deliberately deferred

These are not hidden failures in the current product. They are explicit v1.0
decisions or later contracts:

- interactive permission confirmation: the current security model is explicit
  grant/refusal through policy and `--allow`, with no implicit prompt;
- exact OpenCode parity with Claude Code: the opt-in provider adapter is
  hardened and has an opt-in real smoke test, but OpenCode still lacks a
  native schema flag, a common cost cap and a permanently reliable terminal
  event guarantee;
- exact hard per-turn token enforcement: `budget_usd` is an authorization
  forecast, and `limits.max_tokens` narrows the observed `ReadTokens` boundary,
  but supported external providers do not expose one uniform hard cap;
- citation enforcement as a hard gate: citation evidence exists, but a safe
  threshold for abbreviated or renamed paths is not established.

The historical design notes remain in
[`What is not built yet`](not-built-yet.md). This page is the current acceptance
record and should be updated when one of the deferred contracts is actually
chosen and implemented.
