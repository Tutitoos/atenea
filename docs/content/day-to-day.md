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
run       20260808T171646-d4fc3e
task      find every TODO
verdict   ok
matches   32
spent     2.481s of tool time over 2 step(s), 2.545s elapsed
  explore  1 step(s), 754ms in 767ms
  work     1 step(s), 1.727s in 1.744s

discovered
  [repository] current: 32 hit(s) for "find every TODO", under cmd, docs, internal, pkg

run with --trace for the plan, the funnel and every review
```

`verdict` is the whole summary: `ok`, `failed`, or `canceled` if you stopped it.
The exit code says the same thing to a script — `0` worked, `6` means the
commission ran and came back failed, `130` means you pressed ctrl-c. Note that `1`
is **not** "it did not work": it is reserved for a failure Atenea could not sort,
which means a bug worth reporting. [Getting started]({{< relref "getting-started" >}})
has the full table.

The two times are different questions. `of tool time` is the sum of every step,
which is what the work cost; `elapsed` is the wall, which is what you waited.
They only come apart when a wave has more than one step in it, and what puts
steps in a wave is repositories: with two registered, the same commission
explores both at once and then searches both at once.

```text
$ atenea task "TODO"
run       20260808T171159-d2b5cb
task      TODO
verdict   ok
matches   134
spent     3.781s of tool time over 4 step(s), 2.618s elapsed
  explore  2 step(s), 1.588s in 811ms
  work     2 step(s), 2.193s in 1.77s

discovered
  [repository] atenea: 133 hit(s) for "TODO", under cmd, docs, internal, pkg
  [repository] lanplay: 1 hit(s) for "TODO", under docs
```

`1.588s in 811ms` is two searches that ran together. `--trace` names the waves
themselves, and `--json` carries `elapsed_ms` beside `spent_ms` plus a
`closed_at` per step, which is how a script sees the overlap rather than
inferring it.

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
code.search              [read process]
    amber  claude.search            provider=claude-code        health=unknown
    amber  ripgrep                  provider=ripgrep            health=unknown
    amber  serena.search            provider=serena             health=unknown
```

`amber` means nobody has measured that provider yet, which is not the same as
broken. `health=down` is the one that matters. The orchestrator block above it
separates two things worth keeping apart: `serves` is what an attached adapter
can answer, `no runner` is what the catalogue declares with nothing wired to it.

A third distinction shows up as a `catalog` line beside `settings`, and only
when it applies: implementations the binary ships that your settings file does
not declare. A settings file replaces the catalog rather than patching it, so a
file written before a release quietly never gains what that release added —
`no runner` is something wired to nothing, `catalog` is something never wired
at all because your file has not heard of it.

## `--trace`

Goes on `task` or `ask`. Prints the plan, who was chosen for each step, what it
charged, which files it found, and — the useful half — who was dropped and why.
A drop that is identical in every step prints once at the end instead of under
each one: it is a fact about your catalog, not about any step.

```text
steps
  ask-current          ask      ripgrep                  1.588s
      review   child=ok parent=ok (output matches the capability)
      found    cmd/atenea/cancel_test.go, cmd/atenea/json_test.go, cmd/atenea/main.go, cmd/atenea/main_test.go, cmd/atenea/money_test.go, docs/content/day-to-day.md, internal/adapter/claudecode/cancel_test.go, internal/adapter/claudecode/completeness_test.go
               and 16 more file(s): atenea ask code.search --repo current --json
      dropped  serena.search: needs an index from provider serena, repository has none -- atenea detect looks for one, atenea ask repository.index --repo current builds one
      dropped  claude.search: no attached runner serves it
```

If you only want to know *who would be picked* without spending anything,
`atenea select code.search --repo current` answers the same question for free and
prints the funnel stage by stage.

## `atenea incidents`

The crash notebook. Anything that went wrong badly enough not to fit in a normal
report lands here, and it survives a kill.

```text
2026-08-03 02:05:00  metrics.flush
    metrics: open .../metrics.duckdb: context canceled
```

`atenea incidents clear` marks them read. `atenea metrics` is the other half of
the same story and worth a look once in a while: attempts, failures, how many
were priced, and the worst single call per provider. The gap between attempts and
failures is usually the diagnosis.

If a provider ever returns hits outside the scope it was asked for, `atenea
metrics` prints that count too. It is recorded and never ranked on — a provider
honest enough to report its own overreach must not rank below one that hides it.

## `atenea resume RUN_ID`

For when `task` was interrupted or the process died mid-plan. `--list` shows
what is still worth continuing — every receipt with steps left, oldest first:

```text
$ atenea resume --list
20260808T172045-69883f       canceled  1 step(s) remaining  find every TODO comment
```

Resuming reads the receipt back and dispatches only the steps that never
closed; whatever already succeeded is not repeated:

```text
$ atenea resume 20260808T172045-69883f
run       20260808T172045-69883f
task      find every TODO comment
verdict   ok
matches   2
spent     786ms of tool time over 1 step(s), 821ms elapsed
  explore  0 step(s), 0s in 0s
  work     1 step(s), 786ms in 803ms
```

One step, not two: the first had already closed before the crash and is read
off the receipt rather than paid for again. Resuming a run with nothing left to
do is a clean no-op — `spent 0s of tool time over 0 step(s), 0s elapsed`, the
same verdict as before — not a second billed attempt. `--budget USD` replaces
what remains of the original grant, in case the ceiling was the reason it
stopped; `--allow` instead adds to what the commission already carries, since an
effect already held should never be lost by resuming.

A receipt destroyed by an ugly close is set aside rather than deleted, and
resuming it says so by name: the surviving `.json.torn` file is the only
evidence of what was lost, so the refusal points at that instead of at the
`.json` that is no longer there.
