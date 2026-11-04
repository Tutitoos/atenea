---
title: Day to day
weight: 2
---

# Day to day

Atenea runs as a background service, installed once with `atenea service install`
and started with the machine. Nothing here starts or stops it. These are the four
commands worth remembering; `atenea` on its own prints the rest.

| | When |
| --- | --- |
| `atenea ask CAPABILITY` | Every time you want something answered from the CLI |
| `atenea status` | When you want to know if anything is wrong |
| `--trace` | When an answer looks wrong and you want to see who was picked |
| `atenea incidents` | After a crash, or when the light is amber and you do not know why |

MCP clients make the same calls through the service: every capability is a tool,
and `atenea ask` is the terminal's way to dispatch one.

## `atenea ask CAPABILITY`

The one you actually use. Name a capability and a repository; Atenea picks who
answers, reviews the answer and reports back.

```text
$ atenea ask code.search --repo current --set query=TODO
run       20261104T201438-21de9359f607e059f2d874ecdf3f75ab
task      code.search in current
verdict   ok
spent     16ms of tool time over 1 step(s), 36ms elapsed
  ask      1 step(s), 16ms in 31ms

run with --trace for the plan, the funnel and every review

answer
  matches (2)
    1
      column   4
      line     3
      path     pkg/a.go
      snippet  package pkg

// TODO: first
func A() {}

    2
      column   4
      line     3
      path     pkg/b.go
      snippet  package pkg

// TODO: second
func B() {}
```

`verdict` is the whole summary: `ok`, `failed`, or `canceled` if you stopped it.
The exit code says the same thing to a script — `0` worked, `6` means the
call ran and came back failed, `130` means you pressed ctrl-c. Note that `1`
is **not** "it did not work": it is reserved for a failure Atenea could not sort,
which means a bug worth reporting. [Getting started]({{< relref "getting-started" >}})
has the full table.

The two times are different questions. `of tool time` is the sum of every step,
which is what the work cost; `elapsed` is the wall, which is what you waited.
`--json` carries `elapsed_ms` beside `spent_ms` plus a `closed_at` per step.

`--budget USD` funds one call above whatever the settings file grants. Money
is a permission here: a step with none refuses before spawning anything.
`--allow EFFECT` grants one more beyond what the settings file already stands
behind, repeatable for several — most calls never need it, since
`code.search` already ships pre-granted the one thing it actually causes.

`--repo` is required when more than one repository is registered. `atenea select
code.search --repo current` shows who would answer without dispatching anything.

## `atenea status`

One screen. The first light is Atenea as a whole, then one per provider.

```text
code.search              [read process]
    amber  claude.search            provider=claude-code        health=unknown
    amber  codex.search             provider=codex              health=unknown
    amber  ripgrep                  provider=ripgrep            health=unknown
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

Goes on `ask`. Prints the plan, who was chosen for each step, what it
charged, which files it found, and — the useful half — who was dropped and why.
A drop that is identical in every step prints once at the end instead of under
each one: it is a fact about your catalog, not about any step.

```text
steps
  ask-current          ask      ripgrep                  1.588s
      review   child=ok parent=ok (output matches the capability)
      found    cmd/atenea/cancel_test.go, cmd/atenea/json_test.go, cmd/atenea/main.go, cmd/atenea/main_test.go, cmd/atenea/money_test.go, docs/content/day-to-day.md, internal/adapter/claudecode/cancel_test.go, internal/adapter/claudecode/completeness_test.go
               and 16 more file(s): atenea ask code.search --repo current --json
      dropped  claude.search: no attached runner serves it
      dropped  codex.search: no attached runner serves it
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
