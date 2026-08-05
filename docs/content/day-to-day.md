---
title: Day to day
weight: 2
---

# Day to day

Atenea runs as a background service, installed once with `atenea service install`
and started with the machine. Nothing here starts or stops it. These are the five
commands worth remembering; `atenea` on its own prints the rest.

| | When |
| --- | --- |
| `atenea task "TEXT"` | Every time you want something done |
| `atenea status` | When you want to know if anything is wrong |
| `--trace` | When an answer looks wrong and you want to see who was picked |
| `atenea incidents` | After a crash, or when the light is amber and you do not know why |
| `atenea resume RUN_ID` | After a crash, to pick a commission back up without paying twice |

## `atenea task "TEXT"`

The one you actually use. Hand it a commission in plain words; it explores, splits
the work, picks who answers each piece and reports back.

```text
$ atenea task "find every TODO"
run       20260803T120617-f5f47c
task      code.search in current
verdict   ok
spent     1.016s over 1 step(s)
  ask      1 step(s), 1.016s
```

`verdict` is the whole summary: `ok`, `failed`, or `canceled` if you stopped it.
The exit code says the same thing to a script — `0` worked, `6` means the
commission ran and came back failed, `130` means you pressed ctrl-c. Note that `1`
is **not** "it did not work": it is reserved for a failure Atenea could not sort,
which means a bug worth reporting. [Getting started]({{< relref "getting-started" >}})
has the full table.

`--budget USD` funds one commission above whatever the settings file grants. Money
is a permission here: a step with none refuses before spawning anything.
`--allow EFFECT` grants one more beyond what the settings file already stands
behind, repeatable for several — most commissions never need it, since
`code.search` already ships pre-granted the one thing it actually causes.

To dispatch a single capability instead of a whole commission, `atenea ask` takes
one by name — `atenea ask code.search --repo current --set query=TODO`. Useful for
checking a provider; `task` is the everyday one.

## `atenea status`

One screen. The first light is Atenea as a whole, then one per provider.

```text
code.search              [read]
    amber  claude.search            provider=claude-code        health=unknown
    amber  ripgrep                  provider=ripgrep            health=unknown
```

`amber` means nobody has measured that provider yet, which is not the same as
broken. `health=down` is the one that matters. The orchestrator block above it
separates two things worth keeping apart: `serves` is what an attached adapter
can answer, `no runner` is what the catalogue declares with nothing wired to it.

## `--trace`

Goes on `task` or `ask`. Prints the plan, who was chosen for each step, what it
charged, and — the useful half — who was dropped and why.

```text
steps
  ask-current          ask      ripgrep                  1.015s
      review   child=ok parent=ok (output matches the capability)
      dropped  codebase-memory.search: needs an index from provider codebase-memory, repository has none -- atenea detect looks for one, atenea ask repository.index --repo current builds one
```

If you only want to know *who would be picked* without spending anything,
`atenea select code.search --repo current` answers the same question for free and
prints the funnel stage by stage.

## `atenea incidents`

The crash notebook. Anything that went wrong badly enough not to fit in a normal
report lands here, and it survives a kill.

```text
2026-08-03 02:05:00 metrics.flush  metrics: open .../metrics.duckdb: context canceled
```

`atenea incidents clear` marks them read. `atenea metrics` is the other half of
the same story and worth a look once in a while: attempts, failures, how many
were priced, and the worst single call per provider. The gap between attempts and
failures is usually the diagnosis.

## `atenea resume RUN_ID`

For when `task` was interrupted or the process died mid-plan. `--list` shows
what is still worth continuing — every receipt with steps left, oldest first:

```text
$ atenea resume --list
20260804T114108-a4974e       -         1 step(s) remaining  find every TODO comment
```

Resuming reads the receipt back and dispatches only the steps that never
closed; whatever already succeeded is not repeated:

```text
$ atenea resume 20260804T114108-a4974e
run       20260804T114108-a4974e
verdict   ok
spent     1.033s over 1 step(s)
  explore  0 step(s), 0s
  work     1 step(s), 1.033s
```

One step, not two: the first had already closed before the crash and is read
off the receipt rather than paid for again. Resuming a run with nothing left
to do is a clean no-op — `spent 0s over 0 step(s)`, same verdict as before —
not a second billed attempt. `--budget USD` replaces what remains of the
original grant, in case the ceiling was the reason it stopped; `--allow`
instead adds to what the commission already carries, since an effect already
held should never be lost by resuming.
