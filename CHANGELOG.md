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

- **A spending ceiling of Atenea's own is no longer read as the client being
  broken.** A turn stopped at its `--max-budget-usd` prints no `result` field
  whatsoever — the reason is in `errors` and `terminal_reason` — so the adapter
  read past it, fell back to the child's `exit status 1`, which names nothing,
  and landed in the catch-all: `unavailable: claude code did not answer`. That
  is the one bin that marks a provider **down**, so a grant of ours being too
  small took a perfectly healthy client out of the funnel and read on screen as
  a client that had stopped working. The reason is now read from whichever
  field carries one, and the ceiling bins as `permission_denied`, which is what
  it is: a refusal made on this machine.

- **A turn that charged money and then failed no longer reports spending
  nothing.** The adapter returned an empty outcome beside the error, so
  everything the far side had already said about the attempt went in the bin
  with it. Measured on a real call: 78 seconds and $0.354 charged, filed as
  `spent_usd` empty. Three things were broken at once — the measurement base
  learned the failure was free, the receipt lost the charge, and because the
  core spends its purse down by what comes back, one commission could charge
  past its whole grant without the arithmetic noticing. The weight is now read
  before the verdict and reported whichever the verdict is. A refusal issued
  before the process is spawned still weighs nothing, because it cost nothing.

- **The break-in rotation no longer rewards a provider for failing.** Only a
  call that works leaves a measurement, so a provider that cannot answer stays
  at zero samples permanently — and the rotation hands the turn to whoever owes
  the base the most. Measured: `claude.search` lost seven straight commissions
  against a repository where `ripgrep` held fourteen clean measurements
  averaging 959ms, and the funnel picked it again for the eighth, at about a
  minute and thirty cents a go. Failing was what kept it winning. The rotation
  is now credit: four attempts with no measurement to show and the provider
  ranks on its declared estimate like anybody else — ranked lower, never
  filtered out, so it stays reachable and can earn its first number the moment
  it works. `Cost.Attempts` carries the count the funnel needs to tell a first
  outing from a record of nothing but failure, which also makes the notice
  `atenea select` already printed true for the first time.

- **A run you stopped no longer reads as a failed one.** Getting the failure
  bin right left the report itself unchanged, and the report is what a reader
  sees first: `verdict failed`, then `review child=failed parent=failed`, then
  `failed canceled: claude code was stopped before it answered` — three lines
  blaming the work for a decision the reader made. Worse, the middle one is a
  review of an answer that never arrived. There is now a `canceled` verdict,
  and a step nobody let finish is not reviewed at all: no output came back, so
  neither the child nor the parent has anything to have an opinion about. A
  real fault still outranks a cancellation, so a genuine failure is never
  buried behind an interruption, and a cancellation outranks success, so a
  half-run plan never reports that it worked. Contract `1.2.0`, additive:
  adapters built against `1.1.0` compile unchanged and never have to send it.

- **Stopping a run is no longer filed as a provider running out of time.** The
  two look identical where they are caught — the work did not finish and the
  context is dead — so the whole class was binned as `timeout`. Pressing
  ctrl-c two seconds into a call therefore printed `claude code took longer
  than 5m0s`: a ceiling nobody reached, quoted at somebody who had waited two
  seconds. It also collected a fault against that provider, dropped its health
  towards `down`, and moved the funnel's ranking on the strength of a decision
  the provider had no part in. There is now a `canceled` bin, decided from
  `context.Cause` rather than from the mere absence of a result, and a
  canceled call is not a measurement: nothing about it reaches the base, the
  health verdict or the ranking.

- **A canceled call comes back when it is canceled.** Killing the process
  Atenea started left its grandchildren alive holding the copy of stdout they
  had inherited, so the read went on waiting for a pipe nobody would close:
  measured at twenty-seven seconds for a client whose helper slept twenty-five,
  and unbounded for a helper that never exits. The child now gets its own
  process group and the group is killed; a helper that escapes with `setsid`
  is covered by a deadline on the wait itself. Canceling a call that spawns a
  daemonizing helper went from thirty seconds to under a tenth of one.

- **The measurements a stopped run had already earned survive it.** The flush
  at a phase close inherited the caller's context, so ctrl-c canceled the
  write as well as the work: every measurement in the batch was lost and an
  incident was filed saying `metrics: open …: context canceled`, which is how
  six identical incidents came to be sitting in the notebook. The flush now
  runs on a context detached from the caller, because work that was paid for
  before the interruption is still work that happened.

- **`130` on ctrl-c.** A stopped run used to exit `4`, the bin a script retries
  on. Nothing is wrong with a run somebody stopped, and a script must not
  retry it: it exits `128 + SIGINT` like the shell's own convention, and the
  screen says `canceled: stopped before it finished` instead of naming a limit
  that was never reached.

- **A newly attached provider can still earn its first measurements.** The
  funnel ranks on health before it hands out break-in turns, and until the
  record learned to promote, nothing running from a CLI ever reached `alive`:
  everybody sat at `unknown`, health tied, and the rotation worked. Promotion
  turned that into a trap. The first provider to succeed became alive,
  outranked every unmeasured rival, and was from then on the only one ever
  dispatched — so nothing else was ever measured and the catalog froze on
  whoever happened to answer first. Found by attaching a second client to a
  machine that had been running for a while: twelve calls in a row to the one
  provider with a record, and a new one that could never earn its first.
  `unknown` is not a verdict, it is the absence of a look, and it no longer
  loses to one while break-in is open. Degraded and down keep their places:
  those are things somebody watched happen. The trace was corrected with it —
  it had been reporting these as `healthiest surviving implementation`, which
  was wrong even between two providers that both read `unknown`, and it now
  says which stage really chose and names the alive provider that was
  overtaken.

- **Successful calls count as evidence of health.** The record could only ever
  make a provider look worse. The rule was written against *silence* — nobody
  probed it, so nobody knows — and then applied to success, which is not the
  same thing at all. The consequence was reported from real use: seven
  successful calls in a row, zero failures, and the screen still said
  `health=unknown` with an amber light that no amount of working could clear.

  The bar for the record to promote is now *the last call here worked, and it
  worked recently*. Recently is one hour: long enough that two commands ten
  minutes apart do not disagree about whether the machine is well, short enough
  that it cannot speak for a machine nobody has used today. Last, because a
  failure with nothing after it means the newest thing anybody knows is that
  this broke — not enough to condemn a provider, far too much to call it well,
  so it reads unknown until something succeeds.

  Promotion may only lift `unknown`. A `down` or `degraded` set by a live probe
  stands, because a probe looked seconds ago and a file may predate the outage
  entirely. Downwards the record still overrules everything, including a
  declared `state = "alive"`. A promotion changes the state and does not invent
  a score, so cost stays the tie-break between two working providers.

- **The status screen reads the measurement base.** It walked the declarative
  catalogue and never opened the base, so the one screen whose job is reporting
  health was the only place that could not see the half of health that survives
  a process. Both fixes were needed: either alone leaves the amber in place.

  Where a provider has been tried on several repositories the screen shows the
  worst state it reached, and names the repository — `down` and `down on
  scripts` are different instructions. An unreadable base costs the promotion
  and nothing else; the screen still draws, because a health screen that
  refuses to render because one input is missing is the least useful possible
  answer to something being wrong.

- **The funnel caption is a report, not a constant.** It read `estimated until
  an implementation has been measured` on an empty base and on a machine
  running entirely on real figures — the exact confusion the sentence existed
  to prevent. It now says which it is: `nothing measured yet`, `measured for 1
  of 4 implementations, the rest on declared estimates`, `measured`, or
  `measuring is off: ranking on declared estimates for good` when there is no
  base at all. That last one is deliberately not the `yet` wording: `yet` is a
  promise, and it should not be made for a base that is never coming.

- **A dropped provider is amber, not red.** The documentation has always said
  red is for work that cannot be done and that a provider being down is amber,
  because the funnel hands its work to somebody else and the commission still
  finishes. The code said otherwise, and nobody could tell: from a CLI nothing
  ever probed anything, so no provider ever reached `down` and the wrong color
  never showed. Making the record a health input turned it on — permanently, on
  any machine with one client not logged in. Red is now what it claimed to be:
  a capability with nothing left to answer it.

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

- **`atenea resume --list` no longer offers a closed `ask` as still worth
  continuing.** `Remaining()` decided "nothing left" from whether any step
  declared `Needs`, and a single `ask` step never does — it has no split to
  wait on. So a receipt closed with `verdict ok` kept reporting its one step
  as remaining, forever: `resume --list` advertised it, and resuming it did
  nothing (`Resume`'s own `KindAsk` branch already asks the receipt itself
  whether the step is `OK`, and correctly no-ops), so the listing and the
  command it was advertising a candidate for disagreed about the same file.
  Measured against a real receipt in this repository:
  `20260804T120304-44df2e` (`code.search in docs-tmp`, closed, reviewed
  `ok`) listed `1 step(s) remaining` before the fix and is gone from the
  listing after it. `Remaining()` now checks `Kind` the same way `Resume`
  already does: an `ask` is done once its one step is `OK`, full stop — no
  `Needs`-based inference, which was never the right question for a shape
  that has nothing to split.

### Changed

- **The Claude Code timeout is 90 seconds, not five minutes.** Measured: two
  real searches made 8 and 9 turns in 55s and 66s — about seven seconds a turn
  — and *both* were ended by the money ceiling rather than by time. Five
  minutes was a leash nothing on a paid provider could ever reach, while being
  far longer than anybody waiting at a prompt will sit through. The two
  ceilings do not overlap and neither replaces the other: money stops a client
  working too expensively, time stops one that is not working at all, and a
  client wedged on a lock spends nothing at all.

### Added

- **`atenea resume RUN_ID`** picks an interrupted or failed commission back up
  from its own receipt, dispatching only the steps that never closed rather
  than redoing the whole plan. Measured on this repository: a two-step
  commission (`explore-current` then `search-current`) killed right after the
  first step closed came back through `resume` having redispatched only the
  second — 1.033s of real work, the explore step untouched, `closed_at`
  unchanged — and finished `verdict ok` with a receipt no different from one
  that had never stopped. Resuming a run that is already fully closed
  redispatches nothing at all: `spent 0s over 0 step(s)`, same verdict, a
  clean no-op rather than a second billed attempt. `--budget USD` replaces
  what remains of the original grant, in case the first attempt's ceiling was
  the reason it stopped. A receipt written against a repository that no
  longer exists, or against a contract version this build no longer speaks,
  is refused rather than guessed at.
- **`atenea resume --list`** shows every receipt still worth continuing —
  oldest first, with how many steps are left and the verdict so far — instead
  of a person having to open run files by hand to find out what died.
- **`atenea metrics`** prints what the base measured, per capability,
  implementation and repository: attempts, failures, how many were priced, the
  average of the calls that worked and the worst single call. The three counts
  sit together because the gap between them is the diagnosis.
- **`atenea metrics clear`** forgets it, narrowed by `--capability`,
  `--implementation` or `--repository`. The base is the only thing here that
  decides behavior and cannot be edited by hand — true by construction, and
  still true long after the machine it describes has been fixed. Clearing all
  of it needs `--all` on top of the word: it is the one act that destroys
  something nothing can rebuild. Attempts and folded buckets go together, since
  leaving the folded half would let the numbers reappear an hour later.
- A migration carries the successful half of each rollup in its own columns.
  A legacy bucket that mixed successes and failures cannot be split after the
  fact, so it keeps its counts and contributes nothing to cost — the tempting
  repair, keeping the count and zeroing the sum, would invent an average of
  zero and re-create the bug.
- **Atenea can launch and supervise an MCP server itself**, as a bare
  process, instead of always assuming one is already running behind a
  fixed endpoint. `[orchestrator.serena.process]` names a `command` and
  `args` (`{{port}}` is replaced with the chosen port before every spawn);
  Atenea spawns it in its own process group, waits on the same MCP wire it
  serves on for the `initialize` handshake to answer, and restarts it up
  to `restart_limit` times (default 2, "a couple of times" in the design's
  own words) with `restart_delay` (default 2s) between attempts if it
  crashes -- the same break-in posture this design already applies to
  providers. `lifecycle = "persistent"` starts it with Atenea and keeps it
  running; `"on_demand"` starts it on first use and an idle reaper stops
  it after `idle_timeout` (default 5m) with nothing in flight, gated by an
  in-flight refcount so a call already running is never stopped out from
  under itself. A crash only spends a fresh restart budget once the
  server has stayed ready for `stable_after` (default 30s): without that
  window a server that flickers ready and dies resets its own attempt
  count on every brief success and retries forever -- a real infinite
  crash loop, found and fixed by this package's own tests before it ever
  shipped. Every managed process gets SIGTERM, a grace window (default
  5s), then SIGKILL if it is still not gone, both from the idle reaper and
  from `atenea`'s own shutdown.

  `atenea status` gained a `processes` section: state, PID, port, uptime,
  restart count and last failure reason for every server Atenea itself
  launched, printing nothing at all for a setup that never opted in. A
  restarting or down process turns the big light amber, the same as a
  down provider elsewhere on the screen. Verified end to end against the
  real `serena` binary on this machine -- no ToolHive, no manually-started
  proxy: `atenea ask symbol.definition` spawned it, waited for it to
  answer ready, resolved a real symbol in 1.3s, and left no child process
  running after the command exited.

### Documentation

- The settings file **replaces** the built-in defaults rather than patching
  them. A file holding only an `[orchestrator]` block is a complete description
  of an Atenea with no catalogue at all: it boots, reports red, and answers
  `unknown capability` to everything. `atenea config init` writes the whole
  file to start from. This was true from the first release and written nowhere.
- `.serena/` is ignored. Serena writes a project config into whatever
  repository it is pointed at, describing one machine and belonging to nobody
  else.
- The shipped `default.toml` documents `[orchestrator.serena.process]`
  commented out and inactive: supervision is opt-in, and a machine that
  already points Serena's `endpoint` at ToolHive or a hand-started proxy
  sees no change at all.
- **`symbol.definition` and `symbol.references` have answered for the first
  time.** Not a code change: every line of the adapter, the funnel and the
  failure bins was already there. What was missing was a Serena with a Go
  toolchain behind it — the shipped container never had one. Run against a
  bare-process Serena instead, `symbol.definition` resolved a real call site
  to its real declaration in this repository, and `symbol.references` found
  all six real call sites of a symbol in one pass. `symbol.implementations`
  still does not: it now fails clean into `unavailable` — Go's language
  server not answering the `textDocument/implementation` request Serena's
  tool sends — rather than never being reachable at all. Detail and evidence
  in `docs/content/not-built-yet.md`.

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
