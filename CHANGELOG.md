# Changelog

Notable changes to Atenea, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Two numbers are versioned here and they move independently:

- **Atenea**, the product, in alpha at `0.x.y`. It reaches `1.0.0` when it goes
  stable, not before.
- **`pkg/contract`**, the wire format client adapters compile against, already
  at `1.x.y`. It is a commitment from the first release: an adapter is code
  somebody else builds against, and alpha is not a licence to break it weekly.

A release tag is `vMAJOR.MINOR.PATCH` and names the product version.

## [Unreleased]

### Fixed

- **A failure is no longer a price.** The cost base averaged every attempt
  together, so an implementation that refused instantly — not logged in, no
  index, no server — recorded a stream of very fast, very cheap calls, became
  the cheapest thing on the board, and was handed every commission from then
  on. Health did not save it, because nothing probed it. Failing cheaply paid
  better than working, and the outage reinforced itself: found on a real
  machine, where twelve refusals in a row moved the funnel off a provider that
  worked and onto one that could not answer at all.

  Only successful calls are averaged now. Attempts and failures are still
  counted — they are what health reads — but they divide nothing, and an
  implementation with a record and no successful call falls back to its
  declared estimate. The trace says so outright instead of leaving a reader to
  wonder why the ranking ignored a base full of rows.

- **Health learns from the record, not only from probes.** Running a step is a
  probe and always was, but that verdict lived in a catalog held in memory, and
  Atenea is a CLI at least as often as it is a service: every fresh process
  started with a clean catalog and forgot every fault before it. A provider
  that refused every single call stayed healthy forever.

  Three failures in a row in the same bin is now an outage — the provider
  leaves the funnel and the trace names the count, the bin and what the
  provider actually said. Three in different bins is degraded instead: a
  provider in trouble with no single cause ranks last but stays, because the
  funnel would rather use a flaky provider than none. Both verdicts expire
  after five quiet minutes, because a provider health has dropped is one
  nothing calls, and nothing that is never called can prove it recovered. A
  streak only ever makes a candidate worse than a prober found it.

- Test runs no longer write into the measurement base of the machine running
  them. The CLI suite searches `/srv/api`, which exists nowhere, so it filed a
  failure on every run; with health now reading the record, enough of those
  made the funnel refuse a working provider on a developer's box and nowhere
  else. The fixture pins its own base.

### Added

- **`atenea metrics`** prints what the base measured, per capability,
  implementation and repository: attempts, failures, how many were priced, the
  average of the calls that worked and the worst single call. The three counts
  sit together because the gap between them is the diagnosis.
- **`atenea metrics clear`** forgets it, narrowed by `--capability`,
  `--implementation` or `--repository`. The base is the only thing here that
  decides behaviour and cannot be edited by hand — true by construction, and
  still true long after the machine it describes has been fixed. Clearing all
  of it needs `--all` on top of the word: it is the one act that destroys
  something nothing can rebuild. Attempts and folded buckets go together, since
  leaving the folded half would let the numbers reappear an hour later.
- A migration carries the successful half of each rollup in its own columns.
  A legacy bucket that mixed successes and failures cannot be split after the
  fact, so it keeps its counts and contributes nothing to cost — the tempting
  repair, keeping the count and zeroing the sum, would invent an average of
  zero and re-create the bug.

### Documentation

- The settings file **replaces** the built-in defaults rather than patching
  them. A file holding only an `[orchestrator]` block is a complete description
  of an Atenea with no catalogue at all: it boots, reports red, and answers
  `unknown capability` to everything. `atenea config init` writes the whole
  file to start from. This was true from the first release and written nowhere.
- `.serena/` is ignored. Serena writes a project config into whatever
  repository it is pointed at, describing one machine and belonging to nobody
  else.

## [0.1.0] - 2026-08-02

First tagged release. Atenea decides and delegates: `goal -> capability ->
implementation`. It runs as a core outside the CLIs it serves, four capabilities
answered by three adapters, with a funnel that learns which provider to pick
from what it measured rather than from what somebody typed.

Built as eleven bricks, each leaving something that runs.

### The core and the funnel

- **`pkg/contract`** — the versioned seam between the core and its adapters.
  `Capability` with a checkable input/output schema, `Implementation` in four
  blocks (capability, constraints, cost, health), `Repository` as the unit of
  work, and six failure bins every adapter sorts its far side's errors into.
- **Capability Registry** — the catalogue, safe for concurrent chats, refusing
  orphan implementations and handing out defensive copies.
- **The selector** — a funnel, in stages that each answer one question:
  constraints say who *can* work here, reach says who is *wired up*, health says
  who is *available*, and cost ranks whoever is left. A standing user rule
  outranks the automatic ranking. Every stage records what it dropped and why.
- **Settings** — one declarative TOML file with embedded defaults, so a fresh
  install boots with no file at all. Unknown keys are refused rather than
  ignored.
- **The status screen** — two heights: Atenea as a whole, and one line per
  implementation. Failure bins map onto distinct exit codes.

Cost was deliberately left out of the funnel until real measurements existed
(bricks 8 and 9 below); until then the funnel ranked on reach and health alone.

### The orchestrator

- Turns one sentence into finished work: explore the repositories in scope,
  split the commission into a DAG of steps, dispatch in waves, review every
  answer. Not part of the core — the core says who *should* act, the
  orchestrator acts.
- **Look before you split.** The exploring pass is real and measured; a total
  that leaves it out reports a number that never happened.
- An edge means "after". A step whose prerequisite failed review is blocked
  rather than dispatched, stays on the record with the reason, and only that
  branch stops.
- **Run receipts.** A run is dumped as each step closes and again when the run
  closes, including when it was cut short. Written to a temp file and renamed
  into place, so an interrupted dump never looks like a finished record.

### Adapters

- **omp** — the first client adapter, replacing the local stand-in as the
  shipped far side. Intent-shaped flags (`match_case`, `regex`, `whole_word`)
  are folded into the pattern because omp has no flag for any of them; the
  answer is parsed back with an anchored separator so a path containing a colon
  survives, the column the capability requires is recovered from the line, and a
  search that hit its limit is reported as partial rather than complete.
- **Claude Code** — the second first-class client. `runner` became `runners`, a
  list: with two clients that are both first class, one slot would have made one
  of them permanently unreachable. Two runners claiming the same implementation
  is refused at load rather than settled by map iteration order.
- **Serena** — not a CLI at all. An MCP server behind a local proxy, so this
  adapter holds a session and speaks JSON-RPC over HTTP instead of spawning a
  process. Everything above the seam is unchanged.
- **Chat sessions.** The unit of isolation is the chat, not the client. Two
  chats may be open at once, each with several repositories, and neither may
  read the other's context or borrow its permissions. What they share is the
  floor: the catalogue, the measurements and the history.
- An adapter's far side may *think*, and a thinking far side can report a file
  it was told to leave alone. Every answer comes back through the same scope and
  type checks the request declared, so the security design stays real rather
  than advisory.

### Capabilities

- `code.search` — literal text, tool-agnostic.
- `symbol.definition`, `symbol.references`, `symbol.implementations` — answered
  by Serena.
- Atenea's contract names a **position**, because that is what an editor has;
  Serena's API names a **symbol**. Reading the word under the cursor is the
  adapter's job, and the trace says which name it resolved to.
- **`atenea ask`** — one capability against one repository, through the same
  funnel, review and receipt as any step. The atom a workflow is built from, and
  the way a client that already has a cursor hands it over.

### Learning from what it measured

- **The measurement base.** Every attempt is measured into an embedded DuckDB
  file: time, tokens and peak resident memory, filed under the capability asked
  for and the implementation that answered. Failed attempts too, with their bin
  and the untranslated reason, so a provider that fails expensively stops
  looking cheap.
- The core is the only writer. Writes batch and reach disk on a beat, when a
  phase closes, and before shutdown. Attempts fold hour to day to week to month;
  folding is idempotent and only closed periods fold.
- **Cost joined the funnel** as a ranking and never a filter: an expensive
  provider that is the only one left still gets the work. It breaks a tie only
  when one side is cheaper on both axes, because trading time against tokens
  needs an exchange rate nobody has.
- An implementation ranks on its declared estimate until it has real
  measurements of its own **on that repository**, then on the real ones. The
  trace prints `estimated` out loud so nobody reads a guess as an observation.
  Measurements are read for the running tool version only, so an upgrade starts
  a fresh baseline instead of dragging old numbers along.

### Money is a permission

- A spending ceiling is a **grant**, decided before anything ran, not a
  measurement of what something cost. `budget_usd` belongs to `[orchestrator]`
  and funds **one commission**, split between its steps rather than copied to
  each: four steps share one quarter instead of spending four.
- Running out is `permission_denied`, never a health verdict. The provider was
  not slow and is not down — the grant Atenea passed down was too small — so the
  funnel does not learn a lie about it.
- An exhausted commission keeps working through whoever charges nothing.
  `atenea task --budget` funds one commission above the settings file; a
  negative grant is refused rather than clamped to silence.

### The crash notebook

- Atenea's **own** faults land on disk the instant they happen: one line of JSON
  per fault, synced before the call returns. A batched notebook loses the last
  entry in exactly the crash it exists to describe.
- A provider being down is an ordinary answer with a bin of its own and is not
  filed here. Filing those would bury the rare entry that matters.
- Background jobs get the opposite treatment from foreground work: nobody waits
  on their return value, so a flush failing every 30s for an hour used to look
  exactly like one succeeding.
- **Names, never values.** An entry carries the shape of the work and the
  payload's *keys* — same rule as the sensitive-path list, for the same reason:
  a crash dump is the likeliest artifact to be pasted into a bug report.
- Reading changes nothing. `atenea incidents clear` is a separate word and marks
  read rather than deleting. A torn last line is counted and announced, never
  skipped.

### Running as a service

- **`atenea service install`** writes a `systemd --user` unit. A user unit,
  never a system one, and nothing listens: no port, no socket, no API. The
  commands are not clients of the service — they are the same core reading the
  same disk, which is why they work whether it is running or not.
- **One background lane** for the three rhythms (measurement flush, history
  roll-up, copies). They touch the same files, so a second lane would only buy
  the chance of two of them meeting on the same one.
- A rhythm's first pass is due on the **first** beat. A six-hour rhythm on a
  laptop shut every evening would otherwise hand out a due date it never
  reaches, and the copy nobody notices missing is missing forever.
- **Copies.** A hard-linked snapshot of everything Atenea has learned, taken
  every six hours, five kept in rotation, beside the state root and never inside
  it. Five copies of an unchanged base cost one base; a changed file is copied
  whole and the older snapshot keeps the older bytes, so dropping the oldest is
  safe at any moment.
- **Coming up after an ugly close.** Damage is assessed before any work is
  accepted, never lazily on first use. An interrupted dump is swept, a receipt
  that will not parse is renamed rather than deleted, and a measurement base
  that will not answer is moved aside under its own name so a fresh one can open
  where it was. A base another live Atenea is holding is never touched: moving a
  healthy file out from under a running process would manufacture the corruption
  the check exists to catch.
- The status screen reports the rhythms, the copies and the repair, and every
  fact on it comes from disk or from the settings file — never from a tally in
  the printing process's memory, which would be a different number for every
  reader.
- One OS box, `internal/platform`, is the only place that knows where state,
  settings and copies live on this machine.

### Notes

- The version is a constant in the source, not a link-time flag. A version
  injected with `-ldflags` is one somebody has to remember to inject, and the
  build that forgets does not fail — it ships claiming to be whatever the source
  said. The release workflow refuses a tag that disagrees with the constant.
- A binary built from a checkout appends its revision as SemVer build metadata,
  and a dirty tree says `modified`: `0.1.0+9b34dd0.modified` is not a release,
  whatever the number claims. Build metadata is ignored when versions are
  compared, which is the right meaning — it *is* 0.1.0, built from that tree.
- **One binary ships: `linux-amd64`.** The measurement base is an embedded
  DuckDB, which is a cgo dependency, so cross-compiling needs a C toolchain per
  target and `CGO_ENABLED=0` fails outright rather than degrading. Rather than
  publish a binary for a machine nobody has run Atenea on, the release carries
  the platform its suite passed on. Everything else builds from source with
  `go build ./cmd/atenea`, which is what the README documents anyway — and
  `atenea service install` is implemented for `systemd --user` and says so
  plainly everywhere else.

[0.1.0]: https://github.com/Tutitoos/atenea/releases/tag/v0.1.0
