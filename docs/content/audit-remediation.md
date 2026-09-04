# Audit remediation — September 2026

## 01: Diagnostics and dashboard (B04, A07)

New diagnostic secrets are redacted before persistence; old stats diagnostics are redacted on read without rewriting stored history. Dashboard hosts must be local or match the configured listener IP. Forwarded host headers do not grant access.

### Historical sanitization procedure (operator action)

Stop writers, take a verified backup, and work on a restored copy first. Inventory only diagnostic columns (never print values). Apply the shared RedactRaw function to those columns transactionally, retaining IDs, counters, codes and timestamps. Preview counts of changed records, compare all non-diagnostic fields, and run integrity checks. Replace production stores only after explicit approval and keep the original backup private. Reading through the corrected application is protected immediately; direct database access and old backups may still contain old diagnostics. No automatic historical migration is performed by this change.

## 02: Permissions (B01, B11, R03)

Workflow dispatch now carries the exact step effects, including an explicit empty grant. Direct callers retain the declared default when omitted. Shipped readers use directory-rooted bounded reads and enforce named task files. Custom executables remain trusted programs; these checks are not an OS sandbox.

## 03: Accounting (B02, B03, B06, B07, B08)

Claims reserve the whole requested step share in SQLite before dispatch. A retry that cannot fund that share is refused instead of silently increasing the grant or gambling on a smaller share. Reservations survive restart, include archived attempts, and retain unknown charges at their granted ceiling. Observed charges settle the hold; provider overspend remains visible rather than being clipped. The additive reservation table reconstructs older attempts from their existing trace IDs and grants.

Codex and OpenCode preserve reported usage on failure. OpenCode finalization uses only the remaining budget/tokens. Fallback subtracts cumulative spend from the original grant exactly once. A child killed before emitting any charge remains unknown; Atenea cannot reconstruct an unreported bill.

## 04: Files and web (B05, B09, B13, R05)

Explicit local scopes resolve symlink components before traversal and file reads use an OS-rooted handle. Extract and crawl reject prohibited returned destinations without returning content. Crawl also rejects pages outside the seed hostname. Malformed helper requests receive Parse Error or Invalid Request and do not terminate the protocol loop.

R05 remains an external network-isolation limitation: allowed_domains filters discovery but is not a DNS/redirect/browser-network sandbox. The output gate cannot undo requests already made by Scrapling. Tests use controlled resolver/session responses; upstream browser subresources are not certified. Use a network-isolated provider environment when pre-connection denial is required.

## 05: MCP lifecycle and resource bounds (A01–A05, B10, R02)

A session becomes ready only after initialized completes, independently of whether the server uses session IDs. Handshake waiters honor their contexts. Subsequent HTTP messages carry the supported protocol revision. SSE returns on the matching response event, with an 8 MiB per-message limit; JSON responses have the same limit.

Stdio write admission is cancelable. Cancellation during a write closes the damaged session; cancellation while waiting for another writer does not. The supervisor retains process ownership, and effectful calls are not automatically retried. Process diagnostic/output capture is bounded at 8 MiB per stream and stops overflowing children.

## 06: Historical precision and backups (A06, R01)

Rollups retain a completion upper bound in an additive table. A period cutting that bound excludes the affected bucket and reports partial coverage; old summaries receive a conservative migration-time bound, and read-only older stores remain readable with unknown bounds explicitly omitted. Repeated compaction is idempotent.

SQLite backups use VACUUM INTO and integrity_check, excluding WAL/SHM sidecars. DuckDB uses COPY FROM DATABASE into a fresh attached database after the caller settles and closes its metrics writer. An active DuckDB WAL makes backup fail explicitly for later retry: opening a second engine instance against a live in-process WAL produced a stale snapshot in the isolated test. Database snapshots never reuse hardlinks based on main-file mtime. Consistency is per database, not an atomic transaction across stores. Publication and rotation remain after successful copies and fsync.

## 07: Provider contracts (B12, R04, R06)

Policy now agrees with the configured symbol.search implementation, guarded by a catalog/documentation regression. OpenCode finalization has explicit deny-all permissions and isolated configuration, with an incompatible-version refusal. See audit-provider-permissions.md for evidence, compatibility limits and the proposed claude-mem overrides; the active local configuration is unchanged. audit-provider-matrix.md distinguishes declaration checks from external operational certification. Legacy metrics diagnostics are also redacted when read, and stats redacts before truncating old JSON diagnostics.

## 08: Validation and sandbox design

agent-sandbox-design.md specifies the future Linux namespace/macOS VM boundaries, broker, profiles, fail-closed behavior, lifecycle and acceptance matrix. The runtime itself is deliberately deferred.

The two real OMP CLI integrations run through scripts/omp-integration-check.sh and a separate CI job pinned to OMP 18.0.11. The ordinary suite reports their explicit opt-in skips; the isolated gate must pass separately and is not a retry that hides a failed full run. Production timeouts are unchanged. The crawler gate includes malformed-frame regressions.

### Remaining operational actions

Install/restart only after review and explicit deployment authorization. Historical sanitization and claude-mem profile changes remain proposed operator actions. External MCP operations, model billing and real devices are not certified by this local validation. R03 sandbox implementation and R05 pre-connection network isolation are explicitly documented future boundaries.

## Local validation and closure

Validated on macOS arm64, September 4, 2026. The full Go suite passed with `-race -count=1` and atomic coverage (78.2% statements). The real OMP 18.0.11 gate passed separately. Frontend type checking, lint and all five tests passed; Swift strict concurrency compilation and all five tests passed. Govulncheck found no vulnerabilities. The declared provider matrix passed all 39 required edges and the Spider protocol gate passed. No live MCP probe was enabled.

Integration review added a regression for credentials embedded inside decoded JSON string values, including arrays; these are redacted before JSON serialization. The final push hook runs the complete race suite again, including this change.

| Audit scope | Delivery | Status |
| --- | --- | --- |
| A01–A05, B10 | PR 05 | Transport and output regressions covered locally |
| A06 | PR 06 | Historical boundary and repeated compaction tests pass |
| A07, B04 | PR 01, 07, 08 | Host rejection and new/old diagnostic redaction covered |
| B01, B11 | PR 02 | Exact effect grants and shipped reader scope covered |
| B02, B03, B06–B08 | PR 03 | Reservation, retry, fallback and reported failure cost covered |
| B05, B09, B13 | PR 04, 08 | File containment, returned web destinations and malformed protocol covered |
| B12 | PR 07 | Isolated deny-all finalization tested; actual installed config inspected |
| R01 | PR 06 | Engine snapshots tested; live DuckDB WAL explicitly refused |
| R02 | PR 05 | Bounded output and cancellation covered |
| R03 | PR 02, 08 | Current guards strengthened; OS sandbox designed, runtime deferred |
| R04 | PR 07 | Catalog/documentation agreement regression |
| R05 | PR 04, 08 | Output gate strengthened; pre-connection isolation remains external |
| R06 | PR 07 | Effects investigated and local override proposed, not applied |

These results certify the local contracts and controlled reproductions, not every external provider's current availability. Linux/macOS Intel CI remains a separate remote validation. Merge the eight dependent branches in order, updating each following PR base after its predecessor merges. Deployment and operational actions above remain separate.
