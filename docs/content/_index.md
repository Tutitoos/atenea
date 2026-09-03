---
title: Atenea
type: docs
---

# Atenea

An orchestration core for agents and MCP tooling that lives **outside** the CLIs
it serves. Every client — omp, Claude Code, Codex — connects to the same core.
OpenCode is an opt-in model backend rather than a client adapter.

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

**These pages describe Atenea `1.1.0`**, speaking
contract `3.6.0`. The product is stable; Claude Code, Codex, OpenCode and the
Kivgraph web viewer remain optional external provider surfaces. The
[changelog](https://github.com/Tutitoos/atenea/blob/main/CHANGELOG.md) has what
landed.

The core, the Capability Registry and the funnel run on all four stages. The
shipped adapters include OMP, Claude Code and Codex, with graph
providers available through MCP. It installs as a background service that
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
- [Operations]({{< relref "operations" >}}) — release smoke tests, recovery and incident response
- [v1 readiness]({{< relref "v1-readiness" >}}) — the code-backed acceptance gate and deferred contracts
- [Benchmarks y métricas]({{< relref "benchmarks" >}}) — tests, cobertura, rendimiento, baselines y semáforos
- [Final 1.0.0 audit]({{< relref "v1-final-audit" >}}) — historical repository, team configuration and post-release limits
- [v1 contracts]({{< relref "v1-contracts" >}}) — structural search, permission and provider decisions
- [v1.0 policy]({{< relref "v1-policy" >}}) — garantías, límites y criterios de v1.1
- [Day to day]({{< relref "day-to-day" >}}) — the five commands worth remembering
- [Architecture]({{< relref "architecture" >}})
- [Decision router]({{< relref "decision" >}}) — model, tool, MCP, provider and workflow choices
- [Settings]({{< relref "settings" >}})
- [Computer Use]({{< relref "computer-use" >}}) — macOS desktop access through Atenea
- [When a provider looks flaky]({{< relref "diagnosing-providers" >}})
- [When the instrument is the bug]({{< relref "measuring-the-wrong-process" >}}) — three defects that were not there
- [What is not built yet]({{< relref "not-built-yet" >}}) — and what the next brick is
- [Open questions]({{< relref "open-questions" >}}) — what dogfooding turned up, not yet decided
