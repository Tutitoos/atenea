---
title: Upstream issue drafts
weight: 8
---

# Upstream issue drafts

Defects found in tools this project depends on, written up here **and not filed
anywhere**. A draft leaves this page when it is opened upstream, with the link
replacing the body — never by being deleted. Each one states the reproducer on
this machine, so a later reader can re-run it rather than trust the summary.

## headroom — `inject_tool_search_deferral` can defer every real tool

**Status:** draft, not filed. Found 2026-08-13 against headroom 0.34.0.

**Summary.** The docstring of `inject_tool_search_deferral`
(`headroom/proxy/helpers.py:2287`) states the invariant:

> Invariants enforced (else Anthropic 400s): the search tool is never deferred;
> **at least one tool stays non-deferred**; a deferred tool never carries
> `cache_control`.

The code enforces the first and the third. It does not enforce the second. The
loop keeps a tool resident only when it is a non-dict, a typed/server tool, or
its lowercased name is in `_TOOL_SEARCH_CORE_TOOLS` (`bash`, `read`, `write`,
`edit`, `multiedit`, `apply_patch`, `glob`, …). A client whose tool names miss
that list entirely has **every** tool deferred, and the only entry left resident
is the injected `tool_search_tool_regex` stub — which is not a tool the model can
do any work with.

**Reproducer.** omp (v17.3.0) names its tools with a leading underscore:
`_read`, `_bash`, `_edit`, `_glob`, `_grep`, `_write`, `_eval`, `_task`, `_hub`,
`_todo`, `computer`, `web_search`. `_read` is not `read`, so the exemption misses
all twelve. Running the shipped function against a captured omp request body:

```
omp:         12 tools -> resident 1 (tool_search_tool_regex), deferred 12
Claude Code: 77 tools -> resident 7 (stub + Bash, Edit, Read, Skill,
                                     WebFetch, Write), deferred 71
```

**Observed cost, measured not inferred.** Fixed prompt requiring two file reads,
five runs per path, warm:

| omp | tool executions | uncached `input` per exchange | cheapest warm run |
|---|---|---|---|
| direct to Anthropic | 2 (5/5) | 4–6 | $0.050359 |
| through headroom | 1 (5/5) | 9,488 (5/5) | $0.078518 |

Two effects, both reproducible. The model must run a server-side tool search
before it can act, which changes behaviour — one tool execution instead of two,
every run. And what the search returns lands outside the client's cached prefix:
omp writes a single `cache_control: ephemeral` on the final user message and none
on tools or system, so 9,488 tokens are billed as fresh input on every
tool-using exchange. A no-tool control on the same client, same proxy, same day
bills **2**. Claude Code is unaffected — it carries two breakpoints inside
`system[]` and keeps six real tools resident.

**Suggested fix.** Either enforce the documented invariant (if no real tool would
remain resident, return `tools` unchanged — the same "don't perturb the cache
prefix" bail-out already used when `deferred == 0`), or normalise names before
the core comparison so a leading `_` does not defeat it. The function already
accepts a `core_tools` parameter; the call site in
`proxy/handlers/anthropic.py:2429` passes no override, so there is no way to
correct this from configuration. The only available switch,
`HEADROOM_TOOL_SEARCH=0`, is global and removes the feature's benefit for clients
where it works well.

**Note for whoever files it.** `HEADROOM_TOOL_SEARCH` does not appear in the
proxy's `/proc/<pid>/environ`, in any config file, or in the systemd unit; it is
seeded at startup from the `coding` savings profile via
`seed_proxy_env_defaults`. Anyone reproducing this will conclude the feature is
off unless they check `transforms_applied` on a live request instead.
