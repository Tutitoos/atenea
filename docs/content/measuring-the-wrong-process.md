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

The design of this project is one long argument that a system should never claim
more than it has looked at. This was that argument arriving from the outside, at
the person making it.
