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

**These pages describe Atenea `0.6.1`**, speaking
contract `2.0.0`. The product is in alpha at `0.x.y` and reaches `1.0.0` when it
goes stable; the contract adapters compile against is already a commitment. The
[changelog](https://github.com/Tutitoos/atenea/blob/main/CHANGELOG.md) has what
landed.

The core, the Capability Registry and the funnel run on all four stages. Four
adapters ship: `omp` and Claude Code answer text search by driving a CLI,
`codebase-memory` walks the call graph it keeps on disk for `symbol.calls`,
`code.impact` and `repository.index` and reads that same graph at rest for
`symbol.overview`, and Serena speaks MCP over HTTP for
all four symbol capabilities. It installs as a background service that
keeps its own history in shape, proven now against a live language server
on this repository: `symbol.definition` and `symbol.references` resolve
across files, not only within the one a caller happened to be looking at,
`symbol.implementations` answers for real too, not only the empty
result an earlier bug let through, and `symbol.overview` lists what a file
declares before anything in it is known by name — the question the other
three all assume has already been answered.

Cost ranks the survivors rather than filtering them, and it says which figure
it used. Until an implementation has been measured a couple of times, that
figure is the estimate the catalog declared — the trace prints `estimated` so
nobody reads a guess as an observation. This is the design's break-in period:
the numbers get better as real work runs through, and nothing has to be
rewritten when they do.

## Read next

- [Getting started]({{< relref "getting-started" >}})
- [Day to day]({{< relref "day-to-day" >}}) — the five commands worth remembering
- [Architecture]({{< relref "architecture" >}})
- [Settings]({{< relref "settings" >}})
- [When a provider looks flaky]({{< relref "diagnosing-providers" >}})
- [What is not built yet]({{< relref "not-built-yet" >}}) — and what the next brick is
- [Open questions]({{< relref "open-questions" >}}) — what dogfooding turned up, not yet decided
