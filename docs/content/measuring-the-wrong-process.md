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

### State on the night

The section stays in for now. What is known is an unexplained large effect,
measured against one input, on a feature nobody has shown to help — and no
mechanism is proposed here, because the last two offered tonight were both
plausible, both consistent with everything then measured, and both wrong.
Removing it is a decision worth taking rested.

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

The design of this project is one long argument that a system should never claim
more than it has looked at. This was that argument arriving from the outside, at
the person making it.
