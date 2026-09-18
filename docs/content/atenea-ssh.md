---
title: "Atenea SSH: local control window"
description: "Reviewed delivery contract for epic #141 and documentation issue #142; implementation pending."
weight: 43
---

# Atenea SSH phased delivery

Tracking epic. Child issues are the implementation units; all remain open. The local documentation draft is not published or validated as runtime behavior.

## Phases and dependencies

- [ ] [#142](https://github.com/Tutitoos/atenea/issues/142) — Architecture, protocol and platform contract. Prerequisites: none.
- [ ] [#143](https://github.com/Tutitoos/atenea/issues/143) — Side-effect-free SSH inventory and trust diagnostics. Prerequisites: [#142](https://github.com/Tutitoos/atenea/issues/142).
- [ ] [#144](https://github.com/Tutitoos/atenea/issues/144) — Native vaults and safe authentication. Prerequisites: [#143](https://github.com/Tutitoos/atenea/issues/143).
- [ ] [#145](https://github.com/Tutitoos/atenea/issues/145) — Encrypted history, keys and recovery receipts. Prerequisites: [#144](https://github.com/Tutitoos/atenea/issues/144).
- [ ] [#146](https://github.com/Tutitoos/atenea/issues/146) — Windows connector, installer and early compatibility canary. Prerequisites: [#142](https://github.com/Tutitoos/atenea/issues/142), [#143](https://github.com/Tutitoos/atenea/issues/143), [#144](https://github.com/Tutitoos/atenea/issues/144), [#145](https://github.com/Tutitoos/atenea/issues/145).
- [ ] [#147](https://github.com/Tutitoos/atenea/issues/147) — Visible/hidden execution and distributed admission. Prerequisites: [#143](https://github.com/Tutitoos/atenea/issues/143), [#144](https://github.com/Tutitoos/atenea/issues/144), [#145](https://github.com/Tutitoos/atenea/issues/145), [#146](https://github.com/Tutitoos/atenea/issues/146).
- [ ] [#148](https://github.com/Tutitoos/atenea/issues/148) — Local UI, controller packaging and Terminal launcher. Prerequisites: [#143](https://github.com/Tutitoos/atenea/issues/143), [#144](https://github.com/Tutitoos/atenea/issues/144), [#145](https://github.com/Tutitoos/atenea/issues/145), [#146](https://github.com/Tutitoos/atenea/issues/146), [#147](https://github.com/Tutitoos/atenea/issues/147).
- [ ] [#149](https://github.com/Tutitoos/atenea/issues/149) — Effectful local chat opener. Prerequisites: [#148](https://github.com/Tutitoos/atenea/issues/148).
- [ ] [#150](https://github.com/Tutitoos/atenea/issues/150) — Two-PC acceptance and controller support matrix. Prerequisites: [#142](https://github.com/Tutitoos/atenea/issues/142), [#143](https://github.com/Tutitoos/atenea/issues/143), [#144](https://github.com/Tutitoos/atenea/issues/144), [#145](https://github.com/Tutitoos/atenea/issues/145), [#146](https://github.com/Tutitoos/atenea/issues/146), [#147](https://github.com/Tutitoos/atenea/issues/147), [#148](https://github.com/Tutitoos/atenea/issues/148), [#149](https://github.com/Tutitoos/atenea/issues/149).

Follow the declared dependencies for delivery. [#146](https://github.com/Tutitoos/atenea/issues/146) now depends on inventory, credentials and audit because installer mutations need those controls. [#147](https://github.com/Tutitoos/atenea/issues/147) waits for the early Windows compatibility results.

## Product contract

Atenea SSH opens the same local control UI from `atenea-ssh` in Terminal or an installed local chat integration. It lists concrete SSH aliases, lets the user select and explicitly connect/check one host, shows connector/Codex state, stores SSH passwords and key passphrases in native vaults, sends a prompt with visible/hidden and permission choices, and exposes logs and encrypted action history.

- **Platforms:** controller OS and remote target OS are separate. macOS, Windows and Linux vault/controller rows need their own implementation and native validation; the current Atenea Unix IPC and service code do not imply a working Windows controller. The first remote connector is Windows. Other SSH targets stay listed with unsupported connector state. Missing SSH/network/Codex prerequisites require local bootstrap; the connector cannot install SSH on an unreachable PC.
- **SSH config and trust:** list parsing is side-effect free. `ssh -G` can execute Match exec, so it must never render the list. Selected-host resolution handles executable config/proxies explicitly; keep supported OpenSSH semantics and refuse unsupported cases. Unknown and changed host keys require a reviewed trust flow; never auto-accept them. Aliases/IPs are labels, not durable device identity.
- **Credentials:** passwords bind to verified destination/account; passphrases bind to the local private-key identity with separate per-destination use authorization. Reuse SSH agents, handle native vault failures, and protect the askpass/IPC exchange. Secret values from the vault never enter args/environment/history/logs. Metadata-only credential events are auditable. Windows controller readiness is not established by a Credential Manager adapter alone.
- **Visibility:** visible is default and must support NEW and EXISTING app conversations through target-version-verified paths. The prototype queue only covers an existing thread; [#146](https://github.com/Tutitoos/atenea/issues/146) performs the early canary. Hidden uses `codex exec --ephemeral` and must be shown absent from the target app; --ephemeral help alone is not visual proof. Hidden tasks remain in Atenea history and may cause ordinary OS effects; they are not invisible activity or a provider-retention guarantee. An ephemeral task is not resumed or silently converted to visible; required app-only capabilities are refused.
- **Permissions and outcomes:** visibility does not grant permission. Existing app permissions are inherited unless enforcement is proven. Turn completion, agent-reported success and independently verified action results are distinct. Tool-by-tool history is partial/unavailable unless the provider exposes it.
- **Recovery:** both controller and connector have durable UUID/digest receipts. Locks use stable target/account/session resources, including duplicate aliases and multiple controllers. Ambiguous receipt/start/enqueue/ack windows remain pending and retain locks until reconciled. Connector locks do not lock out the human using the desktop. No automatic new UUID or blind replay; do not promise exactly-once arbitrary side effects. Page through remote history instead of only the last ten turns. Wait expiry and cancellation request are not remote termination.
- **History/privacy:** encrypt full prompts/results locally, protect remote connector payloads and temporary files too, and disclose the provider's own transcript retention. Record connection/trust/credential metadata/install/dispatch/cancel/cleanup events and evidence coverage. Vault-sourced secrets must not enter audit payloads; user prompt text is retained encrypted and may itself be sensitive. Key loss, backup, deletion, export and receipt retention need explicit behavior.
- **Audit availability:** a failed pre-action receipt blocks new mutations. Failure after dispatch stops new work but must not block same-ID status or targeted cancellation. Reconcile audit gaps after recovery without inventing evidence.
- **Desktop window:** use a Wails v2 desktop shell with one shared React/TypeScript interface on macOS, Windows and Linux. Recreate the SwiftUI-style layout and interactions with shared components and design tokens; this is not the SwiftUI framework. Native window chrome, OS shortcuts, dialogs and credential prompts may differ. The Go controller remains independent of the window so closing it does not stop remote work. Limit the WebView-to-Go command surface, authenticate the app-to-controller IPC for the local OS user, restrict navigation and remote content, apply CSP and input bounds, and keep full prompts/actions off the published dashboard. Any optional browser/HTTP surface needs its own authenticated bootstrap, Host/Origin and CSRF controls. Prove rendered parity and packaging separately on each claimed controller OS.
- **Chat entry:** an effectful typed opener with explicit installation/discovery, not an unannotated mutation in the existing read-only atenea.command surface. A process-launch or app-activation acknowledgment is not window-rendering proof.

## Completion criteria

All nine child issues must meet their own acceptance gates. [#150](https://github.com/Tutitoos/atenea/issues/150) repeats the full product flow on two real Windows PCs and separately reports each claimed controller platform. Required unobserved behavior remains open, unless the user explicitly changes scope. Individual source PRs may merge with accurately bounded evidence; this does not by itself complete the epic.

## Review evidence and delivery

The review reproduced Match exec during ssh -G using a temporary local fixture; see [OpenSSH configuration](https://man.openbsd.org/ssh_config). Local Codex CLI help confirms queue targets existing sessions and --ephemeral avoids persisted session files, neither proving new app-owned creation nor target app invisibility. Repository `internal/ipc/ipc.go`, `internal/platform/service_other.go` and `internal/core/command.go` establish the platform and read-only-command constraints.

Every code/documentation phase owns a branch/PR and its required checks. The final acceptance issue can close with reviewed operational evidence; it need not invent a code PR. No direct Closes [#141](https://github.com/Tutitoos/atenea/issues/141) implementation PR. No runtime changes, PC installs or full-feature completion are implied by these planning corrections.

## Out of scope

Restoring the retired native desktop-agent coordinator, an arbitrary remote shell UI, moving ChatGPT account credentials between PCs, automatic fleet installation, or claiming exact external side-effect execution from receipts.
