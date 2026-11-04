---
title: Migrating to contract 5.0
weight: 5
---

# Migrating from 4.x

Contract 5.0.0 finishes the removal tracked in #212: Atenea is `ask` plus the
MCP capability surface, and nothing else. A 4.x settings file is refused until
the tables below are deleted. Changing only the header is not enough, and the
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

`atenea ask` and the whole MCP capability surface (`code.search`,
`code.context`, `workspace.context`, `symbol.*`, `graph.*` and every capability
you declared) behave as before. `[orchestrator]`, the selector, the metrics
base, the result cache and the catalog blocks are untouched, so no other key
moves.

## Fixing a 4.x file

A file that still has a retired table fails at load, naming the table:

```text
atenea: invalid_input: settings ~/.config/atenea/atenea.toml: [workflow] was removed in contract 5.0.0 with the workflow engine; delete the table (see docs/content/migration-5.md)
```

Several retired tables are reported in one message. Delete each one named,
then change the header to `contract = "5.0.0"`. A file that declares a 4.x
header and has none of those tables is refused by the contract check instead:

```text
atenea: invalid_input: settings ~/.config/atenea/atenea.toml: contract 4.1.0 is not supported by this core (5.0.0): contract 5.0.0 removed the agents, the workflow engine, the decide command and the knowledge store: delete the [model], [workflow], [[workflow_profile]], [local_agents], [decision], [knowledge] and [[agent]] tables if present, then change the contract line to "5.0.0" (see docs/content/migration-5.md)
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
  readable.

## Release certification

The checked-in Codex certificate binds the source commit, the binary, the
profiles and the MCP overlay, so it is stale for every source change in this
series (it also expired 2026-10-08). A real Codex re-certification by the
operator, with the private key, is **required before the next release**; the
release workflow's certification gate fails until it is done.
