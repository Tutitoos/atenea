# Audit remediation — September 2026

## 01: Diagnostics and dashboard (B04, A07)

New diagnostic secrets are redacted before persistence; old stats diagnostics are redacted on read without rewriting stored history. Dashboard hosts must be local or match the configured listener IP. Forwarded host headers do not grant access.

### Historical sanitization procedure (operator action)

Stop writers, take a verified backup, and work on a restored copy first. Inventory only diagnostic columns (never print values). Apply the shared RedactRaw function to those columns transactionally, retaining IDs, counters, codes and timestamps. Preview counts of changed records, compare all non-diagnostic fields, and run integrity checks. Replace production stores only after explicit approval and keep the original backup private. Reading through the corrected application is protected immediately; direct database access and old backups may still contain old diagnostics. No automatic historical migration is performed by this change.
