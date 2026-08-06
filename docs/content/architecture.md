---
title: Architecture
weight: 3
---

# Architecture

```text
     user
       |
       v
   +----------------------------------------------+
   |                  ATENEA                      |
   |                                              |
   |   Capability Registry  ->  Selector          |
   |   (what can be asked)      (who answers it)  |
   |             ^                    ^           |
   |             |                    |           |
   |        Orchestrator (agent): explores,       |
   |        splits, dispatches, reviews           |
   +----------------------------------------------+
       |            |            |            |
    adapter      adapter      adapter      adapter     <- dumb translators
       |            |            |            |
      omp      Claude Code    Serena   codebase-memory
```

## Capability vs implementation

A **capability** is the stable half: an action, described without naming a
single tool. `code.search` says what it resolves, what it takes in and what it
gives back, and nothing about how.

An **implementation** is the variable half: ripgrep, Serena, a language server.
Everything that varies by tool lives here, in four blocks.

| Block | Holds |
| --- | --- |
| **1. Capability** | Which capability this answers. |
| **2. Constraints** | Languages, whether an index is required, whether the repository must sit under version control, and the repository sizes it is worth using on. |
| **3. Cost** | Time, tokens and peak memory. Hybrid: an estimate to begin with, real measurements the moment there are enough. |
| **4. Health** | Alive, degraded, down, or unknown before anything has probed it, plus a comparable score. The block that enables fallback. |

A fifth fact rides alongside these four but does not join them: **scope
guarantee**, whether a provider can promise a returned match never left the
scope it was asked to search. It never disqualifies a candidate and the
funnel never ranks on it, so it stays out of the table above — a disclosed
property of the answer a caller gets once an implementation has already been
picked, not a fifth block.

The constraints belong to the implementation, never to the capability. A
capability that grew a language restriction would stop being swappable, and
swappability is the entire point.

## The unit of work is the repository

Not the project. Constraints are measured per repository: the selector asks
"does **this** repository have an index?", and a workspace of forty repositories
is forty separate answers. One global index over all of them was the
alternative, refused for being slow to build and impossible to keep fresh.

An index belongs to the **provider** that built it, not to one implementation of
it, so two implementations of Serena share one warm index.

## The funnel

```text
   all implementations of the capability
                |
                v
     [1] constraints   who CAN work here
                |          language, index, scale
                v
     [2] reach         who is WIRED UP at all
                |          no attached client serves it
                v
     [3] health        who is AVAILABLE right now
                |          only "down" leaves; degraded stays
                v
     [4] choice        user rule, then health, then cost
                |          cheapest of the equally healthy
                v
          one implementation
```

Reach runs after constraints and before health because the trace should carry
the most useful reason it can. A provider that both needs a missing index and
has nothing wired up should be reported for the index: that is the fact the
user can act on, and the one that would still block after the wiring was fixed.

Reach is its own stage rather than a health verdict because the two are
different kinds of fact. Health is what a probe found — running a step is a
probe, and what a runner reports updates it. Reach is a wiring decision that
was made before anything ran. Filing one under the other would mean a settings
change had to wait for a probe to be believed, and it would send someone to
debug a provider that is perfectly well.

Health has two witnesses, and they answer different questions. A probe — and
running a step is a probe — says what a provider reported the last time
somebody spoke to it. The measurement base says what actually happened here,
repeatedly, and it survives the process: Atenea is a CLI at least as often as
it is a service, and a fresh process starts with a clean catalog that has
forgotten every fault before it. Without the second witness a provider that
refuses every single call stays healthy forever, because the only thing that
could mark it down evaporated when the last command exited.

So the record on disk is a health verdict too, and it answers in both
directions.

Downwards, a run of failures. Three in a row in the same bin is an outage and
leaves the funnel; three in different bins is a provider in trouble with no
single cause, which ranks last but stays. Both expire after five quiet minutes,
because a provider health has dropped is a provider nothing calls, and nothing
that is never called can ever prove it recovered.

*In a row* is measured from the newest failure backwards, and it stops at the
first attempt that broke differently — not over the whole run since the last
success. The difference only shows on a provider with a long unbroken record,
and there it is the whole verdict: one that spent a week unreachable, was
fixed, and now refuses every call for a new reason is down today, not merely
flaky. Pooling the run would let the fixed cause mask the live one forever,
because a provider that has never once succeeded here never earns a clean
slate to start a fresh run from.

Upwards, a run of successes. The bar is *the last call here worked, and it
worked recently* — both halves load-bearing. **Recently** is one hour: a
success is a statement about the moment it happened, and an hour is long enough
to cover a working session, so two commands ten minutes apart do not disagree
about whether the machine is well, and short enough that it cannot speak for a
machine nobody has used today. **Last**, because a failure with nothing after
it means the newest thing anybody knows is that this broke. One fault is
ordinary and does not condemn a provider, but it is far too much to call it
well; the honest state is unknown, and the next successful call clears it.

The first version of this rule only looked downwards, on the argument that
silence is not evidence. That is true of silence and false of what it was
applied to: a machine where everything succeeded reported `health=unknown`
forever, the light never went green, and no amount of work could clear it. A
run of successful calls is not silence. It is the most direct possible answer
to the question the funnel is asking.

The two directions are not symmetric in what they may overrule. Downwards the
record beats anything, including a cheerful `state = "alive"` in the settings
file: a streak of real failures on a real repository outranks an opinion.
Upwards it may only lift `unknown`. A `down` or `degraded` was put there by
something that looked more recently than a file can — a probe inside this
process, seconds ago — and promoting over it would hide the outage the operator
is standing in front of. `unknown` carries no claim at all, so there is nothing
to overrule.

A promotion changes the state and nothing else. It does not award a score:
score breaks ties between two providers in the same state, and the tie-break
that matters between two working providers is what they cost — a real number,
measured on both. Inventing a full score for having worked would put every
promoted provider above every other on a figure made up here, and cost would
never be reached.

**Stage 4.** A standing user rule wins outright — the user's word comes before
Atenea's opinion. Otherwise the survivors are ranked by health state, then by
health score, then by cost, then by id. That last tie-break exists so the same
catalog always produces the same answer: a selector that shuffled would make
every measurement below it unreproducible.

Cost ranks; it never filters. An expensive provider that is the only one left
still gets the work, because "too expensive" is not the same answer as "nobody
can do this". And one side only counts as cheaper when it is no worse on
*either* axis — time and tokens — and better on at least one. Trading one
against the other needs an exchange rate nobody has, so a genuine trade-off —
faster but chattier — falls through to the id and stays stable.

### Break-in mode

On the very first boot there is no baseline, and ranking by cost would mean
ranking by estimates somebody typed into a settings file — guesswork wearing a
number. So an implementation's own measurements only outrank its declared
estimate once it has a couple of them; until then the estimate is used and the
trace says `estimated` out loud, so a guess is never read as an observation.

Waiting for those measurements is not enough, because a provider the estimate
ranks last never runs, and one that never runs is never measured. So while a
base is attached and somebody still owes it numbers, the turn goes to whoever
owes the most — ahead of cost. The trace names that outright: `break-in turn`
means cost did not decide this one at all. Once everybody has paid, the
rotation stops on its own and cost takes over for good.

"Behind health" needs one qualification, and it is the difference between a
verdict and the absence of one. Degraded and down come before the turn: those
are things somebody watched happen, and a measurement bought from a provider
known to be limping measures the limp. `unknown` is not a verdict — it means
nobody has looked — and holding it behind one is how it stays that way. The
first provider to succeed becomes alive, would outrank every unmeasured rival,
and would therefore be the only one ever dispatched: nothing else is ever
measured and the catalog freezes on whoever answered first. So an
implementation still owed its break-in measurements ranks with the alive ones
until it has them. Attach a new client to a machine that has been running for
months and it gets its two calls; a provider the record caught failing does
not.

The rotation is credit, not a subscription, and that took a measurement on a
real machine to notice. A dispatch handed to an unmeasured provider is buying
the base a number — but only a call that *works* leaves one, so a provider that
cannot answer here stays at zero samples permanently, and "whoever owes the
most" hands it every single dispatch forever. Failing is what keeps it winning.
Measured: `claude.search` lost seven straight commissions against a repository
where `ripgrep` held fourteen clean measurements averaging 959ms, and the
funnel picked it again for the eighth. Each of those seven cost about a minute
and thirty real cents.

So the credit runs out. Four attempts with nothing to show for any of them and
the provider ranks on its declared estimate like anybody else — ranked lower,
never filtered out, so it stays reachable and can still earn its first number
the moment it works again. Four is two rounds of the rotation: enough that a
cold cache, a passing outage or one stingy grant does not spend the credit.
Partway through still counts as going: a provider holding one of the two
samples it owes is being measured successfully and keeps its turn.

Attempts and samples are therefore both on the record, and the gap between them
is the diagnosis. `atenea select` prints it without spending anything:
`claude.search: 7 attempts here, none of them successful, so it has no measured
cost and ranks on its declared estimate`.

That is also why cost never filters. A provider dropped for being expensive
could never earn the measurement that showed the estimate was wrong.

The same applies after a tool is upgraded: measurements are stored with the
version they belong to, and a new version starts a fresh baseline rather than
dragging the old, slower numbers into the average.

### When the funnel comes up empty

| Situation | Bin |
| --- | --- |
| The capability has no registered provider | `not_found` |
| Nothing fits this repository | `not_found` |
| Nothing that fits is wired up to any attached client | `unavailable` |
| Everything that fits and is reachable is down | `unavailable` |

Those are different problems and the caller reacts differently to each, so they
are different bins. Retrying a provider that just blinked is the orchestrator's
job; the selector reports cleanly and does not retry on its own.

### When a user rule cannot be honored

The preferred implementation is scaffolding, not dogma. If it does not survive
the funnel, Atenea moves on to the next best rather than stopping — but it says
so. Changing the user's choice in silence would betray what they asked for.

## The orchestrator

The registry and the selector answer *who should do this*. Somebody still has
to turn one sentence from the user into finished work, and that somebody is an
**agent**, deliberately not part of the core. The core owns the catalog and the
funnel and says who should act; the orchestrator is the one that acts.

There is **one** agent contract, and today exactly one kind of agent: the
orchestrator. A specialist that would only execute, never decide, was drawn
up early in the design and never built — once "tools do not decide" landed,
there was nothing left for it to do that the orchestrator's own dispatch does
not already do in one call and one review. The contract still takes a second
kind as a field, not a fork, so nothing here forecloses one if a real need
ever shows up.

### One capability, directly

A workflow is several capabilities chained; the single capability is the atom
those chains are made of. `atenea ask` dispatches exactly one, against exactly
one repository, through the same funnel and the same review as any step of a
commission — a second, quieter dispatch path would be a second set of rules to
keep in step with the first.

A commission with no repository means all of them, because the user excluded
none. An `ask` cannot borrow that: a position belongs to one unit of work, and
running it against the rest would answer about files that merely share a path.

This is how a caller that already *has* a cursor hands it over. Exploring finds
text, and a text hit is not a cursor.

### Look before you split

```text
   commission
       |
       v
   [explore]   one light look per repository in scope
       |          finds WHERE the commission lands
       v
   [split]     one step per repository, narrowed to those areas
       |
       v
   [work]      dispatched in waves, reviewed as each child finishes
```

Splitting before looking would mean guessing the shape of a repository nobody
has read. The look is a real step with a real cost: it is measured and shows up
in the phase totals, because a task whose total leaves the look out reports a
number that never happened.

What the look learns becomes a **discovery**, filed at the level it belongs to.
A fact about one repository is not a fact about the workspace.

### Waves, not a queue

The plan is a graph, not a list. Steps with no dependency between them form one
wave and run at the same time, capped by a configurable ceiling so a laptop
stays responsive. Sorting inside a wave is stable, which is what makes two runs
of the same plan comparable.

An edge means **after**. A step whose prerequisite did not pass review is
**blocked**: never dispatched, but still on the record with the reason. Only
that branch stops — work in another repository is none of its business.

### A reviewer at every level

Every child is judged by its parent as it finishes, and the parent's word is
what goes on the record. A child that reports success with an answer that does
not match the shape the capability promised has not succeeded, and the
disagreement is written down with the child's one reply.

Reviewing always, rather than only on failure, is the cheap option: the parent
is already there.

### The permission travels attached

A commission carries what the user allowed, and every step inherits it. Nothing
grants itself anything heavier on the way down. Reading is free by default;
writing and reaching outside the machine are not.

### The runner seam

`contract.Runner` is where deciding ends and doing begins. Everything on the
far side belongs to somebody else: an adapter. Four ship — one drives `omp`,
one drives the Claude Code CLI, one speaks MCP to Serena, one walks a call
graph codebase-memory keeps on disk — and a local stand-in sits in the same
place for a machine where nothing is installed. One interface,
several possible far sides, and swapping them changes nothing above the line.

Several can be attached at once. Each declares the implementations it answers
for, the funnel picks an implementation without caring who runs it, and the
wiring carries the request to whoever serves it. Two runners claiming the same
implementation is refused at load rather than settled by declaration order.

The far sides are not interchangeable in kind. `omp` answers a search with a
tool call and the adapter parses what comes back; Claude Code answers with a
model turn, so the adapter has to ask precisely — the capability's own output
shape becomes a JSON Schema the turn is held to — and then check the answer
again, because a far side that thinks can report a file it was told to leave
alone. Trusting the instruction alone would make the security design advisory.
Serena is not a command at all: it is a server, so that adapter holds a session
instead of spawning a process, and the difference stops at the seam.

A runner that cannot reach a provider says so, and that is not a bug: it is a
provider that is not reachable from here. The funnel drops it at the `reach`
stage before anything is dispatched, saying `no attached runner serves it` —
not `down`, because a provider nobody wired up is not a provider that is
broken, and sending someone to debug the healthy one is what an explainable
decision exists to prevent.

### The paper copy

Memory is a whiteboard; disk is paper. A run is dumped when each step closes
and again when the run itself closes — including when it was cut short, which
is exactly the run worth reading back. The write goes to a temporary file and
is renamed into place: a dump interrupted halfway would look like a valid
record of a run that never happened that way.

The dump is deliberately narrow. It is a receipt, not a transcript.

## The measurement base

The funnel ranks on cost, and on a cold machine cost is whatever somebody typed
into the settings file. The base is where the real figures go: one row per
attempt, filed under the capability that was asked for and the implementation
that answered.

It is a loop, not an archive. What a step cost on the way out is what the
funnel reads on the way in next time, so the estimate in the settings file is
only ever the opening position — it is overtaken the moment an implementation
has measurements of its own on that repository. Per repository, because cost
is not a property of a tool: the same provider is cheap against a warm index
and expensive without one, and a figure borrowed from somewhere else would be
the confident kind of wrong.

### Only a successful call is a price

Every attempt is recorded, successful or not, with its bin and the untranslated
reason. Only the successful ones are averaged into what an implementation
costs.

The first version of this store counted them all, on the argument that a tool
which hangs before failing has still eaten the wait. That much is true, and the
conclusion drawn from it was still wrong: the same average lets an
implementation that refuses *instantly* — not logged in, no index, no server —
record a stream of very fast, very cheap calls. It then becomes the cheapest
thing on the board, the funnel hands it everything, and every commission fails.
Health does not save it either, because nothing probed it. Failing cheaply paid
better than working.

So a failure is not a price; it is the absence of one. It stays in the record —
attempts and failures are both counted, and they are what health reads — but it
divides nothing. An implementation with a long record and no successful call
has no measured cost at all and falls back to its declared estimate, and the
trace says exactly that rather than leaving a reader to wonder why the ranking
ignored a base full of rows.

### Forgetting

The base is the only thing in Atenea that decides behavior and cannot be
edited. A settings file is text; health repairs itself the moment a provider
answers. A baseline is neither: it is true by construction and stays true long
after the machine it describes has been fixed. An afternoon of misconfiguration
leaves numbers nothing will ever contradict, because the calls really were that
shape.

`atenea metrics` prints it and `atenea metrics clear` forgets it, narrowed to
one capability, implementation or repository. Both tables go together — leaving
the folded half behind would let the numbers reappear an hour later with no
explanation — and clearing everything needs `--all` on top of the word, because
it is the one act here that destroys something nothing can rebuild.

```text
  a step closes
       |
       v
  [buffer]  in memory, a batch                 ← never blocks the work
       |
       +--- phase closes -----> flush
       +--- every 30s --------> flush
       +--- shutdown ---------> flush
       |
       v
  measurement (one row per attempt)
       |
       +--- the next funnel asks ---> costs per capability x repository
       |         (flushed first, so it reads what just ran)
       |
   hour -> day -> week -> month                ← the retention ladder
```

Three axes, because two of them can hide the third: time, tokens, and the peak
resident memory of whatever ran. A tool that is quick and quiet while paging a
machine into swap is not cheap, and nothing but the memory figure would say so.

Failed attempts are recorded too, with their bin and the untranslated reason. A
provider that fails expensively has to stop looking cheap, and a baseline built
only from the calls that worked would rank it as the fastest thing available.

What the far side answered when asked its version is filed with every row, so
an upgrade starts a fresh baseline instead of averaging the new binary's
numbers into the old one's. Durations are stored in microseconds: the band
where the funnel has the most to decide is the cheap one, and in milliseconds
every provider in that band records a zero and becomes indistinguishable.

A step that never reached a provider is not measured. A blocked step spent
nothing and nobody answered it; a row for it would sit under an empty
implementation and be read later as a real average.

### One writer, one file

The store is an embedded DuckDB file, and the core is the only thing that
writes to it. A second Atenea starting up does not corrupt it and does not fail:
DuckDB allows one writer, and the store waits out the other's flush rather than
giving up, which turns a crash into a queue.

### Batching, and the two moments that do not wait

Writing every measurement the instant it happens costs real time on the hot
path of real work and buys nothing. So measurements are batched — and batching
is only honest because of two moments that do not wait for the beat: a phase
closing, and the process going down. Between them a crash can lose at most one
phase's worth of numbers.

A one-shot command never ticks at all. `atenea task` lives for a second, so the
shutdown path is the only reason it measures anything.

### The ladder

Fine detail is worth keeping for a week, not forever. Attempts fold into hourly
buckets, hours into days, days into weeks, weeks into months, each tier waiting
longer than the one before. Folding is idempotent: an attempt carries a flag
once it has been counted, so a pass that runs twice cannot count it twice.

Only closed periods fold. An hour still in progress would be summarized halfway
and then never revisited.

Compaction is driven by a mark on disk rather than by the beat alone, because
most Atenea processes are a command that lives for a second and the history
still has to be kept in shape for them. The mark is written inside the same
transaction that does the work, so two Ateneas starting together cannot both
decide they are the one to do it.

### Money is not one of the axes

Some far sides charge. That number is reported, never ranked, and never
reaches the base.

Ranking on it would re-order every provider the day a price list changed, with
nothing in the numbers to say why: the same call, the same speed, a different
seat in the funnel. Worse, the base is indexed by capability, implementation
and repository — not by which account paid — so two machines on different
plans would average into a figure that describes neither. Duration, tokens and
memory repeat: run the same work twice and the same numbers come back. A price
is a fact about a contract, not about the work.

Nor is it folded into the token count as a common currency. A cheap-per-token
model that talks twice as much is genuinely a real trade-off, and pricing it
away would hide exactly the case worth seeing. Tokens are what was used;
dollars are what somebody was charged for using them.

So a charge travels on the outcome as its own number, is totalled onto the
receipt, and is filed on the paper copy — a receipt with no price on it is not
a receipt. It appears on screen only when it is not zero.

## The crash notebook

The base above records what work cost. The notebook records what Atenea broke,
and they are separate files on purpose: a measurement is a batch of ordinary
facts about a normal day, and an internal fault is one rare fact that must be
on disk before the process that noticed it is allowed to die.

```text
  measurements ---> [buffer] ---> every 30s ---> metrics.duckdb   (batched)
  incidents    -------------------------------> incidents.jsonl   (synced, now)
```

One line of JSON per fault, appended and flushed before the call returns. The
cost is real — a disk sync on a path that is meant to be rare — and it is the
entire point: a notebook that batches loses the last entry in exactly the crash
it exists to describe.

### What counts as an incident

Only Atenea's own faults. A provider that is down, a timeout, a rejected
payload — those are ordinary answers with bins of their own, and filing them
here would bury the rare entry under a thousand routine ones.

| Written down | Not written down |
| --- | --- |
| A panic in a step, or in the wave that runs it | A far side returning `unavailable` |
| A background flush or roll-up that failed | A capability the catalogue does not have |
| Measurements dropped at the buffer ceiling | A step the reviewer rejected |

The two panic sites are the two places a fault would otherwise vanish. A step
runs in its own goroutine, so a panic there takes the whole process down past
every caller that might have logged it; and `main` is where anything the step
missed arrives. Both catch, write, and re-panic — the notebook makes the fall
recoverable to *read*, never survivable.

Background jobs are the opposite failure: nobody is waiting on their return
value, so a flush failing every thirty seconds for an hour used to look exactly
like a flush succeeding every thirty seconds for an hour. Now the first one
writes an entry. The dropped-measurement count gets its own, because it is the
more serious half: the job will try again, those rows are gone, and the base
the funnel ranks on is short by exactly that much.

### Names, never values

An incident carries the shape of what was running — the step, the capability,
the repository, the tool version, and the payload's **keys**. Never a value.

This is the same rule the sensitive-path list follows, for the same reason: a
crash dump is the likeliest artifact to be pasted into a bug report. `query`
tells you which field was in play; the string somebody searched for tells you
nothing you could not get by reproducing it, and it might be a token.

### Unread, not unresolved

The status screen shows a count and a date, and only when the count is not
zero — a permanent `incidents 0` trains the eye to skip the one line it exists
to catch. `atenea incidents` prints them whole, stacks included, and changes
nothing: two people investigating the same crash see the same file. Moving the
mark is a separate word, `atenea incidents clear`, and it marks read rather
than deleting. The notebook is the record; the mark is just where you left off.

A torn last line — the file's own crash, mid-write — is counted and announced
rather than skipped. It is the one thing that would make the count quietly
wrong.

## Staying up

Atenea installs itself as a `systemd --user` unit and nothing more. There is no
system unit and no daemon user: everything it touches is under one person's
home, so a system unit would hand a bug reach it never needs. It listens on
nothing — no port, no socket, no API. The commands are not clients of the
service; they are the same core, built again, reading the same disk.

That has a consequence worth being explicit about: **nothing on the status
screen may come from a tally kept in memory**. The command that prints it is a
process that lives for a second and whose clock never beats. So the screen
reports the rhythms, which come from the settings file and are the same
everywhere, and the copies, which are on disk and the same for everybody
looking. A lane that fails reaches the reader through the notebook, which is
also on disk. Every fact on that screen is true no matter who prints it.

### One lane for everything in the background

Three rhythms — the measurement flush, the history roll-up, the copies — run in
a single lane, one at a time. They touch the same files, so a second lane would
only buy the chance of two of them meeting on the same one.

A job's first pass is due on the first beat, not one period later. The
alternative looks harmless and is not: a six-hour rhythm on a laptop that is
shut every evening would hand out a due date it never reaches, and the copy
nobody notices missing is missing forever. Being asked early costs nothing
because every job in this lane is guarded by its own mark on disk — the roll-up
reads when it last folded, the copies read the newest one taken. The clock says
*consider it*; the disk says *is it due*.

### Copies

Everything Atenea has learned is one directory: the measurement base, the run
receipts, the notebook. A copy is a hard-linked snapshot of it, beside it and
never inside it. Unchanged files are shared with the snapshot before, so five
copies of an unchanged base cost one base; a changed file is copied whole and
the older snapshot keeps the older bytes, which is what makes dropping the
oldest safe at any moment.

Unchanged means same size, same modification time and same permissions, with
the times compared exactly rather than rounded to the second. Rounding is the
tempting simplification and the one that breaks the guarantee: a file rewritten
in the same second as the last snapshot would be called unchanged, and the copy
would quietly hold the old bytes — a backup lying about what it holds. Being
too strict only ever costs disk.

A symlink is recreated rather than followed, and never hard-linked: whether
`link(2)` targets the link or what it names is not the same answer on every
filesystem, and a link costs nothing to remake.

### Coming up after an ugly close

A clean stop refuses new work and gives what is running a bounded margin. A
power cut gives nothing, and the next start is the delicate moment — so the
damage is assessed before any work is accepted, never lazily on first use.

A dump interrupted mid-write is swept: it is a record of a run that never
happened that way, and leaving it would let a half-written file pass for a
finished one. A receipt that will not parse is renamed rather than deleted,
because those bytes are the only evidence of what was lost. Good receipts are
not touched — including the receipt of a commission the cut interrupted, which
is exactly the one worth reading back.

If the measurement base will not answer, it is moved aside under its own name
and a fresh one opened where it was. Refusing to start would be the wrong
trade: the funnel already copes with having no measurements — that is the cold
start it was built for — and the history that went is what the copies protect.

The one case that must never be treated as damage is a base another live Atenea
is holding. Moving a healthy file out from under a running process would
manufacture the corruption this check exists to catch, so the two are told
apart by their failure bin and only `unavailable` moves anything.

## Adapters are dumb

All the intelligence stays in the core. An adapter translates a request into
its far side's shape and translates the answer back, and that is all it does.
The far side may be a CLI or a server; the seam does not care.

The return path is the treacherous one, because every CLI phrases failure
differently. Each adapter sorts its own errors into six shared bins:
`invalid_input`, `not_found`, `permission_denied`, `external_denied`,
`unavailable` and `timeout`. A seventh, `canceled`, is never translated from
anything a provider said — it comes from this side, when the user pressed
ctrl-c or a caller dropped the context. Atenea only ever sees the bin, and the
untranslated message travels alongside it so a human can still search for it
verbatim.

`external_denied` is deliberately separate from `permission_denied`: reaching
outside the machine is the one effect that no undo takes back, so it has to be
visible on its own.

`canceled` is separate from `timeout` for the same kind of reason, and the
two look identical at the call site: the work did not finish, and the context
is dead. They mean opposite things. A timeout is a fact about a provider —
it was given a limit and went past it, and it earns a fault for that. A
cancellation is a fact about the person at the keyboard, who pressed ctrl-c
after two seconds and is owed no verdict about anybody's speed. Filing the
second as the first is how a provider collects a fault for a decision that was
never its own, and how a screen ends up quoting a five-minute ceiling to
somebody who waited two seconds. Nothing a user stopped reaches health, the
measurement base, or the ranking that decides who runs next time.

The same misattribution has a second half, one layer up, and it is the half a
reader sees first. Getting the bin right left the report itself still saying
`failed` in three places: the run's verdict, the step's review, and the step's
own result line. So there is a `canceled` verdict as well, and a step nobody
let finish is **not reviewed at all** — because there is nothing to review.
No output came back, so neither the child nor the parent has seen anything to
have an opinion about, and printing two verdicts there dresses opinions nobody
holds as findings.

The precedence between the three is not symmetric, and both directions matter.
A real fault outranks a cancellation: if one step failed on its own and a later
one was stopped, the run failed, because saying `canceled` would bury the fault
behind the interruption. A cancellation outranks success for the opposite
reason: a run somebody stopped has not been shown to work, whatever the steps
that did finish managed to do, so `ok` would promise a plan was carried out
when part of it never ran.

### What that costs in practice: the omp adapter

The first real adapter drives `omp grep`, a tool call rather than a model turn:
deterministic, no tokens, and nothing to log into. Translating *out* is small —
`match_case`, `regex` and `whole_word` are declared as intent and omp's search
has no flag for any of them, so they are folded into the pattern it does read.

Translating *back* is where an adapter earns its keep, and this one is a fair
sample of what a CLI actually hands you:

| What omp does | What the adapter does about it |
| --- | --- |
| Prints for a human; no machine format | Reads the rendered lines, anchored so a path holding a colon still comes apart correctly |
| Never reports a column, which the capability requires | Finds the offset again in the line omp returned, with the pattern it sent |
| Answers a pattern it cannot compile with a clean zero | Compiles the pattern first, so a typo is `invalid_input` and not a fact |
| Treats `-l 0` as a small default, then calls the answer complete | Always states a ceiling, and reports a search that reached it as partial |
| Narrows to nothing when `-g` is repeated | Sends one brace glob instead of several flags |
| Prints paths relative to whatever it was pointed at | Rebases them onto the repository, so the caller can open what it is handed |

None of that is policy, and none of it leaks upwards. The core never sees a
line of omp's output.

### What that costs in practice: the Claude Code adapter

The second adapter drives a client that *thinks*, and every hard part follows
from that. omp answers a search with a tool call; Claude Code answers with a
model turn, so the same capability needs a different kind of care at both ends.

| What a thinking far side does | What the adapter does about it |
| --- | --- |
| Answers in prose unless told otherwise | Turns the capability's declared output shape into a JSON Schema and holds the turn to it |
| May report a file the commission excluded | Re-checks every path against the request before returning, because a prompt is an instruction and not a guard |
| May report a match outside the `scope` the caller asked for | Drops it in `cleanHit`, right where containment and sensitivity are already checked, and reports the count once as an aggregate `Notice` — scope is a request-shaping constraint, not a secret, so a drop is worth saying out loud rather than hiding |
| Costs real money per call | Holds the turn to the share the commission granted it, and refuses before spawning when that share is zero |
| Is slow by nature | Gets a timeout above omp's, because a model that is thinking is not a model that is stuck — 90s, measured: two real searches made 8 and 9 turns in 55s and 66s, and both were ended by the grant rather than by time |
| Reports `is_error: true` with a success subtype when a session is stale | Reads the error flag, not the subtype, and bins an expired login as `unavailable` |
| Reports a failure with no `result` field at all | Reads the reason from whichever field has one — `result`, then `errors`, then `terminal_reason`, then the subtype last |
| Stops at a spending ceiling that Atenea itself set | Bins it as `permission_denied`, not `unavailable`: the grant was too small, the provider is fine, and `unavailable` would take a working client out of the funnel |
| Charges for a session even when the answer is unusable | Reports what it spent whatever the verdict, so a failed turn still shows up in the bill, in the baseline's worst case, and against the commission's purse |
| Reloads every customization on a fresh session, and every commission is one | Passes `--safe-mode`, which is worth about 17,000 tokens a call on the machine this was measured on: five of the nine MCP servers a normal chat connects carry 68,754 characters of tool schema between them, before a hook or a `CLAUDE.md` is counted |
| Spawns helpers of its own — MCP servers, language servers, hooks | Puts the child in its own process group and kills the group, because a grandchild holding the inherited pipe keeps the call open long after the process Atenea started is dead |

That second row is not a theoretical worry, and one part of it survives even a
group kill: a helper that calls `setsid` leaves the group before the group is
killed, and no signal Atenea sends can reach it. Only closing the pipes from
this side ends the wait, which is why there is a deadline on the wait as well
as a kill on the tree. Both halves are load-bearing; removing either one puts
a canceled call back to waiting out a helper nobody can see.

### What that costs in practice: the Serena adapter

The third far side is not a command line at all. Serena is an MCP server behind
a local proxy, so this adapter opens a session and speaks JSON-RPC over HTTP
instead of spawning a process. That changes the mechanics and nothing else: the
same six bins, the same declared output shape, the same funnel above it.

The hard part is not the transport. Atenea's contract names a **position** —
file, line, column — because that is what an editor has. Serena's API names a
**symbol**. One of them has to give, and it is the adapter.

| What Serena does | What the adapter does about it |
| --- | --- |
| Takes a name path, never a position | Reads the one line the caller pointed at and takes the word under the cursor, then asks about that name |
| Its symbol overview carries no line numbers, and a wildcard `find_symbol` search times out on a real repository | Neither backs resolution: `find_declaration` anchors a regex to the position instead — one LSP request, not a project walk — and its failure alone is what falls back to reading the file directly |
| Numbers lines from 0 | Converts once, on the way out, in a function with a name so the off-by-one has one place to be wrong |
| Answers references in a different shape from symbols, with the referring line marked in a rendered block | Parses that block, because the entry's own location is the *enclosing function* — the wrong answer to the question asked |
| Keys references by path, and a map has no order | Sorts, so two identical commissions cannot answer the same thing shuffled |
| Has no scope parameter for references | Narrows here, because a caller that asked about one directory and got the repository was answered a different question |
| Holds one active project at a time | Serializes every exchange and activates once, since a second caller could otherwise retarget the server mid-answer |
| Refuses requests its language server does not implement | Bins that as `unavailable`, so the funnel falls back to somebody who can |
| Reports a file's symbols as bare names with no positions, and its `find_symbol` matcher is not anchored to the depth the overview asked about — Go methods come back receiver-less, so unrelated types' `String()` all collide | Fans `find_symbol` out one call per name, bounded at 16 in flight, then narrows to a candidate whose own `name_path` exactly echoes the one asked for; when none does, the match is genuinely ambiguous and is reported as `invalid_input` rather than a false `not_found` |

This is also the only adapter that opens a file to do its job, which makes the
sensitive-path list load-bearing rather than advisory. Exploring skips those
files in silence because a missed hit costs nothing; a caller pointing at one
exact position inside one is refused out loud, because "nothing here" would be
a lie.

### What that costs in practice: the codebase-memory adapter

The fourth far side is a second CLI, but not one built for a human like omp:
it already speaks JSON on both sides, a request out on stdin and an answer
back on stdout, one process per call with nothing kept running between them.
A failure comes back the same way, as JSON on stderr with an `error` field
rather than a distinct exit path of its own.

| What codebase-memory-mcp does | What the adapter does about it |
| --- | --- |
| Speaks JSON already, on both stdin and stdout | Nothing to parse from a rendered format — the request goes out, the answer comes back, one process per call |
| Reports failure as JSON on stderr, not a distinct channel | Reads stderr's `error` field the same way stdout's answer is read, and sorts it into the same six bins as any other far side |
| Encodes every column the same way, so a number that started life as an integer property can come back as a quoted string rather than a bare one | Accepts both shapes reading a line number out of a graph row, instead of trusting the query to always agree with itself |
| Can only ever answer from a graph it built ahead of time, which may already be behind the working tree | Attaches a best-effort `notice` — an `index_status` call plus `git status --porcelain`, together cheaper than the answer they are checking — when HEAD has moved or the tree holds changes the graph never saw; a failed check reports nothing rather than refusing an answer that already succeeded |

It answers two capabilities neither omp nor Serena can: `symbol.calls` walks
the call graph codebase-memory already built from the repository, and
`code.impact` asks that same graph what a git diff reaches. Both need a call
graph, which is the one thing neither a grep nor a language server keeps.
`code.search` already has three cheaper or equally-capable providers, so this
adapter does not claim it — a fourth identical answer would only give the
funnel one more thing to rank.

All four adapters translate into the same six failure bins and the same output
shape. That is the whole point of the seam: the funnel above them ranks a tool
call against a model turn against a language server without knowing that any of
them exists.

## Effects

Every capability declares what it causes, in four groups: `read`, `write`,
`external`, `process`. Writing breaks something of your own, at home, and can
be undone. Reaching outside escapes the machine and cannot. Process is a
fourth, orthogonal axis: not what a capability changes, but whether
answering it means running a binary Atenea does not fully control the
internals of. It composes with the other three rather than replacing any of
them — `code.search` causes read *and* process at once, because every
implementation of it, a client CLI or the disk-searching stand-in alike, is
a binary, not a library.

### The standing grant

A capability declaring an effect is not the same as a commission being
allowed to trigger it. `Permission` is what travels with a request and is
checked at dispatch, and it is built in layers: the read every commission
gets for free, then whatever `[orchestrator] effects` in the settings file
grants standing to every commission and question, then whatever the caller
asked for on that one call, then whatever `--allow` adds on a resume. Each
layer only ever adds — `Permission.Grant` keeps every effect once no matter
how many layers named it, and nothing later in the chain can take an earlier
grant away.

The standing grant exists because not every effect is a per-request choice.
`code.search`'s only implementations today are both a binary, so requiring
every caller to name `process` on every single call would just move the same
yes to one place instead of saying it once, in a file an operator can read,
edit or remove — the same shape `budget_usd` already has for money. It ships
turned on in the default settings: refusing it by default would not make the
spawn auditable, it would make the one P0 capability unusable out of the box.

### Money is a permission, not a cost

A spending ceiling is in this list, next to the effects. Cost is what something
turned out to be; a ceiling is what it was allowed to be, decided before
anything ran — the same kind of thing as an effect the commission does or does
not cover. So running out of it is `permission_denied`, not `timeout`: the
provider was not slow, the grant was spent, and calling it slowness sends
whoever reads the receipt to look at latency.

Nothing about the fallback changes. Only `unavailable` marks a provider down,
and a ceiling reached says nothing about health — the funnel is free to hand
the next step to the same provider, which is right, because the next step may
be one it can afford.

#### One difference from an effect: money is consumed

An effect copied down to every child stays true. A ceiling copied down to every
child is spent once per child, so a commission dispatching four steps would
spend it four times over. That is why the grant is **split, not copied**.

`budget_usd` under `[orchestrator]` is what one commission may spend across
every step. A wave asks for as many shares as it has steps and gets what is
left divided evenly among them, so the shares of a wave add up to exactly what
the commission had left: even if every step spends its share to the last cent
the wave cannot draw more. Waves are sequential, so the next one divides
whatever the last did not touch. Nothing is reserved and nothing is refunded,
because nothing was taken away in advance.

```
grant $1.00   wave 1: 2 steps -> $0.50 each, spends $0.10 total
              wave 2: 2 steps -> $0.45 each   (the remaining $0.90, split)
```

The split is deliberately blind to which steps will cost anything, and it has
to be: which implementation answers a step is settled by the funnel at
dispatch, *after* the shares are cut, and no implementation declares that it
charges. A free step simply never spends its share.

An adapter therefore carries no ceiling of its own. What a call may spend
arrives on the request, in `contract.Permission`, and the far side is held to
that number — for Claude Code, by passing it as `--max-budget-usd`. A share of
zero is refused before anything is spawned, which is why an exhausted
commission keeps working through whoever charges nothing.

## Contract versioning

`pkg/contract` is versioned on its own, independently from the product. The
product is in alpha at `0.x.y`; the wire format adapters compile against is
already a commitment and starts at `1.0.0`.

An adapter lagging behind by a minor version keeps working — that is what lets
adapters be updated after the core rather than in lockstep with it. An adapter
running *ahead* of the core is refused, because the core cannot honor a field
it has never heard of.
