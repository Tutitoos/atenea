---
title: "Maintenance, device flows and diagnostic statistics"
---

Contract 4.1 adds optional execution context and compiler implementation evidence.
Existing raw MCP schemas and result bodies remain unchanged. A functional MCP
error (`isError=true`) fails the receipt and both request/attempt statistics even
when transport succeeded. Request, attempt and receipt identifiers carry the
correlation; historical records are never matched by approximate timestamps.

`atenea stats --month --used` remains a read operation. It reports requested and
observed coverage. `atenea stats --month --errors --limit 50` pages failures with
`--cursor`; `--error-code`, `--client`, `--profile` and `--origin` narrow the page.
Origins are `normal`, `synthetic` or `unknown`, only from explicit evidence.
Monthly cause/context aggregates survive the seven-day detailed retention.
P95 requires retained observations; expired detail is not reconstructed.

Kivgraph queries have a 90-second budget. A stale query requests one shared
background rebuild and returns `maintenance_pending` with its job identifier.
`atenea.command` with `name=maintenance` reads its state; optional `id` reads a
historical job. Explicit `graph.ensure_fresh` joins the shared job and waits,
with a separate 30-minute indexing budget. Canceling a query leaves the shared
job running. Service shutdown closes owned workers and records interruption.
A failed generation/inputs pair requires an explicit retry or changed inputs.
Only a verified served generation clears stale Kivgraph health observations.

Agent-device 0.20.10 wait/click validation is gated by version and schema hash.
Unknown versions receive a compatibility diagnosis. Use `atenea.command` with
`name=device.help` for examples or `name=device.sessions` for a read-only session
list. The upstream session command is not exposed as an unrestricted operation.
Start each flow with its own explicit session, absolute cwd and device identity.
Dependent calls reuse only that flow's successful context, check live session
state and reserve ownership. Uncertain state-changing calls are not retried.

`symbol.implementations` retains its positional input and `locations` output,
adding optional `limit`, `cursor`, `detection`, provenance and completeness.
Kivgraph `find_implementations` returns compiler-proven Go and TypeScript type
and method relations. Declared and structural evidence remain distinguishable;
empty pages prove absence only within COMPLETE coverage. Rebuild legacy graphs.

This capability requires the locally maintained Kivgraph build exposing
`find_implementations` (validated with `0.9.8-local.atenea-audit.1`, canonical
schema 5 and TypeScript facts v5). Those provider changes remain local and are
not part of an upstream release or this Atenea repository. An older Kivgraph
installation does not gain implementation queries by updating Atenea alone.

To pin a Node-backed CLI, use `scripts/pin-node-launcher.py` with explicit
`--node`, `--entry`, `--output` and `--expected-version`. It verifies versions,
backs up an existing launcher and rejects a changed Node at launch. Validate
with a minimal PATH. Updating Node or the CLI requires explicit repinning.
`tools/mcp-agree --live` records direct server versions and schema fingerprints
per profile; disk declarations, wrapper policy and host-delivered tools are
separate evidence levels.

When the TypeScript worker changes, install the complete verified Kivgraph
bundle. The binary-only development installer deliberately leaves worker files
unchanged. Preserve the previous bundle and active graph before local activation.
