---
title: "Tool activity: atenea stats"
weight: 36
---

`atenea stats` shows recorded activity in colored ASCII tables. It does not probe
providers, execute tools, start MCP backends, or affect the selector's cost base.
`atenea metrics` retains its existing behavior.

```sh
atenea stats
atenea stats --today
atenea stats --week --provider kivgraph
atenea stats --month --repo current
atenea stats --since 2h --tool search --used
atenea stats --since 2026-09-01T00:00:00+02:00 --json
atenea stats --today --used --watch
```

## Periods and output

No period flag means all retained history. `--today` starts at local midnight,
`--week` at Monday midnight, and `--month` at midnight on the first day of the
current month. These are calendar periods, not trailing 24-hour, seven-day or
30-day windows. `--since` accepts a positive Go duration or an RFC3339 timestamp.
Choose exactly one period flag. The header prints both boundaries and the local
time zone. Calls belong to the period in which they started.

`--watch` refreshes every two seconds and recalculates calendar boundaries, so it
continues into the next day, week or month. It requires a terminal; Ctrl+C restores
the cursor and original screen. Use `--used` or provider/tool filters to keep a
large catalog manageable. `--watch` and `--json` cannot be combined.

`--repo`, `--provider`, and `--tool` filter repository ID, provider ID and a tool
name substring respectively. Provider IDs on requests identify the entry point
(`atenea` for capabilities, the server ID for raw MCP); attempt providers identify
the implementation that was invoked. `--used` hides rows without completed or
active calls in the selected period. `--json` returns the same snapshot as the
local `atenea/stats` socket method (version 1), without ANSI escapes.

Colors are automatic only on terminals. `--color=always|never|auto` overrides that
choice; the presence of `NO_COLOR` disables colors even with `always`.

## Reading the tables

Requests and implementation attempts have separate tables and totals. A request
may have zero attempts (rejected at the door), one attempt, or multiple attempts.
They must not be added together. Raw tool aliases normalize to `raw.server.tool`.
Only traffic that passes through Atenea can be observed.

| Column | Meaning |
| --- | --- |
| CALLS | Completed events in this table's level |
| OK | Successful events |
| REFUSED | Structured permission or policy denials |
| FAIL | Other failures, including invalid input, unavailable providers and timeouts |
| CANCEL | Canceled events, separate from provider failures |
| OK% | OK / CALLS, including refusals and cancellations in the denominator |
| MEAN, P95, MAX | Wall time of completed, non-canceled events |
| LAST | Most recent start time in the selected period |

In-flight events appear separately and do not enter CALLS or success rates.
`SIN USO` means no observed activity in the selected period, not healthy. A dash
means no complete measurement. Green denotes successes, yellow refusals, red
failures and gray unused tools. Long tables move timing columns below the rows
on narrow terminals. Recent diagnostics include a bounded, control-stripped
reason; request arguments and responses are not copied into the stats store.

The catalog includes declared capabilities and implementations, and raw tools
remembered during ordinary MCP discovery. A raw catalog not yet discovered is
explicitly incomplete. Disconnected operation uses persisted data and labels
service information as stored; unclosed events in stored data are not proof that
the original process is still running. Read errors are reported instead of
rendering an empty success screen.

## Storage and historical limits

Stats use a separate SQLite database at `<metrics.path>.stats.sqlite`; when
`metrics.path` is unset, the path is beside the default DuckDB measurement base.
The store is lazy: reading a nonexistent stats database does not create one.
Recording is independent of whether routing measurements are enabled.

The first recording/discovery write establishes a persistent coverage start.
Older DuckDB measurements are shown in a separate legacy block up to that cutoff.
They are implementation attempts, not recoverable request counts. Their failure
count cannot reliably distinguish refusals from other failures. Legacy MEAN OK
uses successful attempts, matching the original metrics command.

Detailed events survive at least seven days. On subsequent recording activity,
whole UTC days older than that are transactionally compacted into persistent
summaries. Counts by outcome, timing sums, maxima and last activity survive;
compaction is idempotent. Reading stats never runs compaction.

P95 is exact only where all matching detail remains. It is unavailable for rows
containing summarized history. If a summary straddles a period boundary, the
whole overlapping bucket is omitted and its interval is reported as partial
coverage rather than attributed to the wrong period. The same rule applies to
legacy summaries and the coverage cutoff. Old raw calls and early refusals that
were never recorded cannot be reconstructed. Failed recording operations are
counted and reported when storage becomes accessible again.

Installing a new CLI alone does not instrument an already running old service.
Install the matching binary and restart the service explicitly to enable its new
recording; until then the CLI reports unavailable live stats and any saved data.
