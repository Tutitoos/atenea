package toolstats

import (
	"context"
	"database/sql"
	"time"
)

// eventFilter applies dimensions in SQLite before aggregation or percentile sorting.
const eventFilter = `at>=? AND at<=? AND (?='' OR repository=?) AND (?='' OR provider=?) AND (?='' OR instr(tool,?)>0)`

// eventArgs binds the shared inclusive time interval and dimension predicates.
func eventArgs(q Query) []any {
	return []any{q.Since.UnixMicro(), q.Until.UnixMicro(), q.Repository, q.Repository, q.Provider, q.Provider, q.Tool, q.Tool}
}

// readEvents returns only grouped counters, exact percentiles, and five diagnostics.
// SQLite sorts on disk when necessary; no per-event duration slices enter Go memory.
func readEvents(ctx context.Context, tx *sql.Tx, q Query, grouped map[string]*Row, out *Snapshot) (map[string]*int64, error) {
	args := append([]any{q.Until.UnixMicro()}, eventArgs(q)...)
	prefix := `WITH selected AS (SELECT *,ended IS NOT NULL AND ended<=? AS complete FROM events WHERE ` + eventFilter + `) `
	// The completion timestamp precedes the filter's positional arguments.

	rows, err := tx.QueryContext(ctx, prefix+`SELECT level,tool,provider,
 sum(complete),sum(complete AND outcome='ok'),sum(complete AND outcome='refused'),
 sum(complete AND outcome NOT IN ('ok','refused','cancel')),sum(complete AND outcome='cancel'),sum(NOT complete),
 coalesce(sum(CASE WHEN complete AND outcome!='cancel' AND duration>=0 THEN duration END),0),
 sum(complete AND outcome!='cancel' AND duration>=0),max(CASE WHEN complete AND outcome!='cancel' AND duration>=0 THEN duration END),max(at),max(complete AND duration<0)
 FROM selected GROUP BY level,tool,provider`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		r := Row{Tool: Tool{State: "observed", Repository: q.Repository}}
		var last int64
		if err = rows.Scan(&r.Level, &r.Name, &r.Provider, &r.Calls, &r.OK, &r.Refused, &r.Fail, &r.Cancel, &r.Active, &r.SumUS, &r.Samples, &r.MaxUS, &last, &r.Summarized); err != nil {
			_ = rows.Close()
			return nil, err
		}
		stamp := time.UnixMicro(last)
		r.Last = &stamp
		grouped[key(r.Tool)] = &r
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	// Separate partitions compute row and level totals without averaging percentiles.
	rows, err = tx.QueryContext(ctx, prefix+`, samples AS (
 SELECT level,tool,provider,duration FROM selected WHERE complete AND outcome!='cancel' AND duration>=0
 ), ranked AS (
 SELECT level,tool,provider,duration,row_number() OVER(PARTITION BY level,tool,provider ORDER BY duration) AS pos,
 count(*) OVER(PARTITION BY level,tool,provider) AS n FROM samples
 ), totals AS (
 SELECT level,duration,row_number() OVER(PARTITION BY level ORDER BY duration) AS pos,
 count(*) OVER(PARTITION BY level) AS n FROM samples
 ) SELECT level,tool,provider,duration,0 FROM ranked WHERE pos=(95*n+99)/100
 UNION ALL SELECT level,'','',duration,1 FROM totals WHERE pos=(95*n+99)/100`, args...)
	if err != nil {
		return nil, err
	}
	percentiles := map[string]*int64{}
	for rows.Next() {
		var t Tool
		var v int64
		var isTotal bool
		if err = rows.Scan(&t.Level, &t.Name, &t.Provider, &v, &isTotal); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if isTotal {
			percentiles[t.Level] = &v
		} else {
			grouped[key(t)].P95US = &v
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, prefix+`, diagnostics AS (
 SELECT *,row_number() OVER(PARTITION BY CASE WHEN parent='' THEN id ELSE parent END,code ORDER BY at DESC,id) AS pos
 FROM selected WHERE complete AND outcome IN ('fail','refused')
 ) SELECT at,tool,provider,code,reason FROM diagnostics WHERE pos=1 ORDER BY at DESC,id LIMIT 5`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d Diagnostic
		var at int64
		if err = rows.Scan(&at, &d.Tool, &d.Provider, &d.Code, &d.Reason); err != nil {
			_ = rows.Close()
			return nil, err
		}
		d.At = time.UnixMicro(at)
		d.Code = Clean(d.Code, 80)
		d.Reason = Clean(d.Reason, 240)
		out.Errors = append(out.Errors, d)
	}
	err = rows.Err()
	_ = rows.Close()
	return percentiles, err
}
