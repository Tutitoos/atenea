---
title: v1 contracts
weight: 8
---

# v1 contracts

Fases 10 y 11 convierten las preguntas de diseño restantes en contratos
explícitos. Una capacidad está completa cuando su comportamiento tiene código y
tests; una función que necesita otro provider o una decisión interactiva queda
registrada como límite deliberado, no implícita en la configuración. La matriz
operativa completa está en [v1.0 policy](v1-policy.md).

## Structural search

`symbol.search` is the language-aware counterpart to `code.search`:

- `code.search` remains literal or regex text search and keeps its existing
  output contract;
- `symbol.search` asks Serena's indexed `find_symbol` surface for declarations;
- results include qualified `name`, provider `kind`, relative `path`,
  `line`, `end_line` and deterministic `rank`;
- `scope`, `kind` and `limit` are applied by Atenea after the provider answer;
- sensitive paths are removed before the result crosses the adapter boundary;
- `serena.search` is not an alias for text search; the shipped implementation
  is `serena.symbol_search`.

This keeps structural ranking from changing the meaning of the established
text-search capability.

## Security and permissions

The v1 behavior is intentionally non-interactive at the adapter and daemon
layers. Effects are granted before dispatch, and an effect outside the grant is
refused. The CLI's `--allow` is the explicit escalation mechanism. Adding a
prompt would require a separate UI/session contract and is therefore not
silently introduced into a process that may be running unattended.

## Provider boundaries

OpenCode has an opt-in local-model backend through `[model].backend =
"opencode"`. Its event parser is isolated from Claude Code and requires a
completed `step_finish` event plus text before accepting an answer. It supports
model selection, repository confinement, cancellation, observed usage and MCP
configuration translation. Once a JSON event reports a token or cost overrun,
the runner kills the contained process before reading later events; this is a
local stop, not a provider-side hard cap. Its local boundary also validates
the structured answer's required fields, primitive types, numeric bounds and closed object
properties, rejects trailing JSON values and records provider tool-use events;
it does not pretend that OpenCode's JSON stream is Claude's final envelope. A
tool-enabled turn is bounded at four intermediate tool steps; when that limit
is reached, Atenea resumes the persisted OpenCode session with a finalization
request so a provider outage or tool loop becomes an auditable partial answer
rather than an unbounded timeout. It does not pass OpenCode `--pure`, because
the authenticated server rejects that mode on the current provider path.

`scripts/opencode-smoke.sh` runs an opt-in real-provider smoke test when
`ATENEA_OPENCODE_SMOKE=1` and `ATENEA_OPENCODE_MODEL` are supplied. It is not
part of ordinary CI because it may consume provider allowance.

The backend remains deliberately narrower than Claude Code: OpenCode has no
native JSON-schema flag or provider-independent budget flag. Atenea asks for
structured JSON in the prompt, records `step_finish` usage/cost when present,
rejects a completed result whose observed cost exceeds a positive requested
budget, and the core repeats that rejection at the common runner boundary for
every adapter. The check happens after the provider reports the turn; it
cannot prevent an already-running event from overspending.

Provider boundary errors are mapped into the shared bins: permission and
budget refusals become `permission_denied`, authentication, rate limits and
quota exhaustion become `unavailable`, and context overflow becomes
`invalid_input`. Timeout and caller cancellation remain distinct.

Agent `limits.max_tokens` is carried, validated and used by the planner to
narrow the model client's observed incremental read boundary when a grant
exists. `budget_usd` is a forecast and authorization check, not a hard
provider-side ceiling. The observed boundary is not equivalent to preventing
the provider from finishing an in-flight event; Atenea deliberately does not
claim an exact provider cap.

Citation evidence is a hard acceptance gate for prose results. Every non-empty
prose field must contain at least one recognized `path:line` or `Line N of path`
citation. The reviewer rejects invented paths, out-of-range lines, incorrect
adjacent excerpts and uncited prose fields. Each result retains `citation_count`,
`existence_only`, `content_checked`, `uncited_fields` and one `citations` row per
location with the written path, the repository-relative `resolved_path`, line,
optional quote and outcome. A unique basename may resolve an abbreviated path;
a rename with a different basename remains unresolved rather than being guessed.
The gate validates the cited evidence, not the semantic meaning of surrounding
narrative. `internal/agent/review_integration_test.go` also exercises this
contract through the real `internal/agent.Runner` and the shipped
`agent-exec reviewer`, so the strict result schema is checked at the process
boundary rather than only through direct package tests.

## Acceptance

The implementation and all boundaries are checked by:

```sh
bash scripts/v1-readiness.sh
```

The historical design ledger remains in
[`What is not built yet`](not-built-yet.md); this page and
[`v1 readiness`](v1-readiness.md) describe the current tree.
