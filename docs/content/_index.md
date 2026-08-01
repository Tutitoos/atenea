---
title: Atenea
type: docs
---

# Atenea

An orchestration core for agents and MCP tooling that lives **outside** the CLIs
it serves. Every client — omp, Claude Code, OpenCode — connects to the same
core.

The one sentence that explains the rest: **Atenea decides and delegates, it does
not execute.** The flow is always `goal -> capability -> implementation`.

## Why it exists

Tooling changes constantly. A workflow written against `ripgrep` breaks the day
`ripgrep` is replaced. A workflow written against `code.search` does not: the
capability is stable, and which tool answers it is a decision Atenea makes with
what it knows about the repository in front of it.

## The pieces

| Piece | What it is |
| --- | --- |
| **Capability** | A stable, tool-agnostic action. `code.search`, `symbol.definition`. |
| **Implementation** | A concrete provider of a capability, in four blocks: capability, constraints, cost, health. |
| **Repository** | The unit of work. Not the project — the repository. |
| **Selector** | The funnel that picks an implementation: constraints, then health, then cost. |
| **Adapter** | A dumb translator between the core and one CLI. All the intelligence stays in the core. |

## Where the project stands

Alpha, version `0.x.y`. The first brick is in place: the core, the Capability
Registry and the funnel selector running on constraints and health.

The cost filter is deliberately not wired yet. On a cold start there are no
fresh measurements, so ranking by cost would mean ranking by guesswork. It joins
the funnel once the metrics base is feeding real numbers.

## Read next

- [Getting started]({{< relref "getting-started" >}})
- [Architecture]({{< relref "architecture" >}})
- [Settings]({{< relref "settings" >}})
