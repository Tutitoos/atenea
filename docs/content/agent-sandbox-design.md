# Agent sandbox design — implementation deferred

Status: design deliverable for audit R03. This document does not claim that Atenea currently provides an OS sandbox. Current shipped-reader checks and dispatch grants are implemented separately.

## Security objective and trust boundary

Contain an untrusted agent process and its children even when they ignore the assignment. It must not read host secrets, modify files outside explicitly writable roots, create unapproved network connections, attach to other host processes, or bypass the workflow budget through Atenea's broker. Host kernel, sandbox launcher, broker and configured provider services remain trusted. Do not describe a compromised host/root account as contained.

## Selected architecture

| Platform | Execution boundary | Reason and compatibility |
|---|---|---|
| Linux | Rootless namespaces using bubblewrap, read-only runtime image, seccomp and cgroup v2 | Native Linux CLI compatibility, filesystem/process/network separation without privileged containers. Missing user namespaces or cgroup delegation is an explicit unsupported configuration. |
| macOS | Persistent Linux VM using Virtualization.framework, with a per-run Linux sandbox inside | Stronger maintained boundary than a new dependency on deprecated sandbox-exec profiles. Requires Linux-compatible agent runtimes and a signed native VM launcher. Native macOS-only executables require an explicitly trusted host profile. |

Rejected as the security boundary: prompt instructions, result filtering, Docker with the host socket mounted, directory checks alone, and automatic fallback to unrestricted host execution. The macOS VM is a separate future implementation requiring packaging and lifecycle work, not a shell wrapper shipped by this audit.

## Profiles and mounts

* `read`: immutable workspace snapshot, runtime and explicitly granted files mounted read-only. Private temporary directory and per-run HOME; no host home, credential stores, SSH sockets or Docker sockets.
* `workspace-write`: the read profile plus a writable isolated worktree. A broker exports reviewed changes after exit; it rejects escaping links, device files and writes to Git configuration/hooks. Never mount the main checkout writable by default.
* `external`: additive permission for brokered network access. No raw guest egress; the network namespace has only the broker endpoint. Explicit domains, ports and protocols are part of the approved manifest.
* `trusted-host`: explicit compatibility mode for native tools. It is labeled unsandboxed in receipts and cannot satisfy an enforced-sandbox request.

The read profile's named files are a ceiling, not merely search hints. Runtime dependencies must be declared as immutable mounts rather than discovered by recursively sharing the host filesystem. Symlinks cannot widen a mount grant. CPU, memory, output, process count, disk quota and duration are bounded per run.

## Broker and protocol

The launcher receives a versioned manifest containing run/parent IDs, exact effects, read/write mount grants, network allowlist, resource limits and the reservation ID. The manifest digest is bound to the approved workflow step. The broker derives authority from server-side records, not paths or claims supplied by the child.

Use a private inherited file descriptor or VM vsock channel authenticated with a per-run random capability. Tokens expire at run completion and cannot be shared with unrelated runs. Deny unknown manifest versions and unsupported enforcement features before spawning.

Provider credentials stay in the host broker where the provider supports proxying. A CLI that requires raw secrets must use a dedicated narrowly scoped credential and a compatible profile; never forward the complete host environment. Retain only the session data necessary for that run in an isolated, permission-controlled store.

All network traffic passes a policy proxy. Resolve and validate every destination and redirect, pin the checked address for the connection, deny loopback/private/link-local/metadata destinations by default, and validate SNI/Host together. Prevent direct DNS, UDP/QUIC and alternate-proxy bypasses. Browser subresources follow the same network boundary. Explicit private service access is a separate grant.

Host-side MCP/desktop capabilities remain mediated operations: the broker checks effect, repository, tool and budget on every call. Guest isolation must not grant access to a raw host MCP socket that bypasses those checks. Requests that cause effects are not automatically replayed after reconnect.

## Lifecycle and accounting

Reserve budget before creating a sandbox. Spawn only after mount and egress policy installation succeeds. Cancellation kills the sandbox process group/cgroup; the broker drains already received usage, marks unreported spend unknown and revokes the run token. No descendant survives cleanup.

On crash/restart, reconstruct state from the reservation and sandbox manifest. Keep outstanding monetary holds conservative until reconciled. Reap orphaned guests/worktrees, without treating missing reports as zero cost. Emit denial codes, runtime profile and manifest digest in receipts; do not log arguments, credentials or full responses.

## Compatibility and future rollout

Future configuration: `sandbox.mode = "off" | "enforced"`, initially `off` for compatibility, with an explicit per-agent profile. An enforced assignment fails closed on an unsupported platform/runtime; it never silently switches to trusted-host. No migration turns existing agents into sandboxed agents without compatibility validation.

Linux support is implemented and tested first; macOS VM support follows with the same broker contract. Both must pass the same conformance suite before the feature is described as cross-platform. Runtime images, bubblewrap and VM kernel/rootfs versions are pinned and updated through the normal release review.

## Required acceptance matrix for the later implementation

1. Reject `..`, absolute escapes, intermediate symlinks, symlink swaps, hard-link export tricks and procfs magic links.
2. Block host HOME, keychains, SSH agent, Docker socket, Git hooks/config writes and undeclared repository access.
3. Block descendants escaping the cgroup, ptrace against host processes, daemonization and execution after cancellation.
4. Block direct IP/private DNS, rebinding, redirects, proxy environment overrides, DNS/UDP/QUIC tunnels and browser subresources outside the grant.
5. Reject forged/expired tokens, altered manifests, cross-run socket reuse and broker calls exceeding permissions or budget.
6. Verify mount cleanup, crash recovery, independent concurrent runs, output/memory/disk exhaustion and preservation of observed charges.
7. Run read-only and writable fixture agents, supported Node/Bun/Python/Go CLIs, and brokered MCP calls on Linux and macOS VM.
8. Demonstrate explicit refusal for unsupported native tools and absent runtime features. No test may pass by disabling enforcement.

Deliverables for that later phase: launcher/broker implementation, pinned runtime build, installation/rollback procedure, cross-platform escape tests, compatibility inventory and measured startup/resource overhead. No sandbox runtime is installed by the current audit remediation.
