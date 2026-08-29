# Atenea with Claude Code and ChatGPT Desktop

Atenea is the MCP control plane for desktop clients. The recommended profiles
expose one MCP server, `atenea`, and keep declared, governed, and raw backends
behind that server.

## First start

For ChatGPT Desktop and Codex, install the native MCP entry in the shared Codex
configuration and restart the desktop app:

```text
atenea desktop install chatgpt --profile chatgpt --launch
atenea doctor --client chatgpt
```

For Claude Code, use an ephemeral profile-scoped configuration:

```text
atenea wrap claude --profile claude
atenea doctor --client claude
```

`desktop install` changes only the Atenea-managed block in
`~/.codex/config.toml`. It creates a timestamped backup before a change and
refuses to replace an unrelated `mcp_servers.atenea` entry unless
`--replace` is provided.

## Profiles

The built-in profiles are:

| Profile | Clients | Policy |
| --- | --- | --- |
| `claude` | Claude Code | Atenea only, strict MCP config when supported |
| `chatgpt` | ChatGPT Desktop and Codex CLI | Atenea only |
| `shared` | OpenCode, OMP, and explicit hybrid use | Atenea plus allowed `expose = "on"` MCPs |

Profiles can be overridden in `atenea.toml` with `[[desktop_profile]]`. Use
`direct_mcp` only for explicitly approved `expose = "on"` servers. An MCP with
`expose = "raw"` is never connected directly by a desktop profile.

## MCP contract

The Atenea MCP advertises capabilities first and raw passthrough tools after
them. Tool filters are enforced both when listing tools and when receiving a
call. Unknown tool names return a diagnostic tool result in the default
`fallback = "diagnostic"` mode, so a client session can recover without an
implicit tool substitution.

Supported compatibility normalizations are intentionally small: missing or
null arguments become `{}`, `input` is accepted as an argument alias, and
`raw/<server>/<tool>` is accepted as an alias for `raw.<server>.<tool>`.

## Compatibility matrix

| Capability | Claude Code | ChatGPT Desktop | Codex CLI |
| --- | --- | --- | --- |
| Atenea stdio MCP | Native | Native | Native |
| Declared and raw backends | Atenea bridge | Atenea bridge | Atenea bridge |
| Direct `expose = "on"` MCP | `shared` or `hybrid` | `shared` or `hybrid` | `shared` or `hybrid` |
| Tool allow/deny lists | Atenea | Atenea and Codex config | Atenea and Codex config |
| Headless Codex adapter | Not applicable | Fallback | Available |
| Compatibility events | JSONL | JSONL | JSONL |

## Five-step diagnosis

1. Confirm the client binary or desktop installation and its version.
2. Run `atenea doctor --client <client> --profile <profile>`.
3. Confirm `initialize` and `tools/list` complete successfully.
4. Check profile filters, permissions, timeouts, and `raw` declarations.
5. Execute one read-only tool and inspect Atenea's compatibility JSONL event.

Compatibility events are stored in Atenea's state directory. They include the
client, version, profile, tool, outcome, latency, and fallback status, but never
tool arguments, results, prompts, tokens, headers, or environment values.
