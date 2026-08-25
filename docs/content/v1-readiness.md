---
title: v1 readiness
weight: 7
---

# v1 readiness

This page is the acceptance record for the published releases. The evidence below the
"Latest 1.0.5 verification" heading was measured against that release and stays
filed under it; what was checked for 1.1.0 has a section of its own at the end.
It separates what is
implemented and tested in this repository from capabilities that would require
a new contract or an external provider decision. The normative policy is in
[`v1.0 policy`]({{< relref "v1-policy" >}}).

## Code-backed and shipped

| Area | Evidence | Status |
| --- | --- | --- |
| Core, registry, selector and orchestrator | `pkg/contract/`, `internal/registry/`, `internal/selector/`, `internal/orchestrator/` | Implemented and covered |
| Persistent history | `internal/agent/agent.go`, `internal/trace/`, `internal/metrics/` | Lazy history loading is wired; tests cover prior-run discoveries |
| Permission effects | `pkg/contract/workflow.go`, `internal/core/`, `cmd/atenea/main.go` | Explicit grants and `--allow` are enforced |
| Budget boundary and plan execution | `internal/core/commission.go`, `internal/orchestrator/orchestrator.go`, `pkg/contract/workflow.go` | Reported over-budget results are rejected centrally; reviewed multi-capability DAGs run through `RunPlan` and resume from `KindPlan` checkpoints |
| Measurements and wandering | `internal/metrics/summary.go`, `internal/metrics/compact.go`, migrations `0006` and `0007` | `out_of_scope` is persisted, rolled up and reported |
| Completeness reporting | `internal/agent/model/model.go`, `internal/agent/planner/` | Missing or partial coverage is represented and tested |
| Ranked structural search | `symbol.search`, `internal/adapter/serena/serena.go`, `internal/adapter/serena/symbols.go` | Serena declarations are filtered, ranked deterministically and returned with qualified name, kind and source range |
| MCP lifecycle and passthrough | `internal/mcpstdio/`, `internal/passthrough/`, `internal/supervisor/`, `internal/core/mcp.go` | Shared and per-chat lifecycle, raw allow-list and effects are validated |
| Continuous MCP health | `internal/core/backendstate.go`, `internal/core/startup.go`, `internal/config/default.toml` | Service probes declared servers every 15 minutes by default and persists the last handshake result; `0s` disables it |
| Supported adapters | `internal/adapter/omp/`, `claudecode/`, `codex/`, `serena/`, `kivgraph/`, `tokensave/` | Native adapters compile and are tested; Tokensave is active on the audit machine |
| OpenCode model backend | `internal/agent/opencode/`, `internal/agent/model/`, `scripts/opencode-smoke.sh`, `scripts/opencode-matrix.sh` | Opt-in adapter, local structured-schema and observed-budget enforcement, protocol fixtures, safe real-provider smoke and free-provider/MCP matrix |
| Citation traceability gate | `internal/agent/reviewer/citations.go`, `internal/agent/reviewer/citations_test.go`, `internal/agent/review_integration_test.go`, `internal/config/default.toml` | Every prose field requires evidence; paths, lines, fragments, abbreviated paths, directory renames and multiple sources are audited and retained in the report, including through the real Runner |
| Semantic review | `internal/agent/semanticreviewer/`, `cmd/atenea/agent.go`, `internal/config/default.toml` | Opt-in model-backed judgement of whether conclusions follow from evidence; returns supported/unsupported/indeterminate with confidence, claims, gaps and scope; deterministic citation review remains mandatory |
| Installation and operations | `scripts/install.sh`, `scripts/release-smoke.sh`, `docs/content/operations.md` | Install, update, rollback, uninstall and release smoke are verified |
| Release gate | `.github/workflows/ci.yml`, `release.yml`, `postrelease.yml`, `v1-readiness.yml` | Linux/macOS amd64/arm64 validation is automated; CI enforces 80.0% globally and records one canonical history artifact |
| v1.0 policy gate | `scripts/v1-policy-check.sh`, `docs/content/v1-policy.md` | The declared guarantees and deferred contracts have stable anchors |
| Kivgraph impact and indexing | `internal/adapter/kivgraph/impact_index.go`, `internal/config/default.toml` | `code.impact` and `repository.index` are catalogued and provider-backed; indexing remains explicit `write+process` |
| Durable state and IPC | `internal/backup/`, `internal/checkpoint/`, `internal/ipc/` | Files and published directories are synced; Unix peer credentials are checked and unsupported platforms fail closed |
| Performance regression barriers | `scripts/benchmark-check.sh`, `scripts/load-check.sh`, `pkg/contract/workflow_bench_test.go`, `.github/workflows/ci.yml` | Selector, metrics and medium DAG load benchmarks run with CI thresholds |

## Acceptance command

Run from a clean checkout:

```sh
bash scripts/v1-readiness.sh
```

The gate checks repository hygiene, removal of references to the retired
backend, formatting, module tidiness, vet, native build, the complete race
suite, benchmark thresholds and shell entry points. It does not publish a release.
The declared provider matrix is checked separately from live integrations by
scripts/provider-matrix-check.sh. It proves that the shipped catalog still
contains the required capability-to-provider edges and that the effective
configuration parses. A live MCP probe is opt-in with ATENEA_MCP_CHECK=1;
credentials, paid model calls and provider-side indexing are not fabricated by
this local gate.
The v1-readiness workflow exposes two opt-in jobs: ATENEA_LIVE_MCP starts an
isolated service and performs the MCP bridge handshake, while
ATENEA_OPENCODE_SMOKE runs the real provider/MCP matrix. Neither job runs on
ordinary pull requests unless the repository variable is explicitly enabled.
The same jobs can be launched manually with `live_mcp=true` and
`live_opencode=true`; credentials and provider availability remain external
inputs and a failed live job is recorded as an environment/provider result,
not converted into a local readiness claim.

CI applies a 77.0% regression floor and a hard 80.0% global target. The latest
local observation is 80.1% under the race-enabled command used by CI. GitHub
Actions stores one canonical Linux coverage summary for 90 days and compares
each successful main run with the previous successful main point, allowing a
regression greater than one percentage point to fail.

On 2026-08-22, Hugo `0.165.0` built `docs/` locally without errors. `atenea wrap opencode --version` completed MCP handshakes for all
8 configured team servers: 2 were declared directly and 6 were retained as
raw or without a direct surface; no external tool was invoked.

The wrapper-only handshake check repeated the handshake through `atenea wrap` for `opencode`,
`claude` and `codex`: all 8 servers were reachable, with 2 declared directly
and 6 held as raw or internal surfaces. The installed versions were Claude
Code `2.1.228`, Codex CLI `0.149.0`, OpenCode `1.18.15`, Serena
`1.28.1`, Context7 `4.0.2`, Semgrep `1.23.3`, claude-mem `13.15.3`,
agent-device `0.20.10`, Maestro `1.0.0`, Headroom `1.29.0` and Chrome DevTools
MCP `1.7.0`. That handshake-only check performed no model turn, scan, write or
device action; the later provider validation is recorded below.

Claude Code and Codex remain optional provider surfaces. The earlier minimal
`code.search` for `CapabilityIndex` passed through each wrapper within the
configured `0.25 USD` ceiling: Claude reported `0.210193 USD` and Codex did
not report monetary usage. In the latest external-target recheck, Claude Code
reached its spending ceiling with `0.310367 USD` observed usage. Codex later
completed an isolated external search in `81.8s`; its effective timeout is now
`120s`. The MCP health display
remains on-demand by design; a handshake proves reachability, not the behavior
of every raw tool.

The same day, direct calls through the official MCP transport passed against
Serena, Context7 `4.0.2`, Semgrep `1.23.3`, claude-mem `13.15.3`,
agent-device `0.20.10`, Maestro `1.0.0`, Headroom `1.29.0` and Chrome DevTools
MCP `1.7.0`. Read-only and diagnostic tools were exercised; device, browser
interaction, write and destructive actions were excluded. On the external
TypeScript target, claude-mem answered but its structural search returned no
symbols and its outline could not parse `server.ts`.

## Maintenance recheck

The maintenance recheck on 2026-08-22 replaced timing sleeps in the workflow
resume tests with process-start markers. `TMPDIR=/tmp go test -race -count=5
./internal/workflow` passed all five repetitions. The MCP safety harness also
passed in normal and race mode across `internal/core`, `internal/passthrough`,
`internal/mcpstdio` and `internal/mcpprobe`; destructive `write`, `process` and
`external` fixtures were denied before the backend and receipts remained
available for refused calls.

The configured `claude-mem` process (`13.15.3`) was queried read-only: its
handshake and `tools/list` exposed 14 tools, `list_corpora` returned `[]`, the
memory search returned no observations, the backend TypeScript tree scanned
101 files but produced no structural symbols, and `src/server.ts` could not be
parsed. No memory, corpus or session was written.

The opt-in OpenCode matrix passed all 6 cases with version `1.18.15`: three
free models without MCP and the same three with the isolated Atenea MCP bridge.
All six reported `cost_usd=0.000000`; the MCP cases called
`atenea_catalog_repositories`. Claude Code was not called again because its
previous `0.310367 USD` observation already demonstrated the provider
overspend; repeating it would add cost without new evidence.

Kivgraph `0.3.4` was then configured with Atenea as a fifth repository and
indexed successfully as generation `000009`. Atenea's real service detected
the index as ready; `graph.status`, `symbol.definition`, `symbol.references`,
`symbol.overview`, `symbol.consumers`, `symbol.get` and `symbol.unresolved`
passed through `kivgraph` against the Atenea repository. The adapter also
accepts the installed compact `groups[].files[].at` outline format. The local
binary was rebuilt with LadybugDB `v0.13.1` and now publishes
`get_unresolved_references`.
The Kivgraph registry excludes `docs/`, which is an independent Go module
rather than part of the root graph.

The Kivgraph adapter also provides `code.impact`: it parses a Git baseline
diff, resolves current declaration roots with `get_file_outline`, and walks
incoming consumers with `get_blast_radius`. `repository.index` runs the
official `kivgraph index --full --json` command only with explicit write and
process permission, then verifies the published counters through
`graph_status` for readiness while returning the authoritative counters from
the index command's final JSONL result. Both paths have unit coverage; impact intentionally omits
foreign repositories because its contract returns repository-relative paths
without a repository field.

The final workspace recheck also ran the official index command from the
TaxiPrime workspace root. It completed as generation `000011` with 5 logical
repositories, 656 files, 19,885 symbols, 72,034 edges and 5,080 unresolved
references. `taxiprime-root` remains intentionally absent from the published
graph: it is the aggregate workspace root, while Kivgraph registers the five
logical repositories beneath it. Atenea therefore keeps `indexed_by=[]` for
that aggregate target; marking it as ready would misrepresent the provider's
repository model. The five logical repositories remain ready and provider
backed.

Tokensave `7.10.0` was installed from the official Homebrew tap, initialized
for `/Users/gtrave/Documents/atenea`, and configured as an on-demand runner.
Its live MCP server reported 11,586 nodes, 27,170 status edges and 311 files;
`code.context`, `symbol.overview` and `symbol.calls` passed through Atenea.
The anonymous upload counter was disabled. Large `tokensave_entities` answers
can be truncated by the upstream server; Atenea detects that case and retries
with the official `kinds` filter, then merges and deduplicates the bounded
responses. A real large-file overview returned 45 symbols successfully.

## Latest 1.0.5 verification

On 2026-08-23 the complete current snapshot was applied to an isolated
temporary clone and committed only inside that clone so the clean-tree gate
could run. `bash scripts/v1-readiness.sh` passed all nine stages, including
`go test -race -count=1 ./...`, policy anchors and the `1.0.5`/`3.3.0` build
identity. The release was subsequently published by the release workflow.

The end-to-end test
`internal/agent/review_integration_test.go` launched the real
`agent-exec reviewer` through `internal/agent.Runner`; it accepted two
content-checked citations, validated the strict result schema and confirmed
the reviewer trace points to the work run.

The existing public release was also checked with:

```sh
bash scripts/release-smoke.sh 1.0.5
```

Checksum verification, install, same-version update, rollback and uninstall
passed on macOS arm64. This validates the public `1.0.5` release lifecycle;
the smoke test itself does not publish a release.

The current phase was rechecked on 2026-08-23. `TMPDIR=/tmp go test ./...`,
`TMPDIR=/tmp go test -race -count=1 ./...`, `go vet ./...`, `go build ./...`,
the policy gate and the `1.0.4` release smoke passed. The timing margins in
the Claude/OpenCode fixtures now tolerate loaded machines and the race
detector while remaining below the child-process lifetime.

The same recheck ran the complete 13-capability matrix against the active team
overlay. All 13 returned `ok`, including `code.impact` and `repository.index`
with explicit test/index permissions, plus `symbol.unresolved` through
Kivgraph, a large-file `symbol.overview` through Tokensave, and
`symbol.calls` through a real indexed symbol. Serena and Kivgraph alternative
implementations also returned `ok`; `atenea detect --repo atenea` reached all
8 configured MCP servers. Hugo `0.165.0` built the documentation site successfully.
The two minimal Claude/Codex searches used the configured budget; the OpenCode
matrix used the configured free models. The release workflow and post-release
smoke also passed.

The team service is now installed from `/Users/gtrave/.local/bin/atenea` rather
than a Go build-cache path. Serena runs persistently for six repositories with
`--open-web-dashboard False`; Headroom serves `127.0.0.1:8787/dashboard` with memory
disabled, and Maestro's Viewer serves `127.0.0.1:8765` through a launchd wrapper
using OpenJDK 21. None of these launch agents opens a browser automatically.

## Latest real-provider smoke

On 2026-08-21, `scripts/opencode-smoke.sh` passed against the installed
OpenCode `1.18.15` CLI and the `opencode/hy3-free` model. The run completed a
real `--format json --pure` turn in 80.03 seconds, produced text plus a
structured `{ "ok": true }` answer, and passed Atenea's local schema check.
It supplied no Atenea MCP tools and did not use `--auto`.

This is evidence for one real model and one local CLI version, not a guarantee
for every OpenCode provider or future release. The smoke remains opt-in and
may create the provider client's normal local session state.

## Provider, MCP and permission matrix

On 2026-08-22, `scripts/opencode-matrix.sh` ran the installed OpenCode
`1.18.15` twice per model: once without MCP and once with an ephemeral Atenea
service. The free models below passed both turns, returned the requested local
JSON object and reported zero provider cost across two OpenCode providers:

| Provider/model | No MCP | Atenea MCP | MCP evidence | Input tokens, no MCP / MCP | Time, no MCP / MCP |
| --- | --- | --- | --- | ---: | ---: |
| `opencode/hy3-free` | passed | passed | `atenea_catalog_repositories` | 6,158 / 22,756 | 82.51s / 88.99s |
| `opencode/mimo-v2.5-free` | passed | passed | `atenea_catalog_repositories` | 6,287 / 26,374 | 83.57s / 87.34s |
| `opencode-go/ox-alpha-free` | passed | passed | `atenea_catalog_repositories` | 7,152 / 22,896 | 85.47s / 103.46s |

The MCP half used the actual `atenea mcp` bridge and an isolated state/socket
directory; the service announced 13 capabilities. The runner recorded
`tool_use` events instead of trusting the model's prose. The matrix and the
unit contract both verify the safe headless arguments `--format json --pure`
and the absence of `--auto`; no paid model was used. This is evidence for three
free models across two providers on one CLI version, not provider-wide
compatibility.

On 2026-08-24, the manually dispatched readiness workflow passed the general
gate, all four native builds and the live MCP bridge. Its first OpenCode run
passed `opencode/mimo-v2.5-free`, while `opencode/hy3-free` and
`opencode-go/ox-alpha-free` returned an external `Unexpected server error`.
The targeted retry passed `hy3-free` and `mimo-v2.5-free` and reproduced the
same error only for `ox-alpha-free`. This keeps the live job red as an honest
provider-availability signal; it does not indicate a failure in Atenea's MCP
bridge or local readiness gate.

## External target recheck — 2026-08-22

The optional MCPs were rechecked against the TaxiPrime workspace and a public
documentation target using read-only or diagnostic calls. Serena, Kivgraph,
Context7, Semgrep, agent-device, Maestro, Headroom and Chrome DevTools answered
real calls. Tokensave refused `taxiprime-backend` because its configured root
is Atenea, which is the expected scope boundary. claude-mem answered but its
structural search found no symbols in the TypeScript target and its outline
could not parse `server.ts`.

The optional model providers were also exercised again against
`taxiprime-backend` with the existing `0.25 USD` budget. Claude Code was
authenticated but stopped at its spending ceiling; the
provider reported `0.310367 USD` observed usage, including `0.060367 USD`
above the requested ceiling, and returned no search result. The adapter now
also rejects a success envelope whose reported cost exceeds the permission,
while retaining the observed charge. Codex completed the
diagnostic search in `81.8s` with the timeout override. Neither result changes
the core readiness gate. The
external calls did not
index repositories, launch apps, open a persistent browser, or perform
mutating device/MCP operations.

The readiness script now defaults its temporary test state to `/tmp` on macOS
to stay below the kernel's UNIX-socket path limit; an explicit
`ATENEA_TEST_TMPDIR` may be supplied when a different temporary root is
required.

The final local provider smoke used the installed Claude Code `2.1.228` and
Codex CLI `0.149.0` against disposable repositories. Claude Code exposed two
streaming cost events and was canceled by the observer at the test ceiling
after reporting `0.055262 USD`. Codex initially exposed an invalid nested
JSON-schema error; the adapter now closes nested objects with
`additionalProperties: false`, and the follow-up smoke completed with `ok`, one
match, 40,558 tokens and one provider observation. This validates the real
Codex completion path as well as the adapter's streaming event path.

## 1.1.0 verification

On 2026-08-25 the audit remediation was merged to `main` as fifteen commits and
this is what was run against the merged tree, on this machine unless the run is
named as a CI one:

- `bash scripts/v1-readiness.sh` passed all nine stages, including
  `go test -race -count=1 ./...`, the policy anchors and the `1.1.0`/`3.3.0`
  build identity.
- CI passed all seven jobs on `main`: four native builds, the lint job on both
  ubuntu and macOS, and the widget parser.
- Coverage was 81.3% on ubuntu and 81.4% on macOS, against a hard 80.0% target
  and a 77.0% floor. The history check compared it with the previous successful
  `main` run and reported `previous 80.0%, current 81.3%`.
- The three fuzz targets ran in CI for thirty seconds each and found nothing.
- `golangci-lint` v2.12.2 reported zero issues on both platforms.

What was NOT re-run for this release: no live MCP probe, no paid provider call,
no OpenCode matrix, and no provider-side indexing. Those are the opt-in jobs and
they stay opt-in; the observations recorded above them in this page are still
the ones from 1.0.5 and are dated as such.

Two changes in this release can stop an existing installation from starting, and
both are stated in the changelog: an `[[agent]]` block that omits `max_tokens`
is now refused rather than silently given no ceiling, and a service whose
catalog declares a repository by a relative path is refused rather than
resolving it against whatever directory the unit file left it in.

## Deliberately deferred

These are not hidden failures in the current product. They are explicit v1.0
decisions or later contracts:

- an *implicit* interactive permission prompt: no adapter and no daemon path
  stops mid-run to ask. A service under systemd or launchd has no terminal to
  be asked on, so such a prompt would hang the run or wait for nobody; the
  security model stays explicit grant/refusal through policy and `--allow`.
  The *explicit* confirmation is not deferred and already ships: `--confirm` on
  `task`, `ask`, `decide --run` and `agent` prints the budget and the effects
  about to be granted and refuses outright without a TTY, `agent` demands it
  for any type declaring write or external effects, and `backup discard` takes
  no other word for an answer;
- exact OpenCode parity with Claude Code: the opt-in provider adapter is
  hardened, maps common provider errors and checks reported cost against the
  requested budget, but OpenCode still lacks a native schema flag, a provider
  hard cost cap and a permanently reliable terminal event guarantee;
- exact provider-side hard per-turn token enforcement: Atenea now carries
  `limits.max_tokens` into model turns, stops contained streams as soon as an
  observed over-limit event arrives and rejects the result locally, but
  supported external providers still do not expose one uniform mid-turn hard
  cap, so provider work already in flight can overshoot;
- default semantic verification of narrative claims: the opt-in
  `semantic-reviewer` can judge whether a conclusion follows from its evidence,
  but it is model-backed, costs the configured explore budget and may return
  `indeterminate`; deterministic citation verification remains the acceptance
  gate. Renamed paths with a different basename remain unresolved until the
  answer supplies the current path.

The historical design notes remain in
[`What is not built yet`]({{< relref "not-built-yet" >}}). This page is the current acceptance
record and should be updated when one of the deferred contracts is actually
chosen and implemented.

## Dashboard contract

MCP declarations may include an optional `dashboard` HTTP(S) URL. Atenea
validates the URL and derives a DNS-safe alias from the MCP id. Dashboards are
reported by `status` and `detect`, but starting or probing an MCP never opens a
browser. Use `atenea dashboard <id>` for an explicit accessibility check and
browser launch, or `atenea dashboard <id> --check` to keep the operation
headless. `atenea dashboard hosts --dry-run` previews the managed hosts block;
the real `/etc/hosts` file is changed only by the explicit hosts command.
Serena is discovered by the active project because each persistent repository
instance receives a dynamic dashboard port; its instances are warmed
sequentially to avoid dashboard port collisions.
