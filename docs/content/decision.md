---
title: Decision router
---

# Decision router

`atenea decide` is the explainable front door for turning a natural-language
commission into an execution plan. It is deliberately a dry run unless
`--run` is supplied.

```sh
atenea decide "buscar el flujo de autenticación" --trace
atenea decide "diseñar el flujo de pagos" --repo taxiprime-backend --json
```

The plan makes these decisions visible, in order:

1. **Intent** — `understand`, `search`, `plan` or `change`, using a small
   deterministic classifier that can later be replaced by a model returning
   the same vocabulary.
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

`--json` is intended for another orchestrator or UI. A valid plan can be
executed with one isolated workflow per repository:

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
separately. The existing `task` command remains available as the compatibility
path for its older capability-oriented commission format.

Model history is read from successful routed workflow steps. Native MCP
capabilities already use the selector's health and measured-cost funnel; raw
MCP tools remain explicit-only because they do not declare a semantic
capability contract that could be compared safely.

When a model times out or becomes unavailable, Atenea may try the next
declared fallback only if the remaining budget can be bounded. Provider-reported
costs are preferred; when the CLI returns tokens without dollars, a conservative
token estimate is used for the retry gate but is never recorded as billed USD.
Successful fallbacks appear as workflow notices.
