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

**Narrowed, not closed.** The service now starts in Atenea's own state root
rather than in `$HOME`, so the shipped `path = "."` no longer names a home
directory as a repository to search. That makes the failure empty instead of
private; it does not make a relative repository path mean anything useful to a
daemon. The remaining decision is whether the shipped catalog should declare a
repository at all, or whether a fresh install should start with none and say
so. It cannot be settled by making `path` absolute alone: the `"."` is the
mechanism by which a fresh CLI install works against the tree you are standing
in, and ten end-to-end tests exist because that behaviour is wanted.

**Still open, and still a decision rather than a defect.**

*A rule about when refusing is required.* `atenea floor measure` needs
`--repo` and `--agent`, and both gate spending only as a side effect of being
required values. Nothing anywhere says that a flag which can spend money must
carry a gate. The next flag shaped like the old `--agent` default -- one that
spends by quietly picking a value nobody asked for -- is stopped only by
whoever writes it remembering to check. Making that a rule means deciding what
the rule is: every command that may spend requires a TTY confirmation, or every
command that may spend must name what it spends on, or something narrower.

*Retention.* Run receipts and the trace database grow without bound, and the
shape of the fix is known: a `Prune(before)` on `internal/checkpoint`, a
`DELETE FROM agent_trace WHERE ended_at < ?` on `internal/trace`, a due-marker
like `metrics.CompactIfDue`, and a window in the settings file. What is not
known is the window. A receipt is the only record that a commission happened,
so the number is a decision about what this machine is allowed to forget, and
writing the verbs before that decision would leave two exported methods nobody
calls -- the shape this project criticises in writing.

*How `Outcome.SpentUSD` says "unmeasured".* It is a plain `float64`, so zero
means both "this cost nothing" and "nobody measured it" -- the exact collapse
`Charge.USD` is a pointer to avoid, three types away in the same package. The
two shapes that would fix it are a `*float64` or a `SpentUSDKnown bool` beside
it, and both are a contract change with an entry in `version.go`. It is
recorded here rather than done because `Outcome` is on the Runner seam, which
is the one part of this package that cannot be extended additively, and a
change there wants to travel with whatever else that seam is ever going to
need.

*Early warning on omp's private store.* The limits widget reads another
product's `agent.db` by hand, with no contract behind it, and a renamed field
there breaks it. Two halves: the widget now says `sin lectura` when it cannot
read what it found, so the failure is visible rather than silent, and a test
shouts on a machine that has the store. The half that does not exist is CI:
there is no omp there, so there is no store, and a committed fixture would
check this repository's idea of the format -- which is the error the widget was
built on. Accepted as a limitation, not deferred as work: the early warning is
the maintainer's machine or nothing.

*Quiescing the databases before a backup.* `metrics.duckdb` and `traces.db` are
copied hot, so a snapshot is crash-consistent and not consistent. The document
for `internal/backup` now says so plainly, with what is lost on restore. Doing
better means the tree copier has to know which files are databases and load
their drivers, which belongs to whoever commissions the snapshot rather than to
the copier. The decision is whether a backup that can lose the last flush is
acceptable for these two files, given that both are derived data.

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
