---
title: Migrating to contract 4.0
weight: 4
---

# Migrating from 3.x

Serena is a retired provider. Contract 4.0.0 deliberately rejects 3.x files.
Do not update only the version header: first remove the following declarations:

- `serena` from `orchestrator.runners`.
- `[orchestrator.serena]` and `[orchestrator.serena.process]`.
- The `mcp_server` with `id = "serena"`.
- All five `serena.*` implementation blocks and any selector rules naming them.
- `serena` from repository `indexed_by` lists.

Then change the header to `contract = "4.0.0"` and run `atenea config show`.
Unknown adapter tables, the retired runner, MCP ID, implementation IDs and index
markers are rejected rather than silently ignored. Keep `--no-serena` in
Headroom/OpenCode wrappers: it prevents automatic registration of this retired provider.

The capability contracts are unchanged. `symbol.implementations`, `symbol.search`
and `symbol.unresolved` have no provider; they remain in the catalog/status but
are absent from `tools/list`. Direct calls return `not_offered`.

Kivgraph, Tokensave and generic MCP transports/supervision remain available.
Historical benchmarks, incident reports and Git history are not runtime configuration
and do not need to be deleted.
