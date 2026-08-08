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

## Every plan step is the same capability

The orchestrator's card declares eight capabilities. Its planner emits one.

A commission is `N` explore steps and `N` work steps, one of each per
repository in scope, and every one of them is `code.search` --
`orchestrator.go:442`, `:727` and `:851`, which are all the sites that set a
step's capability. Measured on 2026-08-08 against the shipped catalogue: a
commission over three real repositories produced six steps in two waves of
three, `verdict ok`, and not one of them asked anything but `code.search`.

So a plan that mixes capabilities is not something a richer catalogue would
produce. It needs a planner that chooses a capability per step -- the obvious
first shape being explore with `code.search`, then resolve what the explore
found with `symbol.definition`, which is a real commission a person would want
and which nothing today can express. Until then the honest description of
`atenea task` is: it splits a search across repositories and reviews the
answers, and the other seven capabilities are reachable only one at a time
through `atenea ask`.

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

## Every client declares its own servers, and Atenea offers seven tools

This entry is not from the twenty-eight sheets. It is a later goal, written down
on 2026-08-08 with the machine measured rather than assumed, because it is the
first thing asked of Atenea that its central principle does not obviously allow.

The measurement first. On this machine there are **eight MCP servers and
thirty-four declarations of them** across five client configuration files:
`chrome-devtools` (29 tools, the only one every client agrees on), `serena` (30),
`codebase-memory` (8 cold, 14 warm), `semgrep` (4 filtered over HTTP, 7 raw over
stdio), `context7` (2, and `omp` talks to 3.2.5 while everyone else talks to
4.0.0), `headroom` (3), `claude-mem`, and a second `serena` on `:9121` that only
Atenea's own settings declare. A client sees between 43 and 85 tools depending on
which one it is. Atenea offers **seven**. Six `codebase-memory` processes were
running at once, one per client session, beside a seventh that a hand-written
shim keeps alive for its web UI with a replayed `initialize` and a FIFO -- so
somebody already needed one shared instance and solved it once, for one server,
outside Atenea.

The goal is that a client declares Atenea and nothing else. Nine tools of fifty
have a capability and an adapter, so the question is what happens to the rest,
and the answer cannot be "write forty capabilities": most of those tools have
exactly one provider and nothing to choose between, and a capability whose funnel
can only ever pick one thing is a ceremony.

So: a second mode. A declared backend server, one shared instance of it, its
tools re-exposed verbatim through the same socket, with no funnel and no
capability. The honest objection is that this is a proxy wearing an
orchestrator's name, and it is the right objection -- a proxy forwards
everything, and the day passthrough is the easy path is the day capabilities stop
being written.

What makes it coherent is that Atenea already started this and stopped halfway.
`[[mcp_server]]` exists in the settings file today, keyed by `id`, `url` XOR
`command`, a repeated `id` refused rather than resolved -- and its own
documentation says why it exists: endpoints `atenea wrap` hands to *someone
else's* client "so that client stops spawning a private copy of a server that is
already running." The problem is already named in the design. `wrap` chose the
weaker remedy: tell the client where the shared copy lives, then step out of the
path. That removes duplicate processes and leaves thirty-four declarations in
five files. This entry is the stronger remedy, and it is the same list gaining a
dispatch path rather than a new mechanism beside it.

Four conditions keep it from becoming the proxy, and they belong in the code that
loads the catalogue, not in this page:

1. **The two namespaces are disjoint and unreachable from each other.** A
   capability call can never be answered by a passthrough server, and a
   passthrough tool can never be selected for a capability.
2. **No funnel, said out loud.** A passthrough step records that it had no funnel
   because it is passthrough -- never an empty one. See the entry above: a step
   whose funnel was not kept and a step that never had one must not read the same.
3. **A capability shadows its own raw tools.** The day a second implementation of
   `code.search` arrives through a backend, the raw tool stops being offered, and
   a check fails the build if both are live -- so passthrough cannot quietly
   become the permanent answer to something that deserves a capability.
4. **Passthrough never feeds the measurement base as an implementation.** Its
   latency is an operational fact, not evidence in a decision, because there is
   no competitor for it to be evidence against.

### How a passthrough server is declared

The existing block gains an exposure mode and a lifecycle, and keeps every
refusal it already has:

```toml
[[mcp_server]]
id = "codebase-memory"
command = ["codebase-memory-mcp", "--ui=true"]
expose = "raw"          # off, today's behaviour: wrap only, nothing dispatches
instance = "shared"     # shared, per_repository, per_chat
tools = ["search_code", "search_graph", "trace_path"]   # deny by default
effects = ["read"]      # undeclared is `unknown`, and `unknown` is refused
```

`tools` is an allow list and the default is empty, so a newly declared server
offers nothing until someone names what it may offer. That is the whole answer to
the surface: `chrome-devtools` is eight tools or twenty-nine by one edit in one
file, which is what five client configs have been trying and failing to hold in
agreement.

**Landed on 2026-08-08, in two steps.** First, ahead of any dispatch: `expose`
parsed and its value checked, `off` the default an older file inherits, an
unknown value refused rather than read as `off`, a dotted server id refused,
and `raw` refused as the first segment of any capability or implementation id
-- so the namespaces were disjoint before anything could occupy them.

Then the dispatch path itself. A declared `raw` backend is dialed on the first
call that needs it, its tools appear on `tools/list` as `raw.<id>.<tool>` after
Atenea's own, and a call is forwarded whole and answered whole. Measured
against the live `semgrep` on this machine: a real client saw 8 capabilities
and 4 `raw.semgrep.*` tools on one list, and `raw.semgrep.get_supported_languages`
returned 482 bytes of real answer. Every receipt it writes is `kind = "raw"`
with `funnel.state = "none"` -- and in the same run directory, a `code.search`
step reads `kept` with four stages and two named drops, which is the pair the
receipt entry below was written for.

Then the budget and the permissions, which is what made the mode usable rather
than merely working. `tools` is required of a raw block and has no default:
only the names on it are offered, and only they can be called -- enforced at
the backend, so a name that never appeared on a list is refused when a client
sends it anyway. `effects` is required too, narrowable per tool with
`[[mcp_server.tool]]`, and checked on every call against what the chat may
authorize, through the same `Session.entitled` a capability crosses rather
than a gate of passthrough's own. Measured against the live `semgrep`: two of
its four tools offered, `get_supported_languages` answered, `semgrep_scan`
refused for `process`, `semgrep_rule_schema` refused for being outside the
list -- and all three on the record, each carrying the effects it was measured
against.

Writing the budget turned up a hole in a command nobody had reread: `atenea
wrap` hands every declared server to the client, which for a raw backend is a
direct route around the allow list and the effects check. Raw backends are now
held back from every payload and reported under `held`.

What is still absent is `instance`. Every raw backend is one shared session
for the whole process; `per_repository` and `per_chat` are not built, and the
key is refused as unknown rather than accepted and ignored. Passthrough is
also HTTP-only: `expose = "raw"` on a `command` entry is refused, because one
stdio process shared across chats is a lifecycle nobody has built here yet.

### Names, and a collision that is invisible from a tool list

Capability ids are dotted, and so are implementation ids: the base is keyed by
`capability, implementation, repository`, and today's implementations are
`ripgrep`, `claude.search`, `serena.definition`, `codebase-memory.impact`. So
`serena.find_symbol` is not merely ugly, it is ambiguous with an implementation id
in every receipt and every metrics row. Passthrough tools take a reserved first
segment -- `raw.serena.find_symbol` -- and loading refuses `raw.` as the first
segment of any capability or implementation id, and refuses a server id
containing a dot. Two backends both exposing `search_code` stop colliding by
construction, because the server id is in the name.

### Permissions, for a tool Atenea does not understand

Atenea cannot know what a raw tool does, and that serena list contains
`execute_shell_command` and `replace_in_files`. So effects are **declared** per
server, and per tool where a server mixes them.

Two things in the original sketch turned out differently when it was built.
Undeclared was to mean `unknown`, refused unless a client held a grant naming
the server; instead the *declaration* is refused, so `unknown` is not a state
that can exist at runtime and no new kind of grant was needed. And the refusal
was to happen in `commissioned` -- but that wrapper takes a `contract.Runner`
and a capability, and a raw call has neither, so it never crosses it. The same
rule lives one seam earlier in `Session.entitled`, which is where a
capability's `tools/call` is also held to what the chat may authorize. The
intent was right and the address was wrong: still one gate, not two.

`grounded` applies only when a raw call names a repository, which most will
not, and inventing the parameter so the check has something to read would be
worse than not checking.

### Receipts, for a payload Atenea cannot read

Recordable: the tool name, the backend's own name and version from its
handshake, the duration, the transport verdict, whether the payload came back
flagged as an error, and which chat asked. Not recordable, and each one has to be
*said* rather than left to look like an absence: no discoveries, because nothing
here can interpret an opaque result and a silent omission is indistinguishable
from a tool that found nothing; no cost unless declared.

And health from transport failures only. A tool-level error is usually the
caller's fault -- `invalid_input: unknown input field` is a client sending a key
the schema does not have -- and feeding those into a provider's health record
would condemn a working server for a client's mistake. That is the same defect
that shipped in `0.9.1`: a missing working directory blamed on the binary.

### One shared instance, which is the hard part

An HTTP backend already serves many sessions. A stdio backend is one duplex pipe
with per-connection state, so fan-in needs request ids remapped per chat,
responses routed back to the chat that asked, a decision about what happens to
server-initiated notifications, and a lifetime longer than any one chat --
which is what the supervisor with `EnsureReady` and `Acquire`/`Release` already
does for adapters, and is the reason this is an extension rather than a new
subsystem.

The trap is state that belongs to the connection, and the machine already proves
it: serena's `activate_project` is process-wide, which is exactly why a second
serena runs on `:9121` for one repository. Two chats sharing one instance would
fight over the active project. So the policy is per server and never global:

- **`shared`** -- stateless, or state that is globally consistent:
  `codebase-memory`, `semgrep`, `context7`.
- **`per_repository`** -- the state *is* the project: `serena`. This generalises
  the `:9121` special case into a rule instead of a second machine-level
  exception.
- **`per_chat`** -- the state is the conversation. Saves no processes, but keeps
  the server declared in one file instead of five.

A backend's tool list is also not a constant: `codebase-memory` advertises eight
tools cold and fourteen once its store is open, the extra six being the
project-lifecycle ones. Passthrough lists per session and forwards
`tools/list_changed`; a snapshot taken at declaration time would be wrong on this
machine today.

### What cannot move, so the goal is Atenea plus two

`headroom` compresses *this session's* tool output and hands back a hash to
retrieve later: its state is the session, and centralising it either leaks one
chat's content into another's retrieve or needs a session key Atenea cannot
originate. `claude-mem` reads the client's own transcript directory, config dir
and working directory; it is a function of the client's private context. Both
stay client-declared, and "clients declare Atenea and nothing else" is therefore
false by two -- said here so the plan is not measured against a target it was
never going to reach.

One of the two was **absent rather than immovable** for a while, and what
replaced that absence is worth more than the absence was. On 2026-08-08 the
`claude-mem` declaration in two clients was found to be a locator that exits 1
with `claude-mem: mcp server not found`: the plugin tree it searched was gone,
so the server had not run for days. It was reinstalled the same day and now
runs. The exception was always about what the server *is*, not whether it
happens to be installed, so the count stays two.

Reinstalling it surfaced something the absence had been hiding. The opencode
plugin had been failing to load since 2026-07-10 -- 141 logged
`Plugin export is not a function` errors -- because opencode's loader walks
every export of a plugin module and throws on the first that is not a function,
and the shipped bundle exports two arrays beside its plugin. Re-exporting only
the function fixes the load, and opencode captures for the first time on this
machine: a real run produced a session, a prompt and a summary.

**It captures and cannot be read back, and that is the state to write down.**
The plugin sends `prompt: ""` on session init -- not a race, a constant in the
bundle -- so the worker stores the placeholder `[media prompt]`, and the worker
injects prior memory only when the prompt is *not* that placeholder. The two
halves are the same line of code seen from either end: writing without reading
is half a memory, and the half that is missing is the half a client would
notice. Measured, not inferred: observations stayed at 325 across a
tool-using run, because the plugin fires its observation POSTs without awaiting
them and a one-shot `opencode run` exits first. None of this changes what
`claude-mem` is -- it still reads the client's own private context and still
cannot move -- but a server that only writes is not the working exception this
paragraph used to describe.

Also out of reach in any mode: the client's own built-in file, edit and shell
tools, which are not MCP and never route here; and argument shapes that mean
different things depending on whose working directory is the truth --
`semgrep_scan` takes an absolute host path and `semgrep_scan_with_custom_rule`
takes a relative one plus inline content, so the same call means different things
depending on who is holding the instance. This one carries a warning from the
machine it was measured on: that semgrep once ran as a container with the code
roots mounted read-only, its own rules kept promising that barrier for days after
it stopped existing, and the escape was proven with a bait file outside every
declared root. Whoever holds one shared instance inherits its reach, so this is
where the `grounded` check earns a second use: refusing a raw path that names no
declared repository is the only barrier left once the mounts are gone.

### Decisions taken on 2026-08-08, so they are not re-litigated

- **One shared `context7` credential is accepted.** It completed a Dynamic Client
  Registration and now holds a refresh token, so a shared instance means one
  identity for every chat -- and the socket admits only the owning uid, so "every
  chat" already means "this operator's chats."
- **Serena stays the host process.** When phase 5 gives Atenea the lifecycle, the
  systemd units move in the *same* change, so there is never a moment with two
  owners of the same server.
- **Curated by default, deny by default, per server.**
- **`chrome-devtools` is shared and the interference is documented:** one browser
  is the point, but a tab is per-session and two chats will steal each other's
  selected page. **Revisit the first time two chats actually collide** -- that is
  the trigger, not a feeling that it might happen.

### The order, and what proves each step

1. Namespace and its refusals; `expose` parsed and nothing dispatched. Proven by
   a settings file that fails to load for each of the three collisions.
2. HTTP passthrough, `shared`, on `semgrep` -- with the no-funnel marker landing
   together with the receipt gap above, not after it.
3. Declared effects, and `unknown` refused without a grant. Proven the way the
   undeclared key already is: a real client refused over the wire.
4. stdio fan-in on `codebase-memory`. Proven by process count -- six to one --
   with two chats attached at once and the fourteen-tool surface intact.
5. `per_repository` on serena, folding `:9121` in, units included.
6. `chrome-devtools` with its allow list, and per-client filtering deleted.
7. Clients cut over one at a time, `wrap` extended to emit the reduced config.
   Thirty-four declarations become five, plus `headroom` and `claude-mem`.

A new tool namespace and a new receipt shape are additive, so this is a contract
minor -- `2.3.0` -- and no adapter changes. It is not a `1.0.0` conversation.

**Done when:** a client's configuration names `atenea`, `headroom` and
`claude-mem` and nothing else; `pgrep -c codebase-memory-mcp` reports one with
two chats attached; and a raw call that Atenea cannot interpret still leaves a
receipt that says, in its own field, that it had no funnel because it never had a
choice.

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
