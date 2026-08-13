---
title: Context windows
weight: 9
---

# Context windows on this machine

Every number here was measured on 2026-08-13, not read from documentation. The
ceilings came off `api.anthropic.com` directly; the thresholds were read back
out of the running clients after the settings were applied.

## The ceilings, measured

| client | path | ceiling | how it was established |
| --- | --- | --- | --- |
| omp | direct, `anthropic/claude-opus-5` | **1,000,000** | `400 prompt is too long: 1031017 tokens > 1000000 maximum` (`req_011Ce19r1XCkKpx691n7wr4E`); 340,019 accepted below it |
| Claude Code | `claude-opus-5[1m]` | **1,000,000** | model tagged `[1m]`, beta sent on 2,972 of 2,972 wire samples |
| OpenCode → opus | meridian alias `opus[1m]` | **1,000,000** | 359,513 tokens accepted in one turn |
| OpenCode → sonnet | meridian alias `sonnet`, no tag | **200,000**, blocking at ~177,000 | the same 215k input returned `ContextOverflowError: Prompt is too long` in 4.2 s |

**The `context-1m-2025-08-07` beta does not gate this account's API limit.** omp
sends it never — zero occurrences in the binary, zero of nineteen requests on
the wire — and still reaches 1M. What the header gates is a client's own window
arithmetic, which decides when that client compacts or refuses locally. Two
earlier statements in this repo assumed the beta was the API's gate and put
omp's ceiling at 200,000; the ladder above disproves them. See the ninth
instrument in *When the instrument is the bug*.

## The thresholds, and what each one means

Two numbers of the same kind with opposite meanings. That distinction is the
reason this page exists.

| client | value | key | ceiling it faces | what the number is |
| --- | --- | --- | --- | --- |
| Claude Code | 480,000 → trigger **447,000** | `autoCompactWindow`, `~/.claude/settings.json` | 1,000,000, real | **a chosen cap.** Nothing forces it. It halves a working window on purpose, to hold cost and prompt size down |
| omp | 300,000 | `compaction.thresholdTokens`, `~/.omp/agent/config.yml` | 1,000,000, measured | **a chosen cap**, left where it was. Raising it to 500,000 was considered and refused: it buys fewer compactions and pays cache re-read on every turn, forever, against a wall nothing was hitting |
| OpenCode | 150,000 | `provider.anthropic.models.claude-sonnet-5.limit.input` = 170,000 minus `compaction.reserved` = 20,000, `~/.config/opencode/opencode.json` | ~177,000, real | **a guardrail.** Without it the client compacts at 968,000 against a path that dies at 177,000 — the number exists to keep a failure from being reachable |

A cap you can move whenever you like; a guardrail you can only move by first
moving the wall behind it. Written down together because they look identical in
a config file and are not the same decision.

Claude Code's arithmetic: effective window `480,000 − 20,000` reserve
`= 460,000`, trigger `460,000 − 13,000 = 447,000`. Confirmed from the running
client rather than on paper — with `CLAUDE_CODE_REMOTE=1` the CLI emits its
resolved state:

```json
{"enabled": true, "effective_window": 460000, "threshold": 447000,
 "enforced": true, "source": "settings"}
```

`source: "settings"` is the part that matters: the key in force is the one in
`~/.claude/settings.json`, not the model table and not an environment override.

OpenCode's arithmetic comes from `Is()` in the binary:
`limit.input ? limit.input − reserved : limit.context − min(limit.output, 32000)`.
`compaction.reserved` is consulted **only** on the `limit.input` branch, so
setting it alone against a catalog model that has no `input` key changes
nothing — the two edits only work as a pair. Confirmed live from a fresh server
on port 4099:

```
claude-sonnet-5  limit={'context': 200000, 'input': 170000, 'output': 32000}
claude-opus-5    limit={'context': 1000000, 'output': 128000}
```

and then behaviourally: a sonnet session driven to 150,921 tokens summarised
itself and resumed at 27,670. It compacted where it was told to, below the
177,000 that used to end the session with an overflow.

## What is not fixed

The alternative to the guardrail was `MERIDIAN_SONNET_MODEL=sonnet[1m]`, which
gives OpenCode's sonnet a real 1M window instead of a low threshold. It was
refused for one reason: `mapModelToClaudeModel` forces the non-1m alias when
`agentMode === "subagent"`, so subagent runs would keep the 200,000 window while
the client believed 1,000,000 — the same failure, alive in the one place nobody
watches. Preferring the visible limit to the invisible one is the whole choice.
