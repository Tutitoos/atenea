---
title: "Chat commands"
weight: 35
---

# `/atenea` chat commands

Atenea exposes one closed, read-only command surface for desktop chat clients.
The adapter translates the shortcut to the typed MCP tool `atenea.command`; it
does not execute shell input, forward arbitrary flags or invoke Computer Use.

## Usage

Claude Code gets the literal `/atenea` alias after running
`atenea desktop install claude --profile claude`. Claude Desktop, ChatGPT and
Codex receive the same commands through MCP Prompts, normally shown by the
client as `/mcp__atenea__<command>` or in its prompt/command picker.

```text
/atenea help
/atenea status
/atenea metrics --capability code.search
/atenea traces --open --limit 20
/atenea catalog
/atenea doctor --client claude --profile claude
/atenea detect --repository current
/atenea incidents
/atenea floor
/atenea config
/atenea intent --repository current
```

Markdown is the default response format. Headings, lists and tables are
returned unchanged so Claude Code, Claude Desktop, ChatGPT and other
compatible clients render a readable answer. Integrations that need stable
fields can request JSON:

```json
{"name":"metrics","format":"json","capability":"code.search"}
```

## Available commands

| Command | Purpose |
|---|---|
| `help` | Show available read-only commands |
| `status` | Show service health, providers, repositories and client permissions |
| `metrics` | Show measured capability/provider rows and filters |
| `traces` | Show execution traces; supports id, type, verdict, open, since and limit |
| `catalog` | Show registered capabilities and repositories |
| `doctor` | Show compatibility telemetry for a client/profile |
| `detect` | Probe declared providers and indexes |
| `incidents` | Read the crash notebook without marking incidents read |
| `floor` | Show measured turn-start costs |
| `config` | Show a redacted effective settings summary |
| `intent` | Show safe client declarations for one registered repository |

The aliases `health` and `providers` map to `status` and `catalog`.

## Safety boundary

The command surface is intentionally read-only. Tasks, workflows,
configuration changes and desktop interaction are not available through it.
Those operations keep their existing confirmation, effect, allow-list and
audit policy. `atenea.command` has a closed enum of command names, rejects
unknown fields and never passes a command line to a shell.

Responses redact secrets and do not include entered text, screenshots or other
sensitive visual content. The Markdown renderer escapes table content supplied
by providers or repositories.

## Client boundary

Atenea centralizes the MCP route, but it does not intercept native client
tools such as Bash, Read, built-in Computer Use or each client's automation.
The `/atenea` skill tells Claude Code to use only `atenea.command`; it cannot
remove unrelated native tools from a client session.
