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
do not put private text in diagnostics. A reference is `@e` followed by digits,
optionally followed by `~s` and up to 16 generation digits, for example
`@e12~s4`. The pinned form pairs a ref with the `refsGeneration` from the
snapshot or find response that issued it. The selector must contain `=`. The
upstream variants and permitted fields otherwise remain unchanged.

For both pinned releases (`0.20.10` and `0.21.23`), a `fill` ref target must
omit `target.label`: the SDK serializes that optional label into the entered
text. Atenea rejects any supplied label, including empty or `null`, before
dispatch and omits it from the advertised ref variant. It does not remove the
field from a call or rewrite the text. `click` ref labels and selector/point
targets retain their existing contract.

Each advertised `open` selector branch contains the complete verified
upstream field set, including `session`, `cwd`, `app` and `url`. This lets clients
that project one `anyOf` branch retain all valid options. Each branch still
requires `session`, `cwd` and its own nonempty selector; unknown fields remain
rejected. The upstream schema bytes and fingerprint are unchanged.

JSON Schema can describe these argument shapes. It cannot prove that a
reference is fresh, that a named session belongs to the current flow, that a
device is free, or that the action completed after a transport failure. Atenea
checks ownership and live session state before dispatch. If a mutating result
is uncertain, inspect the session and a fresh snapshot before deciding whether
another action is needed. Use `atenea.command` with `name=device.sessions` or
`name=device.help` for recovery. The local contract tests do not certify
Android or iOS client behavior; each needs separate real-client acceptance.

### Staged agent-device 0.21.23 compatibility

The exact `0.21.23` release has a separate compatibility contract. Its complete
59-tool MCP catalog was compared with the 57-tool `0.20.10` catalog using only
`initialize` and `tools/list`. The capture, npm integrity, upstream Git revision,
and per-tool input-schema fingerprints live in
`internal/agentdevice/testdata/catalog-0.21.23.json`. The input schemas are
fixtures from the upstream MIT-licensed package, not device observations.

For this release, Atenea qualifies only the existing 20-tool **core** catalog,
plus the read-only session inspection command and `help`. Full-only tools remain
unqualified, including `batch`, `replay`, `test`, `settings`, and `shutdown`.
The newly added `action-button` and `fold` are also unqualified. Selecting a
`full` desktop profile does not widen this release's qualified catalog. Atenea
withholds these tools, records a compatibility diagnostic, and refuses direct
calls before dispatch. The `0.20.10` catalog and its existing behavior remain
available. Neither contract admits another version by a version range.

Candidate validation and dispatch share an opaque lease for the exact backend
instance, child process, catalog generation, protocol mode, version and
workspace. Ownership inspection and the final action use that same lease. If
the child exits or is replaced after validation, the call is refused without
starting or dispatching to a replacement. A failure after writing to the
validated child can still be uncertain and must be observed before retrying.

The upstream `0.21.23` MCP surface removed request fields for working directory,
daemon realm/authentication, and Apple runner configuration. Those are operator
configuration. A raw stdio backend must now declare its workspace explicitly:

```toml
[[mcp_server]]
id = "agent-device"
command = ["node", "/qualified/package/bin/agent-device.mjs", "mcp"]
working_directory = "/canonical/existing/qa-workspace"
expose = "raw"
# Keep the existing explicit tool budget and effect declarations.
```

`working_directory` is optional for other releases and supported only by raw
stdio backends. Configuration requires an absolute clean path; each child spawn
also checks that it is an existing directory without a symlink alias and sets
the child's actual `cwd` to that path. HTTP cannot establish this child binding.
Candidate tools are withheld when the binding is absent.

For `0.21.23`, every tool call supplies the matching `cwd` to Atenea. Atenea
checks it against the backend binding, then removes only that locally consumed
field from a detached wire argument map. Upstream schema bytes remain unchanged.
The read-only `device.sessions` command uses the operator's binding. Runtime
`stateDir`, `daemonBaseUrl`, `daemonAuthToken`, and Apple runner fields are
rejected rather than silently removed. Configure the realm and runner through
the backend's operator environment; do not isolate the global device claim
directory, which protects devices held by other sessions.

Qualified argument changes include bounded `open` startup/contending-session
waits, screenshot `cropOn`, scroll `until`, and the read-only `wait` absent
condition. Each remains subject to its exact schema, declared effects and
ownership checks. `wait` accepts exactly one supported condition. References,
explicit open/click/fill session and device selectors, self-contained open
branches, uncertain-result handling and occlusion protections remain in force.
There is no implicit coordinate fallback, keyboard replacement or test IME
activation added by this adapter.

Roll out first in an isolated workspace and realm with a verified package;
keep the normal package, configuration and signing unchanged. Validate automatic
Safari navigation, pinned click/fill, Android with its visible keyboard, Chrome
semantic accessibility, session closure and state restoration separately through
a real MCP client. Source tests and schema capture do not establish those
physical results. Install only after independent review, required checks and
authorization for the exact revision. Rollback restores the previous backend
command/environment and removes this candidate's sessions after inspection;
close only sessions owned by that rollout and preserve shared device claims.
