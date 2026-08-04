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

## Symbol capabilities, half-answered

Brick 7 is closed: it took wiring a bare-process Serena with a real Go
toolchain behind it, not a code change, exactly as expected. `serena.definition`
and `serena.references` both answer for real now, against this repository:

```text
$ atenea ask symbol.definition --repo current \
    --set file=internal/core/status.go --set line=389 --set column=19
verdict   ok
answer
  location
    line     644
    path     internal/core/status.go
```

`atenea metrics` shows a priced sample for `serena.definition`: two calls,
120ms average, both correct. `symbol.references` found all six real call sites
of the symbol it was asked about, in the same repository, in one call.

`symbol.implementations` still does not answer, and that is not the same gap
as before. It fails clean, into `unavailable`, in about two seconds and with
nothing in the log — Go's language server not answering the
`textDocument/implementation` request the way Serena's tool expects, which the
funnel correctly bins as a provider limit rather than a broken commission. The
funnel and the failure bins did their job; nobody has looked yet at whether
this is fixable on Serena's side, worth a fallback, or a permanent gap in the
catalogue's honesty about what `serena.implementations` can promise.

`code.search` still works, over three providers. `codebase-memory.search` is
still declared in the catalogue with **no adapter behind it at all** — it shows
up on every status screen under `no runner` and always will. It is either a
brick nobody has laid or an entry that should be deleted; leaving it as a
permanent amber line is the one thing it should not be.

**Done when:** the catalogue declares nothing that cannot be reached, or the
adapter exists.

## One agent does everything

The contract has two agent families — `AgentOrchestrator` and `AgentSpecialist` —
with authority, visible context levels and capability lists per card. The
vocabulary is complete and validated.

Nothing ever constructs a specialist. One agent explores, splits, dispatches and
reviews, so the backlog's *"autoridad, contexto, límites y handoff"* is a shape
with nothing in it. There is no handoff because there is nobody to hand off to.

**Done when:** a commission is carried out by more than one agent, each seeing only
the context its card declares, and the trace shows the handoff.

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

## Permissions cover three effects, and never ask

The design (backlog P2, *Seguridad*) names five kinds of effect —
read / write / process / network / device — and asks that dangerous actions
require a policy and a confirmation.

The contract has three: `read`, `write`, `external`. Spawning a process is not an
effect of its own, though it is the one Atenea does most; nor is touching a device.
And there is no confirmation anywhere: an effect is either granted in the settings
file or refused, and nothing is ever asked. The design's *"acciones peligrosas
requieren política"* is satisfied; *"y confirmaciones"* is not.

**Done when:** a write outside a granted path stops and asks, rather than being
refused up front or allowed silently.

## OpenCode, still parked

The design parked the third adapter deliberately: *"cuando el sistema esté rodado.
No entra en el orden principal, pero queda como paso propio del plan para no
olvidarlo."* This entry is the not-forgetting. Codex remains out.

**Done when:** the same capability answers through omp, Claude Code and OpenCode.

## What is not missing

Worth writing down, so none of it gets re-litigated: the funnel (constraints →
reach → health → cost, with the break-in rotation and its ceiling), the hybrid
cost with per-version baselines, the six shared failure bins, the cancellation
path down to process groups and inherited pipes, the measurement base with its
rollups and retention, the crash notebook, the receipts, resumable runs
(`atenea resume`, reading a receipt back rather than paying twice for a step
that already succeeded), and the service wiring.
Those are laid, measured, and defended by tests that have been mutated to check
they fail when they should.
