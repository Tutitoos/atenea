# Changelog

Notable changes to Atenea, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Two numbers are versioned here and they move independently:

- **Atenea**, the product, at stable `1.x.y` after the `1.0.0` release.
- **`pkg/contract`**, the wire format client adapters compile against, currently
  at `3.3.0`. It is a commitment from the first release: an adapter is code
  somebody else builds against, and alpha is not a licence to break it weekly.

A release tag is `vMAJOR.MINOR.PATCH` and names the product version.

## [Unreleased]

### Security

- Hold the MCP workflow tools to the chat's grant. `workflow.create` and
  `workflow.launch` dispatched without consulting it, so a client that opened a
  session declaring it would only read could describe a graph whose steps carry
  `write` and `external` and then launch it. The effects arrive inside a file
  rather than inside the request, so no schema could catch this; `launch`
  re-reads them from the persisted run rather than trusting whoever created it.
- Aim a workflow at a repository instead of at the service's working directory.
  Neither tool declared a `repository` argument, and the empty string fell
  through to the process's own directory -- for a daemon, wherever its unit file
  left it.
- Start the background service in Atenea's state root, and then refuse to
  serve a repository declared by a relative path at all. A user unit with no
  `WorkingDirectory` starts in `$HOME`, so the shipped `path = "."` named the
  home directory as a repository to search; naming the state root instead only
  moved it onto Atenea's own receipts. A daemon stands nowhere, so it refuses,
  by name and with the remedy. `config init` writes what `"."` meant when it
  was typed, and the embedded settings keep it, because a command with no
  settings file is standing somewhere and that is the whole point of it.
- Bound every unframed read from a child process. Both stdio transports read a
  server's stdout with no ceiling, so a server that never emitted a newline grew
  the service's memory until the machine gave up.
- Apply the `[local_agents]` token ceiling to types that declare no limit of
  their own, which is the set it exists for.
- Refuse a budget of `NaN` or infinity on every entry point rather than on one.
- Persist the floor a chat narrowed itself to, so resuming a run cannot
  re-apply the operator's wider standing grant.

### Fixed

- Rank the funnel on a total order. The cost comparator was Pareto dominance
  with an id tiebreak, which is intransitive, and with three candidates it could
  seat one that another beat on both tokens and time.
- Bound `max_tokens` to the request rather than to the bill. It was compared
  against a total including the provider's cache reads, so ordinary turns were
  refused after being paid for and reported as `unavailable`.
- Read the model's own account of its coverage on a single-shot turn. Only the
  multi-pass path did, so every dispatch without a budget was refused for
  stating no completeness -- after paying for the turn.
- Read a `kivgraph index` longer than 8 KiB. Its output was captured into a
  buffer keeping only the tail, cut mid-object, and then parsed as complete
  JSONL, so `repository.index` failed on every real repository.
- Bound the readiness probe. A child that accepted a connection and then went
  quiet blocked it forever, and with it the supervisor's stop and the service's
  shutdown.
- Take a workflow over atomically. `Store.Own` overwrote the writer pid
  unconditionally after reading it, so two Ateneas could execute one graph.
- Validate a redo before writing it. The funding check ran after the gate was
  answered and the steps reset, so a refusal left a step no command could reach.
- Refuse a run nothing authorized, and stop creating one: a graph whose shares
  divided a grant of zero compiled, was written, and was then refused by the
  gate, leaving a run `list` showed and `resume` executed.
- Unwind `Engine.execute` the same way on every exit. Two error paths deadlocked
  on an unbuffered channel and three others stranded the goroutines and the
  agent processes they held.
- Write down a review's refusal instead of holding it in memory, and stop
  counting the refused attempt's money twice.
- Error on a settings file named by `$ATENEA_CONFIG` that does not exist,
  instead of booting on the embedded defaults in silence.
- Require `max_tokens` on an agent type, which the shipped settings and the
  documentation both already said was required.
- Bury a stdio child that died on its own, rather than leaving a zombie and
  three descriptors per death.
- Give the shared backend's handshake its own context, so a client that hangs up
  cannot kill a cold server out from under every other chat waiting on it.
- Bound the wait for in-flight connections by the shutdown grace that exists for
  it, and make `Run` wait for the handlers before it returns. `Run` ends with
  `Shutdown`, and a `Shutdown` somebody else already started returns nil at
  once -- correctly, since the teardown must not run twice, but it meant `Run`
  reported the service down while a handler was still writing into the state
  root. A caller that removes that root on Run's return -- a container about to
  unmount, a test with a temporary directory -- was racing a live writer.
- Read the MCP probe's answer rather than the first JSON object carrying an id:
  a server that asks a question before answering was read as having answered.
- Make the metrics buffer a real ring, so a full buffer no longer costs a
  74-microsecond memmove per measurement with the store's mutex held.
- Make tagged release publication idempotent: retries reuse an existing GitHub
  release and replace its assets instead of failing on the duplicate tag.

### Added

- A rule for a command that spends outside a commission: no flag on one may
  carry a default that is not the conservative one. It is stated above
  `floorMeasure` and enforced by a test that reads the source, because the
  source is where a default is written -- `--agent` carried `"plan"` until
  2026-08-16, when a run typed to read that command's own warning text spent
  $0.3487 on a value nobody had named. A second test holds the list to
  anything whose help says it spends real money, so it cannot go stale quietly.
- Retention. Run receipts and agent traces are pruned by a `[retention]` block
  -- ninety days by default, on a daily lane guarded by a mark in the trace
  database. Only closed records go, and by when they ended: an open receipt is
  a commission somebody may still resume and an open trace is the evidence that
  a run died. `keep = "0s"` keeps everything.
- `Outcome.SpentUSDKnown`, which says whether a charge of zero is a price or an
  absence. The core refuses a charge reported without it, and `--json` carries
  the distinction as a pointer: present even as zero means measured.

### Changed

- The backup settles the tree before copying it. Both databases in the state
  root keep a write-ahead log beside them, so a directory copier reached the
  two halves at two different instants and produced a snapshot that opens and
  is missing whatever had not been folded in. `SnapshotIfDue` now takes a
  settle the caller supplies, run only when a copy is due and whose failure
  stops the copy.
- The contract is `3.3.0`. Four additive changes had landed on the public types
  while `contract.Current` stayed at `3.1.0`, so that number named two different
  shapes of the package.
- The suite pins its own temporary root. It could not bind a socket on macOS
  without `TMPDIR=/tmp`, which only the CI workflow set, so 58 tests failed on a
  developer's machine and the pre-push hook with them.
- `release.yml` runs the gates its header claims. `ci.yml` has no tag trigger,
  so a tag pushed to a commit that never reached main was published having run
  neither coverage nor the benchmark barriers nor the provider matrix.
- The linter runs on macOS as well as Linux. A Go build tag decides which files
  exist, so the ubuntu runner had never read `service_darwin.go`: four exported
  functions there had been undocumented since they were written, invisible to a
  gate reporting green on every pull request. Cross-linting does not substitute
  -- DuckDB is cgo, so type-checking a Linux build from a Mac fails in the
  bindings before it reaches this repository's code.
- Three gates that could pass without checking anything now check:
  `v1-policy-check.sh` anchors each file rather than the list,
  `benchmark-check.sh` refuses a line with no `ns/op`, and the readiness gate
  asks `git grep` instead of whichever search tool the machine happens to have.
- Every workflow job declares `timeout-minutes`, and the embedded status-line
  widgets are parsed in CI.
- The one test that started a real, billable Claude Code turn on every
  `go test` -- and on every push, through the pre-push hook -- is opt-in and
  bounded.
- The Claude Desktop packaging is tracked, built from the checkout rather than
  from one developer's `~/.local/bin`, refuses a manifest whose version
  disagrees with the binary, and is published per platform by the release
  workflow.
- Documentation: the repository overlay's fourth block and its ceiling, the
  provider catalog, the layout tree, the benchmark inventory, the coverage
  figures and the contract history now describe the code that exists.

## [1.0.5] - 2026-08-24

### Added

- Add continuous MCP reachability probes with durable, stale-aware readings;
  set `core.health_probe_every = "0s"` to keep probes on demand.
- Add explicit `--confirm` protection for direct agents with write or external
  effects.
- Add the opt-in `semantic-reviewer` agent for structured semantic judgement
  separate from deterministic citation verification.

### Changed

- Persist MCP health observations atomically with restrictive permissions and
  keep service-only background probing out of one-shot command execution.

## [1.0.4] - 2026-08-23

### Added

- Persist runtime provider health and repository indexing observations across
  service restarts, with timestamps for stale-observation handling.
- Add configurable selector health expiry through
  `[selector].health_stale_after`, defaulting to `24h`.

### Changed

- Expired provider health observations return to `unknown` and are eligible for
  a fresh probe instead of permanently suppressing a provider.

## [1.0.3] - 2026-08-23

### Fixed

- Stop contained model processes as soon as an observed OpenCode token or cost
  overrun crosses the local boundary, while preserving provider receipts.
- Stop conversational Claude turns when their observable token usage crosses
  the declared local limit.

### Changed

- Make the live MCP and OpenCode readiness jobs manually dispatchable, install
  a pinned OpenCode client, and retry only transient missing-terminal-event
  provider failures in the live matrix.
- Document the distinction between local enforcement, provider-side hard caps
  and model-backed semantic review.

## [1.0.2] - 2026-08-23

### Added

- Added `mcp_server.instance = "per_chat"` for raw HTTP and stdio MCP
  backends, with isolated lazy sessions and cleanup on chat disconnect.
- Added configuration, MCP integration and lifecycle coverage for shared and
  per-chat backend ownership.

### Fixed

- Carried `limits.max_tokens` into model turns and rejected observed answers
  that exceed the declared token limit, while retaining the provider work
  already reported in the charge.
- Documented the remaining provider-side limitation: work already in flight
  cannot be cancelled at an exact token boundary when the CLI exposes no hard
  cap.

### Validation

- Passed the full normal and race test suites, the v1 readiness gate, all
  native CI build targets, the live MCP smoke and the six-case OpenCode matrix.

## [1.0.1] - 2026-08-22

### Fixed

- Updated the GitHub Actions checkout, Go setup and artifact upload actions to
  Node.js 24 runtimes, removing the Node.js 20 deprecation warning.
- Stabilized the workflow race test by waiting for the child-process marker
  and allowing the runner to observe the active process before cancellation.
- Kept Claude Code over-budget responses out of successful workflow history
  while retaining the provider's observed charge in the receipt.

### Validation

- Revalidated the pre-release gate, MCP safety harness and provider audit after
  the `1.0.0` release.
- Rechecked the read-only claude-mem matrix and the destructive MCP fixtures;
  no memory writes or real destructive actions were performed.
- Passed the free OpenCode provider/MCP matrix in all six cases.
- Refreshed the official Kivgraph index to generation `000011` for the five
  registered TaxiPrime repositories. The aggregate `taxiprime-root` workspace
  remains intentionally unregistered rather than being reported as ready.

## [1.0.0] - 2026-08-22

### Added

- A complete capability registry and orchestrator for code search, symbols,
  graph impact, repository indexing and provider-backed workflows.
- Native adapters and wrappers for OMP, Claude Code, Codex, OpenCode, Serena,
  Kivgraph and Tokensave, with optional raw MCP passthrough.
- Persistent local MCP dashboards for Serena, Headroom, Maestro and Kivgraph
  with explicit browser opening and loopback-only binding.
- Strict citation validation, permission effects, provider health, checkpoints,
  metrics, release gates and checksum-verified installation lifecycle.

### Security

- Destructive MCP actions remain outside the default execution path and require
  explicit effect grants; external actions were validated through refusal or
  simulation during the release audit.
- Obsolete memory-runtime references were removed from the active product.

### Release notes

- Claude Code, Codex and OpenCode remain optional provider surfaces and depend
  on the local CLI, account and provider availability.
- The Kivgraph web viewer is an optional external bundle. The team configuration
  uses a locally built `webassets` binary; the Atenea release does not claim to
  redistribute that third-party native bundle.

## [0.10.4] - 2026-08-21

### Added

- Native release targets for Linux `amd64`/`arm64` and macOS `amd64`/`arm64`.
- macOS `launchd` service installation alongside Linux `systemd --user`.
- Checksum-verified installer updates with rollback and uninstall support.
- CI and release validation across the supported native architectures.

### Removed

- The obsolete external graph adapter and its configuration, registry, and
  test surface.

## [0.10.3] - 2026-08-21

### Added

- **`atenea wrap omp` is now a supported wrapper alias.** It checks and
  reports Atenea's declared MCP servers before launching OMP, while preserving
  OMP's own file-based MCP configuration because the CLI has no ephemeral MCP
  overlay.

## [0.10.2] - 2026-08-21

### Added

- **`atenea workflow redo` dispatches a step that was cut at its own spending
  ceiling, at a share somebody raised.** Until now the only way to retry one was
  to write a new plan: `resume --redo` is for work nobody judged, and a ceiling
  death *was* judged — it reported, and the report said incomplete. Measured
  2026-08-16: of 150 steps cut at a ceiling, 2 had ever been re-dispatched. It is
  an operator's act and never automatic, because the same share buys the same
  result — every share must be a raise, and a redo that does not raise one is
  refused naming both figures. It reopens a finished run to do it, records the
  authorization as a gate of its own kind, and files the dead attempt with the
  share it actually ran under *before* writing the new one. That ordering is what
  leaves the pair the admission rule has never had: cut at `$0.45`, finished at
  `$0.90`. `CostByType` excludes ceiling deaths because their spend is a lower
  bound, so today the rule is built entirely on the population that never needed
  it. A raise past the grant is refused until the grant is raised in the same
  command, and the raised share meets the same funding check a new plan would.
- **A step cut at its ceiling is now decidable from its row, with no prose
  read.** `internal/agent/model` files a ceiling stop as `unavailable` on purpose
  — the ceiling was that one call's own — so the reason kind a real outage writes
  and the one a ceiling death writes are the same word. What separates them is
  the pair of numbers, against the same `0.98` band `CostByType` already excludes
  rows by: sharing the constant is the point, since a step that is re-dispatchable
  is exactly a step the rule refuses to price against. Over the whole record it
  selects 29 of 29.
- **A run whose token count is missing says so instead of reading as cheap.** 29
  rows on this machine carry a real charge and no tokens — `$9.61` — all of them
  ceiling deaths from before `conversation.charge` stopped preferring a result
  event that a killed turn never prints. Their dollars are real and their tokens
  sum as zero, so a total was telling a reader those steps did almost nothing. The
  budget line now reads `at least N tokens spent; 1 step charged with no token
  record`. Derived from the row, because a turn that used no tokens cannot cost
  money: nothing is stored, nothing is migrated, and no historical row is
  rewritten.
- **`workflow create` now refuses a share below what that agent type has cost to
  FINISH, not just to start.** The floor and the rescuable threshold both price a
  probe: one turn that starts, makes at most one tool call, and stops — `$0.06`
  warm on the row this was measured against. A step that does the work costs
  several times that. On a 23-step inventory on 2026-08-16 every share cleared
  both probe rules; five steps finished at `$0.30`-`$0.44` and eighteen died at
  their ceilings having read real files and written nothing, `$4.41` for no
  deliverable. The third rule is the median of the rows that finished, read from
  the same `CostByType` table `atenea metrics` already prints, and it needs three
  clean rows before it may refuse anybody — a median of two is a rumour and the
  middle of two rows is an endpoint. Rows that stopped at their ceiling are
  excluded, as they always were: they are lower bounds, and pricing admission off
  them would quote this system's failures back as the cost of success. A type no
  probe has priced is no longer skipped either — finished rows need no probe, and
  exempting the types with the most evidence because a probe was missing was
  backwards. The refusal names which of the three bound, and prints the median
  with its row count and scope.

- **`atenea floor measure` now makes a real tool call, and prices what comes
  back warm.** A probe that answered without touching a tool measured the one
  event nothing is refused against: the prefix alone, before any tool schema
  had been re-sent or any result had arrived. On this machine the block that
  arrives with the first tool result is `41,927` tokens against a `5,647`-token
  prefix — 8.4x the quantity the floor was built on — and it is written once per
  machine per cache lifetime, then read at a twentieth of the price by every
  step after. `floor measure` now runs one tool call, stores
  `first_call_tokens`, and `atenea floor` prints `WARM USD` (what a step pays)
  beside `COLD USD` (what establishing the cache costs once). The floor rule
  charges the warm figure wherever a row carries one; a row measured before
  this field existed still charges the cold prefix, and says so. The rate a
  probe derives is the receipt over every token it paid for, and not over the
  prefix alone: divided by the prefix, the same `$0.4935` bill reported a
  `$0.21` warm step and a `$4.16` cold start. Priced against everything it
  covers, the derived cold start reproduces the bill to the cent and the
  per-token rate lands within 2% of the same model's independently measured
  rate.
- **Every `agent-exec` run was buying a session title nobody reads; it no
  longer does.** The CLI names each session with a second model call — one
  `claude-opus-5` request at `effort: high`, `max_tokens: 64000`, carrying
  atenea's entire commission as its user message — to title a session
  `--no-session-persistence` throws away. Passing `--name atenea-<role>` gives
  it an answer and it stops asking. Measured against a loopback recorder that
  answers the Messages API locally, so the finding cost nothing: the call fires
  in 4 of 8 unnamed runs (racy — one run of it proves nothing) and 0 of 8 named,
  then 0 of 6 against atenea's real argv.

  `--exclude-dynamic-system-prompt-sections` was measured in the same harness
  and deliberately **not** adopted: it moves 3,368 bytes out of the shared,
  cached system block into `messages[0]` beside the per-step-unique commission.
  It is a cross-user prompt-cache optimisation, and atenea is one user on one
  machine, so here it only shrinks the cached prefix.

- **A plan can clear the floor and still die: `workflow create` now refuses
  below a second, larger number too.** Half of a step's own share is reserved
  as its read allowance, and if that half does not outweigh the turn's own
  first assistant event — cold-equivalent, the same reading a floor is priced
  from — the reserved-answer nudge fires on the arrival of the prompt, before
  the model has read anything of its own, and the step dies with nothing
  written anyway. `workflow create` now computes this threshold from the same
  stored floor row, per repository, agent and model, and refuses a plan on
  whichever of the two requirements is larger, in one pass, naming both in the
  refusal.

  It exists because the floor alone had already been proven insufficient.
  Thirteen model-backed steps, funded above the floor the night before, died
  empty anyway: $5.24 spent against a $3.52 grant. The arithmetic that would
  have caught it — half a share buys 83,000 input-equivalent tokens of reading
  per dollar, and a real turn's own first assistant event already weighed
  65,625 of them — was measured the same day the floor check shipped, and sat
  in a doc comment unread by any admission rule until now.

  `atenea floor` prints the new number beside the ones it already reported:
  the smallest share, for a repository/agent/model row, that buys any reading
  at all before its own first event outweighs it.

- **A step that reads files somebody already named no longer pays for tools it
  never calls.** `reader` ships declared beside `explore`: the same agent, the
  same model role, the same answer, handed `Read` and `Glob` and no
  `--mcp-config` at all. `atenea agent-exec reader` runs it, and a plan picks
  it off the same menu `explore` is on.

  The saving is the whole reason it exists, and it was measured rather than
  assumed. Cold, on one repository against one model: starting an `explore`
  turn costs **$0.27 and 26,603 tokens of prefix before the model has read a
  line**, and the identical probe carrying no tools costs **$0.06 and 4,991** —
  81% of what such a step pays to exist is the definitions of Atenea's
  capabilities. On one eighteen-step plan that was **$2.64 of a $5.29
  requirement**, for twelve steps that read files the ask had already named and
  dispatched not one capability.

  It is one implementation given two tool surfaces, never a copy: `explore` and
  `reader` are the same function called with a different `planner.Surface`, and
  that surface is what `atenea floor measure` reads to build its probe, so the
  turn that gets priced is the turn a real step gets. A `reader` also never
  dials the core — it has no capability catalog to fetch — so it runs on a
  machine whose service is down.

- **A plan funded below the cost of starting a turn is refused before anything
  is written.** `atenea floor measure --repo ID --agent explore|plan` runs one
  real turn that is asked to do nothing at all and records what it cost;
  `atenea workflow create` then refuses a graph whose steps are funded below
  that number, naming the arithmetic per step and the measurement's provenance
  once. Nothing has been measured on a fresh machine, so the check is off until
  somebody measures, and it never raises a share — it declines to start.

  It exists because of a number nobody had checked. On an eighteen-step plan,
  measured twice, twelve steps died at their ceiling having read everything and
  written nothing, and the arithmetic underneath says why: one turn on that
  repository costs about **$0.28 before a single file is read** — 27,666 tokens
  of cache write for the system prompt and the tool definitions — and seventeen
  of the eighteen steps were funded between $0.12 and $0.28. They were not
  budgeted badly, they were budgeted below the price of starting.

  Two things the first live run of the command corrected, both of them
  measurements. The floor is keyed on the **agent**, not just the repository and
  the model: `explore`, carrying Atenea's tools plus `Read` and `Glob`, costs
  $0.28/27,666 tokens where `plan`, carrying none, costs $0.06/4,991 on the same
  repository with the same model — the tool definitions are 81% of the floor,
  and a store keyed without the agent let the cheap row govern the expensive
  steps. And a probe run against a **warm prompt cache is refused rather than
  recorded**: the second probe of an evening reads back the prefix the first one
  wrote and reports $0.01 with zero tokens of cache write, a real receipt from a
  real turn understating the cost of starting by 28x.

- **`atenea intent` reads the client config a repository carries and says how
  each thing it asks for is answered.** `.mcp.json` and `.claude/` for Claude
  Code, `opencode.json` and `.opencode/` for opencode, mapped onto registered
  providers: `funnel` when a provider answers it, `vouched` when Atenea
  declares the backend itself, `prose` for a skill, `unmatched` when nothing
  here provides it. The unmatched list is printed last and counted, because a
  translator that silently drops what it cannot map produces a report in which
  absence and satisfaction look the same.

  It reads and it never runs. A declaration's command, arguments, environment
  and URL are dropped at the parse boundary — not stored, not printed, not
  passed on — so a file arriving with a git clone cannot hand this machine a
  process. A test walks every field of the parse result and fails if one of
  those strings is reachable from it.

  Two measurements changed the design. A real repository using both clients
  reported `3 unmatched` while asking for two things, so declarations of one
  backend now collapse into one row with both files named on it; two that
  disagree on transport are reported as inconsistent rather than resolved, and
  `enabled` is a union. Then the pointer at the foot of `config show` and the
  report itself answered "how much is this asking for" with 13 and 11 on the
  same repository, so both now count through one function.

- **A repository can carry its own settings, in `.atenea/config.toml` at its root.**
  A partial overlay on the global file: what it declares wins, what it omits
  falls back, and a repository without one changes nothing. The active
  repository is the first `.git` above the working directory, and the walk stops
  there — a nested repository does not inherit the workspace above it, because
  it is cloned and published on its own and would otherwise behave differently
  depending on where it was checked out.

  It may declare three things: what the repository is (`[[repository]]` —
  `languages`, `scale`, `vcs`, `indexed_by`), which implementation to prefer for
  it, and further files to treat as delicate. Every other key is refused naming
  the block and the reason. The file arrives with a `git clone`, so the trust
  question is not the global file's: `[[mcp_server]]` and every `process` block
  carry a command to launch, and a shortened `[security] sensitive` would disarm
  the secret skip for the machine that cloned it. `sensitive` is unioned, never
  replaced — tightening is offered and loosening is not. `repository.path` is
  refused because the path is the directory the file was found in, which is the
  only thing keeping this layer inside its own tree.

  The obvious implementation is wrong, and it was measured before it was
  written. Decoding the local file onto the global one and letting TOML merge is
  correct for scalars and tables and silently broken for arrays of tables:
  against `toml v1.6.0`, a global with two `[[repository]]` blocks overlaid by a
  local file declaring one comes back with a single repository, and that
  survivor keeps the **first** global block's path while taking the local
  block's scale. The library merges by position, not by identity. A file naming
  its own repository would have inherited another repository's path and dropped
  the rest of the catalog, with no error. So the local file is decoded into its
  own shape and merged by key, and the measurement is pinned by a test that
  fails if anyone reaches for the shorter version.

  `atenea config show` prints the effective settings with the origin of every
  field, which is the only way to tell an inherited value from a declared one
  that happens to agree. `ATENEA_LOCAL_CONFIG=0` ignores the layer.

  `atenea run` and `service install` read the global file alone: one process
  answering about every repository must not bake in the overlay of the directory
  it started in. A consequence that falls out for free — when an overlay
  applies, `settings` names both files, so the check `status` already makes
  before trusting the running service's screen now correctly declines to.

- **`atenea statusline install` puts Atenea's own light in opencode's sidebar.**
  The traffic light, the version actually running, and unread incidents, read from
  the service's unix socket — the same door the CLI knocks on, so it needs no key,
  no port and no network. It draws the client's footer, beside the client's own
  version. `uninstall` takes it off, `status` says where it stands, the same three
  verbs as `service`.

  That column is the client's and it is 42 columns wide: in its default `auto`
  state it appears only above 120 columns of terminal, and never for a child
  session. Below that width the section is installed, declared, loaded and
  invisible, and no command here can say so.

  The plugin source is embedded in the binary, so `status` can answer the only
  question worth asking after an upgrade: whether the file on disk is the one
  this binary ships. A drift prints both digests and the remedy. Installing from
  a checkout path would have made that unanswerable, which is the defect this
  repository has now paid for twice.

  Two states are told apart deliberately: nothing running prints `atenea
  apagado` in muted grey, a socket that exists and would not answer prints
  `atenea sin lectura` in amber. The connection error cannot separate them —
  Bun reports `ENOENT` for a missing path, a socket left by a crash, and one it
  is not allowed to open alike — so the distinction comes from whether the
  socket file exists. Without it, a service stopped on purpose was reported as
  a fault.

  A `tui.json` holding somebody else's plugins keeps them; one this command
  cannot parse cleanly is refused rather than rewritten, naming the line to add
  by hand. Uninstall removes the config only when taking our entry out leaves
  nothing behind.

- **A second sidebar section, `Models`: which model did what share of this session.**
  `atenea statusline widgets` lists what the binary carries, and the three verbs
  now take a name: `install session-share` draws one model per line under the
  client's own `Context` box —
  `MiniMax-M3 86M (60%)`, `deepseek-v4-pro 42M (30%)`, and so on. Omitting the name
  still means `atenea`, and every screen prints which widget it acted on, because
  with two installed the paths differ by one word. A name this binary does not carry
  is refused with the list of the ones it does, rather than resolving to the
  default — installing a traffic light when somebody asked for a share is the
  failure that check exists for.

  Both numbers on a line come from one sum: the tokens printed are exactly the ones
  the percentage divides. A cache-excluded count beside a cache-included share would
  read as consistent and be wrong by the nine points those bases differ by here.

  Every model gets a line, and the section arrives **collapsed**: one click on the
  header opens it. Closed, the header carries the dominant model, its share and how
  many others there were — `▶ Models (MiniMax-M3 60%, +6)` — and a failure is louder
  shut than open, because `sin lectura` is printed in the header itself rather than
  in a body nobody can see. This retired a cap of five and the summed `+2 otros`
  line that existed only to keep the capped shares accounting for the session: shut,
  the section cannot push anything below the fold, and open, the length is the one
  the reader asked for. It folds on a mouse click and has no key binding, which is
  parity with the client's own `MCP` section rather than a shortfall. A share that
  rounds away prints `<1%`, not `0%`: `699k (0%)` is two statements that contradict
  each other. No combined total — the `Context` box above it already reports one,
  and a second invites reconciling two numbers that answer different questions.

  **Both sections were first put where they fit, not where they belonged.** The
  light went to `app_bottom` and the share to `session_prompt_right` because those
  were the two slots this code had already measured; the sidebar was never
  checked. It takes plugin content: the client offers twelve slots, three of them
  `sidebar_title`, `sidebar_content` and `sidebar_footer`, and its own `Context`,
  `MCP`, `LSP`, `Todo` and `Files` sections are themselves `sidebar_content`
  registrations at orders 100 to 500. `Models` now sits at 150, continuing the box
  it answers next to. A test pins that and refuses a registration naming a slot
  outside the sidebar.

  **Atenea draws the client's footer now.** The line reporting a service belongs
  beside the version of the thing it runs under, and that turned out to be
  reachable — as ownership, not as an addition. The footer is a box outside the
  scrolling column, published as a `sidebar_footer` slot declared
  `mode:"single_winner"`: the lowest order wins outright, the winner is invoked
  with exactly `(context, {session_id})`, and the host's own lines are slot
  children used only if the winner draws nothing. So this widget registers at 50,
  beats the client's 100, and renders all three lines itself — the project path,
  the client's version, and Atenea's.

  The project path follows the rules of the line it replaces: the session's own
  directory when it has one, else the directory the client started in, `$HOME`
  shortened to `~`, and `:branch` only when the session is the one the client is
  standing in. Verified against the host's own render on a session whose directory
  differs from the launch cwd rather than against the minified helper, because
  three functions in that bundle answer to the name the footer calls and none of
  them is identifiable.

  **Neither version is remembered.** The client's is read from the host on every
  paint, Atenea's from its socket, and an unreadable one prints `sin lectura` in
  its own slot. A version line that keeps showing the last good value is the one
  failure it cannot survive.

  **The slot is handed back rather than emptied.** On a machine with no paid
  provider the client uses that same footer for a `Getting started` card with a
  `/connect` prompt, and winning the slot would delete it. Both halves of its
  condition are readable at registration time, measured, so the widget checks,
  declines, and sits in the column at 900 instead. Anything unreadable counts as
  onboarding: declining costs one line's placement, guessing wrong costs a
  first-run user the only prompt that says how to connect a provider.

  **And a client that changes that footer now breaks a build.** Its shape — every
  string it draws, every field it reads, how many elements it builds — is pinned
  in `internal/statusline/testdata/opencode-footer.json` and compared against the
  client installed on the machine: the pre-commit hook runs it here, and a
  scheduled workflow installs the newest published client each morning and runs it
  there. Without that, a release adding a line to its footer would delete it from
  the screen with no error anywhere — the same class of silent loss as an enum
  renamed under a wire format, and it gets the same treatment.

  One placement is genuinely unreachable, measured: there is no slot inside the
  `Context` box.

  When a section outgrows the room the column **scrolls**; it does not clip, and the
  footer stays pinned. The content sits in a 42-column `scrollbox`, and a 24-row
  probe pushed the client's own `LSP` body below the fold while the footer stayed —
  the same screen at a taller pane showed everything again, so nothing is dropped.
  Content below the fold is still content nobody reads, which is why the list
  arrives collapsed.

  **Shares, not currency.** opencode stores a `cost` per message and summing it
  is one line of SQL, but on a subscription that figure is list price for traffic
  nobody itemised, so printing `$42.78` would state as fact something no invoice
  said. The share is true under either billing.

  The base is every token the model handled — `tokens.total` per assistant
  message, input, output, reasoning and cache reads together, falling back to the
  sum of the parts on older rows. It is written down because the answer moves
  with it: on a 1,578-message session MiniMax-M3 is 97.8 % of all tokens and
  88.4 % of input-plus-output alone. Models with no tokens are dropped rather
  than printed as `0 %`.

  It reads the client's own SQLite store read-only, for the session id the host
  hands the plugin: no socket, no network, nothing leaving the machine. 2 ms on a
  3 GB store, because it lands on an index opencode already keeps. An unreadable
  store draws `sin lectura` rather than the last good percentages, which would
  otherwise keep showing numbers that had stopped arriving.

  This widget was built against a measured API, not an assumed one. A probe
  plugin printed what a slot is actually handed before a line of it was written:
  the host passes `session_id` and a full `api` — `api.session.messages(id)`
  works from inside a slot — while `api.route.current` does not exist, and
  `messages()` returned 100 of that session's 1,578 rows, which is what sent this
  to the store instead.

- **A third widget, `limits`: one collapsible section per provider — `Claude` and
  `Codex` — showing how much of each live rate-limit window is left.** Collapsed on
  every launch, both account windows in the closed header (`▶ Claude (5h 78% · 7d
  56%)`), and a bar per window with its reset when opened. Nothing at all when no
  window is live: no header, no placeholder, not even the separator.

  The closed header carries both windows rather than the tightest, and drops from
  the right when it will not fit, so the five-hour figure is the one that survives.
  The question a closed section answers is "is something about to stop me", which
  the five-hour window answers and the weekly one does not. A window scoped to one
  model (`Fable`) never appears there — two rows reading `7d` in one header invite
  reconciling them — and is one click away in the body.

  **This shipped as one line first, and the correction is the entry.** That version
  read `~/.claude.json` and a codex session rollout, and was a line because those
  files are refreshed only by a human: two Claude refreshes in twelve days, codex
  readings on three days out of thirty-one, a five-hour window live about 4% of the
  time. The measurement was of the *client that rewrites a file*, reported as a fact
  about *the number inside it*. This machine had a self-refreshing source all along:
  omp keeps one usage report per provider in its own store and refreshes each about
  every ten minutes. Measured 2026-08-11 — readings one minute old for both
  providers, median gap one hour, and it covers six providers.

  Not the `usage_history` table beside it, which looks like the obvious source and
  prunes: 7,029 rows while its maximum advances, one to three of a provider's limits
  per write, so "newest row per provider" silently drops whichever window was not in
  the last write. A row read at 01:01 was gone eight minutes later. The cached report
  is written whole and carries `window.durationMs`, `window.id` already short as
  `5h`/`7d`, `amount.usedFraction`, a vendor `status` and `scope.tier`.

  **Staleness has two halves and the second is not a threshold.** Past 90 minutes —
  measured p90 gap plus slack — every row shows its age. But a reading older than the
  window it describes is not old: that window is over, a new one opened unobserved,
  and its usage starts from a number never seen here. Those rows are dropped, not
  aged. Measured as the normal path, not an edge case: overnight gaps reach 14.5
  hours and 17 of the last 30 days had one longer than five hours. Verified on screen
  from a doctored store — at 14 hours the five-hour row is gone and the rest read
  `· hace 14h`; at eight days neither section draws.

  Amber is the vendor's `status` plus a reading old enough to show its age; no
  threshold on the percentage is invented here. Four other providers publish into the
  same store and none is drawn: two report in units with no whole to divide by, one
  has said 100% free every time it was measured, one had not been read for two days.

  Whether anything draws is still decided once, when the client loads plugins, so a
  provider with no live window at startup stays absent until restart — it takes a
  reading older than the longest window to land there.

- **A retry now files the attempt it replaced.** `workflow_step` holds exactly one
  attempt: `Claim` clears the outcome columns on every re-claim and `Reset`
  overwrites the terminal status with `pending`, so the row a retry replaced was
  gone. New append-only `workflow_attempt` table, one row per superseded dispatch
  — trace, status, verdict, reason, the share it ran under, spend with all four
  token counts, completeness, the answer, and when it was superseded. One shared
  statement called from both writes, each inside the transaction with the write
  that destroys the row, so the copy and the erasure cannot come apart. The share
  is per attempt because that is the figure that differs between them.

  It does not make the admission rule usable yet, and the reason is not the
  linkage: **nothing can produce the pair.** A step cut at its ceiling finishes
  `incomplete` — it reported, so it was judged — and two refusals stand between
  that row and a second attempt: the run is `already finished`, and `--redo` is
  `for steps nobody judged`. In the local record 2 steps were ever re-dispatched,
  against 150 cut at a ceiling with no path to a retry.

- **`Limits.MaxTokens` is advisory, and `Validate` no longer requires it.** It was
  required, validated positive, encoded on the wire and decoded by the planner
  into a field nothing reads — not even the prompt, so the model was never told.
  Three facts closed it: the shipped CLI (2.1.232) has no token cap among its 65
  flags, so honouring it would mean a new local mechanism rather than wiring;
  three agent types in the live settings declared `max_tokens = 1` beside the
  comment "it spends no tokens; the ceiling still has to be a real number", which
  is a value written to pass validation; and the `200000` the model-backed types
  declare is already exceeded by steps that finish `ok` — 224,148 cache-read
  tokens on one. Zero now means the caller declared none, and `Fits` reads a
  parent's zero as constraining nothing, since otherwise an absence would refuse
  every child that did declare. A negative is still refused. No test anywhere
  asserted the old requirement, so the suite passed unchanged when it came out.

### Fixed

- **`atenea floor`'s `COLD USD` column priced the prefix, not the turn.** It
  printed the stored figure — the prefix's slice of the receipt a first-call probe
  paid — beside a `WARM USD` column that already covered the whole start, so two
  columns of one table had different spans. Measured 2026-08-16 by paying both:
  `explore` read `$0.14` for a `$0.27` turn and `reader` `$0.05` for a `$0.45`
  one, while a row with no first-call probe agreed exactly — putting the error
  only on the rows measured by the better instrument. Now
  `Measurement.ColdStartUSD`, falling back to the stored figure for a legacy row
  with no span to widen over, the same way the token column beside it says
  `(not recorded)` rather than `0`.
- **`atenea workflow show`'s per-step `COST` counted the live attempt only.** The
  run-level balance defect one level down: `admin-config` printed `$0.68` for a
  step that had cost `$1.29`, under a header corrected the same day to `$7.32` —
  so the column no longer summed to the total two lines above it. It now totals
  every attempt (dollars, never the archive's tokens, for the reason
  `workflow.Spend` gives). A step mid-redo — unmeasured live row, priced archive —
  read `unmeasured` while `$0.62` had been spent, and now reports it.
- **`atenea floor measure --agent` is required: a default that spends is not a
  default.** It carried `plan`, which made the shortest form of the only command
  here that spends unattended also the one that chose what to spend on. Measured
  2026-08-16 by tripping it: `atenea floor measure --repo taxiprime-backend`,
  typed to read this command's own warning text and expecting exactly this
  refusal, priced a cold `plan` turn for `$0.3487`. It now refuses with the
  settings file's own list of declared types, exit 2, before the probe — the same
  courtesy an undeclared name already got. `atenea floor` lists every measured
  type and costs nothing, so nothing is harder to discover. The notice itself
  still cannot refuse; that half remains a decision, in `not-built-yet`.
- **A run's balance counted only the live attempt, so it read high after a redo.**
  `workflow_step` holds exactly one attempt: re-dispatching a step overwrites the
  row and files the old one in `workflow_attempt`, where `Run.Spend` could not see
  it. Measured 2026-08-16 on the first real redo — the report said `$6.70 spent,
  $2.30 left` of a `$9.00` grant while `$7.3231` had gone. `Load` now carries the
  archive on `Run.Superseded` and `Spend` totals it as `SupersededUSD`, held apart
  from the step figures because an attempt is not a step and `CostByType` must not
  double-count the step it belongs to. The line reads `$7.32 spent, $0.62 of it on
  1 attempt a redo replaced, $1.68 left`. A balance that reads high is the one
  direction of this that matters: it is what a person checks before authorizing
  another step. Introduced the same day by the archive that makes a redo auditable.
- **`atenea floor measure` no longer quotes a price 1.9x to 9.8x below what the
  turn it is about to spend costs.** The notice named the STORED floor, which is
  by construction the prefix's slice of a receipt that also paid for the block
  arriving with the first tool call — so it said `~$0.14` and `~$0.05` for two
  turns billed `$0.27` and `$0.45`. Found by paying it: two probes budgeted off
  that line were authorized at `$0.31` and cost `$1.09`. The correctly scaled
  figure was already printed by the same command one line later, after the money
  was gone; it is now `floor.Measurement.ColdStartUSD`, used by both lines and by
  `WarmUSD`, which derived it by hand. For any row a first-call probe wrote it is
  not an estimate: it recovers that probe's receipt exactly, `$0.2724` and
  `$0.4477` against `$0.2704` and `$0.4464` priced lane by lane off the recovered
  price list. The notice still cannot refuse — see `not-built-yet`.
- **A turn killed at its ceiling no longer records a price against no usage.**
  `conversation.charge` preferred the result event whenever one arrived, and on a
  budget-exhausted turn that event carries a real `total_cost_usd` and `usage`
  that is zero in every lane — so the row said what the step cost and nothing
  about what it bought. Reproduced on the live CLI in 4.4 seconds for `$0.41`:
  `total_cost_usd: 0.41228` beside all-zero usage, while the same stream's
  assistant events carried 40,956 cache-creation tokens. Read out of the shipped
  binary (2.1.232) they are different accumulators — the price is a cost ledger
  charged as the work happens, the usage is summed only at `message_stop`, which a
  killed message never reaches. `charge` now takes the larger of the two readings,
  whole and never blended lane by lane, which is what `weighed()` already does for
  the allowance and for the same reason: every figure is one the CLI printed. The
  price still comes only from the receipt. A settled turn's receipt is still larger
  and still wins, because streamed `output_tokens` run short. 29 rows in the local
  record have the old shape, every one a ceiling death, `$9.61` charged with
  nothing said about what it read.

  The `claudecode` adapter has the same envelope and no fix available: it asks for
  `--output-format json`, so there is no event stream to recover tokens from. Its
  comment claimed the usage was there; it now records the measurement and names
  the limit.

- **A refusal no longer invents a cent that was never measured.** `centsUp`
  rounds a requirement up so the figure printed is one a person can type back as
  a share. A requirement of exactly `$0.55` arrives from the store as
  `0.5500000000000000444`, and a bare ceiling turned that into `$0.56` — a cent
  nobody measured, printed beside a median as if it were part of it. It now
  subtracts the same `moneyEpsilon` the rest of the check compares money with.
  Found by a test on an observed median, not in the field.

- **A run is no longer executed against a repository other than the one it was
  created for.** A 23-step plan created with `--repository taxiprime-backend`
  and launched without the flag ran every step in Atenea's own checkout: the
  settings declare `current` at that path, so a real repository was served and it
  was the wrong one. Twenty-three readers looked for a Fastify surface in a Go
  orchestrator — eight reported the files absent, fifteen died searching, `$5.88`
  of a `$5.22` grant went, and no route was inventoried. The repository was
  written by `Create` and read only by `checkFunding`: a missing comparison, not
  a missing default, so this refuses rather than adopting what the record says —
  adopting would fix that case by hiding the class. `Launch` checks before
  answering the gate, so a refused launch leaves the run launchable instead of
  reading "launched already" on the retry; `takeOver` carries the same check for
  `run` and `resume`. The refusal names what the run recorded, what would be
  served, and the directory that id stands for, because a real id can name no
  tree: "would serve current" is true and useless.

- **The admission threshold was still priced on turn 1, the cheap half of the
  turn.** It asked whether a step's read allowance outweighed the prefix that
  arrives with its prompt — `5,647` tokens on the measured row — while the event
  that actually stops a step reading is the block arriving with its first tool
  call, `41,927` tokens, 7.4x larger on the same turn. Both weights now derive
  from the whole start, prefix plus first call: on the probed row the threshold
  moves `$0.01` to `$0.06`, and a `$0.05` step that would have been admitted to
  spend its entire allowance being handed its own prompt and one tool result is
  refused. `allowance.FirstEventWeight`/`WarmFirstEventWeight` are
  `StartWeight`/`WarmStartWeight`, named for what they weigh. The refusal names
  the two blocks separately with the second one's size, since that is the number
  doing the work, and still reports the cold figure as what one run of the hour
  pays once.
- **An answer that stated no coverage was recorded as a whole one.** Both
  schemas mark `completeness` required and the provider does not enforce it:
  four fields were demanded and two arrived, on a step that came back `ok`
  while its own summary began "I cannot describe this project". `coverage`
  read absent and "claimed 1" through the same branch, so the silence was
  stored as success and cleared every `on = "ok"` edge downstream. It is now a
  refusal — `invalid_input`, with the turn's charge kept — and a stated claim
  is written to the record even when it is 1, so `completeness` NULL means one
  thing only: an agent type that calls no model. An over-claim above 1 is
  clamped to whole rather than dropped, since dropping it would refuse a turn
  for saying nothing when it said too much. `2 of 210` stored steps carried the
  field; every model-backed step now does or is refused.

- **The rescuable threshold charged every step for a cache write that happens
  once, and refused ten of eighteen adequately funded steps as a result.** The
  rule shipped hours earlier weighed a turn's first assistant event
  cold-equivalent — the whole prefix as cache write, x2 — on the reasoning that
  any step might be the first to pay for it. Measured the same night against a
  loopback recorder and two live probes, that is wrong by 20x: the prefix is
  written once and read at x0.1 by everything after it, with `cache_read`
  pinned at exactly 40,227 on turn 2 and 4,772 on turn 1 across five runs of
  different objectives, different files and different nonces. `workflow create`
  now refuses against `allowance.WarmFirstEventWeight` — the same stored row,
  the same arithmetic, cache read instead of cache write — which moves the
  threshold on the real explore row from $0.65 to $0.04 with no re-measurement.
  The cold figure travels beside it: `atenea floor`'s `RESCUABLE` column is the
  warm per-step number, its `USD` column the one-time cold one, and the refusal
  names both so a person sizing a grant can see what the hour's first run adds
  and that no other run pays it.

  The floor itself, $0.27, was deliberately left cold and is now the only rule
  refusing anything. The probes showed the cost that kills a step is a
  ~40,000-token block arriving at the *first tool call*, and no stored
  measurement contains it — the row on disk carries the turn-1 prefix. A warm
  floor derived from that row would be a confident number about the wrong
  quantity, which is the failure mode this project keeps a page about. It needs
  `atenea floor measure` to make a tool call and record what comes back warm.

- **The `Models` header was shipped one column too wide and wrapped in silence.**
  A `sidebar_content` text gets 37 columns, not the 42 the column is wide, and the
  renderer wraps rather than clips — measured with rulers drawn inside the column at
  200, 130 and 121 columns of terminal. The worst real case on this machine,
  `▶ Models (deepseek-v4-flash-free 100%)`, is 38 and was quietly costing a second
  row: a collapsed section that takes two rows is not collapsed. The model name now
  gives way, marked with an ellipsis, because it is the only part of that header
  whose length is unknown and the full name is one click away in the body. The
  budget is a named constant in both widgets, and the earlier claim that the column
  gives a widget 42 columns is corrected in the docs.

- **The rate-limit sections let a broken reading render as the most ordinary answer
  on the screen.** Every other panel here separates a failure from a silence —
  `Models` says `sin lectura` rather than showing stale shares — and this one, alone,
  did not: an unreadable store and a provider with nothing live both drew nothing at
  all, which is the normal state of that part of the column. The source is another
  product's private store, reached by a fixed path with no declared schema, so the
  day it moves is a question of when, not if, and the answer would have been an
  absence nobody could distinguish from a quiet Tuesday.

  Now three states, not two: a report read and understood with nothing live draws
  nothing; a store that will not open, or opens with a shape this file cannot parse,
  or names a provider without a readable `fetchedAt`, draws one amber
  `Claude sin lectura`; and a provider absent from the store draws nothing, because
  a machine that does not use codex is not a machine with a broken widget. The
  failure line keeps polling, so a store repaired under a running client turns back
  into the section on the next poll — measured, along with the line itself, from a
  store with one field renamed.

  Paired with a test that reads omp's **real** store and fails if an `anthropic` or
  `openai-codex` report stops carrying `fetchedAt`, a percentage with `usedFraction`,
  or a window with an `id` and a `durationMs`. It skips where there is no omp, which
  is every CI runner, and it was verified to fail on a renamed field and on a missing
  table rather than only to pass on a good one.

- **The status light travelled as a number, which pinned its meaning to
  declaration order.** `Light` is an iota, so the wire carried `1` for amber —
  and inserting a state between two existing ones would have silently
  reinterpreted every reading a consumer had already mapped. Nothing reveals
  that until somebody adds a light: the code compiles, the tests pass, and a
  reader paints the wrong color with no way to tell. It is the one field on
  this screen where a wrong answer is indistinguishable from a right one, since
  green, amber and red are all plausible. The light now travels as its name, out
  of a single table the screen, the encoder and the decoder all read.

  The decoder still accepts a number, and that is not tolerance for its own
  sake: an upgrade replaces the binary while a service from the previous version
  still holds the socket, so a command and the service it asks disagree about
  this encoding until somebody restarts. `Asked` returns false on any decode
  error, so a strict reader would have reported "no service" for that whole
  window and dropped the screen to the command's own view. Measured in both
  directions against a service on an isolated state root: the new reader against
  an old service reports `process service` and the old service's own version;
  the old reader against a new service falls back to `process command`, which is
  a degradation the screen names rather than a color it invents.

  Unlike `String`, which still answers `red` for a value outside the table
  because a status line should draw the worst case rather than refuse, the
  encoder refuses outright. Whoever adds a fourth light meets that error in the
  first test that encodes a status, which is where it is cheap.

- **The stale-build check in `measuring-the-wrong-process` could only ever say
  "stale".** It compared `stat -Lc %i` on `~/.local/bin/atenea` against the same
  on `/proc/<pid>/exe`, and that magic symlink is resolved inside procfs, so
  `stat` answers with procfs's own device and inode rather than the file's — one
  `bash`, named both ways, reports `dev=26 inode=71303137` and `dev=64513
  inode=4980754`. The two numbers the page printed as evidence of a real drift
  were that artefact. The check now reads the inode the kernel mapped, from
  `/proc/<pid>/maps`, matching the path *field* because a replaced file renders
  as `… /atenea (deleted)` and an anchored line pattern stops matching in
  exactly the case being checked for; an unreadable mapping is refused out loud
  instead of comparing against an empty string. Equal, unequal and unreadable
  were all three exercised before it was written down.

  Found by running the check once after tagging 0.10.1, which is the only reason
  it was ever run against a service that had just been restarted.

## [0.10.1] - 2026-08-10

A patch: seven defects fixed and five additions, no contract change.
`pkg/contract` stays at `3.0.0`, so an adapter built against 0.10.0 needs
nothing.

Worth saying where this one came from. The three Serena and registry fixes at
the end of `Fixed` were not on any plan — they came out of using the thing:
an overview of a real file that failed over a name nobody asked about, a
snippet that arrived empty, and a repository that could not be named by the
one thing a caller knows about itself. So did the two before them, each found
by driving a client rather than by reading a test that passed. A plan finds
what it thought of; this list is mostly what the plan did not.

### Fixed

- **A backend that asked Atenea a question was never answered, and hung.** The
  protocol runs in both directions, but the stdio passthrough read every line
  carrying an `id` as a *response*. A server's *request* carries an `id` too,
  so it was delivered to a caller waiting on the id the server had chosen —
  nobody, since Atenea's ids start at `1` — and dropped. The server then waited
  for a reply that was never coming. Semgrep asks `roots/list` before it will
  answer its first `tools/call`, so its entire surface was unreachable: every
  call died on its 25 s deadline with the process healthy and idle, and because
  the process is shared, one unanswered question wedged it for every chat
  attached to it. A request is now told `-32601`, against the id the server
  used, echoed verbatim so a string id comes back a string. Refusing is the
  honest answer rather than a stopgap: the handshake declares no capabilities,
  so a server asking for roots, sampling or elicitation is asking for something
  Atenea never offered — what it must not get is silence.

  Found by counting the child's bytes rather than believing the timeout:
  `/proc/<pid>/io` showed 106 in and 47 back on a call that reported nothing
  arriving. The 47 bytes were `{"method":"roots/list","jsonrpc":"2.0","id":0}`.
  Direct probes had looked fine only because they mistook that request for the
  reply.

- **`wrap` handed every client the backends Atenea answers for, and never
  handed it Atenea.** The command exists to point clients at the core, but it
  emitted `serena` and the graph backend — the two backends behind all eight
  capabilities — straight into the client's configuration, so a client pointed
  at them reached past the funnel to the servers the funnel is made of, under
  no allow list and no effect check. Atenea itself appeared in no payload at
  all. A capability-backed backend is now `Held`, reported like the others and
  deliberately kept out, and the core is the one entry every payload carries.

- **`wrap` announced a raw tool surface for backends that have none.** The
  report prints a reason beside every held server, and printed the same one
  for both kinds of hold: `served as raw.<id>.<tool>`. Only a backend carrying
  `expose = "raw"` is re-offered under that name. `serena` and
  the graph backend are held because the capabilities run on them, and Atenea
  serves no tool of theirs at all — measured on the machine this was found on,
  `tools/list` returned 19 raw tools, every one of them `chrome-devtools`,
  `context7` or `semgrep`, the three that declare `expose`. So the report sent
  an operator looking for tools no `tools/list` would ever return. The row now
  says which of the two holds it is, and the check whose whole job is stopping
  a client from believing an unverified claim stopped making two of its own.

- **A chat opened by a client held nothing, so `client_effects` was a ceiling
  with no way to reach it.** `Core.Open` copied whatever its caller asked for
  into the chat's grant, `initialize` asked for nothing because the protocol
  gave a client no way to say, and `Session.Allows` consults that grant alone.
  Every chat therefore opened empty: a raw tool declaring anything beyond
  `read` was refused on every machine whatever the settings file said, and the
  shipped `client_effects = ["process"]` default could not be exercised by
  anybody. A chat now opens holding what the operator grants clients —
  `Floor.Or(standing)`, the same reading an absent key has always had.

  Found by driving a real client against a declared backend, not by reading
  the key's tests: all of them passed, because each one handed `Open` the
  grant it then asked about.

- **One symbol Serena could name but not place failed the whole file.**
  `get_symbols_overview` reports bare names and `find_symbol` is then asked
  where each one is. Anonymous callbacks and generated names are named by the
  first tool and locatable by neither its own `find_symbol` nor any language
  server, and a single one of them turned an overview of a real file into
  `not_found` — the caller lost every symbol in the file over one it had not
  asked about. Unplaceable names are now skipped and counted in the discovery
  notes, the reading the adapter already had for "Serena answered, but not
  about this". A name that is missing for any other reason is still a failure,
  and the count stops a skip from being silent.

- **A snippet was empty whenever Serena returned a symbol without a body.**
  The adapter trimmed whatever body came back to the requested line count, so
  a symbol reported with a path and a line but no body produced a location
  with nothing to read beside it, on the capability whose entire point is the
  fragment. The lines are now read from the file at the reported position when
  the body is absent, through the same reader the rest of the adapter uses —
  so a path the settings file marks sensitive is still refused out loud rather
  than quietly widened by the new route.

- **A caller had to know what the operator named the directory it was already
  working in.** `Repository` matched registered ids alone, so a client or a
  commission naming an absolute path — the one thing every caller does know
  about itself — was answered `unknown repository`. An absolute path now
  resolves to the repository containing it, longest configured path first, so
  a repository nested inside another wins over its parent. A registered id is
  still matched before anything is treated as a path, and a path outside every
  configured repository is still `not_found`: this widens what resolves, not
  what is accepted.

### Added

- **`wrap` supports Claude Code and codex**, alongside OpenCode. Both take
  their configuration on the command line rather than from the environment,
  which is the same promise by another route: `claude --mcp-config <json>`,
  added to every source Claude Code already resolves, and one `-c
  mcp_servers.<id>={...}` per server for codex, each setting a single key of
  the table. Neither writes anything, so a client launched without `wrap` is
  still a client with exactly the configuration it had before. The codex
  overrides are deliberately per-server: `-c mcp_servers={...}` replaces the
  map, and a user's own `config.toml` servers would vanish for the length of
  the session. Measured end to end against codex 0.146.0 — the injected
  `atenea` arrived and the user's `claude-mem` and `mcp-search` were still
  listed beside it.
  Claude Code was measured too, and the flag's placement is part of the
  feature rather than a detail: `--mcp-config` is variadic and also global, so
  in front it eats a following subcommand and behind one it is rejected as an
  unknown option. It goes in front, with a `--` inserted when the user's first
  argument is a bare word. Shipped the other way first, which worked for a
  session and killed every `claude mcp list` run through the wrapper. That a
  session honours the injection is measured, not read from the docs: a server
  whose command touches a file on start left the file behind. `claude mcp
  list` cannot see it — that subcommand reports the servers on disk and
  ignores the flag, even under `--strict-mcp-config`.
- **`atenea wrap --help` names the client it will not take, and why.** `omp`
  reads MCP servers from `mcp.json` files and nothing else: no config-content
  variable, and its `--config` overlay feeds a settings tree whose schema has
  no `mcpServers` key, so an inline overlay could not carry a server either.
  Supporting it would mean writing that file, which is the guarantee wrap
  exists to keep. An omission a reader has to discover is the failure this
  command was written against.
- **A client may ask at `initialize` to hold less than it was granted**, under
  `capabilities.experimental.atenea.grant`. Asking for more is refused there
  rather than at the first `tools/call`, and so is an effect name nobody
  recognizes — a client that misspells `write` asked to be restrained, and the
  full grant would be the opposite of what it asked for. `read` is always
  free, so naming it is always a narrowing.
- **The handshake reports the grant the chat ended up holding**, in the same
  block. Before this the only way a client could learn its own permissions was
  to make a call and read the refusal.
- A chat that narrowed itself pins the floor its commissions carry to the same
  answer, so the door and the step refuse the same things.

## [0.10.0] - 2026-08-08

`pkg/contract` bumped to `3.0.0`. Like `2.0.0` it is a removal, not an
addition: `Repository.SerenaEndpoint` is gone, so an adapter that named it
stops compiling and `Supports` refuses the whole `2.x` line.

**Upgrading is two lines, in this order,** because the core reports them one
at a time and the key is checked before the contract:

```text
settings ~/.config/atenea/atenea.toml: unknown key(s): repository.serena_endpoint
settings ~/.config/atenea/atenea.toml: contract 2.3.0 is not supported by
this core (3.0.0): change the contract line to "3.0.0"; no other key moves
```

Delete the `serena_endpoint` line from every `[[repository]]` that carries
one, then change the contract line. A repository that had a pinned Serena
keeps its dedicated process by declaring the policy once instead:
`instance = "per_repository"` under `[orchestrator.serena.process]`, with
`{{project}}` in `args` and no fixed `port`. Only then do the two systemd
units come out -- until the new binary is installed they are still what the
old one reads.

### Added

- **`[orchestrator.serena.process].instance = "per_repository"` runs one Serena
  per repository instead of retargeting one.** `activate_project` is
  process-wide, so a shared instance tears its language server down and
  reindexes on every alternation between two repositories -- and this machine
  had already answered that by hand, with two systemd units differing only in
  `--port` and `--project`. One declaration now expands to one supervised
  process per `[[repository]]`, each launched against its own project through a
  new `{{project}}` placeholder, each on its own port, each named
  `serena@<repository>` on the status screen. `shared` stays the default and
  the behaviour every managed server had before the key existed. Measured
  against the real binary: two repositories, two processes, one declaration,
  and both reaped when Atenea stopped. The two hand-written `serena` systemd
  units this replaces -- one per repository, differing only in `--port` and
  `--project` -- come out at install time, not before it: they are what the
  currently installed binary still reads through the key removed below, so the
  order is install, migrate the settings file, then `systemctl --user disable
  --now serena.service serena-desktop-remote.service`.
- **Health is recorded per repository.** Found by driving the above rather than
  by reading it: Serena has no TypeScript language server on this machine, a
  call on a TypeScript repository came back `unavailable`, and the Go
  repository next door -- answered by its own healthy process three seconds
  earlier -- was then refused with `every implementation of symbol.definition
  is down`. A provider is not up or down in the abstract. What probing finds is
  now keyed by repository, the funnel filters on what the repository in front
  of it found, and the declaration in the settings file is never overwritten by
  a probe. `atenea status` names the repository that found the failure:
  `health=down (on web: ...)`.
- **A `command` backend declared `expose = "raw"` is now one process shared by
  every chat.** This is the number the feature was for: a stdio MCP server has
  no address, so a client that is not handed one has no choice but to spawn a
  private copy, and this machine was running five copies of one indexer, each
  holding its own index of the same repositories. Atenea spawns it on the first
  call that needs it, replays the handshake once per process rather than once
  per chat, holds stdin open for the life of the process -- a stdio server
  reads EOF on stdin as its client leaving -- and routes each answer back to
  the chat that asked by request id. Measured against the real
  the graph MCP backend: three chats, one child, the budget refusing a tool
  outside it, and the child gone when Atenea stopped.
- **A dead call is not silently repeated.** A `tools/list` that died is asked
  again of the replacement process, because nothing happened. A `tools/call` is
  not: the server may have run the tool and died carrying the answer back, and
  this cannot tell that apart from dying before it started, so re-sending it
  would run a declared `write` twice because a pipe broke. The caller is told,
  and the next call gets a fresh process.

- **`expose` on an `[[mcp_server]]` block, and the `raw.` namespace it
  uses.** The declaration list has always meant one thing -- point a client at a
  server that is already running, then step out of the path. `expose` names
  that behaviour `off` and reserves `raw` for the other one: Atenea holding the
  connection and re-offering a backend's own tools verbatim as
  `raw.<server>.<tool>`. The field is parsed and its value checked; an unknown
  value is refused rather than read as `off`, because a backend an operator
  believes is reachable and nothing offers is the failure this whole list
  exists to prevent.
- **A declared `raw` backend is now dialed, listed and called** (contract
  `2.2.0` -> `2.3.0`). Atenea holds one session per backend, opened on the
  first call that needs it rather than at startup -- a server that is down
  must not stop Atenea starting, and one that comes up later must start
  working without a restart. Its tools appear on `tools/list` after Atenea's
  own capabilities, named `raw.<server>.<tool>`, carrying the backend's own
  input schema unedited: no `repository` argument is added, because a raw tool
  has no idea what a repository is. A call is forwarded whole and answered
  whole, and a backend that does not answer is left off the list rather than
  listed as broken.

  What it deliberately does not touch is the reason it is a separate path: no
  funnel, because there is nobody to choose between; no capability, so no
  schema of Atenea's is checked against an opaque payload; and no row in the
  measurement base, because latency with no competitor is not evidence in a
  decision. Measured against the live `semgrep` server on the author's
  machine: one client listed 8 capabilities and 4 `raw.semgrep.*` tools
  together, and a real call came back with a real answer.
- **Every step's receipt now records its funnel as one field with three
  states.** `kept` carries each stage with its survivors and the reason every
  candidate was dropped; `not_kept` says the trace was not recorded; `none`
  says the step never had a funnel because it was a passthrough. Two adjacent
  explanations for the same silence is exactly the drift this avoids -- before
  this, a passthrough step and a step whose trace was lost read identically.
  A raw call files a receipt of its own, `kind = "raw"`, written closed
  because there is no plan to resume and nothing later can pick it up.
- **A raw backend must declare `tools`, and only those are offered or
  callable.** The allow list has no default and an empty one is refused. Both
  readings of an absent list are defensible -- offer everything, offer nothing
  -- which is exactly why neither may be guessed: one silently widens a
  machine to whatever the backend ships next week, the other is a declaration
  that does nothing. The list is enforced at the backend rather than by
  whoever is listing, so a tool that never appeared on any list is still
  refused when a client sends the name anyway.
- **A raw backend must declare `effects`, and every call is held to them.**
  Atenea cannot infer what somebody else's tool does: a backend's own list can
  hold `execute_shell_command` beside `find_symbol`, and nothing in a name or
  a schema says which is which. `effects` covers the server,
  `[[mcp_server.tool]]` narrows it for one tool, and a tool with no block of
  its own causes what the server declared -- so "undeclared" is not a state
  that can reach a call.

  The check is the one a capability already crosses, `Session.entitled`, not a
  gate of passthrough's own: reading is free, anything else needs the chat's
  grant. Measured against the live `semgrep` with two of its four tools
  allowed: `get_supported_languages` answered, `semgrep_scan` was refused for
  `process`, and `semgrep_rule_schema` was refused for being outside the list.
  All three left a receipt carrying the effects they were measured against.
- **A raw backend is held back from `atenea wrap`.** Every other declared
  server is handed to the client so it can talk to the shared copy directly.
  Doing that for a raw one points the client past the allow list and the
  effects check -- the budget routed around by the command meant to apply it.
  They are still probed and still reported, under a third heading, `held`.
- **`expose = "raw"` on a `command` entry is refused.** Passthrough is
  HTTP-only: a stdio backend needs one process shared between chats, with
  fan-in over a single pipe and a lifecycle outliving any one chat, and that
  is not built. Accepting the declaration would offer nothing and say nothing.
- **`raw` is refused as the first segment of any capability or implementation
  id.** A capability called `raw.search` would be indistinguishable from a
  passthrough to a backend named `search`, which would make the absence of a
  funnel invisible in the one place a caller reads -- the name. Reserving it by
  refusal rather than convention is the difference between a rule and a habit a
  catalogue drifts away from.
- **An `[[mcp_server]]` id containing a dot is refused.** `raw.<id>.<tool>` can
  only be split back into a server and a tool while the id is one segment, so a
  name that could not be parsed later is refused when it is written.

### Removed

- **`[[repository]].serena_endpoint` is gone; contract `2.3.0` -> `3.0.0`.** It
  answered "which Serena serves this repository" one repository at a time, and
  pointed at a process Atenea did not start, watch or stop -- so a repository
  could name an address with nothing behind it and only find out on the call.
  `instance = "per_repository"` answers the same question once, for processes
  Atenea owns. Removing a field is a major: a settings file declaring a `2.x`
  contract no longer loads, and the key is now refused as unknown rather than
  parsed and ignored, which is the failure that rule exists to prevent.

### Fixed

- **Nothing ever closed a declared backend on shutdown.** It cost nothing while
  every backend was an HTTP session -- `Close` only forgot a session id -- but a
  stdio backend is a child process, and one left behind per restart is how a
  machine accumulates the copies this feature removes. Both of `Shutdown`'s
  exits now release every backend, after the door is shut and in-flight work
  has had its margin.

- **A receipt's `effects` were written as base64.** `contract.Effect` is a
  `uint8`, so a list of them is a `[]byte` to `encoding/json` and a real
  receipt read back `"effects":"AAM="` -- a record of what a run was
  authorized to cause, in a form nobody can audit, which is the only reason
  the field is written down. They now marshal as their names and unmarshal
  from either, so receipts filed before this still load.

## [0.9.1] - 2026-08-08

### Fixed

- **A repository whose path is not on the disk blamed the adapter binary and
  took the provider down everywhere.** Every adapter turns a repository's path
  into its child process's working directory, and Go reports a missing
  `cmd.Dir` by naming the *binary*: a commission over three repositories, one
  of them a path that did not exist, failed with `omp could not be started:
  fork/exec /home/me/.local/bin/omp: no such file or directory` for an `omp`
  that was installed, executable and had just answered two other steps in the
  same wave. The bin was the worse half -- `unavailable` is the bin that
  condemns a provider, so health went down for `code.search` globally and the
  second wave failed on every repository with "every implementation is down".
  One wrong line in the settings file disabled a working tool for the whole
  machine. There is now a gate beside the permission gate, on the same seam and
  for the same reason: it names the repository and the directory, refuses
  before anything is spawned, and files it as `invalid_input` -- a bin the
  health record ignores, because nothing is wrong with the provider.

## [0.9.0] - 2026-08-08


### Added

- **A settings file can say what a connected client may do, separately from
  what the operator's own console may do.** `[orchestrator] client_effects` is
  the standing grant for a chat an MCP client opened. It ships equal to
  `effects` and separate from it: an absent key inherits, so no file written
  before today behaves differently, but the inheritance happens once when the
  settings are read and the two are separate lists after that. Widening your
  own floor for an afternoon at the terminal no longer widens every client
  that connects, silently and permanently, with nothing able to say otherwise.

  An empty list is a real answer -- a client may read and nothing else -- which
  is why the key is a list that may be empty while `effects` is one that may
  not. It is not the default: on this machine every implementation of
  `code.search` is a binary, so read-only-for-clients would refuse the headline
  capability on day one, and a default that does that teaches people to turn
  the default off rather than teaching caution.

  It is a ceiling as well as a floor. A chat cannot be opened claiming more
  than it, and is refused at `initialize` rather than at its first
  `tools/call`. The ceiling arrives with the line that sets it: a file that
  never wrote one has no ceiling, because "I said nothing about clients" must
  not quietly become "clients may hold nothing".

  `atenea status` prints both, as `standing` and `clients`, and marks the
  second `inherited` when it is a copy -- two identical lists with nothing
  between them look like two decisions when only one was made.
- **Atenea is an MCP server, and clients connect to it.** The premise the whole
  design was built around, working end to end for the first time: every
  capability in the settings file is a tool, offered over the socket the
  service already opened, answered by whichever implementation the funnel picks
  at the moment of the call.

  `atenea mcp` is the bridge a client launches -- newline-delimited JSON on
  stdin and stdout, relayed to the service and nothing more. It parses nothing
  and decides nothing, because a bridge that understood the protocol would be a
  second place the answers could differ. It is deliberately not a fallback into
  running Atenea in-process: that would give each client its own core and its
  own catalog, which is the arrangement this design exists to replace.
  `atenea mcp --check` reports whether a service is listening without going
  through a client, because clients show one line and hide the reason.

  Each connection is one chat, named by the `clientInfo` in its handshake and
  closed when the client hangs up. Tools are refused before the handshake:
  until a client says who it is there is no chat for the work to belong to,
  which would make it untraceable and ungoverned. A chat asks for no grant and
  is given none -- it can look, and it runs under the settings file's standing
  grant, because a session grant only ever widens the operator's floor.

  Every tool takes a `repository`, which no capability declares: the unit of
  work is Atenea's question, not the capability's, and a model has no `--repo`
  to reach for. Required only when more than one is registered, matching what
  the CLI already does. The tool's `inputSchema` and `outputSchema` are the
  capability's own declarations from `2.2.0`, which is what that release was
  for.

  Verified against Claude Code, not a fake: it listed all eight tools and
  called `code.search`, which reached ripgrep through the funnel and came back
  with the file, line, column and snippet.

- **The status screen shows who is connected.** `atenea status` has a `chats`
  table: one row per client, with its name, how long it has been there, how
  many runs it has asked for and what it may authorize. There was never a chats
  table before -- not an empty one, none at all, because nothing could have
  filled it. Two clients at once is the only way the isolation between them
  stops being a claim in a design document.

- **The service has a door, and `atenea status` knocks on it.** A Unix socket
  under the state root, opened by the service and by nothing else, speaking the
  same JSON-RPC line protocol MCP does. `atenea status` now asks the running
  service for its view instead of working one out from disk, and the `process`
  line says which it got. The half of that screen that only the service can
  know -- the uptime, the clock's real run, the chats open right now -- reaches
  a reader for the first time.

  It asks only when the service is answering about the same settings file the
  command was asked about: naming a file is a different question, and a service
  running another one is not the answer to it. Anything else -- nobody home, a
  reply that is not the protocol, a door that opens and never speaks -- falls
  back to the local screen, because the caller's whole reason for asking is
  that it has a worse answer ready.

  The door is this machine's: the socket is `0600` in a `0700` directory, and
  every connection is checked against the kernel's own answer for who opened it
  (`SO_PEERCRED`) before a byte is read. A killed service leaves the name
  behind, so a leftover is probed and cleared rather than treated as occupied
  -- one `SIGKILL` must not lock the machine out until somebody deletes a file
  by hand.

- **A capability can state its own shape as JSON Schema, on both sides**
  (contract `2.1.0` -> `2.2.0`). `Capability.InputSchema` and
  `Capability.OutputSchema` turn the declaration into the one form a caller who
  has not sent a payload yet can act on -- a model being asked to fill in a
  form, or a client reading a tool list.

  The generator already existed inside the Claude Code adapter, wired to the
  outputs, because a model has to be told what form to answer in. Only half of
  that reason was ever adapter-specific: a caller building a *request* needs
  the same statement about the inputs, and the only place that can honestly
  make it is the package that also refuses the payload. `ValidateInput` and
  `InputSchema` are now one declaration read in two directions, in one package,
  with a test that fails the moment the two disagree about any payload.

- **A chat can dispatch one capability, not only a whole commission.**
  `Session.Ask` is `Session.Do`'s isolation applied to the shape a single
  `tools/call` arrives in: what the chat may authorize going in, what it may be
  told coming out, and the run attributed to it on the receipt. `Core.Ask`
  already took that shape but is the console's door -- it trusts the effects it
  is handed, because somebody standing at a terminal is the user and there is
  nobody above them to ask. A client speaking for a chat is not, and until now
  it had no door of its own.

  The two halves are shared with `Do` rather than copied, and neither is the
  dispatch path's own check: that one compares a capability's declared effects
  against the permission the step carries, which is a question about the
  machine's policy rather than about who is asking. What a chat is entitled to
  ask for in the first place is only knowable here.

- **The run report says what the commission took, not only what it cost.** A
  wave charges every step it ran and spends one stretch of the operator's
  afternoon, so those are two numbers: `spent 3.781s of tool time over 4
  step(s), 2.618s elapsed`, and per phase `explore 2 step(s), 1.588s in 811ms`.
  Only the sum was ever printed, which is the larger of the two -- a run that
  took 2.6s was reported as 3.7s, and a parallel run was indistinguishable from
  a queue. `--json` carries `elapsed_ms` at both heights beside `spent_ms` and
  `closed_at` per step, and every entry point stamps it: a commission, a single
  `ask`, and a `resume`, which redispatched real work and reported `0s elapsed`
  beside a step that took 714ms.

  What makes a wave wide is **repositories**: the work is split per repository,
  so a settings file with one `[[repository]]` produces waves one step wide
  however high `max_parallel` is set. That is now written down where the ceiling
  is documented, because it is the whole reason the gap could sit unnoticed. The
  machinery has been correct and covered by tests since the first orchestrator
  -- one of them asserting overlap across two repositories all along, because
  the fixture registers two while the shipped file registers one -- and on this
  machine it had never once been asked to run two steps at the same time. It has
  now: two repositories, both explores at once and both searches at once, the
  explore pair overlapping for 775ms of their 777ms.

### Changed

- **The schema Claude Code is held to now refuses keys the capability never
  declared.** `additionalProperties: false` is emitted at every depth, which is
  what `ValidateOutput` has always enforced on the answer coming back. The ask
  and the check were describing different shapes: a model that invented a field
  was told nothing going out and refused coming in. Verified against the real
  CLI, which accepts the closed schema and answers within it.

### Fixed

- **A chat could not be granted `process`, so no chat could run `code.search`.**
  The guard on `Open` matched the grant against a list of three effects retyped
  in `internal/core`; the contract declares four. `process` was added to the
  contract after the guard was written and a `switch` does not notice. Since
  `code.search` declares `read` and `process` together -- ripgrep is both -- and
  it is the first capability any client calls, every chat was refused the P0
  capability at the door.

  The guard now asks the contract instead of holding its own copy of the answer,
  so the next effect added cannot repeat it, and a test walks every effect the
  contract declares rather than a list retyped in the test.

- **A fresh settings file classified its repository `small`, and two
  capabilities were unreachable because of it.** The shipped `[[repository]]`
  wrote `scale = "small"` -- a size nobody had measured, on a repository the
  file had just invented -- while `vcs` on the line below correctly shipped
  unspecified with a comment saying why. `symbol.calls` and `code.impact` are
  the two implementations that ask for a medium repository or bigger, so both
  were dropped on day one. The funnel reported the drop accurately, which made
  them look unimplemented rather than unclassified. The settings page had
  already written the rule being broken: an unknown fact is not a proven
  mismatch, and dropping candidates over one silently empties the funnel.
  `scale` now ships empty, and a test asserts the shipped repository classifies
  nothing and that no shipped implementation is dropped over a size nobody
  measured.

- **`atenea metrics` printed one implementation on two rows without saying what
  split them.** The base keys on the tool version on purpose -- yesterday's
  numbers for yesterday's binary are history, not a baseline -- and the table
  never had that column, so the same capability, implementation and repository
  appeared twice with no visible reason. An attempt refused before the far side
  ran has no version at all, which is exactly when the duplicate shows up. The
  version is now a column, `-` when there is none. The columns were also fixed
  widths guessed before the catalog existed: `symbol.implementations` and
  the graph overview implementation both outgrew theirs and shifted every row six
  characters off its header. Widths now come from the rows.

- **The status screen counted one call as a measurement while every decision
  below it said `estimated`.** The funnel does not believe an implementation's
  own numbers over its declared estimate until it has two of them -- one can be
  a cold cache -- but the caption counted any implementation with a single
  successful call. On a machine part-way through its break-in that printed
  `measured for 10 of 11 implementations` above traces that all read
  `(estimated)`, which is precisely the misunderstanding the measurement base
  exists to prevent. The count now uses the funnel's own threshold, so the two
  answer the same question.

- **A step's close time on the receipt was the wave's, not the step's.** The
  recorder runs after a whole wave returns and read the clock there, so every
  step in a wave was filed with the same instant. Read the way `closed_at`
  beside `duration_ms` invites -- as an interval -- that moved the quick steps of
  a wave to the back: a 440ms step that started with its partner appeared to
  start 1.27s later, after the slow one had almost finished. The stamp now comes
  from the step's own close, which is the single exit every path already goes
  through, so two intervals show the overlap they had.

## [0.8.0] - 2026-08-07

### Added

- **`atenea wrap CLIENT` launches a client with MCP servers Atenea checked a
  moment earlier.** The servers come from new `[[mcp_server]]` blocks in the
  settings file; each one is handshaked before the client starts, and only the
  ones that answered are declared to it. Measured on the machine this was
  written on: of six declared servers one was refusing connections while the
  supervisor in front of it reported `running`, and it had been doing so for
  five hours. The client that used it listed it as connected-until-used; the
  supervisor logged nothing.

  **`declared` means the server answered one MCP handshake. It does not mean
  its tools work**, and the report says so on every run, including a run with
  nothing wrong -- `5 declared, 0 refused` is exactly the moment the word is
  trusted furthest and read least. The gap is real: on the same machine a
  server answers the handshake in under two seconds and every call to its
  primary tool fails, and had been failing for days. Checking further means
  calling tools, and calling a tool is doing work with side effects and a
  bill. The handshake is the last thing that is free.

  A refused server is left **out** of the payload rather than switched off.
  The client deep-merges what Atenea generates over its own config, so an
  absent key leaves whatever the user already had in place: Atenea declining
  to vouch for a server is never the reason a working one disappears.

  Nothing is written to disk. The configuration lives in one environment
  variable for the lifetime of the child process, which is what makes it safe
  to try -- run the client without `wrap` and it has exactly the configuration
  it had before. There is no `unwrap` because there is nothing to undo.

  `opencode` is the one client wired today, via `OPENCODE_CONFIG_CONTENT`.

- **`[[mcp_server]]` settings blocks.** `id` plus exactly one of `url` or
  `command`, with optional `env` and `timeout`. A block naming both, naming
  neither, carrying a relative or non-http URL, or repeating an `id` already
  declared is refused at load: the payload is keyed by `id`, so a duplicate
  would silently make one of the two dead. This list is not the catalogue and
  nothing dispatches against it -- Atenea reaches its own providers through
  adapters.

- **`enum` on a capability's string field, closing it to a fixed set.**
  `pkg/contract` bumped to `2.1.0` — additive: an adapter built against
  `2.0.0` compiles unchanged and leaves the slice nil, which closes nothing.
  A file targeting `2.0.0` still loads; the shipped file now declares `2.1.0`
  because it uses the field.

  Three sets are declared today, and they were already written in prose:
  `symbol.calls` input `direction` (`incoming`, `outgoing`, `both`), its
  output `direction` (`incoming`, `outgoing` — a hop is found walking one way,
  never both at once), and `repository.index` `mode` (`fast`, `moderate`,
  `full`). A refusal names the whole set, because the caller this exists for
  cannot be asked and the message is the only place it learns.

  It is opt-in for a reason: `symbol.overview`'s `kind` deliberately stays
  open, since a provider names symbol kinds in its own vocabulary and closing
  that set would refuse honest answers. Numeric bounds were left out — a range
  in the contract binds every implementation, and a line number is bounded by
  the file, not by the capability.

  The generated JSON Schema carries the set on the node holding the value:
  for a `string_list` that is `items`, not the array, because a set says which
  words may appear and never how many.

- **The status screen names which process it is.** A new `process` line, `service`
  or `command`. Every other fact on that screen is true whoever prints it, which
  is a rule the background section was built around; the repair is now the one
  exception, so the screen says which of the two you are reading rather than
  leaving a silent `recovered` line to be read as "nothing was repaired".

### Fixed

- **A command no longer sweeps the receipts of a service that is running.** The
  recovery pass deleted every `*.tmp` it found on every core construction --
  which is to say on every subcommand, not just on the way up -- and
  `Store.Recover` holds a mutex for the whole pass precisely so that "a dump
  being written right now cannot have its temporary file swept out from under
  the rename". A mutex cannot do that across processes. An `atenea status` run
  beside a live service could delete the file a run had open, and that run then
  reported a checkpoint write failure for a reason that had nothing to do with
  it.

  Upkeep now belongs to one process and is claimed on disk, in
  `upkeep.lock` in the state root -- beside the receipts and the base it
  protects, and deliberately not in `$XDG_RUNTIME_DIR` where a lock of this kind
  would ordinarily go: that variable is set for a `systemd --user` service and
  for a login shell but unset under cron, so the two would claim two different
  files and both go on sweeping. A lock only excludes people looking in the same
  place. The state root is derived from `HOME`, so every process sharing the
  state shares the claim on it. The service sweeps receipts and ticks the clock; a second `atenea run` is refused
  `unavailable` and the refusal names the pid that already holds it -- which is
  why the claim is a file carrying a pid rather than a kernel lock, since "no"
  without a pid leaves an operator with two processes and no way to tell which
  one to stop. Every other command runs beside the service exactly as before:
  the refusal is for the upkeep, not for using Atenea.

  The clock had the same exposure and survived it by luck: `Run` is what starts
  the beats and only `atenea run` ever called it, so the invariant was true by
  accident. It is now declared, and the mechanism the resume lock already used
  is shared rather than copied -- `internal/pidlock` is the one implementation,
  and `checkpoint.Lock` calls it.

- **Resuming a torn receipt nobody has set aside says what closes it.** Setting a
  torn receipt aside is the service's job and only the service's, so on a machine
  whose service has not started since the cut nothing has done it yet and the
  loader meets the unparsed file directly. It refused with the parse error alone
  -- `unexpected end of JSON input` -- which names a real fault and no way out of
  it, the same dead end as reporting a missing `.json`. It now names the remedy
  and where the evidence will be. A run nobody ever wrote does not borrow the
  sentence: there is nothing for a service to set aside.

- **A probed stdio server no longer hangs the probe by outliving it.** Killing
  the process Atenea started left that process's own helpers running, and
  `Wait` cannot return while a survivor still holds the inherited pipe. The
  probe now kills the process group, the way the rest of the codebase already
  does through `internal/procgroup`.

- **A connection failure reports the cause without restating the request.**
  Go's `*url.Error` puts the method and the whole URL in front of the clause
  that says what happened; the address is already on the line above it.

- **A stdio server that dies on startup said so two different ways, and a race
  picked which one you got.** Both the write of `initialize` and the read of the
  reply can be the first to notice the child is gone: the write sees `EPIPE`, or
  the request fits in the pipe buffer and the death surfaces one line later as
  EOF. The read path reported the process fact and carried the server's own
  stderr; the write path reported `closed before it could be asked: write |1:
  broken pipe` -- a connection metaphor for a process that never got off the
  ground, which is the exact sentence this probe exists to replace. Measured:
  the same commit passed on the machine it was written on for days and failed on
  a CI runner, because the runner is slower to start a child and faster to reap
  it. One fact now has one sentence, shared by both paths, and a write error that
  is genuinely a write error keeps its own wording.

### Removed

- **`file.read` is struck from the design's base list.** It was never built,
  and until now the entry sat there implying a gap that somebody would
  eventually try to close. A capability exists so the funnel has something to
  choose between: `code.search` has four candidate implementations and the
  choice is real -- literal text is cheap and exact, a model turn is expensive
  and can infer, a graph needs an index and ranks what it finds. Reading a
  named range of a named path has one implementation on any machine, the
  filesystem, and every client that would ask already holds it. Nothing to
  constrain, no health to track, no cost to rank, and nothing to fall back to.
  A funnel in front of a file read is overhead with a trace attached.

  It looked like a gap exactly once, and only under an artificial rule: a run
  forbidden from using anything but Atenea had to read a function body by
  searching for its name with `context_lines = 30`, and the window truncated
  the following declaration mid-expression. That is a real limitation of
  reading through a search tool. It is not an argument for the capability,
  because the constraint that produced it does not exist in normal use -- the
  client reads the file and asks Atenea only what needs deciding. If a machine
  ever appears where reading is genuinely a choice between providers, that is a
  new capability with its own contract, not this one revived.

## [0.7.0] - 2026-08-07

`pkg/contract` bumped to `2.0.0`, and it is the first bump that is not
additive: `Health.ObservedAt` is gone, so an adapter built against `1.x` that
named it in a composite literal stops compiling, and `Supports` refuses the
whole `1.x` line rather than letting a peer find the gap one field at a time.
Nothing wrote the field and nothing read it -- the detail is under **Removed**
below. Every Runner in existence is in this repository, so no adapter author
is woken by this.

A settings file is, and that is the number doing its job rather than
inflating. A major bump is a warning aimed at whoever can feel the break, and
the earlier ones in this file had nobody to warn: eleven minor bumps in a row
cost a reader nothing because nothing downstream could tell. This one is felt
by every file on disk, once, by exactly one line -- which is the smallest
break the number is capable of announcing, and precisely the case it exists
to announce. Sizing the bump to how much code moved would have hidden it.

**Upgrading needs one line.** Every settings file on disk says
`contract = "1.0.0"`, so this core refuses it and names the fix:

```text
settings ~/.config/atenea/atenea.toml: contract 1.0.0 is not supported by
this core (2.0.0): change the contract line to "2.0.0"; no other key moves
```

That is the whole upgrade. No settings key was added, renamed or removed by
the bump, and a `Health` never appears in a settings file at all -- the file
is already correct and only says the wrong year. A file from the *future* is
told the opposite, because no edit to it can help: upgrade the binary.

### Changed

- **The permission gate was copy-pasted into five adapters and absent from the
  core's dispatch path; it is now one site the core owns.** `claudecode.go`,
  the graph adapter, `omp.go`, `serena.go` and the local stand-in each
  carried the same three lines checking `req.Allowed()`, and nothing sat on the
  seam every dispatch actually crosses. That is the most security-relevant
  decision in the system living in five dumb translators -- and `pkg/contract`
  explicitly anticipates adapters supplied from outside this repository, which
  enforced nothing whatsoever unless their author happened to copy those lines.
  The check now lives in `internal/core/commission.go`, wrapped around the
  attached seam in `attach`, and the five copies are gone. Measured live: with
  the standing grant narrowed to `["read"]`, `code.search` is refused with
  `permission_denied: code.search causes process, which the commission does not
  cover` -- through an adapter that no longer checks anything itself.

- **A trace repeated the same static drop under every step.** On the dogfood
  run four of five lines per step were the same three sentences: "no attached
  runner serves it" does not become truer for being printed six times, and the
  repetition buries the drops that did vary, which are the only ones worth
  reading a trace for. Drops identical across every step that reached the
  funnel now print once under `dropped in every step`. A single-step run is
  unchanged: one step cannot repeat itself, and there the drops are the whole
  story of that funnel.

### Added

- **`atenea status` names shipped implementations your settings file does not
  declare.** Settings replace the catalogue wholesale rather than patching it
  -- documented and deliberate -- but that means a file written before a
  release never gains what the release shipped, and nothing said so. Measured
  on the machine cutting these releases: a file predating `0.6.0` was still
  registering one implementation of `symbol.overview` after the binary began
  shipping two, so the funnel ran with a single candidate, and when that
  candidate died there was nothing to fall back to. The warning is advisory and
  names only implementations of capabilities the file still declares: dropping
  a whole capability is a deliberate act, not drift.

- **A trace says where the hits are, not just how many.** A commission reports
  a count because a count is all that composes across repositories -- but
  nobody can act on a count, and learning which files were behind
  `15 hit(s) for "CANDIDATES"` cost a second full dispatch as `ask --json`.
  Each step under `--trace` now prints the distinct files it found, capped at
  eight, and when it caps it names the exact command that prints the rest.

- **`out_of_scope` has a reader, and survives being folded.** The column was
  recorded from `0.5.0` and read by nothing. It now has both halves it was
  missing: `atenea metrics` prints a line for any provider that returned
  results outside the scope it was asked for, and migration `0007` adds the
  column to the rollup table so the number is not silently rounded to zero by
  the first compaction pass. It is still never scored, and that asymmetry is
  deliberate: a provider that reports its own overreach honestly must not rank
  below one that hides it.

### Fixed

- **Four failure bins that describe the request were condemning the provider.**
  A streak of failures demotes a provider and drops it from the funnel, which
  is right for `unavailable` and `timeout` and wrong for `not_found`,
  `permission_denied`, `invalid_input` and `canceled`: those are facts about
  what was asked for, not evidence about who was asked. Measured: a TypeScript
  sweep of 34 files answered 26 correctly, but three generated files absent
  from the graph returned an honest `not_found` in a row, and Atenea marked
  every implementation of `symbol.overview` down for the entire repository --
  every real file after that point failed. The same sweep now reports 26 ok, 8
  honestly failed. The four bins are invisible to the health record: a run of
  them neither condemns a provider nor exonerates one, leaving the streak
  exactly as the last real attempt left it.

- **`symbol.definition` reported the doc comment instead of the declaration.**
  The serena adapter took the first line of the range the language server
  reported, which holds for gopls and breaks for rust-analyzer: it starts a
  symbol's range at its doc comment. Serena publishes only `body_location`, so
  there is no name range to ask for -- the range is now scanned for the line
  that writes the name, skipping comment lines first. Measured on a Rust
  repository: `symbol.overview` answers for 21 of 21 files where it managed 2
  before, and `definition` on `pick` answers line 116 rather than 48.

- **Resuming a torn run pointed at a file that was never there.** A receipt
  destroyed by an ugly close is set aside as `.json.torn` rather than deleted,
  precisely so somebody can read what was lost -- and `atenea resume` answered
  "no such file or directory" for the `.json`, which is true and sends the
  reader to the one path with nothing on it. It now names the file that exists.

- **A full disk read as a permissions problem.** ENOSPC has no bin of its own
  and must not get one: nothing a provider or a caller did caused it, and the
  bin it lands in is now exempt from the health record for exactly that reason.
  What was wrong is the sentence -- the disk was the last clause of a line
  about a run id, and the first place anybody takes `permission_denied` is
  `ls -l`, not `df`. Every write in `internal/checkpoint` now leads with
  "no space left on device" when that is what happened. Measured on a real
  filled filesystem.

- **`lefthook install` was a required step mentioned only as a comment in a
  command list.** Git never clones hooks, so every fresh checkout of this
  repository has none: measured, a clone accepts a deliberately unformatted
  file at exit 0 until that command runs. The README and getting-started now
  say so in prose, say to run it first, and say plainly that the hooks are a
  convenience while the enforced gate is the release workflow -- the one check
  nobody can skip by forgetting a setup step. A manual step nobody is told
  about is the same as no step -- the same shape as the lint streak recorded
  under `0.6.0`, where the hook existed and had never been installed on the
  machine cutting the releases.

- **The stand-in's ignore list was described as standing in for `.gitignore`,
  and it does not.** `default.toml` explained the list as the rule a real
  search tool gets from the repository, told to the stand-in instead. Measured
  on a probe repository, the two disagree in both directions: the stand-in
  skipped a tracked file under `build/` that omp searched, and returned two
  `.gitignore`'d files omp did not. The comment now says the list is a floor
  and nothing more, and that having no client installed is the reason to
  choose the stand-in -- speed is not, even at the 28-65x measured.

### Removed

- **`contract.Health.ObservedAt`** -- breaking, and the reason for the major
  bump above. A `Health` value cannot outlive its evidence: `Fault.Health` and
  `Baseline.Health` both take `now` and refuse to speak once `FaultWindow` or
  `SuccessWindow` has passed, so a `Health` that exists at all is one still
  inside its window. The timestamp was a second mechanism for a job the
  windows already do, and two mechanisms for one job is how they drift apart.
  It was written in one place, read in none, and asserted by a single test
  that checked it equalled the success it was copied from. What made it worth
  removing rather than leaving inert is that it was exported: an outside
  adapter would eventually have populated it believing the core read it.

## [0.6.1] - 2026-08-07

### Fixed

- **The release gate claimed to run the same check as CI and did not, which is
  why `v0.6.0` is a tag with no release behind it.** The step is named "the
  same gate CI runs on main" and its comment said so twice, but CI installs
  the linter with `golangci/golangci-lint-action@v8` while this step piped
  `install.sh` from the linter's *master* branch into `sh`. Two mechanisms,
  one claim of equivalence, and nothing checking it. On 2026-08-07 upstream
  began shipping a `.sbom.json` asset whose name is a superstring of the
  tarball's; the unpinned script downloaded the SBOM, checksummed it against
  the tarball's hash, and exited 1. Both hashes in that error are legitimate
  published values -- `fd3a137c…` is genuinely the SBOM and `8df580d2…`
  genuinely the tarball -- so nothing was compromised; the script simply
  picked the wrong file. The step now uses the same action, so the
  equivalence is a fact about the file rather than a promise in a comment.

  `v0.6.0` is left exactly as it fell: a tag on a commit CI proved green,
  with no GitHub release attached. Moving it would have made the release list
  contiguous by deleting the evidence that a gate failed, and a changelog that
  edits away its own inconvenient facts is worth less than the gap it hides.
  Everything `0.6.0` describes below shipped in `0.6.1`.

## [0.6.0] - 2026-08-07

### Added

- **`symbol.overview` has a second provider, and a way to say what it cannot
  be asked.** The graph overview implementation answers the capability from the graph
  the provider already built: one `query_graph` round trip for what the file
  declares, one pass over the file to recover the columns the graph does not
  store. It answers markdown too, where the headings are what the file
  declares and no language server has anything to say.

  It can only answer at `depth = 0` — the graph holds a file's top-level
  declarations and nothing nested inside them, so a deeper ask would return
  the same list and read as a complete answer to a different question. Saying
  that needed a constraint of a kind that did not exist: every constraint
  until now read the repository ("can this provider work *here*"), and this
  one reads the request ("can it be asked *this*"). `max_input` bounds a
  declared integer input by name, inclusive, and binds only when the call
  actually names it.

  At `depth = 0` both providers compete; at `depth = 1` this one is dropped in
  the constraints stage naming both numbers, and Serena, which descends
  properly, is what is left. A bound naming an input the capability does not
  declare, or declares as anything but `int`, is refused when the settings
  file loads — otherwise the funnel reads a name no request carries and the
  narrowing silently does not exist.

  `pkg/contract` bumped to `1.11.0`: additive, an adapter built against
  `1.10.0` goes on compiling and leaves the map nil, which bounds nothing.

  The `kind` this provider reports is the graph's node label and will not
  always match Serena's word for the same symbol — measured: a Go const comes
  back as `Variable` here and `Constant` from Serena. That is not normalised
  away, because it cannot be honestly: the graph has no `Constant` label and
  no property separating a const from a var, so the distinction it would have
  to report is one it never read. The capability now says outright that `kind`
  is the provider's own vocabulary and is the one field here that is not
  comparable between providers.

### Fixed

- **A provider that wandered out of scope on every call paid nothing for it.**
  The count of dropped strays existed for the length of one sentence: the
  adapter wrote `"N match(es) fell outside the requested scope and were
  dropped"` onto the answer, whoever asked read it once, and nothing that
  ranks providers ever saw the number. It is now a fact on `contract.Outcome`
  and a column in the measurement base, with the sentence built from the same
  number rather than beside it.

  It is recorded and deliberately never scored. Health answers "can this
  provider answer at all", and one that wanders still answers — the core drops
  the strays and the caller gets a clean result, so demoting it would remove a
  working provider to punish a defect already neutralized, and the funnel
  would then report nothing available for something that demonstrably works.
  What wandering costs is tokens and time on results nobody could use, which
  is a cost fact; cost is the stage that chooses between providers that all
  work, and it will read this when it ranks on measurements instead of
  estimates. Both halves are tests: one that the number reaches the disk, one
  that five all-wandering calls leave health alone.
  `pkg/contract` bumped to `1.10.0`: additive, an adapter built against
  `1.9.0` goes on compiling and leaves it zero, which reads as "nothing
  strayed" — the same thing it means for a provider confined by construction.
- **A settings file could name an implementation no adapter could run, and
  nothing noticed until the call.** A runner's served list came from the
  settings file and was trusted whole: `Serves` said yes, `atenea status`
  printed the implementation as served, the funnel chose it, and only then did
  dispatch reach a switch with no case for it and return `not_found` --
  blaming the request for a wiring mistake made long before it. `contract.Runner`
  now also reports the capabilities it can actually execute, and the core
  checks the served list against them at load. An id the catalog does not
  declare stays deliberately allowed: it can never be chosen, and refusing it
  would break a small hand-written catalog attaching a runner whose shipped
  defaults name more than it uses.

  This adds a required method to `contract.Runner`, which no version number in
  this scheme describes honestly: a minor bump promises adapters keep
  compiling and they do not. It carries none. Every implementer lives in this
  repository — adapters are selected by name in the core, so one cannot yet be
  supplied from outside — and all of them were edited in the same commit. The
  package now says which half of it the version promise covers, and that the
  change which has to be major is the one that opens the interface to outside
  implementers, not a method added after it.
- **The legacy graph search implementation was declared with nothing behind it.** Measured
  before removing it: on a medium repository declaring a graph index,
  with the id added to the adapter's served list, the config loaded, the status
  screen said the adapter served it, the funnel chose it, and the call returned
  `not_found: graph adapter has no implementation of code.search`.
  The catalogue entry is gone. `ripgrep` already answers `code.search`
  correctly and cheaply everywhere with no index; a graph-backed search's real
  advantage is ranking hits into their containing symbols, which `code.search`
  has no output field to carry. That idea is recorded in
  [What is not built yet] as a possible future capability with its own
  contract, not as a lost implementation of this one.
- **`idle_timeout` was accepted beside `lifecycle = "persistent"` and did
  nothing.** The idle reaper only ever visits `on_demand` servers, so the key
  was inert for a reason that lives in the supervisor and appears nowhere in
  the file. It is refused at load now, naming both lifecycles so the message
  says which one the key belongs to. The survey that went with it found four
  more knobs that stop applying — `[metrics]` and `[backup]` under
  `enabled = false`, `checkpoint_dir` under `checkpoints = false`, and
  `restart_delay`/`stable_after` under `restart_limit = 0` — and all four are
  deliberately kept, because each sits in the same table as the key that
  switches it off and `atenea status` reports the result as `off` or drops
  the rhythm entirely. The rule the file is now held to is that a knob which
  stops applying must have something visible saying why; those four have a
  witness and `idle_timeout` had none.
- **A malformed `orchestrator.serena.endpoint` was only refused when no
  process table sat beside it.** Declaring a managed process takes the address
  over, and the written one was then never read, so it was never checked —
  `endpoint = "localhost:9121"` passed with the table and failed without it,
  which meant deleting the table could break a file that had always loaded.
  The endpoint is now validated either way, through the same rule the adapter
  applies, so a file means one thing rather than two.
- **A crashed Serena was given up on at the first crash, never the "couple of
  times" the design says.** `supervisor.DefaultRestartLimit = 2` was declared
  and applied nowhere. `restart_limit` is a `*int` precisely so an omitted key
  can be told apart from an explicit `0`, but `config` resolved the absent
  pointer to a Go zero, and `supervisor.withDefaults` — which fills the other
  five process timings — deliberately leaves `RestartLimit` alone, because by
  the time a `Spec` is built zero is a legitimate "never retry". Each side's
  comment named the other as the one that would apply it, and neither did.
  Measured against the binary: a server that cannot spawn is now attempted
  three times over 4.1s, where it used to be attempted once and marked down.
- **`checkpoints` was never named on the settings page.** Turning run receipts
  off was documented as pointing "the orchestrator at no store at all", which
  is a Go-level idea a settings file cannot express. `checkpoint_dir = ""`
  turns nothing off: an empty string is indistinguishable from an absent key
  and inherits the default, so it keeps writing. Only `checkpoints = false`
  blanks it, and it beats an explicit path written beside it. `atenea status`
  reports which way it went on its `runs` line, as a directory or as `off`.
- **The eleven fields of `[orchestrator.serena.process]` had no coverage at
  all**, including the one with teeth: declaring the table used to stop
  `endpoint` from being read, and therefore from being validated. That hole is
  closed above; what was missing here was the page. It now documents every
  field against measured defaults — `{{port}}` substitution, `env` extending
  rather than replacing the inherited environment, and `restart_limit`
  counting retries rather than attempts — and both load-bearing claims are
  tests rather than sentences.
- **The settings page claimed a value you leave out is absent; for everything
  outside the catalog it falls back to a compiled default.** The catalog blocks
  really are replaced outright — that part was right, and the page's own
  empty-catalog example still holds. But `[core]`, `[orchestrator]`,
  `[metrics]`, `[backup]` and the adapter blocks under them are applied key by
  key: the very file the page prints as "knows nothing" runs with `parallel 4`,
  `metrics.flush 30s`, `metrics.compact 1h`, `backup 6h` and `5 of 5 kept`,
  none of which it declares. Two consequences went with it, both measured
  against the binary rather than reasoned about: an omitted list is not an
  empty one (drop `implementations` from `[orchestrator.serena]` and all four
  symbol capabilities are still served; write `[]` and none are), and `effects`
  is the deliberate exception, because a grant nobody wrote down is a grant
  nobody made. `default.toml`'s own header carried a sharper version of the
  same error — "nothing in this file is compiled into the binary", said by the
  file that is embedded in it.
- **A test now holds the shipped file and the compiled fallbacks to saying the
  same thing.** That agreement is what makes the difference invisible today and
  what a future edit could quietly break on one side only, leaving a partial
  file that still works and no longer means what the file it was copied from
  meant.
- **`golangci-lint` had been red since v0.3.0 and three releases were cut on
  top of it.** An unused `serverVersion` on the Serena runner, orphaned when
  per-call `ToolVersion` replaced it, and one `behaviour` in a contract
  comment. Neither changes behaviour; the streak is the finding. `lefthook.yml`
  already runs the linter pre-commit — it has never been installed on the
  machine cutting the releases, so CI was the only gate and nobody read it.

## [0.5.0] - 2026-08-06

### Added

- **`symbol.overview` answers "what does this file contain," the one
  question the other three symbol capabilities all assume is already
  known.** `symbol.definition`, `.references` and `.implementations` each
  need a name or a position handed in first; nothing listed a file's own
  symbols the way an editor's outline pane does, noticed while dogfooding
  ordinary day-to-day use and tracked in the open questions doc until now.
  `serena.overview` drives Serena's `get_symbols_overview` for the flat or
  nested (`depth`) list of names, then fans out one `find_symbol` call per
  name -- bounded at 16 in flight together -- to recover the line and
  column neither `get_symbols_overview` nor any other Serena tool reports
  on its own; a local read of the resolved line recovers the column past
  that. This is the first capability to issue concurrent calls inside a
  single commission, so the MCP session state the adapter shares between
  them -- the JSON-RPC id counter and the session handle -- moved under a
  lock of its own; the per-commission lock above it excludes other
  commissions, never these siblings from each other. The orchestrator
  advertises the capability on its agent card, so a plan can reach for it
  the same way it reaches for the other three.

  Building it against a real repository surfaced a real ambiguity in how
  Serena's own tools disagree with each other: `find_symbol` matches an
  unqualified pattern like `"kind"` against any symbol named `kind`
  anywhere in the file regardless of nesting, so a query meant for this
  repository's own top-level `type kind uint8` also matched an unrelated
  nested field, `overviewEntry.kind`, three types away -- and separately,
  `get_symbols_overview` reports every method as a bare, receiver-less
  name, so three unrelated types in `cmd/atenea/main.go` each defining
  `String()` all overview as the same unqualified `"String"` with no way
  to tell them apart. `locateOne` now prefers a candidate whose own
  `name_path` exactly echoes the path the overview walk claimed, when at
  least one exists, which resolves the first case outright; when none does
  -- the only way the second case can happen, since `get_symbols_overview`
  never hands this adapter a receiver to ask for -- the unnarrowed matches
  are reported as genuinely ambiguous rather than guessed at or claimed
  not found.

  Cost estimates in `default.toml` are measured, not guessed: 392-630ms and
  ~1700-1900 tokens across eighteen live runs in two independent sessions,
  against this repository's own largest file at nested depth (91 symbols),
  including two `serena.service` restarts checked specifically for the cold
  language-server tax every other Serena implementation's estimate only
  allows headroom for -- none was measurable here, both cold calls landing
  inside the ordinary spread. The declared estimate rounds to the top of
  both ranges rather than the middle, because a funnel told a provider is
  cheaper than it is will pick it over one it should have lost to. Verified
  against the compiled binary and a live language server on this repository:
  `ask symbol.overview --repo current --set file=internal/adapter/serena/serena.go --set depth=1`
  resolves all 91 symbols including both `kind` collisions correctly and
  distinctly; the same call against `cmd/atenea/main.go` answers
  `invalid_input: "String" matches 3 symbols; serena's overview cannot be
  trusted to mean one of them` instead of a false `not_found`. No change to
  `pkg/contract`: the new capability and implementation are data in
  `default.toml`, answered through the existing request/outcome shape.

### Fixed

- **`claude.search`'s `scope` input was advisory only -- a returned match was
  never checked against it.** `omp` makes scope real by construction:
  `targets()` refuses any path outside it before ripgrep ever runs.
  `claude-code` only ever wrote scope into the prompt and trusted the model
  to honor it, unlike the sensitive-path list the same adapter already
  double-checks. `cleanHit` now checks a match's path against the requested
  scope right after confirming it sits inside the repository at all -- the
  same place the sensitivity check already runs -- and drops (never fails)
  anything outside it; a drop surfaces as an aggregate Notice on the Outcome
  (`"N match(es) fell outside the requested scope and were dropped"`), never
  a silent subtraction.

  Implementations can now declare which of the two ways they keep this
  promise: `Implementation.ScopeGuarantee` is `confined` when the tool is
  physically restricted to scope and cannot see outside it (`ripgrep`, via
  `targets()`), `filtered` when the provider may look anywhere but every hit
  is checked afterward (`claude.search`, as of this fix), or the empty
  default for anything that has not declared either -- read as the weakest
  claim, never as confined. It sits outside the four blocks that decide
  selection: it never disqualifies a candidate and the funnel does not rank
  on it, a disclosed property of the answer rather than a filter, printed
  per implementation by `atenea catalog` so the promise is queryable instead
  of assumed.
  `pkg/contract` bumped to `1.9.0`: additive, an adapter built against
  `1.8.0` goes on compiling and simply never sets the field.

## [0.4.0] - 2026-08-05

### Fixed

- **`symbol.definition`/`symbol.references`/`symbol.implementations` could
  only ever find an answer inside the file the caller happened to be
  pointing at.** `symbolAt` resolved a position by reading that one file and
  asking Serena's `find_symbol` to match the word under the cursor against
  it -- which works when the declaration happens to live in the same file
  as the call site, and answers nothing when it does not. Most real calls
  do not: this repository's own production metrics showed 12 of 18 real
  `symbol.definition` calls failing for exactly that reason. `symbolAt` now
  asks Serena's `find_declaration` first -- one LSP request anchored to the
  exact position with a regex built from the surrounding line, resolved
  wherever the declaration actually lives -- and falls back to the old
  same-file search only when `find_declaration` itself cannot answer: an
  ambiguous regex match, or a position the language server resolves
  nothing for. `referencing` and `findImplementations` now ask about the
  file the resolved declaration lives in, not the file the query named, so
  a symbol used far from its own declaration still gets every reference
  and implementation back instead of an empty answer from the wrong file.
  Verified against the compiled binary and a live language server on this
  repository:
  `ask symbol.definition --repo current --set file=cmd/atenea/main.go --set line=940 --set column=23`
  now resolves
  `capability.ValidateInput`'s call site to its declaration in
  `pkg/contract/capability.go:215`, and `symbol.references` on the same
  position finds all 7 real call sites across 3 files, where both
  previously answered from `cmd/atenea/main.go` alone or not at all.

- **`atenea ask`/`atenea task` could dispatch a step with a payload missing
  a required field, and only the chosen implementation would ever notice.**
  `runStep` already ran pricing and the funnel's selection before building
  the request, then handed it straight to `a.runner.Run`.
  `RunRequest.Validate`, which checks exactly this, existed already and was
  called defensively by some adapters -- but only after the orchestrator had
  picked an implementation and invoked it. `cmdAsk` had the same gap one
  layer up: it built the payload and dispatched without ever calling
  `capability.ValidateInput`, the method the capability contract already
  exposes for this. Both call sites now validate before the runner is ever
  invoked -- `cmdAsk` before the request leaves the CLI, `runStep` right
  before `runner.Run` so the same guarantee holds for `atenea task` and any
  future caller that reaches the funnel directly. Verified against the
  compiled binary: `ask code.search --repo current` with no `--set query`
  now exits `2` (`invalid_input`) with no run receipt printed, where it
  previously reached the runner.

- **An unknown capability id got the same flat `unknown capability %s`
  whether it was a real typo or something that never existed.**
  `atenea ask code.serach` (one transposed letter) and
  `atenea ask nonsense.capability` read identically on the way out, so the
  only way back from a typo was guessing again or running `atenea catalog`
  to read the real list. `Registry.Capability` now measures the
  Levenshtein distance from the id that was not found to every id that is
  registered, and names the nearest one when it is within 3 edits -- close
  enough to read as the same word misspelled, not a different capability.
  Past that distance nothing is suggested; a guess dressed as help is worse
  than the plain refusal. Verified against the compiled binary:
  `ask code.serach` now answers
  `unknown capability code.serach; did you mean code.search?`.

- **No subcommand answered `--help` or `-h`.** Every subcommand's own
  `flag.FlagSet` had its output routed to `io.Discard` -- so the CLI's own
  `invalid_input` wrapping, not Go's default flag-error text, is what a bad
  flag prints -- which silenced `flag.ErrHelp`'s usage text along with
  everything else. Worse, four commands (`ask`, `select`, `task`, `resume`)
  read a positional argument before their flag set ever saw anything, so
  `-h` in that position was consumed as the capability id or commission
  text instead of being recognized as a flag at all. `run` now checks every
  subcommand's own arguments for `-h`/`--help` before dispatching, and
  answers from a per-command usage message when it finds one -- ahead of
  any positional-argument parsing, any flag set, and any settings file
  load, so help never depends on a working config existing. Verified
  against the compiled binary across all twelve subcommands, including
  `ask -h` and `select -h` with no other arguments.

- **`symbol.implementations` could only ever answer when the answer was
  empty.** The `0.3.0` fix below made `parseReferences` treat
  `find_implementations`'s `[]` the same as `find_referencing_symbols`'s
  `{}`, but `findImplementations` still called `parseReferences` for
  everything -- and that function only ever understood the OTHER shape:
  entries nested path -> kind, which is `find_referencing_symbols`'s shape,
  not `find_implementations`'s. A real `find_implementations` answer comes
  back in `find_symbol`'s shape instead, a flat array, so every call with a
  genuine hit still failed with `unavailable: serena did not answer` while
  `Raw` showed Serena's correct, parseable answer sitting right there. Found
  asking about `contract.Runner` in this repository, which has five real
  implementations. The only test ever written for `find_implementations` fed
  it `"[]"` -- legal JSON either way, so it exercised the empty branch and
  proved nothing about the one that returns data. `findImplementations` now
  parses with `parseSymbols`, `find_symbol`'s own reader, instead of
  `parseReferences`; a new test feeds it a real two-hit answer across two
  files. Verified live: `symbol.implementations` on `contract.Runner` now
  returns 12 locations across every real and test implementation of the
  interface in this repository, instead of failing on all of them.

- **A provider that had never once worked here could not be marked down.** The
  failure streak's bin count was taken over the whole run since the last
  success -- and for a provider with no success on record, that run is its
  entire history. Any variety in it at all left more than one bin, which
  blanked the shared `Kind` and downgraded the verdict from `down` to merely
  `degraded`, so the provider stayed in the funnel however consistently it
  was failing right now. Worse, it could never recover from that: the only
  thing that starts a fresh run is a success, which is exactly what a broken
  provider does not have. Measured on this machine: `claude.search` on this
  repository, 8 attempts, 0 successes -- five `unavailable` from the days it
  was not logged in, then three `permission_denied` from its real spending
  ceiling. A cause fixed two days earlier was masking the one failing every
  call. The streak is now read from the newest failure backwards and stops at
  the first attempt that broke differently: three of one bin at the newest end
  is an outage even when older, unrelated failures sit behind it. `Streak`
  still carries the whole run, so the degraded sentence is unchanged; the
  outage sentence names the run that actually earned the verdict rather than
  the whole history. Verified against that live record: `3 permission_denied
  failures in a row`, `down`, dropped from the funnel. The docs described this
  behaviour correctly all along -- only the code disagreed.

### Added

- **`--json` on `task`, `ask` and `resume` prints the full result as
  structured JSON instead of prose, for a caller that wants to parse it
  rather than read it.** `printResult`/`printAnswer` render for an eye --
  columns, indentation, a line omitted because a human already knows what
  zero would have meant -- so `printResultJSON` is a second, parallel
  renderer over the same `orchestrator.Result` rather than a mode bolted
  onto the first. It always prints the complete receipt and ignores
  `--trace`: there is no eye to spare on the wire, so the plan and every
  step are on it every time, not only when asked for. Unit-tested against a
  synthetic result and round-tripped through the real binary end to end.

- **A step's receipt now says how far a charge ran past its own grant, and
  the CLI trace prints it.** Money is a permission, but a far side's own
  spending ceiling is checked between complete turns, not inside one: a
  single expensive turn can still finish after the budget for it was already
  gone. `grant.spend` already clamps the purse at zero when that happens, so
  the commission's books stay honest -- but a clamped purse does not say the
  overspend occurred. `orchestrator.Overspend` reports the gap between a
  step's `SpentUSD` and its granted `Permission.BudgetUSD`; persisted on the
  checkpoint as `overspend_usd` (omitted when zero, same convention as
  `spent_usd`) and printed as `overspent $X.XXXX` beside the existing
  `charged` line whenever it is non-zero.

- **A successful `claude.search` answer can now carry a doubt about its own
  completeness.** Investigated whether Atenea could enforce a hard spending
  guarantee on a paid provider mid-call: the Claude Code CLI reports cost as
  one blob after a turn closes, with no incremental figure to check against
  mid-turn, so a real guarantee is not achievable at the current adapter
  boundary, only after-the-fact detection. Two honest signals stand in for
  it, each appending a `contract.Outcome` `Notice` rather than failing the
  call outright: an answer with `num_turns <= 1` never read a tool result
  back (the completion that calls a tool cannot also be the one reporting
  what it found, so the prompt's own "grep before you answer" cannot have
  run), and an answer that spent 80% or more of its ceiling is the same
  shape, one step earlier, as every recorded case on this machine that died
  outright mid-search. Printed under the step in `--trace` output; most
  calls trip neither check.

- **A repository's `indexed_by` could be silently wrong, in either
  direction, with nothing in Atenea to catch it.** The settings file states
  it by hand, as a starting point, and nothing ever went back to check it
  against the provider it names -- verified on this repository itself:
  `indexed_by = ["serena"]` for `current` while the graph MCP backend
  already held a real, ready index for the exact same path. `symbol.calls`
  and `code.impact` each have exactly one implementation, both
  the graph backend's and both `requires_index = true` with no fallback, so
  the stale belief did not degrade either capability against this
  repository -- it made both completely unusable, `not_found` on every
  call, for a reason nothing on the status screen or in a funnel trace
  explained beyond "repository has none."

  `contract.IndexProber` is a new, optional interface a runner implements
  when it can answer "do you already have one" without being asked to
  build it: the graph backend answers by calling `index_status`, the same
  tool its freshness check already calls for its own reasons; Serena
  explicitly does not implement it, because `activate_project` succeeds
  silently on an empty project and cannot tell "indexed" from "nothing
  here yet." `Registry.SetIndexed`/`Repository.SetIndexed` correct the
  belief in memory, the same one-place-a-catalog-entry-changes-while-
  running exception `SetHealth` already is -- not written back to the
  settings file, so a later process starts again from what the file
  declares, exactly like health. `Core.DetectIndexes` sweeps every
  attached prober against one repository or every registered one, and
  `atenea detect [--repo ID] [--json]` is the CLI hook, on demand rather
  than on every startup: a probe is a subprocess call per repository per
  provider, and paying that unconditionally would tax the common case
  (everything already correct) for the sake of the uncommon one this
  exists to catch.

  Detecting only ever reads. A repository truly nothing has indexed needs
  an actual build, which is the second, deliberately separate half:
  `repository.index` (effects `write` + `process`, a `mode` input of
  `"fast"`/`"moderate"`/`"full"` defaulting to `"moderate"`) drives
  the graph MCP backend's own `index_repository` tool and reports back
  `status`/`nodes`/`edges`. The constraints-stage drop reason for a
  missing index now names both:
  `needs an index from provider %s, repository has none -- atenea detect
  looks for one, atenea ask repository.index --repo %s builds one`.

  Two capabilities instead of one implicit mechanism is a deliberate
  reversal of the earliest sketch of this idea, worth recording: the
  original design imagined the selector itself noticing a missing index
  and building one silently, inside the funnel, as part of answering a
  normal question. That collides with the effects contract built since --
  the selector is a pure decision funnel with zero I/O of its own, and no
  permission is ever granted implicitly mid-flight; every `write`+
  `process` action needs an explicit grant, the same rule that makes
  `repository.index` its own capability rather than a mode of any read
  one. The sketch and the effects model it predates are not compatible,
  and the model that was actually built and defended wins. Verified
  against the compiled binary and this repository's own settings file:
  `select symbol.calls --repo current` dropped the graph call implementation for
  "no index" and answered `not_found`; `detect --repo current` reported
  the graph backend ready`; correcting `indexed_by` to include
  the graph backend by hand afterward cleared that drop, leaving only the
  pre-existing, unrelated `min_scale = "medium"` floor this small
  repository was always going to hit. Contract `1.8.0`.

## [0.3.0] - 2026-08-05

### Fixed

- **A symbol with zero implementations reported the provider as down instead
  of answering.** `find_implementations` answers no hits with a bare `[]`;
  `parseReferences` only ever recognized `{}`, the shape
  `find_referencing_symbols` uses for the same case, so unmarshaling `[]`
  into its object-shaped target failed every time and the call surfaced as
  `unavailable: serena did not answer`. That is the one bin that marks a
  provider down, so asking about a real symbol that genuinely implements
  nothing took Serena out of the funnel for the rest of the fault window on
  a correct answer. Root-caused with the raw capture below: the failure's
  `Raw` read `serena sent references nobody can read: []`, which is the
  provider answering exactly as designed. `parseReferences` now treats `[]`
  the same as `{}`. Verified against the live repository that reported it:
  `symbol.implementations` on a method with no implementers now returns
  `verdict ok` with zero locations instead of failing.

- **A provider that fails without saying why no longer discards the only
  evidence of it.** `Runner.call` built the failure text from the MCP tool
  result's own content, so an answer with `isError: true` and nothing in
  `content` trimmed down to an empty string -- worse than no evidence, since
  it reads exactly like a call that ran and had nothing to add rather than
  one that failed silently. It now falls back to the raw response frame when
  the structured content is empty, so there is always something to show.

### Added

- **A generic-bin failure now carries the provider's own words, not just
  Atenea's one-line summary of them.** Every adapter already computed this
  text on the way to picking a bin -- the untranslated error a regex match
  failed to place -- and discarded it once the bin was chosen. `Raw` on
  `Failure` carries it out. From there it rides the same path the failure
  itself takes: onto the dropped candidate in a funnel trace, onto the step
  in a commission's trace and receipt, onto the measurement a failed attempt
  writes to the base, and onto the health mark a provider that reported
  itself down leaves for the next call -- each one a `raw` line beside the
  summary it belongs to, printed only when there is one. Nothing upstream of
  an adapter has to change to get it: the six failure bins were always the
  full story the core needed to decide with, `Raw` is additional evidence for
  whoever debugs after, and a client built against `1.4.0` goes on compiling
  and simply never sends it. Contract `1.5.0`.

- **`code.impact` failing at dispatch time on a repository with no version
  control is now a constraint the funnel checks up front, the same way a
  missing index already was.** Its one implementation walks a git diff
  against a baseline, so a directory with no `.git` at its root always failed
  there -- correctly classified, but only after a real subprocess had already
  spawned, with nothing in `atenea status` or a funnel trace warning it would.
  `Constraints.RequiresVCS` and `Repository.VCS` name the fact the same way
  `RequiresIndex`/`IndexedBy` already do, and the funnel drops a candidate
  that needs one and does not have it at `constraints`, before reach or
  health, with a reason that says so. Unspecified is not the same as
  confirmed absent: a repository nobody has declared either way about is
  never disqualified, the same reading an unclassified `Scale` already gets,
  so retrofitting this onto a provider that already worked does not silently
  drop every repository that has not yet said `vcs = "absent"` or
  `vcs = "present"`. Contract `1.6.0`.

- **A repository can pin its own Serena endpoint so two projects stay warm
  without tearing each other down.** One Serena process holds one active
  project; switching pays a full language-server restart (measured ~0.3 s into
  a Go repo, ~1–2.5 s into a Rust/TS one, and throws away multi-gigabyte
  rust-analyzer state). `Repository.SerenaEndpoint` is an optional MCP URL:
  empty keeps today's single-default behaviour (adapter endpoint +
  `activate_project` retarget); a set URL routes that repository to its own
  process. The adapter keeps one MCP session per distinct URL, locked
  independently, so two endpoints answer in parallel. A real retarget on a
  shared endpoint now leaves a discovery note
  (`serena retargeted <url> from <old> to <new>`) on the step, so a slow
  multi-repo run is no longer silently slow in the trace. Contract `1.7.0`.

## [0.2.0] - 2026-08-04

### Fixed

- **A step `resume` correctly skips redispatching no longer comes back
  silent.** `alreadyOK` steps are never rerun -- that is the entire point of
  resuming instead of replanning -- but what they had found was never written
  anywhere durable: `Outcome.Discoveries` lived only in the process's memory,
  and a step this attempt never dispatches has no fresh `StepResult` to carry
  it. A crash between two waves therefore cost every discovery the closed
  wave had made, permanently, with nothing in the resumed run's own output to
  say so. Verified against a receipt with two steps already closed and
  reviewed `ok`, each carrying a discovery: resuming it dispatched nothing
  (`spent 0s over 0 step(s)`, the same no-op shape as a fully closed run) and
  printed both discoveries verbatim, sourced from the receipt rather than a
  rerun. `checkpoint.StepState` now keeps `discoveries` alongside the fields
  it already carried for exactly this reason; `Resume` reads it back for
  every step this attempt does not touch and folds it in beside whatever the
  steps it does redispatch report fresh.

- **A spending ceiling of Atenea's own is no longer read as the client being
  broken.** A turn stopped at its `--max-budget-usd` prints no `result` field
  whatsoever — the reason is in `errors` and `terminal_reason` — so the adapter
  read past it, fell back to the child's `exit status 1`, which names nothing,
  and landed in the catch-all: `unavailable: claude code did not answer`. That
  is the one bin that marks a provider **down**, so a grant of ours being too
  small took a perfectly healthy client out of the funnel and read on screen as
  a client that had stopped working. The reason is now read from whichever
  field carries one, and the ceiling bins as `permission_denied`, which is what
  it is: a refusal made on this machine.

- **A turn that charged money and then failed no longer reports spending
  nothing.** The adapter returned an empty outcome beside the error, so
  everything the far side had already said about the attempt went in the bin
  with it. Measured on a real call: 78 seconds and $0.354 charged, filed as
  `spent_usd` empty. Three things were broken at once — the measurement base
  learned the failure was free, the receipt lost the charge, and because the
  core spends its purse down by what comes back, one commission could charge
  past its whole grant without the arithmetic noticing. The weight is now read
  before the verdict and reported whichever the verdict is. A refusal issued
  before the process is spawned still weighs nothing, because it cost nothing.

- **The break-in rotation no longer rewards a provider for failing.** Only a
  call that works leaves a measurement, so a provider that cannot answer stays
  at zero samples permanently — and the rotation hands the turn to whoever owes
  the base the most. Measured: `claude.search` lost seven straight commissions
  against a repository where `ripgrep` held fourteen clean measurements
  averaging 959ms, and the funnel picked it again for the eighth, at about a
  minute and thirty cents a go. Failing was what kept it winning. The rotation
  is now credit: four attempts with no measurement to show and the provider
  ranks on its declared estimate like anybody else — ranked lower, never
  filtered out, so it stays reachable and can earn its first number the moment
  it works. `Cost.Attempts` carries the count the funnel needs to tell a first
  outing from a record of nothing but failure, which also makes the notice
  `atenea select` already printed true for the first time.

- **A run you stopped no longer reads as a failed one.** Getting the failure
  bin right left the report itself unchanged, and the report is what a reader
  sees first: `verdict failed`, then `review child=failed parent=failed`, then
  `failed canceled: claude code was stopped before it answered` — three lines
  blaming the work for a decision the reader made. Worse, the middle one is a
  review of an answer that never arrived. There is now a `canceled` verdict,
  and a step nobody let finish is not reviewed at all: no output came back, so
  neither the child nor the parent has anything to have an opinion about. A
  real fault still outranks a cancellation, so a genuine failure is never
  buried behind an interruption, and a cancellation outranks success, so a
  half-run plan never reports that it worked. Contract `1.2.0`, additive:
  adapters built against `1.1.0` compile unchanged and never have to send it.

- **Stopping a run is no longer filed as a provider running out of time.** The
  two look identical where they are caught — the work did not finish and the
  context is dead — so the whole class was binned as `timeout`. Pressing
  ctrl-c two seconds into a call therefore printed `claude code took longer
  than 5m0s`: a ceiling nobody reached, quoted at somebody who had waited two
  seconds. It also collected a fault against that provider, dropped its health
  towards `down`, and moved the funnel's ranking on the strength of a decision
  the provider had no part in. There is now a `canceled` bin, decided from
  `context.Cause` rather than from the mere absence of a result, and a
  canceled call is not a measurement: nothing about it reaches the base, the
  health verdict or the ranking.

- **A canceled call comes back when it is canceled.** Killing the process
  Atenea started left its grandchildren alive holding the copy of stdout they
  had inherited, so the read went on waiting for a pipe nobody would close:
  measured at twenty-seven seconds for a client whose helper slept twenty-five,
  and unbounded for a helper that never exits. The child now gets its own
  process group and the group is killed; a helper that escapes with `setsid`
  is covered by a deadline on the wait itself. Canceling a call that spawns a
  daemonizing helper went from thirty seconds to under a tenth of one.

- **The measurements a stopped run had already earned survive it.** The flush
  at a phase close inherited the caller's context, so ctrl-c canceled the
  write as well as the work: every measurement in the batch was lost and an
  incident was filed saying `metrics: open …: context canceled`, which is how
  six identical incidents came to be sitting in the notebook. The flush now
  runs on a context detached from the caller, because work that was paid for
  before the interruption is still work that happened.

- **`130` on ctrl-c.** A stopped run used to exit `4`, the bin a script retries
  on. Nothing is wrong with a run somebody stopped, and a script must not
  retry it: it exits `128 + SIGINT` like the shell's own convention, and the
  screen says `canceled: stopped before it finished` instead of naming a limit
  that was never reached.

- **A newly attached provider can still earn its first measurements.** The
  funnel ranks on health before it hands out break-in turns, and until the
  record learned to promote, nothing running from a CLI ever reached `alive`:
  everybody sat at `unknown`, health tied, and the rotation worked. Promotion
  turned that into a trap. The first provider to succeed became alive,
  outranked every unmeasured rival, and was from then on the only one ever
  dispatched — so nothing else was ever measured and the catalog froze on
  whoever happened to answer first. Found by attaching a second client to a
  machine that had been running for a while: twelve calls in a row to the one
  provider with a record, and a new one that could never earn its first.
  `unknown` is not a verdict, it is the absence of a look, and it no longer
  loses to one while break-in is open. Degraded and down keep their places:
  those are things somebody watched happen. The trace was corrected with it —
  it had been reporting these as `healthiest surviving implementation`, which
  was wrong even between two providers that both read `unknown`, and it now
  says which stage really chose and names the alive provider that was
  overtaken.

- **Successful calls count as evidence of health.** The record could only ever
  make a provider look worse. The rule was written against *silence* — nobody
  probed it, so nobody knows — and then applied to success, which is not the
  same thing at all. The consequence was reported from real use: seven
  successful calls in a row, zero failures, and the screen still said
  `health=unknown` with an amber light that no amount of working could clear.

  The bar for the record to promote is now *the last call here worked, and it
  worked recently*. Recently is one hour: long enough that two commands ten
  minutes apart do not disagree about whether the machine is well, short enough
  that it cannot speak for a machine nobody has used today. Last, because a
  failure with nothing after it means the newest thing anybody knows is that
  this broke — not enough to condemn a provider, far too much to call it well,
  so it reads unknown until something succeeds.

  Promotion may only lift `unknown`. A `down` or `degraded` set by a live probe
  stands, because a probe looked seconds ago and a file may predate the outage
  entirely. Downwards the record still overrules everything, including a
  declared `state = "alive"`. A promotion changes the state and does not invent
  a score, so cost stays the tie-break between two working providers.

- **The status screen reads the measurement base.** It walked the declarative
  catalogue and never opened the base, so the one screen whose job is reporting
  health was the only place that could not see the half of health that survives
  a process. Both fixes were needed: either alone leaves the amber in place.

  Where a provider has been tried on several repositories the screen shows the
  worst state it reached, and names the repository — `down` and `down on
  scripts` are different instructions. An unreadable base costs the promotion
  and nothing else; the screen still draws, because a health screen that
  refuses to render because one input is missing is the least useful possible
  answer to something being wrong.

- **The funnel caption is a report, not a constant.** It read `estimated until
  an implementation has been measured` on an empty base and on a machine
  running entirely on real figures — the exact confusion the sentence existed
  to prevent. It now says which it is: `nothing measured yet`, `measured for 1
  of 4 implementations, the rest on declared estimates`, `measured`, or
  `measuring is off: ranking on declared estimates for good` when there is no
  base at all. That last one is deliberately not the `yet` wording: `yet` is a
  promise, and it should not be made for a base that is never coming.

- **A dropped provider is amber, not red.** The documentation has always said
  red is for work that cannot be done and that a provider being down is amber,
  because the funnel hands its work to somebody else and the commission still
  finishes. The code said otherwise, and nobody could tell: from a CLI nothing
  ever probed anything, so no provider ever reached `down` and the wrong color
  never showed. Making the record a health input turned it on — permanently, on
  any machine with one client not logged in. Red is now what it claimed to be:
  a capability with nothing left to answer it.

- **A failure is no longer a price.** The cost base averaged every attempt
  together, so an implementation that refused instantly — not logged in, no
  index, no server — recorded a stream of very fast, very cheap calls, became
  the cheapest thing on the board, and was handed every commission from then
  on. Health did not save it, because nothing probed it. Failing cheaply paid
  better than working, and the outage reinforced itself: found on a real
  machine, where twelve refusals in a row moved the funnel off a provider that
  worked and onto one that could not answer at all.

  Only successful calls are averaged now. Attempts and failures are still
  counted — they are what health reads — but they divide nothing, and an
  implementation with a record and no successful call falls back to its
  declared estimate. The trace says so outright instead of leaving a reader to
  wonder why the ranking ignored a base full of rows.

- **Health learns from the record, not only from probes.** Running a step is a
  probe and always was, but that verdict lived in a catalog held in memory, and
  Atenea is a CLI at least as often as it is a service: every fresh process
  started with a clean catalog and forgot every fault before it. A provider
  that refused every single call stayed healthy forever.

  Three failures in a row in the same bin is now an outage — the provider
  leaves the funnel and the trace names the count, the bin and what the
  provider actually said. Three in different bins is degraded instead: a
  provider in trouble with no single cause ranks last but stays, because the
  funnel would rather use a flaky provider than none. Both verdicts expire
  after five quiet minutes, because a provider health has dropped is one
  nothing calls, and nothing that is never called can prove it recovered. A
  streak only ever makes a candidate worse than a prober found it.

- Test runs no longer write into the measurement base of the machine running
  them. The CLI suite searches `/srv/api`, which exists nowhere, so it filed a
  failure on every run; with health now reading the record, enough of those
  made the funnel refuse a working provider on a developer's box and nowhere
  else. The fixture pins its own base.

- **`atenea resume --list` no longer offers a closed `ask` as still worth
  continuing.** `Remaining()` decided "nothing left" from whether any step
  declared `Needs`, and a single `ask` step never does — it has no split to
  wait on. So a receipt closed with `verdict ok` kept reporting its one step
  as remaining, forever: `resume --list` advertised it, and resuming it did
  nothing (`Resume`'s own `KindAsk` branch already asks the receipt itself
  whether the step is `OK`, and correctly no-ops), so the listing and the
  command it was advertising a candidate for disagreed about the same file.
  Measured against a real receipt in this repository:
  `20260804T120304-44df2e` (`code.search in docs-tmp`, closed, reviewed
  `ok`) listed `1 step(s) remaining` before the fix and is gone from the
  listing after it. `Remaining()` now checks `Kind` the same way `Resume`
  already does: an `ask` is done once its one step is `OK`, full stop — no
  `Needs`-based inference, which was never the right question for a shape
  that has nothing to split.

- **Resuming a commission before it ever split could deny the redone look
  the read every commission gets for free.** The never-split resume path
  rebuilt the permission for the redone explore step straight from the
  checkpoint's own `Effects` field — `contract.Permission{Task:
  record.Task, Effects: record.Effects}` — but that field is documented to
  hold only what a commission asked for *beyond* reading, the same
  convention `Run` and `Ask` both follow by adding the free read at
  construction time and never storing it back. A commission that never
  asked for anything heavier than reading — the ordinary case — checkpoints
  with `Effects` empty, so resuming it before splitting ran rebuilt a
  permission with no effects at all, and any adapter enforcing
  `Permission.Allows` would have refused the redone step `permission_denied`
  for reading a repository, something no fresh commission is ever refused
  for. Covered by `TestResumeRedoesExploreWhenSplittingNeverRan`, which
  asserts the redone step's permission allows `EffectRead` from a checkpoint
  recorded with no `Effects` at all. The redone look now starts from the
  same layers a fresh commission does — read, the standing grant, then the
  record's own effects — before `--allow` adds one more.

### Changed

- **The Claude Code timeout is 90 seconds, not five minutes.** Measured: two
  real searches made 8 and 9 turns in 55s and 66s — about seven seconds a turn
  — and *both* were ended by the money ceiling rather than by time. Five
  minutes was a leash nothing on a paid provider could ever reach, while being
  far longer than anybody waiting at a prompt will sit through. The two
  ceilings do not overlap and neither replaces the other: money stops a client
  working too expensively, time stops one that is not working at all, and a
  client wedged on a lock spends nothing at all.

### Added

- **`atenea resume RUN_ID`** picks an interrupted or failed commission back up
  from its own receipt, dispatching only the steps that never closed rather
  than redoing the whole plan. Measured on this repository: a two-step
  commission (`explore-current` then `search-current`) killed right after the
  first step closed came back through `resume` having redispatched only the
  second — 1.033s of real work, the explore step untouched, `closed_at`
  unchanged — and finished `verdict ok` with a receipt no different from one
  that had never stopped. Resuming a run that is already fully closed
  redispatches nothing at all: `spent 0s over 0 step(s)`, same verdict, a
  clean no-op rather than a second billed attempt. `--budget USD` replaces
  what remains of the original grant, in case the first attempt's ceiling was
  the reason it stopped. A receipt written against a repository that no
  longer exists, or against a contract version this build no longer speaks,
  is refused rather than guessed at.
- **`atenea resume --list`** shows every receipt still worth continuing —
  oldest first, with how many steps are left and the verdict so far — instead
  of a person having to open run files by hand to find out what died.
- **`atenea metrics`** prints what the base measured, per capability,
  implementation and repository: attempts, failures, how many were priced, the
  average of the calls that worked and the worst single call. The three counts
  sit together because the gap between them is the diagnosis.
- **`atenea metrics clear`** forgets it, narrowed by `--capability`,
  `--implementation` or `--repository`. The base is the only thing here that
  decides behavior and cannot be edited by hand — true by construction, and
  still true long after the machine it describes has been fixed. Clearing all
  of it needs `--all` on top of the word: it is the one act that destroys
  something nothing can rebuild. Attempts and folded buckets go together, since
  leaving the folded half would let the numbers reappear an hour later.
- A migration carries the successful half of each rollup in its own columns.
  A legacy bucket that mixed successes and failures cannot be split after the
  fact, so it keeps its counts and contributes nothing to cost — the tempting
  repair, keeping the count and zeroing the sum, would invent an average of
  zero and re-create the bug.
- **Atenea can launch and supervise an MCP server itself**, as a bare
  process, instead of always assuming one is already running behind a
  fixed endpoint. `[orchestrator.serena.process]` names a `command` and
  `args` (`{{port}}` is replaced with the chosen port before every spawn);
  Atenea spawns it in its own process group, waits on the same MCP wire it
  serves on for the `initialize` handshake to answer, and restarts it up
  to `restart_limit` times (default 2, "a couple of times" in the design's
  own words) with `restart_delay` (default 2s) between attempts if it
  crashes -- the same break-in posture this design already applies to
  providers. `lifecycle = "persistent"` starts it with Atenea and keeps it
  running; `"on_demand"` starts it on first use and an idle reaper stops
  it after `idle_timeout` (default 5m) with nothing in flight, gated by an
  in-flight refcount so a call already running is never stopped out from
  under itself. A crash only spends a fresh restart budget once the
  server has stayed ready for `stable_after` (default 30s): without that
  window a server that flickers ready and dies resets its own attempt
  count on every brief success and retries forever -- a real infinite
  crash loop, found and fixed by this package's own tests before it ever
  shipped. Every managed process gets SIGTERM, a grace window (default
  5s), then SIGKILL if it is still not gone, both from the idle reaper and
  from `atenea`'s own shutdown.

  `atenea status` gained a `processes` section: state, PID, port, uptime,
  restart count and last failure reason for every server Atenea itself
  launched, printing nothing at all for a setup that never opted in. A
  restarting or down process turns the big light amber, the same as a
  down provider elsewhere on the screen. Verified end to end against the
  real `serena` binary on this machine -- no ToolHive, no manually-started
  proxy: `atenea ask symbol.definition` spawned it, waited for it to
  answer ready, resolved a real symbol in 1.3s, and left no child process
  running after the command exited.

- **`symbol.calls` and `code.impact` answer for the first time, through a
  fourth adapter: the graph backend.** Both walk the call graph
  its MCP process keeps on disk rather than parsing anything live,
  which is also why both implementations declare `requires_index = true`
  and a `min_scale = "medium"` floor — unlike Serena, which reads a file the
  moment it is asked, this provider only ever answers from an index built
  ahead of time, and a repository nothing has indexed, or one too small to
  have declared the scale, is refused rather than handed to a provider with
  nothing to answer from. `code.impact`'s half of the work is a real `git
  diff --unified=0` against the caller's baseline, parsed into per-file
  hunks and walked forward into the symbols the current tree's own lines
  now sit inside — never the baseline's, which would name a symbol the
  change may have already deleted. Verified against this repository: two
  hops out from `cmdResume` in both directions returned 47 real calls in
  27ms; the blast radius of the working tree against `HEAD` returned 11
  affected symbols across the 5 files actually different from it, in 92ms.
  Getting `atenea ask` to reach either one surfaced a real gap predating
  this adapter: the orchestrator's own agent card — the one authority check
  every `ask` passes through — had never declared either capability, so
  both were refused `invalid_input: agent orchestrator may not ask for
  symbol.calls` before a single implementation was ever consulted. The card
  now names both.

- **`symbol.calls` and `code.impact` now say when their own answer might
  already be behind.** Both walk a call graph the MCP backend built at
  some point in the past, and nothing forces a rebuild before the next
  question: a commit lands, a file changes on disk, and the graph never
  hears about either — there is no watcher, no hook, nothing that reindexes
  on its own. Every call now asks two cheap questions before answering —
  `index_status` for whether HEAD has moved since the index was built, `git
  status --porcelain` for whether the working tree holds changes nobody has
  indexed — and attaches a plain-text notice when either is true. Measured
  against this repository with both conditions live at once: `index_status`
  and `git status --porcelain` together cost 18ms, cheaper than the 27ms
  `symbol.calls` itself took and the 92ms `code.impact` took, but real cost
  paid on every single successful call, unconditionally, because there is
  no cheaper moment to pay it in. The check is best-effort by design: it
  cannot refuse an answer that already succeeded, and a check that itself
  fails — no git repository, `index_status` erroring — reports nothing,
  indistinguishable from a check that ran and found nothing wrong, because a
  caller cannot act on that difference either way. Contract `1.3.0`:
  `Outcome.Notices` is additive, the same shape as `1.2.0`'s `canceled` bin
  — an adapter built against `1.2.0` never populates it and goes on
  compiling.

  `atenea ask` shows it beside the answer whether or not `--trace` is
  passed — the common case is a plain `ask`, and a caveat about the very
  data on screen must not hide behind a flag most callers never reach for.
  Under `--trace` it is shown once, in the per-step trace alongside the
  review it qualifies, not said twice.

- **`code.search` declares that answering it spawns a process, and every
  real implementation always did.** `Effect` gains a fourth value,
  `process`, orthogonal to `read`/`write`/`external`: not what a capability
  changes, but whether answering it runs a binary Atenea does not fully
  control the internals of. `ripgrep` and the local stand-in are both a
  binary invoked through `exec.CommandContext`, so refusing this effect by
  default would not make the spawn auditable, it would make the one P0
  capability unusable out of the box. It is granted by a new standing layer
  instead of by every caller by hand: `[orchestrator] effects = ["process"]`
  in the settings file is a grant every commission and question receive on
  top of the free read, the same way `budget_usd` already stands behind
  every commission's spending, and the shipped `default.toml` turns it on
  so a fresh install works unchanged. An operator who wants every caller to
  ask for it explicitly can delete the line; nothing about `code.search`
  requires it beyond what it already declared.

  A one-off order beats the standing grant: `--allow EFFECT` on `atenea
  task`, `atenea ask` and `atenea resume` adds to what a single commission
  carries, repeatable for several, refused with `invalid_input` at the flag
  for a name this build does not recognize rather than reaching a step and
  failing there for a reason nobody can trace back to the typo. `resume`'s
  `--allow` only ever adds: unlike `--budget`, which replaces what remains
  of the original grant, an effect already held is never worth losing by
  accident — `Permission.Grant` composes read, the standing grant, what the
  commission or step already carried, and `--allow`, in that order, keeping
  each effect once regardless of how many layers named it. Contract
  `1.4.0`, additive: `Effect` was already an open `uint8`, not a set an
  adapter built against `1.3.0` could exhaust, so it goes on compiling and
  simply never sees the new value.

### Documentation

- The settings file **replaces** the built-in defaults rather than patching
  them. A file holding only an `[orchestrator]` block is a complete description
  of an Atenea with no catalogue at all: it boots, reports red, and answers
  `unknown capability` to everything. `atenea config init` writes the whole
  file to start from. This was true from the first release and written nowhere.
- `.serena/` is ignored. Serena writes a project config into whatever
  repository it is pointed at, describing one machine and belonging to nobody
  else.
- The shipped `default.toml` documents `[orchestrator.serena.process]`
  commented out and inactive: supervision is opt-in, and a machine that
  already points Serena's `endpoint` at ToolHive or a hand-started proxy
  sees no change at all.
- **`symbol.definition` and `symbol.references` have answered for the first
  time.** Not a code change: every line of the adapter, the funnel and the
  failure bins was already there. What was missing was a Serena with a Go
  toolchain behind it — the shipped container never had one. Run against a
  bare-process Serena instead, `symbol.definition` resolved a real call site
  to its real declaration in this repository, and `symbol.references` found
  all six real call sites of a symbol in one pass. `symbol.implementations`
  still does not: it now fails clean into `unavailable` — Go's language
  server not answering the `textDocument/implementation` request Serena's
  tool sends — rather than never being reachable at all. Detail and evidence
  in `docs/content/not-built-yet.md`.

## [0.1.0] - 2026-08-02

First tagged release. Atenea decides and delegates: `goal -> capability ->
implementation`. It runs as a core outside the CLIs it serves, four capabilities
answered by three adapters, with a funnel that learns which provider to pick
from what it measured rather than from what somebody typed.

Built as eleven bricks, each leaving something that runs.

### The core and the funnel

- **`pkg/contract`** — the versioned seam between the core and its adapters.
  `Capability` with a checkable input/output schema, `Implementation` in four
  blocks (capability, constraints, cost, health), `Repository` as the unit of
  work, and six failure bins every adapter sorts its far side's errors into.
- **Capability Registry** — the catalogue, safe for concurrent chats, refusing
  orphan implementations and handing out defensive copies.
- **The selector** — a funnel, in stages that each answer one question:
  constraints say who *can* work here, reach says who is *wired up*, health says
  who is *available*, and cost ranks whoever is left. A standing user rule
  outranks the automatic ranking. Every stage records what it dropped and why.
- **Settings** — one declarative TOML file with embedded defaults, so a fresh
  install boots with no file at all. Unknown keys are refused rather than
  ignored.
- **The status screen** — two heights: Atenea as a whole, and one line per
  implementation. Failure bins map onto distinct exit codes.

Cost was deliberately left out of the funnel until real measurements existed
(bricks 8 and 9 below); until then the funnel ranked on reach and health alone.

### The orchestrator

- Turns one sentence into finished work: explore the repositories in scope,
  split the commission into a DAG of steps, dispatch in waves, review every
  answer. Not part of the core — the core says who *should* act, the
  orchestrator acts.
- **Look before you split.** The exploring pass is real and measured; a total
  that leaves it out reports a number that never happened.
- An edge means "after". A step whose prerequisite failed review is blocked
  rather than dispatched, stays on the record with the reason, and only that
  branch stops.
- **Run receipts.** A run is dumped as each step closes and again when the run
  closes, including when it was cut short. Written to a temp file and renamed
  into place, so an interrupted dump never looks like a finished record.

### Adapters

- **omp** — the first client adapter, replacing the local stand-in as the
  shipped far side. Intent-shaped flags (`match_case`, `regex`, `whole_word`)
  are folded into the pattern because omp has no flag for any of them; the
  answer is parsed back with an anchored separator so a path containing a colon
  survives, the column the capability requires is recovered from the line, and a
  search that hit its limit is reported as partial rather than complete.
- **Claude Code** — the second first-class client. `runner` became `runners`, a
  list: with two clients that are both first class, one slot would have made one
  of them permanently unreachable. Two runners claiming the same implementation
  is refused at load rather than settled by map iteration order.
- **Serena** — not a CLI at all. An MCP server behind a local proxy, so this
  adapter holds a session and speaks JSON-RPC over HTTP instead of spawning a
  process. Everything above the seam is unchanged.
- **Chat sessions.** The unit of isolation is the chat, not the client. Two
  chats may be open at once, each with several repositories, and neither may
  read the other's context or borrow its permissions. What they share is the
  floor: the catalogue, the measurements and the history.
- An adapter's far side may *think*, and a thinking far side can report a file
  it was told to leave alone. Every answer comes back through the same scope and
  type checks the request declared, so the security design stays real rather
  than advisory.

### Capabilities

- `code.search` — literal text, tool-agnostic.
- `symbol.definition`, `symbol.references`, `symbol.implementations` — answered
  by Serena.
- Atenea's contract names a **position**, because that is what an editor has;
  Serena's API names a **symbol**. Reading the word under the cursor is the
  adapter's job, and the trace says which name it resolved to.
- **`atenea ask`** — one capability against one repository, through the same
  funnel, review and receipt as any step. The atom a workflow is built from, and
  the way a client that already has a cursor hands it over.

### Learning from what it measured

- **The measurement base.** Every attempt is measured into an embedded DuckDB
  file: time, tokens and peak resident memory, filed under the capability asked
  for and the implementation that answered. Failed attempts too, with their bin
  and the untranslated reason, so a provider that fails expensively stops
  looking cheap.
- The core is the only writer. Writes batch and reach disk on a beat, when a
  phase closes, and before shutdown. Attempts fold hour to day to week to month;
  folding is idempotent and only closed periods fold.
- **Cost joined the funnel** as a ranking and never a filter: an expensive
  provider that is the only one left still gets the work. It breaks a tie only
  when one side is cheaper on both axes, because trading time against tokens
  needs an exchange rate nobody has.
- An implementation ranks on its declared estimate until it has real
  measurements of its own **on that repository**, then on the real ones. The
  trace prints `estimated` out loud so nobody reads a guess as an observation.
  Measurements are read for the running tool version only, so an upgrade starts
  a fresh baseline instead of dragging old numbers along.

### Money is a permission

- A spending ceiling is a **grant**, decided before anything ran, not a
  measurement of what something cost. `budget_usd` belongs to `[orchestrator]`
  and funds **one commission**, split between its steps rather than copied to
  each: four steps share one quarter instead of spending four.
- Running out is `permission_denied`, never a health verdict. The provider was
  not slow and is not down — the grant Atenea passed down was too small — so the
  funnel does not learn a lie about it.
- An exhausted commission keeps working through whoever charges nothing.
  `atenea task --budget` funds one commission above the settings file; a
  negative grant is refused rather than clamped to silence.

### The crash notebook

- Atenea's **own** faults land on disk the instant they happen: one line of JSON
  per fault, synced before the call returns. A batched notebook loses the last
  entry in exactly the crash it exists to describe.
- A provider being down is an ordinary answer with a bin of its own and is not
  filed here. Filing those would bury the rare entry that matters.
- Background jobs get the opposite treatment from foreground work: nobody waits
  on their return value, so a flush failing every 30s for an hour used to look
  exactly like one succeeding.
- **Names, never values.** An entry carries the shape of the work and the
  payload's *keys* — same rule as the sensitive-path list, for the same reason:
  a crash dump is the likeliest artifact to be pasted into a bug report.
- Reading changes nothing. `atenea incidents clear` is a separate word and marks
  read rather than deleting. A torn last line is counted and announced, never
  skipped.

### Running as a service

- **`atenea service install`** writes a `systemd --user` unit. A user unit,
  never a system one, and nothing listens: no port, no socket, no API. The
  commands are not clients of the service — they are the same core reading the
  same disk, which is why they work whether it is running or not.
- **One background lane** for the three rhythms (measurement flush, history
  roll-up, copies). They touch the same files, so a second lane would only buy
  the chance of two of them meeting on the same one.
- A rhythm's first pass is due on the **first** beat. A six-hour rhythm on a
  laptop shut every evening would otherwise hand out a due date it never
  reaches, and the copy nobody notices missing is missing forever.
- **Copies.** A hard-linked snapshot of everything Atenea has learned, taken
  every six hours, five kept in rotation, beside the state root and never inside
  it. Five copies of an unchanged base cost one base; a changed file is copied
  whole and the older snapshot keeps the older bytes, so dropping the oldest is
  safe at any moment.
- **Coming up after an ugly close.** Damage is assessed before any work is
  accepted, never lazily on first use. An interrupted dump is swept, a receipt
  that will not parse is renamed rather than deleted, and a measurement base
  that will not answer is moved aside under its own name so a fresh one can open
  where it was. A base another live Atenea is holding is never touched: moving a
  healthy file out from under a running process would manufacture the corruption
  the check exists to catch.
- The status screen reports the rhythms, the copies and the repair, and every
  fact on it comes from disk or from the settings file — never from a tally in
  the printing process's memory, which would be a different number for every
  reader.
- One OS box, `internal/platform`, is the only place that knows where state,
  settings and copies live on this machine.

### Notes

- The version is a constant in the source, not a link-time flag. A version
  injected with `-ldflags` is one somebody has to remember to inject, and the
  build that forgets does not fail — it ships claiming to be whatever the source
  said. The release workflow refuses a tag that disagrees with the constant.
- A binary built from a checkout appends its revision as SemVer build metadata,
  and a dirty tree says `modified`: `0.1.0+9b34dd0.modified` is not a release,
  whatever the number claims. Build metadata is ignored when versions are
  compared, which is the right meaning — it *is* 0.1.0, built from that tree.
- **One binary ships: `linux-amd64`.** The measurement base is an embedded
  DuckDB, which is a cgo dependency, so cross-compiling needs a C toolchain per
  target and `CGO_ENABLED=0` fails outright rather than degrading. Rather than
  publish a binary for a machine nobody has run Atenea on, the release carries
  the platform its suite passed on. Everything else builds from source with
  `go build ./cmd/atenea`, which is what the README documents anyway — and
  `atenea service install` is implemented for `systemd --user` and says so
  plainly everywhere else.

[1.0.0]: https://github.com/Tutitoos/atenea/releases/tag/v1.0.0
[0.10.3]: https://github.com/Tutitoos/atenea/releases/tag/v0.10.3
[0.10.1]: https://github.com/Tutitoos/atenea/releases/tag/v0.10.1
[0.10.0]: https://github.com/Tutitoos/atenea/releases/tag/v0.10.0
[0.9.1]: https://github.com/Tutitoos/atenea/releases/tag/v0.9.1
[0.9.0]: https://github.com/Tutitoos/atenea/releases/tag/v0.9.0
[0.8.0]: https://github.com/Tutitoos/atenea/releases/tag/v0.8.0
[0.7.0]: https://github.com/Tutitoos/atenea/releases/tag/v0.7.0
[0.6.1]: https://github.com/Tutitoos/atenea/releases/tag/v0.6.1
[0.6.0]: https://github.com/Tutitoos/atenea/releases/tag/v0.6.0
[0.5.0]: https://github.com/Tutitoos/atenea/releases/tag/v0.5.0
[0.4.0]: https://github.com/Tutitoos/atenea/releases/tag/v0.4.0
[0.3.0]: https://github.com/Tutitoos/atenea/releases/tag/v0.3.0
[0.2.0]: https://github.com/Tutitoos/atenea/releases/tag/v0.2.0
[0.1.0]: https://github.com/Tutitoos/atenea/releases/tag/v0.1.0
