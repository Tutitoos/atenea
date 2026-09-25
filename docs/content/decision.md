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
laya_api_key_env = "ATENEA_LAYA_API_KEY" # omit when the service has no bearer key
timeout = "10s"
minimum_confidence = 0.8
```

If the service requires a bearer token, put its value in the named environment
variable. ATENEA never stores the token in its settings file. Commission text is
sent to the endpoint only in `observe` or `laya` mode. Keep the service bound to
loopback for local use; use HTTPS and an explicitly trusted endpoint for a
remote service. Redirects are refused.

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
service. Verify those IDs against the checkpoint you intended to measure. The
current rules baseline on this curated corpus is 25/32 (78.1%) on calibration
and 11/16 (68.8%) on the held-out test split. The held-out set is intentionally
small and emphasizes negations and plan-versus-change ambiguity; these figures
describe this corpus, not general production accuracy.

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
| Calibration (32) | 25/32 (78.1%) | 22/32 (68.8%) | 23/32 (71.9%) | 20/23 (87.0%) | 26/32 (81.3%) |
| Held-out test (16) | 11/16 (68.8%) | 12/16 (75.0%) | 12/16 (75.0%) | 9/12 (75.0%) | 11/16 (68.8%) |

Raw Laya confusion matrix over all 48 rows:

| Expected \\ predicted | change | plan | search | understand |
| --- | ---: | ---: | ---: | ---: |
| change | 10 | 1 | 0 | 1 |
| plan | 3 | 6 | 0 | 3 |
| search | 0 | 1 | 11 | 0 |
| understand | 2 | 1 | 2 | 7 |

The 0.8 gate improved calibration by one example and left held-out accuracy
unchanged. This run does not show a held-out uplift, so keep `rules` as the
default and treat `0.8` as an experimental threshold. Use `observe` to gather
representative traffic evidence before considering `laya` mode. Local fake-server
tests still validate only the wire contract; the table above is a separate run
against the real checkpoint. The evaluated checkpoint is marked Apache-2.0 on
its [Hugging Face model page](https://huggingface.co/convaiinnovations/laya/tree/main/multilingual);
check the license on the pinned artifact before redistributing weights. ATENEA
does not bundle Python, PyTorch, or model weights.

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
