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
its three candidate providers.

A binary built from a checkout stamps its own revision onto the version, so
`version` prints `0.10.0+2cd1401` rather than the bare number quoted on these
pages — and `0.10.0+2cd1401.modified` when the tree has uncommitted changes.
That suffix is SemVer build metadata: it says which tree this came from and is
ignored when versions are compared. A bare number means the build had nothing to
stamp: a release artifact, or a build from a linked `git worktree`, which Go does
not stamp even with `-buildvcs=true`. Do not read a bare number as proof of a
release on a machine where someone builds in worktrees.

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

Whichever of those four you land on, `atenea status` compares the catalog it
loaded against the one built into the binary and prints a `catalog` line naming
anything the binary ships that your file does not declare. A settings file
replaces the catalog rather than patching it, so an older file silently misses
whatever later releases added — this is how you find out, instead of finding
out the day a funnel that should have had a fallback turns out to have one
candidate. It stays quiet about capabilities you removed on purpose.

## Ask the funnel a question

```sh
./bin/atenea select code.search --repo current
```

```text
capability  code.search
repository  current
chosen      ripgrep  (the only surviving implementation)

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
run       20260808T172133-fcb614
task      ValidateOutput
verdict   ok
matches   30
spent     1.205s of tool time over 2 step(s), 1.276s elapsed
  explore  1 step(s), 778ms in 797ms
  work     1 step(s), 426ms in 442ms

discovered
  [repository] current: 30 hit(s) for "ValidateOutput"

plan
  wave 1  explore-current
  wave 2  search-current

steps
  explore-current      explore  ripgrep                  778ms
      review   child=ok parent=ok (output matches the capability)
      found    CHANGELOG.md, README.md, docs/content/getting-started.md, internal/adapter/claudecode/claudecode.go, internal/adapter/claudecode/claudecode_test.go, internal/adapter/codebasememory/calls.go, internal/adapter/codebasememory/impact.go, internal/adapter/codebasememory/index.go
               and 9 more file(s): atenea ask code.search --repo current --json
  search-current       work     ripgrep                  426ms
      review   child=ok parent=ok (output matches the capability)
      found    CHANGELOG.md, README.md, docs/content/getting-started.md, internal/adapter/claudecode/claudecode.go, internal/adapter/claudecode/claudecode_test.go, internal/adapter/codebasememory/calls.go, internal/adapter/codebasememory/impact.go, internal/adapter/codebasememory/index.go
               and 9 more file(s): atenea ask code.search --repo current --json

dropped in every step
  serena.search: needs an index from provider serena, repository has none -- atenea detect looks for one, atenea ask repository.index --repo current builds one
  claude.search: no attached runner serves it
```

Two heights, like the status screen: the summary always, the full trace only
when asked for. Drop `--trace` and everything from `plan` down disappears.

Each step names the files it found, capped at eight, and when it caps it gives
you the command that prints the rest: a count is all that composes across
repositories, but a count is not something anybody can act on. The two drops
are identical in both steps, so they print once at the end rather than twice
each — a fact about this machine's catalog, not about either step.

That run is Atenea's own repository, and it shows the one case that cannot be
narrowed: this page and the README quote the search term, and a hit with no
directory above it has no area to narrow to. So the work ran wide — no `scope`
line — rather than quietly dropping it. Against a repository where the hits sit
under `internal` and `pkg` only, the work that follows is narrowed to those two
areas instead of walking the tree again.

The timings are omp's, the one runner the shipped settings attach. Every other
implementation of `code.search` is dropped here for a reason the trace states.

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

`atenea status` counts the same threshold in its funnel caption: an
implementation is measured there once it has enough successful calls for its own
numbers to be believed over its declared estimate, which is two -- one can be a
cold cache. That is why a caption saying nothing is measured yet can sit above a
base that already has rows in it, and why the caption and the reason on `select`
never disagree.

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
  --set file=internal/selector/selector.go --set line=161 --set column=20
```

```text
run       20260808T172223-d465d5
task      symbol.definition in current
verdict   ok
spent     158ms of tool time over 1 step(s), 207ms elapsed
  ask      1 step(s), 158ms in 178ms

discovered
  [repository] position internal/selector/selector.go:161:20 names "Select", which is symbol Select
  [repository] serena answered symbol.definition for current with 1 location(s)

run with --trace for the plan, the funnel and every review

answer
  location
    line     161
    path     internal/selector/selector.go
```

A position is a position in *this* tree: these two examples name real lines in
Atenea's own source, and editing the file moves them. If one comes back
`not_found`, the line moved — that is the capability working, not failing.

`--set` takes `name=value` and is typed by the capability's own declaration:
`line` is an integer because the capability says so, and a value that is not
one is refused before anything is dispatched. Repeat it for a list field, and
repeat the flag for each entry:

```sh
./bin/atenea ask symbol.references --repo current \
  --set file=pkg/contract/capability.go --set line=135 --set column=6 \
  --set scope=internal --set scope=cmd
```

There is no `matches` line: a commission counts hits across repositories, an
ask has an answer. Printing a zero nobody counted would read as "found
nothing" rather than "did not count".

Everything above prints for a person. `task`, `ask`, `resume` and `detect` also
take `--json`, which prints the whole result as one JSON object instead — always
complete, and it ignores `--trace` rather than interleaving prose into it. That
is the mode to parse; the prose layout above is not a format anything should
depend on.

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

There is no port and no network: the one thing the service listens on is a Unix
socket in your own state root, opened by the service and reachable only by you.
`atenea status` knocks on it and prints what the running service says — the
uptime, the clock's real run, the chats open right now, none of which a command
can know on its own. With no service up it works out what it can from disk and
says so on the `process` line, so the command works either way.

`atenea service uninstall` stops it, disables it and removes the unit. It leaves
the state root alone — what Atenea has learned is not the service's to delete.

## Connect a client

Atenea is an MCP server, and the way in is `atenea mcp`: a bridge the client
launches, speaking MCP on stdin and stdout and relaying to the running service.
It decides nothing on its own — the catalog, the funnel and the permissions all
live in the one service — so a client that connects sees exactly what
`atenea catalog` shows.

```json
{
  "mcpServers": {
    "atenea": { "command": "atenea", "args": ["mcp"] }
  }
}
```

Every capability becomes a tool, described by the declaration in your settings
file. Every tool takes a `repository`, because that is Atenea's unit of work —
required only when you have more than one registered, exactly like `--repo` on
the command line.

Check the setup without going through a client, which is worth doing because a
client that cannot start its server usually says so in one line and hides the
reason:

```text
$ atenea mcp --check
atenea 0.10.0 is listening at ~/.local/state/atenea/run/core.sock
8 capability(ies) would be offered as tools
2 chat(s) open right now
```

Each connected client is a chat, named by the client itself, and `atenea status`
lists them while they last:

```text
chats
  claude-code      c-8f21e4   up 12m      runs 3   adds=-
  omp              c-1b90aa   up 4m       runs 0   adds=-
```

`adds` is what that chat asked for on top of the standing grant in your settings
file — not everything it may do. A dash means it asked for nothing extra, and it
still runs on the floor that file sets.

## Put a section in the client's sidebar

`atenea status` answers when you ask it. These answer without being asked: three
widgets in opencode's sidebar, drawing four things — three sections under the
`Context` box it draws itself, and the footer at the bottom of that column. They
are installed separately on purpose, because they read different things.

```text
$ atenea statusline widgets
atenea         Atenea's traffic light, the version running and unread incidents
session-share  which model did what share of this session's tokens
limits         how much of each provider's live rate-limit window is used
```

```text
Context
217,811 tokens
22% used
$13.04 spent

▶ Models (MiniMax-M3 60%, +6)

▶ Claude (5h 78% · 7d 56%)

▶ Codex (7d 91%)

▼ MCP
• atenea  Connected

LSP
LSPs will activate as files are read

~/Desktop/atenea

• OpenCode 1.18.16
⊙ Atenea 0.10.1+751972f
```

**That column has to be on screen for any of them to appear.** It is 42 columns
wide — of which a section's own text gets **37**, measured with rulers drawn inside
it at 200, 130 and 121 columns of terminal, and the renderer **wraps** rather than
clips, so a 38-column line becomes a second row nobody asked for. Its default state
is `auto`, which shows it only when the terminal is wider than 120 columns — and
never for a child session. Read out of the shipped client (1.18.16), which also
carries `show` and `hide` for that state. On a narrow terminal these widgets are
installed, declared, loaded, and invisible, and no command of ours can tell you so:
the placement is the client's.

The column is ordered, and the client's own sections claim round numbers —
`Context` 100, `MCP` 200, `LSP` 300, `Todo` 400, `Files` 500. `Models` sits at 150
so it continues the `Context` box it answers next to, and the two rate-limit
sections at 151 and 152 directly under it. A test pins every one of those, reads
them out of the file that ships, and refuses a registration naming a slot outside
the sidebar.

The last three lines are not in that column at all. They are the client's footer,
a box **outside** the scroll, pinned to the bottom — and Atenea draws it. The
client publishes it as a `sidebar_footer` slot declared `mode:"single_winner"`:
the lowest registered order wins it outright and the host's own lines are slot
children used **only** if the winner draws nothing. So there is no arrangement in
which both draw, and adding a line under the version means owning every line
there. This widget registers at 50, beating the client's 100, and renders the
project path, the client's version, and Atenea's line — which is the whole point,
since a line reporting a service belongs beside the version of the thing it runs
under, not a gap above it.

Three rules come with owning it, and each one is a check rather than a promise:

- **The two versions come from two places and neither is remembered.** The
  client's is read from the host on every paint; Atenea's from its socket. An
  unreadable one prints `sin lectura` **in its own slot** — a version line that
  keeps showing the last good value is the one failure it cannot survive.
- **The widget hands the slot back rather than deleting something.** On a machine
  with no paid provider the client uses that same footer for a `Getting started`
  card with a `/connect` prompt. Both halves of its condition are readable at
  registration time, so the widget checks, declines the footer, and sits in the
  column at 900 instead. Anything it cannot read counts as onboarding: declining
  costs one line's placement, guessing wrong costs a first-run user the only
  prompt that tells them how to connect a provider.
- **A client that changes its footer breaks a build.** The shape of that
  component — every string it draws, every field it reads, how many elements it
  builds — is pinned in `testdata/opencode-footer.json` and compared against the
  client installed on the machine. The pre-commit hook runs it here; a scheduled
  workflow installs the newest published client each morning and runs it there.
  When they add a line, that fails and names it, instead of the line quietly
  disappearing from your screen.

One placement is genuinely **not** available: **inside the `Context` box**. There
is no slot there. The client offers twelve — `app`, `app_bottom`, `home_logo`,
`home_prompt`, `home_prompt_right`, `home_bottom`, `home_footer`,
`session_prompt`, `session_prompt_right`, `sidebar_title`, `sidebar_content`,
`sidebar_footer` — and a section of one's own directly beneath it is as close as
it allows.

**When a section outgrows the room, the column scrolls rather than clipping, and
the footer stays pinned.** The content sits in a 42-column `scrollbox`: a probe with
24 rows rendered all 24 and pushed the client's own `LSP` body below the fold,
while the footer stayed exactly where it was. Nothing was dropped — the same
screen at a taller pane showed all of it again — but content below the fold is
content nobody reads, which is why the model list arrives collapsed.

### The traffic light

```text
$ atenea statusline install atenea
widget    atenea
plugin    ~/.config/opencode/plugins-tui/atenea.tsx
declared  ~/.config/opencode/tui.json

a running opencode loads plugins at startup: restart it to see the line.
```

It reads the service's own unix socket — the same door this CLI knocks on — so it
needs no key, no port and no network. What it draws:

```text
⊙ Atenea 0.10.1+751972f  2 sin leer
```

One line, written the way the client writes its own version line: a coloured
bullet, the name, the version. The bullet carries the light: green, amber, red, and
muted grey when nothing is running. That last state says `apagado` rather than
warning, because a service you stopped on purpose is not a fault — and the two are
told apart by whether the socket file exists, not by the connection error, which
arrives as `ENOENT` whether the path is missing, stale, or unreadable. The unread
count is only there when something is unread, and it sits beside the version rather
than under it: in this column an empty line of its own would cost a visible blank
row, measured against the real sidebar before the shape was chosen.

The version is printed exactly as the service reports it, build metadata and all.
Trimming `0.10.1+751972f.modified` down to `0.10.1` would hide the part that says
this binary is not the one that was tagged.

### Which model did the work

The second widget answers a question about the session in front of you, and it
never talks to Atenea at all:

```text
▶ Models (MiniMax-M3 60%, +6)
```

Closed, which is how it arrives, and one click on the header opens it:

```text
▼ Models
MiniMax-M3 86M (60%)
deepseek-v4-pro 42M (30%)
glm-5.2 10M (7%)
qwen3.7-max 3M (2%)
gemini-3.1-pro-preview 699k (<1%)
qwen3.7-plus 442k (<1%)
minimax-m3 394k (<1%)
```

Shares, not money. opencode does store a `cost` per message, and adding it up is
one line of SQL — but on a subscription that figure is list price for traffic
that was never itemised, so a panel printing `$42.78` would be stating as fact
something no invoice ever said. The share is true under either billing.

**The base is every token the model handled**, which is the `tokens.total` the
client records per assistant message — input, output, reasoning and cache reads
together — falling back to the sum of those parts when an older row has no
total. Two models, one session, and the answer moves with the base: measured on
a 1,578-message session, MiniMax-M3 is **97.8 %** of all tokens but **88.4 %**
of input-plus-output alone, because cache reads dominate a long session and they
are work the model was actually handed. Rows with no tokens at all are dropped
rather than printed as `0 %`: a model you opened and abandoned did no share of
anything.

**Both numbers on a line come from that one sum.** The tokens printed are exactly
the ones the percentage divides, so the pair cannot disagree — a cache-excluded
count beside a cache-included share would read as consistent and be wrong by the
nine points those bases differ by here.

**Every model gets a line, and the section is collapsed until you ask.** There was
a cap of five here, with a summed `+2 otros` line under it so the visible shares
still accounted for the session. Collapsing retires both: shut, this is one line
that cannot push anything below the fold; open, the length is the length you asked
for. A share that rounds to nothing prints `<1%` instead of `0%`, because
`699k (0%)` states two things that contradict each other. Rounding still lets the
column sum to 99 or 101, and that is left visible rather than fudged.

**Closed, the header still answers something.** It carries the dominant model with
its share and how many others there were — `(MiniMax-M3 60%, +6)` — because a
collapsed section that shows only its own name is a line spent on a label. A
failure is louder shut than open: `sin lectura` and `sin modelos todavia` are
printed in the header itself, so closing this can never turn a broken reading into
an absence nobody notices. There is no arrow when there is no list behind it,
which is how the client's own `MCP` section behaves above two servers.

It folds on a **mouse click** on its header, and that is parity rather than a
shortfall: the client collapses its own `MCP` section with the same handler and a
local signal, with no command and no key binding anywhere near it. The open state
is not remembered across restarts — collapsed is the state this section wants on
every launch.

There is no combined total: the `Context` box directly above already reports the
session's tokens, and a second total invites reconciling two numbers that answer
different questions.

It reads opencode's own SQLite store, opened **read-only**, for the session id
the client hands the plugin — no socket, no network, and nothing of yours leaves
the machine. The store is 3 GB here and the query costs 2 ms, because it lands on
an index the client already keeps on `(session_id, time_created, id)`.

Every state says something, because a blank line is a visible empty row in that
column: a session with nothing in it reads `sin modelos todavia`, and a store that
will not answer reads `sin lectura` rather than the last good percentages, which
would otherwise keep showing numbers that had stopped arriving.

### How much of the window is left

One collapsible section per provider, collapsed on every launch, and nothing at
all when no window is live:

```text
▶ Claude (5h 78% · 7d 56%)
▶ Codex (7d 91%)
```

Open, each window gets a bar and a line saying when it comes back:

```text
▼ Claude
5h  ████░░░░░░░░░░░░░░░░  78% free
    resets in 2h42m
7d  █████████░░░░░░░░░░░  56% free
    resets in 3d0h
7d  ░░░░░░░░░░░░░░░░░░░░ 100% free
    Fable only
```

The closed header carries **both** account windows, fastest first, and drops from
the right if it ever will not fit — so the five-hour figure is the one that
survives a narrow line. That is deliberate: the question a closed section has to
answer is "is something about to stop me", which the five-hour window answers and
the weekly one does not. A window belonging to one model rather than the account
(`Fable`) is never in the header, because two rows reading `7d` there would invite
you to reconcile them; it is one click away in the body.

**Where the numbers come from — and the measurement that was wrong.** The first
version of this widget read `~/.claude.json` and a codex session rollout, and was
a single line for a measured reason: those files are refreshed only by a human, so
a five-hour window looked live about 4 % of the time and a section with a body
would have said `sin ventana viva` almost always.

That measured the *client that rewrites a file* and then reported on *the number
inside it*. This machine had, the whole time, a source that refreshes itself: omp
keeps one usage report per provider in its own store and refreshes each roughly
every ten minutes while it runs. Measured on 2026-08-11 — readings one minute old
for both providers, a median gap of one hour. With a reading that refreshes itself,
a section that answers on a click is the honest shape.

Not the `usage_history` table in the same store, which looks like the obvious
source and is a trap: it prunes — 7 029 rows while its maximum advances — and
writes one, two or three of a provider's limits per reading, so "the newest row per
provider" silently loses whichever window was not in the last write. A row read at
01:01 was gone eight minutes later. The cached report is the whole thing, written
at once, and it carries what a rule here would otherwise guess: `window.durationMs`,
`window.id` already short as `5h` and `7d`, `amount.usedFraction`, a vendor
`status`, and `scope.tier`.

**Two halves to staleness, and the second is not a threshold.** Past 90 minutes —
the measured p90 gap plus slack — every row shows its age, because a limit figure
without one reads exactly like a fresh one. But when the reading is older than the
window it describes, that window is **over**: a new one opened while nobody was
looking, and its usage starts from a number this machine never saw. Such a row is
not old, it is about a different window, so it is dropped rather than shown with an
age attached.

That is the morning path, not an edge case. Measured here, gaps between readings
run to 14.5 hours overnight and 17 of the last 30 days had one longer than five
hours, so opening a laptop after any of them puts the 5h window in exactly that
state:

```text
▶ Claude (7d 56% · hace 14h)
▶ Codex (7d 91% · hace 14h)
```

The five-hour row is gone, not aged. Eight days away and neither section draws at
all.

**Amber is the vendor's word, not a threshold invented here.** Each window carries
its own `status`, and that — plus a reading old enough to show its age — is what
turns a section amber. Providers reporting in units with no whole to divide by are
not drawn as bars rather than drawn as invented ones: four others publish into the
same store, and none is on screen.

**A failure does not look like an answer.** Silence is the legitimate state here —
no live window, or a provider this machine does not use — and it is also what a
broken reading would produce, because the source is another product's private store
reached by a fixed path with no declared schema. Those were the same absence until
they were split: a report that is read and understood but has nothing live draws
nothing, while a store that will not open, or opens and says something this widget
cannot parse, draws one amber line — `Claude sin lectura` — exactly as the `Models`
section does. The line keeps reading, so when the store comes back it becomes the
section again on the next poll, with no restart. Measured both ways: a store with
one field renamed produced the line, and repairing it under the running client
produced the section.

A Go test guards the other end of that: it opens omp's real store and fails if an
`anthropic` or `openai-codex` report stops carrying `fetchedAt`, a percentage with
`usedFraction`, or a window with an `id` and a `durationMs`. It skips where there is
no omp — which is every CI runner — so it shouts only on a machine that has the
thing it checks, which is also the only machine where the widget could be wrong.

**Whether it draws is decided once, when the client loads plugins.** A slot
callback is invoked a single time — measured with a counter that stayed at 1 across
repaints — and neither a callback nor a component that returned `null` is ever
asked again. A provider with no live window at startup therefore stays absent until
the client is restarted; it takes a reading older than the longest window to land
there. That is also why nothing is faked to hold a place: `null` costs zero rows,
while an empty `<text>` costs a blank one and an empty `<box>` costs the separator.

### Any widget, same three verbs

The plugin source travels inside the `atenea` binary, so `status` can answer the
question that matters after an upgrade — is the file on disk the one this binary
ships?

```text
$ atenea statusline status session-share
widget    session-share
client    opencode
plugin    ~/.config/opencode/plugins-tui/session-share.tsx
installed yes
declared  yes
shipped   0807eddc0834
on disk   0807eddc0834
```

Different digests print the remedy, naming the widget so the command you are
handed repairs the line you were reading. `atenea statusline uninstall
session-share` takes both the file and the declaration away, and leaves the
other widget — and any other plugin in that config — untouched. A name this
binary does not carry is refused with the list of the ones it does, rather than
quietly resolving to the default: installing a traffic light when somebody asked
for a share is the failure that check exists for. Omitting the name still means
`atenea`, and the output says so.

A `tui.json` this command cannot parse cleanly — one carrying comments, say — is
refused rather than rewritten, with the line to add by hand.

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

Resuming a run whose receipt was torn refuses and names the `.torn` file rather
than reporting the `.json` that is no longer there: the file that exists is the
one worth reading, and the missing one leads nowhere.

A base held open by another Atenea is not damage and is never moved. That check
is the difference between recovering from corruption and manufacturing it.

The repair is the service's, and only the service's. A command cannot tell an
abandoned temporary file from one the service has open this instant, so it leaves
the directory alone — the `process` line on the status screen says which of the
two you are reading, and a `recovered` line absent from a command means nothing
was repaired *by that process*. A second `atenea run` is refused and names the
pid that already holds the upkeep; every other command runs beside it untouched.

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
capability      implementation  repository version                    tries   failed   priced       each      worst
code.search     ripgrep         current    omp/17.2.10                  113        9      104  966.738ms   3.25242s
code.search     claude.search   current    2.1.220 (Claude Code)          3        0        3 1m19.557393s 2m32.638458s
```

`version` is the version of the tool behind the implementation, and it is part
of the key rather than decoration: yesterday's numbers for yesterday's binary
are history, not a baseline. One implementation therefore appears once per
version it was measured at, and an attempt refused before the far side ever ran
shows a `-` there, because nobody could ask it what it was.

A provider that returned hits outside the scope it was asked for gets a line of
its own below the table. That number is recorded and never ranked on: a
provider honest enough to report its own overreach must not rank below one that
hides it.

The three counts sit together because the gap between them is the diagnosis.
**Only the priced ones are a price.** A failure is counted and never averaged
in: a provider that refuses instantly — not logged in, no index, no server —
would otherwise record a stream of very fast, very cheap calls and become the
cheapest thing on the machine, and the funnel would hand it everything while
every commission failed. Failing cheaply must not pay. `ripgrep` above failed
nine of its hundred and thirteen attempts, and not one of those nine moved the
966ms it is ranked on. A provider with attempts but nothing priced has no
measured cost at all, so it ranks on whatever estimate the settings file
declared for it, and the trace says so.

Failures decide health instead. Three in a row *in the same bin* is an outage:
the provider leaves the funnel and the trace names the count, the bin and what
the provider actually said. Three in a row in *different* bins is a provider in
trouble with no single cause, so it is marked degraded and ranks last rather
than being dropped — the funnel would rather use a flaky provider than none.

Four bins never reach that record: `not_found`, `permission_denied`,
`invalid_input` and `canceled` describe the request, not the provider, so a run
of them neither condemns one nor clears one. Before that exemption existed, a
sweep of thirty-four TypeScript files hit three generated ones absent from any
graph, each answered an honest `not_found`, and Atenea marked every
implementation of `symbol.overview` down for the whole repository — every real
file after those three failed, against a provider that was answering correctly.

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
atenea 0.10.0  contract 3.0.0  AMBER
funnel    constraints -> reach -> health -> cost (measured for 8 of 11 implementations, the rest on declared estimates)
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
lefthook install        # do this first: installs the pre-commit and pre-push hooks
go test -race ./...     # the suite
air                     # hot reload while developing, local only
```

**Run `lefthook install` after cloning.** Git never clones hooks -- they live in
`.git/hooks`, which no clone copies -- so a fresh checkout has none, and
unformatted code commits without complaint. Measured, not assumed: a clone of
this repository accepts a deliberately unformatted file at exit 0 until that
command has run. One command installs both hooks: pre-commit runs `gofmt`,
`go vet` and `golangci-lint`; pre-push runs the suite with `-race`.

The hooks are a convenience, not the guarantee. **The enforced gate is the
release workflow.** It re-runs the linter and the full suite at tag time and
refuses to publish if either fails, which makes it the only check that cannot
be skipped by forgetting a setup step. It has refused: `v0.6.0` is a tag with
no release behind it, because that gate failed while CI on the same commit was
green. The changelog explains what broke and why the tag was left where it
fell.

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
