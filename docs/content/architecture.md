---
title: Architecture
weight: 2
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
       |                |                |
    adapter          adapter          adapter        <- dumb translators
       |                |                |
      omp          Claude Code       OpenCode
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
| **2. Constraints** | Languages, whether an index is required, and the repository sizes it is worth using on. |
| **3. Cost** | Time and tokens. Hybrid: an estimate to begin with, real measurements the moment there are enough. |
| **4. Health** | Alive, degraded or down, plus a comparable score. The block that enables fallback. |

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

**Stage 4.** A standing user rule wins outright — the user's word comes before
Atenea's opinion. Otherwise the survivors are ranked by health state, then by
health score, then by cost, then by id. That last tie-break exists so the same
catalog always produces the same answer: a selector that shuffled would make
every measurement below it unreproducible.

Cost ranks; it never filters. An expensive provider that is the only one left
still gets the work, because "too expensive" is not the same answer as "nobody
can do this". And cost only breaks a tie when one side is cheaper on *both*
axes — time and tokens. Trading one against the other needs an exchange rate
nobody has, so a genuine trade-off falls through to the id and stays stable.

### Break-in mode

On the very first boot there is no baseline, and ranking by cost would mean
ranking by estimates somebody typed into a settings file — guesswork wearing a
number. So an implementation's own measurements only outrank its declared
estimate once it has a couple of them; until then the estimate is used and the
trace says `estimated` out loud, so a guess is never read as an observation.

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

There is **one** agent contract, not two. An orchestrator and a specialist
differ by a field, not by a type: two separate contracts would drift apart the
first time one of them grew a field.

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
far side belongs to somebody else: an adapter. Three ship — one drives `omp`,
one drives the Claude Code CLI, one speaks MCP to Serena — and a local stand-in
sits in the same place for a machine where nothing is installed. One interface,
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

The funnel ranks on cost, and until something measures it, cost is whatever
somebody typed into the settings file. The base is where the real figures go:
one row per attempt, filed under the capability that was asked for and the
implementation that answered.

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

## Adapters are dumb

All the intelligence stays in the core. An adapter translates a request into
its far side's shape and translates the answer back, and that is all it does.
The far side may be a CLI or a server; the seam does not care.

The return path is the treacherous one, because every CLI phrases failure
differently. Each adapter sorts its own errors into a handful of shared bins:
`invalid_input`, `not_found`, `permission_denied`, `external_denied`,
`unavailable`, `timeout`. Atenea only ever sees the bin, and the untranslated
message travels alongside it so a human can still search for it verbatim.

`external_denied` is deliberately separate from `permission_denied`: reaching
outside the machine is the one effect that no undo takes back, so it has to be
visible on its own.

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
| Costs real money per call | Caps each turn with a budget the settings declare; zero is refused rather than read as "no limit" |
| Is slow by nature | Gets a timeout an order of magnitude past omp's, because a model that is thinking is not a model that is stuck |
| Reports `is_error: true` with a success subtype when a session is stale | Reads the error flag, not the subtype, and bins an expired login as `unavailable` |
| Charges for a session even when the answer is unusable | Reports what it spent whatever the verdict, so a failed turn still shows up in the bill |

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
| Its symbol overview carries no line numbers, and a wildcard search times out on a real repository | Neither is used; the file is read directly, which is one line instead of a project walk |
| Numbers lines from 0 | Converts once, on the way out, in a function with a name so the off-by-one has one place to be wrong |
| Answers references in a different shape from symbols, with the referring line marked in a rendered block | Parses that block, because the entry's own location is the *enclosing function* — the wrong answer to the question asked |
| Keys references by path, and a map has no order | Sorts, so two identical commissions cannot answer the same thing shuffled |
| Has no scope parameter for references | Narrows here, because a caller that asked about one directory and got the repository was answered a different question |
| Holds one active project at a time | Serializes every exchange and activates once, since a second caller could otherwise retarget the server mid-answer |
| Refuses requests its language server does not implement | Bins that as `unavailable`, so the funnel falls back to somebody who can |

This is also the only adapter that opens a file to do its job, which makes the
sensitive-path list load-bearing rather than advisory. Exploring skips those
files in silence because a missed hit costs nothing; a caller pointing at one
exact position inside one is refused out loud, because "nothing here" would be
a lie.

All three adapters translate into the same six failure bins and the same output
shape. That is the whole point of the seam: the funnel above them ranks a tool
call against a model turn against a language server without knowing that any of
them exists.

## Effects

Every capability declares what it causes, in three groups: `read`, `write`,
`external`. Writing breaks something of your own, at home, and can be undone.
Reaching outside escapes the machine and cannot. Putting both in one bag would
give the dangerous one the permissions of the harmless one.

## Contract versioning

`pkg/contract` is versioned on its own, independently from the product. The
product is in alpha at `0.x.y`; the wire format adapters compile against is
already a commitment and starts at `1.0.0`.

An adapter lagging behind by a minor version keeps working — that is what lets
adapters be updated after the core rather than in lockstep with it. An adapter
running *ahead* of the core is refused, because the core cannot honor a field
it has never heard of.
