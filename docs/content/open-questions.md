---
title: Open questions
weight: 7
---

# Open questions

Not gaps against the design — the design never asked for these. Noticed
using Atenea day to day, not yet decided, written down as they came up so the
next session starts from this instead of memory. An entry leaves this list
by being answered, not by quietly falling off it.

## What the 2026-08-25 audit settled, and what it did not

A full audit of the repository confirmed 209 findings and the remediation
closed almost all of them. Three entries on this page and several in
[`What is not built yet`]({{< relref "not-built-yet" >}}) are affected, so the
state of each is recorded here rather than left to be inferred from a diff.

**Closed by the remediation, and no longer open questions.** The funnel's cost
comparator was not a strict weak order and could seat a provider beaten on both
axes; `max_tokens` was compared against a total that included the provider's
cache, so ordinary turns were refused after being paid for; a workflow launched
over MCP resolved its repository from the service's own working directory; two
MCP tools committed money and effects without crossing the chat's grant; and
`Store.Own` was not atomic, so two Ateneas could execute one graph. None of
these were questions anybody had asked -- they are listed because the pages
that describe how the funnel and the workflow behave now describe something
different.

**Narrowed on 2026-08-25 and closed on 2026-08-26.** The service was pointed at
Atenea's own state root rather than `$HOME`, which made the failure empty
instead of private without making a relative repository path mean anything to a
daemon. The entry below records how that was finished; this paragraph is kept
because the intermediate state shipped nowhere but was the reasoning that led
to the split.

**Answered on 2026-08-25, and built.** Four decisions were taken rather than
recorded, so they leave this page by being answered:

*Retention is ninety days.* Run receipts and agent traces are pruned by a
`[retention]` block, `keep = "2160h"` and `every = "24h"`, on a lane of its own
guarded by a mark in the trace database. Only CLOSED records go, and by when
they ended: an open receipt is a commission somebody may still resume and an
open trace is the evidence that a run died, which is the one kind worth keeping
past its age. `keep = "0s"` keeps everything, which is the right answer for a
state root managed elsewhere and is now sayable rather than implied. The
measurement base is untouched -- it has a retention ladder of its own and grows
in detail rather than in rows.

*`Outcome.SpentUSDKnown` says whether the zero is a price.* A bool matching
`CostUpdate.Known` rather than a pointer matching `Charge.USD`, because
`Outcome` travels on the Runner seam, which cannot be extended without breaking
every implementer: an adapter built against 3.2.0 goes on compiling and leaves
it false, which reads as "nobody said" -- the honest answer for an adapter that
never had a way to say otherwise. The core refuses a charge reported without
it, because a figure with no measurement behind it is a number and not a price.
The receipt and the `--json` output carry the distinction too; `charged_usd` is
a pointer there, so present-even-as-zero means measured and absent means
nobody said. Contract 3.3.0.

*The backup settles the tree before it copies it.* `SnapshotIfDue` takes a
function the caller supplies, run immediately before the copy and only when a
copy is actually due, whose failure stops the copy. The core passes one that
flushes the measurement batch, issues a DuckDB `CHECKPOINT` and runs
`PRAGMA wal_checkpoint(TRUNCATE)` against the trace store. It stays a parameter
because the copier walks a directory and deliberately does not know which files
in it are databases.

*Early warning on omp's private store: accepted as a limitation.* The widget
reads another product's `agent.db` by hand, with no contract behind it. The
silent half is already closed -- it says `sin lectura` when it cannot read what
it found -- and a test shouts on a machine that has the store. CI cannot give
the early warning: there is no omp there, and a committed fixture would check
this repository's idea of the format, which is the error the widget was built
on. Not deferred as work; the early warning is the maintainer's machine or
nothing.

**Answered on 2026-08-26, and built.** The two the register was still holding.

*A relative repository path is a command's convenience and a service's
refusal.* `path = "."` is what makes a fresh install work against whatever tree
you are standing in, and the CLI is the thing standing somewhere; a daemon
stands nowhere. The service now refuses a relative path by name, with the one
command that fixes it, and `config init` writes what `"."` meant at the moment
it was typed — so the documented onboarding produces an absolute path and the
convenience survives exactly where it makes sense. The embedded settings keep
the relative value, because a command with no settings file has to work.

*A flag on a command that spends outside a commission may not carry a default.*
That is the rule, it is written above `floorMeasure` where the next such flag
will be added, and a test enforces it — by reading the source, because the
source is where a default is written. A second test closes the list from the
other end: anything whose own help says it spends real money must be held to
the rule, so the register cannot go stale by somebody forgetting to add a
command to it.

Money everywhere else in Atenea is a permission: granted per commission, split
between the steps, refused when it runs out. A step spending its share is the
design working, and holding `task` or `ask` to this rule would be holding the
whole product to it. The rule is about the commands with no grant above them,
where the only thing bounding the spend is that a person named what was about
to happen — which is a property a default removes silently, and which nothing
but a rule can keep.

## Channels that leave no trace on disk

Three separate surfaces moved on 2026-08-13 with nothing written down anywhere
a later reader could find. One pattern, not three incidents:

- the 49 `mcp__claude_ai_*` connectors, attached to the claude.ai account and
  named in no file on the machine;
- the `ListAgents` built-in, which appeared on the wire inside a fifteen-minute
  window with no local file touched and no binary upgraded;
- claude-mem mounted twice per omp session. **omp records nothing about MCP
  spawns**: 67 log files from that day, zero lines mentioning a server
  starting. The proof that two backends existed rather than one came from
  `/proc` — live parentage and start times — and would have vanished with the
  next restart.

The open question is not how to log more. It is which of these channels should
be *required* to leave a trace, and which are honestly unobservable and must
therefore be measured on demand and reported with a date. `mcp-agree` answers
the third case today by counting routes on the wire; the first two it can only
timestamp. Deciding that split is the work, and it belongs before the next
instrument is written, not after it reports green.

## Root multiplication in Dart projects

omp creates one `dart-analyze` and one `dart-test` per detected root
(`for (const _ of roots)`). `~/Desktop/taxiprime-app` holds seven
`pubspec.yaml`, four of them inside `.upgrade-backups/`. Unverified whether
that directory is excluded; if it is not, stale copies of an app are analysed
on every run. Cost is processes and wall clock, not tokens. Noticed while
retiring the Dart MCP check, which defended against a different failure.

## omp's prompt prefix moves between identical runs

Two consecutive `omp -p "Reply with exactly: ok" --no-session` runs, one second
apart through headroom on 2026-08-13, both wrote cache and read none:
`cache_write` 25,909 then 25,943, `cache_read` 0 both times. Thirty-four tokens
differ between two invocations of the same prompt, which is enough to miss the
prefix cache every time. Earlier the same day the same pair warmed cleanly, so
this is not constant. Cause unchased: something in omp's system blocks — a
timestamp, a session id, a rules or skills listing — is not stable across
invocations. Cost is a full cache write per run instead of a read, roughly 12×
on the input side of a cold turn. Not headroom's: the churn is in the body omp
hands over.

## What a citation should point at: the text, or the route

`auth-mod` cited `auth.routes.ts:49` for the claim `/auth/email/login` — the full mounted path,
prefix included — when that line declares only the local sub-path
(`fastify.post('/email/login', ...)`); the `/auth` prefix is applied at a different, uncited
line entirely. Not a one-off: a nearly identical citation in the same run (`admin-mock-sim`,
`/admin/test/mock-places` against `admin.routes.ts:4404`) makes the same move and is
indistinguishable in intent, only escaping the checker because of an unrelated second citation on
its line (see `measuring-the-wrong-process.md`, instrument 42). Writing the effective route while
citing the line that only has the sub-path is how people naturally describe an API surface — the
prefix is real, it is just declared somewhere else.

The open question is what to tell an agent to cite: the line where the text it quotes literally
appears, or the line where the route is actually mounted, which may be a different file and may
not exist as a single line at all when a prefix is composed from more than one plugin
registration? Whichever is chosen has to be sayable as one instruction a citing agent can follow
while writing, not a rule this checker infers after the fact — the checker can verify a citation
once written; which location counts as *the* citation for a composed route is a decision about
what to ask for, not about how to check what was given.
