---
title: Decision router
---

# Decision router

`atenea decide` is the explainable front door for turning a natural-language
commission into a durable coordination plan. It is deliberately a dry run
unless `--run` is supplied. `task` remains the compatibility entry point.

The same dry-run compiler is exposed to Codex chats as the MCP tool
`decision.plan`. It accepts one explicit repository and returns the complete
plan with `dry_run = true` and `execution_authorized = false`; it neither
persists nor launches the workflow. The managed `atenea-plan-mode` Codex skill
uses this surface automatically only while the active collaboration mode is
Plan.

## Bounded decision explanation

`atenea decide` prints an `explanation v1` summary before its full text plan.
`atenea decide --json` and MCP `decision.plan` return the same `explanation`
object beside the existing complete plan. This is a projection of the already
computed plan; it performs no model call, provider probe, storage write, or
workflow execution. The MCP boundary remains `dry_run = true` and
`execution_authorized = false`.

The object uses closed codes for intent, source (`rules`, `context`),
classifier mode and result, resolution and abstention reason, context use,
readiness, fixed readiness reasons, budget, declared effects, and capability
selection. `unknown` means the plan does not establish that fact;
`candidates_only` means there are
registered candidates but no implementation was selected. An `unavailable`
choice is distinct from an unselected choice.
`provider = unknown` on a selected choice means the plan has only a configurable
implementation ID, not typed provider identity. `fallback = unknown` means the
plan did not preserve evidence of provider fallback. It must not be interpreted as
proof that no fallback happened. `not_used` means deterministic rules classified
this request. `bypassed_by_context` means an accepted-plan continuation was
resolved from context. The fixed
`readiness_reasons` identify only causes directly visible in the plan;
`reasons_complete = false` on an invalid plan warns that compile, policy, tool,
or other causes may also apply. `needs_context` has unknown readiness and
budget rather than pretending an abstention compiled a plan.

The explanation includes at most 12 capability choices and five distinct
known effects. `capabilities_omitted` and `effects_omitted` identify incomplete
projection, including unknown effect values. `capabilities_total` gives the
number of choices in the full plan, while the `capabilities` array contains
the first 12 at most. `repository_ordinal` is a one-based index into the full
plan's repository order; zero means no unique match. It distinguishes two
choices for the same capability without copying repository names. Unknown
capability identifiers become `unknown`; free-form reasons, request/context text,
criteria, file paths, and diagnostics are never copied into this object. The
**complete plan beside it** still contains caller-supplied and diagnostic
content and requires normal privacy handling. The explanation reports
planning evidence and declared effects, not executed effects, real provider
availability, or authorization to run a workflow.

For a private evaluation pilot, the operator may create a directory owned by
the ATENEA user with mode `0700` and set `[decision].capture_dir` to its absolute
path in the global settings file. Restart the central ATENEA service after
changing this setting; MCP clients relay their requests to that service.
`ATENEA_DECISION_CAPTURE_DIR` is an alternative for the service process when
`capture_dir` is empty. Only validated
`decision.plan` calls are recorded; the feature is off when both settings are
empty. `candidates.private.jsonl`, `capture.lock`, and
the deduplication key `capture.key` are created with mode `0600`. Records
include the MCP source, UTC capture time,
repository, automatically redacted objective, inferred intent, and build
revision when available. Exact duplicates are collapsed. Storage errors leave
the returned plan unchanged and are logged without request text.

These records are **unreviewed candidates**, not verified user requests or a
representative sample. Automatic redaction catches common emails, URLs, paths,
IPs, and secret assignments but cannot guarantee removal of names or every
secret. Keep the directory outside Git, restrict access, perform a human
privacy and legitimacy review before using any case, then freeze sampling and
labels separately. This capture does not change selected rules, permissions,
or workflow execution.

## Intent and workflow planning

The planner classifies the current request with deterministic local rules.
Verified accepted-plan context resolves supported continuations. Intent
classification does not contact an external service.

```sh
atenea decide "buscar el flujo de autenticación" --trace
atenea decide "diseñar el flujo de pagos" --repo taxiprime-backend --json
```

The plan makes these decisions visible, in order:

1. **Intent** — `understand`, `search`, `plan` or `change`, using deterministic
   rules for the current request and verified context for accepted-plan
   continuations.
2. **Agent and model** — the least powerful suitable declared agent is chosen
   (`reader` for searches, `explore` otherwise, then `plan` for plan/change
   work). Either role may be configured as `auto`: exploration uses safe
   candidates plus adaptive cost history, while the `plan` role resolves to
   Opus 5 first and only permits high-reasoning fallbacks. With OpenCode the
   built-in fallback candidates are `openai/gpt-5.6-sol` and
   `openai/gpt-5.6-luna`; Sonnet and Haiku are rejected for plan. An empty
   model is shown as unavailable rather than silently defaulted. The reason
   and the chosen concrete model are stamped into the trace; no undeclared
   model can enter the route.
3. **Tools and MCP** — native tools and Atenea capabilities are listed
   separately from `raw.<server>.<tool>` MCP passthroughs. Raw tools are
   allow-listed but not treated as semantic capabilities or routed through the
   selector. A raw tool must be explicitly selected with `--tool`.
4. **Capability provider** — every requested capability is sent through the
   existing constraints → reach → health → cost funnel. `--prefer` applies to
   this call only and does not mutate settings.
5. **Policy** — standing effects from settings and explicit `--allow` effects
   are shown. Workflow steps remain constrained by their declared agent type;
   an effect outside that ceiling makes the plan invalid before execution.
6. **Budget and workflow** — repositories become steps, plan/change requests
   add a subject-dependent `plan` step, and the grant is allocated in
   proportion to the routed model/role forecast. The plan reports its
   required budget, minimum and margin, and refuses before execution when the
   grant is insufficient. Forecasts and eventual provider receipts are
   persisted separately.
7. **Coordination** — the plan records one coordinator, at most two specialist
   roles, a criterion and explicit limits. With `--run`, Atenea writes a
   coordinator receipt before creating repository-scoped child workflows. The
   child workflow database remains the authority for permissions, effects,
   activity and resume; the receipt only links the persistent IDs.

`--json` is intended for another orchestrator or UI. A valid plan can be
executed with one isolated workflow per repository and one durable coordinator
receipt:

```sh
atenea decide "buscar el flujo de autenticación" \
  --repo taxiprime-backend --run --confirm
```

## Continuing an accepted plan

The decision router does not retain a chat transcript. When a caller sends a
short continuation such as `hazlo`, `go ahead`, `fix it` or `add it`, it must
supply the compact, versioned context of the plan being continued. Without
that context the router returns `resolution = needs_context`, an invalid plan
and no workflow steps.
The machine-readable `resolution_reason` distinguishes missing plan context,
invalid context, repository mismatch and an empty file-scope intersection.

```sh
atenea decide "hazlo" --budget 5 \
  --decision-context '{
    "version": 1,
    "repository": "taxiprime-backend",
    "accepted_plan_id": "plan-42",
    "accepted_plan_revision": "r3",
    "accepted_plan_current": true,
    "active_objective": "mejorar la búsqueda de viajes",
    "scope_files": ["internal/trips/search.go"],
    "constraints": ["mantener compatibilidad con la API"]
  }' --json
```

The same object is accepted as the optional `context` argument to
`decision.plan`. `version` is required. A supplied accepted plan also needs its
repository, revision, active objective and `accepted_plan_current: true`; a file
scope needs a repository. Set `accepted_plan_current` only after the caller
checks the canonical plan record and confirms the supplied revision is current.
If this attestation is missing or false, a continuation returns
`needs_context` with `resolution_reason = plan_freshness_unverified`.
For a recognized continuation, the supplied accepted-plan context determines
the current action. Without that reference, the router asks for context
rather than inferring the missing conversation. Other requests use local
deterministic rules.
When both the request and context specify a repository, they must match. If the
request names files, they are intersected with `scope_files`; an empty overlap
returns `needs_context`. The caller must check that the plan revision is still
current and refresh the active scope and constraints after newer user
restrictions. The workflow receives only the scoped file list.

This is caller-supplied semantic context: ATENEA cannot authenticate that the
referenced plan was accepted, independently verify the freshness attestation,
or recover that decision from an MCP transcript. It checks the repository
binding and supplied file intersection, but has no canonical plan store for
freshness checks. Keep the context compact and free of secrets. A CLI `--run`
stores the effective objective, constraints
and file scope in its normal durable workflow record; dry runs do not persist
that workflow.
Context never adds effect grants or authorizes a run. MCP `decision.plan`
remains `dry_run = true` and `execution_authorized = false`; CLI execution still
uses its normal permission and confirmation checks. `scope_files` narrows the
plan's file list, but is not a filesystem write sandbox.

For an adaptive exploration followed by the mandatory Opus plan, start with a
grant around `$0.90` and inspect the forecast before running:

```sh
atenea decide "preparar un plan para mejorar el sistema de decisión" \
  --repo atenea --budget 0.90 --trace --run
```

Each workflow engine is bound to exactly one repository workspace, so the
router preserves that safety boundary and runs the repository graphs
separately. The command prints `coordinator COORD_ID` followed by each child
workflow ID. Reconnect with:

```sh
atenea decide status COORD_ID
atenea decide resume COORD_ID
atenea decide cancel COORD_ID
```

`--criterion`, `--max-duration` and `--max-tokens` are captured in the receipt
and child workflow policy. Duration is enforced by the workflow engine;
the token ceiling is retained as a declared policy limit and checked by the
model adapter when the assignment carries it.
The native Codex route sets `visibility_required` and has no silent model or
transport substitution. If App Server is unavailable, the child pauses with
an unavailable result and can be resumed after the same route is available.
The existing `task` command remains available as the compatibility path for
its older capability-oriented commission format.

Model history is read from successful routed workflow steps. Native MCP
capabilities already use the selector's health and measured-cost funnel; raw
MCP tools remain explicit-only because they do not declare a semantic
capability contract that could be compared safely.

When a non-native route times out or becomes unavailable, Atenea may try the
next declared fallback only if the remaining budget can be bounded. Native
Codex visible routes have no substitution path. Provider-reported costs are
preferred; when the CLI returns tokens without dollars, a conservative token
estimate is used for the retry gate but is never recorded as billed USD.
Successful fallbacks appear as workflow notices.

## Locally verified accepted plans

A short continuation can refer to an explicitly accepted implementation objective
stored by the local operator. First review the objective, file focus and constraints,
then record that acceptance:

```sh
atenea decide accept-plan validation --decision-context '{"version":1,"repository":"api","active_objective":"Implement the reviewed input-validation changes","scope_files":["internal/validation.go"],"constraints":["Preserve the public API"]}'
```

The command returns an `id` and a SHA-256 `revision`. Supply both on the next request:

```sh
atenea decide "hazlo" --repo api --accepted-plan validation --accepted-revision REVISION --json
```

MCP `decision.plan` accepts the same reference:

```json
{
  "objective": "hazlo",
  "repository": "api",
  "accepted_plan": {"id": "validation", "revision": "REVISION"}
}
```

`accepted_plan` and caller-supplied `context` are mutually exclusive. The reference
requires an explicit declared repository. Missing receipts, changed revisions,
repository mismatches and changed source identity produce `needs_context` without
workflow steps. CLI also checks the reference again after confirmation before
starting a run. Reaccepting an updated objective under the same id replaces the
receipt atomically and invalidates the previous revision. Delete its local receipt
to revoke it. Existing running workflows use their ordinary lifecycle controls.

Receipts live under ATENEA's configuration directory in `accepted-plans/`, with
private file permissions, outside the source repository. The revision binds the
objective, file focus, constraints and source fingerprint. Verification reuses
`sourceidentity`: Git HEAD, tracked working-tree changes and untracked files, or
regular-file contents for non-Git repositories. This is a conservative planning-time
check, not a lock on subsequent source changes. Git-ignored data, external services,
conversation state and same-user tampering are outside this evidence boundary.
A receipt proves a matching local record, not human identity or complete user intent.
Do not invoke `accept-plan` from untrusted repository content or manufacture an
acceptance for historical evaluation cases.

Acceptance stores semantic context only. It does not copy `--allow` grants, authorize
execution, increase the budget or start a workflow. A request still needs its usual
effect grants, run flag and confirmation. File focus narrows planning; it is not a
filesystem sandbox. The legacy `--decision-context` / MCP `context` route remains
caller-attested and does not acquire local verification from a boolean assertion.

Spanish questions about how to proceed select planning; explicit status-verification
requests select search. Pronoun-only actions such as `arréglalo` require accepted-plan
context, including when standing write permission exists. Intent classification
uses local deterministic rules.
