---
title: "Device flows and diagnostic statistics"
---

Contract 4.1 adds optional execution context.
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

Agent-device 0.20.10 wait/click validation is gated by version and schema hash.
Unknown versions receive a compatibility diagnosis. Use `atenea.command` with
`name=device.help` for examples or `name=device.sessions` for a read-only session
list. The upstream session command is not exposed as an unrestricted operation.
Start each flow with its own explicit session, absolute cwd and device identity.
Dependent calls reuse only that flow's successful context, check live session
state and reserve ownership. Uncertain state-changing calls are not retried.

To pin a Node-backed CLI, use `scripts/pin-node-launcher.py` with explicit
`--node`, `--entry`, `--output` and `--expected-version`. It verifies versions,
backs up an existing launcher and rejects a changed Node at launch. Validate
with a minimal PATH. Updating Node or the CLI requires explicit repinning.
`tools/mcp-agree --live` records direct server versions and schema fingerprints
per profile; disk declarations, wrapper policy and host-delivered tools are
separate evidence levels.
