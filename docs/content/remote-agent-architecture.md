---
title: "ADR: Remote agent architecture"
description: "Planned architecture and security boundary for remote desktop agents (issue #88)."
weight: 8
---

# ADR: Remote agent architecture

**Issue:** #88
**Status:** Decision-complete design direction; planned, not validated
**Scope:** The remote-agent transport, identity, lifecycle, capabilities, and
platform support described on this page.
**Evidence:** No subject evidence was supplied for this ADR. Every platform,
mode, capability, and protocol behavior below is therefore a plan, not a claim
that Atenea or an agent currently implements or supports it.

## Context

Atenea needs a governed way to observe and operate a user’s remote desktop
without making the desktop itself a general-purpose execution host. The
remote machine must be able to keep running its local session, and a person
must be able to continue using the PC while an attended operation is in
progress. This is a device-agent problem, not a request to expose a remote
login service.

The design must therefore provide a small, versioned, auditable protocol, a
shared control-plane coordinator, and a reference conformance harness before
three independently maintained native, control-plane-only baseline agents:

| Agent | Implementation constraint | Planned target |
| --- | --- | --- |
| Windows | C#/.NET 10, self-contained deployment | Windows 10 22H2 x64 and Windows 11 x64 |
| macOS | Swift agent | macOS target to be selected during implementation |
| Linux | Rust agent | Linux target to be selected during implementation |

The agents are separate implementations of one wire contract. They do not
share a downloaded runtime, load one another’s modules, or accept arbitrary
code from Atenea. A platform-specific implementation may report a capability
as unavailable; it may not pretend that another implementation executed it.

## Decisions

### 1. Connection and protocol

The agent always initiates the connection. The only permitted path is an
outbound WebSocket Secure connection over the Tailscale network to an
allowlisted Atenea endpoint, using the exact subprotocol:

```text
atenea.remote.v1
```

The agent has no listening socket. It must refuse a connection if Tailscale is
unavailable, the endpoint is not reachable through the configured tailnet
path, TLS validation fails, or the negotiated subprotocol is not exactly
`atenea.remote.v1`. Public DNS, public inbound ports, port forwarding, and
direct Internet fallback are not part of this design.

Each frame is bounded and carries an opaque request ID, device identity,
capability name, mode, deadline, and server grant reference. A mutating frame
also carries the observation generation on which it relies. A response names
the same request ID and returns a typed result or typed refusal. Unknown frame
versions, malformed fields, expired deadlines, missing grants, and unknown
capabilities are refusals, never best-effort execution.

### 2. One-time enrollment and persistent identity

Enrollment is an explicit administrative action:

1. The control plane creates a short-lived, single-use enrollment record for a
   named device and intended platform.
2. The new agent generates its private key locally and sends a certificate
   signing request plus the one-time enrollment proof over the tailnet.
3. The control plane sends an X.509 challenge containing a fresh nonce and
   challenge context. The agent signs it with the generated private key.
4. The control plane verifies proof of private-key possession, the enrollment
   record, platform declaration, and certificate policy before issuing the
   device certificate.
5. The enrollment record is consumed. Reuse, replay, expiration, a mismatched
   device, or a failed challenge terminates enrollment and does not create a
   partially trusted device.

The private key and device identity are persistent across reconnects and
restarts. They are stored using the platform’s protected local facility and
are not sent to Atenea, copied into logs, or exportable through a remote
capability. Every later connection presents the device certificate and proves
possession using a fresh X.509-bound challenge; tailnet reachability alone is
not identity.

The control plane owns certificate issuance, renewal, and revocation. A
revoked device is marked unavailable before a new request can be authorized;
the bounded revocation behavior for active requests is defined below. The
revocation path also walks the active-session registry, sends a typed
revocation close, and closes every active socket for that device. Reconnects
remain refused until a new enrollment is completed. If revocation state cannot
be read, the control plane fails closed for that device rather than treating
unknown state as valid.

### 3. Session lifecycle, heartbeat, and negotiation

After authentication, the agent and control plane exchange a version and
capability hello. The hello declares the agent build, platform, architecture,
mode support, and exact capability identifiers. The server returns an explicit
accepted protocol version, identity, policy, and capability set. The accepted
set is the intersection of policy and the agent’s declared set; no capability
is inferred from a similar name, and no unsupported capability is silently
replaced by another one.

The planned heartbeat is a server-bounded ping every 15 seconds with a
45-second liveness deadline. A missed deadline marks the session unavailable,
stops pending work, and closes the socket. The agent does not queue or replay
mutating requests after a disconnect. A reconnect must authenticate again and
renegotiate version, mode, grants, and capabilities.

Mode is an explicit session property, set by the control plane and confirmed
by the local agent. An agent cannot promote a background session to attended
or isolated by itself. A mode change invalidates pending action requests and
is recorded in the audit stream.

### 4. Authorization, leases, and revocation

Every authorized request has an agent-enforced monotonic lease. The lease
starts when authorization is accepted, is measured with a monotonic clock, and
is no longer than 5 seconds; the server may choose a shorter lease. A wall
clock change, heartbeat, retry, or reconnect never extends the lease. The
agent stops the request at lease expiry and returns a typed refusal if the
lease is no longer valid.

Revocation is fail-closed and takes effect in this order: the control plane
marks the device, grant, or session unavailable and rejects new work
immediately; it then sends a typed revocation event to each connected agent.
Connected agents must acknowledge the event, stop in-flight work, and close
the affected session. A missing acknowledgement does not extend a lease and
closes the session. A partitioned agent cannot receive the event, so its
monotonic lease is the upper bound: it stops in-flight work at lease expiry.
No mutating request is queued or replayed after revocation, lease expiry,
disconnect, or reconnect. If revocation state is unavailable, new work is
refused and active work is stopped at its lease boundary.

Every mutating action carries a fresh `observation_generation` no older than
2 seconds at dispatch. For this ADR, mutating actions are `open`, `close`,
`invoke`, `set`, `select`, `toggle`, `expand`, `scroll`, `click`, `type`, and
`key`. Immediately before dispatch, the agent revalidates the target identity,
owning process, focus and visibility where relevant, current mode, grant and
lease, and sensitive classification. A missing or older generation, changed
identity or owner, failed focus/visibility check, changed mode or grant, or
unknown or changed sensitive classification returns `stale_target` or a
stricter typed refusal; the action is not dispatched and is not retried
through another capability.

### 5. Independent native agents

The Windows agent is a self-contained C#/.NET 10 executable and service. It
does not depend on a machine-wide .NET installation. The macOS agent is a
native Swift process, and the Linux agent is a native Rust process. Each
agent has its own packaging, update, permission, capture, input, and process
isolation implementation while conforming to the same protocol and refusal
semantics.

Updates are signed, versioned artifacts installed by an explicit local
installation workflow. The remote protocol cannot download and execute a
module, script, plugin, or dynamic library. A version or signature that is
not accepted by the local update policy fails closed.

### 6. Modes and Windows support boundary

The planned Windows modes are:

| Mode | Meaning | Input boundary |
| --- | --- | --- |
| `background` | Service operation without an actively supervising local user | `remote.desktop.click`, `remote.desktop.type`, and `remote.desktop.key` are always refused; other capabilities still require explicit grants and target policy |
| `attended` | A local user has explicitly enabled a bounded session and can see or interrupt it | The three direct input capabilities may be available after policy, target, and grant checks |
| `isolated` | Operation inside a disposable compatible Hyper-V environment | The three direct input capabilities may be available only inside the isolated target and after policy and grant checks |

Background and attended operation are planned for Windows 10 22H2 x64 and
Windows 11 x64, including Home editions. Isolated operation is planned only
for compatible Windows 10/11 x64 Pro or Enterprise installations with
Hyper-V. Windows Home is not an isolated-mode target. A missing Hyper-V
feature, incompatible edition, failed VM boundary, or uncertain mode causes a
typed refusal; it does not fall back to background or attended operation.

Attended mode is not a claim that a human will notice every screen or action.
It means the local user explicitly enabled the session, the session has a
bounded lifetime, and the agent can receive interruption. The control plane
must still deny sensitive surfaces and unapproved actions.

### 7. Remote capability namespace

Remote operations use the `remote.desktop.*` namespace. They are separate from
local `desktop.*` in routing, permissions, audit records, device identity,
target policy, and receipts. A local capability is never used as a transport
fallback for a remote request, and a remote capability is never exposed under
the local namespace.

The complete planned v1 surface is:

| Capability | Planned contract | Default safety rule |
| --- | --- | --- |
| `remote.desktop.targets` | Enumerate registered, policy-visible remote targets | Return only identifiers and bounded metadata; do not expose raw filesystem or credential data |
| `remote.desktop.open` | Open an explicitly registered application or document target | No shell string, arbitrary URI, or unapproved executable |
| `remote.desktop.apps` | List policy-visible applications/windows and their stable handles | Sanitize titles and metadata; omit denied or sensitive surfaces |
| `remote.desktop.inspect` | Read a bounded semantic/accessibility view of a target | Redact secret fields and refuse uncertain or sensitive targets |
| `remote.desktop.screenshot` | Return a bounded, policy-approved current image | Ephemeral by default; no screen persistence or automatic history |
| `remote.desktop.close` | Close an explicitly selected application/window target | Require a stable target handle and explicit grant; no process-tree kill |
| `remote.desktop.invoke` | Invoke a named, allowlisted semantic action | The action name must be registered and target-bound; never evaluate code |
| `remote.desktop.set` | Set an allowlisted control value | Refuse secret fields, untyped values, and targets outside policy |
| `remote.desktop.select` | Select an allowlisted item or control | Require a stable inspected target; no coordinate guessing |
| `remote.desktop.toggle` | Toggle an allowlisted boolean control | Require a stable inspected target and explicit grant |
| `remote.desktop.expand` | Expand an allowlisted semantic container | Refuse ambiguous or sensitive targets |
| `remote.desktop.scroll` | Scroll a selected target within bounded limits | Require a target handle and bounded delta; no unrestricted gesture stream |
| `remote.desktop.wait` | Wait for a bounded state or timeout | Bounded deadline only; it cannot keep a revoked session alive |
| `remote.desktop.get` | Read a specific, policy-approved control value or state | Sanitize output and refuse secrets or an unproven target |
| `remote.desktop.click` | Send one guarded pointer click to a selected target | Attended or isolated only; absent mode, grant, or target proof is typed refusal |
| `remote.desktop.type` | Enter text into a selected non-sensitive target | Attended or isolated only; typed text is never persisted by default |
| `remote.desktop.key` | Send an allowlisted key or bounded key sequence | Attended or isolated only; absent mode, grant, or target proof is typed refusal |

`click`, `type`, and `key` are direct input and are never available in
`background`. The agent must return a stable refusal code such as
`mode_required`, `capability_denied`, `target_unavailable`, or
`sensitive_surface`; it must not downgrade to `invoke`, `set`, a coordinate
guess, or a local `desktop.*` call. The same typed-refusal rule applies to any
capability that is missing from negotiation or local platform policy.

Sensitive surfaces are outside this ADR: password managers, keychains,
credential stores, authentication and one-time-code prompts, banking and
payment surfaces, private-message content, and any target classified as
sensitive by policy. If classification is unavailable or uncertain, the
target is treated as sensitive and refused. Screen pixels, extracted text,
typed text, and application payloads are held only for the bounded request
unless a separate, explicit evidence policy authorizes a redacted artifact.

### 8. Diagnostics and audit boundaries

Diagnostics are sanitized operational data: protocol and agent versions,
platform, capability names, refusal codes, timing buckets, connection state,
heartbeat state, and opaque correlation IDs. They exclude private keys,
enrollment proofs, provider keys, cookies, environment variables, command
arguments, typed text, screen pixels, accessibility text, document contents,
and raw application responses. Paths and window titles are sanitized
independently, before any length bound is applied. A length bound limits size
but is not sanitization. If either sanitizer cannot establish a safe redacted
form, the field is omitted; if it is required to authorize or identify the
target, the operation is refused. Raw or merely truncated paths and titles
never leave the agent.

The audit stream records control-plane facts needed to answer who authorized
what, on which device, in which mode, and with which result:

- enrollment creation, successful consumption, challenge result, certificate
  issuance, renewal, and revocation;
- session authentication, negotiation, mode changes, heartbeat timeout,
  disconnect, and forced close;
- capability request name, opaque request ID, grant reference, target class,
  deadline, refusal/result code, and the identity of the authorizing policy.

Audit records do not contain screen or text payloads, typed values, secrets,
provider keys, or arbitrary action arguments. The audit boundary is not a
screen recording system. Observed screen/text is not persisted by default,
and a future evidence-capture feature must define its own consent, redaction,
retention, access, and deletion contract before it can be enabled.

Authorization is fail-closed at the audit boundary. Before any capability is
dispatched, a sanitized authorization-start record must be durably appended
and its append and integrity acknowledgement verified. The record includes
only the opaque request and session identifiers, capability, target class,
mode, grant reference, lease, observation generation and age, and authorizing
policy identity. Audit unavailability or an integrity failure returns a typed
refusal and prevents dispatch. After dispatch, completion must also be
durably appended. Failure to append completion closes the session and blocks
further work on it.

## Explicit exclusions

This ADR deliberately excludes:

- RDP, WinRM, VNC, SSH-as-desktop-control, or any other remote-login service;
- public inbound ports, Internet fallback, port forwarding, and unauthenticated
  direct connections;
- arbitrary shell, arbitrary process execution, free-form scripts, and
  process-tree control;
- provider API keys, model credentials, or other Atenea secrets on agents;
- dynamic code loading, remote plugins, downloaded executable logic, or
  protocol messages that evaluate code;
- silent capability fallback, silent protocol downgrade, and namespace
  substitution between `remote.desktop.*` and local `desktop.*`;
- sensitive surfaces and operations that cannot prove a non-sensitive target;
- default persistence of observed screen pixels, extracted screen text,
  accessibility trees, typed text, or application content.

## Alternatives considered

| Alternative | Decision | Reason |
| --- | --- | --- |
| RDP or a remote-login protocol | Rejected | It makes a login/session transport the security boundary and does not provide the narrow, typed, auditable capability contract required here. |
| Public HTTPS/WSS listener on each device | Rejected | It expands the attack surface and makes Internet exposure part of installation; the agent must instead dial out through Tailscale only. |
| A shared cross-platform runtime | Rejected | It weakens native permission and isolation choices and conflicts with independent Windows C#/.NET 10, macOS Swift, and Linux Rust agents. |
| Arbitrary shell behind one remote tool | Rejected | It collapses the capability boundary into code execution and cannot safely express target, mode, or sensitive-surface policy. |
| Silent fallback to local `desktop.*` or another capability | Rejected | It can operate a different machine or bypass the negotiated security policy; absence must be visible as a typed refusal. |
| Persist all screenshots and extracted text for replay | Rejected | It creates a high-value data store and changes observation into unsolicited surveillance; evidence capture requires a separate explicit contract. |

## Consequences

Positive consequences:

- The network boundary is narrow: an enrolled agent dials one private,
  authenticated endpoint and has no inbound service.
- Device identity survives reconnects, while revocation has an explicit effect
  on already active sessions.
- The capability namespace, mode, grant, target, and audit record remain
  separable from Atenea’s existing local desktop surface.
- Native agents can enforce platform-specific permissions and isolation without
  shipping provider credentials or a general-purpose execution engine.
- Unsupported versions, modes, capabilities, targets, and revocation state are
  visible failures rather than accidental fallbacks.

Costs and trade-offs:

- Three agents require three packaging, update, permission, and conformance
  pipelines.
- Attended interaction requires local-user consent and interruption handling;
  it cannot be treated as unattended automation.
- Isolated Windows operation is unavailable on Home and depends on a compatible
  Hyper-V boundary.
- The server must maintain certificate and session revocation state and must
  deliver forced closes reliably.
- Default non-persistence improves privacy but makes incident reconstruction
  dependent on separately authorized, sanitized evidence.

## Staged delivery

Every stage below is planned and must publish evidence separately from design
approval. Passing a local unit test does not prove support on a real client or
platform.

### Stage 0 — Shared protocol and control-plane coordinator

Define the `atenea.remote.v1` envelope, typed refusal codes, enrollment state
machine, certificate policy, revocation events, monotonic request leases,
heartbeat deadlines, mode transitions, capability schemas, observation
generations, sanitization rules, and audit event schema. Implement the shared
control-plane coordinator for authentication, negotiation, authorization,
dispatch gates, revocation, and audit ordering. This stage has no native
agent, observation, or action implementation.

**Exit evidence:** planned protocol and security fixtures; no platform or
capability is called supported.

### Stage 1 — Native control-plane baselines

Implement the outbound Tailscale-only WSS client, one-time X.509 enrollment,
persistent identity, authentication, negotiation, heartbeat, forced close,
sanitized diagnostics, and common refusal contract in the independent Windows
C#/.NET 10, macOS Swift, and Linux Rust agents. This baseline advertises no
desktop observation or action capability. A deterministic reference harness
exercises the same wire contract before any platform claim is promoted.

**Exit evidence:** protocol-conformance fixtures plus provider-real tailnet
enrollment, reconnect, heartbeat, and revocation traces for each baseline.
Installation or a passing harness is not client-real desktop evidence.

### Stage 2 — Read-only observation

Add platform permission checks and the read-only capabilities `targets`,
`apps`, `inspect`, `screenshot`, `wait`, and `get` to the already conforming
native baselines. Packaging and update evidence remains platform-specific.

**Exit evidence:** client-real permission and observation runs on each claimed
target, plus privacy tests proving no default screen/text persistence. Until a
target has this evidence, its observation capabilities remain planned.

### Stage 3 — Governed semantic actions

Add `open`, `close`, `invoke`, `set`, `select`, `toggle`, `expand`, and
`scroll`, with stable target handles, explicit grants, bounded deadlines, and
sensitive-surface refusal. Keep every action in `remote.desktop.*` and keep
local `desktop.*` routing unchanged.

**Exit evidence:** target-policy and refusal conformance tests plus attended
and background client-real traces. A successful request sent to an agent is
not proof that the intended UI outcome occurred; outcome verification must be
reported separately.

### Stage 4 — Direct input and Windows isolation

Add `click`, `type`, and `key` only after the attended consent/interruption
flow and isolated Hyper-V boundary are implemented. Prove Home background and
attended behavior separately from Pro/Enterprise isolated behavior on Windows
10 22H2 x64 and Windows 11 x64.

**Exit evidence:** client-real attended traces, isolation escape tests, typed
refusals in background/Home, and forced close on revocation. No passing test
may achieve support by silently changing mode or target.

### Stage 5 — Conformance and operational readiness

Run the same protocol, privacy, revocation, downgrade, audit, and refusal
conformance suite against all three agents. Document support per OS version,
edition, architecture, mode, and capability. Add rollback, certificate
rotation, lost-device revocation, and incident-response procedures.

**Exit evidence:** reproducible test reports plus provider-real tailnet and
client-real device evidence for every claimed matrix cell. Any unobserved cell
remains planned, not validated.

## Threat model

The control plane, certificate authority, policy store, and Tailscale ACLs are
trusted configuration authorities but are not assumed infallible. The remote
agent host, local desktop applications, network path, and capability results
are treated as potentially compromised or misleading. The protocol must
reduce authority when it cannot establish its preconditions.

| Asset | Trust boundary | Threat | Mitigation | Fail-closed behavior |
| --- | --- | --- | --- | --- |
| Device private key and identity | Agent protected storage ↔ control plane CA | Theft, export, replay, or impersonation | Generate locally; protected storage; X.509 proof of possession; one-time enrollment; certificate policy | Failed proof, missing key, or invalid certificate prevents session creation |
| Enrollment record | Administrator ↔ enrollment service ↔ new agent | Brute force, replay, wrong-device enrollment | Short-lived single-use record bound to device/platform; nonce challenge; consume atomically | Reuse, expiry, mismatch, or challenge failure creates no identity |
| WSS session | Agent ↔ Tailscale-only endpoint | MITM, endpoint substitution, public exposure, downgrade | Outbound-only tailnet route; TLS validation; exact `atenea.remote.v1`; bounded frames; ACLs | No tailnet route, TLS failure, wrong subprotocol, or unknown version closes the socket |
| Active authorization | Control-plane policy/grants ↔ agent | Stale grant, confused deputy, cross-device request | Server-owned grant reference, device/session binding, mode and deadline on every request | Missing, expired, mismatched, or unreadable grant returns typed refusal |
| Revocation state | CA/policy store ↔ session registry ↔ agent | Revoked device continues operating | Check before authorization; connected-agent acknowledgement; active socket closure; agent-enforced monotonic request lease of at most 5 seconds | Unknown state denies new work immediately; connected work stops on acknowledgement and partitioned work stops no later than lease expiry |
| Heartbeat/liveness | Session registry ↔ agent | Dead or partitioned agent appears live; queued action after reconnect | Bounded heartbeat and deadline; no mutating replay; session generation | Deadline expiry cancels pending work and closes session |
| Screen pixels and UI text | Agent capture ↔ control plane/client | Secret leakage, log exfiltration, replay | Sensitive-surface policy; sanitization; bounded in-memory lifetime; no default persistence | Uncertain classification or persistence-policy failure refuses observation |
| Direct input | Control plane ↔ local user/session ↔ OS input APIs | Unattended typing/clicking, stale-target action confusion, local-user interference | Mode gate; explicit grants; attended interruption; isolated VM option; observation generation at most 2 seconds old; immediate identity, owner, focus/visibility and sensitivity revalidation | Background, stale generation, changed target, or missing mode/grant yields a typed refusal and no input event |
| Sensitive applications | OS surface ↔ target classifier/policy | Credential, banking, payment, keychain, or private-message access | Deny list and conservative classification; no secrets in diagnostics | Unknown or sensitive target cannot be opened, inspected, or acted on |
| Agent executable | Installer/update authority ↔ local host | Tampered artifact, plugin injection, dynamic code execution | Signed/versioned native artifacts; self-contained Windows build; no remote code loading | Signature, version, or install-integrity failure prevents launch/update |
| Provider credentials | Atenea provider boundary ↔ agent boundary | API-key theft or privilege expansion | Keep provider keys and model credentials in the control plane/provider boundary | Any request needing a provider key on the agent is unsupported/refused |
| Audit records | Agent/control plane ↔ audit store | Sensitive payload logging, tampering, misleading evidence | Allowlisted metadata schema; opaque IDs; independently sanitized fields; durable append and integrity acknowledgement before dispatch | Audit unavailability, integrity failure, or unsafe required metadata refuses dispatch; completion-append failure closes the session and blocks further work |
| Capability namespace | Local `desktop.*` ↔ remote `remote.desktop.*` routers | Cross-machine confusion or silent fallback | Separate registries, grants, receipts, identities, and explicit route names | Namespace mismatch is a typed error; no alternate route is attempted |

## Support and evidence matrix

This matrix describes the intended support envelope, not current support. Every
row is **planned — not validated** until the required evidence is produced.

### Platforms and modes

| Platform/target | Background | Attended | Isolated | Required evidence | Status |
| --- | --- | --- | --- | --- | --- |
| Windows 10 22H2 x64 Home | Planned | Planned | Not a supported target | Real Home device traces plus typed isolated refusal | **Planned — not validated** |
| Windows 10 22H2 x64 Pro/Enterprise | Planned | Planned | Planned with compatible Hyper-V | Real device traces, VM-boundary tests, escape tests | **Planned — not validated** |
| Windows 11 x64 Home | Planned | Planned | Not a supported target | Real Home device traces plus typed isolated refusal | **Planned — not validated** |
| Windows 11 x64 Pro/Enterprise | Planned | Planned | Planned with compatible Hyper-V | Real device traces, VM-boundary tests, escape tests | **Planned — not validated** |
| macOS Swift agent | Planned target | Planned target | Not specified by this ADR | Real macOS installation, permission, transport, and refusal evidence | **Planned — not validated** |
| Linux Rust agent | Planned target | Planned target | Not specified by this ADR | Real Linux installation, permission, transport, and refusal evidence | **Planned — not validated** |

### Protocol and lifecycle

| Surface | Required evidence before support can be claimed | Status |
| --- | --- | --- |
| Tailscale-only outbound WSS and `atenea.remote.v1` | Tailnet-only connection trace, negative public-route test, TLS/subprotocol refusal tests | **Planned — not validated** |
| One-time enrollment and X.509 challenge | Replay, expiry, wrong-device, key-possession, and successful enrollment traces | **Planned — not validated** |
| Persistent device identity | Restart/reconnect proof with protected key handling and no key export | **Planned — not validated** |
| Revocation closes active sessions | Provider-real revocation event showing forced close and blocked reconnect | **Planned — not validated** |
| Heartbeat and liveness deadline | Missed-heartbeat cancellation and reconnect negotiation trace | **Planned — not validated** |
| Version/capability negotiation | Compatible and incompatible hello traces; no downgrade or silent fallback | **Planned — not validated** |
| Sanitized diagnostics and audit boundaries | Redaction tests and audit inspection proving payloads/secrets are absent | **Planned — not validated** |

### Remote capabilities

| Capability | Planned availability | Required evidence | Status |
| --- | --- | --- | --- |
| `remote.desktop.targets` | All supported agents, policy-visible targets | Client-real enumeration and redaction tests | **Planned — not validated** |
| `remote.desktop.open` | Explicitly registered, non-sensitive targets | Allowlist and arbitrary-shell negative tests | **Planned — not validated** |
| `remote.desktop.apps` | Policy-visible applications/windows | Client-real listing and sensitive-surface omission | **Planned — not validated** |
| `remote.desktop.inspect` | Bounded, non-sensitive semantic inspection | Redaction, ambiguity, and target-policy tests | **Planned — not validated** |
| `remote.desktop.screenshot` | Bounded, non-sensitive ephemeral observation | No-persistence test and client-real capture | **Planned — not validated** |
| `remote.desktop.close` | Explicit target only | Target binding and no-process-tree-control tests | **Planned — not validated** |
| `remote.desktop.invoke` | Registered semantic actions only | Action registry and code-evaluation negative tests | **Planned — not validated** |
| `remote.desktop.set` | Registered non-sensitive controls | Type, grant, and secret-field refusal tests | **Planned — not validated** |
| `remote.desktop.select` | Stable inspected targets | Target-handle and ambiguity tests | **Planned — not validated** |
| `remote.desktop.toggle` | Stable inspected boolean controls | Target, grant, and refusal tests | **Planned — not validated** |
| `remote.desktop.expand` | Stable inspected containers | Target and sensitive-surface refusal tests | **Planned — not validated** |
| `remote.desktop.scroll` | Bounded selected targets | Delta/deadline and target-binding tests | **Planned — not validated** |
| `remote.desktop.wait` | Bounded state/deadline wait | Timeout, cancellation, and revocation tests | **Planned — not validated** |
| `remote.desktop.get` | Specific non-sensitive state/value | Redaction and unknown-target tests | **Planned — not validated** |
| `remote.desktop.click` | Attended or isolated only | Home/background typed refusal plus attended/isolated client-real action evidence | **Planned — not validated** |
| `remote.desktop.type` | Attended or isolated only | Secret-field refusal, no-persistence test, and client-real action evidence | **Planned — not validated** |
| `remote.desktop.key` | Attended or isolated only | Background typed refusal, allowlist, and client-real action evidence | **Planned — not validated** |

No matrix cell is promoted by documentation alone. A local protocol test is
local evidence; a tailnet connection is provider-real transport evidence; and
a successful operation on a real supported device is client-real evidence.
Until all required evidence for a cell exists, its support level remains
planned and unvalidated.

## Unresolved evidence and follow-up decisions

The design is decision-complete for this ADR, but implementation evidence is
intentionally unresolved:

- No agent exists here that proves the Windows C#/.NET 10, macOS Swift, or
  Linux Rust targets.
- No real Windows 10 22H2, Windows 11, Home, Pro, Enterprise, or Hyper-V run
  has been observed for this contract.
- No tailnet endpoint, certificate authority, enrollment ceremony, revocation
  stream, heartbeat measurement, or protocol conformance run has been
  validated.
- No platform permission mapping, accessibility/capture mechanism, input
  interruption behavior, or isolated VM boundary has been validated.
- The exact macOS versions, Linux distributions, Tailscale ACL layout,
  certificate rotation schedule, audit retention, and sensitive-surface
  classifier remain implementation details to resolve before the relevant
  Stage 1 or Stage 2 exit evidence can be claimed.
- The semantics and outcome verification for each native application need
  client-real fixtures; an accepted command or captured screen alone is not
  proof of the requested UI result.

These gaps are not support claims and must not be closed by changing a status
cell to “supported” without the evidence named in the matrix.
