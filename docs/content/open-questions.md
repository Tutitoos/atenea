---
title: Open questions
weight: 7
---

# Open questions

Not gaps against the design — the design never asked for these. Noticed
using Atenea day to day, not yet decided, written down as they came up so the
next session starts from this instead of memory. An entry leaves this list
by being answered, not by quietly falling off it.

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
