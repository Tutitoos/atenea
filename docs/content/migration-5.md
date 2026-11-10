---
title: Migrating to contract 5.0
weight: 5
---

# Migrating from 4.x

Contract 5.0.0 finishes the removal tracked in #212: Atenea is `ask` plus the
MCP capability surface, and nothing else. It also drops the Kivgraph provider
and the capabilities only it served (#208). A 4.x settings file is refused until
the tables, keys and blocks below are deleted. Changing only the header is not enough, and the
header alone is not what fails first: any retired table is refused by name.

## What was removed

Commands (`atenea --help` no longer lists them; each answers `unknown command`):

- `atenea task`, `atenea resume`
- `atenea workflow` (and `recovery pilot`)
- `atenea decide`
- `atenea agent`, `atenea agents`, `agent-exec`, `atenea floor`, `atenea traces`

MCP tools: `workflow.*`, `decision.plan` and `knowledge.context`.

Settings tables, with what each was:

| table | what it was |
| --- | --- |
| `[model]` | the seam the built-in agents called a model through |
| `[[agent]]`, `[[agent.result]]` | the declared agent types |
| `[local_agents]` | the ceiling on agent types a repository declared |
| `[workflow]`, `[[workflow_profile]]` | the workflow engine and its profiles |
| `[decision]` | the `decide` command (every key was already inert) |
| `[knowledge]` | the knowledge store |

`atenea ask` and the remaining MCP capability surface (`code.search`,
`code.context`, `workspace.context`, `symbol.overview`, `symbol.calls`, the
`desktop.*` and `android.*` families and every other capability you declared)
behave as before. `[orchestrator]`, the selector, the metrics base, the result
cache and the catalog blocks are untouched apart from what the next section
lists, so no other key moves.

## The provider dashboard command

`atenea dashboard ID` and `atenea dashboard hosts` are gone (#235), with the
`dashboard` key of `[[mcp_server]]` and the dashboard URL that `status` and
`detect` printed per server (text and `--json`). The command only checked and
opened a URL you declared; nothing else read the key, and Atenea never managed
a dashboard of its own after #211. `/etc/hosts` is no longer touched: a block
that an earlier `atenea dashboard hosts` wrote stays there, between its
`atenea` markers; delete it by hand if you want it gone.

A settings file whose `[[mcp_server]]` still has `dashboard` is refused at
load, naming the server, with every other removed setting in the same message:

```text
mcp_server "fixture" key dashboard was removed in contract 5.0.0 with the provider dashboard command; delete the key (see docs/content/migration-5.md)
```

Delete the key. A repository `.atenea/config.toml` may not declare
`[[mcp_server]]` at all, so the overlay needs no edit.

## Headroom

Headroom is no longer part of Atenea (#234). `atenea wrap --via-headroom` and
`atenea wrap --headroom-port` are gone, with the Headroom overlay and its
re-entry shim; run `atenea wrap CLIENT` instead. A command line that still
passes either flag is refused by name, not as an unknown flag:

```text
--via-headroom was removed in contract 5.0.0 with Headroom; run `atenea wrap CLIENT` instead (see docs/content/migration-5.md)
```

Nothing else changes in a settings file. `headroom` is not a reserved id: an
`[[mcp_server]]` with `id = "headroom"` is an ordinary MCP server and still
loads, so no edit is required. `tools/mcp-agree` now treats `atenea` alone as
the core.

On your machine, outside this repository and not done by Atenea:

- replace every shell alias that calls `atenea wrap --via-headroom CLIENT` with
  `atenea wrap CLIENT`;
- run `headroom unwrap CLIENT` for each client you wrapped with Headroom;
- stop the Headroom proxy.

## The Kivgraph provider and the capabilities only it served

Kivgraph is no longer part of Atenea (#208): the adapter, its `kivgraph`
runner, `[orchestrator.kivgraph]` (with its `process` and `dashboard_process`
tables), the `kivgraph.*` implementations, the `atenea.command maintenance`
command and the `atenea_kivgraph_usage` receipt key are gone. Nothing replaces
it. `indexed_by` and `requires_index` go with it, because Kivgraph was the only
provider that kept an index to ask about, and so does the index half of
`atenea detect` (it now only probes the declared `[[mcp_server]]` entries, and
`--repo` is gone).

These capabilities had no implementation left, so they left the shipped catalog
instead of staying declared but unserved. They disappear from `tools/list`, the
`catalog` and the orchestrator's askable surface:

| removed | removed | removed | removed |
| --- | --- | --- | --- |
| `symbol.search` | `symbol.intent_search` | `symbol.definition` | `symbol.references` |
| `symbol.implementations` | `symbol.dependencies` | `symbol.consumers` | `symbol.get` |
| `symbol.unresolved` | `symbol.source` | `symbol.impact` | `graph.status` |
| `graph.repositories` | `graph.ensure_fresh` | `repository.index` | `code.impact` |

These keep a provider and are unchanged: `code.search` (ripgrep, Claude Code,
Codex), `code.context` and `symbol.overview` and `symbol.calls` (Tokensave), and
the `desktop.*` and `android.*` families. `workspace.context` is not a catalog
capability and is unchanged.

The contract version stays `5.0.0`: it has not been released, so this is part of
its notes rather than a `6.0.0`. Clients that called a removed tool get the
ordinary unknown-tool answer. The client defaults (`client_denied_capabilities`,
the desktop profiles and the `atenea wrap` translation) never named any of them,
so nothing there changes.

The acceptance corpus moves to **v3** (`benchmarks/corpus/v3`): S01, S02, S03,
S05, S06 and S07 were probed only through the Kivgraph adapter and the Dart
coverage matrix (`cmd/atenea-dart-coverage`, removed), so they are retired
rather than re-pointed; S04 (workspace scope), S08 and S09 (MCP declarations)
remain. The runner therefore proves workspace scoping and the two protocol
declarations, and no longer language-specific context or implementation
relations. The `quality` gate went with the scenarios it covered.

## Fixing a 4.x file

A file that still has a retired table fails at load, naming the table:

```text
atenea: invalid_input: settings ~/.config/atenea/atenea.toml: [workflow] was removed in contract 5.0.0 with the workflow engine; delete the table (see docs/content/migration-5.md)
```

Several retired tables are reported in one message. Delete each one named,
then change the header to `contract = "5.0.0"`. The Kivgraph leftovers are named
the same way, all in one message with the sentences separated by `;`. The
sentences are:

```text
[orchestrator.kivgraph] was removed in contract 5.0.0 with the Kivgraph provider; delete the table and every sub-table (see docs/content/migration-5.md)
runner "kivgraph" was removed in contract 5.0.0 with the Kivgraph provider; delete the entry from orchestrator.runners (see docs/content/migration-5.md)
[[implementation]] kivgraph.definition, kivgraph.references was removed in contract 5.0.0 with the Kivgraph provider; delete the blocks (see docs/content/migration-5.md)
capability "symbol.definition" was removed in contract 5.0.0 with the Kivgraph provider, the only implementation it had; delete its [[capability]] block, every [[implementation]] that serves it and every [[selector.rule]] that names it (see docs/content/migration-5.md)
```

(The last covers a `[[capability]]`, `[[implementation]]` or `[[selector.rule]]`
naming a removed capability; several are listed together as `capabilities "a",
"b"`. `indexed_by` and `requires_index` are refused the same way. A file
generated by the old `config init` carries all of them, so delete the
`[orchestrator.kivgraph]` tables, the 17 `kivgraph.*` implementation blocks, the
16 capability blocks above, `indexed_by` on each repository and every
`requires_index` line.) The simplest fix for an unedited generated file is to
run `atenea config init --force` and re-apply your own changes. A file that declares a 4.x
header and has none of those tables is refused by the contract check instead:

```text
atenea: invalid_input: settings ~/.config/atenea/atenea.toml: contract 4.1.0 is not supported by this core (5.0.0): contract 5.0.0 removed the agents, the workflow engine, the decide command, the knowledge store, the Kivgraph provider and the provider dashboard command: delete the [model], [workflow], [[workflow_profile]], [local_agents], [decision], [knowledge] and [[agent]] tables, the [orchestrator.kivgraph] tables, the "kivgraph" runner, the kivgraph.* implementations, the capabilities named in docs/content/migration-5.md, every indexed_by key and the dashboard key of every [[mcp_server]] if present, then change the contract line to "5.0.0" (see docs/content/migration-5.md)
```

Validate the result with `atenea config show`. A 3.x file still needs the
[4.0 steps]({{< relref "migration-4" >}}) first.

## Old files stay on disk

Nothing deletes state. These files are no longer read or written, and can be
removed by hand:

- `traces.db` (including the workflow tables and the retention mark that now
  lives in `retention.mark`), `knowledge.sqlite` and `floors.json` in the state
  directory;
- `accepted-plans/` and `candidates.private.jsonl`;
- old checkpoint kinds still decode, so a receipt written by a 4.x core stays
  readable;
- Kivgraph's own installation and graph data (`~/.local/opt/kivgraph`,
  `~/.local/state/kivgraph`, its daemon unit) and the `indexed_by` entries in
  the registry state file, which are ignored when read. Shipped SQL migrations
  are not edited, and any Kivgraph rows in the measurement base stay dormant.

## Release certification

The checked-in Codex certificate binds the source commit, the binary, the
profiles and the MCP overlay, so it is stale for every source change in this
series (and the acceptance corpus pin changed again with v3) (it also expired 2026-10-08). A real Codex re-certification by the
operator, with the private key, is **required before the next release**; the
release workflow's certification gate fails until it is done.
