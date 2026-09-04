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

Explicit local scopes resolve symlink components before traversal and file reads use an OS-rooted handle. Extract and crawl reject prohibited returned destinations without returning content. Crawl also rejects pages outside the seed hostname. The helper distinguishes JSON parse errors from syntactically valid invalid requests, validates IDs and object-valued params before dispatch, and keeps the protocol loop alive after either error.

R05 remains an external network-isolation limitation: allowed_domains filters discovery but is not a DNS/redirect/browser-network sandbox. The output gate cannot undo requests already made by Scrapling. Tests use controlled resolver/session responses; upstream browser subresources are not certified. Use a network-isolated provider environment when pre-connection denial is required.

## 05: MCP lifecycle and resource bounds (A01–A05, B10, R02)

A session becomes ready only after initialized completes, independently of whether the server uses session IDs. Handshake waiters honor their contexts. Subsequent HTTP messages carry the supported protocol revision. SSE returns on the matching response event, with an 8 MiB per-message limit; JSON responses have the same limit.

Stdio write admission is cancelable. Cancellation during a write closes the damaged session; cancellation while waiting for another writer does not. The supervisor retains process ownership, and effectful calls are not automatically retried. Process diagnostic/output capture is bounded at 8 MiB per stream and stops overflowing children.

## 06: Historical precision and backups (A06, R01)

Rollups retain a completion upper bound in an additive table. A period cutting that bound excludes the affected bucket and reports partial coverage; old summaries receive a conservative migration-time bound, and read-only older stores remain readable with unknown bounds explicitly omitted. Repeated compaction is idempotent.

SQLite backups use VACUUM INTO and integrity_check, excluding WAL/SHM sidecars. DuckDB uses COPY FROM DATABASE into a fresh attached database after the caller settles and closes its metrics writer. An active DuckDB WAL makes backup fail explicitly for later retry: opening a second engine instance against a live in-process WAL produced a stale snapshot in the isolated test. Database snapshots never reuse hardlinks based on main-file mtime. Consistency is per database, not an atomic transaction across stores. Publication and rotation remain after successful copies and fsync.
