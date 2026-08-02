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
| **Selector** | The funnel that picks an implementation: constraints, reach, health, then cost. |
| **Adapter** | A dumb translator between the core and one far side — a CLI, or an MCP server. All the intelligence stays in the core. |

## Where the project stands

Alpha, version `0.x.y`. The core, the Capability Registry and the funnel run on
all four stages. Three adapters ship: `omp` and Claude Code answer text search
by driving a CLI, and Serena answers the three symbol capabilities by speaking
MCP over HTTP.

Cost ranks the survivors rather than filtering them, and it says which figure
it used. Until an implementation has been measured a couple of times, that
figure is the estimate the catalog declared — the trace prints `estimated` so
nobody reads a guess as an observation. This is the design's break-in period:
the numbers get better as real work runs through, and nothing has to be
rewritten when they do.

## Read next

- [Getting started]({{< relref "getting-started" >}})
- [Architecture]({{< relref "architecture" >}})
- [Settings]({{< relref "settings" >}})
