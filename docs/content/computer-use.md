---
title: Computer Use
weight: 7
---

# Computer Use through Atenea

Atenea exposes macOS Computer Use as typed `desktop.*` capabilities. Clients
connect to the `atenea mcp` bridge; they do not connect directly to the helper
or to a second Computer Use MCP server.

## First-phase posture

The first phase is observation only for connected clients:

- `desktop.apps`
- `desktop.inspect`
- `desktop.screenshot`

The interactive capabilities remain denied by
`[orchestrator] client_denied_capabilities`:
`desktop.move`, `desktop.drag`, `desktop.scroll`, `desktop.click`,
`desktop.type` and `desktop.key`.

`client_effects = ["process", "device"]` permits the observation surface while
still withholding `write` and `external`. The capability kill switch is needed
as well because pointer movement is deliberately classified as `read + device`.

## macOS boundary

Configure `[desktop] applications` with explicit bundle identifiers. An empty
list denies every application. Use `denied` for password managers, keychain,
banking and any other application that must never be inspected, even when a
wildcard allow-list is used.

The helper needs Accessibility for accessibility-tree inspection and input
control. It needs Screen Recording for window captures. Atenea reports a
missing permission as a typed refusal and does not retry a mutating operation
after the helper exits or loses its graphical session.

## Visual tracking on macOS

With `[desktop] visual_feedback = true` (the default), the helper shows a
3-point Atenea gradient border around the captured window, a virtual cursor in
that window and in a movable 360×240 preview. The preview is local and
ephemeral: it adapts between observation and action rates, blurs after idle,
closes after 30 seconds, and never records video, screenshots or event history
on disk. Closing it suppresses only the visuals; Atenea continues working.

`desktop.screenshot` returns an opaque `frame_id`. Pass that token to
coordinate actions when available. Atenea validates the PID, bundle, window
ID, geometry, scale and visibility again before sending an event, and refuses
stale frames or a point covered by another application. Accessible controls use
their Accessibility action first; canvas and emulator surfaces use a guarded
foreground CGEvent fallback, so a target may briefly take focus.

The event monitor ignores Atenea's marked synthetic events. Human movement,
clicks, scrolling or keys pause the current action and show `Paused`; `Resume`
unblocks future actions without replaying the interrupted one. If the monitor
permission is unavailable, observations remain usable but mutating operations
are refused while visual feedback is enabled. Set `visual_feedback = false` to
hide the border, cursor and preview while keeping all frame and window safety
checks.

## Enabling interaction deliberately

To enable the second phase, add the required application bundle ID, grant
`write` and `external` to the connected-client floor, set
`client_denied_capabilities = []` or remove only the selected capabilities,
and keep `look_then_act = false` unless the operator accepts the prompt-
injection tradeoff. The CLI's `atenea desktop ... --confirm` remains the
manual confirmation path.

Receipts retain the capability, application, non-sensitive coordinates or key,
effects, result and denial reason. Typed text and image content are excluded.

## Centralization limit

MCP adds Atenea tools; it does not transparently replace a client's native
`Bash`, `Read`, `Glob`, browser or automation tools. Disable direct Computer
Use declarations and configure only the Atenea MCP server when Atenea must be
the central route.
