---
title: v1 contracts
weight: 8
---

# v1 contracts

Fases 10 y 11 convierten las preguntas de diseño restantes en contratos
explícitos. Una capacidad está completa cuando su comportamiento tiene código y
tests; una función que necesita otro provider o una decisión interactiva queda
registrada como límite deliberado, no implícita en la configuración. La matriz
operativa completa está en [v1.0 policy]({{< relref "v1-policy" >}}).

## Structural search

`symbol.search` retains its language-aware declaration search contract, including
qualified names, kinds, source ranges and ranks. It currently has no provider,
as do `symbol.implementations` and `symbol.unresolved`. These three capabilities
are not advertised by `tools/list`; direct calls return `not_offered`.
`code.search` remains literal or regex text search.

## Security and permissions

Effects are granted before dispatch, and an effect outside the grant is refused.
The CLI's `--allow` is the explicit escalation mechanism.

There is no *implicit* interactive permission prompt, and that is the deliberate
limit: no adapter and no daemon path stops to ask. A process running unattended
under systemd or launchd has no terminal to be asked on, so a prompt introduced
there would either hang the run or be answered by nobody. Introducing one would
require a separate UI/session contract.

What does exist is an interactive permission gate the caller asks for by name.
`--confirm` on `task`, `ask`, `decide --run` and `agent` prints the execution
summary -- the budget and the effects about to be granted -- and requires a
confirmation on a TTY before anything is dispatched; without a TTY it refuses
rather than proceeding. It is a floor for one command, opted into per
invocation. `agent` goes further and *requires* it for any type declaring write
or external effects, so the shortest form of that command cannot cause them.
`backup discard` requires the same word for the same reason.

The distinction matters because the two are easy to conflate: Atenea will never
interrupt a run to ask, and Atenea will always ask when the caller said to.

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
[`What is not built yet`]({{< relref "not-built-yet" >}}); this page and
[`v1 readiness`]({{< relref "v1-readiness" >}}) describe the current tree.
