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
adding optional `limit`, `cursor`, `detection` and `language`, plus provenance,
resolution, edge kind, coverage and completeness. The 1.2 contract accepts only
Go and TypeScript source files, including repositories that contain both; Dart
and other languages are rejected before the Kivgraph session is opened.

Kivgraph `find_implementations` returns compiler-proven Go and TypeScript type
and method relations. The provider wire has no `resolution` field: Atenea
derives `resolution=exact` only after validating the language-specific evidence
matrix. Go accepts `GO_TYPES_USE` with `structural` or `GO_OBJECT_PATH` with
`typed`; TypeScript accepts `TYPESCRIPT_IMPL_DECLARED` with `declared` or
`TYPESCRIPT_IMPL_STRUCTURAL` with `structural`, together with an exact
confidence (`EXACT_TYPECHECKED`, `EXACT_DECLARATION_MAPPED`,
`EXACT_PACKAGE_MAPPED` or `STRUCTURAL_CERTAIN`) and `IMPLEMENTS` or
`OVERRIDES`. Candidate and unresolved evidence
are not implementation rows; they remain in the provider's real coverage
fields `candidate`, `unresolved_related` and `package_level`.

Repository identity, safe relative paths, requested scope, snapshot,
completeness, pagination and generation are validated together. Exact coverage
is authoritative and cannot be inflated from the returned page. Empty pages
establish absence only within COMPLETE coverage; LOWER_BOUND or truncated pages
do not.

Subject resolution is deterministic and ambiguous positions stop before
`find_implementations`. The adapter performs no cache, implicit indexing or
rebuild. Legacy Kivgraph generations that omit the new relation metadata are
reported as invalid evidence rather than being promoted to exact locations.

The P13 provider baseline is Kivgraph `e28323742c8f148a859dcc54045727629ab4ba8e`
from its upstream `main`. Atenea consumes the native `find_implementations`
tool and validates its returned evidence; it does not duplicate Kivgraph's
graph or Dart analysis implementation. The matrix keeps declaration,
connection and functional testing as separate facts: a declared tool is not a
connection, and a connection is not a tested semantic result. `symbol.unresolved`
remains outside the advertised capability set.

Kivgraph's component tests generate Dart `IMPLEMENTS`/`OVERRIDES` evidence, but
the real `kivgraph index --full` attempt failed because LadybugDB native support
was unavailable and no generation was published. Atenea's
`symbol.implementations` preflight remains Go/TypeScript only. Dart support in
that Atenea capability is pending a real provider and client end-to-end
observation; no Dart locations are claimed here.

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
