---
title: Settings
weight: 4
---

# Settings

Atenea is a declarative engine. The catalog of capabilities, the
implementations behind them, the repositories they run against and the user's
selector rules all live in one TOML file. Changing behavior means editing that
file, not the core.

One file on purpose: ceilings, rhythms and catalog in a single place, so
nothing ends up baked into the code or scattered across three configs.

Unknown keys are refused. A typo that is silently ignored is a setting the user
believes is in force and is not.

## What the file replaces, and what it only overrides

Atenea ships a full settings file compiled into the binary, and that is what
runs when no file exists on disk. The moment a file does exist it is used
instead. What *instead* means depends on which part of the file you are looking
at, and the difference is the thing to know before you delete a line.

**The catalog is replaced.** The `[[capability]]`, `[[implementation]]`,
`[[repository]]` and `[[selector.rule]]` blocks are not merged with the shipped
ones: what your file lists is the whole list. `[[mcp_server]]` is the same kind
of list without being part of the catalog, and for it the rule costs nothing:
the shipped file declares none, so there is nothing there to lose. A settings
file containing only

```toml
contract = "5.0.0"

[orchestrator]
runners = ["omp", "claudecode"]
```

is a complete, valid file describing an Atenea that knows no capabilities at
all. It boots, `atenea status` shows the orchestrator red and `serves -`, and
every command answers `unknown capability`. Nothing is hidden and nothing
crashed; you asked for an empty catalog and got one.

**The knobs are overridden.** The ceilings and rhythms — `[core]`,
`[orchestrator]`, `[metrics]`, `[backup]`, and the adapter blocks under them —
are applied key by key on top of a compiled default. The file above sets no
step ceiling and no rhythm at all, yet that same `atenea status` prints
`parallel 4`, `metrics.flush 30s`, `metrics.compact 1h`, `backup 6h` and
`5 of 5 kept`. None of that is in the file; all of it is in force.

So a knob you leave out is not absent, it is whatever the binary falls back to.
Today those constants say exactly what the shipped file says — a test holds
them to it — which is what makes the difference easy to miss. It stops being
invisible on the upgrade that changes one.

**A list you leave out is not an empty list.** `runners`, `skip_dirs`,
`sensitive` and every adapter's `implementations` tell an omitted list apart
from an explicitly empty one, deliberately:

| Written | Means |
| --- | --- |
| nothing | the built-in list stands |
| `[]` | genuinely nothing: dispatch nowhere, skip no directory, treat no file as sensitive |

Strip `implementations` out of `[orchestrator.tokensave]` and its built-in list stands.
Write `implementations = []` to disable all of that adapter's implementations.

`effects` is the one list that does not play by this rule, because a grant
nobody wrote down is a grant nobody made. Leave it out and you have none, not
the shipped `["process"]` — and since every implementation of `code.search` is
a binary, a file that omits it answers `permission_denied: code.search causes
process, which the call does not cover`.

So the way to change one setting is still to start from the whole file:

```sh
atenea config init          # writes the built-in file, catalog and all
atenea config init --force  # overwrites one that is already there
atenea config path          # says where that is
```

then edit it. A bare `init` refuses when a file is already there and names
`--force`, so writing the defaults can never silently discard settings you had.
A full file states every value where you can read it, which is
the only version of this that survives an upgrade. Merging the catalog was
considered and refused: a half-file whose meaning depends on what a particular
binary happened to ship is a file nobody can read on its own. The knobs that do
fall back are precisely where that objection still bites, which is the argument
for pinning them rather than trusting them.

A full rewrite is still the only way to *have* everything a release shipped. It
is no longer the only way to find out you do not: `atenea status` compares your
catalog against the built-in one and prints a `catalog` line beside `settings`
naming the implementations it is missing. It stays quiet about capabilities you
dropped outright, because dropping one is a decision rather than drift. The
warning exists because the drift is otherwise invisible until it bites: a file
written before `0.6.0` kept registering one implementation of `symbol.overview`
after the binary began shipping two, so the funnel ran with a single candidate,
and the day that candidate died there was nothing behind it.

## Skeleton

```toml
contract = "5.0.0"          # required: the contract version this file targets

[core]
shutdown_grace = "10s"      # margin a clean stop gives in-flight work
health_probe_every = "15m"  # background MCP reachability probe; "0s" disables

[result_cache]
max_entries = 128             # bounded LRU entries for complete code.context
max_bytes = 4194304           # bounded serialized result bytes (4 MiB)
ttl = "5m"                    # positive freshness window; zero is refused
```

`[result_cache]` controls the in-memory, read-only `code.context` result
cache. Its limits are validated at startup; partial, truncated, failed or
sensitive results are never stored, and cache hits do not create provider
measurements.

`selector.quality_minimum_samples` defaults to `2`. Quality is collected only
from validated outcomes for the requested capability, repository language and
tool version. Declared, wired or merely connected providers remain candidates;
an unknown quality observation carries no penalty and an explicit preference
still wins.

The `contract` line is the one field with no default: a file must say which
core it was written for, and a core refuses a file from a different major
version by name rather than reading it and hoping. Minor lag is supported, so
a file targeting `5.0.0` remains readable by a later `5.x` core because every
minor addition is backward-compatible. A file from another major version is
refused. A 4.x file is told what contract 5.0.0 removed (see the
[5.0 migration guide]({{< relref "migration-5" >}})):

```text
settings ~/.config/atenea/atenea.toml: contract 4.1.0 is not supported by this
core (5.0.0): contract 5.0.0 removed the agents, the workflow engine, the decide
command and the knowledge store: delete the [model], [workflow],
[[workflow_profile]], [local_agents], [decision], [knowledge] and [[agent]]
tables if present, then change the contract line to "5.0.0" (see
docs/content/migration-5.md)
```

Do that and you are done. The refusal is deliberately not a fallback to the
defaults: a file quietly ignored is a machine running settings nobody chose.
A file from a *newer* core than the binary is told to upgrade the binary
instead, because no edit to the file can fix that one.

## Removed: the `[decision]` table

`[decision]` (`capture_dir`) went away with the decision router (#212). A settings
file that still has the table is refused at load, naming it: `[decision] was
removed in contract 5.0.0 with the decide command; delete the table`.

## The orchestrator

```toml
[orchestrator]
max_parallel = 4            # repositories one workspace.context asks at once; 0 lifts the ceiling
budget_usd = 0.90           # what ONE CALL may spend
effects = ["process"]       # granted standing to every call
client_effects = ["process"] # the same, for a chat a client opened; also its ceiling
client_denied_capabilities = ["desktop.move", "desktop.click"] # MCP kill switch
  runners = ["omp"]           # any of omp, claudecode, codex, tokensave, desktop, android, local; [] dispatches nowhere
checkpoints = true          # false is the only way to stop writing run receipts
checkpoint_dir = ""         # "" uses $XDG_STATE_HOME/atenea/runs

  [orchestrator.omp]
  binary = "omp"                       # bare name is looked up on PATH
  implementations = ["ripgrep"]        # what the adapter answers for
  match_limit = 10000                  # matches one search asks omp for
  timeout = "30s"                      # after this, omp is stuck, not slow

  [orchestrator.local]
  implementations = ["ripgrep"]        # what the stand-in can actually execute
  # never walked; the stand-in reads no .gitignore, so it is told instead
  skip_dirs = [".git", "node_modules", "vendor", "dist", "build", ".venv", "target"]

  [orchestrator.claudecode]
  source = "auto"                      # terminal or auto; Claude.app is a GUI client
  terminal_binary = "claude"            # Claude Code headless executable
  implementations = ["claude.search"]  # a different id from ripgrep's, on purpose
  # no ceiling here: what a call may spend arrives with the request
  timeout = "90s"                      # ~13 model turns; the grant bites first

  [orchestrator.codex]
  source = "auto"                      # terminal, app or auto
  terminal_binary = "codex"             # Codex CLI on PATH
  app_binary = "/Applications/ChatGPT.app/Contents/Resources/codex"
  implementations = ["codex.search"]
  timeout = "90s"

  [orchestrator.tokensave]
  root = "/srv/workspace"              # required, and ABSOLUTE: every path is relative to it
  implementations = ["tokensave.context", "tokensave.calls", "tokensave.overview"]
  timeout = "90s"                      # re-syncs its own index first: slower than omp, not stuck

    # Required: a stdio child has no address to dial.
    [orchestrator.tokensave.process]
    command = "tokensave"              # required once this table exists
    args = ["serve", "--path", "/srv/workspace"]
    lifecycle = "on_demand"            # required: "persistent" or "on_demand"
    instance = "shared"                # the only value here; per_repository is refused
    idle_timeout = "10m"               # on_demand only, same as every process table

```

`max_parallel` is the real brake on total memory: it caps how many repositories
one `workspace.context` call asks at once, so a laptop stays responsive. `0`
means the built-in ceiling, which is a choice for a build machine, not a
default. An `ask` dispatches a single step, so the ceiling never applies to it.

`runners` names the far sides of the contract, and it is a list because several
can be attached at once. `omp` is the client adapter that ships attached.
`claudecode` drives the Claude Code CLI and is off by default, because it is
the only far side that costs money per call. Graph providers speak MCP rather than a client CLI.

`local` and `omp` both answer for `ripgrep`, so attaching both is refused by the
rule below and the choice between them is exclusive. It is worth making
deliberately, because the stand-in is not a slower omp — it is a faster one that
answers a slightly different question. Spawning nothing makes it 28–65× quicker
on the repositories this was measured against, with identical results on all
three. But it reads no `.gitignore`, and against a probe repository built to
separate the two rules the answers diverged in both directions: the stand-in
skipped a tracked file under `build/` because the name is on its fixed list, and
returned two `.gitignore`'d files — one of them a private note — that omp did
not. Neither answer is wrong for the rule it applies. Only one of them applies
the rule the repository itself declares, which is why `omp` ships attached and
the speed is not the reason to switch.

Each runner answers for its own implementations, and two of them claiming the
same one is refused at load. Dispatch would still work — whoever was asked
first would answer — but which client ran your work is not a detail to settle
by declaration order.

`match_limit` cannot be `0`. Zero reads like "no limit" and is precisely the
value omp treats as "use a small default", after which it reports the short
answer as complete. Atenea states the number instead, and a search that reaches
it comes back with the ceiling named in `discovered` — a partial answer that
does not say so is a wrong answer.

`budget_usd` is what **one call** may spend, across every paid provider it
dispatches to. It lives under `[orchestrator]` and not on an adapter because
money is a permission, and permissions are granted per call: an adapter with a
ceiling of its own would be a second number to keep in step with this one.

Zero is not a typo and not "no limit": it is a call that may not spend, which is
exactly what a machine with no paid provider attached wants. Free providers
keep working. A negative number *is* a typo and is refused at load, because
clamping it would silently switch off every paid provider and the refusals that
followed would read as an outage.

What the step was actually held to, and what it actually charged, are both on
the receipt -- `charged` on the summary, and per step under `--trace`.

One order beats the standing grant, in both directions: `atenea ask ...
--budget 3` funds that call and nothing else.

Running out is reported as `permission_denied`, not as slowness: the far side
was not slow, the grant was spent. It does not mark the provider down, so the
next step can still go to it — and a step that costs nothing still runs.

`effects` is granted the same way, standing to every call this core
dispatches, on top of the read that is always free. It exists
because not every effect is a per-request choice: `code.search`'s only
implementations today are both a binary, so requiring every caller to grant
`process` by hand on every single call would just move the same yes to one
place instead of saying it once, here. The shipped default turns it on for
exactly that reason — refusing it by default would not make the spawn
auditable, it would make the one P0 capability unusable out of the box.

One order beats the standing grant here too: `atenea ask ... --allow write`
grants one more effect to that one call, repeatable for several. `--allow` only
ever adds -- an effect already held is never worth losing by accident.

`client_effects` is that same grant for a chat opened by a connected MCP
client, rather than by the person at this terminal. Until it existed there was
one line for both: widening `effects` to `write` for an afternoon's work
widened every client that connected, permanently and silently, and this file
had no way to say otherwise. That is the gap it closes.

It ships equal to `effects` and separate from it, and separate is the part
that matters — after the day you widen your own line, the two are different
numbers. Deleting the key does not turn the rule off: it hands clients
whatever `effects` says, which is what every settings file written before the
key existed already does, so nothing changes by upgrading. `atenea status`
prints both on its `standing` and `clients` lines, and marks the second
`inherited` when it is a copy, because two identical lists with nothing
between them look like two decisions when only one was made.

An empty list is a real answer and not an omission — that is why this key is a
list that may be empty while `effects` is one that may not. `client_effects =
[]` means a connected client may read and nothing else, which on this machine
refuses `code.search`: every implementation of it is a binary and so needs
`process`. That is why it is not the default. A default that refuses the
headline capability on day one does not teach caution, it teaches people to
turn the default off.


The provider behind it, `[orchestrator.desktop]`, is also the one whose helper
is **built on the machine that runs it** and ships in no release. Distributing
a macOS binary that drives the screen needs a Developer ID signature and
notarization; measured on macOS 26.6, an Apple Development signature is
rejected by Gatekeeper on another machine exactly as an unsigned one is, so the
weaker options do not substitute. Code compiled locally carries no quarantine
attribute, so Gatekeeper never enters and no certificate is needed by anybody:

    swift build -c release --package-path helper

A certificate would buy one thing and it is not the right to run: that the
permission survives a rebuild. See `helper/README.md` for the measurements.

`[orchestrator.android]` is the direct Android provider. `adb_binary` and
`scrcpy_binary` select the two executables without accepting command fragments
or arbitrary arguments; bare names are resolved through `PATH`. `timeout`
bounds each ADB/UIAutomator call. `frame_ttl` bounds the one-shot token returned
by `android.screenshot`; coordinate actions recapture the device and refuse
the token when the pixels changed, while explicit semantic selector actions
validate the window, orientation and current UI target instead. Naming
`android` in `runners` enables the provider, but authorizes no device by itself.
`helper_mode` is a closed choice: `auto` uses the optional helper only when its
installed manifest has the supported schema and protocol, `adb` never probes
the helper, and `helper` refuses the call when the helper is absent or
incompatible. The shipped setting is `auto`; direct Go embedders that omit the
option retain the historical ADB-only behavior.

`device` is the one effect that argues the other way, and it is on neither
floor as shipped. It marks a capability that reaches the pointer, the keyboard
or the screen, and the permission behind it is not Atenea's to spend: measured
on macOS 26.6, the system attributes its device grant to the responsible
ancestor rather than to the process asking, so an executable whose own
identifier was never authorized reports full access merely for having been
launched from a terminal that has it. Granting `device` on `client_effects`
would therefore hand a connected client something nobody granted Atenea. It
belongs on `effects` alone, typed deliberately, and only where Atenea is that
responsible ancestor.

A chat opened by a client holds this list. That sentence is younger than the
key: in the historical `0.10.0` behavior a chat opened holding nothing at all, so
`client_effects` was a ceiling with no way to reach it, and a raw tool
declaring anything beyond `read` was refused on every machine whatever this
file said. Deleting the key still hands clients whatever `effects` says — the
two readings agree on a file that never wrote the line, which is why upgrading
changes nothing for anyone who never wrote it.

It is also a ceiling, and now that a chat can reach it, the ceiling is a rule
rather than a statement. A client may ask at `initialize` to hold *less* than
this list:

```json
{"capabilities": {"experimental": {"atenea": {"grant": ["read"]}}}}
```

A client that needs `write` for one call in fifty can spend the other
forty-nine unable to make a mistake. Asking for *more* is refused at the door
rather than at the first `tools/call` — a client told at the door can say so,
one told mid-conversation has already promised somebody the work. So is an
effect name nobody recognizes: a client that misspells `write` asked to be
restrained, and handing it the full grant would be the opposite of what it
asked for.

The handshake answers with what the chat actually ended up holding, in the
same block. Before that the only way for a client to learn its own permissions
was to make a call and read the refusal — and a client that has to provoke an
error to find out what it may do will provoke it on the user's work.

A capability declares the effects running it has, and a step is refused when
its permission does not carry one of them — `code.search causes process, which
the call does not cover`, as a verdict on the step rather than a
transport error. Both grants rest on that one check, which sits in the core
wrapping whichever runner is attached rather than in the adapters.

An implementation no attached runner can execute is not removed from the
catalog. It is dropped by the funnel's `reach` stage, which says so in the
trace — `no attached runner serves it`, not `down`, because a provider nobody
wired up is not a provider that is broken. The status screen lists them up
front, under `no runner`.

`checkpoints` is what turns run receipts off, and the only thing that does.
`checkpoint_dir` follows the ordinary override rule, where an empty string is
indistinguishable from a key nobody wrote and so inherits the default rather
than blanking it: `checkpoint_dir = ""` keeps writing, to
`$XDG_STATE_HOME/atenea/runs`. Only `checkpoints = false` stops it, and it
wins over an explicit path written beside it. `atenea status` reports which
way it went on its `runs` line — a directory, or `off`.

### Managed processes

A managed process requires `command` and `lifecycle`. `persistent` starts at
service startup; `on_demand` starts on first use and stops after `idle_timeout`.
`restart_limit` defaults to 2 retries; 0 disables retries. `restart_delay`,
`stable_after`, `ready_timeout` and `stop_grace` bound recovery and shutdown.
`env` adds variables to the inherited environment. For HTTP processes, `port = 0`
allocates a free port and substitutes `{{port}}` in `args`.
Tokensave serves one root and does not accept `instance = "per_repository"`. MCP stdio declarations support `shared` and `per_chat`.

### The far side that serves one root

`tokensave` is a stdio server rather than an HTTP one, so there is no
`endpoint` for it to fall back on and `[orchestrator.tokensave.process]` is
required. Naming `tokensave` in `runners` without
one is refused at startup — `tokensave has no process to launch -- a stdio
server has no address to dial without one` — because there is nothing else in
the block that could say where the far side is.

`root` is the key no other adapter has, and it is required for the same
reason it exists. tokensave serves **one project rooted at a directory** and
speaks paths relative to that root, so every path crossing this adapter is
translated in both directions and the translation has to know where the root
begins. Leave it out and the runner is refused by name:

```text
settings ~/.config/atenea/atenea.toml: orchestrator.tokensave.root is
required -- every path tokensave reports is relative to it
```

It has to be absolute, and a relative one is refused rather than resolved:
"resolved against what" has no honest answer here, because the process that
loads this file is not the one that reads the path six months later from a
systemd unit with a working directory of its own. It is declared rather than
derived from the `[[repository]]` list for the matching reason — a root
guessed from that list is silently wrong the day a repository is added
outside it.

Every repository this provider answers for lives under that root, and the
graph does not stop at a repository boundary: a symbol in one repository can
have callers in another. Those rows have nowhere to travel, since
`symbol.calls` declares repository-relative paths and no field for "another
repository", so they are dropped and the drop comes back as a discovery.
Renaming a foreign path into a repository-relative one would be a lie the
caller could not detect.

The three implementations are what the far side can answer honestly:
`tokensave.context` for `code.context`, `tokensave.calls` for `symbol.calls`
and `tokensave.overview` for `symbol.overview`. Two things are deliberately
absent. No `code.search`, because tokensave has no text engine and a flat
search answered out of a symbol index is a different answer wearing the same
name. And no indexing implementation at all: this far side re-syncs its index
on every call, so there is nothing for the funnel to trigger.

That re-sync is also what `timeout` is sized for. `90s` is generous rather than
tight because an index brought back into step before the answer is slow long
before it is stuck.

`instance` may only be `shared`, and `per_repository` is refused rather than
accepted: a second copy pointed at the same root would index the same files
twice into the same database. The whole graph belongs to the root, not to a
repository, which is why one process is the honest number.

## The measurement base

```toml
[metrics]
enabled = true              # off leaves a working core that never learns
path = ""                   # "" -> $XDG_STATE_HOME/atenea/metrics.duckdb
flush = "30s"               # how often the batch reaches disk
compact = "1h"              # how often the retention ladder is walked
buffer_limit = 10000        # measurements held in memory when flushes fail
```

What every attempt cost — time, tokens, memory — filed under the capability
asked for and the implementation that answered. It is the fuel the funnel ranks
on; until it has real figures the funnel uses the estimates written further
down the file, which is guesswork wearing a decimal point.

`enabled = false` is a bigger switch than it looks. Nothing is written, so
nothing is ever read back: the funnel keeps ranking on those estimates forever,
and break-in mode is off with it — there is no point handing a provider a turn
to earn numbers nobody will record. A core with the base switched off works
exactly as well on its first day and never gets better.

With the base on, the turn it hands out is credit rather than a standing
entitlement, because only a call that *works* leaves a measurement. A provider
that cannot answer here would otherwise sit at zero samples forever and be
handed every dispatch on the strength of it. Four attempts with nothing to show
for any of them and it ranks on its declared estimate instead — ranked lower,
never dropped, so it can still earn its first number the day it starts working.
`atenea select` shows the gap for free: `7 attempts here, none of them
successful`.

It also costs the health screen half its evidence. A provider is marked alive
by having worked recently and marked down by a run of failures that are its own
fault — a clean `not_found` or a spent budget is a fact about the request and
never counts — and both facts come out of this base — so with it off, every
provider that nothing has probed
in *this* process reads `health=unknown` forever. `atenea status` says which of
the two it is showing, on the funnel line: `measuring is off: ranking on
declared estimates for good`, rather than the `nothing measured yet` of a base
that is simply empty. The word `yet` is a promise, and it should not be made on
behalf of a base that is never coming.

`path = ""` is not "nowhere", it is the default location, beside the run
receipts and under the same state root: both are the same kind of thing, what
Atenea remembers about work it has done.

Neither rhythm can be set to zero. A beat of zero reads like "never", and a
maintenance task that silently stops happening is worse than one that is
switched off in a place you can see. Off is spelled `enabled = false`.

`buffer_limit` is the ceiling on how many measurements wait in memory while
flushes are failing. Past it the oldest are dropped and counted, because the
only thing worse than losing a measurement is not knowing that you did — the
count lands in the crash notebook, where `atenea incidents` will show it.

## What protects the history

```toml
[backup]
enabled = true              # off means no copies at all
dir = ""                    # "" -> a folder BESIDE the state root
every = "6h"                # how often a copy is taken
keep = 5                    # how many survive; the sixth arrives, the oldest leaves
```

The measurement base, the run receipts and the crash notebook are everything
Atenea has learned. A disk that loses them loses the reason the funnel picks
anybody, and unlike a provider going down there is no bin for it and no
recovering afterwards.

`dir = ""` is a folder of its own beside the state root, never inside it: a
copy under the tree it copies recurses into itself, and dies with the tree it
exists to survive. A path inside the state root is refused rather than
corrected. Point this at another disk and it stops being a copy and starts
being a backup.

Copies are hard-linked against the one before, so a snapshot of an unchanged
base costs a directory entry rather than a database. What changed is copied,
and the older snapshot keeps the older bytes — dropping the oldest can never
take a file another copy still needs.

`keep = 0` is refused. It reads as "copy, then delete the copy", and the way to
say what it looks like it means is `enabled = false`. Every rhythm in this file
refuses zero for the same reason.

`every` is a floor, not an alarm clock. Whether a copy is due is read from the
newest one on disk rather than from an in-memory timer, so a machine restarted
more often than the rhythm still copies: without that, a six-hour rhythm on a
laptop shut every evening would never take a single one.

## The crash notebook

It has none, deliberately. A notebook you have to switch on before it works is
one that is off on the day you need it, so there is no `[notebook]` table and
no way to move it: it is always on, at `$XDG_STATE_HOME/atenea/incidents.jsonl`,
beside the receipts and the base.

Nothing about it is tunable because nothing about it should be traded away. It
writes one line per fault and flushes before returning — a rhythm setting would
only ever be used to make it lose the entry it exists to keep.

## The desktop allow-list

```toml
[desktop]
applications = []           # bundle identifiers that may be looked at; EMPTY DENIES ALL
action_applications = []    # bundle identifiers that may be acted on; absent inherits applications
denied = ["com.apple.keychainaccess", "com.1password.1password"]  # always wins
look_then_act = false       # may a chat act on the screen it just read?
visual_feedback = true      # show the ephemeral Atenea overlay and miniature
```

`applications` is the one list in this file where empty means *nothing* rather
than *everything*, and the inversion is deliberate. `desktop.inspect` and
`desktop.screenshot` can read any window on the machine, and a capability like
that must not be switched on by a settings file that forgot to mention it. Find
the identifiers with `desktop.apps`, which needs no entry here because it
returns names and identifiers and nothing about what any window contains.

`action_applications` independently governs `desktop.open`, `desktop.click`, `desktop.move`,
`desktop.drag`, `desktop.scroll`, `desktop.type`, and `desktop.key`. If the key
is absent it inherits `applications`, preserving older configuration files. If
it is present as `[]`, every mutation is denied while observation can remain
enabled. For an iOS Simulator-only action surface use
`action_applications = ["com.apple.iphonesimulator"]`; Finder and TextEdit can
remain in `applications` without becoming actionable.

`denied` always wins, and it is seeded rather than empty. Two lists rather than
one, because a single list would make "never look at my password manager" a
thing you state by omission — and omission is what happens when somebody adds
an entry in a hurry. The shipped refusals are the applications where one
screenshot is a credential. Deleting the block restores them; writing an
explicitly empty list is a statement and is honored.

The single entry `"*"` means every application `denied` does not name. It has to
be typed, and that is the point: the rule above is that omission is not a
statement, so the widest allow-list this file can express must not be reachable
by leaving something blank. Writing it beside named identifiers is refused at
load — "everything, and also these two" is two sentences that disagree about
which is in force. `denied` outranks it, which is what keeps it survivable: even
`"*"` cannot reach a password manager.

Bundle identifiers rather than display names throughout: a name is localized,
changes under the reader's feet, and two applications may share one.

`look_then_act` decides whether one chat may act on the screen it has already
read, and `false` is the security control rather than a cautious guess about
one. `desktop.inspect` and `desktop.screenshot` return whatever a window chose
to display, written by whoever controls it. With this off, a chat handed that
content may no longer move the pointer or type — so a sentence inside somebody
else's email cannot reach this machine's input. Acting is still available:
`atenea desktop` shows what will happen and waits for a person to agree.

Turning it on is the only way to get the continuous loop — look, click, look
again — which is what driving a desktop actually requires. What it costs, said
plainly: Atenea runs no classifier over what it captured, so with this on there
is nothing left between a window's text and the pointer. What still stands is
`denied`, the hard refusal to type into a secure field, credential redaction in
`desktop.type`, and the receipts. `atenea status` prints a line whenever it is
on, because a control that is off gets remembered and one that is on gets
forgotten.

`visual_feedback` is enabled by default on macOS 14 and later. It shows a
click-through Atenea border, a virtual cursor and a movable live miniature while
Computer Use is active. Input uses a private synthetic state table and events
bound to the captured window and directed to its process; accessible buttons
may use AXPress. Input actions never post to the HID/session stream, warp or
restore the human pointer, borrow human modifiers, activate or raise the target.
Only an explicit `desktop.open` requests foreground activation. The miniature
is blurred after a short idle period, closes after 30 seconds and never writes
frames, video or an interaction history to disk. Set it to `false` to hide the
visuals while retaining frame validation, cross-window safety checks and the
mandatory passive human-input monitor.
Input requires a logged-in console session with an available foreground app;
it is refused when loginwindow owns the foreground. A disabled monitor requires
checking permissions and explicitly restarting the helper; it is not replaced
automatically within the process.

Human input directed to the controlled application cancels the current action
and the preview shows `Paused` with a `Resume` button. The passive annotated
event tap uses the recipient PID, so typing, scrolling or clicking in another
application does not pause CUA; physical pointer travel is also free. A direct
interaction whose recipient cannot be established is refused conservatively.
This is application isolation, not simultaneous writers within one application.
The miniature's `Pause` button also blocks future actions while idle, until a
physical `Resume`. Resume never retries an interrupted action. All six actions require
the opaque `frame_id` returned by `desktop.screenshot`; no action implicitly
reuses the latest frame. Expired frames and any window move, resize, display,
scale, rotation, or topology change are refused until a fresh screenshot is
taken. An action result says only `action_sent`; a later observation must
verify the UI result. The `input` receipt identifies the effective backend,
private source, resolved PID, event count, sampled pointer/focus and measured
elapsed time, plus the last dispatched logical CUA pointer position when known.
The latter does not prove that an app accepted hover. Before/after equality does
not prove absence of transients, and those samples may legitimately differ while
the human works. Concurrent acceptance observes marked input at the global event
tap and at a decoy app, target effects and any target activation separately.

An explicit `[[capability]]` catalog is not merged with new defaults. When
upgrading private input, copy the `input` output record and its nested fields
from the matching six `desktop.click/move/drag/scroll/type/key` declarations in
`internal/config/default.toml` before restarting the service. Keep local app
allow-lists and grants unchanged. A legacy output without that record is
refused before contacting the helper; otherwise output validation could reject
an acknowledgement after the action already happened. Confirm the new record
in all six MCP `tools/list` output schemas before functional acceptance.
The `desktop.inspect` node output also needs the optional boolean `redacted`,
which identifies values omitted by privacy checks. Copy that field from the
matching default declaration when upgrading an explicit catalog.

Keyboard actions query the target application, not global focus. Unknown or
secure focused elements, unknown text-field subroles and ambiguous target windows
are refused. A moved window still receives an emergency release at the last
dispatched position, without moving to the intended destination; cancellation
does not undo partial effects.
For `Cmd+A` in an accessible text field, Atenea sets the selected-text range
using its character count, without reading the text or activating the app.
The receipt identifies this semantic operation as `ax_selection`. Other contexts
and unsupported selections use private PID key events, whose effect still
requires observation. A failed AX selection is never followed by a key replay.

AppKit's ordinary `AXTextArea` explicitly reports that it does not support a
subrole; that known response is accepted, while query failures and unknown roles
are refused. Secure roles/subroles are checked along the complete ancestry up
to the captured window. This relies on the application's accessibility model;
it cannot recognize an application that mislabels its own password controls.

Some apps ignore background gestures or activate themselves in response to
input. PID delivery alone is not evidence of UI success or universal
compatibility. Observe the requested effect and foreground state after each
action; never retry automatically or fall back to the human input stream.
Use role filters when inspecting a window. Default inspection omits the menu
bar, and secure elements and their descendants omit attribute values.

Neither list is a permission on its own. The capabilities behind them cause the
`device` effect, which no floor grants by default, and the adapter refuses them
outright unless Atenea is the process macOS attributes the permission to — see
the effect's own section above.

## The Android device allow-list

```toml
[android]
allowed_serials = ["emulator-5554"]

[orchestrator.android]
adb_binary = "adb"
scrcpy_binary = "scrcpy"
helper_mode = "auto" # auto, adb, or helper
timeout = "15s"
frame_ttl = "30s"
```

`allowed_serials` contains exact values from `adb devices -l`. Empty denies
every emulator and physical device, and `"*"` is refused: connecting a new
phone must never authorize it implicitly. `android.devices` may list a device
and report `allowed = false`, but screenshot, inspection, input and mirroring
all refuse it until the serial is written explicitly.

`android.screenshot` returns coordinates in the Android device's own pixel
space. Its `frame_id` is short-lived and one-shot. Before `android.tap`,
`android.swipe`, `android.type` or `android.key`, the adapter takes another
capture and refuses a changed screen for coordinate-only actions. This is
intentionally strict: an animation can require a fresh screenshot, but an old
coordinate can otherwise press a different control without warning.

For Android variants whose system bars or compositor change pixels between
otherwise equivalent frames, request `android.screenshot` with `semantic =
true`, then include a `selector` in the action. A selector contains one or more
exact `resource_id`, `text`, and `content_desc` fields. Atenea checks the same
focused window, dimensions and UIAutomator orientation, resolves the selector
again, and refuses zero or multiple visible enabled matches. `android.tap` then
uses that current control's bounds and must not also receive `x` or `y`. This
does not make UI text trusted: it prevents stale coordinates, while the caller
still treats the selected UI content as untrusted.

Frame identifiers are random and tied to the current observed device
connection generation, so identical pixels from separate captures or devices
cannot share an action token. Actions for one serial are serialized; separate
allowed devices can still operate independently.

By default, MCP receives the screenshot as one native image block and keeps
Base64 out of the JSON text and structured result. Set
`legacy_base64 = true` in an `android.screenshot` call only for a client that
requires the old field. `resolution = "adaptive"` delivers a PNG with its
longest side at most 1280 pixels and reports both delivered dimensions and the
native device dimensions; coordinate actions always use the latter. Native
resolution remains the default because local PNG scaling may cost more than it
saves for a local client.

`android.inspect` reads a bounded UIAutomator hierarchy and marks it untrusted,
because an application controls its text. `android.mirror` starts scrcpy with
fixed arguments for the selected serial; `android.unmirror` can stop only the
process that the same adapter started. scrcpy is the live operator view. ADB is
the action path, so window size, scaling and occlusion do not move a tap.

`android.inspect` may filter that hierarchy with `text_contains`, exact
`resource_id`, a device-space `region`, or `visible_only = true`. Omitting all
of them preserves the complete bounded tree.

`android.diagnose` refreshes the helper probe for one explicitly allowed
serial. It reports the configured mode, selected backend, helper package,
version, protocol and capabilities, plus `task_result = "not_run"` and
`verified = false`. Screenshot and inspection results carry the same cached
selection alongside `observation_backend = "adb"`: negotiation does not
mislabel ADB pixels or UIAutomator XML as helper-produced, and it is not a
verified task outcome. Reconnection invalidates the cached selection.

Typing is deliberately limited to bounded safe ASCII. Unicode, arbitrary shell
text and credentials need a separately reviewed input helper rather than being
smuggled through `adb shell input text`.

## Reaching the desktop from a client

Three things have to line up, and they are separate on purpose.

**Atenea has to be the service.** macOS attributes a device permission to the
process responsible for itself, which is the one launchd started. Run from a
shell, Atenea borrows the terminal's screen and input access instead, and the
adapter refuses rather than spending a permission nobody granted it and which
Atenea's own settings could not switch off. `atenea service install`, then grant
Accessibility — and Screen Recording, for captures — to `atenea` itself in
System Settings.

Use `scripts/install-dev.sh` if you build your own: it builds, signs and
installs both binaries and restarts the service, which is the whole of what has
to happen in the right order. `atenea status` says `(ad-hoc: grant dies on next
build)` beside the desktop surface when it did not.

Sign the binary first if you build your own. An unsigned binary's TCC grant is
pinned to a hash that changes on every build, so it dies the next time you
compile; signed with any certificate, the grant follows the identifier instead.
An entry can look enabled in System Settings while pointing at an identity that
no longer exists — remove it and add it again if a permission you granted stops
being seen.

**The floor has to allow it.** `device` is on neither floor as shipped. A client
that only reads wants it on `client_effects`; the acting capabilities also cause
`write` and `external`, and granting those reaches every client, not only the
one you had in mind.

**The application has to be on the list.** See `[desktop]` above. Empty denies
everything.

`atenea desktop ACTION` adds a confirmation to all of this: it shows what is
about to happen, waits for a yes, and then dispatches through the service, which
is the only process that can perform it. Note that it dispatches over the same
socket a client uses, so it is governed by `client_effects` too — the
confirmation is a control on top of the floor, never instead of it.

## Payloads a shell cannot express

`atenea ask --set NAME=VAL` cannot express a `record_list` input and refuses
rather than half-parsing JSON. `--payload FILE` takes the whole payload as JSON
instead, which is the same document an MCP client would have sent:

```sh
atenea ask workspace.context --repo api --repo web --payload context.json --json
```

The two are mutually exclusive: one is the whole payload and the other is a
field of it, and merging them would mean a rule about which wins per field.

## Security

```toml
[security]
sensitive = [".env", "*.pem", "*.key", "id_rsa", "*credentials*.json"]
```

A single list of paths and patterns governs what is never read, matched against
both the file name and its repository-relative path. Sensitive files are
skipped in silence: a search that reported "1 match in .env" would leak the
very thing the list exists to protect.

There is one exception, and it is the opposite of silence. A symbol lookup
points at one exact position, so the adapter has to open that file to read the
word under the cursor. Skipping a search hit costs the caller nothing; telling
someone who named a line that there is nothing there would be a lie. That is
refused out loud, as `permission_denied`.

Declaring it in one place is the point. The moment the answer to "is this
sensitive?" lives in several files, it starts differing between them.

## Capabilities

```toml
[[capability]]
id = "code.search"          # dotted lowercase
version = "1.0.0"
summary = "Find literal text in a repository."
semantics = "Flat text search. Options are stated as intent, never as an order."
effects = ["read", "process"]  # read | write | external | process | device

  [[capability.input]]
  name = "query"            # lowercase snake_case
  type = "string"
  required = true
  summary = "The text to look for."

  [[capability.input]]
  name = "direction"
  type = "string"
  required = true
  enum = ["incoming", "outgoing", "both"]   # optional: closes the set
  summary = "Which way to walk."

  [[capability.output]]
  name = "matches"
  type = "record_list"
  required = true

    [[capability.output.field]]
    name = "path"
    type = "string"
    required = true
```

Field types: `string`, `string_list`, `int`, `bool`, `record`, `record_list`.
The set is small on purpose — a contract has to be checkable, not expressive.
`record` and `record_list` take nested `field` entries; the others must not.

`enum` closes a `string` or `string_list` to a fixed set of values. Omit it and
the field stays open; declare it and anything else is refused, with the list
named in the refusal. On a `string_list` it constrains each element — which
words may appear, never how many.

It is there for the caller that cannot be asked. A summary saying *"incoming",
"outgoing" or "both"* is enough for a person; a machine filling the field from
a generated schema has to be told, or it finds the edge by being refused. Which
is also why it is opt-in: `kind` in `symbol.overview` deliberately has no enum,
because a provider names symbol kinds in its own vocabulary and closing that
set would refuse honest answers.

Numeric bounds are not part of a field. A range in the contract is a range
every implementation must honour, and a line number is bounded by the file, not
by the capability — `max_input` under an implementation is where a *particular*
tool says what it can be asked.

### What a call is about, beyond the repository

```toml
[[capability]]
id = "example.fetch"
subject_from = "url"        # which input says what this call is about
subject_kind = "url_host"   # how to read it
```

Both keys or neither, and most capabilities declare neither. Health and cost
are recorded per repository, which is the only dimension a code capability
has — and a capability that ignores the repository entirely has nowhere to hang
them. A capability that reaches the open web and never looks at a checkout used to put
every site in one bucket.

What that cost was measured, not imagined: one page behind Cloudflare marked
the cheapest implementation of a page-fetch capability unhealthy, and the next fetch of an
unrelated site skipped that implementation too — with a drop reason still
quoting the first site's url. One protected page paid for a browser on every
page after it, for as long as `health_stale_after` stood.

**It scopes health and deliberately not cost.** Whether a site refuses a
provider is a fact about that site. What a fetch *costs* is a fact about the
machinery — the far side and its browser dominate, not the host — so merging
subjects there gives a larger sample of one thing rather than mixing two.

`subject_kind` is a kind rather than a free string because the subject is a
grouping key: two calls meaning the same place must produce the same key, and
whatever the caller happened to type does not. `url_host` reads the input as a
URL and takes its host, lowercased and without a port, so `https://Example.COM/a`
and `http://example.com/b` are one subject.

The whole declaration is checked when settings load — an input that is not
declared, an input that is not a string, one key without the other. A subject
key that means nothing does not fail at call time; it files measurements under
nonsense and lets the funnel rank as if that were fine, so it is refused at the
door instead.

`atenea select` names no subject and says so in its notices: it asks who
*would* answer a capability, which is a question about the capability rather
than about a call, and the health it shows is what was recorded for no subject
in particular.

## Implementations

```toml
[[implementation]]
id = "fixture.search"
provider = "fixture"         # illustrative provider, not a shipped adapter
capability = "code.search"
scope_guarantee = ""        # "", filtered, confined -- see below

  [implementation.constraints]
  languages = ["go", "typescript"]   # empty means language-agnostic
  requires_vcs = false               # needs the repository under version control (e.g. a git diff against a baseline)
  min_scale = ""                     # "", small, medium, large
  max_scale = ""
  max_input = { depth = 0 }          # bounds what this one may be ASKED, not where it may run

  [implementation.cost]
  estimated_duration = "600ms"
  estimated_tokens = 900
  tool_version = ""                  # the version these estimates belong to

  [implementation.health]
  state = "unknown"                  # unknown | alive | degraded | down
  score = 0.0                        # 0..1, breaks ties inside one state
  reason = ""
```

`scope_guarantee` declares how strongly this implementation keeps a call inside
the `scope` it was asked to search. Two mechanisms answer honestly and a caller
deserves to know which one it got:

| Value | Means |
| --- | --- |
| `confined` | The provider physically cannot see outside scope. `ripgrep` ships this: `targets()` refuses any path that leaves the requested scope before the search ever runs. |
| `filtered` | The provider may read anywhere, but every returned match is checked afterwards and anything outside is dropped and reported through a `Notice`. `claude.search` ships this: a model is asked nicely, then verified. |
| `""` | Nobody has declared anything. Read as the weakest claim, never as `confined` — the same convention `min_scale` and `vcs` use for a fact nobody has stated. |

It is disclosure, not selection: the funnel never ranks on it and it never
disqualifies a candidate. `atenea catalog` prints it per implementation so the
promise is queryable rather than assumed.

`max_input` is the one constraint that reads the **request** instead of the
repository. Every other entry in the block answers "can this provider work
*here*"; this one answers "can this provider be asked *this*". It bounds a
declared integer input by name, inclusive, and it binds only when the call
actually names that input — an omitted value is the capability's own default,
which every implementation must honour, so nothing is dropped for it.

The graph provider ships `{ depth = 0 }`: its graph holds what a file
declares at its own top level and nothing nested inside those declarations, so
a deeper ask would return the same list and read as a complete answer.
At `depth = 0` both providers are candidates and the funnel ranks them; at
`depth = 1` it is dropped in the constraints stage and the fixture provider, which descends
properly, is the one left. The drop names both numbers.

A bound on an input the capability does not declare, or declares as anything
other than `int`, is refused when the settings file loads. The alternative is
silent: the funnel would read a name no request carries and the narrowing you
asked for would simply not exist.

Two things are **not** declarable, deliberately:

- **Measurements.** They are earned by running. A hand-written measurement would
  poison the very baseline the selector is meant to learn from.
- **Live health.** The `state` here is a starting point and an operator
  override; once the health checker exists, live probes own that value.

`unknown` health is not the same as `down`. An unprobed provider is still a
candidate, just a less trustworthy one, and it ranks below anything alive.

## Repositories

```toml
[[repository]]
id = "api"
path = "/srv/api"
languages = ["go"]
scale = "small"             # "", small, medium, large
vcs = "present"             # "", present, absent -- whether the root sits under version control
```

An unclassified `scale` or an unspecified `vcs` never disqualifies anyone: an
unknown fact is not a proven mismatch, and dropping candidates over it would
silently empty the funnel.

`path` has to be a directory that is really there. Every adapter makes it the
working directory of the tool it launches, and a missing one used to come back
as `omp could not be started: fork/exec /home/me/.local/bin/omp: no such file
or directory` -- Go names the binary when it cannot enter `cmd.Dir`, so the one
thing that was fine got the blame. A step against a repository that is not on
the disk is now refused before anything is spawned, named at the directory, and
filed as `invalid_input`: a bin the measurement base ignores, so a typo here
cannot mark a working provider down for every other repository on the machine.

The example above is a repository somebody classified. The shipped file is not:
it leaves `scale` empty, because a fresh install has measured nothing and a
guess is not free. Writing `small` there drops every implementation that asks
for a medium repository or bigger, which then looks unimplemented rather than
unclassified. Set it once you know, and the implementation comes back.

`indexed_by` and the `requires_index` constraint are gone with the Kivgraph
provider, the only one that kept an index to ask about: a settings file that
still has either is refused at load with the edit that fixes it (see the
[5.0 migration guide]({{< relref "migration-5" >}})).

## Removed: the `[model]`, `[[agent]]` and `[local_agents]` tables

The built-in agents (`atenea agent`, `atenea agents`, `agent-exec`, the `floor`
measurements and `atenea traces`) went away in #212, and with them the settings
that only they read:

| removed | what it was |
| --- | --- |
| `[model]` | the seam the model-backed agents called a model through |
| `[[agent]]`, `[[agent.result]]` | the eleven declared agent types |
| `[local_agents]` | the ceiling on agent types a repository declared |
| `[[agent]]` in `.atenea/config.toml` | agent types declared by a repository |

A settings file that still has any of them is refused at load, naming the table
(for example `[[agent]] was removed in contract 5.0.0 with the built-in agents;
delete every block`). Delete them from the
global file and from any repository `.atenea/config.toml`; nothing else in the
file changes. The `[orchestrator]` table and the capability catalog are
untouched, so `atenea ask` and the MCP tools work as before.

`traces.db` and `floors.json` stay in the state directory untouched; nothing
reads or writes them any more and nothing deletes them. They can be removed by
hand. The retention pass now keeps its mark in `retention.mark` beside the run
receipts, and the backup settles only the measurement base before it copies.

## Removed: the `[workflow]`, `[[workflow_profile]]` and `[knowledge]` tables

The workflow engine (`atenea workflow`, `[workflow]`, `[[workflow_profile]]`,
the recovery pilot and the knowledge store with `[knowledge]` and
`knowledge.context`) went away in #212. A settings file that still has any of
the three tables is refused at load, naming the table (for example `[workflow]
was removed in contract 5.0.0 with the workflow engine; delete the table`).
Delete the tables.
`knowledge.sqlite` and the workflow tables inside `traces.db` stay on disk
untouched; nothing reads them any more. The same holds for `traces.db` as a
whole and for `floors.json` (see the section above).

## Removed: the Kivgraph provider

The Kivgraph provider went away in #208 with every setting that only it read:
`[orchestrator.kivgraph]` (and its `process` and `dashboard_process` tables),
the `kivgraph` runner name in `orchestrator.runners`, the `kivgraph.*`
implementation blocks, `indexed_by` on a `[[repository]]` (and in a repository's
own `.atenea/config.toml`) and the `requires_index` constraint. Each is refused
at load, naming the setting and the edit (for example `[orchestrator.kivgraph]
was removed in contract 5.0.0 with the Kivgraph provider; delete the table and
every sub-table`). So is any `[[capability]]`, `[[implementation]]` or
`[[selector.rule]]` that names a capability only Kivgraph served; the list is in
the [5.0 migration guide]({{< relref "migration-5" >}}).

## MCP servers

```toml
[[mcp_server]]
id = "fixture"                        # the name the client will see
url = "http://127.0.0.1:40010/mcp"   # http endpoint
dashboard = "http://127.0.0.1:40010/dashboard"  # optional web UI; opened only by `atenea dashboard <id>`
timeout = "5s"                       # bounds the check; omitted takes the default
expose = "off"                       # off (default) points the client here; raw is a passthrough

[[mcp_server]]
id = "graph"
command = ["graph-backend"]           # stdio; started once per check, then killed
[mcp_server.env]
GRAPH_BACKEND_UI = "false"
```

This list is not the catalog and nothing dispatches against it. Atenea reaches
its own providers through adapters; these are endpoints `atenea wrap` hands to
*someone else's* client so that client stops spawning a private copy of a
server that is already running.

`dashboard` is optional metadata for a real HTTP(S) web UI belonging to the
MCP. Atenea validates its scheme, host and port, reports the URL in `status`
and `detect`, and never opens a browser when the MCP starts or is probed. To
open one explicitly, use `atenea dashboard <id>`. The command checks that the
URL is reachable first. `atenea dashboard hosts --dry-run` previews the
idempotent, Atenea-managed block in `/etc/hosts`; writing that file requires
the explicit `atenea dashboard hosts` command and appropriate permissions.

Dashboards use the explicitly configured URL. No provider discovery is performed.
Atenea no longer serves a dashboard of its own: a settings file that still has
a `[dashboard]` table is rejected as an unknown key, and the table must be
deleted.

Each block sets `url` or `command`, never both; repeating an `id` is refused
rather than resolved; and an `id` may not contain a dot. Those refusals are
the same rule: the payload is keyed by `id`, so a block that cannot be turned
into exactly one endpoint would end up either ignored or silently overwritten
by the block after it, and a declaration nobody can act on is worse than no
declaration -- a client is told the server exists before anyone finds out it
does not. The dot is refused for the same reason one step later: a passthrough
tool is named `raw.<id>.<tool>`, which can only be split back into a server
and a tool while the server is one segment.

`expose` is the field that separates the two things this list can mean. `off`,
the default and everything described above, makes the entry a pointer: the
client is told where the shared server is and talks to it directly. `raw`
means Atenea holds the connection and re-offers that server's tools verbatim
under `raw.<id>.<tool>`, with no funnel, no capability, and no health record
of its own.

A backend declared `raw` is dialed on the first call that needs it, not at
startup: one that is down when Atenea starts must not stop it starting, and one
that comes up later must start working without a restart. The session is opened
once and shared by every chat, which is the whole reason for declaring it here
rather than in five client configs. `tools/list` then carries that server's
tools after Atenea's own capabilities, named `raw.<id>.<tool>`, with the
backend's own input schema forwarded unedited -- no `repository` argument is
added, because a raw tool has no idea what a repository is. A backend that does
not answer is left out of the list rather than listed as broken. Output schemas
and structured content are forwarded too. The catalog is cached for the
lifetime of the shared backend process and concurrent `tools/list` requests
are coalesced; a process restart invalidates that cache.

What a raw call does *not* touch is the point of keeping it separate: no
funnel, because there is nobody to choose between; no capability, so no schema
of Atenea's is checked against it; and no row in the measurement base, because
latency with no competitor is not evidence in a decision. It still leaves a
receipt -- `kind = "raw"`, written closed, with the step's `funnel.state` set
to `none`. That is not the same as a step whose funnel went unrecorded, which
reads `not_kept`, and telling those two silences apart is why the field has
three states rather than being absent.

### What a raw backend may offer, and what it costs

A `raw` block must declare both, and neither has a default:

```toml
[[mcp_server]]
id = "semgrep"
url = "http://127.0.0.1:40020/mcp"
expose = "raw"
instance = "shared"                                  # shared | per_chat
tools = ["get_supported_languages", "semgrep_scan"]   # the allow list
effects = ["read"]                                    # what they may cause

  [[mcp_server.tool]]
  name = "semgrep_scan"      # narrower than the server's, for one tool
  effects = ["read", "process"]
  timeout = "60s"             # optional per-tool override

    [[mcp_server.tool.rule]]
    when = { action = "read" }
    effects = ["read"]        # unmatched calls use the conservative fallback
```

`tools` is the budget. Only the names on it are offered, and only they can be
called: the list is enforced at the backend, so a name that never appeared on
any tool list is still refused when a client sends it anyway. Both readings of
an *absent* list are defensible -- offer everything, offer nothing -- which is
exactly why neither is guessed. A block declaring `raw` with no `tools` is
refused, and so is an empty list.

A raw **stdio** block may also set `working_directory = "/canonical/workspace"`
next to `command`. It is not supported by HTTP or pointer (`expose = "off"`)
backends. The path must be absolute and clean; both readiness probes and raw
child spawns check that it is an existing directory without a symlink alias and
use it as the child's actual cwd. An omitted value preserves cwd inheritance.
A tool request cannot redirect this process. The exact agent-device `0.21.23`
contract requires this binding and checks each local `cwd` argument against it;
see [MCP compatibility](../mcp-compatibility/).

`effects` is what those tools are authorized to cause, in the same five names
capabilities use: `read`, `write`, `external`, `process`, `device`. Atenea cannot infer
them; a backend's own list can hold `execute_shell_command` beside
`find_symbol`, and nothing in a name or a schema says which is which. A
`[[mcp_server.tool]]` block narrows the declaration for one tool, and a tool
with no block of its own causes what the server declared. A per-tool block
naming a tool outside `tools` is refused -- it describes a call that is already
refused a layer earlier, and left in it reads as coverage nobody has.
Rules under a tool may narrow effects when the listed argument values match; if
no rule matches, the tool's declared effects remain in force.

At call time the declared effects are held against what the chat may
authorize, through the same `Session.entitled` a capability crosses. Reading
is free; anything else needs a grant. Captured against the live `semgrep` on
the author's machine, with the block above:

```text
get_supported_languages  ok       supported languages are: apex, bash, c, c#, …
semgrep_scan             refused  permission_denied: session … may not authorize process
semgrep_rule_schema      refused  permission_denied: semgrep: tool "semgrep_rule_schema"
                                  is not in this backend's tools
```

All three left a receipt, and each carries the effects it was measured
against -- `["read"]`, `["read","process"]`, `["read"]`. A refused call is
recorded exactly like one that ran, because an attempt that was stopped is
what an audit is looking for.

That middle line was captured against a chat holding nothing beyond `read`,
which in the historical `0.10.0` behavior was every chat there was. The same call on a machine
whose `client_effects` grants `process` now runs: the refusal was the ceiling
being unreachable, not `semgrep_scan` being forbidden.

A raw backend is also **held back from `atenea wrap`**. Every other entry is
handed to the client so it can talk to the shared server directly; doing that
for a raw one would point the client past the allow list and the effects
check, so the budget would be bypassed by the very command meant to apply it.
It is still probed and still reported, under `held`.

`instance = "shared"` is the default and keeps one upstream MCP session for
the whole Atenea service. Use `instance = "per_chat"` when the backend's
session state belongs to one client conversation: Atenea creates that backend
on the first `tools/list` or `tools/call` for the connection, reuses it for the
rest of the chat, and closes it when the connection ends. The setting applies
to both HTTP (`url`) and stdio (`command`) raw backends; it is refused on a
pointer (`expose = "off"`) because Atenea does not own that lifecycle.

`raw` is reserved as the first segment of any capability or implementation id,
so nothing in the catalogue can ever collide with a passthrough name -- refused
in `Capability.Validate` and `Implementation.Validate`.

**Both transports are served.** A `url` entry is one HTTP session shared by
every chat. A `command` entry is one *process*: Atenea spawns it on the first
call that needs it, replays the MCP handshake once for the process rather than
once per chat, holds its stdin open for as long as it lives -- a stdio server
reads EOF on stdin as its client leaving -- and routes each answer back to the
chat that asked by request id. It is stopped when Atenea stops, because Atenea
started it. This is the case worth declaring: an HTTP server can be pointed at
by five clients, but a stdio server has no address, so every client that wants
one has no choice but to spawn its own. On the machine this was written for
that meant five private copies of one indexer, each holding its own index of
the same repositories.

One rule is stricter over a pipe than over HTTP: **a call that dies is not
retried**. A `tools/list` that died is asked again of the replacement, because
nothing happened; a `tools/call` is not, because the server may have run the
tool and died carrying the answer back, and re-sending it would run a declared
`write` twice. The chat is told instead, and the next call gets a fresh
process.

`atenea wrap opencode` handshakes every entry here and passes on only the ones
that answered -- minus the raw ones, which Atenea serves itself. Captured on a
real machine:

```text
wrap opencode  4 checked: 3 declared, 0 refused, 1 held

  declared  chrome-devtools  http   http://127.0.0.1:40021/mcp  chrome_devtools 1.6.0 (2ms)
  declared  context7         http   http://127.0.0.1:40011/mcp  Context7 4.0.0 (478ms)
  declared  fixture           http   http://127.0.0.1:40010/mcp  the fixture provider 1.28.1 (27ms)
  held      semgrep          http   http://127.0.0.1:40020/mcp  Semgrep 1.23.3 (3ms)
                             served as raw.semgrep.<tool>; opencode is not pointed at it

  Declared means it answered an MCP handshake, not that its tools work.
```

chrome-devtools was measured at the time of this audit; it is no longer a supported or declared backend (see #209). context7 was measured at the time of this audit; it is no longer a supported or declared backend (see #209). The capture above is dated evidence.

`atenea wrap --via-headroom opencode` composes the same Atenea overlay with
Headroom's provider and transport configuration. The two JSON payloads are
deep-merged by key; malformed payloads or incompatible key collisions stop the
launch instead of silently dropping an MCP server or changing the route.

## What `declared` promises, and what it does not

The check is one MCP handshake per server. That is a real measurement and it
is a narrow one: it proves the server is reachable, starts, and speaks the
protocol. It proves nothing about whether its tools work.

The gap is not theoretical. On the machine above, `semgrep` answers the
handshake in under two seconds and reports version 1.23.3 -- and every call to
its primary tool fails, because the tool builds its ruleset from a registry
the machine is configured not to contact. It had been failing for days. Wrap
declares it, correctly, and the report says in one line what that word covers,
which is why the line is printed on an all-green run too: `5 declared, 0
refused` is exactly the moment the word is trusted furthest and examined
least.

Checking further would mean calling tools, and calling a tool is doing work --
with side effects, a bill, and no way to tell a broken server from a slow one.
The handshake is the last thing that is free. Wrap narrows the distance
between *declared* and *working*; it does not close it, and it says so.

A refused server is **absent** from what the client receives, not disabled in
it. The client merges Atenea's configuration over its own key by key, so an
absent key leaves the user's entry alone: Atenea declining to vouch for a
server must never be the reason a working one disappears. The corollary is
visible above -- `chrome-devtools` stays broken in exactly the way it was
already broken, and wrap's contribution is that somebody now knows.

Nothing is written to disk. The configuration rides in one environment
variable or on the client's own command line, for the lifetime of the child
process, so a client launched without `wrap` is a client with exactly the
configuration it had before, and there is no `unwrap` because there is nothing
to undo. Three clients are wired today, and they take it in two ways:

| client | how it arrives | against the client's own |
|---|---|---|
| `opencode` | `OPENCODE_CONFIG_CONTENT` | deep-merged, key by key |
| `claude` | `--mcp-config <json>` | added to every other source it resolves |
| `codex` | one `-c mcp_servers.<id>={...}` per server | one key each, rest of the table untouched |

The codex shape is one server per override rather than one for the whole
table on purpose: `-c mcp_servers={...}` replaces the map, and every server
the user declared in their own `config.toml` would be gone for the length of
the session.

`omp` is not wired, and it is the interesting one. Its MCP servers are read
from `mcp.json` files and nothing else -- there is no config-content variable,
and its `--config` overlay feeds a settings tree whose schema has no
`mcpServers` key in it at all, so even an inline overlay could not carry a
server. Wrapping it would mean writing one of those files. That is the
guarantee above being traded away, so it is not done, and `atenea wrap --help`
says so by name rather than leaving a reader to discover the omission.

## Desktop client profiles

The optional `[[desktop_profile]]` blocks define the policy Atenea applies when
wrapping Claude Code, ChatGPT Desktop or Codex. The built-in profiles are
`claude`, `chatgpt`, `shared` and `agent-device-full`; a user block with the same `name` replaces
that preset completely.

The profile fields are `mcp_mode`, `direct_mcp`, `enabled_tools`,
`disabled_tools`, `raw_catalogs`, `startup_timeout`, `tool_timeout`, `fallback` and
`client_flags`. `atenea_only` exposes only Atenea, while `hybrid` may expose
declared `expose = "on"` MCPs listed by `direct_mcp`; raw MCPs are never
exposed directly. Tool allowlists run before denylists and are enforced both
when listing and calling tools. `startup_timeout` and `tool_timeout` cap the
session and call durations, and `fallback` is either `diagnostic` or `none`.
Client flags are added only when the installed client advertises them.

`raw_catalogs` selects a named catalog for a raw MCP server. The
`agent-device` catalog is `core` by default and `full` in the explicit
`agent-device-full` profile; unknown catalog names or server ids are rejected.

## Arguments handed to the client, and `--auto`

Everything after the client name is passed to the client untouched. For
`opencode` wrap adds nothing of its own: verified by replacing the binary with
a stub that printed its argv, which received exactly `--auto`. For the two
command-line clients it adds exactly the flags in the table above, and where
they go is not cosmetic. `--mcp-config` is variadic -- it swallows every
following token that does not begin with a dash -- and it is also a global
flag, which a subcommand will not accept. All three placements were measured
against claude 2.1.220 and only one survives both facts:

| placement | result |
|---|---|
| `claude --mcp-config <json> mcp list` | `MCP config file not found: mcp` |
| `claude mcp list --mcp-config <json>` | `error: unknown option '--mcp-config'` |
| `claude --mcp-config <json> -- mcp list` | the subcommand runs |

So the flags go in front, and a `--` is inserted when the user's first
argument is a bare word. Appending them behind the user's arguments reads as
the safe choice and was shipped that way first: it works for a session and
kills every subcommand, because a global flag behind a subcommand is not a
global flag any more.

That a session honours the injected config is measured rather than read: a
server whose command leaves a file behind on start left one. It cannot be
read back with `claude mcp list`, which reports the servers on disk and
ignores the flag entirely -- even under `--strict-mcp-config`, where the list
came back identical to the unflagged one.

That flag is worth naming here rather than leaving it in a shell alias,
because it is the one part of the launch line Atenea does not govern. It
belongs to opencode, whose own help describes it as *auto-approve permissions
that are not explicitly denied (dangerous!)*. It is not a `wrap` option and
never was; on the machine this was written on it arrived by inheritance, from
an older `headroom wrap opencode --auto` alias, and rode along unexamined when
the alias moved to `atenea wrap` on 2026-08-09.

It does not widen anything Atenea enforces. The permission check refuses a
step whose effects its capability does not declare no matter which client
asked, so `--auto` cannot buy a caller an effect the settings file withholds.
What it governs is the other half: what the client approves on its own behalf,
with its own tools, before Atenea is ever consulted. The two are independent,
which is exactly why one of them being a default nobody chose is worth writing
down.

Recorded as a standing posture decision to revisit, not a recommendation.

## Selector rules

```toml
[selector]
health_stale_after = "24h"
```

`health_stale_after` is how long a runtime provider observation remains
trusted. When it expires, Atenea treats the provider as `unknown` and lets the
next dispatch re-probe it; the setting does not age declarative health written
in the catalog.

The service also probes declared MCP servers every `core.health_probe_every`
and persists the last result under the private state directory. A successful
probe is `ok`, a failed handshake is `failed`, and an unconfigured interval of
`0s` keeps probing on demand through `atenea detect` only. The probe checks the
MCP handshake; it does not execute tools or claim semantic availability.

```toml
[[selector.rule]]
capability = "code.search"
repository = "api"          # omit to apply everywhere
prefer = "ripgrep"
```

The most specific rule wins: one scoped to a repository beats a global one for
the same capability. Two rules for the same capability and repository are
refused rather than resolved by file order.

A rule pointing at something the catalog does not have stops the boot. A rule
that quietly matches nothing is a preference the user believes is in force and
is not.

## A repository's own settings

A repository may carry `.atenea/config.toml` at its root. It is a partial
overlay on the global file: what it declares wins, what it leaves out falls
back, and a repository without one changes nothing at all. Nothing in it is
required.

```toml
# /home/you/work/api/.atenea/config.toml
[[repository]]
scale = "medium"           # this repository is medium here, whatever the global file says
                           # languages and vcs are not named, so they inherit

[[selector.rule]]
capability = "code.search"
prefer = "ripgrep"         # in this repository only
```

`atenea config show` prints the result with the origin of every field, which is
the only way to tell an inherited value from a declared one that happens to
match:

```
global   /home/you/.config/atenea/atenea.toml
overlay  /home/you/work/api/.atenea/config.toml
         root /home/you/work/api, patches repository api
         declares repository, repository.scale

repository api  /home/you/work/api
  global   languages   go
  local    scale       medium
  global   vcs         present
```

### Which file applies

The active repository is found by walking up from the working directory to the
first `.git`, and the overlay is the one at that root. The walk stops there: a
repository nested inside a workspace does **not** inherit the workspace's
overlay. A nested repository is cloned and published on its own, so inheriting
would make it behave differently depending on where it happens to be checked
out — and it is the same boundary the client harnesses on this machine already
stop at, so there is one rule to remember rather than two.

Measured in a workspace holding a nested repository, with an overlay in each:

| working directory | overlay that applies |
| --- | --- |
| `kena-workspace` | `kena-workspace/.atenea/` |
| `kena-workspace/cli` | `kena-workspace/.atenea/` |
| `kena-workspace/libraries` (own `.git`) | `libraries/.atenea/` |
| `kena-workspace/libraries/packages/env` | `libraries/.atenea/` |

From inside `libraries`, the workspace's own declared scale is still what the
global file says. The outer overlay is not consulted, not merged and not
partially applied.

A `[[repository]]` block is matched to the global catalog **by path**, not by
id: the subject of the file is the directory it was found in. When the global
file already declares that path, the block patches it field by field. When it
does not, the repository is added, taking its id from the directory name — and
a clash with an id the global file already uses for a different path is refused
rather than resolved.

### What a repository may not declare

The file travels inside the repository, so cloning somebody's repository is
accepting their settings. That is a different trust question from the global
file, where the machine's owner is the only author, and most of this schema
answers it badly: `[[mcp_server]]` and every `process` block carry a command to
launch, `[[implementation]]` decides what runs behind a capability, and a
shortened `[security] sensitive` disarms the skip that keeps secrets out of a
search.

So the overlay accepts three things and refuses the rest **by name**, with the
reason:

| allowed | what it says |
| --- | --- |
| `[[repository]]` — `languages`, `scale`, `vcs` | what this repository is |
| `[[selector.rule]]` | which implementation to prefer, for this repository only |
| `[security] sensitive` | further files to treat as delicate |

`sensitive` is **unioned** with the global list, never replaced: a repository
may tighten the guard and may not loosen it, so an empty local list adds
nothing rather than clearing anything.

A selector rule is scoped to the overlay's own repository whether or not it
says so, and naming a different one is refused. `prefer` must name an
implementation the global settings declare — a rule pointing at something this
binary does not have would be a preference the reader believes is in force and
is not, which is the same refusal the global file already makes.

`repository.path` and `repository.id` are refused too. The path is the
directory the file was found in; letting the file name another one is the only
way this layer could reach outside its own tree.

Everything else — `contract`, `[core]`, `[orchestrator]`, `[metrics]`,
`[backup]`, `[[capability]]`, `[[implementation]]`, `[[mcp_server]]` — is
refused naming the block and why. The keys that left the schema with the
built-in agents (`[[agent]]`, `[local_agents]`, `[model]`) are refused naming the
removed table:

```
atenea: invalid_input: local settings /tmp/repo/.atenea/config.toml:
  mcp_server: it carries a command to launch, so a cloned repository would be
  handing this machine a process to run; repository.path: the path is the
  directory this file was found in; naming another one is the one way this
  layer could reach outside its own tree
```

Set `ATENEA_LOCAL_CONFIG=0` to ignore the layer entirely.

### Where it does not apply

`atenea run` — the service — reads the global file alone. It is one process
answering about every repository on the machine, and the overlay of whichever
directory it was started in would be wrong for all the others. `service
install` is the same, for the same reason.

One consequence is worth knowing rather than discovering: when an overlay
applies, `settings` names both files, joined by ` + `. `atenea status` compares
that string against the running service's before trusting its answer, so a
command run inside a repository with an overlay computes its own screen instead
of printing one built from settings that are not the ones in force here.

### A directory that declares nothing

`.atenea/` present with no `config.toml` inside is refused rather than ignored.
The mistake it catches is `.atenea/atenea.toml`, and left quiet it is a whole
settings file that never takes effect and never says so.

## What the repository asks for

The layer above is the settings a repository carries *for Atenea*. This one is
different in every way that matters: it is the configuration a team keeps for
**its own clients** — `.mcp.json` and `.claude/` for Claude Code, `opencode.json`
and `.opencode/` for opencode — and Atenea has no authority over it at all.

`atenea intent` reads it and answers one question: for each thing this project
asks for, what happens here?

```
asked for
  funnel     fixture (local)
             -> provider fixture: code.search, symbol.overview
                via fixture.overview, fixture.search
  vouched    semgrep (local)
             declared by Atenea and handed to clients by `atenea wrap`
  unmatched  dart (local, off in the project's settings)
             nothing registered here provides it

1 unmatched: this project asks for them and nothing here provides them
  dart (.mcp.json)
```

### It reads and it never runs

A `.mcp.json` is a list of commands somebody else wrote, and it arrives with a
git clone. The whole value of Atenea sitting between a client and its backends
disappears the moment reading such a file can start a process.

So the guarantee is not a policy, it is the shape of the code: a declaration's
command, arguments, environment and URL are **dropped at the parse boundary**.
They are not stored, not printed, not passed on. Nothing downstream can run
them because nothing downstream has them. A test walks every field of the
parsed result and fails if any of those strings is reachable from it.

That is the same trust model as the `.atenea/config.toml` allow-list one
section up, arrived at from the other side: there, a cloned repository may
adjust settings but may never name a command to launch; here, a cloned
repository may say what it wants but may never be the reason something starts.

### The unmatched list is the point

It is printed last, counted, and never abbreviated. A translator that quietly
dropped what it could not map would produce a report where "everything is
answered" and "half of it vanished" look identical, and the reader would learn
only that the command ran.

### One backend, two clients, one ask

A project that uses both clients declares the same backend twice. Those
collapse into one row, with both files named on it, because the count is what
the report is read for and a two-client repository does not want twice as much
as a one-client one.

Two declarations of one name that **disagree** — different transports — are
reported as inconsistent rather than resolved. A convenience that hides a
contradiction in somebody's own configuration has stopped being one. Enabled is
the exception and is a union: switched off for one client and on for another is
on, because reporting it as off would describe a machine nobody is running.

### It writes nothing

Not to `.claude/`, not to `.opencode/`, not anywhere. Measured the way the
overlay layer was: 30,956 files under the clients' own directories, snapshotted
before and after six invocations — nothing created, nothing removed, nothing
modified.
