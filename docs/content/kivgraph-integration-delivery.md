---
title: Kivgraph integration delivery — 2026-09-04
weight: 7
---

## Implemented

Contract 4.0 remains additive. The provider matrix now has 39 implementations.
Kivgraph supplies structural search, source batches, cross-repository impact,
repository inventory, explicit freshness maintenance and composed code context.
Existing definition, references, overview, intent, dependencies, consumers,
symbol retrieval, status, diff impact and indexing remain supported.
Unsupported implementation/unresolved capabilities remain unoffered.

Search identities can feed `symbol.get`. Directory outlines retain file paths.
Graph evidence receipts retain generation, freshness, completeness, coverage and
pagination, alongside the existing usage receipt and compatibility alias.
Structural exploration prefers graph context; literal queries keep text search.
Provider preferences and repository-specific Tokensave exclusions are preserved.

The Kivgraph index publishes a generation-bound content attestation. Atenea
requires matching current inventories before and after graph reads. This covers
uncommitted changes, new/deleted files and configured exclusions; commit equality
alone is insufficient. Missing registered roots block queries. Coverage limits
are separate from freshness and do not provoke blind rebuild loops.

Automatic full indexing is disabled by default. This installation explicitly
authorizes rebuilding registered repositories only. Full passes share a
cross-process lock, record failed attempts and use the official CLI. Explicit
maintenance can retry failures. A verified repair allows runtime-failed graph
queries to be probed again; it does not claim all queries work.

## Initial local installation

This section records the installation before the later authorized PR preparation.
It is not a statement of the current branch or publication status.

- Atenea: `1.1.0+399034e.modified`, contract `4.0.0`.
- Kivgraph: `0.9.2-local.atenea-freshness`, full native engine and web viewer.
- Index deadline: 30m. Local active desktop profiles: 31m.
- Codex MCP deadline: 1860 seconds, including generated wrapper configuration.
- Index subprocess PATH is explicit; service PATH previously lacked Go.
- The authorized `fourmans-web` path correction preserves its repository identity.
- No commit, push, publication or external PR merge was performed.

Rollback copies:

- `~/.local/opt/kivgraph-backup-before-atenea-freshness-20260904`
- `~/.config/kivgraph/repositories.yaml.before-atenea-integration-20260904`
- `~/.config/atenea/atenea.toml.before-kivgraph-integration-20260904`
- `~/.codex/config.toml.before-kivgraph-integration-20260904`
- Timestamped `atenea.atenea-backup.*` and desktop-helper backups under
  `~/.local/bin` and `~/.local/libexec` respectively.

## Verification

Atenea full Go suite, vet, targeted race suites, dashboard typecheck/lint/tests
and build, provider matrix and retirement guard passed. Adapter tests exercise
actual shipped output contracts, invalid batches/bounds, missing inventories,
failed-rebuild loop prevention, concurrent rebuild sharing and wait cancellation.

Kivgraph targeted Go tests, native indexing/rebuild/MCP tests, vet and full bundle
checksums passed. The running-tests skill guided native test selection and bundle
validation. Freshness package race coverage is 81.6%.

The full Kivgraph Go suite is **not green** on this host:
`TestClaudeDesktopIsDetectedByItsOwnEntry` fails because `/Applications/Claude.app`
is installed. The unchanged integration detector sees that global installation
despite the test's temporary home. The isolated test reproduces the failure.

`node scripts/kivgraph-integration-smoke.mjs --queries` exercises the installed
service, protocol initialization, tool listing and real graph queries. One
receipt per adapter invocation is asserted. The script uses synthetic client
names for five bridges: it does not prove every third-party GUI has reconnected.
Generated Codex, Claude and OpenCode overlays route through Atenea without a
parallel direct graph connection. OMP remains the existing pass-through wrapper.

Final live run: `--queries --repair` passed against generation 75, including
maintenance through MCP and both backend and app sources. The viewer returned
HTTP 200. Repair also clears a failed-attempt marker when an explicit freshness
check verifies that transient changes have already settled, without rebuilding.

Real queries distinguish the schema's inferred `UserType` from actual role codes.
The source check targets app `UserTypeCode`, with an independent local file
comparison. These are code-level facts, not a live database enumeration.
Empty reference/consumer results marked LOWER_BOUND do not prove absence.

The strict gate was also observed withholding results after a source change
during publication. Initial index failure exposed a missing Go PATH; a second
check exposed persisted query health after repair. Both recovery paths were fixed.
The Dart smoke also caught an upstream optimization: non-empty complete symbol
pages may omit completeness. Such rows are now accepted without manufacturing
a COMPLETE claim; empty or paginated pages still require the verdict.

## Remaining delivery boundaries

At initial delivery, the clean-tree readiness gate awaited an authorized commit.
The integration PR records subsequent validation and publication status.
Existing local changes were preserved. The failed Kivgraph environmental test
must be resolved or explicitly accounted for before declaring a fully green release.
Reconnect existing MCP sessions to receive new schemas and instructions.
Chat notices are client/model behavior; receipts and instructions are verified
on the wire, not guaranteed rendering in every UI. Maintenance start/wait/end
messages are service logs, not a new streaming MCP progress protocol.
Other client-imposed deadlines can still be shorter than Atenea's deadline.

OpenAI Docs guided the Codex deadline adjustment; its documented per-server
setting is `tool_timeout_sec`:
[official MCP configuration](https://learn.chatgpt.com/docs/extend/mcp?surface=cli).
