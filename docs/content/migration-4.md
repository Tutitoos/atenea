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

At the original retirement, `symbol.implementations`, `symbol.search` and
`symbol.unresolved` had no provider. The subsequent Kivgraph integration restores
`symbol.search` with a structural declaration search. The other two remain in
the catalog/status but absent from `tools/list`; direct calls return
`not_offered`. New source, symbol-impact and graph-maintenance capabilities are
additive to contract 4.0 and do not revive the retired provider.

Kivgraph, Tokensave and generic MCP transports/supervision remain available.
Historical benchmarks, incident reports and Git history are not runtime configuration
and do not need to be deleted.
