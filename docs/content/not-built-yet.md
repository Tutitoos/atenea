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

For the current implementation status, use [v1 readiness](v1-readiness.md).
This page is a historical design ledger: several entries record decisions that
were later implemented or deliberately narrowed, while the readiness page is the
acceptance source for the shipped tree.

## Ranked code search, if anything ever wants it

`code.search` used to declare a fourth graph implementation,
with no adapter behind it. It is now deleted. The entry was selectable and
impossible: on a medium repository that declared a graph index, and
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
the graph backend (8 cold, 14 warm), `semgrep` (4 filtered over HTTP, 7 raw over
stdio), `context7` (2, and `omp` talks to 3.2.5 while everyone else talks to
4.0.0), `headroom` (3), `claude-mem`, and a second `serena` on `:9121` that only
Atenea's own settings declared -- both of those `serena` units are what
`instance = "per_repository"` replaces, and they come out when it is installed.
A client sees between 43 and 85 tools depending on
which one it is. Atenea offers **seven**. Six graph backend processes were
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
id = "graph-backend"
command = ["graph-backend-mcp", "--ui=true"]
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
key is refused as unknown rather than accepted and ignored. Both transports
are reachable now: a `command` entry declared `expose = "raw"` is spawned once
and shared, which is the entry above this one.

### Names, and a collision that is invisible from a tool list

Capability ids are dotted, and so are implementation ids: the base is keyed by
`capability, implementation, repository`, and today's implementations are
`ripgrep`, `claude.search`, `serena.definition`, and the graph impact provider. So
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

### One shared instance, and the policies still missing from it

**Landed on 2026-08-08 for the only policy that existed anyway: `shared`.** A
`command` block may now be declared `expose = "raw"`. Atenea spawns the process
on the first call that needs it, replays the handshake once per spawn, holds
stdin open for the life of the process, and routes each answer back to the chat
that asked by JSON-RPC id. Measured against the real graph MCP backend:
three chats, one child process, and the child gone when Atenea stopped.

**Landed on 2026-08-08 for `per_repository` too, on the server that needed
it.** `[orchestrator.serena.process].instance = "per_repository"` turns one
declaration into one supervised process per `[[repository]]`, each launched
against its own project with `{{project}}` and its own port, named
`serena@<repo>` on the status screen. The retarget it removes was the whole
cost: `activate_project` is process-wide, so two repositories sharing one
instance tore each other's language server down on every alternation. Measured
against the real binary: two repositories, two processes, one declaration, both
reaped when Atenea stopped -- and the two hand-written systemd units that
implemented this by hand are what it replaces. They come out at install time
rather than with the commit: the binary running on this machine today is the
one that still reads them, through the per-repository endpoint key this change
removes.

That left one hole, found by driving it rather than by reading it: health was
keyed by implementation alone. A repository whose language server was missing
marked `serena.definition` down for the *machine*, and the repository next door
-- answered by its own healthy process three seconds earlier -- was refused
with `every implementation is down`. Observations are now recorded per
repository, which is the honest key in both policies.

What is left of the seam:

- **`shared`** -- stateless, or state that is globally consistent:
  the graph backend, `semgrep`, `context7`. Built, and the default.
- **`per_repository`** -- the state *is* the project: `serena`. Built.
- **`per_chat`** -- the state is the conversation. Not built. Saves no
  processes, but would keep the server declared in one file instead of five.
  Nothing on this machine needs it yet, which is why it is refused as an
  unknown value rather than accepted and ignored.

Server-initiated notifications are also still dropped. A message with no id
answers nobody, and handing one to a chat that did not ask would be inventing a
recipient; `tools/list_changed` is the one that would matter, and it does not
matter yet because the list is re-read on every `tools/list` rather than cached.
A backend's tool list is not a constant -- the graph backend advertises eight
tools cold and fourteen once its store is open -- so that re-read is the design,
not an optimisation waiting to happen. Measured again while building this: the
same server advertised `get_graph_schema` and not `list_projects` on a cold
start, so a snapshot taken at declaration time would have been wrong within
the minute.

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
4. stdio fan-in on the graph backend. Proven by process count -- six to one --
   with two chats attached at once and the fourteen-tool surface intact.
5. `per_repository` on serena, folding `:9121` in, units included. **Done
   2026-08-08**, and it cost a contract major (`3.0.0`): the per-repository
   Serena URL on `[[repository]]` is gone, since the policy answers the same
   question for a process Atenea actually owns.
6. `chrome-devtools` with its allow list, and per-client filtering deleted. **Done
   2026-08-09**, and it cost no contract change: the allow list is settings, and
   there was never per-client filtering in the code to delete -- it lived in the
   clients' own declarations, which are gone. Proven live against the running
   service: thirteen of twenty-nine tools reachable, and `click` and
   `lighthouse_audit` refused by name with `not in this backend's tools`.
7. Clients cut over one at a time, `wrap` extended to emit the reduced config.
   Thirty-four declarations become five, plus `headroom` and `claude-mem`.
   `wrap` is **done 2026-08-09** and it cost no contract change: the payload
   holds back the backends a capability already answers for, and carries the
   core itself, which it never did. Fixing it surfaced why the surface it
   serves had been half dead -- see the `roots/list` entry in the changelog.
   The curated surface was then proven live: `context7` and `semgrep` given
   their allow lists, twenty-six tools reachable, one call per backend
   answered against this repository, and the two refusals are the effect gate
   answering instantly rather than anything hanging.

   What was left at that historical checkpoint was not code: repointing the four client configurations on a
   machine. That is the one step this repository cannot finish on its own,
   because those files are written by another repository's installer, and a
   cutover it does not know about is a cutover its next run silently reverts.

A new tool namespace and a new receipt shape are additive, so those steps were a
contract minor -- `2.3.0` -- and no adapter changes. Step 5 was not: removing a
field is a major, and the contract then read `3.0.0`. At that historical
checkpoint it was still not a `1.0.0` conversation; the current release status
is recorded in [the final audit](v1-final-audit.md).

**Done when:** a client's configuration names `atenea`, `headroom` and
`claude-mem` and nothing else; the backend process count reports one with
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

That was true of the wrapper and wrong about the key. The grant was never
handed to a chat: `Open` copied whatever the caller asked for, `initialize`
asked for nothing because nothing in the protocol let a client say, and
`Allows` consulted that empty list alone. So `client_effects` was a ceiling
with no floor under it, and every raw tool declaring more than `read` was
refused on every machine whatever the file said — the wrapper bound
correctly onto a permission nobody had granted. A chat now opens holding
what the operator grants clients, and may ask at `initialize` to hold less.
Found on 2026-08-09 by driving a real client against a declared backend
rather than by reading the key's own tests, all of which passed.

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

## Three sidebar sections, cut down to one line — 2026-08-11

All three were designed, and all three were measured before a line was written.
Two of them the measurement killed outright; the third it cut down to one line that
is absent most days. Each is here with the condition that would revive the full
thing, because a feature dropped without its condition gets re-proposed every few
months.

**The third one came back the next day, and the condition is why.** It was written
down as "something refreshes those figures without a command being typed by hand",
and looking for that turned up a source that had been on this machine the whole
time — a per-provider usage report omp refreshes itself about every ten minutes.
The rate-limit section is built, and its entry is gone from this page rather than
left here reading as unbuilt. What the condition actually caught was a measurement
error: the 4 %-live figure was measured on the client that rewrites a file, and
then reported as a fact about the number inside it.

### Per-server MCP context cost

The design was one row per MCP server: status icon, name, error, tool count, and
the context tokens its schemas consume with its share — the number that would
make somebody trim an allow-list.

**It cannot be read from the client.** What opencode exposes about MCP is
`{name, status, error}` and nothing else: `api.state.mcp()` and the `/mcp` route
return exactly that, and the only other MCP routes are auth, connect and
disconnect. The one route that serves JSON schemas —
`/experimental/tool?provider&model`, 14 tools and 22,057 bytes on this machine —
lists built-in and *plugin* tools only. Measured: of those 14, the two that look
like servers, `claude_mem_search` and `headroom_retrieve`, come from opencode
plugins, while the servers themselves advertise 14 and 3 tools. MCP tool schemas
do reach the model — this project's history has 271 calls to `agent-device_find`
and 41 to `serena_activate_project`, so they are named `<server>_<tool>`
somewhere in that payload — but no client surface publishes them, and nothing
stores a decomposition: `session_context_epoch` is empty and a message's tokens
are `{total, input, output, reasoning, cache}`.

The denominator was never the problem: the provider reports `limit.context` per
model, so a share of the whole context window is one division away. The numerator
is what does not exist.

Atenea could measure the servers itself by connecting to them, and that is
exactly why this is dropped rather than deferred: it would spawn second copies of
every server and report the cost of *Atenea's* context, not the client's. A
section that could only fill in Atenea's own row — one server of three, the one
whose surface this project already controls — is a line, not a section.

**Condition to revisit:** opencode publishes MCP tools with their schemas, or
records a per-server share of the context it sends. Either makes the numerator
real and the section honest.

### RTK usage for the project

The design was a panel of what rtk saves on this project. The state is real:
`~/.local/share/rtk/history.db`, 60 MB, WAL, `commands(timestamp, original_cmd,
rtk_cmd, input_tokens, output_tokens, saved_tokens, savings_pct, exec_time_ms,
project_path)`, 21,330 rows across 263 projects. Scoped to this repository by
path prefix: 3,606 commands, 66.3M input tokens, 59.8M saved, 120 minutes of
execution.

**It is not this client's data.** rtk has exactly one automatic wiring on this
machine — `PreToolUse[Bash]` → `rtk hook claude`, in Claude Code's settings — and
no hook or plugin in opencode or codex references it at all. A panel in
opencode's sidebar showing those savings would read as "this is what I am saving
here", and it is another client's work. The database cannot correct the
impression either: it has `project_path` and `timestamp`, and no client, session
or account dimension anywhere.

Two traps recorded for whoever builds this later. The percentage has two bases
that disagree by a factor of three on the same rows: token-weighted savings are
90.2%, the average of the per-row percentages is 26.7%, and the honest headline
is the weighted one. And the project filter must be a path prefix, the way rtk's
own `--project` flag does it: `LIKE '%atenea%'` also matches
`~/.local/state/atenea`, which is not this project.

**Condition to revisit:** rtk is hooked into opencode, so that the rows describe
the client the panel is drawn in.

### A rate-limit section, reduced to a line that is usually absent

**Built on 2026-08-11 — see the section above.** Kept as a heading rather than
deleted, because this page has been cited by date and a heading that vanishes turns
an old citation into a dead reference. The condition was met by a source nobody had
looked for, not by a provider changing anything.

### Discoveries are the top of the document, not facts worth outliving the task

**Measured 2026-08-14, not fixed.** The `history` context level serves past runs
of the same agent type their predecessors' discoveries, and the mechanism works:
run 5 was handed run 4's three notes, from the trace database, unprompted. What
it was handed is junk.

`firstSentences` in `internal/agent/planner/planner.go` splits a report on `. `
and keeps the first three fragments under `contract.MaxDiscoveryLength`. On a
markdown answer — which is what a model returns — that yields the heading and
the first line under it:

```
repository | ## Where a row is OPENED
repository | **`internal/trace/trace.go` - `func (s *Store) Begin(...)` (line 237).**
repository | Single `INSERT INTO agent_trace` of id, parent_id, type_name, ...
```

The design's own words are "a short fact worth outliving its task". What is
persisted is **the top of the document**: position, not significance. A second
run reading that learns where the previous answer started, which it would have
found anyway.

Two things make this harder than it looks, and are why it is here rather than
patched. Selecting the *interesting* sentences from a report is a judgement, and
the honest implementations of it are a second model call (priced, on every run
that answers) or a declared `discovered` field the agent fills in itself (free,
but it moves the judgement to the agent and every agent type would have to
declare it). Picking between those is a design decision with a cost attached,
not a bug fix.

**How you would know it was finished:** two consecutive runs of the same type,
where the second's served history contains a claim the first *found* — a
file:line, a count, a refuted assumption — and no markdown heading anywhere in
the set.

## A second code graph, built, declared, and still unjustified — 2026-08-14

Ladygraph — a code knowledge graph with its own persistent store — was cloned,
source-built and evaluated on this machine for one hour, and then, later the same
day, built into Atenea as a structural provider. Both halves of that sentence are
true and neither cancels the other: **the plumbing is finished and the
justification is not.** Four capabilities are declared, an adapter answers them
over stdio, and every one was driven end to end against a live graph. The
condition that would make them worth paying for — recorded below, unchanged from
the day it was written — has still not been met.

This entry stays on this page for that reason, and it is **not a to-do**. There
is nothing left to implement. It is a build waiting for the question it was built
to answer, which is a different and more uncomfortable thing to have on the
shelf. The three grounds the original answer rested on are kept below, because
two of them are still true.

**The cost gate answered no.** *Measured, not felt.* Declared narrow — only the
four answers no attached provider gives, never mirroring the symbol family or
`code.impact` — the surface adds **736 tokens** (cl100k_base, `~/bin/fixed-cost`)
to every request from all three fixed-prefix clients: omp 12,130 → 12,866
(+6.07%), opencode +6.49%, codex +8.63%. claude-code defers MCP schemas, so it
pays nothing fixed. That is a permanent per-request tax on three clients, and this
week it would have bought four answers not once needed. The one that justifies it
— cross-repository consumers, the single answer neither a language server nor
the graph backend's path-shape edges can give — matters for a question not yet
asked. Mirroring the seven serena/graph duplicates instead would have
cost +1,688; the narrow shape refuses that, but narrow-and-unused is still 736
tokens of nothing, forever.

**It was not a declaration, and that is precisely what got built.** The three
existing providers are one HTTP-MCP server (serena) and two per-call CLIs (omp,
the legacy graph backend). Ladygraph's `serve` is a persistent **stdio** MCP server — a
third transport shape Atenea did not have. That shape now exists:
`internal/mcpstdio` (one session per child, `initialize` then `tools/call` over
its stdin and stdout), `supervisor.TransportStdio` carrying it over the same
restart-limit, on-demand and idle-reaper machinery the HTTP process blocks
already used, and `internal/adapter/ladygraph` on top of both. This paragraph
predicted a build rather than a config line, and it was right. That was a reason
to hesitate, never a reason it could not be done.

**Then it was measured, which is the third ground.** The graph was built by hand —
`ladygraph init` + `index --full` over taxiprime, no Atenea in the middle — and
asked the real question over `serve`'s stdio. A full index of four disjoint
repositories takes **4.07 s**, peak RSS **~794 MB**, and publishes 3,068 symbols /
14,753 nodes / 26,194 edges into a 22 MB `graph.db`. The state directory reports
544 MB, of which **513 MB is a fixed LadybugDB `space-reserve` preallocation** —
not corpus-proportional, and not a number to quote as the cost of this corpus.

**The four repositories are genuinely decoupled, so the answer is empty and the
empty answer is right.** `find_cross_repo_consumers` on backend's real schema
symbols (`reservas`, `users`, `drivers`) returns `total: 0` for every one.
Verified against the source rather than trusted: `backend` (`taxiprime-backend`,
Fastify) and `frontend` (`@taxiprime/frontend`, React + react-query) declare no
dependency on each other and no workspace joins them — they meet over HTTP, not
over shared types. `app` is Dart. `scrapper` is a standalone Go module. There is
nothing for a cross-repository query to find, and the tool says so.

**The one real cross-boundary edge is root's, and it is invisible for two
structural reasons — neither of them a weakness in the tool.** The only importers
of `backend/src` anywhere in the tree are the six files in `migration/scripts/`,
which pull `users`, `drivers`, `reservas` and `socialAccounts` from
`backend/src/db/schema` (every other hit across `*.ts,tsx,dart,go` is `.md`
prose). That edge cannot be indexed here because:

1. **Nesting.** `root` is `new-app`, which contains `backend/`, `frontend/` and
   `app/`. `init` accepts all four; `index --full` then fails fast with
   `nested repositories ("…/new-app" contains "…/new-app/backend")`. A parent and
   its children cannot be peer repositories, so the four-repository shape that was
   asked for is not expressible at all.
2. **No project.** `migration/` has a named `package.json` but no `tsconfig`, so
   its `ProjectPath` is empty and `internal/indexer/full.go:449` skips it by
   design — Ladygraph refuses to guess the root `tsconfig` (ADR 0009). The warning
   it prints, `declares no package`, is loose wording for "declares no project".

The resolver itself is not the problem: `ts-worker`'s `cross-repository` suite
passes, five tests. The capability works; this codebase never presents a case it
can index.

**Dart does not show up at all: 231 files, zero symbols, a registry entry with no
coverage.** Ladygraph is a graph for Go and TypeScript, with a Rust loader.
Registering `app` succeeds, indexing it reports `app declares no package, so it
contributes nothing`, and the published graph holds not one Dart symbol. For
taxiprime — whose client *is* the Flutter app — that single line decides the
question on its own: the repository this provider would most be asked about is the
one it cannot read.

**The loud-failure requirement — built, and verified by inverting it.** The
empty-graph trap is the one that bites: a first `serve` with no config writes an
empty config and an empty graph, and answers every query with nothing —
successfully, exit 0. The adapter inverts it. Every implementation carries
`requires_index = true`; detection maps an absent graph, an empty graph, or a
repository missing from the published snapshot to not-indexed; and the call path
returns `FailureUnavailable` instead of an empty success.

Proven by causing the trap rather than reasoning about it: the same binary, run
with an isolated `HOME`, which is exactly what makes `serve` scaffold an empty
graph. All four capabilities fail with `unavailable: ladygraph has no published
graph to answer from (status "empty")`, and `detect` reports every repository
`not ready` with that same sentence. A repository merely absent from a healthy
graph reads differently and correctly — `ladygraph's published graph does not
include repository /home/tutitoos/Desktop/atenea` — and the funnel drops the
implementation by name rather than answering empty. Down provider, not a
repository with no matches, as required.

### The declaration, as shipped

The draft that used to sit here — some 250 lines of TOML — has been deleted on
purpose. It is real now, in `internal/config/default.toml`, and a copy of a
config file kept in prose beside it is a fixture of one's own belief: it drifts,
and the drift is invisible because both copies look authoritative. Read the file.

Four `[[capability]]` blocks — `symbol.consumers`, `symbol.get`,
`symbol.unresolved`, `graph.status` — and four `[[implementation]]` blocks —
`ladygraph.cross_repo_consumers`, `ladygraph.get`,
`ladygraph.unresolved_references`, `ladygraph.status`, every one
`requires_index = true` — plus `[orchestrator.ladygraph]`. The orchestrator's
compiled card in `internal/orchestrator/orchestrator.go` names all four; without
that the catalog, the funnel and a live runner can all be ready and the ask is
still refused with `agent orchestrator may not ask for …`.

**Shipped unattached, deliberately.** `runners = ["omp"]` is unchanged and
`[orchestrator.ladygraph.process]` ships commented out, the same way serena's own
process block does, because `command` is an absolute path that exists on this
machine and no other. Uncommenting it is a per-machine decision, not a compiled
default.

**What the draft got wrong, corrected against the wire.** Each of these was
written from the design, read as evidence, and refuted by asking the server:
`graph_status` takes no arguments at all, so the capability declares no inputs;
`get_unresolved_references` has no `path_prefix`, and its rows carry a byte
`offset` and never a line; `find_cross_repo_consumers` records name their class
in `category` — `exact_symbol`, `package`, `candidate`, `unresolved`, not
`exact` — and a `package` row carries no file at all, so `path` had to become
optional; and `get_file_outline` returns an object whose declarations hang off
`symbols`, alone among these tools in not returning a row list.

**The contract ruling worth keeping.** `symbol.consumers` is position-first
(`file`, `line`, `column`) and never takes a `stable_key`, even though
`find_cross_repo_consumers` requires one. A capability whose required input only
one provider can mint is not a capability, it is a tool passthrough: no language
server and no path-shape matcher can produce a Ladygraph stable key, so declaring
it would make the capability permanently unimplementable by anyone else and
unreachable for the caller who has a file and a line, which is what callers
actually hold. The adapter pays the resolution instead — `get_file_outline`,
innermost containing declaration wins, `name` breaks ties, and an ambiguity it
resolved is reported as a discovery rather than hidden. `symbol.get` is the one
deliberate exception, because it is *about* stable-key identity. One exception is
considered; two would be a leak.

**Both open decisions, closed.** `graph.status` kept its own namespace rather
than folding into `catalog.graph_status`. Transport is the shared persistent
stdio server, one for the machine, as drafted — per-call spawning was rejected
because it pays a graph open on every request.

**Adapter:** `internal/adapter/ladygraph`, one `initialize` then a `tools/call`
per request over the child's stdio, mapping `symbol.consumers →
find_cross_repo_consumers`, `symbol.get → get_symbol`, `symbol.unresolved →
get_unresolved_references`, `graph.status → graph_status`. The binary is the
checkout-independent build at
`~/.local/opt/ladygraph-eval/v0.5.1/bin/ladygraph`.

**One prerequisite that fails silently, learned the hard way.** That install is
Go-only: it ships no `ladygraph-ts-worker`, and `version --json` reports
`"node": null, "typescript": null` to say so. Indexing any TypeScript repository
without a worker on `PATH` dies with `decode TypeScript facts: unexpected end of
JSON input` — not "worker missing". A worker must dispatch `facts …` to
`ts-worker/dist/facts-cli.js` and everything else to `dist/index.js`, which is a
`hello`-only probe; pointing the whole command at `index.js` produces exactly the
same misleading JSON error. Any launcher written for it must not hardcode a path
into `~/Desktop/ladygraph`, which is what makes the install checkout-independent
in the first place.

**What it costs today: nothing — and the baseline no longer describes it.**
`~/fixed-cost-ladygraph-base.json` was measured against the draft's schemas with
the provider attached, and predicted omp +736. The shipped schemas are leaner,
the provider ships unattached, and this machine's own
`~/.config/atenea/atenea.toml` is a complete catalog that does not name the four:
measured 2026-08-14, they are absent from the live `tools/list` entirely. The tax
is not being paid. Re-measure the day the process block is uncommented and the
machine's config adopts them — and re-measure rather than quoting the old number,
which is the entire reason the file exists. A `--compare` run against that
baseline on the same day showed +342 across three clients from `workflow.create`
and `workflow.launch`, unrelated work landing in the same tree: a baseline names
a moment, not a cause.

**The condition, unchanged and still unmet.** Nothing above changes it, and the
build does not earn it. It stays what it was: `migration/` becomes a registerable
TypeScript project — a named `package.json` with a `tsconfig` that covers it — so
the one real cross-boundary edge in this codebase becomes expressible; or two of
these repositories actually share symbols, through a published package or a
shared types workspace, anything but HTTP, so there is a second edge to find.
Both are file-level facts, checkable in a second: either the `tsconfig` exists or
it does not, either two repositories share an import or they do not.

Until one of them is true on disk, there is no cross-repository question this
provider could answer that grep cannot. The difference from the version of this
entry written that morning is only that the answer is now one uncommented block
away instead of a build away — and that is not a reason to uncomment it.

## A floor that guards the door and not the room — 2026-08-14

`atenea workflow create` now refuses a plan whose steps are funded below either
of two measured requirements: the floor, what starting a turn costs before any
file is read, and the rescuable threshold added 2026-08-15 -- the point past
which a step's own read allowance outweighs the weight of its first assistant
event. Both checks live in `Create` and nowhere else, which is a deliberate
limit and a real hole: a proposal approved mid-run (`Store.Ask` / `Engine.grow`)
can still add a step funded below either requirement, and it will die exactly
the way the twelve steps of 2026-08-14 died to the first, or the thirteen the
following night died to the second.

It was left open because refusing there is a different decision, not the same
one moved. At create time the refusal costs nothing — no run exists, the person
edits a file and tries again. Mid-run the same refusal kills a live expansion of
a graph that is already spending, and the honest options (decline the expansion
and continue, or stop the run) are a choice about somebody's money that should
be made deliberately rather than inherited from a check written for a different
moment.

**The condition:** an expansion is observed to add a step below the floor on a
real run. Until then this is a hole with a receipt, not a bug — the door is the
path every plan takes, and no measured failure has yet come through the window.

## The planner writes two things it cannot check — 2026-08-15

Both found on the same eighteen-step plan, and neither is a money problem: a
larger grant and a measured floor leave both exactly where they are.

**It assigns work to an agent that cannot do it.** Six of the eighteen steps
were `reviewer`, and each one reads like this:

> Open `src/modules/admin/admin.routes.ts` yourself and check the subject's
> route inventory. Verify: that the stated route count matches the number of
> route declarations actually in the file; that no route present in the file is
> missing from the answer; that each route's cited line really declares that
> route.

The built-in `reviewer` cannot do any of it. It is mechanical by design — see
its own doc: "a review that cannot be checked is an opinion" — and what it
checks is `path`, `bytes`, `lines` and `content` out of the subject's result,
which is the **filereader's** result schema. Handed an explore answer, which
carries `summary` and `findings`, it returns "the result names no path, so
there is nothing to re-read". Six steps were written for an agent that does not
exist, by a planner reading a menu that told it the type reviews answers and
did not tell it which answers it can read. The gate never fired, so this cost
nothing on the night and would have cost a third of the graph on any night it
did.

**It funds types that call no model.** `reviewer` and `filereader` spend
nothing — no turn, no tokens, `max_tokens = 1` in the declaration — and the six
reviewer steps were allocated $0.78 of a $3.50 grant, **22.7%**, at $0.12 to
$0.18 each. The planner is dividing a grant by step count weighted by its own
sense of size, with no notion that some types are free. Whatever derived shares
ends up computing, the first term is zero for a type that calls no model, and
that is knowable from the declaration without measuring anything.

**The condition, for both:** the agent menu the planner is handed carries what
a type can be given and what it costs — the result schema it consumes, and
whether it spends. Today the menu carries a one-line summary and the planner
guesses the rest. Neither defect is fixable downstream: `workflow create` can
refuse a plan that is underfunded because a floor is a number it can compare,
and it cannot refuse a plan that is misassigned, because nothing has written
down what a fit would be.

## Health and indexed_by are the same unanswered question, asked twice — 2026-08-15

`filterHealth` (`internal/selector/selector.go:323`) drops any implementation whose
`impl.Health.Usable()` is false before the funnel tries it. `contract.Health.Usable()`
(`pkg/contract/implementation.go:316`) is `State != HealthDown` — nothing else. `Health` is
written in exactly one production call site, `internal/orchestrator/orchestrator.go:1140-1144`:
when a real call fails with `FailureUnavailable`, the orchestrator writes `HealthDown` for that
`(repository, implementation)` pair, carrying the failure's own text as `Reason`. Nothing else
in the shipped path ever writes `HealthAlive` back — the only call that clears a `HealthDown`
lives in a test (`core_test.go:472`). `Health` itself carries no timestamp: once down, an entry
has no way to become old, only to be overwritten by another `SetHealth` call that never comes.
The only thing that clears it is a full process restart, which throws the whole map away and
starts every implementation back at the zero-value `HealthUnknown` — usable, per the same
`Usable()`. Measured 2026-08-15: three `code.impact` calls against `taxiprime`, varying
`baseline` and `scope`, all returned `every implementation of code.impact is down for
repository taxiprime`, while `symbol.calls` against the same repository in the same minute
answered cleanly off the same provider. `systemctl --user restart atenea` cleared it on the
next call, because restart is the only invalidation this mechanism has.

`indexed_by` (`atenea.toml`, read across `config.go`, `registry.go`, `selector.go`, `core.go`)
is the identical shape from the write side instead of the read side: a claim about whether a
provider serves a repository, set once — by hand, in a settings file, or in memory by `atenea
detect` — and never rechecked against the thing it claims. `detect` does not call `SetHealth`;
it prints and exits.

These are not two bugs. They are one missing operation — *re-probe a provider this process has
already formed an opinion about* — absent from two call sites that both currently treat
"observed once" as "true forever," in opposite directions: `Health` latches down and never
un-latches; `indexed_by` latches up (whatever the file says) and never re-confirms. One design
has to cover both, or the second one just gets rebuilt as a copy of the first with the same gap.

**A separate fault, not fixed by any of this.** Measured the same day: restoring `indexed_by =
[..., "graph-backend"]` across six repositories was done by hand, from a backup, reconstructing
what the value had been before an earlier retirement — and a fresh `detect` per repository, run
afterward, found the claim true for exactly one of six. That is not the design gap above. Nothing
about the missing reconciliation mechanism made those five values wrong; running `detect` before
writing them, which cost nothing and needed no new code, would have caught it the same day it
happened. The design gap explains why a wrong value, once written, sits unnoticed instead of
self-correcting. It does not explain why the value was wrong in the first place — that was a step
skipped, not a feature missing, and building the reconciliation mechanism below would not by
itself stop the next person from writing six confident, unchecked declarations by hand again.

**The shape.** `contract.Health` gains one field: `ObservedAt time.Time`. Nothing else about it
changes — `Usable()` stays a state check, not a time check, so this stays additive. `indexed_by`
stops being a separate value read straight out of config at load time. It becomes the initial
`Health` write for that `(repository, provider)` pair — `HealthAlive` if declared, `HealthUnknown`
if not declared — recorded with `ObservedAt` at config load. From that point on, `indexed_by` and
capability health are the same map, read the same way, and a repository's config no longer makes
a claim the runtime can't see next to the runtime's own answer.

**Invalidation.** Three triggers, not one, because the failure modes are different shapes:
- *Time.* `filterHealth` compares `ObservedAt` against a staleness ceiling before trusting a
  cached verdict. Past it, treat the entry as `HealthUnknown` — usable, not down, not
  presumed-alive either — and let the funnel's own attempt be the re-probe: success writes
  `HealthAlive` with a fresh `ObservedAt` through the existing orchestrator path, failure writes
  `HealthDown` through the same path that already exists today. No new probing loop, no
  background goroutine — the ceiling just stops a two-week-old verdict from being read as this
  morning's. The right ceiling is not measured yet; this page is explicitly not the place to
  assert one unmeasured.
- *Explicit.* `atenea detect --repo X` calls `SetHealth` instead of only printing. The
  correction a person runs by hand starts surviving the next restart, which closes the exact gap
  measured this session: a `detect`-confirmed fact currently dies at the next `systemctl --user
  restart atenea`, the same restart that's the only cure for a stale `HealthDown`.
- *Restart.* Unchanged, and worth keeping: a fresh process should still start every provider at
  `HealthUnknown` rather than trusting whatever the last process believed, because the last
  process's belief is exactly what this whole entry is about not trusting past its shelf life.

**What status shows.** `atenea detect --repo X` prints, today, one thing per provider: whether
it looks reachable right now. Extend it to print what it currently hides — the state this
process already has on file *before* the fresh probe, and how old it was — declared, observed
state, observed age, last failure text if any. Today's five wrong-but-silent `indexed_by`
entries would have shown as `graph-backend: declared alive, health unknown (never probed)` —
visibly a claim without a confirmation, not a fact.

**Done when:** a `Health` entry recorded before this ships is never read as current after it —
concretely, an implementation latched `HealthDown` in a running process gets a real retry on the
first call after the staleness ceiling passes, with no restart in between, and `atenea detect`'s
own result for a repository survives a restart of the process that ran it.

Not built. This is one design, not implemented, because building it inside the same session that
found both halves of the gap is exactly the haste this page exists to refuse.

## Silence and full coverage are the same answer — 2026-08-15

The ceiling failures on this page fail loudly: `status incomplete`, a reason recorded, a cost
that does not match an answer. This one is the opposite, and it is worse for exactly that
reason. Of nineteen steps in `wf1786790289244-1`, eighteen came back `incomplete` and one came
back `ok`. The `ok` is the false one.

`auth-mod`, a `reader`, share $0.22, spent $0.19, recorded `status ok`, `verdict ok`,
`completeness` NULL. Its own answer says:

> **summary:** I cannot describe this project, because I was unable to locate it before being
> asked to stop.
>
> **findings:** Nothing was read, so I have no sourced findings. Every item the commission asks
> for is undetermined, and I am naming them individually so the gaps are not mistaken for
> absences [...]

The prose is honest. It is the record that lies, and it does so because two different things
arrive at the same place.

**The answer is missing half its required fields, and nothing noticed.** `exploreSchema`
(`internal/agent/planner/prompt.go:307`) declares `required: [summary, findings, completeness,
stopped_at]`. The recorded result JSON has exactly two keys: `findings` and `summary`. Both
fields that describe how much of the objective was covered are absent from an answer whose own
summary says it covered none of it. `required` in that map is sent to the provider and never
checked locally — the answer unmarshals into a struct (`model.go:1348`) where an absent field is
the zero value, so a two-field reply and a four-field reply are the same parse.

**Absent is then read as fine.** `claim.reported()` (`model.go:1403`) maps a nil completeness to
nil — deliberately: the field's own doc (`model.go:455-464`) says nil means "a single-shot turn,
or a schema the caller declared without the property", and dropping an unreadable figure rather
than repairing it into one is the right call. But that doc enumerates two ways to be unclaimed
and this is a third it does not consider: the schema *did* declare the property, *did* mark it
required, and the model ignored it. `coverage()` (`internal/agent/planner/planner.go:574`) then
collapses the distinction for good — `Completeness == nil || *Completeness >= 1` takes the same
branch, returning no partial marker and no refusal. **"I did not say" and "I covered everything"
are one code path.**

**Silence is treated better than honesty.** A model that answers 0.4 with no `stopped_at` is
refused outright as unauditable (`planner.go:577`). A model that answers 0.4 *with* a
`stopped_at` is recorded as a partial and reads as such downstream. A model that answers nothing
at all clears both, and lands `StatusOK`. The gradient runs the wrong way: the more the model
says about its own shortfall, the more ways it can be caught, and saying nothing is the strictly
safest option available to it. Note it was asked directly — `finalizeMessage` says *"Set
completeness to the fraction covered and stopped_at to what you did not reach"* — and returned
neither.

**It clears the strictest gate there is.** `outcome` (`internal/workflow/engine.go:1046`) maps
`VerdictOK` to `StatusOK`. `Requirement.satisfiedBy` (`graph.go:107`) draws the only line the
graph has: `OnAnswered` clears on ok, failed or incomplete alike, while `OnOK` clears on
`StatusOK` and nothing else. So this answer does not merely pass the lenient gate a partial
would also have passed — it is the one result in nineteen that satisfies `OnOK`, the requirement
that exists precisely to make a downstream step wait for work that *succeeded*.

The one thing that caught it did so by accident, for an unrelated reason: `audit-auth-mod`
returned *"the result names no path, so there is nothing to re-read"* and went `incomplete` —
that is the reviewer-cannot-read-an-explore-answer defect logged above, not a detection of
anything. Fix that defect and the reviewer would have re-read the file, found no inventory to
check against, and reported the failure one layer further in — still not as a false claim.

**The field meant to make this auditable is empty almost everywhere.** `completeness` is non-NULL
in **2 of 210** `workflow_step` rows in `traces.db`, across every workflow ever run on this
machine. A column populated 1% of the time is not a record of coverage; it is a record of the two
occasions an answer happened to carry it. On the other 208 the system has been reading silence,
and reading it as fine.

**The condition.** This is a shape the instruments page has recorded twice already — an empty
list read as an empty field, and a store that cannot tell failure from empty — arriving now in
the agent protocol: a sentinel meaning *no information* shares a branch with one meaning *good
news*. Two fixes are separable and neither needs a model call:

- **Enforce `required` where the answer is parsed, since the provider does not.** Four fields
  were demanded and two arrived. `json.Unmarshal` into a struct cannot see that; a check against
  the same schema map already in hand can, and a reply missing a required field is a refusal in
  the same class as "the model's answer is not in the shape it was given" (`planner.go:503`),
  which already exists for a malformed plan.
- **Split unclaimed from complete in `coverage()`.** They are one branch today. An answer that
  never stated its coverage is not auditable as a whole answer and should not clear `OnOK` on the
  strength of a field nobody filled in.

**Done when:** an answer omitting `completeness` cannot be recorded as `StatusOK`, and the count
of steps carrying the field stops being 1%.

Not built. `reported()`, `coverage()` and `outcome` sit on the path every step's result takes,
and the last of them decides what every `on = "answered"` and `on = "ok"` edge in every graph
waits for; changing what "answered" means is not a change to make in the session that noticed
it. Recorded here with the correction that produced it: this
entry was first drafted claiming the model asserted `completeness: 1` and was rewarded for it.
That was inference from a NULL column, and the raw result JSON falsifies it — the model asserted
nothing. The bug is not a lie told by a model; it is a silence the system reads as a yes.

**Closed 2026-08-15**, on the first of the two fixes, which turned out to be enough for the
Done-when. `coverage()` refuses an answer that states no coverage instead of reading it as
whole, and keeps the claim on the record when it is stated — including when it is 1, which is
what stops a NULL from meaning two things. `reported()` clamps an over-claim to 1 rather than
dropping it to an absence the new rule would refuse. Verified against the real binary with a
shimmed CLI, both arms, `$0.00`: the arm that states `completeness` records `1` and verdict
`ok`; the arm that omits it is `failed`, `invalid_input`, with the charge kept.

What did **not** change, deliberately: `Requirement.satisfiedBy` still reads `Status` alone, so
`on = "answered"` and `on = "ok"` are unchanged — the refusal moves an unauditable answer out of
`StatusOK` before any edge sees it, which is why neither word had to be redefined.

### The open question: is "whole" a gate or a judgement?

A partial that names where it stopped clears both `answered` and `ok`, and there is no word for
*only if whole*. The obvious move is a third one — `on = "whole"` — and it sounds right until
somebody asks what a reviewer should then do with a partial.

**Auditing a partial is legitimate work.** "Here are 60 of 107 routes, and here is where I
stopped" is checkable: every claim in it has a file and a line, and the reviewer's finding is
about those sixty. A gate that skips it means the only thing ever audited is work that needed
auditing least — the answers that already claim to be whole — which inverts the reason
`OnAnswered` is the default in the first place (see `Requirement`'s own doc: a reviewer that
only sees successes audits the half that needs it least).

So the question is not "which word is missing" but **where the sufficiency judgement belongs**.
Two readings, and they are not the same mechanism:

- **On the edge.** `on = "whole"` is a gate: cheap, declarative, visible in the graph file, and
  it decides on a number the upstream step asserted about itself. It cannot know whether 0.6 is
  enough for *this* criterion, because the criterion is prose and the gate reads a float.
- **In the reviewer.** "This answer covers 60 of 107 routes and the criterion asks for a
  complete inventory, so it is insufficient" is a verdict a reviewer can reach and cite. It
  costs a model turn, it is auditable, and it is the same shape as every other judgement in
  this system: an agent reads the evidence and says what it means.

The second is probably right, and it implies the missing piece is not a keyword but a
**reviewer that is told its subject's completeness and stopped_at** — which it is not today: the
subject card carries the result, not the claim about the result. That is a smaller change than a
new edge semantics and it needs no graph-language decision at all.

**Not decided.** Recorded as a question because the cheap answer (add the word) forecloses the
better one, and because nothing has yet been measured about how often a partial is actually
sufficient for the criterion that asked for it.

What *is* measured, as of 2026-08-16, is the distribution, and it is not the one this entry
was written against. `n = 8` partials on this machine of 34 rows carrying a completeness at
all -- **six of them readers**, not explore steps, and four at or below 0.55:

| coverage | step | type |
|---|---|---|
| 0.10 | `booking-b` | reader |
| 0.15 | `admin-usuarios` | reader |
| 0.30 | `users-mod` | reader |
| 0.55 | `census` | reader |
| 0.80 | `explore` | explore |
| 0.85 | `explore` | explore |
| 0.90 | `devops-internal` | reader |
| 0.95 | `users-mod` | reader |

The two explore rows at 0.80 and 0.85 are the original `n = 2`; every reader partial arrived
after this was written. A rule that treats a partial as nearly-whole was plausible at `n = 2`
with both above 0.8 and is not plausible here: the median is `0.55`, and `0.10` is a step that
reached none of its objective and still recorded `ok`.

`booking-b` at `0.10` is not a count correction, it is the completeness question above with a
worse example than the one that opened it. `auth-mod` recorded no `completeness` at all, so the
bug was legible as a missing field -- absence read as fine. `booking-b` recorded `0.10`, an
actual claim of near-total failure, and `verdict ok` still followed it: the defect is not that
the field goes missing, it is that no value written into it, however small, changes what `ok`
means. A reviewer told the number would catch this one on sight; nothing today is told the
number at all.

## The bill is not a function of the tokens we count — 2026-08-16

Two floor probes, eleven seconds apart, same repository, same model, same CLI (`2.1.232`), both
cold, doing **identical** work — one tool call against a pattern chosen to match nothing, then
two characters of answer. `input_tokens 2`, `output_tokens 17` on both. The only difference is
the agent surface, which changes the tool definitions in the prefix:

| surface | prefix | first call | start | receipt | per start token |
|---|---|---|---|---|---|
| `explore` (MCP catalog + Read/Glob) | 25,704 | 25,778 | 51,482 | `$0.2724` | `5.29e-6` |
| `reader` (Read/Glob) | 4,532 | 40,061 | 44,593 | `$0.4477` | `1.00e-5` |

**`reader` paid 64% more for 13% fewer tokens.** Both receipts are the provider's, not a
derivation. So the effective rate per token the client counts differs by `1.9x` between two
surfaces of one model in the same minute, and at least one of the two derived rates is not a
price of anything.

This is the same term seen from a different angle on 2026-08-15, when `cache_read` came back
pinned at exactly `40,227` on turn 2 across five runs against different objectives, different
files and different nonces — a quantity that does not vary with the work cannot be a function of
the work. Counting more carefully did not close it: tonight's `first_call_tokens` is measured off
the receipt's own per-message counts, and it still does not predict the bill.

**What this does *not* touch.** The admission threshold is priced in tokens against a fixed
`tokensPerUSD` constant — `allowance.MinShareUSD(WarmStartWeight)` never reads `usd_per_token`.
Predicted from the token counts alone, `explore` needs `$0.07` and `reader` `$0.06`; the table
prints exactly those. A wrong per-token rate cannot move what a step is refused for. It moves
the `WARM USD` and `COLD USD` columns, which are reported, and any grant derived from them.

**Done when:** one of two things is true. Either the gap is named — some part of the request is
billed that this project does not count, and `Weigh` gets a term for it with a receipt behind
it — or the derived rate stops being reported as a price. Until then, a per-step budget should
be built from a receipt for the shape of step being budgeted, never from `WARM USD × steps`.

**Not built,** and not cheap to close: it needs the provider's own accounting of a request whose
every client-side count is known, which is a different instrument from anything here. What is
built is the refusal that no longer depends on it.

## The grant is not a bound, and `budget_usd` says otherwise - 2026-08-16

Measured on the 23-step run in the thirty-second instrument. The overshoots were not small and
were not noise:

| step | share | spent | |
|---|---|---|---|
| `auth-mod` | `$0.09` | `$0.41` | **4.6x** |
| `spine` | `$0.10` | `$0.41` | **4.1x** |
| `sweep` | `$0.11` | `$0.28` | 2.5x |
| the fifteen that died at the ceiling | `$2.26` | **`$3.68`** | 163% |
| the whole run | `$5.22` granted | **`$5.88`** | 112.6%, `$-0.66` left |

**What bounds a turn today.** Exactly one thing reaches the provider: `--max-budget-usd`, from
`model.Request.BudgetUSD`. The CLI checks it *between messages*, so what it bounds is the decision
to start another message, never the cost of the one already in flight. Total spend is therefore
`ceiling + cost of one in-flight message`, and nothing atenea passes constrains the second term.
`Request` carries `Role`, `Prompt`, `Schema`, `Dir`, `BudgetUSD`, `ReadTokens`, `Timeout` and
`Tools`. There is no token cap among them.

A per-turn token cap is the only shape of bound that could work here, since money is checked
between messages and never within one. `Limits.MaxTokens` is exactly that shape and enforces
nothing; it is a separate failure and has its own entry below.

**Why the read allowance cannot cover the gap.** `ReadTokens` is atenea's own nudge, and it works
on weighed usage accumulated from assistant events. On the fifteen steps that died at the ceiling
the record holds a dollar figure and **zero tokens** -- `spine`, `auth-mod` and `public-mod` each
show `$0.41` with input, output, cache-read and cache-write all `0`. A turn stopped at its budget
reports `total_cost_usd` and no usage, which `claudecode`'s envelope doc had already measured. So
the one mechanism that could interrupt a runaway turn is blind on precisely the turns that run
away. It bounds *reading*, and this money went to *searching*: globs over a tree with no matches,
which pay for a tool result and the model's next thought and read no file at all.

**The bound that does exist, and why it is not a budget.** Since the check is between messages,
one turn's worst case is one message's worst case. Priced with this project's own constants --
`allowance.tokensPerUSD` at 83,000 input-equivalent tokens to the dollar, output weighted 5x --
the 64,000-output-token request the CLI is observed to make is 320,000 input-equivalent tokens,
about **`$3.86`**, before any input. Against the smallest share of `$0.09` that is a **43x**
bound. The honest statement is not "unbounded"; it is "bounded at a figure nobody chose, two
orders of magnitude above the shares in use".

**What this makes `budget_usd`.** A forecast, priced from receipts, with per-step error observed
at up to 4.6x and aggregate error of 12.6% on this run. It is a good forecast and it is not an
authorization ceiling, which is what `Permission.BudgetUSD`'s own doc called it until this entry:
"a spending ceiling is an authorization the user granted". A user who granted `$5.22` was charged
`$5.88` and was never asked about the rest.

**The cheap half is done - 2026-08-16.** Three docs now say what the number is rather than what it
was hoped to be: `Permission.BudgetUSD` ("IT IS NOT A CEILING", with the measured error and the
one-message bound), `Limits` and its `Validate` (required and validated is not honoured), and
`atenea workflow`'s help, which a person reads before granting: budget for the work, then expect a
step to exceed its share by up to one turn and the run to exceed its grant by the sum. Nothing
about behaviour changed. What changed is that the promise now matches the mechanism.

**Done when** something enforces a bound: a provider that aborts mid-turn, or a per-turn token cap
actually passed and honoured. At that point the token cap becomes the real lever and the dollar
figure stays the forecast it always was.

**Not decided:** which, and deliberately left open. Recorded because the run that found this was
budgeted on the assumption that a share was a bound, and every number in that plan inherited the
assumption.

## A required ceiling that binds nobody: `Limits.MaxTokens` - 2026-08-16

Filed apart from the entry above on purpose. That one is a bound checked at the wrong moment --
real, enforced by the provider, just enforced between messages instead of within one. This one is
a bound that **does not exist while looking like it does**, which is a different failure and the
more misleading of the two.

`pkg/contract` declared it "the token ceiling for the whole run, input and output together".
`Limits.Validate` refuses a run whose `MaxTokens` is not positive -- and that refusal is
deliberate, written so that "nobody decided" cannot pass for "no limit". Every signal the type
gives a reader says the number is load-bearing.

It is not read. It is encoded onto the wire as `limits.max_tokens` (`internal/agent/wire.go`),
decoded by `internal/agent/planner` into a struct field, and **nothing consults that field**.
`model.Request` has no token cap to carry it into: the fields are `Role`, `Prompt`, `Schema`,
`Dir`, `BudgetUSD`, `ReadTokens`, `Timeout`, `Tools`. No argv flag hands a token limit to the CLI.
A turn may spend any multiple of this number, and on 2026-08-16 turns did.

**Why this is the worse shape.** A missing field asks a question. A required, validated field
answers one -- wrongly. The validation is the tell: refusing a zero teaches every reader that the
value matters, so nobody goes looking for the enforcement, and a caller sizing a run reads a
guarantee that was never anywhere. The measured cost of that misreading is in the entry above: a
plan whose shares were set believing a turn was capped.

**Not deleted, on purpose.** The field is the right shape for the bound this system lacks. Money
cannot bound a searching turn -- it is checked between messages, and a glob over a tree with no
matches pays for a tool result and the model's next thought inside one message. Tokens can, and
`MaxTokens` is already declared and on the wire in every assignment. What is missing is the two
ends: a `Request` field, and a cap the provider will take.

**Decided 2026-08-16: the field stopped claiming to be a ceiling, and `Validate` stopped
requiring it.** Three facts settled it, all measured that day and none of them cheap to guess:

- **No provider takes a token cap.** 65 flags on CLI 2.1.232; the only spend bound among them is
  `--max-budget-usd`, in dollars. "Honour it" could not mean passing it along -- it would mean
  atenea enforcing it locally, a new mechanism, not a wiring job.
- **The requirement bought invented numbers, and the settings file admits it.** Three agent types
  declared `max_tokens = 1` beside the comment *"it spends no tokens; the ceiling still has to be
  a real number"*. That is a value written to satisfy `Validate`, not a decision.
- **The one number that looks real is already exceeded by work that finishes.** The two
  model-backed types declare `200000`; `auth-mod` completed `ok` on 224,148 cache-read tokens
  alone. Honouring today's declarations would kill steps that succeed.

So `MaxTokens` is now advisory, zero means the caller declared none, and `Fits` reads a parent's
zero as constraining nothing -- otherwise an absence would refuse every child that did declare.
`Validate` still refuses a negative, which is not a declaration of anything.

**Re-measured 2026-08-16, against the live config and the live record. This widens the finding,
it does not narrow it.**

**Three types declare `200000` now, not two.** `explore`, `reader`, and `plan`
(`internal/config/default.toml:1824,1858,1890`) — `plan` was not counted when this entry was
first written. The `max_tokens = 1` types are unchanged and still honest: `reviewer` (61 rows)
and `plan-check` (18 rows) have never recorded a token in either lane; `filereader` has never run
once on this machine, so its `1` is untested, not confirmed.

**Read literally — "input and output together," the field's own declared definition — the
ceiling has never once been exceeded.** The highest `input + output` on any completed row: `plan`
`20,145`, `explore` `20,476`, `reader` `7,256` — a tenth of `200000` at the very top of the range.
Enforcing the number as written would have refused nothing real; it would have been a ceiling
that never bound anything, which is a different failure from the one this entry names.

**Read as the bill actually reads — cache included — the ceiling is not marginal, it is routine.**
Of `explore`'s 22 completed runs, 20 (91%) clear `200000`, the worst at `1,599,487` — eight times
the declared number, on `wf1786667473939-1`. `reader` clears it on 11 of 33 (33%), topping out at
`268,328` on `census`, `wf1786845363956-1` — 207,330 of that cache-read alone. `plan` has never
cleared it; its worst is `116,465`.

**The row this entry cited — `auth-mod`, `224,148` cache-read tokens alone — is real, current,
and not the extreme case.** It is not even `reader`'s own worst: `census`, same run, went higher.
The single example understated both how often this happens and by how much.

**Same conclusion, different reason.** Under its own definition the number never fired; under the
definition that matches money, it fires on the majority of one type's successful work. Neither
reading makes `200000` a bound worth enforcing as declared — the first because it is too loose to
ever bind, the second because it is too tight to survive contact with real reads. Enforcing
nothing stays correct; inventing a replacement number from this pass would repeat the mistake
the entry above already names — a value chosen to pass a check rather than measured against the
work.

**What is still not built** is the bound itself, and the decision above does not pretend
otherwise: it removes a false claim, not the gap. Wiring a real per-turn token cap needs values
somebody measured first, and the discipline the third fact names -- every declared number today
would have cut work that completes, so the values are the hard part, not the mechanism.

One thing the requirement never had: **a test.** No test anywhere asserted that a non-positive
`MaxTokens` was refused, so the full suite passed unchanged when the check came out. A rule
nothing defends is a rule already half-gone.

## A share funded above the observed maximum still died, four times - 2026-08-16

The prediction on the record, made before the run: nineteen steps funded `$0.45` each all
finish, because `$0.45` sits above `$0.44`, the top of the observed range across fourteen
completed reader rows, and because share size was the discriminator on the previous run.

**Falsified. Four of nineteen died at their ceiling** - `admin-config` `$0.62`,
`sentry-mod` `$0.52`, `drivers-mod` `$0.49`, `census` `$0.54`. The fifteen that finished
spent `$0.20`-`$0.41`, every one *under* its share. There is no overlap between the two
groups: the population is bimodal, and no single flat number separates them, because the
figure a dying step reports is the point it was *cut at*, not what it needed.

Two of the four read *small* ranges - `admin-config` is 12.4 KB, `sentry-mod` 8.1 KB - so
neither bytes nor route count predicts membership. What the four share is unclear from
the receipts, and that is the gap: **this system can price what a step costs when it
succeeds and cannot price what it needs when it fails.** Every completed row is an
observation of a sufficient share; every incomplete row is a lower bound wearing the same
column name, and `CostByType` already excludes them for exactly this reason. The admission
rule shipped yesterday is therefore built entirely on the population that never needed it.

**What it would take:** a step that hits its ceiling would have to be re-run once at a
raised share and the second figure recorded beside the first, making one row that says
"cut at `$0.62`, finished at `$X`". Nothing in the engine does this; `resume` redispatches
but records the retry as its own row with no link to what it replaced.

**Not built.** And not cheap: it means spending real money on a step already known to have
failed, on purpose, to buy the only measurement that would size the next one.

## A killed turn keeps its price and loses its tokens - 2026-08-16

`admin-config` recorded `$0.62` spent against 1,416 cache-write, 4,772 cache-read and 152
output tokens - arithmetic that prices at roughly `$0.05` by any rate, and around `$0.02`
by whatever rate the fifteen successful rows imply. The fifteen that finished all price
the other way, charged consistently ~40% of their token arithmetic, a stable ratio that
says the rate assumption is wrong but the *recording* is whole. `admin-config` inverts it
by 12x, and it is one of the four that were killed.

So the usage of the turn that was in flight when the ceiling fired is never recorded,
while its cost is - `total_cost_usd` arrives with the abort, the `message_stop` that
would have carried the usage does not. This is the same seam as the reserved-answer nudge
being blind on the turns that overspend, and it has the same consequence: **the steps
this project most needs to measure are the ones whose token record is missing.**

**Done when:** a step killed at its ceiling records the usage it actually consumed, or
records explicitly that it does not know - a null, not a small number that reads as a
measurement. Today a reader of that row would conclude the step did almost nothing, and
it is the only row on the run where the dollars and the tokens disagree about that.

**Built 2026-08-16, and two claims above are wrong.** Both corrections come from
reproducing the shape instead of reasoning about it: a real ceiling death on the live CLI,
4.4 seconds, `$0.41`.

**The zeros are total, not partial.** A budget-exhausted result event carries
`total_cost_usd: 0.41228` and `usage` that is zero in EVERY lane - not a small number, no
number. Read out of the shipped binary (2.1.232) the two come from different accumulators:
`total_cost_usd: OA()` is `costLedger.totalCostUSD()`, charged as the work happens, while
`usage: this.totalUsage` is summed only at `message_stop`, which a killed message never
reaches. One construction site for the class that owns it, so it is cumulative across
passes - the per-pass reading was checked and refuted. So `admin-config`'s small figures
are not this defect at all; they are a turn that settled some messages and was killed
during the expensive ones. 29 rows in the local record have the pure shape - every one a
ceiling death, `$9.61` charged with nothing said about what it bought.

**The fix was not in the adapter.** It is `conversation.charge` in
`internal/agent/model`, which preferred the result event whenever one arrived and so
threw away tokens it had already read and deduped correctly - the same stream's assistant
events carried 40,956 cache-creation tokens for that money. It now takes the larger of the
two readings, whole and never blended lane by lane, which is what `weighed()` already did
for the allowance and for the same reason: every figure is one the CLI printed. The price
still comes only from the receipt.

The `claudecode` adapter has the same envelope and **cannot** be fixed this way: it asks
for `--output-format json`, one envelope, no event stream, so there is no second reading
to recover from. That path still records a price against zero tokens, which is the
codebase's existing way of saying "not recorded" - `cmd/atenea/floor.go` prints exactly
that for a row with a price and no token count. Its comment claimed the usage was there;
it now carries the measurement and names the limit.

## `verdict = ok` cannot be made to mean "did the work" - 2026-08-16

Asked because two rules wait on the answer: the admission rule takes its median from
`verdict = ok` rows (`Store.costRows`), and the completeness entry above needs to know
whether a recorded `ok` is worth anything. The answer is that it is not, that no signal in
the record fixes it, and that the one thing which does work is not a reviewer.

**What `ok` actually means.** For a model-backed step, `internal/agent/planner` sets
`Verdict: "ok"` when four things hold: the CLI answered, the structured output parsed,
`summary` and `findings` are both non-empty, and `coverage()` did not refuse. Nothing
compares the answer to the world. `auth-mod` on `wf1786790289244-1` answered *"I cannot
describe this project, because I was unable to locate it before being asked to stop"* -
non-empty summary, non-empty findings, `verdict ok`, `$0.19`, and it sits in the
population the admission rule takes its median from.

**Four candidate discriminators, all refuted by measurement.** The population is the 29
clean `reader` rows with `verdict = ok`:

- **A non-empty answer.** The refusal above is fluent non-empty prose. It is the shape a
  good answer has.
- **`completeness`.** Eight of the 29 come from `wf1786840197972-1`, the run served the
  wrong tree, and **five of those eight claim `1.00`** - full coverage of an objective the
  tree could not contain. A `NULL` passes too: `coverage()` returns `nil, "", nil` for an
  absent claim, so the row is recorded as whole.
- **Tokens.** The strongest-looking candidate and the most clearly wrong. The row that read
  nothing has **83,336 cache-read tokens**, mid-distribution, above eleven rows that did
  real work; the eight wrong-tree rows have the **highest reads on record**, 209k-255k.
  Reading is not evidence of reading the subject.
- **Cost.** The wrong-tree rows cost `$0.22`-`$0.31` against the good run's
  `$0.20`-`$0.41`. Indistinguishable.

**What the contamination is actually worth.** One cent. The `reader` median is `$0.30`
over all 29 clean rows and `$0.31` with the wrong-tree run removed. Worth writing down
because the obvious inference - eight bad rows in twenty-nine must move the number - is
false, and a median is why. The rule is not in danger from this; the *record* is.

**The one thing that works, and it is not a reviewer.** Citations that resolve. Measured
on the same two runs: the wrong-tree run's cited paths resolve 13/19 against the tree that
was actually served and 6/19 against the tree the commission named; the correctly-served
run's resolve 0/20 against the served tree and 18/20 against the intended one. That
separates them cleanly, mechanically, with no judgement - the step supplies the
coordinates and the engine checks them against the world. It is the same check that
verified 155 of 155 declarations by hand on the 19-step run.

**Done when:** a reader step's schema requires citations and the engine resolves them
before recording `ok`. Note what that costs honestly: as a hard gate it needs a *rate*
(neither run resolves 100%, because paths get abbreviated and renamed), and a rate is a
threshold, which is the heuristic this entry exists to refuse. So the citation check is
decisive as **evidence** and not yet sound as a **gate**, and closing that distance is the
work, not the writing.

**Not built,** and deliberately not attempted here. It changes what every reader step must
answer and what `answered` means to every downstream gate - `complete()` in the turn loop,
`coverage` in the report, `outcome` for every `on = "answered"` consumer.

**Meanwhile, one asymmetry is worth naming as a defect rather than a gap.** `coverage()`
refuses a partial that will not say where it stopped - "not auditable" - but accepts an
answer that claims nothing at all. The principle is already in the code; it is applied to
`0.5 with no stopped_at` and not to `no claim`. Of the rows above, every `plan` row and
most `explore` rows carry `completeness = NULL` and are recorded as whole answers.

## A spend notice that reads like a gate - 2026-08-16

`atenea floor measure` prints `about to spend real money: one turn on ... -- about $0.27`
and then spends it, on the same breath. There is no prompt, no `--yes`, and no way to see
the figure without paying it. The line is phrased as a question and is a receipt stub.

Measured by tripping it: `atenea floor measure --repo taxiprime-backend`, typed to read the
notice's new wording, spent **$0.3487** on a cold `plan` turn. `--agent` was optional and
defaulted to `plan`, so the shortest form of the command was also the one that spent without
naming what it spent on. The notice printed correctly and changed nothing, because a notice
that cannot refuse is only ever read afterwards.

Two shapes would close it, and only one was ever a decision:

- **A required `--agent`. Built 2026-08-16.** Not a spending policy tightened but a default
  that spends removed: `--agent` had no business carrying `plan`, because the only thing a
  default can do on this command is pick what to spend money on. It now refuses with the
  settings file's own list, exit 2, before the probe - and the test that defends it points
  at a fake CLI that ANSWERS, so restoring the default fails on the row that appears rather
  than on the message. `atenea floor` lists every measured type and costs nothing, so there
  was no discovery argument for the default either.
- **A confirmation. Decided 2026-08-16: not built, on purpose.** `--yes` to proceed,
  otherwise print the figure and exit 0 having spent nothing. Declined, not deferred: the
  defect that opened this entry is fixed on both counts that mattered here — the notice
  quotes the real receipt now, and the default that actually spent is closed. A `--yes` on
  top of both is a belt over a working brace, and it is not free: at least seven existing
  `floor_test.go` cases call this command expecting it to spend with no `--yes` and would
  need the flag added to keep passing, and any unattended caller of this command elsewhere
  that could not be ruled out on this machine would break silently until updated. Chosen
  with that cost weighed, not because the choice was costless.

The cheapest half needed no decision and is also built: the figure the notice quotes is
now `Measurement.ColdStartUSD`, the receipt, where it used to be the stored floor - the
prefix's slice, 1.9x to 9.8x too small (see instrument 36 in `measuring-the-wrong-process`).

**Not closed clean — the residue is structural, and it outlives this decision.** `--repo`
and `--agent` gate spend only as a side effect of being required values; neither exists
because a rule here says spending needs a gate. Nothing in `floorMeasure` says that. The
next flag or default shaped like the old `--agent` default — one that can spend money by
quietly picking a value nobody asked for — is stopped only by whoever writes it remembering
to check, which is exactly how this one went unstopped until it was tripped. That is the
open condition this entry leaves behind: not unrefused money, but an unenforced rule about
when refusing is required.
