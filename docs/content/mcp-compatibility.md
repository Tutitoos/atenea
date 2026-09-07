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
protocol notification. Stdio cancellation uses `notifications/cancelled` for
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
