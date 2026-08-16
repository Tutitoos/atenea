---
title: When the instrument is the bug
weight: 6
---

# When the instrument is the bug

Three defects were reported against this project on 2026-08-09, with numbers
attached to each. All three were confidently written, none of them existed, and
every one came from the same measurement taken through an instrument nobody had
checked.

This page is the write-up. The three findings are worth knowing because two of
them look exactly like real bugs. The shape is worth knowing better, because the
next one will wear a different name and the numbers will look just as solid.

## The three claims

Six repositories had just been declared, Serena's `lifecycle` was under review,
and the question was whether `persistent` earned its resident cost. The
measurements were taken with `atenea ask`, run from a shell, timed with a clock.
They produced this:

1. **`persistent` instances are never used.** With the supervisor holding a
   Serena alive for a repository, a call was observed spawning a *second*
   throwaway server, using it, and killing it. Two point six gigabytes of
   processes, watched, kept warm and never dialed.
2. **`idle_timeout` has no observable effect.** Three consecutive calls each
   paid a full cold start. Nothing survived between them, so the knob described
   in `settings.md` was configuring nothing — the exact failure that document
   forbids by name.
3. **The break-in rotation never converges.** In a newly declared repository the
   turn went to a model-backed text search that hit its spending ceiling and
   returned `permission_denied`. A failed attempt records no measurement, so the
   turn came round again. Cost: about thirty cents each, forever.

Each was reported with a trace, a process id or a wall-clock figure beside it.

## What was actually true

**The third fell first, and it fell to reading the code instead of the clock.**
The ceiling exists: `BreakInAttempts = 4` in `internal/selector`, with `Barren`
meaning zero samples across at least that many attempts, and four tests
defending it — one of which documents this very machine. The rotation had been
observed at its second attempt of four and called infinite. Nothing was broken;
the measurement had stopped halfway and drawn a line through one point.

**The first two fell together, to a process id.** The "throwaway" server had a
parent, and the parent was neither the supervised Serena nor the service. It was
the measuring command itself.

`atenea ask` does not talk to the running service. It calls `load(settingsPath)`
and builds its own core, its own supervisor and its own Serena, answers the
question, and takes all of it down on the way out. That is documented behaviour,
stated plainly in `architecture.md`: *"Every other command works beside it
untouched."* Only `atenea mcp` relays to the service, which is why it is the
command a client is configured with.

So every number in claims 1 and 2 was real, and every one of them was measured
against a process the setting under test never governed. Re-measured through
`atenea mcp`, with the same six repositories:

| | idle | first call | subsequent |
| --- | --- | --- | --- |
| `persistent` | 25 processes, 2722 MiB | 2.07s | 0.38–0.40s |
| `on_demand` | 1 process, 54 MiB | 3.20s | 0.38–0.41s |

The supervised instance is used, and using it is nine times faster than the
3.6s the command line had been reporting. `idle_timeout` holds the server
between calls and the reaper stops it about a minute after the window expires —
watched, nine processes down to four, the remaining four being the raw MCP
servers rather than Serena.

The recommendation that came out of the bad measurement — `on_demand` — survived
the good one. That is the part worth being uncomfortable about. A conclusion
that happens to be right is not evidence that the road to it was.

## Why it was convincing

Nothing here was sloppy in the ordinary sense. The processes were real, the
timings were real, the traces were real, and two independent measurements agreed
with each other. They agreed because they shared a mistake, which is what makes
this failure mode expensive: **corroboration between two readings from the same
instrument is not corroboration at all.**

The tell was available from the first minute and went unread. `atenea status`
prints a `process` line saying whether you are reading the service or working it
out from disk. The question "which process am I actually measuring" was
answerable at any point by looking at a parent pid — the same one that eventually
collapsed the whole report.

## The fourth one, caught in time

There was nearly a fourth. Once the three were withdrawn, the break-in rotation
was allowed to finish and paid for: fifteen dispatches across four repositories,
until the model-backed search had either earned its two measurements or spent
four attempts earning nothing. Six of those fifteen were waste, and worth owning
here — the loop driving them stopped on "something other than the model was
chosen" rather than on "the rotation is over", so it kept paying after break-in
had ended and health had taken the wheel. The rotation's own price was nine
dispatches; the other six bought a lesson about stop conditions. Immediately
afterwards the funnel was picking that expensive search on two repositories —
and not on the break-in turn, which was over, but on **health**.

The mechanism was easy to find and looked damning. `SuccessWindow` is one hour:
an implementation whose last success here is older than that stops reading
`alive` and reads `unknown`, and the promotion that lifts `unknown` back up is
gated on `inBreakIn`, so it protects a newcomer and not a veteran. Two providers
both working, separated by nothing but which of them ran more recently, with the
winner refreshing itself on every dispatch and the loser ageing out. Written up
that way it is a starvation loop, and the write-up was half drafted.

It was labelled a prediction instead, because the claim it rested on — that the
state was stable — had not been observed, only reasoned. The prediction was that
with no further dispatches both sides would age out, health would stop
separating them and cost would take over. Fifty-five minutes later, that is
exactly what happened: both `unknown`, and every one of the four repositories
choosing `ripgrep`, three of them reading `cheapest of the healthy ones
(measured)` and the fourth naming the four barren attempts outright.

The recency advantage had not been the system's behaviour. It had been the
fifteen dispatches, fired minutes earlier, by the person then measuring the
result. The same error as the first three, one layer up: **a system you have
just perturbed is not a system you are observing.** The only thing that caught
it was refusing to write "is" where the evidence only supported "should be".

## A second instrument, found the same week

`atenea` on this machine is a copy at `~/.local/bin/atenea`, and the user
service runs that same path: `ExecStart=/home/tutitoos/.local/bin/atenea run`.

A running process holds the inode it was launched from, not the path. Reinstall
over that path while the service is up and nothing fails, nothing is logged,
`systemctl status` stays green and the unit is never told — but the service and
the CLI you type are now two different builds:

```
~/.local/bin/atenea                    inode 7015723
/proc/<pid>/maps, executable mapping   inode 7024105
```

Those two are a real drift, measured on 2026-08-10: a build copied over the
live path while the service stayed up.

Both answer `atenea version` with the same string, because the version is
stamped at link time and both came from the same tree. Nothing on any screen
separates them. Everything measured through the CLI then describes one build,
everything the service does describes the other, and a fix confirmed in one can
be entirely absent from the other for as long as nobody restarts.

This is the same shape as the three defects above, one layer lower: the earlier
ones measured the wrong *process*, this one measures the wrong *build* of the
right process.

**And the check first published here was itself another one, in the document
arguing against it.** It compared `stat -Lc %i` on the path against
`stat -Lc %i` on
`/proc/<pid>/exe`, which cannot be equal on any Linux: that magic symlink is
resolved inside procfs, so `stat` answers with procfs's own device and inode
numbers rather than the file's. Measured on 2026-08-10, `/proc/self/exe` and
the very same `bash` binary named by its path report `dev=26 inode=71303137`
and `dev=64513 inode=4980754` — one file, two numbers. The check therefore
said "stale" every time it was run, including on a service started seconds
earlier, and the two numbers printed as evidence above this paragraph in the
first version of this page were that artefact rather than a real drift. A check
that always fires carries the same information as a check that never does.

The inode the kernel actually mapped is in `/proc/<pid>/maps`, beside the
device and the path, and that one compares — with two details that both bit the
first attempt at writing it:

```sh
pid=$(systemctl --user show atenea -p MainPID --value)
running=$(awk '$6 ~ /\/atenea$/ {print $5; exit}' /proc/$pid/maps)
[ -n "$running" ] || { echo "cannot read the mapping for pid $pid"; exit 1; }
[ "$running" = "$(stat -c %i ~/.local/bin/atenea)" ] \
  || echo "the service is running a build that is no longer at that path"
```

The match is on the sixth *field*, not the whole line, because the moment the
file is replaced the kernel renders that mapping as
`/home/tutitoos/.local/bin/atenea (deleted)` — a pattern anchored with `$` on
the line stops matching in exactly the case the check exists to catch. And the
empty answer is refused out loud, because a shell comparison against an empty
string is not a measurement: the version anchored on the line printed the
warning for the drift, correctly, while having read nothing at all.

All three outcomes were measured on 2026-08-10 before this was written: equal
(`7015723 = 7015723`) on a service restarted after the install, unequal
(`7024105` running against `7015723` on disk) after copying a different build
over the live path, and the guard firing on a pid with no such mapping.
`readlink /proc/<pid>/exe` is the other honest reading — it appends
` (deleted)` once the inode has no name left — but it stays silent for the case
that matters here, where the path exists and holds something else.

Note what this means for identical bytes: installing the same artefact twice
creates a new inode, so the check reports a drift the moment you reinstall, even
when nothing changed. That is the correct answer, not a false alarm. The process
is holding the old file, and the only thing that makes the two agree is a
restart.

## A third instrument: a renderer that draws nothing and says nothing

Measured on 2026-08-10, building Atenea's status line as a TUI plugin for
opencode 1.18.16, whose renderer is `@opentui/solid`.

The line would not appear. The first hypothesis was the obvious one — the plugin
was not being loaded — and it was wrong, in the way this page keeps being about:
it was a hypothesis about the instrument's subject when the instrument was the
problem.

Three readings settled it, none of them the screen:

1. `~/.local/state/opencode/plugin-meta.json` carries a `load_count` per plugin
   id. It incremented on the run in question. The module was imported.
2. A write from the slot callback, one from the component body, and one from
   `onMount` all landed, within 6 ms of each other. The slot was registered, the
   component was constructed, and it mounted.
3. The terminal's own bytes, captured with `script` into a file and searched
   there, contained no trace of the line.

So the plugin loaded, registered, ran, mounted — and drew nothing. The cause was
a `<Show>` from `solid-js` wrapping the subtree. Both of its forms behave the
same way here: the function-child form, `{(value) => <box .../>}`, and plain
children under a truthy `when`. In each case the whole subtree is absent from the
frame, with no error, no log line, and no warning at `--log-level DEBUG`.

An earlier version of the same file had a second instance of the shape: a
`<span>` placed as a direct child of `<box>` instead of inside a `<text>`. Also
dropped, also silently. Two different mistakes, one indistinguishable symptom.

**This is why that symptom is worth writing down rather than filing.** A plugin
that fails to load produces exactly the same nothing — that failure is
[opencode#41574](https://github.com/anomalyco/opencode/issues/41574), filed the
same day from this repository, after a probe proved a module throwing at import
time yields no log, no toast, no stderr and no entry in `plugin-meta.json`. When
two unrelated faults share one appearance, the appearance stops being evidence.
What separated them was a counter that increments on load, three marks written by
the code under test, and reading the frame instead of a log.

The status line therefore contains no conditional elements at all. Its dot and
its incident counter are strings that go empty, and the element tree never
changes shape. That is not a style preference: a tree that cannot change shape
cannot lose a branch to this.

No issue is filed for the renderer yet, deliberately. There is a measurement but
not a minimal reproduction against a stock slot, and a report that cannot be run
by the person receiving it is a claim, not a finding — which is the whole subject
of this page. The day the repro exists, it gets filed.

## A fourth instrument: three samplers that reported innocence

Measured on 2026-08-10, building `mcp-writer-trap`: a watcher that names whoever
rewrites a client's MCP config, at the moment it happens. It waits on inotify
over five config files and photographs `/proc` when one is written. The subject
was a config that had been re-stamped once, by nothing reproducible.

It took four versions, and the first three all produced a log that read like a
quiet machine:

1. **It only listed processes that were still alive.** The event fires after the
   write, and a writer that finished has already left `/proc`. The log named
   every long-lived MCP server on the machine and not the one process that
   mattered. Fixed with a ring of recently-seen processes, kept for ten seconds
   past exit and marked as gone.
2. **It died on its first scan and kept the log open.** Parsing
   `/proc/<pid>/stat` splits on the last `)` to get past `comm`; a process whose
   name contains `)` raises `IndexError`, which killed the sampling thread. The
   ring stayed frozen at whatever the first pass captured — so the instrument
   answered "nobody wrote this" while not sampling at all, and answered it in the
   same format as a real negative. Fixed by never letting the loop end, and by
   printing a heartbeat every five seconds with its own pass rate and ring size.
3. **It marked as examined the processes it had failed to examine.** A pid
   sampled between `fork` and `exec` has an empty `cmdline`. That read was
   skipped, and the pid then went into the seen set, so it was never looked at
   again. The result was an instrument that named a one-second writer on one run
   and nobody on the next, with no difference between the runs. Fixed by keeping
   unresolved pids out of the seen set: a pid we could not classify has not been
   seen.

Only the third version was intermittent, which is the only reason it was caught.
The first two were consistent, and consistency reads as reliability.

What the fourth version can actually do, measured against deliberate writers:
85 passes per second, a one-second writer named 5 of 5 with its parent, a 300 ms
writer named, a 15 ms writer named 0 of 5. That last row is a property of
polling, not a bug to fix later: naming a writer shorter than a sampling tick
needs `fanotify` with `FAN_REPORT_PID`, which needs root. A real installer —
hundreds of milliseconds in interpreter startup alone — is comfortably inside
range. The heartbeat is in the log for one reason: so that a frozen sampler can
never again be read as a calm machine.

## A fifth instrument: the tracer that stopped the exchange

Measured on 2026-08-10, answering a question the whole page had been circling
from the wrong side: which endpoint does a client actually contact, and with what
model. Every previous answer had been read from configuration or inferred from a
counter. `strace -f -e trace=connect,sendto,write` answers it from the process:
the destination address of every connection, and — for anything not wrapped in
TLS — the request itself.

It worked, and it produced the two facts config could not: a request carrying its
real upstream in a header, so routing needs no inference at all; and a local
bridge on a port that the configuration file does not mention, because the file
names a long-lived listener while each run spawns its own ephemeral one.

The instrument failure was in the second attempt. The first pass truncated
payloads at 700 bytes, which showed the headers and cut the body — so the model
name, the one field the question was about, was missing. The obvious fix was a
bigger buffer, and `-s 60000` made the tracer slow enough that the traced program
never finished: a 300 s run consumed a 420 s ceiling with no answer. The setting
that made the evidence legible destroyed the event that was to be evidenced.

`-s 4000` captured the request whole — 3074 bytes, ending in its closing brace —
and left the exchange fast enough to complete. That is the entire lesson: an
observer sits inside the system it observes, and its cost is part of the
measurement. A tracer that changes an outcome is not measuring that outcome, and
the failure is quiet in the usual way — the run does not report that it was
slowed, it just times out, which reads like a broken subject rather than a
heavy-handed instrument.

One value in that session's table is still unnamed on purpose: a remote address
whose TLS `ClientHello` was not captured. It resolves, today, to the same pair of
A records as the client's own gateway names, and the certificate it serves says
so — but which name that connection asked for was not measured, so it is not
written down as though it were.

## A sixth instrument: a correct measurement of a file that was not the source

Measured on 2026-08-10, and it cost a phase of work and a widget that had already
shipped.

The question was whether a sidebar could honestly show how much of a provider's
rate-limit window is left. That turns on one thing: how often the number is
refreshed. So the refresh rate was measured, carefully and correctly. Claude's
figures live in `~/.claude.json`, rewritten by its CLI on `/status` or `/usage`:
two refreshes in twelve days, with the client itself running for 24 hours after
the last one without touching it. codex's arrive inside a session rollout,
appended only by a real model turn: readings on three days out of thirty-one. A
five-hour window is therefore live about **4 %** of the time.

From that, a design argument followed, and it was a good one. A section with a row
per provider would read "no live window" almost always, which teaches a reader to
skip that part of the screen — and then the day the weekly figure matters, they do
not see it either. So the feature was cut down to a single line that is absent most
days, the reasoning was written into the code, the docs and the changelog, and the
cut version was recorded on the not-built-yet page with the condition that would
revive it: *something refreshes those figures without a command being typed by
hand*.

Every number in that paragraph is correct. The file is refreshed exactly that
rarely. What none of them establishes is the thing the design rested on, because
`~/.claude.json` was **not the source** — it is one client's cache of it. This
machine already had another: omp keeps a usage report per provider in its own
store and refreshes each about every ten minutes on its own. Measured the next day:
readings one minute old for both providers, a median gap of one hour, six providers
covered, and the same numbers the first version was scraping out of two files by
hand — plus the window durations the first version had to infer from labels.

The correction is not "the measurement was sloppy". It is that the measurement
answered **how often does this client rewrite this file** and was reported as **how
often does this number change**. Those are different questions with the same units,
and nothing in the output distinguishes them.

**Why it was so hard to catch.** A broken sampler announces itself eventually:
numbers stop, or contradict something. This produced a long, internally consistent,
well-evidenced argument, with a real measurement under every clause, that reached
the wrong shape. Everything about it reads as rigour — which is exactly what makes
it durable: nobody re-opens a question that was answered with figures.

What did catch it, in the end, was writing the revival condition down. Stating
"something refreshes those figures without a command being typed" is a testable
claim about the machine, and it took ten minutes to discover that something already
did. Had the feature been cut without recording its condition, the wrong shape would
have survived until somebody re-proposed the section from scratch.

The cheap check, for next time: before measuring how a number behaves, ask who else
on this machine already reads it. A number worth putting on a screen is usually
worth something to another program too, and that program's copy is often fresher,
wider and already parsed.

## A seventh instrument: a ledger column that named a transport, not a client

`~/.headroom/savings_events.jsonl` carries a `client` field, and it reads like
an attribution: `opencode` 9,879 rows, `claude-code` 2,420. It is not one. The
value comes from `CLIENT_UA_MAP` in `headroom/proxy/auth_policy.py`, which
matches the *prefix* `claude-cli/` and never looks at the rest of the string.
Every harness that speaks through the Claude CLI SDK profile lands in one
bucket, whatever product it is.

That went from harmless to wrong on 2026-08-13, when omp was routed through the
proxy. omp does not spawn a `claude` child — measured, watching `/proc` across a
full run: no child appears, and the request arrives from omp's own process
wearing `claude-cli/2.1.220 (external, claude-desktop)`. Three of its requests
were filed as Claude Code before anybody looked at the column.

**The fix was already in the code, unused.** `classify_client_signals` returns
an explicit `x-client` header before it consults the UA table. Both clients
forward `ANTHROPIC_CUSTOM_HEADERS`, so one env var per shell function states the
name instead of inferring it:

```bash
omp()    { ANTHROPIC_BASE_URL=… ANTHROPIC_CUSTOM_HEADERS="x-client: omp" … }
claude() { ANTHROPIC_BASE_URL=… ANTHROPIC_CUSTOM_HEADERS="x-client: claude-code" … }
```

Verified on the wire against a loopback recorder, and then live: omp's row came
out `omp` while its user-agent still said `claude-cli/`, which is the proof that
the header decided and not the prefix. Status 200, warm cost unchanged to the
cent — `$0.013349`, billed input 26,479, `cache_hit_pct=100` — so the header sits
outside the cached prefix.

**The boundary, because a column that changed meaning needs a date.** The
`client` label in `savings_events.jsonl` is authoritative **from
2026-08-13T16:34:46Z forward, and only for `omp` and `claude-code`**. Before that
timestamp it is a transport bucket keyed on the `claude-cli/` prefix. Three rows
— `16:17:29`, `16:17:39`, `16:17:47Z` — are omp mislabelled as `claude-code`, and
are **left as they are on purpose**: the row's own fields cannot prove the
correction (`pid` is the proxy's, not the client's), and a hand-edited ledger is
worth less than a documented one. The retained `proxy.log` set, which does record
raw user-agents, only reaches back to 2026-07-30 10:20 — the 344 rows from 07-26
and 07-29 have no evidence behind them at all.

Two clients are still unnamed on the wire, each for its own reason, and neither
is an oversight:

- **codex** speaks the OpenAI protocol. `POST /v1/responses` on the proxy answers
  `401`, not `404`, so the route exists and is ChatGPT-session aware — but that
  path is annotated *always routes direct*, and codex has never been pointed at
  it here. Untried, not unsupported.
- **OpenCode** is out of the traffic path deliberately. Routing it is not a
  transport change: its `anthropic` provider is a loopback marker for an
  in-process bridge, so the only real target is the `headroom` provider already
  declared in `opencode.json` — `@ai-sdk/openai-compatible`, an API key, and
  4.6-era model names. That swaps the claude_max subscription session for
  metered billing. A provider decision wearing a transport decision's clothes.

Until either sends `x-client`, `claude-code` remains the fallback for anything
wearing that SDK profile, and the column should be read as "the Claude CLI wire",
not "the Claude Code product".

## An eighth instrument: a benchmark whose workload omitted the feature under test

omp was admitted to the headroom path on 2026-08-13 on a measured −39.6%: warm
billed input 44,019 → 26,479, cost `$0.022118` → `$0.013349`, verified twice and
byte-identical between runs. The number was correct. The prompt was *"Reply with
exactly: ok"* — a turn that calls no tools, measured against a proxy whose main
transform is deferring tool schemas. The benchmark left out the one thing the
optimisation acts on.

**On real tool work the sign flips.** Fixed prompt, two small files that must
both be read to answer, five runs per path:

| omp, warm | tool executions | uncached `input`/exchange | cheapest run |
|---|---|---|---|
| direct | 2 (5/5) | 4–6 | **$0.050359** |
| through headroom | 1 (5/5) | **9,488 (5/5)** | **$0.078518** |

A constant 9,488-token block is billed at full input price on every exchange
through the proxy, against 4–6 direct; the proxy's own log shows several of those
calls at `cache_read=0 cache_write=0 cache_hit_pct=0`. Behaviour changed too:
one tool execution instead of two, every run, answers still correct. That second
finding is **unexplained** and is written here unexplained rather than rounded
off.

**omp stays in the path anyway, and this is the entry that says why.** The reason
is uniform routing across clients — one place where model traffic can be seen,
labelled and measured — not economy. It is a **deliberate cost, not a saving**,
and any later reader quoting the −39.6% should quote this paragraph instead.

**Claude Code's numbers are real, and they decay.** Same tool-using prompt: 3
turns and 2 API calls on both paths, 51,434 billed input direct against 24,554
through headroom — **−46%**, no extra round trip from deferral. Then six
sequential turns in one session, growing to ~125k tokens per call, against an
identical direct control: **$2.1912 vs $2.2377, −2.1% overall, −4.0% across the
steady turns**. Nothing broke — `cacheWrite` stayed pinned at ~22,4xx per turn on
both paths from turn 3 on, so the prefix held and was never rewritten as history
grew. The saving simply stops mattering: what headroom removes is a flat ~13k of
system prefix inside the *cached* region, and a constant offset against a cost
base that grows with history shrinks to noise. A percentage measured at cold
start is not a property of the proxy; it is a property of the context size it was
measured at.

**The mechanism, found afterwards: a leading underscore.** `inject_tool_search_deferral`
prepends a `tool_search_tool_regex` stub and marks every *non-core* tool
`defer_loading: true`. The core exemption is an exact, case-insensitive match
against `_TOOL_SEARCH_CORE_TOOLS` — `bash`, `read`, `edit`, `write`, `glob`… omp
names its tools `_read`, `_bash`, `_edit`, so not one of them matches. Run the
real function against both clients' captured bodies and the asymmetry is total:

| client | tools sent | resident after transform | deferred |
|---|---|---|---|
| Claude Code | 77 | **7** — the stub plus `Bash`, `Edit`, `Read`, `Skill`, `WebFetch`, `Write` | 71 |
| omp | 12 | **1 — the stub, and nothing else** | 12 |

With no resident tool, omp cannot call anything until the server runs a tool
search — which is the one-execution-instead-of-two, and it is not a mystery any
more. What the search pulls back then lands *outside the cached prefix*, because
omp writes exactly one `cache_control: ephemeral`, on the final user message,
and none on tools or system; Claude Code carries two breakpoints inside
`system[]`, so its tool surface caches on its own. Hence 9,488 tokens billed
fresh on every tool-using exchange and **2** on a no-tool control.

**Fixable upstream, not from configuration.** The function takes a `core_tools`
parameter, but the call site passes no override, so there is no knob: stripping a
leading `_` before the comparison, or gating on client, would fix omp and leave
Claude Code untouched. The only switch that exists today, `HEADROOM_TOOL_SEARCH=0`,
is global — it would remove Claude Code's saving along with omp's penalty.

## A ninth instrument: a client's local arithmetic read as the server's limit

On 2026-08-13 I stated twice that omp's real context ceiling was 200,000
tokens, because omp never sends `anthropic-beta: context-1m-2025-08-07` — 0
occurrences in its 176 MB binary, 0 of 19 requests on the wire, against
2,972 of 2,972 from interactive Claude Code. The beta exists to unlock 1M, so
its absence looked like the wall.

The ladder says otherwise. Direct to `api.anthropic.com`, no beta anywhere:

| sent | model | result |
| --- | --- | --- |
| 231,050 | claude-opus-5 | 200 |
| 231,054 | claude-opus-4-8 | billed, then a *content* refusal — `Refusal (bio)`, not a length error |
| 340,019 | claude-opus-5 | 200 |
| 1,031,017 | claude-opus-5 | `400 prompt is too long: 1031017 tokens > 1000000 maximum` |

**The ceiling is 1,000,000 and the beta does not gate it on this account.**
What the header gates is the *client's own* window arithmetic: Claude Code's
`o_f()` returns `1e6` when the model is tagged `[1m]` or the beta is present,
and falls to the `nEr = 200000` default otherwise. That constant is what a
client uses to decide when to compact and when to refuse locally — it is not a
report about the API, and it was read as one.

The two failures in the ladder are each worth keeping. The 1.03M attempt cost
nothing: an over-limit request is refused before it is billed, so probing a
ceiling from above is free and probing it from below is not. The opus-4-8
refusal cost $1.44 and was not a limit at all — 200k of random dictionary words
trips the usage-policy classifier, so the filler used to measure a length
ceiling has to be benign prose or the instrument measures the wrong refusal.

Same shape as the sixth and the seventh: a real number, correctly read, about
something adjacent to the question. Here the adjacency is *whose* limit — the
client's model table and the server's enforcement are two different claims, and
only one of them 400s.

## A tenth instrument: correct evidence that made the answer worse

Every instrument above measured the wrong thing. This one measured the right
thing, reported it accurately, and the output got worse anyway — which makes
it the most useful entry on the page.

A planner divides a grant between the steps it writes. It had no idea what
anything cost, so it divided evenly: on 2026-08-14 it gave `$0.10` to a step
whose type had never finished under `$1.26`. The fix was to read back what
each agent type had actually cost from the workflow record and put it in the
prompt — median, range, sample count, with censored rows excluded and counted.
The mechanism was built, unit-tested at every layer, and verified end to end
by capturing the argv of a stubbed model CLI: the table arrived, verbatim.

What the planner was handed, scoped correctly, every figure true:

```
  - filereader: never measured
  - reviewer:   never measured
  - plan-check: never measured (excluded: 1 ran unpriced)
  - explore:    median $1.29 over n=1 run(s), range $1.29-$1.29
  - plan:       median $0.28 over n=1 run(s), range $0.28-$0.28
```

The plan it produced dropped `explore` entirely — six `filereader` steps and
two reviewers, a graph that **cannot search the repository it is auditing**.
The previous plan, written without the table, used five explorers. Same
binary, same commission, same grant; the cost table was the only variable.

The prompt carried a sentence written to prevent exactly this: *"never
measured" means exactly that: nobody has priced it here, not that it is free.*
It did not survive contact with the number beside it.

### The hypothesis, and the measurement that refused it

The reading at the time: the number was not the problem, the **asymmetry**
was. One type carried a figure and every other type carried a phrase, and a
model minimising cost reads that as a ranking with one known-expensive entry.
A median over one sample is not a median anyway — it is an anecdote with a
statistic's name on it — so withholding it below three clean runs was correct
on its own terms and cost nothing to do.

It was done, and it was tested. The threshold works: reconstructed against a
copy of the database as it stood, the next planner was handed

```
  - filereader: never measured (excluded: 1 ran unpriced)
  - reviewer:   never measured
  - plan-check: never measured (excluded: 2 ran unpriced)
  - explore:    never measured (2 clean runs so far, too few for a median)
  - plan:       never measured (2 clean runs so far, too few for a median)
```

— no figure anywhere, perfect symmetry between the priced and the unpriced.

**`explore` did not come back.** Three `filereader` steps and four reviewers,
`$0.88` of `$3.50`, still no step that can search. The asymmetry hypothesis is
**refuted**: whatever removed the searching type from the graph, it was not
the published median, because the median was gone and the type stayed gone.

What is left of the difference between the plans that used explorers and the
plans that do not is the cost section as a whole — its header, five lines that
say nothing is priced, and a closing paragraph warning that a share below what
a type costs buys a step that stops at its ceiling having produced nothing.
That warning is the current suspect: told that under-funding is the danger and
that no type's cost is known, choosing the types that plausibly cost nothing is
a defensible reading. It is a hypothesis with two runs either side of it, and
it is recorded here as a hypothesis.

Two entries on this page for one change, then: a measurement that improved the
evidence and did not improve the answer, and a correction to it that was right
and did not work either. Both are kept, because a page that only records the
diagnoses that landed teaches the wrong lesson about how often they do.

### The general shape

**Evidence a model can act on is not the same as evidence a model reads
correctly.** Every prior entry on this page asks whether the number is true.
This one asks a second question that no amount of accuracy answers: *what
distinction does publishing it draw, and is that distinction real?* A figure
published beside an absence is not one fact, it is two — the figure, and the
contrast — and the second one is invented by the layout rather than measured.

Three rules follow. The first two are earned; the third is the one this entry
exists to record.

1. **Publish a statistic only where the data supports the word.** `median`
   over `n=1` is a category error, and the reader is entitled to take the word
   at face value. This one is right whether or not it fixes anything.
2. **An absence beside a number is a comparison.** Where some rows are unknown,
   the unknowns and the knowns must be rendered so that the difference between
   them cannot be read as an ordering.
3. **A plausible mechanism is not a diagnosis until the fix moves the
   number.** The asymmetry story explained every fact available when it was
   written, named a real defect, and produced a change that was correct in
   isolation — and the output did not move. Explaining the observation is the
   cheap half; the expensive half is the run that could have refuted you, and
   this one did.

What survives unrefuted is narrower than it felt at the time — and narrower
again after the next entry, which took the ground out from under this whole
section.

## An eleventh instrument: the comparison that was never controlled

Everything above compares configurations one run apiece. Run 8 had the cost
section and produced no explorers; run 10 had it without the closing warning
and produced five. That was reported here as a single-variable comparison and
the warning was named as the cause.

Nobody had asked whether the process was stable. On 2026-08-14, after the
grant bug below surfaced, the same commission was run five times with nothing
changed at all — same grant, same binary, same prompt, same everything:

| run | explore steps | allocated |
|-----|---------------|-----------|
| 1 | 0 | $0.85 |
| 2 | 0 | $0.88 |
| 3 | 5 | $0.90 |
| 4 | 0 | $0.90 |
| 5 | 4 | $0.90 |

`0, 0, 5, 0, 4` at rest. Both outcomes the configuration experiment was
distinguishing occur unprompted, in the same cell, with nothing varied. Run
10's five explorers and run 8's zero are two draws from that distribution.
**The warning may still have done it. Nothing here shows that it did, and the
run that was offered as proof cannot carry the claim.**

Two facts about the setup made this worse than ordinary sampling noise, and
both were visible before any of it ran:

- **The planner's main input is another model's answer.** The `explore` step
  is a model turn whose text feeds the planner, so "the same commission" was
  never the same prompt. Captured this time, the five planner prompts had five
  distinct hashes.
- **Each run mutates the next one's input.** The cost table is read from the
  same database the runs write to: `n=6`, `n=7`, `n=8`, `n=9`, `n=10` across
  the five, with the median moving $1.54 → $1.47 → $1.54 → $1.47.

A prompt log helped here and should have existed earlier. Four runs had been
compared against a prompt captured from a stub binary standing in for the CLI
— a different string than the real planner reads — and the gap surfaced only
as a grep reporting a grant figure from the wrong run. `ATENEA_PROMPT_LOG`
now records what was sent, at the call site, on the way out.

**A controlled comparison is only controlled if the process is stable, and
that is a measurement, not an assumption.** It is one cheap run repeated: the
null experiment, the same cell twice, before any cell is compared to another.
Eleven runs of interpretation preceded it here, and the honest cost of
skipping it is not the runs — it is that three entries above were written
with more confidence than their evidence supported.

What survives: adding a section of cost evidence to a prompt was followed by
plans that could not search, and no correction has reliably changed that back.
Whether the section caused it is settled in the next entry, which could only
be written once the noise had a name.

## A twelfth instrument: the feature that was measured, and the bug that fixed
what it was aimed at

Freezing the input made the comparison affordable. One real planner assignment
was captured verbatim — exploration, served context and cost table inside it —
and replayed against three builds, five runs each. One prompt hash per cell,
checked rather than assumed; the cost table is pinned by construction, because
a replay writes no `workflow_step` row for its successor to read.

| run | A: no cost section | B: section + closing warning | C: section as shipped |
|-----|--------------------|------------------------------|-----------------------|
| 1 | 8 explore, 11 steps, $3.45 | 2, 4, $3.50 | 3, 6, $3.39 |
| 2 | 6 explore, 12 steps, $3.50 | 2, 4, $3.50 | 3, 6, $3.48 |
| 3 | 8 explore, 12 steps, $3.50 | 2, 3, $3.50 | 3, 5, $3.50 |
| 4 | 9 explore, 12 steps, $3.45 | 2, 4, $3.50 | 3, 6, $3.45 |
| 5 | 7 explore, 12 steps, $3.45 | 2, 4, $3.50 | 3, 6, $3.45 |
| | **6–9**, median 8 | **2**, all five | **3**, all five |

**The cost section as shipped makes plans worse.** Exploring steps fall from
6–9 to 2–3 while allocation stays in the same band, $3.39–$3.50 in every cell.
Less exploration for the same money, which is not the trade the section was
built to make: it was added so a planner would fund steps adequately, and
funding is exactly what it does not touch.

The warning that was accused, then acquitted, then accused again is a minor
term. B and C do not overlap, so it does have an effect — of **one step**, in
the predicted direction. What it does not do is what was claimed twice
tonight: **zero explorers never occurred in any of the fifteen frozen runs.**
The `0`s that started this were exploration variance wearing a mechanism's
clothes.

### The half nobody was measuring

Under-allocation — the problem the cost table was built to solve — was a bug
in the contract. The planner was told "the grant for the whole graph is $X"
where X was the plan step's own `budget_usd`, so it divided its own allowance
and eleven runs allocated $0.87–$0.90 whatever the commission said, including
one granted $10.00. With the run's grant travelling beside the step's share,
the same commission allocates $3.50 of $3.50 and $9.68 of $10.00.

**25% to 100%, from a field on a card.** No configuration of the cost table
moved that number, before or after. The table was aimed at under-allocation
and hit composition instead; the bug hunt was aimed at composition and fixed
under-allocation. Both landed — neither on its target.

### What generalises, and what does not

**Generalises:** the ordering (no section explores most, section-with-warning
least, shipped configuration between them) and the size class — the section is
a 2–4× effect on composition and a null effect on allocation. Five runs per
cell is enough to see an effect that large and not enough to see a small one,
which is why the one-step warning difference is reported as separated rather
than as a finding.

**Does not generalise:** every magnitude in the table. All fifteen runs replay
a single frozen exploration, so "the section costs about five exploring steps"
is a fact about this input. A second frozen card would test that, and has not
been run.

### Removed, not reverted

The section came out of the planning prompt on 2026-08-14. The distinction
matters for whoever reads this next: a revert says the change was a mistake,
and this one was a correct measurement delivered to the wrong reader. What was
removed is the delivery. `CostByType`, the store method, the `repository`
column, the `workflow_step` pricing columns and every test defending the
censoring, the verbatim `never measured` and the printed `n` all stay, with
the renderer exported as `CostReport` for a reader, for `plan-check`, or for a
consumer shown to benefit. An untested renderer rots, and the next consumer
would inherit a broken measurement rather than none.

The worked example in the prompt lost its `budget_usd = 0.25` at the same
time, unmeasured and said so: a worked example carrying a figure is an anchor,
and leaving one in place after this week is not a defensible default.

Confirmed on one frozen replay against the measured baseline -- 7 exploring
steps, 11 steps total, $3.45 of $3.50, and the plan compiles. One run, because
this is a removal checked against a baseline rather than a new comparison.

No mechanism is offered here. The last two proposed were both plausible, both
consistent with everything then measured, and both wrong; the honest state is
a large effect, measured against one input, on a feature nobody had shown to
help.

## A thirteenth instrument: a retry that reported progress by re-sending a request that could only fail

This one is not from Atenea. It surfaced in the omp harness on 2026-08-14, and it
earns a place here anyway, because the page is about the shape and not about which
tool produced it — and it is the cleanest instance of the shape yet. The earlier
entries are all *readings* that describe the wrong thing. This is an *action*:
well-formed, executed, reported as progress, and unable to do anything but fail.

A session wedged. Every request to Anthropic came back `400 invalid_request_error`,
rejected at `messages.1.content.5`: *thinking or redacted_thinking blocks in the
latest assistant message cannot be modified*. That error is deterministic and about
the request body — the same body re-sent can only earn it again. omp classified it
as a transient failure: it retried, and on retry fell back `claude-sonnet-5:max →
claude-opus-5`, and both the retry and the fallback re-sent the byte-identical
assistant turn, thinking blocks and signatures unchanged.

Five outbound requests were logged inside six minutes; this much is measured, from
the bodies omp actually put on the wire:

| # | model sent | thinking signed by | rejected at |
| --- | --- | --- | --- |
| 1 | `claude-sonnet-5` | `claude-sonnet-5` | `content.5` |
| 2–5 | `claude-opus-5` | `claude-sonnet-5` | `content.5` |

The error never moved off `content.5`. A genuine model/signature mismatch on the
fallback would have failed at `content.0`; it did not, which is the tell that blocks
0–4 validated under both models and the resend carried the one corrupt block along
untouched. The TUI showed `Fallback: … -> claude-opus-5`, then `Retry failed after
1 attempts`, and every later keystroke reproduced the same 400. The mechanism did
not fail. It succeeded, on every attempt, at issuing a new and well-formed request
that had been decided against before it left — a crash would have ended the session
honestly; this kept it alive, showing retry progress, doing the wrong thing forever.

What corrupted `content.5` in the first place is **not** measured here, and is filed
as not measured. The proxy in front of the API was cleared by reading it — it
rewrites only the `tools` array and forwards message content unchanged, confirmed
from its source and its live transform feed — so the invalid body is omp's own
outbound. The probable trigger is tool-search deferral collapsing a multi-round
discovery into a single assistant turn of six thinking blocks, on a signing-proxy
path that three earlier omp fixes (`#1531`, `#6495`, `#6717`) did not fully close;
but no pre-collapse stream was captured and omp ships here as a compiled binary, so
the mechanism stays a hypothesis. It is filed as one —
[oh-my-pi#8559](https://github.com/can1357/oh-my-pi/issues/8559), marked inference
inside the report itself.

The retry is the half that is measured, and it is filed separately:
[oh-my-pi#8558](https://github.com/can1357/oh-my-pi/issues/8558). The two are
independent — repairing the corruption stops this particular loop, but the retry
would still turn the next non-retryable `400` into the same unbreakable cycle,
because the defect is the classification, not the block. A `400
invalid_request_error` about message content is not transient; retrying it by
re-send, and falling back to another model with the previous model's signed thinking
still attached, are two ways of spending attempts on a request that cannot be
granted.

The shape, for the index: **an action that returns something answer-shaped instead
of erroring is the same failure as an instrument that reads something answer-shaped
instead of reporting *unknown*** (see 12). A retry that re-sends a guaranteed-invalid
body is a `fine` standing where the honest state was *this cannot succeed*, and the
fix points the same way this page keeps arriving at from every other side: make the
failure loud — refuse the resend, surface the terminal error — rather than let a
well-formed motion stand in for progress.

### What happened to both halves

Both were fixed the same afternoon, by someone else, within twenty minutes of
being filed — and the two outcomes are worth separating, because they are not
the same kind of result.

The measured half needed no defence. #8558 was reproduced on `main` in ten
minutes and closed by [#8560](https://github.com/can1357/oh-my-pi/pull/8560):
the deterministic immutable-thinking `400` now surfaces as terminal, with no
same-model retry, no model fallback, and no retry-progress event. That is what
a measured report buys — nobody had to agree with an interpretation, only read
a table of five requests and their rejections.

The unmeasured half was **confirmed, and the confirmation was not mine**. The
report ordered three candidate mechanisms by fit and refused to pick one. The
first — server and tool-search blocks dropped during the collapse, extending
the `#6495` family — is the one that reproduced: a focused stream emitting
`thinking → server_tool_use → tool_search_tool_result → thinking → tool_use`
persisted as `thinking → thinking → tool_use`, both tool-search blocks dropped
before the custom-endpoint projector runs. Fixed in
[#8561](https://github.com/can1357/oh-my-pi/pull/8561).

Being right about the hypothesis is the least interesting part of that, and it
would have been just as correct to be wrong. What did the work was the report
naming the experiment it could not run — *replay the latest assistant message
and assert every original block survives byte-for-byte, in order* — and someone
with source access and a stream capture running exactly that. The half I could
not measure was closed faster than the half I could, because it was labelled as
unmeasured and came with the test that would settle it.

## A fourteenth instrument: two comparisons whose sides were in different units

Two defects in one evening, one shape: a comparison whose two sides were never
expressed in the same units. They are filed together because only one of them
announced itself.

**The loud one.** A tree of 15,563 files was walked before a run and after it,
recording `(path, mtime, size)` per file. The baseline used `st_mtime_ns`; the
re-check, written an hour later, used `st_mtime`. Every file came back changed:

```
.env | then: (1785883432157773146, 3143) | now: (1785883432.1577733, 3143)
```

Nanoseconds against float seconds — the same instant, twice, disagreeing about
everything. It cost one minute. `15563 changed, 0 added, 0 removed` is not a
result anybody believes, so it was re-derived in the correct units before it
reached a sentence: **1 added, 3 changed**, all four stamped 85 minutes after
the run under audit had finished.

**The quiet one.** The same evening, the question was whether a write had
restarted a live API. The container log was searched for
`Server listening|Database connected|migration`. It returned **0** — over the
precise window, and over the whole history since 08-10 — and that zero was
reported as evidence that nothing had restarted.

The log is pino with colour on. Every line carries `\x1b[...m` around the very
tokens being matched:

```
2026-08-14T17:09:10.256510005Z     ^[[35mreqId^[[39m: "req-mp"$
```

Strip the escapes and the same window yields three restarts. **A grep that
cannot match is indistinguishable from a thing that did not happen.** The output
of a broken instrument and the output of a clean system are the same string:
nothing. This is the second time in two days from the identical cause — on
08-13 the escapes around `reqId` broke a scanner's correlation the same way —
which makes it a property of this log rather than an accident: **any pattern
matched against `taxiprime-backend` output is wrong until the escapes are
stripped.**

The pair inverts the usual intuition about severity. The absurd reading was
harmless; it could not survive being written down. The plausible one — a clean
zero, in the direction already expected — went into a report as fact and would
have stayed there.

### What the fixed instrument then measured

With the escapes stripped, the first hard number on something that had been a
hypothesis for days: **one save in that tree costs two full boots against the
live dev database.**

The container's inner process restarts and runs the migrator:

```
16:52:27Z [tsx] change in ./src/modules/reservas/reservas.routes.ts Restarting...
16:52:28Z   message: 'relation "__migrations" already exists, skipping',
16:52:28Z [migrate] up to date (63 migrations tracked)
16:52:28Z INFO (307040): Database connected
16:52:28Z INFO (307040): Server listening at http://127.0.0.1:22021
```

And a PM2 ghost — `taxiprime-api-dev`, pid 2232, the same tree as cwd, up since
08-13 — independently boots, migrates, connects Redis, and only then dies:

```
18:52:27 6:52:27 PM [tsx] change in ./src/modules/reservas/reservas.routes.ts Rerunning...
18:52:27 [migrate] up to date (63 migrations tracked)
18:52:27 INFO (3581486): Database connected
18:52:27 INFO (3581486): Redis connected
18:53:08 Error: listen EADDRINUSE: address already in use 0.0.0.0:22021
```

Three saves at 16:52:27, 16:53:07 and 16:53:55 — **88 seconds, six migrator
passes**, six connections to the live dev database, three of them from a process
that can never serve a request because the container holds the port. No
migration was applied on any pass: all six read `up to date (63 migrations
tracked)`. The cost is the boot, not the schema.

Two details make the ghost worse than a duplicate. It is invisible from the API
side — nothing in the container's log mentions it, and the only symptom is
`EADDRINUSE` in a PM2 file nobody reads. And it dies *after* the expensive part:
the port check that stops it runs once the database and Redis connections are
already open.

## A fifteenth instrument: a ceiling that enforced nothing and killed everything

`--max-budget-usd` is the only thing standing between a step and an unbounded
bill, and it is read as a limit everywhere in this project. Measured on
eighteen real steps, twice, it did neither job.

It did not bound the spend. Every one of the twelve steps that ran overshot its
own declared share, by 1.15x to 1.79x, and the run overshot its $3.50 grant:

| step | declared | spent | ratio |
|---|---|---|---|
| admin-aux | $0.18 | $0.32 | 1.79x |
| ws-routes | $0.18 | $0.29 | 1.61x |
| mechanisms | $0.20 | $0.31 | 1.55x |
| auth-routes | $0.22 | $0.26 | 1.16x |

And it killed the step at the strictly worst point in the run: after the money
had bought the reading and before a word of the answer was written. `result_len
= 0` on all twelve, $3.78 for zero answers. A limit that stops a process once
it has paid for everything and produced nothing is the downside of a limit with
none of the upside.

The second reading is the one worth keeping. Fixing the kill — hold back part
of the share, tell the step to answer instead of killing it — changed nothing
on the same plan, and the reason was arithmetic that had never been checked
against the instrument's own floor. One turn on this repository costs about
**$0.35 before a single file is read**: 25,340 tokens of cache write for the
system prompt and tool schemas. Seventeen of the eighteen steps were funded
below that floor. They were not budgeted badly; they were budgeted below the
price of starting, and no split of $0.20 leaves room to write when $0.20 does
not buy the first request.

The same fix on the same card, at a share that clears the floor, turned a $1.11
death that wrote nothing into a $0.69 answer at 90% coverage naming what it had
not reached. The mechanism was never the thing that was broken. The number
underneath it was, and nothing in the pipeline had ever compared that number to
what a turn actually costs.

### What the floor actually is, once it was measured instead of estimated

`$0.35` above was inferred from a trajectory. Measuring it directly — one turn
that is asked to do nothing at all, `Reply with exactly: ok`, on the same
repository with the same model and the same tools a step gets — gives a smaller
number and a much more useful one:

| agent | tool surface | cache write | cost of starting |
|---|---|---|---|
| `explore` | Atenea's capabilities + Read + Glob | 27,666 tok | **$0.28** |
| `plan` | none | 4,991 tok | **$0.06** |

Same repository, same `claude-opus-5`, same evening. The difference between the
two rows is nothing but the tool definitions, so **81% of the cost of starting a
turn is the tool schemas** — $0.22 of $0.28. The system prompt, the thing one
would naturally blame, is the cheap sixth.

That reframes the twelve deaths a third time. They were not underfunded because
someone typed small numbers; they were underfunded because every step was billed
for a catalog before it was allowed to read one line, and nothing in the pipeline
knew that catalog had a price. It also says where the floor can be lowered, which
an estimate never could: not by trimming the prompt, but by handing a step fewer
tools.

The first version of the stored measurement got this wrong in a way worth
recording, because it is the same error one layer up. It keyed the floor by
`(repository, model)` — the two things that are obviously about cost — and
measuring `plan` silently overwrote `explore`, replacing $0.28 with $0.06. A
check built to refuse underfunded explore steps would have waved every one of
them through, quoting a real measurement of the wrong thing. The floor is per
tool surface, and the tool surface belongs to the agent type.

The second version got it wrong in a better way. With the agent in the key, both
rows were re-measured minutes after the first pair, and both came back at
**$0.01 with zero tokens of cache write** — a 28x discount that arrived as a
real receipt, from a real turn, priced by the CLI itself. Nothing was broken.
The prompt cache was warm: the prefix the cold probe had paid 27,666 tokens to
write was still resident, so the second probe read it back at a tenth of a cent
and reported that as the cost of starting.

A floor measured that way is worse than no floor, because it is quotable. The
probe now refuses a reading whose cache reads exceed its cache writes and says
to come back when the entry has aged out. The refusal was confirmed on live
traffic within the minute: `27666 tokens read, 0 written` — the same number the
cold probe wrote, coming back as evidence that the measurement was of the wrong
turn.

The general shape, and the reason this belongs with the other fourteen: **a
measurement that costs almost nothing to take is telling you that somebody else
already paid for it.** A price falling by an order of magnitude between two
identical runs is not good news about the second one.

### The instrument kept warm the thing it was measuring

Waiting the cache out took three attempts, and the reason is the sharpest part
of this entry. The prefix was written at 23:11 and probed again at 23:28, 23:35
and 00:25 — seventy-four minutes after the write, on a one-hour entry — and the
reading was still warm, at `23,278 tokens read` both of the last two times, the
same figure to the token. Every probe was refreshing the entry it was trying to
find expired. **A measurement taken on a schedule tight enough to be convenient
is a measurement of its own last attempt.** The cold reading arrived only after
sixty-six minutes with no probe at all: `$0.27, 26,603 tokens`, within 4% of the
first cold reading of the evening, which is what makes both of them credible.

One more thing fell out of the failed attempts, and it moves the design. A
repository that had never been probed — this one — came back warm on its very
first probe, `23,278 read, 3,323 written`: it shared 23,278 tokens with a prefix
written for a different repository. **The dominant term in the floor is not the
repository at all, it is the tool surface**, which is machine-wide; the
repository contributes about 3,300 tokens of delta. The store is keyed on all
three because a floor should be quotable as the thing it names, but the number
it holds is mostly a fact about the agent type.

What that leaves unresolved, honestly: within a run, the first step to start
pays the write and every step after it reads. The floor as measured is the
pessimistic, cold, first-mover price, and refusing on it is refusing on the
worst case. That is the right default for a plan nobody has run yet — any step
may be the first — but it is not the average, and the day a plan is refused that
would in fact have run warm, this paragraph is where the argument starts.

End to end, on tonight's real eighteen-step plan, against the cold floor:
`workflow create` refused it, exit 2, naming ten of the eighteen steps and the
arithmetic on each, and the newest row in the run store is still the one from
before the floor existed. Nothing was written.

## A sixteenth instrument: four fixtures that encoded the belief and then confirmed it

Measured on 2026-08-14, building the Ladygraph provider into Atenea: a stdio-MCP
transport, an adapter, four capabilities, and a unit-test suite written alongside
all three. Every package was green. `go test ./... -race` passed. Then the thing
was pointed at the real server for the first time and four defects fell out in a
row, three of which had a passing test sitting directly on top of them.

They are worth listing plainly, because the mechanism is identical each time and
the surface is different each time:

1. **`core.guardedRunner` swallowed an optional interface.** It embeds
   `contract.Runner` — an interface, so only that method set is promoted.
   `contract.IndexProber` is optional and found by type assertion, and the
   assertion fails on the wrapper. Ladygraph is the first supervised runner that
   also probes, so it reported no index for a graph it was holding, with no error
   anywhere. The tests for the adapter's own `ProbeIndex` all passed: they called
   it directly, which is the one path production never takes.
2. **A capability the orchestrator's card does not name cannot be dispatched.**
   Declared in the catalog, chosen by the funnel, answered by a live runner, and
   still refused with `agent orchestrator may not ask for graph.status` — a gate
   the CHANGELOG records hitting once before, for `symbol.calls`.
3. **The `NOT_FOUND` reclassification was unreachable.** The transport bins every
   tool refusal as `FailureInvalidInput`, correctly — it must not learn one
   provider's vocabulary. The adapter's `failureFor` short-circuited on
   `errors.As` before its own rule ran, and the tool-name prefix meant the code
   was not the first colon-separated segment either. Its unit test constructed a
   bare `SYMBOL_NOT_FOUND: …` error by hand and passed.
4. **`get_file_outline` returns an object, not a list.** Its declarations hang
   off `results.symbols`, alone among these tools. The fixture said `"results":
   [ … ]`, the decoder was written to match the fixture, and the package stayed
   green while every live outline call failed with `cannot unmarshal object into
   Go struct field`.

Three of the four passed their unit tests because each test asserted against the
design, and the design was wrong in a way only the real server could say. That is
the sharpest version of this week's pattern: **the fixture that encodes your
belief and then confirms it.** A mock is not a cheap copy of the far side; it is
a written record of what you currently think the far side does, and a test built
on one measures the consistency of your beliefs, never their accuracy. It cannot
fail for the only reason that matters, and it reports that inability as a pass.

What separates this from the rest of the page is where the deception sits. The
earlier instruments produced a *reading* about the wrong process — a pid, a file,
a column, a client name. Here the instrument produced no reading at all. It
produced **agreement**, which is worse, because agreement is what a test is for
and there is no anomalous number to notice. Nineteen adapter tests, 1,769 across
the tree, all green, describing an integration that could not complete a single
call.

The corrective is not more tests. It is one call to the real thing before the
fixtures are written down: five minutes of a JSON-RPC driver over
`ladygraph serve`'s stdin printed the exact envelopes, and every one of the four
would have been impossible to write. The fixtures that shipped were rewritten
from those payloads afterwards, which is the same work in the correct order. The
ordering is the whole finding — the shapes were measurable at any point, and were
measured only after the code had been built against a guess about them.

There is one honest defence of the suite. Once the shapes were right, those same
tests held the corrections in place and caught two regressions during the fix.
A fixture is a fine way to *keep* behaviour that has been observed. It is not a
way to *learn* it, and the failure here was using it for the second.

## A seventeenth instrument: a parent that no longer governed what it still named

The page describing this failure was, for one turn, itself a casualty of it.
Told to add an entry here, the first search run was a filename glob for
`*instrument*` under the repository root. It matched nothing, because it was
answering the wrong question — not "does a file about this exist" but "does a
file *named* this exist" — and this directory names files for what happened,
not what they are about: `not-built-yet.md`, `what-a-capability-is-worth.md`,
and this one, titled *When the instrument is the bug* and never once spelling
the word in its own filename, though it spells it a dozen times in the prose.
A glob built to match a name cannot find a file whose name was never built
that way, however many times the word it is chasing appears inside. `ls
docs/content/` — one call, twelve files — would have settled it immediately;
it was not tried until asked why the first attempt had failed. This is lesson
10 wearing a different sentence: there, a number's *shape* was measured
before its *source* was confirmed, a refresh rate read off a cache mistaken
for the origin. Here, a *shape* — a naming pattern — was searched for before
its *source*, one real filename in the directory, was ever looked at. The
page stating that lesson was findable in one call, and went unfound, by the
same move the lesson describes.

Measured 2026-08-15, checking whether Atenea's `agent-device` allow-list gates the
process doing the work, or only the process standing in front of it.

The first check almost repeated the page's oldest mistake. Two earlier phases in
the same session had each restarted `atenea.service` once, and a restart mints a
new incarnation of every process the service owns, its per-client MCP bridges
included. Asked to confirm the bridge was still alive, the obvious move was
`ps -p` against a pid recorded after the session's first restart — already
superseded by its second. It returned nothing: a result indistinguishable, read
cold, from "not running." It meant the opposite. `pgrep -af 'agent-device mcp'`
found the current one, 783943, alongside a second bridge, 2018103, spawned the
day before by an unrelated session and never gated by anything.

The second reading was subtler, because nothing about it would ever time out or
come back empty. The device work is done by neither bridge: both discover a
single long-lived daemon, pid 225716, through a lock file at
`~/.agent-device/daemon.json`, and reuse it rather than starting their own. `ps`
reports the daemon's parent as 2018103 — the stray bridge. Read as "the bridge
governs the daemon," that is false, and has been false since the daemon started:
it calls `setsid()` at launch, measured the same week, taking its own process
group and session for the specific purpose of surviving whatever spawned it.
`ps` was not lying — 2018103 really was 225716's parent at fork time, and stays
the value in that column for exactly as long as 2018103 remains alive to hold
it. **A `ppid` records who forked a process, not who governs it now, and the
two facts look identical for as long as the fork-time parent happens not to
have died** — the same gap lesson 1 closed from the other side, where the
column was the tell instead of the trap.

What actually bounds the daemon is the allow-list at the one gate it is reached
through, and nowhere else. Atenea's `tools = ["devices", "boot", "open",
"snapshot", "click", "find"]` narrows what Atenea's own clients can ask this
daemon to do. It says nothing about the daemon's own surface — the full CLI
carries `press`, `type`, `fill`, `settle`, and more — nothing about a bridge
spawned outside Atenea entirely, and nothing about anyone on the host who can
read the bearer token sitting next to the pid in `daemon.json`. Same shape as
14, one layer over: a boundary measured at one hop describes that hop.

Not measured, and filed as such: whether the daemon does anything beyond
`setsid()` to survive its parent — polls for it, ignores it entirely, was never
asked to care — is unknown. Its source is rollup output, single-letter
bindings, no line attributable to a decision either way. The finding stands on
a syscall confirmed live, not on a reading of code that could not be read.

## An eighteenth instrument: live confirms the channel, not the content

Measured 2026-08-15, directly, not by re-reading `atenea-config-guard`'s own comment about
`atenea mcp --check` and assuming the sibling command shares its blindness. `mcp-agree --live`
does not call `--check` at all: it spawns bare `atenea mcp` — the exact command a real client's
own config declares (`~/.omp/agent/mcp.json`: `command: .../atenea, args: ["mcp"]`) — and speaks
the real protocol at it, `initialize` then `tools/list`, over a live stdio pipe. That is a
stronger claim than the comment had made, so it got tested on its own rather than inherited.

Baseline: `atenea 42, headroom 3, serena 23`, ficheros VERDE. Then `atenea.toml`'s mtime was
bumped with `touch` — content untouched, byte-identical, only the timestamp moved past
`atenea.service`'s `ActiveEnterTimestamp` — exactly the condition `atenea-config-guard` exists
to catch. The same handshake, run again immediately after: `atenea 42, headroom 3, serena 23`,
identical to the digit, still VERDE. Run once more a minute later: the same again.
`atenea-config-guard`, unmodified, run against the identical induced state in between: caught it
at once, named both timestamps, gave the restart instruction. One touch, two checks, one blind.

That confirms what the comment implied but had not itself shown: bare `atenea mcp` bridges to
the same long-running core the systemd unit supervises rather than reparsing on its own account,
so a handshake against it can only ever answer with whatever the core currently holds in memory
— which is exactly the thing that goes stale. The exact original mtime was restored afterward;
nothing about the finding needed a mark left behind to stay true.

One adjacent thing in that same output did change, and belongs kept separate rather than folded
in: the account-surface line grew a `CADUCA` suffix after the touch, because plain mode's own
fingerprint moved and stopped matching the one `--wire` had cached from an earlier run. That is
a real check, correctly firing — but on a different subject, this run's file-derived fingerprint
against a previous run's cached one, not on whether atenea's core has re-read the file since it
changed. `mcp-agree` now carries two "is this current" checks, watching two different things,
and neither covers the other's blind side.

So, plainly, what `--live` can and cannot back. **Can:** that the bridge process starts, speaks
real MCP correctly, and returns a tool list agreeing with what the core currently holds — which
rules out crashed, hung, or answering the wrong protocol, and is a genuine claim. **Cannot:**
that what the core holds is what is on disk right now. A stale core answers a real handshake
honestly, promptly, and wrongly, and `--live` has no way to notice, by construction — not a bug
in this check, a boundary in what it was ever built to see. Live proves reachable; it does not
prove current, and this page already has a name for a check that reports the fine direction by
default on a question it cannot see — see 12 — and for one that cannot see past the files it
reads — see 11. This is that second argument's mirror: the channel that skips files entirely
turns out to have a blind side of its own, just a different one.

The mode built specifically to distrust static configuration and go verify the real process
instead still has a boundary, and the boundary sits exactly where the fault that cost this
project twice this week lives. That is the instruments page's whole argument, arriving again
from the one place it had not yet been asked to look.

## A nineteenth instrument: two correct halves that only fail as a pair

Measured on 2026-08-15, porting a CI workflow into the repository whose code it checks.

Every other entry on this page is one thing lying: a figure measured against the wrong process,
a fixture encoding a belief, a check reporting the fine direction on a question it cannot see.
This one is different in kind, and the difference is worth its own entry. **Both halves were
correct. Both were verified. The defect existed only in their combination**, and no test of
either half could have found it, because neither half contained it.

The two changes. First, a workflow file, ported from the repository it had been stranded in by a
split. It was verified the expensive way rather than the confident way: a fresh clone of the
published remote, then the job's four steps run in order with the pinned toolchain —
`install --frozen-lockfile`, `typecheck`, `lint`, `test`, all exit 0, 123 tests. That closed a
real open question, since `typecheck` fails on the development machine for permissions reasons,
and the clean clone proved the failure was not in the code.

Second, a one-field addition to `package.json`: `packageManager: "pnpm@11.5.1"`, pinning a
version that until then existed only inside the workflow. Also verified — valid JSON,
`install --frozen-lockfile` exit 0, lockfile untouched.

The first run died in five seconds, before installing anything:

```
Error: Multiple versions of pnpm specified:
  - version 11 in the GitHub Action config with the key "version"
  - version pnpm@11.5.1 in the package.json with the key "packageManager"
```

`pnpm/action-setup` refuses to choose when the version is declared twice. The refusal is correct
behaviour — guessing is precisely how the drift the second change was written to close would have
come back, invisible. The tool was right. The two changes were each right. The pair was wrong.

What makes this its own shape is that the standard defence does not reach it. Verifying a change
in isolation is the discipline this page keeps recommending, and here it was followed twice,
honestly, with real commands and real exit codes — and it could not have worked. An interaction
defect lives in no component, so component-level evidence is not weak evidence about it; it is
**silent** about it. Two green checks composed to red, and the greens were not wrong.

There is a second boundary underneath, and it is the more useful half. The clean-clone run
exercised the four *scripts*. It did not exercise the three *actions* above them —
`actions/checkout`, `pnpm/action-setup`, `actions/setup-node` — which exist only on a runner and
were treated as inert plumbing. The fault was in that untested layer. This is 11 wearing new
clothes: a check cannot see past the files it reads, and a local rehearsal cannot see past the
layer it can execute. Naming the layer you did not run is part of stating what your evidence
covers, and "I verified both halves" was a true sentence that implied a false one.

The detail that earns it a place here rather than a footnote: this was committed **while writing
the commit message that names the pattern**. The message for the `packageManager` change says, in
as many words, that the shape of the day's defects is *two places that should agree with nothing
checking that they agree*. It then created exactly that, in the same push — a third place holding
the same version, with nothing comparing them. Knowing a failure shape by name, well enough to
write it down, does not confer immunity from it. That is not a lapse in attention; it is what
makes the shape worth writing down at all. The page exists because recognition after the fact is
the normal case, and the honest form of the lesson is a check, not a resolution to be careful.

The check, stated plainly: **when two changes each remove ambiguity about the same value, verify
them together before pushing, because agreement is a property of the pair and neither one has
it.** Cheaply available here and not taken: the workflow and `package.json` both named the pnpm
version, and one `grep` across the pair would have shown two declarations where the whole point
was to have one.

## A twentieth instrument: a comment that records who wrote a line, read as who needs it

Measured on 2026-08-15, deciding whether two API keys could be deleted from `~/.bashrc`.

The file said this, and had said it for months:

```
# Added by codebase-memory-mcp install
export MINIMAX_API_KEY="sk-cp-..."
export GEMINI_API_KEY="AQ.Ab..."
```

Three people read that comment and drew the same conclusion — that the keys belong to
codebase-memory-mcp, so the question of deleting them is a question about that one tool. The
investigation that followed was careful and it was aimed one step to the left of the target. It
proved, with a scrubbed environment (`env -i`, both variables confirmed unset inside the call),
that the tool returns a complete graph without either key. Correct, reproducible, and not the
thing that decides anything. It then ranked the options for removing them, and every option in
the ranking inherited the same unexamined premise: that a line's installer is its consumer.

The measurement that settled it took two commands. `omp --help` names both variables in its own
credential contract — `GEMINI_API_KEY - Google Gemini models`, `MINIMAX_API_KEY - MiniMax
models`. `omp token minimax` returns the `.bashrc` value verbatim. With the variables unset, the
model catalogue drops from 86 entries to 32 and the credential lookup answers `No active
credential found`. **The installer named in the comment does not need the keys. The harness that
every terminal on this machine runs does, for 54 models.**

The comment was never false. That is the whole shape. A false provenance note — the kind
corrected elsewhere on this page, where a test comment claimed files were untracked when `git
log` showed the diff had existed since July — is a lie you can catch by checking it. This one
survives every check, because `codebase-memory-mcp install` really did write those lines. It is a
true statement about 2026-06 read as a statement about today, and the reader supplies the tense.

What makes it systematic rather than unlucky is an asymmetry in who annotates. **Installers
annotate their own writes; consumers never annotate someone else's file.** `omp` did not append
"and I read these too", because appending to a dotfile you do not own is rude and nobody does it.
So the only annotation a shared file carries is about its origin, the dependency that arrived
afterwards leaves no trace at all, and the surviving comment reads as authoritative precisely
because it is the only writing on the page. The longer a line lives, the more consumers it can
accumulate and the more stale its one comment becomes — the note ages in the wrong direction.

The near miss is worth stating in its own terms, because the failure mode would have been the
silent one this page keeps returning to. Deleting the lines does not error. `omp` starts, the
catalogue is simply smaller, and a model that used to resolve answers "No active credential
found" — a sentence that reads like a configuration you never had rather than one you removed
twenty minutes ago.

The check, and it is cheap: **before deleting a line because a comment says who put it there,
ask who reads it — provenance is not consumption.** The decisive form is a scrubbed-environment
A/B against the things that actually run from that shell, not against the thing named in the
comment: `env -u VAR` and count what changes. The earlier `env -i` run was the same technique
pointed at the subject the comment nominated, which is how a good instrument produces a true
answer to the wrong question.

## A twenty-first instrument: three identical answers from a verdict that had already gone stale

Measured 2026-08-15, testing `code.impact` against `taxiprime` once codebase-memory was reachable
again. Three calls, each varying `baseline` or `scope`, returned the same sentence: `unavailable:
every implementation of code.impact is down for repository taxiprime`. Three independent-looking
readings agreeing with each other is exactly the shape this page opened with, and it was exactly
as misleading here: they were not three readings of the provider. They were one reading of the
process asking about it, taken three times.

`symbol.calls`, same repository, same minute, no restart in between, answered cleanly — a real
call graph off codebase-memory. The provider was not down. Only `code.impact`'s belief about it
was, and nothing asking `code.impact` again could ever discover that, because each ask reads the
same cached verdict rather than the provider. `systemctl --user restart atenea`, and nothing else,
cleared it: the next call succeeded on the first try, against the same repository.

Same shape as 23, one layer over. There, a stale core answered a real handshake honestly and
wrongly about which tools existed; here, a stale core answers a real capability call honestly and
wrongly about whether a provider is reachable. Both are the same fact wearing different clothes: a
long-running process's belief about the world is not the world, and a check that can only ever ask
that process again inherits its staleness rather than testing it. A third instance of the identical
mechanism turned up the same day, unforced: `indexed_by`, once corrected in memory by `atenea
detect`, is not written back to the settings file, so the correction itself does not survive the
next restart — the same restart that would otherwise be needed to clear a stale `down`. Three
separate caches, three separate call sites, one missing operation between all of them: nothing on
this path re-asks a provider it has already formed an opinion about.

## A twenty-second instrument: a store that cannot tell failure from empty

Measured 2026-08-15, reading codebase-memory's own installed README (v0.8.1, undated) for the
first time this session. It describes the cache holding three projects — `4mans-beta`,
`taxiprime-app`, `Kena` — 72,464 nodes, 179,600 edges, 148MB on disk. None of the three exist
today. `list_projects`, called live, returns two projects on the entire machine: the current
repository's root and a throwaway test checkout in `/tmp`.

This entry does not name a cause, because nothing that survives names one. What could be
checked, was: the daemon did not run at all on 08-10 — zero journal entries that day — then ran
without interruption from 08-13 11:50 through 08-15 04:08, the exact minute of this session's own
retirement of the provider, with no delete event anywhere in that window. The installed binary
went from v0.8.1 (the README's own version) to v0.9.0 — the version running today, binary mtime
07-08 — on the same calendar day v0.9.0 was officially released. `daemon.log` recorded a
`0.10.0` handshake on 07-06, two days *before* v0.9.0 itself was released and over a month before
DeusData published any real v0.10.0; that entry was a local build carrying a version string
ahead of any tag this project ever cut, not an official artifact this install downgraded from.
Checked two sessions later, in full: the 07-08 install is an ordinary same-day release-day
update, ruling the version history out as a plausible mechanism for what follows — it is noted
here only because, before that check, it briefly looked like one. `delete_project` is a real,
documented CLI verb on this binary, and there is no audit record anywhere on this machine of
whether it was ever called. systemd's own journal keeps nothing from before 08-11; several
machine reboots in that window rotated it away.

Three projects and 179,600 edges are gone between a README that names no date and a today that
has one. The honest description of this store, right now, is that it cannot answer the one
question that matters most after a loss: whether it failed, or whether something asked it to
forget. Both leave the same disk footprint behind — a database holding less than a document says
it once did — and nothing this session could read tells them apart. That is not a gap in this
investigation. It is a gap in what the store itself can say about its own history, and no amount
of asking it again closes it.

## A twenty-third instrument: a rescue that needs more money than the thing it rescues

The reserved answer, built 2026-08-14, replaces a kill with a message: when a
turn has spent its read allowance, tell it to answer with what it has instead
of letting the ceiling take it empty. It was proven on a controlled A/B — same
card, same $0.90 share, one binary killing at the ceiling and the other
answering at completeness 0.90 for less money.

Run on a real nineteen-step plan the next night, it protected nothing. All
thirteen model-backed steps died at their ceiling with zero passes, $5.24 spent
against a $3.52 grant, output between 99 and 650 tokens each and no answer.

The receipts separate perfectly, across every step measured since the mechanism
shipped, on one number nobody had thought of as a threshold:

| read allowance | steps | outcome |
|---|---|---|
| above 70,000 input-equivalent tokens | 4 | every one answered (0.80, 0.85, 0.90, and one plain ok) |
| below 70,000 | 13 | every one died empty |

The number is not new. It is written in this codebase, in the doc comment on
the field that carries the allowance: *"the very first assistant event already
weighed 65,625 input-equivalent tokens — about $0.20 — because the CLI's system
prompt and tool definitions are cached on it… an allowance under ~70,000 buys
no reading at all on this CLI."* Measured, recorded, and then not converted
into the one thing that would have used it: an admission rule.

Below that line the mechanism does not fail quietly, it fires **too early**.
The allowance is crossed by the first event of the turn — the arrival of the
prompt itself — so the nudge lands before the model has opened a file, telling
it to answer with what it has, which is nothing. It reads on, and the hard
ceiling takes it exactly as before. The rescue was not absent; it was spent on
an empty hand.

So the binding constraint on a step is not the floor. **A step must be funded
past $0.84 on this repository and model** — the share at which half of it, the
read allowance, exceeds the weight of its own first event — where the floor
says $0.27. The floor is necessary and it is nowhere near sufficient, and a
plan can satisfy it on every step and still die on every step, which is what
happened.

### And it cannot protect an agent that calls no tool

Measured directly, on the production argv: a turn with no tools emits **two**
assistant events in 133 seconds — one at 31.6s and one that arrives with the
result — and both carry the usage of the prompt alone, `output_tokens: 2`,
never the answer accumulating behind them. A tool-using turn gets a fresh event
per round trip, which is what lets an accumulator climb and cross anything.

That leaves one usable observation for a toolless agent, at a quarter of the
way in, carrying a number that does not move. If the threshold is not already
crossed there, it never will be. `plan` at a $0.90 share is the one step in the
table above that clears 70,000 and died anyway, and this is why.

The mechanism is therefore **structurally limited to agents that call tools**:
`explore` and `reader` can be nudged, `plan` cannot, and the three mechanical
types never turn at all. That is a property of where the CLI reports usage, not
a bug to fix in this package — nothing on the stream would tell an accumulator
that a toolless answer is getting expensive.

### Closed 2026-08-15

`workflow create` now refuses on this number directly. The threshold is not a
second constant beside the floor -- it is derived, per `(repository, agent,
model)`, from the same stored row this page already distrusts hand arithmetic
on: `allowance.MinShareUSD` off the row's own `FirstEventTokens`, recomputed
on every `create` rather than typed once and left to go stale. The floor
stayed rather than being subsumed, and for a reason that is a property of a
price, not of the rules: the threshold dominates the floor only while a
model's measured rate stays under `$2.41e-5` per token, and today's opus rate
(`$1.0149e-5`) is comfortably under that line, not structurally under it. A
rule deleted because a currently-larger rule shadows it is the same mistake
this whole page is named for.

## A twenty-fourth instrument: a probe that works on one backend is not a probe

Measured 2026-08-15, enumerating what two raw MCP backends offer, with one
function used against both. Against `agent-device` it worked exactly as
intended: **55 tools, 215,174 bytes, 0.06 seconds**, every name listed. Against
`maestro`, the same function returned nothing and reported no answer after 2.4
seconds.

Read as a measurement, that is a dead server. It is not. Running the same
command with stderr no longer discarded shows the opposite:

```
mcp_viewer_ready http://127.0.0.1:10000
kotlin-logging: initializing... active logger factory: Slf4jLoggerFactory
MCP Server: Started. Waiting for messages. Working directory: /tmp
```

It started, it said so, and it exited **rc 0** when stdin closed without ever
writing a JSON-RPC frame to stdout. Newline-delimited JSON on stdin, which is
what the sibling backend answers, is not what this one reads. The silence was
the framing, not the server — and nothing in the silence says which.

Two things make it worse than an ordinary null result. The failure is
**asymmetric within one call site**: the same helper is correct and incorrect
depending on which binary it is pointed at, so the code cannot be trusted or
distrusted as a unit, and a suite that probes only the cooperative backend stays
green. And the probe **is not read-only**, though it was written as though it
were: `mcp_viewer_ready` is a web application on `127.0.0.1:10000`, started as a
side effect of asking a question. It went away with the process here. A probe
that leaves a listener behind is an instrument that changes the machine it is
measuring.

What settled it was not a better handshake. It was three channels that were
already working, each answering a different question, none of them this one:

| question | channel | reading |
|---|---|---|
| what is *allowed* | `atenea.toml`, read from source | six tools each, plus the four excluded Cloud tools by name |
| what is *offered right now* | `fixed-cost --repo`, a live handshake through atenea | atenea serving 42 tools, `raw.agent-device.click` the priciest schema on this machine |
| whether it ever *answered* | the receipt store, `~/.local/state/atenea/runs/` | `raw.maestro.list_devices` and `inspect_screen`, three calls, all `ok`, 2026-08-15 00:57–00:58 |

The receipts are the decisive one, and they are decisive precisely because they
are not a probe: they are what the server did when something real asked. A
backend that cannot be interrogated directly can still be observed working, and
the record of it working outranks any fresh attempt to reproduce it. The
published figure for maestro's ten offered tools therefore keeps its original
date — 2026-08-14, when a handshake did succeed through a client that speaks its
framing — rather than being silently re-dated to a day it could not be
reproduced on. An unreproducible measurement is not thereby wrong; writing
today's date on it would be.

## A twenty-fifth instrument: a timeout sized for the handshake, a hint sized for the wrong caller

Two defects in `agent-device`, surfaced restarting three Android emulators on
2026-08-15, one shape: something was calibrated against one operation and
handed unchanged to a different one that nobody had re-checked it against.

**The timeout.** `atenea.toml`'s `agent-device` backend carries one
`timeout = "15s"`, sized off a 50ms `initialize` handshake. `boot` is not a
handshake. Measured live twice today: Pixel_9 booted in **19.133s**, Pixel_10
in **18.309s**, both past the ceiling, so both attempts through Atenea
reported a timeout while the AVD kept booting underneath it -- a false
negative on every cold boot, not an occasional one. Reading
`internal/passthrough/stdio.go` settles what the field actually bounds:
`Spec` carries exactly one `Timeout`, and `send()` applies it identically to
the handshake and to every `tools/call` after, `boot` included. `config.go`'s
`[[mcp_server.tool]]` table parses only `name` and `effects` -- there is no
per-tool timeout to give `boot` instead. Raising the one number is the only
lever, and it has a price on the other five: `devices`, `snapshot` and `find`
answer in well under a second when the backend is healthy, so a longer
ceiling changes nothing there -- until one of them is actually stuck, at
which point a caller now waits for the same longer number before Atenea says
so.

**The hint.** Session `default` already held a stale claim before any of
this: "Pixel 10" at `emulator-5554`, recorded before serials were reassigned
across a restart -- live, `emulator-5554` is Pixel 8
(`~/.agent-device/sessions/default/requests/383caef68f8e7262.ndjson`, today
17:36:57). Session `cliente`'s own attempt, moments later, is the one with a
full trail. Resolving "Pixel 8" by name landed on `emulator-5556`, already
dead, and `adb: device 'emulator-5556' not found` came back as a
COMMAND_FAILED
(`~/.agent-device/sessions/cliente/requests/44c99ba333ba314c.ndjson`,
17:37:21). The failed attempt still recorded the claim: the retry,
`--serial=emulator-5554` this time -- the device's real, live serial -- was
refused with INVALID_ARGS, `Session "cliente" is already bound to android
device "Pixel 8" (emulator-5556)`
(`.../ceb3d026751db4d8.ndjson`, 17:37:48). A call that never reached the
device left the session wearing its serial anyway, and nothing invalidates
that when the emulator behind it restarts. The refusal's own hint reads `Run
agent-device session list to inspect active sessions... first run
agent-device close --session cliente` -- both CLI verbs. Neither is in the
six-tool allow-list Atenea exposes (`devices`, `boot`, `open`, `snapshot`,
`click`, `find`), and no parameter on any of the six schemas substitutes:
`open` carries the only `force` flag among them, and its own description
scopes it to overwriting a `--save-script` target, not to replacing a device
claim. Read at face value, a caller restricted to the six MCP tools has no
path to the hint's own remedy.

### The remedy exists -- it just does not reach the caller reading the hint

The first draft of this claim was that the remedy in the hint does not exist
at all. It does: run live, `agent-device close --session cliente` returned
`Closed: cliente`, `agent-device session list` went from the stale claim to
`{"sessions": []}`, and a fresh `raw.agent-device.snapshot` against the same
session name and the live serial succeeded cleanly afterward -- a real
accessibility tree, no error. The binding does not never expire, and a
surface does clear it. That surface is the CLI, reached by shell, which is
exactly what the six-tool MCP allow-list exists to route around. The hint is
not prescribing a remedy that does not exist; it is printing the one
write-up for a CLI operator into a channel that, by design, may have no CLI
underneath it.

Both defects are the same shape read twice: a number and a sentence, each
correct for the operation it was built against, handed to a different
operation without anyone checking the fit still held.

## A twenty-sixth instrument: an empty list read as an empty field

Found while looking for an MCP-side escape hatch for the session-binding
defect above, and pulled out into its own entry rather than left as
corroboration inside it: it is not a second case of the same shape, it is a
different and worse one. The pair above was a number and a sentence, each
correct for the operation it was calibrated on and wrong for the one it was
handed to. This one is two surfaces answering the identical question from
two different places, and neither says so.

`agent-device session list` reported `{"sessions": []}`. In the same
minute, `raw.agent-device.open` was tried against two live serials and
refused both times with `DEVICE_IN_USE`: `emulator-5554` held by session
`default`, `emulator-5558` held by session `conductor`. Asked again
immediately after both refusals, `session list` still answered
`{"sessions": []}`. Two sessions the enforcement path names by name and by
device are invisible to the read path that is supposed to be the caller's
window onto exactly that state.

This is worse than a stale entry that persists, because it inverts the
usual order of a check. The instinctive safe move -- list first, act only
if the target looks free -- is precisely the move that fails here: it does
not raise a false alarm, it gives a clean answer that is wrong, and a
caller who trusts it walks straight into the conflict it existed to
prevent. A follow-up read today of `agent-device device status` -- a third
channel, advisory claim files on disk, read "without starting or
contacting a daemon" per its own help text -- told a third and different
story again: three claims, `cli8`, `client` and `qa-driver`, all marked
`owner-process-dead`, naming neither `default` nor `conductor`. Three
surfaces, three answers, none of them the state the daemon actually
enforced when a real call was made.

`session list` was not deliberately distrusted until this session -- there
was no reason yet to doubt the thing named "list" -- and it failed the same
way the first two false reports on this page did: by answering fast,
looking exactly like what was asked for, and describing a different piece
of the system than the one the caller was about to touch.

## A twenty-seventh instrument: a value that governs every call and cannot be read back

The 45s ceiling from the entry above was applied at 20:36 tonight, and the question that
followed was the honest one: does the *running* process hold it, not just the file. The answer
turned out to need an instrument that does not exist.

The first attempt was cheap, clean, and proved nothing. `raw.agent-device.boot` against
`Pixel_9` -- already fully booted, `sys.boot_completed=1` confirmed moments before -- returned
in **173ms**, cross-confirmed against the daemon's own session log (`durationMs:173`, an
independent source reporting the identical number). A pass this fast is consistent with a 15s
ceiling, a 45s ceiling, or no ceiling at all. Success without duration near the boundary is not
evidence of which number governed it; the call never came close to testing what it was sent to
probe.

### A cold boot would not have settled it either

The next thought was to force a real cold boot: stop a live emulator, restart it, watch whether
it still succeeds now that the old 15s would have failed it. Two entries up, Pixel_9 and
Pixel_10 already generated exactly this data point by accident -- 19.133s and 18.309s, both past
the old ceiling -- and that is real signal, worth stating precisely for what it is and is not. A
cold boot succeeding proves the ceiling moved *above 15s*. It does not prove it is 45s. A boot
that passes at 19s shows only that the ceiling is above 19 -- a 20s ceiling, a 45s ceiling, and
an unbounded one all produce the identical pass. Boot duration is not a dial a caller can turn
to land a probe inside a specific window; it is an environmental fact, set by whatever the
machine is doing at the time, not by the person asking the question. Paying the risk of
stopping a live emulator, under tonight's own documented memory pressure, buys at best a lower
bound -- not the number that was actually asked for.

### Nowhere the number is written down

With the direct test ruled out, the next move was to look for the value somewhere cheaper:
somewhere Atenea already reports on itself. It is not in `atenea status`. The `servers` table
there lists all eight configured stdio backends -- `serena`, `context7`, `semgrep`,
`codebase-memory`, `agent-device`, `maestro`, `headroom`, `chrome-devtools` -- uniformly, one row
each, with columns for health, transport, `expose`, `checked`, and the full command line. No
column carries a timeout, for any of the eight; the absence is a property of the table's format,
not of any one backend's entry in it. It is not in `atenea catalog` either -- that command walks
the capability and implementation graph, and a backend exposed raw is neither, by definition:
`agent-device`'s tools never enter the funnel `catalog` is built to describe. And the startup log
prints exactly one line about configuration -- `settings /home/tutitoos/.config/atenea/atenea.toml`
-- the path read, never a value read from it.

The number is not lost. For `agent-device`, traced in full for the entry two above, it is parsed
once at process start into the `Spec.Timeout` field the passthrough layer applies to every call
on that backend (`internal/passthrough/stdio.go`) -- it sits in memory the entire time the
process runs, next to the process that is supposed to be the source of truth for it. There is
simply no path from that field back out to a caller standing outside the process, wondering
whether a hung call is still within budget or has already blown past it. **A configuration value
that governs every call on a backend, and cannot be read back from the running process governed
by it, is not provable except by finding an operation slow enough to fail on the old number and
succeed on the new one -- and nobody can schedule when that happens.**

Tonight it stops here: 45s is established by reading the source, the same way the file was
edited, not by observing the process obey it. That is a real gap between two different kinds of
confidence, and it stays open until some future call, run for an unrelated reason, happens to
land inside the 15s-45s band on its own.

### What the smallest fix looks like, not built tonight

The `servers` table in `atenea status` is the candidate, and it fits for reasons beyond
convenience. It already has the right cardinality: one row per backend, all eight already
enumerated. It already mixes a static configuration fact into what reads as a health screen --
the full command line printed in that row today is exactly as static and exactly as non-health
as a timeout would be, so a `timeout=45s` column would not be a new category of information
there, only one more field of a kind that row already carries. And it is free: the value is
already resident in the parsed spec the moment the process is up, unlike `checked` or the health
light, both of which cost an actual call to a backend to produce. `status` costs a fraction of a
second precisely because it does not probe anything -- `checked=-`, "nobody has asked yet" -- and
that is exactly the moment an operator staring at a hung call needs the number, not a moment
later. A machine-readable path -- `atenea intent --json`, which already exists to say how a
repository's calls will be answered -- is a reasonable second home for the same fact, but that is
a second surface, not part of the smallest version of this fix.

## A twenty-eighth instrument: green tests, an active service, and the wrong bytes

Every other entry on this page is about an instrument that measured something, and measured
the wrong thing. This one is about the absence of an instrument entirely, and it is the most
expensive absence recorded here: three separate measurements on 2026-08-15, all internally
consistent, all reported in good faith, all describing code that was not running.

`~/.local/bin/atenea` had an mtime of **2026-08-14 17:44**. The commit that introduced the
reserved-answer mechanism, `d142f87`, landed at **21:16** the same evening — three and a half
hours later. The binary answering every workflow step therefore could not contain the
mechanism, and did not: a single `grep` for the literal `finalize` message returns nothing from
those bytes. `$atenea` resolves through `os.Executable()`, so the orchestrator hands every
spawned step a copy of itself, and the staleness propagated perfectly and invisibly to all
nineteen.

What makes this worth its own entry is how thoroughly the surrounding signals agreed that
everything was fine. The service was `active`. The suite was green — 1,819 tests, 41 packages.
`config-guard` confirmed the config was current, and it was: that instrument does its job
exactly, and its job is the settings file, not the executable. `selftest` passed. `sync status`
reported no drift. Every one of those was true, and none of them was about the bytes.

### The three readings it produced

A plan step at 04:20 died at its ceiling having spent $1.53 of a $0.90 share, and the death was
read as a hole in the new mechanism for toolless agents. A nineteen-step workflow at 10:26 came
back 13 deaths from 13 model steps and zero partials, and was read as the mechanism failing
under load. Both were re-run against a binary that actually contained the code, and both changed:
13/13 deaths became 12/13 with one answer, total spend fell from $5.238 to $4.158, and
`cache_read` moved from 0 to 45,000–83,000 per step — the signature of passes happening at all.
The conclusions drawn from the first two readings were not merely unsupported. They were about
a different program.

### Why this one was cheap to fix and stayed unfixed

The answer was already in the binary the whole time. Go stamps `vcs.revision`, `vcs.time` and
`vcs.modified` into build info for anything compiled inside a work tree, and `go version -m`
prints them. No new bookkeeping, no cache, no state that can itself rot — which matters on a page
where half the entries are instruments that went stale. The check is a comparison between two
strings that both already exist.

It stayed unwritten because the question never sounds like a question. Nobody wonders whether
the binary is the source; the deploy is a step you remember doing. That is precisely the shape
of every entry here — a fact so obviously true that no one spends the one command it costs to
confirm it.

Now `stale-deploy`, deployed beside the binary it checks, with the comparison defended by a
hermetic test in `agent-config`'s `selftest`. One deliberate restraint in it: being behind HEAD
is reported but is not the verdict. This repository writes prose in the same tree as code, and a
check that goes red on every paragraph is one nobody reads by the end of the week — so only files
that enter the build make it stale, and which files those are is discovered by reading the
`//go:embed` directives rather than by a list that would drift. Its first run against this
machine found the binary stale *again*, from a `model.go` change committed an hour earlier.


## A twenty-ninth instrument: the same quantity corrected three times in one night

Measured 2026-08-15. A rule shipped that morning refused ten of eighteen steps at `$0.65`
each. By the end of the night the same rule asked `$0.04`, from the same stored row, with
no re-measurement of the row and no change to the arithmetic. Nothing was wrong with the
instrument. What moved three times was **which quantity a step's price is a function of**,
and each correction was found by measuring rather than by reasoning about the last one.

**First: file size.** The plan under refusal sized every step's share by the bytes it was
told to read, and a proposal to merge twelve readers into four rested on the same
arithmetic. Two probes, same code path, files differing 17.7x (452 vs 8,002 tokens): the
turn-2 cache write moved 41,973 -> 53,036. The file is 7.3x of the delta and 17.7x of
itself. Sizing by bytes predicted the wrong thing, and the merge proposal died with it.

**Second: round trips.** If not size, then per-call. Eight warm turns cost **less than two
cold ones**: `$0.4373` for eight against `$0.4935` for two, `$0.089` mean per warm round
trip against `$0.49` for the cold first tool call. Cost is not proportional to calls
either.

**Third: cache warmth, and it is one-time.** The block is `~40,000` tokens arriving at the
first tool result, written once and read at a twentieth of the price by everything after
it -- `cache_read` pinned at **exactly 40,227 on turn 2 and 4,772 on turn 1** across five
runs spanning different objectives, different files, different nonces, with and without
`--max-budget-usd`. Two arms with matched cold starts and distinct nonces were
superimposable per turn (1.02x, output variance), which also killed the `--max-budget-usd`
cache-busting hypothesis I had been carrying for an hour.

The rule that shipped in the morning charged **every** step for that one-time write. The
correction is one line of arithmetic -- `Weigh(input, output, prefix, 0)` instead of
`Weigh(input, output, 0, prefix)`, cache read instead of cache write -- and it moves the
admission threshold on the real explore row from `$0.65` to `$0.04`, twenty times, with the
cold figure still reported beside it as what establishing the prefix costs whichever run of
the hour is first.

### The two things this did not fix

The floor beside it, `$0.27`, is still the cold prefix priced and still charged per step,
and it is now the only rule refusing anything. It was **not** re-derived here, on purpose:
the stored row carries the turn-1 prefix (26,603 tokens), and the probes showed the cost
that actually kills a step is the `~40,000`-token block at the *first tool call*, which no
stored measurement contains. Deriving a warm floor from the row that is on disk would be
this page's own title one more time. It needs `atenea floor measure` to make a tool call and
record what comes back warm -- a measurement, not an edit.

And a free one, taken the same night because it cost nothing: every `agent-exec` run was
paying for a **session title nobody reads**. One opus request at `effort: high`,
`max_tokens: 64000`, carrying atenea's whole 2,453-byte commission, to name a session
discarded by `--no-session-persistence`. Against a loopback recorder it fired in **4 of 8**
control runs -- racy, which is why one run of it proves nothing -- and in **0 of 8** with
`--name`, then 0 of 6 against atenea's real argv. `--exclude-dynamic-system-prompt-sections`
was measured in the same harness and **rejected**: it moves 3,368 bytes out of the shared
cached system block and into `messages[0]`, next to the per-step-unique commission. Its
benefit is cross-user prompt-cache reuse; atenea is one user on one machine, so here it is a
strictly smaller cached prefix. Free is not the same as good, and the recorder can tell the
difference for `$0.00`.


## A thirtieth instrument: the probe that ends the entry above it

Measured 2026-08-15, hours later. The entry above closes by naming the measurement it could
not make: `atenea floor measure` had to make a tool call and record what comes back warm.
It does now, and the numbers it records are the ones the paid probes found: a `5,647`-token
prefix, a `41,927`-token block arriving with the first tool result, `$0.4935` for the pair.
`atenea floor` prints both prices — `WARM USD` `$0.02`, what a step pays; `COLD USD` `$0.06`,
the prefix priced once — and the admission rule charges the warm one wherever a row carries
it, the cold one where no probe has made a tool call yet, and says which in the refusal.

**The fourth wrong quantity was in the instrument, and no test saw it.** The probe stores a
per-token rate by dividing its receipt by what the receipt paid for. With no tool call that
was the prefix, and dividing by the prefix was correct for as long as the prefix was
everything. The first run of the new probe printed a step starting at `$0.21` warm and
`$4.16` on the establishing run, from a turn that had cost `$0.4935` — an 8.4x error, exactly
the ratio between the two messages, pointing the same direction as the bug the entry above
is about. Four suites were green when it printed that: the rate had no assertion that
compared it to anything outside itself. The check that caught it is the cheapest one
available and it is not a test — read the number the command prints and ask whether the bill
it came from says the same thing. The derived cold start now reproduces the receipt to the
cent (`$0.4935`), and the derived rate lands within 2% of the same model's rate measured
independently a day earlier. Both of those comparisons are now assertions; neither existed
when the arithmetic was written.

## A thirty-first instrument: two real steps, and the constant they check

Measured 2026-08-16, buying a receipt for a shape nobody had priced: not a probe that starts a
turn and stops, but a real reader step of a real plan, reading real route files and writing the
inventory it was commissioned for.

| step | bytes read | output | receipt | weighed usage | coverage |
|---|---|---|---|---|---|
| `admin-reservas` (lines 380-1065 of one file) | 34,077 | 6,690 tok | `$0.3751` | 75,017 | `1` |
| `booking-mod` (two whole files) | 66,069 | 5,130 tok | `$0.9880` | 197,605 | `0.95` |

**The constant now has a check from outside itself.** `allowance.tokensPerUSD` is `166,000`
input-equivalent tokens to the dollar, measured 2026-08-14 and until now compared only against
its own arithmetic — the thing lesson 31 was written about. Divide each receipt into its own
weighed usage and both answer the same number: **199,992** and **200,005** weighed tokens per
dollar, on two turns of different sizes doing different work. Five significant figures of
agreement between two independent runs is a real rate, and the shipped constant understates it
by 20%.

That direction is the safe one. `tokensPerUSD` appears in two places — the read allowance a turn
is handed, and `MinShareUSD` — and understating what a dollar buys makes the allowance smaller
and the admission threshold higher than the truth. Nothing is under-refused. It is not corrected
here, on purpose: one model on one machine is not enough rows to move a constant that governs
every repository, and the number that would replace it should come from more than two turns of
one agent type.

**What the pair actually revealed is not about the constant.** `booking-mod` was given a `$1.50`
ceiling — four times its measured cost — and was still nudged into answering early, because the
allowance is spent on *reading* and reading was 171,955 of its 197,605 weighed tokens. Reading
at 200,000 weighed tokens to the dollar against an allowance handed out at 83,000 to the dollar
means a read-heavy step must be funded **two to four times its own cost** before it can finish
reading, and a step funded at exactly what it costs will answer at about `0.95` with its gaps
named. Both figures are correct and they answer different questions: what the work costs, and
what the work must be granted to be allowed to complete it. A budget built from the first and
launched expecting the second buys partial answers and calls it a shortfall in the model.

The `0.95` was honest, which is the other half of the receipt: the nudged step named three
things it had not reached, two of which were exclusions its own commission had imposed. That
field existed and was empty on 208 of 210 rows twelve hours earlier.


## The general lesson

1. **Verify the instrument before the subject.** A measurement tool is a claim
   about what it observes, and that claim needs evidence exactly as much as the
   thing being measured. One `ps` column — the parent pid — was the difference
   between three defects and none.
2. **A number is not a finding.** Every figure in the false report was correctly
   measured. They described a process that no setting under test governed, and
   nothing about their precision hinted at it. Precision travels perfectly well
   in the wrong direction.
3. **A rotation observed halfway is not a rotation observed.** Two of four
   attempts is a data point, not a trend. Where the code states a ceiling, read
   the ceiling; where it is defended by a test, run the test. Both are cheaper
   than the dispatch that would have proved it, and neither costs thirty cents.
4. **Being right by accident still needs the correction published.** The final
   configuration did not change, so nothing on this machine would have exposed
   the error. An unpublished wrong reason is a trap left armed for whoever
   inherits the file, and the comment in `atenea.toml` justifying `on_demand`
   would have cited numbers that could never be reproduced.
5. **Let the system settle before reading it.** The fourth claim was a
   measurement of the fifteen dispatches that had just been fired at it. When a
   reading follows your own activity, the first hypothesis is that you are
   looking at your own activity — and the cheapest way to test it is to wait
   and read again, which costs patience and nothing else.
6. **A version string is not a build.** Two binaries linked from the same tree
   print the same version and can differ in every line that matters. What
   identifies a running program is the inode it holds, not the name it reports
   — and on a machine where the binary is installed by copying over a live
   path, they come apart silently and stay apart until a restart.
7. **Nothing on the screen is not a diagnosis.** A component that never loaded,
   one that threw at import, and one that ran perfectly and was dropped by the
   renderer all look identical from the outside. Where a fault is silent, the
   only way forward is to instrument the stages you can prove — a load counter,
   a mark per stage, the frame itself — and to keep them until the symptom has a
   name. A subtree that vanishes without an error is the same defect as a
   version string that describes another build: a system claiming, by saying
   nothing, that there is nothing to say.
8. **An instrument that can be silent must report that it is running.** A
   sampler that died and a machine where nothing happened produce the same log,
   and one of them is lying. The fix is not more care in the sampler; it is a
   line that says how many passes it made in the last five seconds, so that
   silence has to be either corroborated or contradicted. The same applies to
   the negative result generally: "nothing found" is only evidence when the
   instrument also says it looked.
9. **The observer's cost is part of the measurement.** Turning a tracer's detail
   up far enough to read a request body slowed the traced program past its own
   timeout: the setting that made the evidence legible destroyed the event to be
   evidenced. Nothing announced it — the run simply did not finish, which reads
   as a broken subject. Where an instrument sits inside the system it watches,
   the reading is only trustworthy at a detail level the system survives.
10. **Find the source before measuring the shape.** A refresh rate was measured
    correctly on `~/.claude.json` and reported as a fact about the number inside
    it — but that file is one client's cache, and another program on the same
    machine held a copy refreshed every ten minutes instead of twice in twelve
    days. The design built on it was cut to fit a scarcity that did not exist, and
    shipped. This failure mode is sharper than a broken instrument: a broken
    sampler eventually contradicts something, while this produces a long, correct,
    convincing argument with a real measurement under every clause. Everything
    about it reads as rigour, and nobody re-opens a question that was answered with
    figures. The cheap defence is one question asked before the first measurement —
    who else on this machine already reads this number? — and one habit after:
    write down the condition that would revive whatever you just cut, because a
    condition is a testable claim and a conclusion is not.
11. **A file-based auditor cannot see what never travels through files.** On
    2026-08-13 `mcp-agree` reported four clients in agreement. It reads
    declarations, and the declarations were correct: 45 tools provable from
    config — atenea 28, claude-mem 14, headroom 3. The wire carried **126**.
    The missing 81 were 32 Claude Code built-ins, which no config declares, and
    49 `mcp__claude_ai_*` connectors — Gmail, Google Calendar, Google Drive,
    Spotify — attached to the claude_max OAuth session and named in no file on
    the machine. Not a bug in the reading: a channel the instrument has no
    organ for. What makes it worse than a wrong number is that the auditor's
    green was *earned* on everything it could see, so its confidence scaled
    with its blindness. The correction is not a better file parser. It is that
    an instrument must know which of its claims its evidence can reach, and
    declare itself blind on the rest: the tool now prints two lines, one
    green-or-red and fingerprinted over the files that can move it, and one
    that only ever says `measured <date>` — because there is no file behind
    the account surface, no hash can cover it, and the honest states are
    *measured recently* and *unknown*, never *fine*. The first version of that
    second line was guarded by a clock, on the reasoning that where a
    fingerprint cannot reach an expiry is the only remaining guard. Two days
    later the channel moved a tool in fifteen minutes and the clock was deleted
    rather than shortened — see 12.
12. **An instrument's default failure direction must be *unknown*, not *fine*.**
    Every guard is wrong sometimes; the design question is which way. On
    2026-08-13 the account line stopped being guarded by a TTL and started
    being guarded by the identity of the shell session that took the
    measurement. The first implementation keyed that identity on `getsid()` —
    correct on a terminal, useless under the agent harness it was running in,
    which `setsid`s every command: three consecutive invocations reported
    sessions 2497260, 2497261 and 2497264 over one unchanged shell. Keyed that
    way the cache could *never* be current. That is a real bug, and it was the
    safe way to have it: its cost was a re-measurement — 2.5 s per client, zero
    tokens — and its output was `unknown`, which is true. Compare the two
    guards retired the same day. The TTL failed toward *fine*: it asserted
    currency about a channel it had stopped watching, and would have served a
    stale green for a week. The Dart toggle check failed toward *broken*: it
    asserted a duplicate that could no longer exist, because its subject had
    been deleted from the binary. Both spend something that is not the
    instrument's to spend — trust in the first case, attention in the second.
    A wrong `unknown` costs work. A wrong `fine` costs the reason the tool
    exists. When the two cannot be had at once, choose the one that fails into
    more measuring.
13. **A process's environment is not its configuration.** `/proc/<pid>/environ`
    is a snapshot taken at `exec`, and a program is free to write its own
    settings into `os.environ` afterwards. On 2026-08-13 the headroom proxy had
    **no** `HEADROOM_TOOL_SEARCH` in its environ, no config file naming it, and
    nothing in the systemd unit — while the feature it gates was active on every
    single request. It is seeded at startup from the savings profile (`coding`,
    whose `tool_search = True`) through `seed_proxy_env_defaults`, which uses
    `setdefault` so an explicit user setting still wins. Reading the process
    environment — the obvious source, one step better than reading a config file
    — reported *off* for something that was *on*. This is the third instrument in
    one day to answer a question about live state from a place the state does not
    have to pass through: config files that describe intent, a clock that guards
    a channel it stopped watching, and now an environment that describes only how
    a process started. The rule generalises past this stack: when the subject can
    be changed after the source is written, the source is documentation, not
    measurement. Ask the running thing what it is doing — here, one request and a
    look at `transforms_applied`.
14. **A limit a client enforces is not a limit the server enforces.** Both are
    real; they are different numbers about different machines, and only one of
    them can refuse you. On 2026-08-13 the same model, on the same account,
    accepted 340,019 tokens from omp and refused 215,000 from OpenCode — because
    OpenCode's path runs the Claude Code CLI under an alias with no `[1m]` tag,
    so the CLI blocked locally at its own `200,000 − 20,000 − 3,000` while the
    API's wall stood at 1,000,000 the whole time. Read a ceiling out of a binary
    and you have learned when that binary will stop; you have not learned when
    the endpoint will. The endpoint answers only when asked, and asking is
    cheap in exactly one direction: an over-limit request is refused before it
    is billed, so probe a wall from above.
15. **Measure the process's variance before comparing configurations.** Eleven
    runs were interpreted one sample per cell, and three sections were written
    naming causes, before anyone ran the same cell twice. The same commission,
    unchanged, produced `0, 0, 5, 0, 4` explorer steps — both outcomes the
    comparison was distinguishing, with nothing varied. A single run per cell
    cannot separate an effect from noise, and a comparison built on two of them
    is not controlled however carefully the variable was isolated. The null
    experiment is the cheapest run on the page: the same cell, twice, first.
    Two properties make it mandatory rather than good practice — an input
    produced by a model is never the same input twice, and a run that writes to
    the table its successor reads has already changed the next cell.
16. **Freeze the input before comparing prompts.** When a stage's input is
    another model's answer, "the same task twice" is not the same input twice,
    and every comparison downstream inherits that spread. Recording one real
    assignment and replaying it verbatim turned a $40 question into a $7 one
    and collapsed the noise from `0, 0, 5, 0, 4` to `3, 3, 3, 3, 3`. Capture
    the card at the boundary the process actually reads, not a reconstruction
    of it: a hand-built copy here was missing the served cost table, ran
    cleanly, and would have measured a different configuration than the one
    shipping.
17. **A feature can work and still be aimed at the wrong problem.** The cost
    table was built to stop under-allocation. It never moved allocation in any
    configuration — a contract bug did, from 25% of the grant to 100% — while
    what it did move, badly, was something nobody was watching: the plans it
    informed explored 2–4× less for the same money. Ask what a feature changed,
    not only whether it changed what it was for. The second question is the one
    with an answer prepared in advance, and it is the cheaper half.
18. **Name the experiment you could not run.** An unmeasured half is not a hole
    in a report; it is the part of it somebody else can act on, but only if it
    arrives as a question with a procedure attached. On 2026-08-14 a defect was
    split into what was on the wire — five requests, five rejections, all at
    `content.5` — and what was not: which mutation produced that block, which
    needed a stream capture and a source read this machine could not do. The
    unmeasured half named three candidates ordered by fit, refused to pick, and
    stated the assertion that would separate them. It was reproduced and fixed
    by someone with the access, faster than the measured half. The lesson is not
    that the leading hypothesis happened to be right — being wrong there costs
    nothing when it is labelled. It is that *I do not know* is a dead end when it
    stands alone and a work item when it carries the test, and that the cost of
    labelling it is one sentence against the cost of a confident guess that gets
    believed.
19. **A search that finds nothing is a reading with two explanations.** Absence
    of evidence and a broken matcher produce byte-identical output, and the
    broken matcher is the one that agrees with whatever you expected. Both units
    defects of 2026-08-14 were the same failure — sides of a comparison in
    different units — but only the absurd one, `15563 changed`, could not be
    believed. The plausible one, a clean `0` restart lines from a log whose every
    token is wrapped in ANSI escapes, was already written into a report as proof
    the API had not moved; the same escapes had broken a different scanner
    twenty-four hours earlier. So before a zero is allowed to mean anything, make
    the instrument produce a one: match a line you know exists, on the same
    stream, through the same pipe. A positive control costs one command, and it
    is the only thing that separates a quiet system from a deaf one.

20. **Check a limit against the floor of the thing it limits.** A ceiling is
    arithmetic about a process, and arithmetic that has never been compared to
    what the process costs is a guess with a number's authority. Seventeen of
    eighteen steps were funded below the price of one turn — $0.35 of system
    prompt and tool schemas before a file is opened — and every layer above
    treated those figures as budgets: the planner wrote them, the engine
    dispatched on them, the receipt reported against them, and the fix built to
    make them survivable could not fire inside them. Nothing in the chain asked
    the only question that mattered, which is whether the limit is larger than
    the smallest thing it can permit.

21. **A fixture is a record of your belief, not a copy of the far side.** A test
    built on one cannot fail for the only reason that matters, and it reports
    that inability as a pass. Building a provider on 2026-08-14 produced four
    integration defects in a row, three of them sitting under a green test: an
    optional interface silently dropped by a wrapper, a decoder written to match
    a hand-typed envelope the server never sends, an error rule made unreachable
    by a prefix the mock did not add. Nineteen adapter tests and 1,769 across the
    tree all agreed with an integration that could not complete one call. The
    other instruments on this page produced a wrong reading; this one produced
    agreement, which is harder to notice because agreement is what a test is for.
    So call the real thing once before writing the fixtures down — the envelopes
    took five minutes to print and made all four defects unwriteable. Fixtures
    are how you *keep* behaviour you have observed, never how you *learn* it.

22. **A `ppid` records who forked a process, not who governs it.** The two
    facts coincide only until the fork-time parent dies or the child calls
    `setsid()`, and neither event rewrites the column — it goes on reporting
    an accurate, irrelevant answer for as long as the original parent happens
    to still be alive. A daemon measured 2026-08-15 still showed a disposable,
    stray bridge as its parent more than a day after calling `setsid()` at
    launch, because nothing had yet killed that bridge to make the column
    catch up. It was killed later, for a reason that had nothing to do with
    this finding — clearing the stray session it belonged to — and the column
    updated within the same second, unprompted: `ppid` read 2018103
    immediately before the signal and 1638 immediately after, with nothing
    else about the daemon's own state disturbed. The lesson did not need a
    second measurement to hold. It got one anyway, live, in the middle of
    doing something else. Read a `ppid` for what forked a process. For what
    currently holds it, read the process group and session it actually runs
    under, or better, an explicit lifecycle contract — not a column that was
    last true at fork time and has not been asked again since.

23. **A live connection proves the channel, not the content.** A handshake that gets a real
    answer from a real process confirms the process is up and speaking correctly — nothing about
    whether that answer reflects what changed upstream of it since. `mcp-agree --live` spawns
    the same bridge command a client would and reads back a real `tools/list`, measured
    2026-08-15 against a config staled on purpose: the count did not move, because the bridge
    relays a long-running core's memory rather than reparsing anything, and the core's memory is
    exactly what a live socket cannot audit from outside itself. Liveness answers *is this
    reachable*. Freshness answers *is this current*. A check built to answer the first can stay
    green forever on a subject that has already failed the second.

24. **Verify the pair, not the halves.** Two changes that each remove ambiguity about the same
    value can both be correct, both be verified, and still fail together — because agreement is
    a property of the pair and neither half holds it. Measured 2026-08-15: a ported workflow
    proven green in a clean clone, and a `packageManager` pin proven green against a frozen
    lockfile, composed into `Multiple versions of pnpm specified` and died in five seconds.
    Isolation testing, the discipline this page keeps urging, is not weak evidence here; it is
    silent, since an interaction defect lives in no component. One `grep` across both files
    would have shown two declarations where the entire purpose was to have one. Worth noting
    that this was committed while writing the commit message naming the day's pattern as *two
    places that should agree with nothing checking that they agree* — knowing a shape by name
    is not immunity from it, which is the reason to keep a check rather than a resolution.

25. **A comment records who wrote a line, never who reads it.** `# Added by
    codebase-memory-mcp install` above two API keys in `~/.bashrc` was true, months old, and
    read by three people as a statement about who needs them. Measured 2026-08-15: the named
    installer runs fine with both unset, while `omp` — unmentioned, because consumers do not
    annotate other people's dotfiles — resolves 54 of its 86 models through exactly those two
    variables. Installers annotate their own writes and consumers never do, so a shared file
    records only its origin and its one comment ages in the wrong direction. Provenance is not
    consumption: ask who reads the line, with `env -u` against what actually runs, not `env -i`
    against the tool the comment nominates. Deleting them would not have errored — the
    catalogue would just have been quieter by 54.

26. **A cached verdict about a provider is not the provider, and re-asking through the same core
    never tests the difference.** `code.impact` answered `down` three times running for a
    provider that was, the whole time, answering `symbol.calls` cleanly, because atenea's
    long-running core latches a reachability verdict per capability once observed and has no path
    back from `down` to `up` except a full restart. `indexed_by` has the identical gap from the
    other side: a correction made in memory by `atenea detect` is never written back to the
    settings file, so it does not survive the restart that would otherwise be needed to clear a
    stale `down`. Three consistent answers from a channel that cannot revise itself are not
    corroboration; they are the same stale opinion, read again.

27. **A store that lost data and a store that was told to forget it look identical from
    outside.** Three projects, 72,464 nodes, 179,600 edges, gone between a README's own account
    of itself and a live query today — real, confirmed by asking the current store directly, not
    a reading error. What can't be recovered is which kind of gone: `delete_project` is a real,
    callable verb with no audit trail on this machine, and the daemon's own journal was rotated
    away before the window that would have mattered. A system's data can be checked against its
    own claims about itself. A system's history cannot be checked at all once nothing recorded it
    happening. Log the action, not only the resulting state, or the two failure modes stay
    permanently indistinguishable from the one place anyone would go looking.

28. **A green check is about whatever it checks, and the deployed bytes are usually
    nobody's.** An active service, 1,819 passing tests, a config confirmed current and a
    clean sync all held while `~/.local/bin/atenea` was three and a half hours older than
    the commit whose behaviour three measurements were attributing to it. None of those
    checks was wrong; the gap was that no check took the executable as its subject. Ask
    what each instrument's subject actually is, and the one nobody named is the one that
    will produce confident numbers about a program that is not running.

29. **A right measurement that stays a comment is not a control.** The other
    entries on this page are wrong measurements or wrong instruments; this one
    was neither. 65,625 input-equivalent tokens -- a real turn's own first
    assistant event, cold-equivalent -- was measured 2026-08-14 and written,
    correctly, into the `ReadTokens` doc comment the same day the floor check
    shipped. Thirteen model-backed steps, funded with allowances of
    9,960-33,200 input-equivalent tokens, died empty the following night --
    $5.24 spent against a $3.52 grant -- and the floor check refused none of
    them, because it was checking a different number than the one that killed
    them. The question after measuring something correctly is not "is this
    written down" but "which admission rule now refuses what this number says
    is impossible" -- and until `workflow create` asked that question of this
    one, the answer was none.

30. **A one-time cost charged per step is a wrong price, not a safe margin.** The
    conservative reading -- "any step may be the first to pay for the prefix, so
    charge them all as if they were" -- was written into a doc comment as
    prudence and shipped as an admission rule. It was off by 20x, and the
    direction it was wrong in was not safe: it refused ten of eighteen adequately
    funded steps and would have had a $3.50 plan re-priced at $15.17. Before
    pricing anything per step, measure whether the quantity recurs. `cache_read`
    pinned to the same integer across five runs of different work is what
    "one-time" looks like on the wire; the pessimistic assumption looks like
    nothing at all, because nobody measured it.

31. **Arithmetic checked only against itself passes forever.** A derived rate had
    tests for its shape, its rounding and its absence, and every one of them
    computed the expected value the same way the code did. It was 8.4x wrong for
    a day and green the whole time; what caught it was running the command once
    and comparing the number it printed to the bill it was derived from. Every
    derived figure needs at least one assertion whose right-hand side comes from
    outside the derivation -- a receipt, or the same quantity measured another
    way. Without one, the test is a transcription of the bug.

32. **A sentinel shared by "absent" and a real value erases the difference for
    good.** Third instance on this machine, three subsystems, one shape.
    `Charge.USD` is a pointer because an unmeasured cost read as `$0.00` would
    pass an unpriced run off as a free one. A stored floor of zero is a real
    measurement -- `reviewer` calls no model -- so `Get` had to hand it back
    rather than treat zero as absence. And `coverage()` returned `nil` for both
    "the model claimed 1" and "the model claimed nothing", which is why 62
    stored `ok` steps could not answer which had happened: the column was
    populated 1% of the time and every reader of it was guessing. The first two
    were caught while designing the field. The third shipped, because the
    collapse was in a function that *dropped* information rather than in the
    type that stored it -- a pointer cannot protect a field its writer nils out
    on purpose. Ask of every optional field: which two facts share this
    sentinel, and would I be able to tell them apart from the record a week
    later?

The design of this project is one long argument that a system should never claim
more than it has looked at. This was that argument arriving from the outside, at
the person making it.
