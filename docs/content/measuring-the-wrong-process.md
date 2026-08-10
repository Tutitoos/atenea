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

The design of this project is one long argument that a system should never claim
more than it has looked at. This was that argument arriving from the outside, at
the person making it.
