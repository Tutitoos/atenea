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

## Intent classification with Laya

The `[decision]` settings block controls intent classification. The default
`mode = "rules"` keeps the built-in deterministic classifier and makes no
network request. `mode = "observe"` sends the commission to a configured Laya
service and records its proposal while keeping the rules result in the plan.
`mode = "laya"` selects a valid Laya result only when its `answer_confidence`
meets `minimum_confidence`; otherwise the plan uses the deterministic result.
Laya's `confidence` field measures normalized entropy and is not the value to
use for this gate.

ATENEA speaks Laya's `POST /v1/systemone` HTTP protocol. A minimal local setup is
to install Laya with its optional service dependencies and bind it to loopback:

```sh
python -m pip install "laya[serve]"
LAYA_HOST=127.0.0.1 LAYA_PORT=8000 LAYA_PRELOAD=0 laya-serve
```

Then set the effective ATENEA configuration:

```toml
[decision]
mode = "observe" # change to "laya" only after evaluating this checkpoint
laya_endpoint = "http://127.0.0.1:8000/v1/systemone"
laya_model = "multilingual" # optional; empty lets Laya route each request
laya_api_key_env = "ATENEA_LAYA_API_KEY" # omit when the service has no bearer key
timeout = "10s"
minimum_confidence = 0.8
```

If the service requires a bearer token, put its value in the named environment
variable. ATENEA never stores the token in its settings file. Commission text is
sent to the endpoint only in `observe` or `laya` mode. Keep the service bound to
loopback for local use; use HTTPS and an explicitly trusted endpoint for a
remote service. Redirects are refused. `laya_model` accepts `english`,
`multilingual`, or `typed-decisions`. An empty value leaves routing to Laya;
pinning a checkpoint makes a comparison repeatable across languages.

The confidence value is an initial setting, not a validated threshold for a
particular checkpoint or domain. Use the labeled evaluation corpus and its
runner to measure the exact checkpoint and language mix before enabling Laya
for plan selection:

```sh
python scripts/evaluate-laya-intents.py \
  http://127.0.0.1:8000/v1/systemone --model multilingual --threshold 0.8
```

The runner reports baseline and Laya accuracy, coverage at the requested
threshold, gated accuracy, and a confusion matrix for separate calibration and
test splits. By default, the service chooses a checkpoint for each request; use
`--model multilingual` to pin one checkpoint across both language groups. The
report includes the requested model plus model and routing IDs returned by the
service. Verify those IDs against the checkpoint you intended to measure. At
the exact Laya integration revision measured on 2026-09-25, the frozen rules
baseline on this curated corpus was 25/32 (78.1%) on calibration and 11/16
(68.8%) on the held-out test split. Later deterministic-classifier changes
alter several predictions; this report does not measure those changes. The
48-case set is historical evidence for its recorded revision, not a
confirmation set for the updated classifier. Issue #165 tracks a separate
representative evaluation. These figures do not estimate general production
accuracy.

### Measured Laya run

On 2026-09-25, the runner completed all 48 rows against local `laya==0.3.20`
(`torch==2.14.0`, Python 3.13.15, MPS). The request pinned `multilingual`; the
service reported model `laya-rl-agent` and route `multilingual`. The checkpoint
was `convaiinnovations/laya` subfolder `multilingual`, Hub revision
`55cf4c4ebb4ebe31b2550e8bdf3bd21b99753851`. Corpus SHA-256:
`bcb642e70fee6e03dc467e11cca6f36d6c01dbd775b6c831a585d0cfda5cae55`.
The corpus contains 12 examples for each label (`understand`, `search`, `plan`,
`change`), split into 32 calibration rows and 16 held-out rows (four per label).

| Split | Rules | Laya raw | Laya coverage at 0.8 | Laya accuracy when selected | Gated result |
| --- | ---: | ---: | ---: | ---: | ---: |
| Calibration (32) | 25/32 (78.1%) | 22/32 (68.8%) | 26/32 (81.3%) | 21/26 (80.8%) | 24/32 (75.0%) |
| Held-out test (16) | 11/16 (68.8%) | 12/16 (75.0%) | 13/16 (81.3%) | 11/13 (84.6%) | 13/16 (81.3%) |

Raw Laya confusion matrix over all 48 rows:

| Expected \\ predicted | change | plan | search | understand |
| --- | ---: | ---: | ---: | ---: |
| change | 10 | 1 | 0 | 1 |
| plan | 3 | 6 | 0 | 3 |
| search | 0 | 1 | 11 | 0 |
| understand | 2 | 1 | 2 | 7 |

These figures use the exact option order sent by ATENEA's Go adapter. The
initial standalone Python runner sent options in a different order: its earlier
gate results were 26/32 on calibration and 11/16 on test. Laya's confidence
depends on option order, so that earlier gate result cannot represent the
production request. The Python runner now matches Go's wire order. A second
run through the Go dry-run planner produced 48 valid paired plans, 48 service
responses, no service failures and no effects outside the explicit grants.
With the aligned order, the gate loses one calibration case and gains two
held-out cases. It also raises false `change` classifications from one to
three in calibration. This small curated corpus does not justify enabling
`laya` by default or treating `0.8` as validated. Use `observe` to gather
representative traffic evidence. A separate unpinned, offline-only probe
routed one Spanish request to the English checkpoint and got HTTP 500; pinning
`multilingual` avoided that missing-checkpoint failure. Local fake-server
tests validate only the wire contract; these measurements use the real
checkpoint. Per-case predictions and provenance are stored in
`benchmarks/runs/laya-intent-2026-09-25/report.json`. The evaluated checkpoint
is marked Apache-2.0 on
its [Hugging Face model page](https://huggingface.co/convaiinnovations/laya/tree/main/multilingual);
check the license on the pinned artifact before redistributing weights. ATENEA
does not bundle Python, PyTorch, or model weights.

### Representative decision evaluation

The 48 curated examples above are a smoke corpus, not a production-quality
estimate. For a promotion decision, collect 200–400 distinct, consented or
otherwise legitimately available real requests, remove names, paths, secrets,
and customer data, and keep the files outside Git. Include the actual language
mix and separate stress cases for negation, ambiguous requests, and
`plan` versus `change`. Set the calibration/test split before trying thresholds;
group near-duplicates in one split. Have two reviewers label the intended
action independently and resolve disagreements before running the classifier.
Keep labels in a separate file so the service never sees them.

Each line of the private case file has `id`, `text`, and `split`; only an
explicitly authorized write scenario may carry `granted_effects: ["write"]`.
An optional `context` uses the versioned `decision.IntentContext` shape, and
`files` can state the paths requested by the case. Each line of the private
gold file has the same `id` and either an `expected` intent or an
`expected_resolution`:

```json
{"id":"r001","text":"Planifica la migración sin cambiar archivos.","split":"test"}
{"id":"r002","text":"hazlo","split":"test","context":{"version":1,"repository":"atenea","accepted_plan_id":"accepted-7","accepted_plan_revision":"r2","accepted_plan_current":true,"active_objective":"actualizar la búsqueda","scope_files":["internal/search.go"],"constraints":["mantener compatibilidad"]}}
{"id":"r003","text":"hazlo","split":"test"}
```

```json
{"id":"r001","expected":"plan"}
{"id":"r002","expected":"change","expected_resolution":"resolved"}
{"id":"r003","expected_resolution":"needs_context"}
```

The case and gold lines belong in **different files**. A resolved label needs
an `expected` intent; a `needs_context` label omits it. Legacy gold rows with
only `expected` still mean `expected_resolution: resolved`. Run the local Laya
service with the intended routing configuration, then evaluate the no-context
rules and gated plans plus context-aware plans for rows that provide context.
For a regular request, the contextual classifier receives the effective
objective, constraints, and current request. For each regular request whose
context resolves, the service is called once without context and once with
context. An accepted-plan continuation is resolved from its typed context and
intentionally bypasses Laya. Missing or invalid context and file-scope
mismatches remain scoreable `needs_context` outcomes and are intentional
service skips. Pass the effective ATENEA settings file with configured model
roles and a declared repository. The embedded defaults leave model roles empty,
so they cannot produce valid plans for comparison. The evaluator never runs a
workflow or sends gold labels to the service.

```sh
go run ./cmd/atenea-laya-eval \
  --cases /private/laya-cases.jsonl \
  --labels /private/laya-labels.jsonl \
  --settings /private/atenea.toml \
  --repository atenea \
  --endpoint http://127.0.0.1:8000/v1/systemone \
  --model multilingual \
  --expected-routing-model multilingual \
  --threshold 0.8 \
  --report /private/laya-report.json \
  --review-packet /private/laya-review.jsonl
```

The report contains no request or context text. It records intent and
resolution confusion, paired wins/losses, false `change` classifications,
plan validity and effects, model/route identities, and service responses,
failures, and intentional skips. Context metrics use only labeled rows that
actually supplied context; `context_count` and `context_labeled` expose those
denominators. A valid `needs_context` plan has no workflow steps. It is an
intentional outcome, not a service failure. Corpus, gold, and settings hashes
identify the evaluated inputs without copying their text.
False-change counts distinguish rules, raw Laya, gated, context rules,
context Laya, and context-gated candidates. Raw Laya counts require an actual
service response.
`observe_duration_ms` includes planning work and the model call; it is not pure
model latency; context rows also have `context_observe_duration_ms`. A missing
response falls back to rules and counts as a service failure. A run against a
fake service proves the harness only; use the real
checkpoint and verify its identity for model-real evidence. Laya may route
English and non-English requests to different checkpoints; the report lists
every route. The optional `--model` pins the same checkpoint that
`[decision].laya_model` uses in production. Use `--expected-routing-model NAME`
to reject a response from a different route, and record the exact checkpoint
revision separately.

For blind review, give two independent reviewers separate copies of the
`--review-packet` file, **without the report**. Before evaluation, anonymize
request text, context, constraints, and paths. The packet includes the request,
requested `files` and explicit `granted_effects` (empty arrays when absent),
semantic context, scoped files, and two unmarked plan summaries: intent, agent,
roles, model availability (not model names), selected tools, workflow steps,
effects, estimated budget, validity, and file scope. Candidate and context-source
identity are omitted; repository and accepted-plan identifiers are replaced
with generic labels. Each reviewer fills
`preferred_option` with `a`, `b`, `tie`, or `both_bad` and adds a short
`reason`. The scorer groups preferences by both split and the compared
candidate pair, so context/no-context comparisons within one split remain
separate. Review whether the plan answers the request, selects appropriate
steps, preserves the user's limits, and stays within the supplied scope. Score
the two completed packets; disagreements require a third adjudication packet
containing just those IDs:

```sh
python scripts/score-laya-plan-reviews.py \
  --report /private/laya-report.json \
  --reviewer /private/laya-reviewer-1.jsonl \
  --reviewer /private/laya-reviewer-2.jsonl \
  --adjudication /private/laya-adjudication.jsonl
```

Predeclare a promotion rule before reading the held-out results: require a
clear paired improvement in correct decisions and blind plan preference,
no increase in false `change` results or unauthorized effects, acceptable
service availability and latency, and no material regression in any intent.
The tool reports evidence; it does not turn on `mode = "laya"` automatically.
For live shadow observation, use `mode = "observe"` with the same local
service. It records Laya's proposal in each dry-run plan while the rules plan
remains selected. Keep case, label, review, and full plan files private with
restrictive permissions; the review packet still contains the anonymized
request and semantic context. No representative private corpus or live shadow
result was available for the 48-row run above.

```sh
atenea decide "buscar el flujo de autenticación" --trace
atenea decide "diseñar el flujo de pagos" --repo taxiprime-backend --json
```

The plan makes these decisions visible, in order:

1. **Intent** — `understand`, `search`, `plan` or `change`, using deterministic
   rules by default, or the optional Laya classifier in observation or opt-in
   selection mode.
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
the current action and takes precedence over intent classification. ATENEA
does not send a bare `hazlo` or `do it` to Laya and ask it to infer the missing
conversation. Other requests follow the configured `rules`, `observe` or
`laya` mode.
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
context, including when standing write permission exists. Rules remain the default.
Development replay on an already inspected pilot is regression evidence, not a
held-out accuracy estimate or a reason to enable Laya by default.
