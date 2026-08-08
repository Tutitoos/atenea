---
title: What is not built yet
weight: 6
---

# What is not built yet

The design was written before the code, in twenty-eight sheets, and it ordered the
work as nine bricks laid one at a time — core first, then the registry and the
funnel, then the orchestrator, then one adapter, then a second, then the remaining
capabilities, then the measurement base, then cost.

**All nine are laid.** What follows is what the design asked for and the code does
not do yet, in the order the design itself put them. Each entry says how you would
know it was finished, because "done" is the word this page exists to be careful
with.

## Ranked code search, if anything ever wants it

`code.search` used to declare a fourth implementation, `codebase-memory.search`,
with no adapter behind it. It is now deleted. The entry was selectable and
impossible: on a medium repository that declared a codebase-memory index, and
with the id added to the adapter's served list, the funnel chose it and the
call came back `not_found`. A catalogue entry nothing can run is not a
placeholder for future work, it is a promise the funnel keeps making and
nothing can keep.

`serena.search` stays unclaimed on purpose, which is the difference: Serena is
wired for the symbol family, and a text search it has no code for would make
the funnel promise an answer nobody can give.

What was genuinely interesting about a graph-backed search is not in
`code.search` at all. `ripgrep` already answers that capability correctly and
cheaply everywhere, with no index. The graph's advantage would be *ranking* --
deduplicating matches into their containing functions and ordering them by
structural importance -- and `code.search` has no output field that can carry
it, so an implementation would have had to throw the advantage away to fit the
contract.

**If it is ever wanted, it is a new capability with its own contract**, not a
re-declaration of this one. A capability whose output says which symbol
contains each hit, and in what order, is a different question from "where does
this text appear". Nothing is blocked on it today.

## Reading a file is not a capability

The design's base list names `file.read` beside `code.search` and the symbol
family. It is struck, not pending.

A capability exists so the funnel has something to choose between. `code.search`
has four candidate implementations and the choice is real: literal text is cheap
and exact, a model turn is expensive and can infer, a graph needs an index and
ranks what it finds. Reading a named range of a named path has one implementation
on any machine — the filesystem — and every client that would ask already holds
it. There is no constraint to check, no health to track, no cost to rank and
nothing to fall back to. A funnel in front of a file read is overhead with a
trace attached.

It looked like a gap exactly once, and only under an artificial rule: a run
forbidden from using anything but Atenea had to read a function body by searching
for its name with `context_lines = 30`, and the window truncated the following
declaration mid-expression. That is a real limitation of reading through a search
tool. It is not an argument for the capability, because the constraint that
produced it does not exist in normal use — the client reads the file, and asks
Atenea only what needs deciding.

If a machine ever appears where reading is genuinely a choice between providers,
that is a new capability with its own contract, not this one revived.

## Wandering is recorded and nothing ranks on it yet

An adapter that cannot confine its own search checks every match afterward and
drops the ones outside the requested scope. The count of those strays is
recorded per attempt, in the `out_of_scope` column of the measurement base, and
`atenea metrics` prints it. Nothing *ranks* on it.

It is deliberately not health. Health answers "can this provider answer at
all", and a provider that wanders still answers — the core drops the strays and
the caller gets a clean result, so marking it down would remove a working
provider to punish a defect that was already neutralized. It is also not a
failure, for the same reason: the answer is right.

What wandering actually costs is tokens and time spent on results nobody could
use, and that is a cost fact. Cost is the last funnel stage, the one that
chooses between providers which all work. It already ranks on measured numbers
where they exist — `Cost.Effective` returns the measurement once a provider has
cleared the break-in threshold and the declared estimate before that, and the
trace says which of the two settled it. What it ranks on is duration and
tokens. Strays are a third number sitting in the same base, read by a person
and by nothing else: a provider returning nine of them for every good hit is
paying nine times over for the same answer, and no funnel stage notices.

The column keeps a week of attempts at full grain, the same window health reads
for its fault streak, and folds into the rollup ladder after that like every
other count. It was not carried there at first, on the argument that a lifetime
sum nothing reads is exactly the kind of entry the section above this one warns
about — but a number with a reader that silently shrinks on a compaction
schedule is worse than one with no reader, so it now survives the fold.

**Done when:** the cost stage reads this number alongside duration and tokens.

## One agent does everything

The contract drew up two agent families early on: `AgentOrchestrator` and a
specialist that would only execute, never decide. Only the orchestrator was
ever built, and `AgentSpecialist` is now deleted rather than finished.

It is not a gap waiting its turn. The reason to want several orchestrators
in the first place was context economy — *"cada uno ve solo lo que necesita
y ahorras tokens"* — but the orchestrator that got built spends no tokens of
its own to economize: it is a deterministic dispatcher, not a model burning
context on its own reasoning, and every adapter that does spend tokens
already starts a fresh process per call. "Tools do not decide" closed the
rest of the case — a specialist would only ever have made one implementation
call and reviewed one outcome, which is exactly what the orchestrator's own
dispatch already does. There was nothing left for a second agent to be the
one to do.

This stays one agent, honestly, until a capability shows up whose dispatch
genuinely needs a decision the orchestrator's own loop does not already make.

## History is declared and never loaded

`ContextHistory` — *"what happened in earlier sessions: user decisions and facts
Atenea discovered, little and good, loaded lazily"* — is in the contract, and the
orchestrator's card declares that it sees it.

Discoveries are produced, reported on the result, and now survive within the
commission that made them: a step `resume` correctly skips redispatching used
to come back silent, because `Outcome.Discoveries` lived only in that
process's memory and a step never rerun has no fresh `StepResult` to carry
it. The receipt now keeps each step's discoveries, so a crash between two
waves no longer costs the closed wave what it had already found. That is the
whole of the fix: one commission's own discoveries surviving one commission's
own crash. A *later* commission is still blind to it -- none of a finished
run's discoveries are ever read back in when the next one starts, so the lazy
loading the design describes still has no loader. Every run against a
repository still starts knowing nothing about any run before it, including
the one that finished a minute earlier.

**Done when:** a second commission against the same repository starts from what
the first one found.

## Permissions cover four effects, and never ask

The design (backlog P2, *Seguridad*) names five kinds of effect —
read / write / process / network / device — and asks that dangerous actions
require a policy and a confirmation.

The contract has four: `read`, `write`, `external`, `process`. That is a
closed count, not four-of-five-so-far. `network` was never a fifth group of
its own — `external` already names it, "leaves the machine: network,
external services," from the design's own three-way split, closed before
this backlog list was ever written. `process` was the real gap:
`code.search`'s own adapters made it impossible to ignore, since every
implementation of it spawns a binary to answer at all, and a permission
model that could not name that was checking three quarters of what the one
P0 capability actually does. It closed in `1.4.0`.

`device` is not a gap waiting its turn. Nothing in Atenea's own catalog is
device-shaped: no capability declares it, no adapter could cause it, and no
design decision ever closed it into the contract the way read, write and
external were — it lived only in this backlog's own five-item wishlist,
written before the three-way split that actually shipped. The one plausible
source of a real need, an MCP tool for driving a mobile device, is scoped to
a different project's tooling, not a settled requirement inside this
catalog. `ParseEffect("device")` is refused on purpose, and two tests exist
to keep it that way — this stays four, honestly, until something in
Atenea's own catalog genuinely needs a fifth.

There is still no confirmation anywhere: an effect is either granted —
standing, in the settings file, or one call at a time with `--allow` — or
refused, and nothing is ever asked. The design's *"acciones peligrosas
requieren política"* is satisfied for every effect the contract actually
has; *"y confirmaciones"* is not, for any of them.

**Done when:** a write outside a granted path stops and asks, rather than
being refused up front or allowed silently.

## OpenCode, still parked

The design parked the third adapter deliberately: *"cuando el sistema esté rodado.
No entra en el orden principal, pero queda como paso propio del plan para no
olvidarlo."* This entry is the not-forgetting. Codex remains out.

It is not redundant the way the specialist was — `omp` and `claudecode`
already cover the two real approaches to `code.search`, literal text and a
model turn, but neither can be pointed at a free or local model. `omp` is a
fixed process Atenea cannot steer from the outside; `claudecode` always
bills Anthropic. Cost will not surface the difference on its own:
`Cost.Sample` measures duration, tokens and peak memory, never dollars, so a
local model would not win the funnel's ranking just for being free — on a
normal machine it likely loses on duration and memory both. The one place it
would earn its keep is the gap that opens once `budget_usd` runs dry
mid-commission: paid providers refuse from there on and whoever charges
nothing keeps working, and today that leaves `code.search` with nothing
semantic left standing, only literal grep.

**Done when:** a local model is wired as a real implementation, and a real
commission has hit its `budget_usd` ceiling with `claude.search` still
having work left to do — both at once, not either alone, and not "the
system feels settled."

## What is not missing

Worth writing down, so none of it gets re-litigated: the funnel (constraints →
reach → health → cost, with the break-in rotation and its ceiling), the hybrid
cost with per-version baselines, the six shared failure bins, the cancellation
path down to process groups and inherited pipes, the measurement base with its
rollups and retention, the crash notebook, the receipts, resumable runs
(`atenea resume`, reading a receipt back rather than paying twice for a step
that already succeeded), the service wiring, and all four symbol
capabilities — `symbol.definition`, `symbol.references`,
`symbol.implementations` and `symbol.overview` — each answering for real
against this repository: the first three past the empty or same-file
answers earlier bugs let through, the fourth past the cross-type
ambiguity its own live verification against this repository found.
Those are laid, measured, and defended by tests that have been mutated to check
they fail when they should.

The entry that stood here about a chat only ever widening the operator's floor
closed on 2026-08-07, the day after it stopped being hypothetical. The settings
file has `client_effects`: a grant for a chat a client opened, separate from
the operator's own, defaulting to it so no existing file changes behavior, and
a ceiling as well as a floor. `atenea status` prints both and marks the second
`inherited` when it is a copy.

What makes it bind was already there: `commissioned` wraps the attached runner
and refuses any step whose permission does not carry an effect its capability
declares. Both grants rest on that one wrapper, which is why neither of them
needed a check of its own.

## What the proof settled, on 2026-08-08

Every one of the eight shipped capabilities has now been answered over MCP by a
real client against this repository, in one session: `code.search`,
`code.impact`, `repository.index`, `symbol.calls` and all four symbol
capabilities. Before this, one tool of the eight had ever been called that way.

Two clients were connected at the same moment and both appeared on `atenea
status`, each with its own session id and its own run count -- the isolation the
code says is only visible on that screen. Their receipts carry a chat apiece,
and the funnel handed the same capability to different implementations for the
two of them, which is the break-in rotation spreading measurements rather than
anything going wrong. A terminal ask leaves a receipt with no chat at all, which
is correct: nobody opened one.

Three defects came out of it, all in the reporting rather than the machinery,
and all in the CHANGELOG: a fresh settings file that classified its own
repository and cost two capabilities, a metrics table that split rows by a
column it never printed, and a status caption that counted a measurement the
funnel did not yet believe. The run also spent real money -- one `code.search`
went to Claude Code for $0.1286 of its $0.25 ceiling -- and that landed on the
receipt with its ceiling beside it, which is what the adapter's own comment
said had to happen.

## What the first wide wave settled, on 2026-08-08

Parallel dispatch has now happened on this machine. A commission over two real
repositories ran both explores at once and then both searches at once: the
explore pair overlapped for 775ms of their 777ms, the work pair for the whole of
the shorter one, and the run took 2.6s against 3.7s of step time. Before this,
286 receipts held plans at most two steps long and **no wave wider than one**.

Nothing in the machinery was missing, and nothing was added to it. The wave
splitter, the `max_parallel` ceiling and the per-repository fan-out were correct
and covered by tests -- one of which had been asserting overlap between two
repositories since long before this -- because the test fixture registers two
repositories while the shipped settings file registers one. That is the whole
distance between "tested" and "has happened": the width of a wave comes from the
catalog, and every real run on this machine had one repository in it.

Two defects came out of running it, both in the reporting, both now fixed and in
the CHANGELOG: the report printed only the summed step time, so a parallel run
read as slower than it was and could not be told from a queue; and a step's
`closed_at` was stamped by the recorder after the wave returned, so reading it
beside `duration_ms` -- the one obvious use of the pair -- placed a quick step
1.27s after the partner it actually started with. The second one misled its own
author first, which is the strongest thing that can be said for fixing it.

One correction to the record here, and it is about method rather than code:
`atenea resume` was listed among things never watched live because no receipt on
this machine showed one. It had been watched, days earlier, and the receipts were
cleaned up afterwards -- an audit of surviving artifacts cannot see what was
tidied, and it understates rather than overstates. It has now been watched again,
twice, with the receipts left in place: a commission interrupted at 1.1s with
ctrl-c, resumed, the closed explore step read off the receipt and only the work
step redispatched, and a second resume of the same run correctly doing nothing at
all. Both are in the documented examples, which are output the binary actually
printed today.
