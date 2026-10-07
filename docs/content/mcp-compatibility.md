---
title: MCP compatibility
weight: 5
---

# MCP compatibility

Atenea recognizes the exact MCP revisions `2025-06-18` (legacy) and
`2026-07-28` (modern). The names and validation policy live in
`internal/mcpcompat`; transports do not silently substitute one revision for
the other.

Legacy uses `initialize`/`initialized` and may keep a transport session. Modern
requests are self-contained: they carry the reserved protocol, client identity
and capability metadata in `params._meta`. Modern HTTP requests use the
protocol and method headers and do not send `Mcp-Session-Id`; modern stdio
requests keep JSON-RPC framing and use the same reserved metadata.

The core creates a temporary application session for each modern dispatch and
closes it before sending the response. The observed client and capabilities
are context, never a permission grant. A workflow can continue across calls
only through its explicit persisted identifier; transport reconnects do not
carry application permissions or hidden client state.

Both transports preserve cancellation semantics for their era. HTTP modern
cancellation closes the request context, while legacy cancellation uses the
protocol notification. stdio cancellation uses `notifications/cancelled` for
both eras. Reconnection re-establishes the selected protocol and keeps the
application allow-list separate from the transport lifecycle.

Modern results share the MRTR contract. `complete` is accepted; `input_required`
is preserved as untrusted data, including the `inputRequests` map, opaque
`requestState` string and any `inputResponses` field, and is reported to the
caller. `inputResponses` is a retry parameter and does not make a server result
valid by itself. Atenea does not execute those
requests, manufacture responses or retry them automatically. The current core
does not initiate an MRTR input exchange; interactive input handling remains a
future, explicit capability.

Modern `server/discover` is validated as a cacheable result with an object
`capabilities` value and a version intersection. The core advertises only its
modern stateless revision and its actual tools/prompts capabilities. A future
peer version may appear alongside modern and is ignored until supported.
Modern-pin performs and validates discovery before its first request on HTTP,
stdio and passthrough transports. HTTP passthrough mirrors valid primitive
`x-mcp-header` tool arguments into `Mcp-Param-*`, with strict RFC token names,
safe integer handling and encoded non-header-safe string values.

The compatibility tests cover local Unix-socket core dispatch, streamable HTTP,
stdio, probes and passthrough backends. They do not claim that every external
desktop client or remote MCP server has been functionally validated.

## Agent-device interaction contract

For the pinned `agent-device` `0.20.10` release, Atenea advertises a detached,
stricter schema for `raw.agent-device.open`, `click` and `fill`. The upstream
schema is retained intact for version and fingerprint checks. If the release or
one of those schemas changes, Atenea withholds the affected tool from
`tools/list` until its contract is reviewed; a direct call is refused as
`compatibility_unverified` before device dispatch.

Supply an explicit nonempty `session` and absolute `cwd` for all three tools.
`open` also needs a nonempty `udid`, `serial` or `device`. For `click` and
`fill`, use a discriminated `target` object: `{"kind":"ref","ref":"@e12"}`,
`{"kind":"selector","selector":"role=button"}` or
`{"kind":"point","x":10,"y":20}`. `fill` additionally requires `text`;
do not put private text in diagnostics. A reference must match `@e` followed
by digits. The selector must contain `=`. The upstream variants and permitted
fields otherwise remain unchanged.

JSON Schema can describe these argument shapes. It cannot prove that a
reference is fresh, that a named session belongs to the current flow, that a
device is free, or that the action completed after a transport failure. Atenea
checks ownership and live session state before dispatch. If a mutating result
is uncertain, inspect the session and a fresh snapshot before deciding whether
another action is needed. Use `atenea.command` with `name=device.sessions` or
`name=device.help` for recovery. The local contract tests do not certify
Android or iOS client behavior; each needs separate real-client acceptance.
