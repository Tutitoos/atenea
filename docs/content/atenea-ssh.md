---
title: "Atenea SSH: local control window"
description: "Proposed multi-host Codex bridge, visibility modes, credentials and audit boundary (issue #141)."
weight: 43
---

# Atenea SSH: local control window

**Status:** Proposed contract for issue #141. No implementation or real-device
validation is claimed by this page.

## Purpose and entry points

`atenea-ssh` in a terminal and the `atenea-ssh` request in a local client with
the Atenea MCP installed open the same control window on the computer running
Atenea. A remote or cloud chat cannot open a window on an unrelated computer.
The window uses a separate loopback-only action service. It is not a route in
the existing read-only dashboard, which may be served over Tailscale or LAN.

The host list comes from concrete OpenSSH `Host` aliases in the current user's
configuration, including `Include` files. Wildcards, negated patterns, and
`Match` blocks do not become selectable devices. OpenSSH itself resolves each
selected alias's effective settings. Listing a host makes no connection;
diagnosis happens only when requested or while that host is selected. The
connection retains strict host-key verification. A changed or unknown host key
requires a separate, explicit trust decision outside task dispatch.

## Host and connector state

The selected host shows the effective alias and user, connection and
authentication state, operating system, connector version and installation
state, Codex availability, active request, last diagnostic error and bounded
logs. The UI distinguishes at least `unchecked`, `offline`,
`authentication_required`, `connector_absent`, `version_mismatch`, `ready`,
`busy` and `error`. It does not call a host ready merely because port 22 is
open. Installation or update is explicit, checks artifact integrity and
preserves pending requests and a rollback copy. The first connector target is
Windows and runs work in the logged-in user's session; its account and Codex
login stay on that PC.

## Credentials

An SSH password and a private-key passphrase are separate credential types,
bound to the resolved host identity and login user. Atenea uses the operating
system's protected credential store on the controller: macOS Keychain, Windows
Credential Manager or Linux Secret Service, with an explicit unavailable state
where no protected store exists. A key passphrase already managed by the
system SSH agent is not copied into Atenea. No secret is stored in OpenSSH
config, Atenea configuration, a command argument, process environment,
request receipt, log or history row. Credential retrieval and SSH prompting
are local to the controller; the remote connector never receives the store's
master key or unrelated credentials.

## Task visibility

Visibility is chosen on each request and defaults to `visible`. It is
independent of the task's permission mode and is recorded with the request.

| Mode | Codex execution | PC app | Atenea history |
| --- | --- | --- | --- |
| `visible` | A durable app-owned conversation, new or explicitly selected | May be opened and continued on the PC | Full request and state history |
| `hidden` | An independent `codex exec --ephemeral` turn | No app conversation is created or opened | Full request and state history |

`--no-open` by itself does **not** implement hidden mode: it only suppresses
window opening and may leave a durable conversation. An existing app-owned
thread cannot be continued in hidden mode, and a hidden turn cannot later be
resumed as a visible app conversation. Requests needing that thread or its
desktop capabilities refuse hidden mode before dispatch. The UI shows this
constraint and requires the user to choose visible mode. The selected sandbox
(`read-only` or an explicitly permitted write mode) is still enforced
independently of visibility. App-owned threads keep their existing app
permissions; Atenea must not describe them as read-only merely because the
control window defaults to read-only for new CLI work.

## Dispatch, recovery and history

Before dispatch, Atenea persists an immutable receipt keyed by host identity
and UUID. The receipt binds the prompt, visibility, permission mode, selected
thread and timeout. A second submission of the same UUID and same bytes
returns its existing state; different bytes are refused. An uncertain SSH
failure is reconciled through the original UUID and host, never by sending a
new request. Each host serializes its own connector requests; independent
hosts may run concurrently. `queued`, `running`, `completed`,
`needs_attention`, `failed`, `app_pending`, `cancelled`, `timed_out` and
`orphaned` remain distinct states. Cancellation does not undo work already
completed on the PC.

The local history records when, which SSH alias and verified host identity,
request UUID, prompt, visibility, permission mode, remote thread when present,
state transitions, result and errors. Prompts and sensitive result bodies are
encrypted at rest under a data key protected by the controller's native
credential store. The UI may decrypt them for the local user; the published
dashboard and ordinary operational logs do not expose them. Diagnostic logs
are bounded and sanitized before rendering. If the key store or durable
history is unavailable, dispatch refuses before contacting the host rather
than performing an unaudited action.

## Evidence needed before release

Local tests must cover SSH config enumeration, identity changes, credential
store failures, action-service authorization, idempotency, concurrent hosts,
history encryption/restart and visible/hidden refusal paths. A real Windows
test must separately prove connector installation, a visible app conversation,
a hidden ephemeral turn absent from the app, reconnect without duplicate
dispatch and history retrieval. Repeat host-specific checks on a second PC
before calling the feature multi-host validated. Source checks and unit tests
alone are not evidence of those real-device behaviors.
