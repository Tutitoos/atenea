package toolstats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Validate rejects invalid statistics query intervals.
func (q Query) Validate() error {
	if !q.Until.IsZero() && q.Since.After(q.Until) {
		return fmt.Errorf("stats: since is after until")
	}
	return nil
}

// matches applies exact repository/provider and substring tool filters.
func (q Query) matches(tool, provider, repo string) bool {
	return (q.Tool == "" || strings.Contains(tool, q.Tool)) && (q.Provider == "" || q.Provider == provider) && (q.Repository == "" || q.Repository == repo)
}

// key identifies one provider tool at a single accounting level.
func key(t Tool) string { return t.Level + "\x00" + t.Provider + "\x00" + t.Name }

// Read uses a read-only connection and a consistent snapshot. It never performs maintenance.
func (s *Store) Read(ctx context.Context, q Query, catalog []Tool) (Snapshot, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		out, err := s.readOnce(ctx, q, catalog)
		if err == nil {
			for i := range out.Errors {
				out.Errors[i].Reason = Clean(contract.RedactRaw(out.Errors[i].Reason), 240)
			}
			for i := range out.Coverage.Notes {
				out.Coverage.Notes[i] = contract.RedactRaw(out.Coverage.Notes[i])
			}
			return out, nil
		}
		transient := transientRead(err)
		if !transient || time.Now().After(deadline) {
			return out, err
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// readOnce queries a consistent read-only snapshot and annotates coverage limitations.
func (s *Store) readOnce(ctx context.Context, q Query, catalog []Tool) (Snapshot, error) {
	if message, _ := s.writeError.Load().(string); message != "" {
		return Snapshot{}, fmt.Errorf("stats recording unavailable: %s", message)
	}
	now := time.Now()
	if q.Until.IsZero() {
		q.Until = now
	}
	if err := q.Validate(); err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{Version: 1, At: now, Query: q, Source: "disk", Service: "stored", Catalog: catalog, Rows: []Row{}, Totals: []Row{}, Errors: []Diagnostic{}, Legacy: []LegacyRow{}, Coverage: Coverage{DetailSince: now.UTC().AddDate(0, 0, -7).Truncate(24 * time.Hour), Notes: []string{}}}
	grouped := map[string]*Row{}
	rowFor := func(t Tool) *Row {
		k := key(t)
		if r := grouped[k]; r != nil {
			return r
		}
		r := &Row{Tool: t}
		grouped[k] = r
		return r
	}
	seed := func() {
		unique := make([]Tool, 0, len(out.Catalog))
		seen := map[string]bool{}
		for _, t := range out.Catalog {
			if !seen[key(t)] {
				seen[key(t)] = true
				unique = append(unique, t)
			}
		}
		out.Catalog = unique
		for _, t := range out.Catalog {
			if t.Level != "catalog" && q.matches(t.Name, t.Provider, q.Repository) {
				rowFor(t)
			}
		}
	}
	deferFinalize := func() { seed(); finalize(&out, grouped) }
	if _, err := os.Stat(s.Path); os.IsNotExist(err) {
		out.Coverage.Notes = append(out.Coverage.Notes, "New activity recording has not started.")
		deferFinalize()
		return out, nil
	} else if err != nil {
		return out, err
	}
	db, err := sql.Open("sqlite", dsn(s.Path, "ro"))
	if err != nil {
		return out, err
	}
	defer func() { _ = db.Close() }()
	dead, release, err := deadOwners(ctx, db, s.Path)
	defer release()
	if err != nil {
		return out, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	var started int64
	if err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='started'`).Scan(&started); err != nil {
		return out, err
	}
	at := time.UnixMicro(started)
	out.Coverage.Started = &at
	var dropped int64
	err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='dropped'`).Scan(&dropped)
	if err != nil && err != sql.ErrNoRows {
		return out, err
	}
	out.Coverage.Dropped = dropped + s.dropped.Load()
	names, err := tx.QueryContext(ctx, `SELECT provider,tool FROM catalogs ORDER BY provider,tool`)
	if err != nil {
		return out, err
	}
	for names.Next() {
		var p, n string
		if err = names.Scan(&p, &n); err != nil {
			_ = names.Close()
			return out, err
		}
		out.Catalog = append(out.Catalog, Tool{Level: "request", Name: n, Provider: p, State: "stored"}, Tool{Level: "attempt", Name: n, Provider: p, State: "stored"})
	}
	err = names.Err()
	_ = names.Close()
	if err != nil {
		return out, err
	}
	discovered, err := tx.QueryContext(ctx, `SELECT provider FROM discovered`)
	if err != nil {
		return out, err
	}
	known := map[string]bool{}
	for discovered.Next() {
		var p string
		if err = discovered.Scan(&p); err != nil {
			_ = discovered.Close()
			return out, err
		}
		known[p] = true
	}
	err = discovered.Err()
	_ = discovered.Close()
	if err != nil {
		return out, err
	}
	for i := range out.Catalog {
		if out.Catalog[i].State == "catalog_unknown" && known[out.Catalog[i].Provider] {
			out.Catalog[i].State = "catalog_saved"
		}
	}
	percentiles, err := readEvents(ctx, tx, q, grouped, &out, dead)
	if err != nil {
		return out, err
	}
	var hasBounds int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='rollup_bounds'`).Scan(&hasBounds); err != nil {
		return out, err
	}
	boundsColumn, boundsJoin := "NULL", ""
	if hasBounds > 0 {
		boundsColumn = "max_ended"
		boundsJoin = " LEFT JOIN rollup_bounds USING(bucket,level,tool,provider,repository)"
	}
	folded, err := tx.QueryContext(ctx, `SELECT bucket,level,tool,provider,repository,calls,ok,refused,fail,cancel,dsum,samples,dmax,last,`+boundsColumn+` FROM rollups`+boundsJoin+` WHERE bucket+86400000000>? AND bucket<=?`, q.Since.UnixMicro(), q.Until.UnixMicro())
	if err != nil {
		return out, err
	}
	for folded.Next() {
		var r Row
		var bucket, last, maxDuration int64
		var maxEnded sql.NullInt64
		if err = folded.Scan(&bucket, &r.Level, &r.Name, &r.Provider, &r.Repository, &r.Calls, &r.OK, &r.Refused, &r.Fail, &r.Cancel, &r.SumUS, &r.Samples, &maxDuration, &last, &maxEnded); err != nil {
			_ = folded.Close()
			return out, err
		}
		if !q.matches(r.Name, r.Provider, r.Repository) {
			continue
		}
		if bucket < q.Since.UnixMicro() || bucket+86400000000 > q.Until.UnixMicro() || !maxEnded.Valid || maxEnded.Int64 > q.Until.UnixMicro() {
			out.Coverage.Partial = true
			rowFor(r.Tool).Summarized = true
			out.Coverage.Notes = append(out.Coverage.Notes, fmt.Sprintf("Omitted summarized interval %s to %s: boundary detail or completion bound unavailable.", time.UnixMicro(bucket).UTC().Format(time.RFC3339), time.UnixMicro(bucket+86400000000).UTC().Format(time.RFC3339)))
			continue
		}
		r.Summarized = true
		r.State = "historical"
		stamp := time.UnixMicro(last)
		r.Last = &stamp
		if r.Samples > 0 {
			r.MaxUS = &maxDuration
		}
		add(rowFor(r.Tool), r)
	}
	err = folded.Err()
	_ = folded.Close()
	if err != nil {
		return out, err
	}
	if q.Since.Before(at) {
		out.Coverage.Notes = append(out.Coverage.Notes, "Requests and raw tools before recording started were not fully measured; older routing metrics are separate.")
	}
	if out.Coverage.Dropped > 0 {
		out.Coverage.Partial = true
		out.Coverage.Notes = append(out.Coverage.Notes, "Some recording operations failed; counts may be incomplete.")
	}
	for _, flag := range []string{"recovered", "maintenance_failed"} {
		var count int64
		err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, flag).Scan(&count)
		if err != nil && err != sql.ErrNoRows {
			return out, err
		}
		if count > 0 {
			if flag == "recovered" {
				out.Coverage.Partial = true
				out.Coverage.Notes = append(out.Coverage.Notes, fmt.Sprintf("%d recording interruptions recovered; execution outcomes and durations are unknown (FAIL, recording_interrupted).", count))
			} else {
				reason, _ := s.maintenance.Load().(string)
				if reason == "" {
					readErr := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='maintenance_reason'`).Scan(&reason)
					if readErr != nil && readErr != sql.ErrNoRows {
						return out, readErr
					}
				}
				out.Coverage.Notes = append(out.Coverage.Notes, "Statistics maintenance failed; recording continues and maintenance will retry after backoff. "+Clean(reason, 240))
			}
		}
	}
	deferFinalize()
	for i := range out.Totals {
		r := &out.Totals[i]
		if !r.Summarized {
			r.P95US = percentiles[r.Level]
		}
	}
	return out, nil
}

// add combines counters and timing sums without combining percentile estimates.
func add(dst *Row, src Row) {
	dst.Calls += src.Calls
	dst.OK += src.OK
	dst.Refused += src.Refused
	dst.Fail += src.Fail
	dst.Cancel += src.Cancel
	dst.Active += src.Active
	dst.SumUS += src.SumUS
	dst.Samples += src.Samples
	dst.Summarized = dst.Summarized || src.Summarized

	if src.MaxUS != nil && (dst.MaxUS == nil || *src.MaxUS > *dst.MaxUS) {
		v := *src.MaxUS
		dst.MaxUS = &v
	}
	if src.Last != nil && (dst.Last == nil || src.Last.After(*dst.Last)) {
		v := *src.Last
		dst.Last = &v
	}
}

// calculate derives percentages and means, suppressing incomplete percentiles.
func calculate(r *Row) {
	if r.Calls > 0 {
		v := float64(r.OK) * 100 / float64(r.Calls)
		r.OKPercent = &v
	}
	if r.Samples > 0 {
		v := r.SumUS / r.Samples
		r.MeanUS = &v
	}
	if r.Summarized {
		r.P95US = nil
	}

}

// finalize merges catalog rows, computes independent totals, and orders output.
func finalize(out *Snapshot, grouped map[string]*Row) {
	totals := map[string]*Row{"request": {Tool: Tool{Level: "request", Name: "TOTAL"}}, "attempt": {Tool: Tool{Level: "attempt", Name: "TOTAL"}}}
	for _, r := range grouped {
		if out.Coverage.Dropped > 0 {
			r.Summarized = true
		}
		if t := totals[r.Level]; t != nil && r.Summarized {
			t.Summarized = true
		}
		if out.Query.Used && r.Calls == 0 && r.Active == 0 {
			continue
		}
		calculate(r)
		out.Rows = append(out.Rows, *r)
		if t := totals[r.Level]; t != nil {
			add(t, *r)
		}
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Calls != b.Calls {
			return a.Calls > b.Calls
		}
		return a.Name < b.Name
	})
	for _, level := range []string{"request", "attempt"} {
		r := totals[level]
		calculate(r)
		out.Totals = append(out.Totals, *r)
		if r.Summarized {
			out.Coverage.Notes = append(out.Coverage.Notes, "P95 unavailable for "+level+" rows with summarized history or unknown durations.")
		}
	}
	seen := map[string]bool{}
	notes := out.Coverage.Notes[:0]
	for _, n := range out.Coverage.Notes {
		if !seen[n] {
			seen[n] = true
			notes = append(notes, n)
		}
	}
	out.Coverage.Notes = notes
}

// transientRead uses SQLite primary result codes, including extended lock codes.
func transientRead(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code() & 0xff
		if code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED {
			return true
		}
	}
	return strings.Contains(err.Error(), "no such table")
}
