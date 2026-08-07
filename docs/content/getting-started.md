---
title: Getting started
weight: 1
---

# Getting started

## Requirements

Go 1.24 or newer. Nothing else: Atenea is a single binary and its settings file.

## Build and run

```sh
go build -o bin/atenea ./cmd/atenea
./bin/atenea version
```

A fresh install boots without any setup. When no settings file exists, Atenea
falls back to the built-in defaults, which already carry the P0 capability and
its four candidate providers.

```sh
./bin/atenea status
```

Dispatching real work needs the `omp` CLI on `PATH`: that is the client adapter
the defaults attach. Without it Atenea still plans and chooses — the step fails
as `unavailable`, says which binary it looked for, and the catalog marks that
provider down. On a machine with no client installed, set
`runners = ["local"]` for the stand-in that searches the disk directly.

### Attaching Claude Code

The second client adapter drives the `claude` CLI you are already logged into.
It is off by default because it is the only far side that costs money per call:

```toml
[orchestrator]
runners = ["omp", "claudecode"]
budget_usd = 0.25            # what ONE COMMISSION may spend, across every step
```

No API key is involved. Atenea never sees a credential — it speaks to a client
that is already authenticated, and the session lives inside that client.

`budget_usd` is granted per commission, not per call: a task that dispatches
four steps splits that quarter between them rather than spending it four times.
`atenea task "..." --budget 3` funds one commission above the standing grant.
When a commission runs out, paid steps come back `permission_denied` and free
providers keep working.

With both attached, `ripgrep` still answers an ordinary search: the funnel
ranks on cost, and a flat text search is two orders of magnitude cheaper
through a tool than through a model. Claude Code wins when the cheaper
providers cannot work on that repository or are down — and a `[[selector.rule]]`
will hand it the work outright when you want it.

### Attaching Serena for symbols

Serena answers `symbol.definition`, `symbol.references`,
`symbol.implementations` and `symbol.overview`. It is not a CLI: it runs as an
MCP server behind a local proxy, so the setting is a URL rather than a binary.

```toml
[orchestrator]
runners = ["omp", "serena"]

  [orchestrator.serena]
  endpoint = "http://127.0.0.1:40010/mcp"
```

Two things have to be true for the funnel to reach it. Serena drives one
language server per language, so the repository's `languages` must be among the
ones the implementation declares; and it is useless on a repository it has not
indexed, so name it in `indexed_by`:

```toml
[[repository]]
id = "current"
path = "."
languages = ["go"]
indexed_by = ["serena"]
```

Miss either and the funnel drops it at `reach` or `constraints` and says which
— a provider nobody wired up is not a provider that is broken.

### Attaching codebase-memory

`codebase-memory` answers `symbol.calls` and `code.impact` by walking a call
graph it keeps on disk instead of parsing anything live, and `repository.index`
builds that graph in the first place — the one thing neither a grep nor a
language server has. It is a CLI like `omp`, so the
setting is a binary name, not a URL:

```toml
[orchestrator]
runners = ["omp", "codebasememory"]

  [orchestrator.codebasememory]
  binary = "codebase-memory-mcp"
```

Both implementations declare `requires_index = true` and a `min_scale =
"medium"` floor: unlike Serena, this provider only ever answers from an
index built ahead of time, so a repository nothing has indexed, or one too
small to have declared its scale, is refused rather than sent to a provider
with nothing on disk to answer from — the funnel drops it at `constraints`
or `reach` and says which, the same as any other unattached provider.

`code.impact` alone also declares `requires_vcs = true`: it measures against a
point in the repository's history, and there is none to measure against on a
directory with no version control at its root. A repository nobody has said
either way about is not refused for it — only one explicitly declared
`vcs = "absent"` is, the same up-front, no-dispatch drop as the index case
above instead of a git failure surfacing mid-call.

Either belief can go stale independently of anything breaking: an index
built after the settings file already named the repository leaves
`indexed_by` still reading "none" for a real one, and the funnel has no way
to tell the difference from the outside. `atenea detect --repo current` asks
every attached provider that can answer and corrects the belief for the
rest of that process's run — nothing on disk changes, so a later invocation
starts again from what the file declares, the same as health already does.
A repository truly untouched needs `atenea ask repository.index --repo
current` instead, which spawns the build itself: `write` and `process`
effects, the one action detecting is built to never take on its own.

## Write your own settings

```sh
./bin/atenea config path     # where Atenea will look
./bin/atenea config init     # write the built-in defaults there
```

Settings are resolved in this order:

1. `--config PATH`
2. `$ATENEA_CONFIG`
3. `$XDG_CONFIG_HOME/atenea/atenea.toml`, falling back to `~/.config/atenea/atenea.toml`
4. the built-in defaults

A file named explicitly with `--config` must exist. A missing file at the
default location is not an error — that is the fresh-install path — but a
missing file you asked for by name is, because staying quiet there would hide a
typo.

## Ask the funnel a question

```sh
./bin/atenea select code.search --repo current
```

```text
capability  code.search
repository  current
chosen      ripgrep  (cheapest of the healthy ones (estimated))

funnel
  constraints  3 in -> 2 out: claude.search, ripgrep
      dropped serena.search: needs an index from provider serena, repository has none -- atenea detect looks for one, atenea ask repository.index --repo current builds one
  reach        2 in -> 1 out: ripgrep
      dropped claude.search: no attached runner serves it
  health       1 in -> 1 out: ripgrep
  choice       1 in -> 1 out: ripgrep
```

Every decision carries its trace. A choice nobody can explain is a choice nobody
can trust, and the trace is what later turns into the observability layer.

Each stage answers a different question, and the trace says which one settled
it. `constraints` asks whether a provider can work on this repository at all;
`reach` asks whether anything attached can even invoke it; `health` asks whether
it is well. What is left, `cost` ranks — and the word `estimated` in the reason
is the trace admitting that no measurement exists yet, so the number is the one
the catalog declared rather than one Atenea observed. Hand it some work and
that word changes; [the base](#hand-it-a-commission) is where it comes from.

## Hand it a commission

`select` asks who *would* answer. `task` hands the whole job over: the
orchestrator looks at every repository in scope, splits the work, dispatches it
and reviews what comes back.

```sh
./bin/atenea task "ValidateOutput" --trace
```

```text
run       20260802T003739-e22d82
task      ValidateOutput
verdict   ok
matches   11
spent     12ms over 2 step(s)
  explore  1 step(s), 6ms
  work     1 step(s), 5ms

discovered
  [repository] current: 11 hit(s) for "ValidateOutput", under internal, pkg

plan
  wave 1  explore-current
  wave 2  search-current

steps
  explore-current      explore  ripgrep                  6ms
      review   child=ok parent=ok (output matches the capability)
      dropped  serena.search: needs an index from provider serena, repository has none -- atenea detect looks for one, atenea ask repository.index --repo current builds one
  search-current       work     ripgrep                  5ms
      review   child=ok parent=ok (output matches the capability)
      scope    internal, pkg
      dropped  serena.search: needs an index from provider serena, repository has none -- atenea detect looks for one, atenea ask repository.index --repo current builds one
```

Two heights, like the status screen: the summary always, the full trace only
when asked for. Drop `--trace` and everything from `plan` down disappears.

The look found hits under `internal` and `pkg` only, so the work that followed
was narrowed to those two areas instead of walking the tree again.

A hit sitting at the repository root is the one case that cannot be narrowed:
there is no directory above it, so the work runs wide rather than quietly
dropping it. That is easy to see for yourself — this page and the README now
quote the search term, so running the example against Atenea's own repository
reports more hits and no `scope` line.

`--repo` narrows the commission; repeat it for several. Every run leaves a
receipt under `$XDG_STATE_HOME/atenea/runs`, including one that was cut short.

What each step cost lands beside it, in `metrics.duckdb`: one row per attempt,
including the ones that failed. That base is what the funnel ranks on. Nothing
has to be switched on for it, and `enabled = false` under `[metrics]` stops it.

Run the same ask a few times and watch the reason on `select` change. It opens
at `(estimated)`, passes through `break-in turn` while each provider is handed
the work often enough to earn its own numbers, and settles at `(measured)` —
at which point the estimates in the settings file stop mattering for that
repository. A provider the file guessed was expensive can win here, and that
is the entire point of handing out those turns.

What a step *charged*, if anything did, is not in there. Money is never one of
the axes the funnel ranks on, so it stays out of the base and goes on the
receipt instead: a `charged` line on the summary when a run cost anything, the
step that incurred it under `--trace`, and `spent_usd` on the run in
`$XDG_STATE_HOME/atenea/runs`.

The ceiling each step was held to goes on the receipt beside what it charged,
so a run that came back short says whether the provider stopped or the money
did: `answered current for $0.10 of its $0.90 ceiling`.

## Ask for one capability

`task` is a commission: explore, split, dispatch. `ask` is the atom underneath
it — one capability, one repository, no planning:

```sh
./bin/atenea ask symbol.definition --repo current \
  --set file=internal/selector/selector.go --set line=118 --set column=18
```

```text
run       20260802T132043-9f7e98
task      symbol.definition in current
verdict   ok
spent     41ms over 1 step(s)
  ask      1 step(s), 41ms

discovered
  [repository] position internal/selector/selector.go:118:18 names "Select", which is symbol Selector/Select
  [repository] serena answered symbol.definition for current with 1 location(s)

answer
  location
    line     118
    path     internal/selector/selector.go
```

`--set` takes `name=value` and is typed by the capability's own declaration:
`line` is an integer because the capability says so, and a value that is not
one is refused before anything is dispatched. Repeat it for a list field, and
repeat the flag for each entry:

```sh
./bin/atenea ask symbol.references --repo current \
  --set file=pkg/contract/capability.go --set line=140 --set column=6 \
  --set scope=internal --set scope=cmd
```

There is no `matches` line: a commission counts hits across repositories, an
ask has an answer. Printing a zero nobody counted would read as "found
nothing" rather than "did not count".

The trace names the symbol the position resolved to. That is not decoration:
Atenea speaks positions and Serena speaks symbols, so the answer cannot be
checked against the question without it.

## Run it as a service

```sh
./bin/atenea run
```

It boots the catalog and waits. `Ctrl-C` or `SIGTERM` starts a clean stop: new
work is refused immediately, and whatever is already running gets the margin set
by `core.shutdown_grace`.

## Install it on the machine

`run` in a terminal lasts as long as the terminal. To have Atenea there after a
reboot, install it — the binary writes its own unit:

```sh
atenea service install     # writes the unit, enables it, does not start it
systemctl --user start atenea.service
atenea service status      # where it stands
```

A user unit, never a system one. Atenea holds no privilege worth borrowing and
everything it touches is inside your home, so `sudo` would only widen what a
bug could reach. The price is that a user unit needs lingering to survive a
logout; `service status` prints whether you have it, and the command to get it
if you do not.

`ExecStart` is the absolute path of whichever binary installed the unit. Move
the binary and the unit points at nothing, so install from where it will live:

```sh
go build -o ~/.local/bin/atenea ./cmd/atenea
~/.local/bin/atenea service install
```

Nothing listens. There is no port, no socket and no API: the service is the same
core the commands use, kept up so the rhythms can beat. `atenea status` reads the
same disk rather than asking it anything, which is why it works whether the
service is running or not.

`atenea service uninstall` stops it, disables it and removes the unit. It leaves
the state root alone — what Atenea has learned is not the service's to delete.

## Day to day

Three things happen on their own once it is installed, and one line tells you
whether they are still happening:

```text
background
  rhythms      metrics.flush 30s, metrics.compact 1h, backup 6h
  copies       5 of 5 kept in ~/.local/state/atenea-backups, newest 2026-08-02 21:26
```

`rhythms` comes from the settings file, `copies` from the disk. Neither is a
tally this command kept in memory, which matters because the command is a
process that lives for a second and the service it reports on is not: a per
process counter would say "not yet" on a machine that has been copying all day.

**Copies** are hard-linked, so five snapshots of an unchanged base cost one
base. A file that changed is copied and the older snapshot keeps the older
bytes. The sixth arrives and the oldest leaves. They live *beside* the state
root, never inside it — a copy under the tree it copies recurses into itself.
Restoring is `cp -a`: point `XDG_STATE_HOME` at the restored folder and Atenea
opens it.

`STALE` appears when the newest copy is older than two rhythms. Two, not one:
a beat can be seconds late and a light that flapped would train the eye to skip
it.

**An ugly close** — a power cut, a `SIGKILL` — is repaired on the way up, before
any work is accepted, and says so once:

```text
recovered 1 interrupted dump(s) swept, 1 torn receipt(s) set aside
```

A dump interrupted mid-write is removed: it is a record of a run that never
happened that way. A receipt that will not parse is renamed `.torn` rather than
deleted, because the bytes are the only evidence left of what was lost. Good
receipts are never touched. If the measurement base itself will not answer it is
moved aside under its own name and a fresh one opened in its place — the history
that went with it is what the copies are for.

A base held open by another Atenea is not damage and is never moved. That check
is the difference between recovering from corruption and manufacturing it.

**The light** is one glance, and it is the worst of everything under it. Amber
for Atenea being unwell — a lane that failed, copies gone stale, an ugly close
it recovered from, an incident nobody has read — and amber for a provider that
is down or has never been measured. The work still gets done in every one of
those cases, which is why none of them is red.

```sh
atenea status              # the light, the providers, the background
atenea incidents           # what went wrong, with paths
atenea incidents clear     # says you have read them
```

Clearing removes *that* reason for amber and no other. A fresh install is amber
because `health=unknown` is not a claim that anything works — it is Atenea
saying nobody has looked yet. That is the break-in period, and the way out of
it is work: the first successful call against a repository turns that provider
green, and the screen says what it is claiming and when.

```text
  green  ripgrep  provider=ripgrep  health=alive  (last call here worked, 3m12s ago)
```

An hour after the last successful call it goes back to unknown. That is not a
fault and nothing has broken — a success is a statement about the moment it
happened, and a screen that stayed green overnight would be reporting last
night. Amber here means *nobody has looked recently*, which on a machine you
have not used today is the truth.

A provider the record has caught failing says so instead, with the count, the
bin and its own words:

```text
  amber  claude.search  provider=claude-code  health=down
         (3 unavailable failures in a row, last one claude code is not logged in on this machine)
```

Amber, not red, even for a provider that is out. The funnel drops it and the
work goes to whoever is left, which is the system doing its job; red is
reserved for a capability with nothing able to answer it at all. A machine
where one client is permanently unusable would otherwise show a red light that
never goes off, which says as little as an amber nobody can clear.

On a workspace the reason names the repository it came from, because "down" and
"down on `scripts`" are different instructions. The state shown is the worst
one the record found anywhere: a provider that is warm on one repository and
dead on another is not well.

The amber from a fault comes back the next time it happens. That is what makes
*unread* the honest word for it.

## What the base measured, and how to forget it

Every attempt is written down: what it cost, whether it worked, and the bin it
failed in. That record is what the funnel ranks on, so it is worth being able
to look at.

```sh
atenea metrics             # per capability, implementation and repository
```

```text
capability         implementation         repository      tries   failed   priced       each      worst
code.search        ripgrep                current            40        0       40     1.01s      1.04s
code.search        claude.search          current            14       14        0         -    948ms
```

The three counts sit together because the gap between them is the diagnosis.
**Only the priced ones are a price.** A failure is counted and never averaged
in: a provider that refuses instantly — not logged in, no index, no server —
would otherwise record a stream of very fast, very cheap calls and become the
cheapest thing on the machine, and the funnel would hand it everything while
every commission failed. Failing cheaply must not pay. `claude.search` above
has fourteen attempts and no cost at all, so it ranks on whatever estimate the
settings file declared for it, and the trace says so.

Failures decide health instead. Three in a row *in the same bin* is an outage:
the provider leaves the funnel and the trace names the count, the bin and what
the provider actually said. Three in a row in *different* bins is a provider in
trouble with no single cause, so it is marked degraded and ranks last rather
than being dropped — the funnel would rather use a flaky provider than none.

*In a row* counts back from the newest failure and stops at the first one that
broke differently. So a provider with a long record of assorted trouble, now
failing every call the same way, is down on today's reason — the older, often
already-fixed causes further back do not dilute it.

Both verdicts expire. A provider dropped by health is a provider nothing calls,
so nothing could ever prove it recovered; after five quiet minutes the streak
stops counting and the next call goes through. The older failures stay on
record, so a relapse costs one call and a recovery costs one call.

The base is the only thing here that decides behavior and cannot be edited by
hand. It is true by construction — those calls really did fail — and it stays
true long after the machine it describes has been fixed. So it can be forgotten,
narrowly:

```sh
atenea metrics clear --implementation claude.search   # one provider's record
atenea metrics clear --repository api                 # one repository's
atenea metrics clear --all                            # the lot; --all is required
```

A narrowing flag is a statement of intent on its own. A bare `clear` is refused,
because emptying the whole base is the one act in Atenea that destroys something
nothing else can rebuild.

## When Atenea itself falls over

Providers fail all the time; that is a normal answer with a bin on it. Atenea
breaking is the rare one, and it is written to a separate file the instant it
happens — before the process is allowed to die.

The status screen only mentions it when there is something to mention:

```text
atenea 0.5.0  contract 1.9.0  AMBER
funnel    constraints -> reach -> health -> cost (measured for 1 of 11 implementations, the rest on declared estimates)
incidents 1 unread, latest 2026-08-02 19:32:35  (atenea incidents)
```

```sh
./bin/atenea incidents          # print the new ones, whole, with stacks
./bin/atenea incidents --all    # including the ones already marked read
./bin/atenea incidents clear    # move the mark; nothing is deleted
```

```text
2026-08-02 19:32:35  orchestrator.step  run=20260802T173235-8912fd  step=ask-current  capability=code.search  repository=current  fields=query
    the runner reached a state it does not have a name for
    goroutine 20 [running]:
    ...
```

Reading changes nothing on disk, so two people can investigate the same crash
and see the same file. `fields=query` is the payload's keys and never its
values — a crash dump is the likeliest thing to end up pasted into a bug
report.

The notebook has no settings. One that you have to switch on before it works
is one that is off on the day you need it. It lives beside the run receipts,
at `$XDG_STATE_HOME/atenea/incidents.jsonl`.

## Exit codes

A script has to be able to tell a broken settings file from a provider that is
simply down, so the failure bins map onto distinct codes.

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `2` | `invalid_input` — bad arguments or a broken settings file |
| `3` | `not_found` — unknown capability, repository, or nothing fits |
| `4` | `unavailable` / `timeout` |
| `5` | `permission_denied` / `external_denied` |
| `6` | the commission ran and came back `failed` |
| `130` | you stopped it — `128 + SIGINT`, which is the number a shell reports for ctrl-c on its own |
| `1` | anything unsorted, which means a bug |

`6` is a different axis from the rest. Nothing about the call was wrong and the
report on stdout is complete — the work is what failed. It cannot borrow `1`,
which means a bug, and it cannot be folded into the bins above it either,
because several steps can fail for different reasons in one run. The reason
lives in the report; the exit code only says that there is one.

`130` is its own axis too, and the reason it is not `4` is worth a line. A
script that retries on a timeout must not retry this one: nothing is wrong,
somebody asked for it to stop. What Atenea does on the way out is finish the
sentence honestly — it kills the whole process group it started, so a client
that spawned helpers of its own does not go on holding the terminal; it writes
the measurements the run had already earned, because that work was paid for;
and it records nothing about the call that was interrupted, because nobody
learned anything about how fast that provider is.

## Development

```sh
go test -race ./...     # the suite
lefthook install        # pre-commit: gofmt, go vet, golangci-lint
air                     # hot reload while developing, local only
```

### Publishing these docs

The `docs` workflow builds this site and deploys it to GitHub Pages on every
push that touches `docs/`. It needs Pages to already exist, and the workflow
cannot create it: `GITHUB_TOKEN` can deploy to a site, but creating one is
repository administration and that escalation is deliberately closed. On a
fresh fork it is one command, run once, by an account that administers the
repository:

```sh
gh api -X POST /repos/OWNER/REPO/pages -f build_type=workflow
```
