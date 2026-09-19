---
title: "Atenea SSH: local control window"
description: "Reviewed delivery contract for epic #141 and documentation issue #142; implementation pending."
weight: 43
---

# Atenea SSH phased delivery

Tracking epic. Linked issues are the implementation units and show current delivery status. This contract describes intended behavior; it is not evidence of runtime validation.

## Phases and dependencies

- [#142](https://github.com/Tutitoos/atenea/issues/142) — Architecture, protocol and platform contract. Prerequisites: none.
- [#151](https://github.com/Tutitoos/atenea/issues/151) — Early desktop shell, per-user controller and cross-platform feasibility. Prerequisites: [#142](https://github.com/Tutitoos/atenea/issues/142).
- [#143](https://github.com/Tutitoos/atenea/issues/143) — Side-effect-free SSH inventory and trust diagnostics. Prerequisites: [#142](https://github.com/Tutitoos/atenea/issues/142).
- [#144](https://github.com/Tutitoos/atenea/issues/144) — Native vaults and safe authentication. Prerequisites: [#143](https://github.com/Tutitoos/atenea/issues/143), [#151](https://github.com/Tutitoos/atenea/issues/151).
- [#145](https://github.com/Tutitoos/atenea/issues/145) — Encrypted history, keys and recovery receipts. Prerequisites: [#144](https://github.com/Tutitoos/atenea/issues/144).
- [#146](https://github.com/Tutitoos/atenea/issues/146) — Windows connector, installer and early compatibility canary. Prerequisites: [#142](https://github.com/Tutitoos/atenea/issues/142), [#143](https://github.com/Tutitoos/atenea/issues/143), [#144](https://github.com/Tutitoos/atenea/issues/144), [#145](https://github.com/Tutitoos/atenea/issues/145).
- [#147](https://github.com/Tutitoos/atenea/issues/147) — Visible/hidden execution and distributed admission. Prerequisites: [#143](https://github.com/Tutitoos/atenea/issues/143), [#144](https://github.com/Tutitoos/atenea/issues/144), [#145](https://github.com/Tutitoos/atenea/issues/145), [#146](https://github.com/Tutitoos/atenea/issues/146).
- [#148](https://github.com/Tutitoos/atenea/issues/148) — Complete desktop UI, app packaging and Terminal launcher. Prerequisites: [#143](https://github.com/Tutitoos/atenea/issues/143), [#144](https://github.com/Tutitoos/atenea/issues/144), [#145](https://github.com/Tutitoos/atenea/issues/145), [#146](https://github.com/Tutitoos/atenea/issues/146), [#147](https://github.com/Tutitoos/atenea/issues/147), [#151](https://github.com/Tutitoos/atenea/issues/151).
- [#149](https://github.com/Tutitoos/atenea/issues/149) — Effectful local chat opener. Prerequisites: [#148](https://github.com/Tutitoos/atenea/issues/148).
- [#155](https://github.com/Tutitoos/atenea/issues/155) — Local task-completion and attention notifications. Prerequisites: [#145](https://github.com/Tutitoos/atenea/issues/145), [#147](https://github.com/Tutitoos/atenea/issues/147), [#148](https://github.com/Tutitoos/atenea/issues/148), [#151](https://github.com/Tutitoos/atenea/issues/151).
- [#150](https://github.com/Tutitoos/atenea/issues/150) — Two-PC acceptance and controller support matrix. Prerequisites: [#142](https://github.com/Tutitoos/atenea/issues/142), [#143](https://github.com/Tutitoos/atenea/issues/143), [#144](https://github.com/Tutitoos/atenea/issues/144), [#145](https://github.com/Tutitoos/atenea/issues/145), [#146](https://github.com/Tutitoos/atenea/issues/146), [#147](https://github.com/Tutitoos/atenea/issues/147), [#148](https://github.com/Tutitoos/atenea/issues/148), [#149](https://github.com/Tutitoos/atenea/issues/149), [#151](https://github.com/Tutitoos/atenea/issues/151), [#155](https://github.com/Tutitoos/atenea/issues/155).

Follow the declared dependencies for delivery. [#151](https://github.com/Tutitoos/atenea/issues/151) proves the desktop/controller foundation before vault integration; [#143](https://github.com/Tutitoos/atenea/issues/143) can proceed independently after the contract. [#148](https://github.com/Tutitoos/atenea/issues/148) integrates the full UI after the backend features. [#146](https://github.com/Tutitoos/atenea/issues/146) now depends on inventory, credentials and audit because installer mutations need those controls. [#147](https://github.com/Tutitoos/atenea/issues/147) waits for the early Windows compatibility results.

## Later extensions

- [#152](https://github.com/Tutitoos/atenea/issues/152) — Linux SSH target connector with mode-specific readiness. Prerequisites: [#147](https://github.com/Tutitoos/atenea/issues/147) and [#150](https://github.com/Tutitoos/atenea/issues/150).
- [#153](https://github.com/Tutitoos/atenea/issues/153) — macOS SSH target connector with verified app-visible chats. Prerequisites: [#147](https://github.com/Tutitoos/atenea/issues/147) and [#150](https://github.com/Tutitoos/atenea/issues/150).
- [#154](https://github.com/Tutitoos/atenea/issues/154) — Opt-in encrypted history synchronization between controllers. Prerequisites: [#145](https://github.com/Tutitoos/atenea/issues/145) and [#150](https://github.com/Tutitoos/atenea/issues/150).

Linux/macOS extensions first prove mode-specific session and key-access feasibility; hidden execution does not by itself require a GUI session. History sync requires a reviewed transport, origin-authentication, membership/key-epoch and deletion contract before coding. Imported records are observation-only and cannot modify operational recovery state or trigger task notifications. Revocation protects new publication after the authoritative epoch change, not previously obtained copies.

These issues extend the system after the eleven core phases. They have their own acceptance gates and do not block [#150](https://github.com/Tutitoos/atenea/issues/150) or core closure of [#141](https://github.com/Tutitoos/atenea/issues/141).

## Product contract

Atenea SSH opens the same local control UI from `atenea-ssh` in Terminal or an installed local chat integration. It lists concrete SSH aliases, lets the user select and explicitly connect/check one host, shows connector/Codex state, stores SSH passwords and key passphrases in native vaults, sends a prompt with visible/hidden and permission choices, and exposes logs and encrypted action history.

- **Platforms:** controller OS and remote target OS are separate. macOS, Windows and Linux vault/controller rows need their own implementation and native validation; the current Atenea Unix IPC and service code do not imply a working Windows controller. The first remote connector is Windows. Linux and macOS SSH targets stay listed with unsupported connector state until optional [#152](https://github.com/Tutitoos/atenea/issues/152)/[#153](https://github.com/Tutitoos/atenea/issues/153) deliver and verify their modes. Missing SSH/network/Codex prerequisites require local bootstrap; the connector cannot install SSH on an unreachable PC.
- **SSH config and trust:** list parsing is side-effect free. `ssh -G` can execute Match exec, so it must never render the list. Selected-host resolution handles executable config/proxies explicitly; keep supported OpenSSH semantics and refuse unsupported cases. Unknown and changed host keys require a reviewed trust flow; never auto-accept them. Aliases/IPs are labels, not durable device identity.
- **Credentials:** passwords bind to verified destination/account; passphrases bind to the local private-key identity with separate per-destination use authorization. Reuse SSH agents, handle native vault failures, and protect the askpass/IPC exchange. Secret values from the vault never enter args/environment/history/logs. Metadata-only credential events are auditable. Windows controller readiness is not established by a Credential Manager adapter alone.
- **Visibility:** visible is default and must support NEW and EXISTING app conversations through target-version-verified paths. The prototype queue only covers an existing thread; [#146](https://github.com/Tutitoos/atenea/issues/146) performs the early canary. Hidden uses `codex exec --ephemeral` and must be shown absent from the target app; --ephemeral help alone is not visual proof. Hidden tasks remain in Atenea history and may cause ordinary OS effects; they are not invisible activity or a provider-retention guarantee. An ephemeral task is not resumed or silently converted to visible; required app-only capabilities are refused.
- **Permissions and outcomes:** visibility does not grant permission. Existing app permissions are inherited unless enforcement is proven. Turn completion, agent-reported success and independently verified action results are distinct. Tool-by-tool history is partial/unavailable unless the provider exposes it.
- **Recovery:** both controller and connector have durable UUID/digest receipts. Locks use stable target/account/session resources, including duplicate aliases and multiple controllers. Ambiguous receipt/start/enqueue/ack windows remain pending and retain locks until reconciled. Connector locks do not lock out the human using the desktop. No automatic new UUID or blind replay; do not promise exactly-once arbitrary side effects. Page through remote history instead of only the last ten turns. Wait expiry and cancellation request are not remote termination.
- **History/privacy:** each controller reads its own SSH config, vault and encrypted audit store; the first version has no automatic cross-controller history. Optional [#154](https://github.com/Tutitoos/atenea/issues/154) exchanges selected encrypted history with source provenance after explicit enrollment, while raw SSH config, private keys and native-vault secrets remain local. Encrypt full prompts/results locally, protect remote connector payloads and temporary files too, and disclose the provider's own transcript retention. Record connection/trust/credential metadata/install/dispatch/cancel/cleanup events and evidence coverage. Vault-sourced secrets must not enter audit payloads; user prompt text is retained encrypted and may itself be sensitive. Key loss, backup, deletion, export and receipt retention need explicit behavior.
- **Audit availability:** a failed pre-action receipt blocks new mutations. Failure after dispatch stops new work but must not block same-ID status or targeted cancellation. Reconcile audit gaps after recovery without inventing evidence.
- **Desktop window:** use a Wails v2 desktop shell with one shared React/TypeScript interface on macOS, Windows and Linux. Recreate the SwiftUI-style layout and interactions with shared components and design tokens; this is not the SwiftUI framework. Native window chrome, OS shortcuts, dialogs and credential prompts may differ. The Go controller remains independent of the window so closing it does not stop remote work. Validate the entire Wails native surface, including framework runtime commands rather than only our Go bindings, authenticate the app-to-controller IPC for the local OS user, restrict navigation and remote content, apply CSP and input bounds, and keep full prompts/actions off the published dashboard. Production uses packaged local assets and no browser control endpoint. The early [#151](https://github.com/Tutitoos/atenea/issues/151) gate covers WebView runtimes, native IPC and actual rendering; [#148](https://github.com/Tutitoos/atenea/issues/148) delivers the full feature UI. Prove rendered parity and packaging on all three required controller families with shared reference states, light/dark themes, licensed font/icon assets and accessible controls. Rendering differences and OS chrome are documented; SwiftUI does not define one pixel-identical screen.
- **Desktop lifecycle and privacy:** the graphical app and controller run for the intended OS user, with versioned authenticated IPC and a single owner of durable state. Window close is distinct from controller stop, logout and sleep; reconnect reconciles the same remote request rather than replaying it. WebView caches, URL state, logs and telemetry must not persist decrypted prompts or credentials. Draft persistence goes through the encrypted controller store; explicit exports/copies disclose plaintext. Production packaging and manual upgrades must verify artifacts and preserve compatible state and receipts.
- **Local notifications:** core [#155](https://github.com/Tutitoos/atenea/issues/155) uses the independent user-owned controller to show generic completion/failure or attention notices after verified state transitions, even with the Wails window closed. No target alias, prompt, result, credential or remote log appears in OS notification content or activation links. An OS-required same-user helper is allowed without a live Wails window. Denied/headless delivery leaves unread history for the next authenticated opening; Linux action capability is detected and manual navigation is available where actions are unsupported. Stop/logout/sleep defer delivery, with bounded expiry/deduplication. Imported history never triggers task alerts; OS acceptance is not proof the user saw a notice.
- **Chat entry:** an effectful typed opener with explicit installation/discovery, not an unannotated mutation in the existing read-only atenea.command surface. A process-launch or app-activation acknowledgment is not window-rendering proof.

## Completion criteria

All eleven core phase issues must meet their own acceptance gates, including local notifications. The three later extensions remain separately tracked. macOS, Windows and Linux controller families are required, each with explicitly tested OS versions/architectures and graphical/runtime prerequisites; an unobserved family remains incomplete. [#150](https://github.com/Tutitoos/atenea/issues/150) repeats the full product flow on two real Windows PCs and separately reports each claimed controller platform. Required unobserved behavior remains open, unless the user explicitly changes scope. Individual source PRs may merge with accurately bounded evidence; this does not by itself complete the epic.

## Review evidence and delivery

The desktop review uses the [Wails runtime injection documentation](https://wails.io/docs/guides/frontend/), [runtime browser API](https://wails.io/docs/reference/runtime/browser/) and [platform prerequisites](https://wails.io/docs/gettingstarted/installation/). These establish why the early native gate and complete bridge inventory are necessary; they do not validate an Atenea app. The existing dashboard is a React SPA (`ssr: false`); reusable UI does not imply sharing its published listener or privileged services.

The review reproduced Match exec during ssh -G using a temporary local fixture; see [OpenSSH configuration](https://man.openbsd.org/ssh_config). Local Codex CLI help confirms queue targets existing sessions and --ephemeral avoids persisted session files, neither proving new app-owned creation nor target app invisibility. Repository `internal/ipc/ipc.go`, `internal/platform/service_other.go` and `internal/core/command.go` establish the platform and read-only-command constraints.

Every code/documentation phase owns a branch/PR and its required checks. The final acceptance issue can close with reviewed operational evidence; it need not invent a code PR. No direct Closes [#141](https://github.com/Tutitoos/atenea/issues/141) implementation PR. No runtime changes, PC installs or full-feature completion are implied by these planning corrections.

## Out of scope

Restoring the retired native desktop-agent coordinator, an arbitrary remote shell UI, moving ChatGPT account credentials between PCs, automatic fleet installation, or claiming exact external side-effect execution from receipts.

## Implementation contract v1

This section is normative design for #142, not evidence of an installed controller or a supported Codex route. Implementations must negotiate capabilities and pass the issue-specific gates below. Examples use synthetic identities only.

### Requirement ownership

| User requirement | Implementation owner | Acceptance owner |
| --- | --- | --- |
| Same window from Terminal and local chat | #148 launcher, #149 effectful opener | #150 |
| Shared SwiftUI-style interface on three OS families | #151 foundation, #148 full UI | #150 |
| SSH config list and selected-host diagnosis | #143 | #150 |
| SSH password and key-passphrase storage | #144 | #150 |
| Connector absent/version/errors/logs | #146, #147, #148 | #150 |
| New/existing visible and hidden requests | #146 feasibility, #147 execution | #150 |
| Full prompt, result and action history | #145, #147 evidence, #148 presentation | #150 |
| Local completion/attention alerts | #155 | #150 |
| Linux/macOS remote targets | #152 / #153 | Respective extension |
| Optional cross-controller history | #154 | #154 |

### Supported configuration and evidence matrix

The following are required implementation rows, currently unverified. Minimum OS/runtime versions must be pinned from actual packaged builds in #151; this design does not invent a supported version range.

| Role | OS | Prerequisites | Required evidence |
| --- | --- | --- | --- |
| Controller | macOS | Logged-in graphical account; WKWebView; Keychain; user-owned IPC/controller | #151 native build/install/render/lifecycle; #144 vault; #150 integrated flow |
| Controller | Windows | Logged-in graphical account; WebView2; Credential Manager; per-user named-pipe controller | Same gates, including native ACL/server-identity tests; no session-zero GUI |
| Controller | Linux | Declared distro and X11/Wayland session; Wails-compatible WebKitGTK; Secret Service and session bus | Same gates per declared configuration; no inference from headless builds |
| First remote target | Windows | Trusted SSH endpoint, authorized account, Codex authentication and supported client; intended interactive session for visible modes | #146 install/protocol/canary; #147 turn behavior; #150 two distinct PCs |
| Later remote target | Linux | SSH/account, version-tested Codex route, reviewed protected key source; GUI only for claimed visible routes | #152, not a core readiness claim |
| Later remote target | macOS | SSH/account, Keychain availability and Codex route; GUI ownership/permissions for visible routes | #153, not a core readiness claim |

Development compiler/SDK requirements belong in the build matrix, separately from installed-user runtimes. Unavailable runtime, account, secret store or GUI prerequisites produce distinct unavailable states. Cross-compilation cannot fill a native-rendered evidence cell.

### Processes and trust boundaries

1. `atenea-ssh` is a local launcher. It activates the current user's installed graphical app. Repeated launch must reuse that app, not create a second controller or probe any host.
2. The Wails app renders packaged React/TypeScript assets. It has no durable history keys, remote credentials, shell API or production browser control listener. The published Atenea dashboard remains read-only.
3. A separate unprivileged per-user controller owns inventory, vault access, encrypted history, outbox and remote request coordination. It starts on demand under a single-instance OS lock and persists independently of window close or Quit UI. Explicit Stop controller drains local admissions and preserves receipts; it does not cancel remote work.
4. The controller uses the selected trusted SSH route to call a fixed connector protocol entry point. Prompts travel in bounded protocol input, never shell command text, arguments or environment. The target connector owns encrypted receipts, account/session admission and Codex interaction.
5. Native notification adapters or a narrowly scoped same-user helper receive generic notices and opaque lookup IDs. They do not own history or execute remote requests.

Local IPC is separate from the existing daemon's Unix-only surface. Unix sockets require a user-owned private directory, peer-user validation and refusal of symlink/stale ownership surprises. Windows named pipes require a DACL limited to the intended SID, client and server process identity checks, and session-aware app activation. A path or pipe name alone is not authentication. Startup must not overwrite another owner’s endpoint. Exact native implementation is a #151 gate.

IPC starts with a bounded handshake containing `protocol_major`, `protocol_minor`, `component`, `installation_id` and `session_instance_id`. Bind the negotiated connection to authenticated OS identity; none of those self-reported JSON identifiers grants access. Reject a different major version, an unsupported required capability or stale app activation. A new connection cannot replay a prior authorization. Activation grants only opening a window/detail; dispatch requires a separate validated command.

Logout and sleep may suspend or terminate local processes. On restart, acquire the single-writer lock, reopen the existing store/key namespace, then reconcile known UUIDs. Never regenerate missing keys or auto-submit drafts. Multiple logged-in graphical sessions cannot steal each other's focus; ambiguous activation is an explicit error. A compromised same-user OS account is outside this boundary.

### Wire encoding and limits

Use protocol `atenea-ssh/1` over authenticated local IPC and SSH standard input/output. Each message is one four-byte unsigned big-endian length followed by a UTF-8 JSON object. Maximum frame: 1 MiB; reject the length before allocating. For v1 reject unknown fields, duplicate JSON keys, invalid UTF-8, non-finite/overflow numbers, trailing JSON and unknown enum values. No JSON execution or polymorphic type loading. Negotiate extensions explicitly before changing the closed schema.

Bounds are bytes after UTF-8 encoding: prompt 256 KiB, path 4 KiB, identifier 256 bytes, diagnostic page 64 KiB, 100 history events per page. Output streams use sequence-numbered chunks up to 64 KiB. Backpressure and a configurable encrypted retention quota prevent unbounded memory/disk use; quota exhaustion marks truncated evidence and stops new admissions, never silently changes a remote outcome. All times are UTC RFC3339 timestamps except local monotonic wait timers. Client timestamps are evidence, not lock expiry authority.

The envelope contains `protocol_major: 1`, `protocol_minor: 0`, a UUID `message_id`, `operation`, and an operation-specific `body`. Operations are `hello`, `doctor`, `prepare`, `submit`, `status`, `cancel` and `history_page`. A response echoes `message_id`, supplies `ok` and exactly one of `body` or `error`. Request correlation does not establish authenticity without the underlying transport/account checks.

A dispatch payload has these required fields:

| Field | Type and meaning |
| --- | --- |
| `request_id` | Random UUID persisted before remote mutation; stable across retries/reconciliation |
| `target_id`, `account_id`, `installation_id` | Verified enrollment bindings, not aliases or IP addresses |
| `controller_id` | Source controller identity; not an authorization token |
| `mode` | `visible_new`, `visible_existing`, `hidden_ephemeral` |
| `permission_profile` | `inherit_existing` or an explicitly negotiated enforced profile ID; unsupported enforcement is refused |
| `thread_id` | Required only for `visible_existing`; absent for the other modes |
| `workspace_id` | Target-side approved workspace mapping; arbitrary shell/path interpolation is forbidden |
| `prompt` | Exact submitted UTF-8 text, within the bound above |
| `wait_timeout_ms` | Integer 1–300000; bounds a local observation wait, not remote execution |
| `required_capabilities` | Unique sorted list, at most 32 known capability IDs |

The controller constructs the immutable payload once, in the field order above with no insignificant whitespace, and persists the resulting bytes encrypted. Arrays preserve declared order; strings are not normalized. `payload_sha256` is SHA-256 of those exact UTF-8 bytes. The `prepare`/`submit` body carries `payload_b64` plus the digest; the connector verifies bytes and schema before admission. Base64 is only framing, not encryption. Reuse persisted bytes after restart instead of reserializing. Same request UUID with any different digest is a conflict even if JSON would otherwise be equivalent. Logs and public evidence contain neither bytes nor base64 payloads.

`prepare` durably records the immutable request and obtains target admission but does not start Codex. `submit` requests execution of that exact prepared receipt. Repeating either operation with the same identity/digest returns the existing receipt/state; it must not create another provider turn. After an uncertain provider call, status reconciliation—not another submit—owns progress.

### Doctor, result and errors

`doctor` returns observed `target_id`, `installation_id`, account identity (Windows SID), connector/protocol/Codex versions, `observed_at`, and separate status values for transport, trust, credentials, connector, CLI auth, GUI session, workspace and each mode. A mode includes `available`, a stable reason code, supported permission profiles and evidence type. A successful SSH command does not prove GUI readiness.

Capabilities include `visible_new`, `visible_existing`, `hidden_ephemeral`, `cancel_confirmed`, `history_pagination` and any explicit permission enforcement. Advertise only capabilities tested for the exact target version/account/session combination. Invalidate cached doctor evidence on host/account/installation/version/workspace/session change, trust rotation, authentication failure or reconnect; dispatch checks current prerequisites. An old timestamp can remain visible as historical evidence, never current authorization.

`status`/`submit` results contain request UUID/digest, verified target/account/installation, connector-owned state revision, state, observation time, provider reference when known, evidence coverage, output cursor and an optional error code. Model separately `provider_turn_complete`, `agent_reported_outcome` and `verified_action_outcome`; absence is `unknown`, not success. Sequence-numbered event pages return an opaque bounded cursor and `has_more`; cursors are scoped to the authenticated request. Duplicate/out-of-order pages must not duplicate history or notifications.

Stable errors: `invalid_request`, `unsupported_version`, `unsupported_capability`, `identity_changed`, `trust_required`, `authentication_required`, `session_unavailable`, `workspace_unavailable`, `busy`, `digest_conflict`, `audit_unavailable`, `key_unavailable`, `quota_exceeded`, `recovery_required`, `not_found`, `internal_error`. Error bodies contain `code`, `retry_class` (`never`, `after_user_action`, `status_only`) and a sanitized bounded explanation. An error never instructs a controller to mint a fresh execution UUID. Raw stderr is separately encrypted diagnostic evidence, not an error string broadcast to notifications/logs.

### Identity, authorization and workspace binding

Before a connector exists, bind trust to the explicitly reviewed SSH destination/port, host key or certificate authority policy, route and remote account. Inventory aliases are display references to that binding. Installation creates an account-owned random installation ID and maps the authenticated SSH account to its Windows SID. Record the reviewed binding of endpoint, account SID and installation ID; do not trust a connector's self-declared identity without authenticated transport.

After enrollment, admission keys use installation ID plus account SID and the relevant provider session resource. Aliases that resolve to the same binding share admission; two genuinely independent targets do not. Duplicate/cloned installation IDs, changed account SID, host-key rotation or reinstallation require explicit re-enrollment and credential-use review. A new endpoint must not inherit passwords solely by copying an alias.

Password vault entries key by verified destination/account binding. Passphrases key by the local private-key fingerprint, with independent destination-use authorization. The protected store holds values, while history holds opaque references and metadata. Never copy authentication values into wire payloads, argv, environment, telemetry or history. Unavailable/locked stores fail closed; no plaintext fallback.

A workspace ID resolves on the target to an explicitly approved account-owned directory. Resolve and validate the real path at use time, including traversal, symlink/reparse-point and ownership checks. Keep working-directory arguments separate from prompts and never evaluate either as shell source. Changing a workspace invalidates its approval/evidence.

### Request state machine and ownership

| State | Authoritative owner | Allowed next observation/effect |
| --- | --- | --- |
| `draft` | Local encrypted store | Explicit user submission creates immutable `prepared_local` |
| `prepared_local` | Controller | Durable pre-action audit, then connector `prepare`; no Codex effect yet |
| `prepared_remote` | Connector durable receipt | Revalidate identity/session, hold admission and accept exact `submit` |
| `submission_unknown` | Connector or controller after uncertain transport/provider boundary | Same-ID status/reconciliation only; retain admission |
| `running` | Connector with correlated provider evidence | Status, output/history pages or targeted cancel request |
| `cancel_requested` | Connector acknowledgment of intent | Remains active until termination/completion is actually observed |
| `completed`, `failed`, `cancelled` | Connector with correlated terminal evidence | Read-only reconciliation/history; never resubmit |
| `recovery_required` | Controller/connector with insufficient identity or evidence | Explicit recovery path under retained admission; not a terminal outcome |

Pre-provider validation failure is a rejected admission with no provider effect. `failed` is reserved for an observed failed execution, not a network timeout. A wait timeout changes the caller's waiting state, not this execution state. Loss of an acknowledgment after enqueue/start stays uncertain even if retry seems convenient. Late terminal evidence may resolve `submission_unknown` or `cancel_requested` without manufacturing intermediate events.

The connector is the only provider writer per admitted resource; new visible creation conservatively locks the target account's app-creation resource until a provider thread is bound. Existing visible work locks its account/thread. Hidden work has a separate request resource plus account concurrency limits. More than one controller obeys the same remote locks. Human app interaction is not controlled by these locks; detect unsupported/busy/conflicting turns and preserve uncertainty rather than attribute somebody else's result to this request.

Cancellation targets only the correlated request/process/provider capability. A kill request or transport disconnection does not prove completion of a process tree or rollback of external effects. If confirmed cancellation cannot be supported for a route, expose that limitation before submission. Hidden ephemeral execution cannot be resumed after termination as an invisible continuing conversation.

### Storage, history and failure recovery

The controller and connector each durably persist receipts before their respective effects. Encrypt payloads/results with authenticated encryption and fresh nonces, binding record identity as associated data. Native-protected keys are separate from SSH credentials. Protect journals, WAL, temporary files and backups; test synthetic sentinel scans. Windows connector key protection is scoped to the intended account; protected data must not depend on an unrelated elevated installation account.

History records source controller/event ID, target/account/installation, request/digest, alias/trust metadata, prompt, requested/effective mode and permissions, state transitions, result and evidence coverage. Operational receipts stay separate from later imported history. #154 imported records cannot drive execution, locks, recovery or #155 notifications. Prompt text may itself contain user-written secrets and requires explicit export consent.

Key loss reports `key_unavailable`; never generate a replacement over unreadable data. Encrypted backup restore requires the original protected key or an explicit separately protected recovery key, an exclusive writer and version checks. Restored controller identities cannot become concurrent writers. A user-requested payload deletion leaves minimal non-replay receipts; receipt expiry is forbidden while a remote request remains active or uncertain. Cleanup records what was actually deleted; OS/provider copies have their own retention and cannot be claimed erased.

If pre-action audit fails, no new remote mutation is admitted. After dispatch, block new requests but allow bounded status/targeted cancellation with already-known receipts and available authenticated transport. This exception cannot bypass trust, unavailable credentials or a lost request mapping. Report audit gaps after recovery without inventing lost evidence.

Notifications derive only from local request transitions through durable/reconstructible outbox intent. Keep requested, OS-accepted and unknown delivery distinct from remote outcomes. Closed windows are supported; stopped controllers/logged-out users are not guaranteed immediate delivery. Generic payloads, bounded deduplication/expiry, capability-aware activation and unread history belong to #155.

### Desktop bridge and visual acceptance

#151 must enumerate and exercise the pinned Wails host dispatcher, including direct bridge calls for browser URLs, clipboard, events, dialogs, file drops/downloads, window operations and every Go binding. Wrapper omission is not enforcement. Establish a host-side allowlist for only intended window/fixture/product commands, or document and resolve a framework limitation before passing the foundation gate. External navigation, arbitrary file access and shell execution are not product capabilities. Bound all native inputs and authenticate controller calls independently of UI state.

Production assets use no remote scripts/fonts/CDNs and no production development server. Apply a CSP compatible with the reviewed pinned runtime, deny remote navigation and render host-controlled text without HTML execution. Wails internal asset transport and any origin checks are part of the review. WebView localStorage, IndexedDB, caches, URLs and crash/console output must not persist decrypted history or credentials. Lock/disconnect clears transient views and discards stale responses; persisted drafts go through the encrypted controller.

Reference states: empty config, searchable host list, selected unknown host, checking, trust required/changed, credentials locked, connector absent, unsupported mode, ready, busy, running, uncertain, cancelled, failed, completed, history empty/populated and detail with partial evidence. Each is tested with synthetic text, keyboard focus, accessible names, light/dark appearance, scaling and reduced motion. Reuse licensed system-neutral icons and fonts; do not redistribute Apple-only assets. Same information structure and interactions are required across WebViews; OS chrome and rasterization differences are documented.

### Implementation gates

The dependency graph above is acyclic. #142 fixes this contract; #151 supplies executable desktop/controller evidence and #143 supplies pure inventory. Credential/audit/connector/dispatch/UI/chat/notification phases then follow their declared prerequisites. #146 canary must prove new visible creation, existing visible continuation and hidden behavior on the target client before #147 claims those modes. Source implementation may be reviewed while native evidence remains pending, but a dependent phase must not treat a failed foundation as ready.

#150 requires two authorized Windows targets and native controller evidence for all three OS families. Store private alias/account mappings outside GitHub; published evidence uses synthetic target labels. If an account/app/session is unavailable, preserve that row as unverified rather than substituting a CLI launch or a successful build. No part of this document is authorization to relax host trust, override the user's account session or silently change the requested visibility.
