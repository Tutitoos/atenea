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

## `codebase-memory.search`, a brick nobody has laid

`code.search` declares four implementations in the shipped catalogue —
`ripgrep`, `serena.search`, `codebase-memory.search` and `claude.search` —
and two of them have never had an adapter behind them. `serena.search`
stays unclaimed on purpose: Serena is wired for the symbol family, and a
text search it has no code for would make the funnel promise an answer
nobody can give. `codebase-memory` answers three capabilities now —
`symbol.calls`, `code.impact` and `repository.index` — for the same
reason it stays away from `code.search`: three cheaper or
equally-capable providers already exist for it, and a fourth identical
answer would only give the funnel one more thing to rank.

`codebase-memory.search` is the one of the four nothing explains. It
shows up on every status screen under `no runner` and always will, until
this closes. It is either a brick nobody has laid or an entry that should
be deleted; leaving it as a permanent amber line is the one thing it
should not be.

**Done when:** the catalogue declares nothing that cannot be reached, or the
adapter exists.

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
that already succeeded), the service wiring, and all three symbol
capabilities — `symbol.definition`, `symbol.references` and
`symbol.implementations` — each answering for real against this
repository, not only the empty or same-file answers earlier bugs let
through.
Those are laid, measured, and defended by tests that have been mutated to check
they fail when they should.
