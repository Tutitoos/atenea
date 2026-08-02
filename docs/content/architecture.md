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
     [2] health        who is AVAILABLE right now
                |          only "down" leaves; degraded stays
                v
     [3] choice        user rule first, then the healthiest
                |          cost joins this stage later
                v
          one implementation
```

**Stage 3 today.** A standing user rule wins outright. Otherwise the survivors
are ranked by health state, then by health score, then by id — that last
tie-break exists so the same catalog always produces the same answer. A
selector that shuffled would make every measurement below it unreproducible.

**Stage 3 tomorrow.** Cost slots in ahead of the health ranking, and the rule
still outranks it: the user's word comes before Atenea's opinion. It is not
wired yet on purpose — see the break-in mode below.

### Break-in mode

On the very first boot there is no baseline. Ranking by cost would mean ranking
by the estimates somebody typed into a settings file, which is guesswork wearing
a number. So the funnel runs on constraints and health until real measurements
exist. The same applies after a tool is upgraded: measurements are stored with
the version they belong to, and a new version starts a fresh baseline rather
than dragging the old, slower numbers into the average.

### When the funnel comes up empty

| Situation | Bin |
| --- | --- |
| The capability has no registered provider | `not_found` |
| Nothing fits this repository | `not_found` |
| Everything that fits is down | `unavailable` |

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
far side belongs to somebody else: in production a client adapter. Until the
first adapter exists a local stand-in sits exactly where the adapter will,
outside the core, chosen by the settings file — so the skeleton beats without
pretending the far side is already built.

A runner that cannot reach a provider says so, and that is not a bug: it is a
provider that is not reachable from here. The step fails as `unavailable`, the
catalog marks that provider down, and the next run picks somebody else. That
loop is the fallback design working, not an error path.

### The paper copy

Memory is a whiteboard; disk is paper. A run is dumped when each step closes
and again when the run itself closes — including when it was cut short, which
is exactly the run worth reading back. The write goes to a temporary file and
is renamed into place: a dump interrupted halfway would look like a valid
record of a run that never happened that way.

The dump is deliberately narrow. It is a receipt, not a transcript.

## Adapters are dumb

All the intelligence stays in the core. An adapter translates a request into its
CLI's shape and translates the answer back, and that is all it does.

The return path is the treacherous one, because every CLI phrases failure
differently. Each adapter sorts its own errors into a handful of shared bins:
`invalid_input`, `not_found`, `permission_denied`, `external_denied`,
`unavailable`, `timeout`. Atenea only ever sees the bin, and the untranslated
message travels alongside it so a human can still search for it verbatim.

`external_denied` is deliberately separate from `permission_denied`: reaching
outside the machine is the one effect that no undo takes back, so it has to be
visible on its own.

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
