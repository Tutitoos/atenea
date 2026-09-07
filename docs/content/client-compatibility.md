---
title: Client compatibility matrix
weight: 6
---

# Client compatibility matrix

`atenea doctor --client <client> --json` keeps the existing diagnostic report
and adds a versioned `compatibility` entry. `atenea doctor --all --json`
returns a deterministic matrix for Codex CLI, ChatGPT Desktop, Claude Code,
Oh My Pi and OpenCode. Statuses are independent: `pass`, `fail`, `unknown` or
`partial`.

`declared` means that the named client/profile was detected. `server_probe`
is direct, read-only evidence about the Atenea MCP server. It never upgrades
`connected`, `tested`, `presentation`, `reconnect` or `observed`; those remain
`unknown` until that specific client produces a correlated transcript and
workflow evidence. OMP and OpenCode use their own inspector slot; until those
inspectors exist their configuration is explicitly unknown and Claude files
are never read or used for remedies.

Every matrix entry records `evidence_level` and `source` as `fixture`, `real`,
`manual` or `unknown`. A fixture can never produce a real presentation claim.
The exact client identities are `Codex CLI`, `Claude Code`, `OpenCode`,
`ChatGPT Desktop` and `Oh My Pi`.

The matrix records version, date, operating system, architecture, profile,
transport, requested and observed protocol, and scope. Evidence is bounded and
redacted before JSON or Markdown rendering; client paths, credentials, headers,
prompts and tool arguments are not part of the matrix.

The pilot harness is opt-in and accepts explicit fixture clients through an
injection interface. It gives each fixture disposable HOME, CODEX_HOME and XDG
roots, explicit argv and environment overlays, and correlates handshake,
transport, protocol version, run_id and workflow.status events. It validates
activity, notices, checklist titles and evidence, the deterministic twenty
segment progress bar, cursor continuity, permissions, requested/observed
versions, invocation IDs and duplicate effects across reconnect. Presentation
is pass only when an independently attested real client render event is
present; a fixture's own self-reported presentation is ignored. The transcript
must be ordered as Markdown notice, tool request, response, activity and
post-call checklist/bar. Reconnect closes the first session, requires a new
handshake, and checks monotonic activity cursors and one replay of each
invocation.

The supported ephemeral overlays use the client contracts already implemented
by `atenea wrap`: Codex uses repeated `-c mcp_servers.<id>=...`, Claude uses
`--mcp-config <json>`, and OpenCode uses `OPENCODE_CONFIG_CONTENT`. ChatGPT
Desktop and Oh My Pi have no generic safe injection and remain manual/unknown.

The sandbox requires a pre-existing external sentinel and never creates or
modifies files in the user's profile. Its environment is allowlisted and every
writable path must remain inside the disposable roots. Missing adapters,
missing transcripts and untested surfaces remain `unknown`; they are not
reported as tested or partial successes. Root hashes are recalculated after the
run and the harness cannot prevent a client from receiving an absolute path in
an explicitly supplied argument; it only restricts the launcher environment
and reports the sentinel/root evidence.

`scripts/clientcompat-real-gates.sh` is opt-in and fails closed unless the
owner supplies each real executable, disposable root, pre-existing sentinel,
running Atenea core socket, timeout, relative transcript filename and absolute Atenea executable. It asks
`atenea compat-overlay` for the machine-readable overlay emitted by the same
`internal/wrap` renderers, writes the transcript inside the disposable
sandbox, and links only the explicitly supplied Unix socket into the isolated
XDG state root. The launcher starts from an explicit environment allowlist, so
API keys, proxy variables, SSH agent sockets and unrelated credentials are not
inherited. Timeout and normal cleanup terminate the whole client process group
before sentinel and root hashes are checked. It emits only `candidate` or `unknown`; process exit codes and text
matching never certify MCP or presentation. A controlled structured recorder
with executable identity/version/hash plus an independent observer must
promote the result. Unknown or non-zero gate results return a non-zero exit
status. ChatGPT Desktop and Oh My Pi are reported as manual/partial because
they have no safe generic injection. No real-client gate was run by `go test
./...`.

The legacy `doctor` JSON retains `client_path` for backward compatibility;
that legacy field is outside the sanitized compatibility matrix.
