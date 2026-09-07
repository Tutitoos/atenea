# Dart capability coverage

Version: `1.0.0`
Date: `2026-09-06`
Scope: Dart fixtures and local Atenea contracts; no external provider or client

Provider baseline: **Kivgraph** at `e28323742c8f148a859dcc54045727629ab4ba8e` (github.com/Tutitoos/kivgraph). verified upstream main revision; Atenea consumes the native MCP tool and does not duplicate its graph implementation.

| Capability | Language | Declared | Connected | Functionally tested | Evidence | Scope |
|---|---|---|---|---|---|---|
| code.context | dart | proven | unknown | proven | local | one explicit Dart repository fixture through the local code.context contract |
| symbol.implementations | dart | unsupported | unknown | unsupported | local | Dart request rejected during Atenea preflight before a Kivgraph session/provider call |
| workspace.context | dart | proven | unknown | proven | local | two independent Dart child repository fixtures coordinated locally |

## Provider attempts

- **Kivgraph**: `failed` via `kivgraph index --full`, evidence `pending`; LadybugDB native support is unavailable; no generation was published.

## Component evidence

- **Kivgraph Dart loader**: `proven`, evidence `component_local`; Component-local evidence does not prove an Atenea provider connection, client presentation, or Dart symbol.implementations support.

## Limitations

- Dart source interpretation was not tested; local Session responses prove routing and policy only
- S03 and S07 are revalidated from the fixed corpus separately and its SHA is never changed by this report
- real_provider and real_client evidence are pending; a real Kivgraph CLI attempt reached index --full but failed because LadybugDB native support was unavailable, so no generation was published
