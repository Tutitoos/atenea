package toolstats

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"time"

	"github.com/Tutitoos/atenea/internal/dbaccess"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Boundary buckets can be reconstructed only while ALL their folded detail remains.
// Fully contained rollups and unfolded detail are disjoint; partial rollups use
// retained detail or report a coverage gap, never silently round the user's dates.
const legacyBuckets = `WITH bounds AS (SELECT ?::TIMESTAMP AS lo, ?::TIMESTAMP AS hi),
 buckets AS (
 SELECT r.*, bucket + CASE grain WHEN 'hour' THEN INTERVAL '1 hour'
 WHEN 'day' THEN INTERVAL '1 day' WHEN 'week' THEN INTERVAL '1 week' ELSE INTERVAL '1 month' END AS ends,
 (SELECT count(*) FROM measurement m WHERE m.folded AND m.capability=r.capability
 AND m.implementation=r.implementation AND m.repository=r.repository AND m.tool_version=r.tool_version
 AND m.happened_at>=r.bucket AND m.happened_at<ends) AS detail_count
 FROM rollup r, bounds WHERE ends>lo AND bucket<hi
 ) `

// Legacy adds pre-cutoff routing measurements to a separate historical block.
func Legacy(ctx context.Context, path string, out *Snapshot) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	releaseLocal, err := dbaccess.AcquireConnection(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = releaseLocal() }()
	releaseFile, err := dbaccess.Acquire(ctx, path, false)
	if err != nil {
		return err
	}
	defer func() { _ = releaseFile() }()
	db, err := sql.Open("duckdb", path+"?access_mode=read_only")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	until := out.Query.Until
	if out.Coverage.Started != nil && out.Coverage.Started.Before(until) {
		until = *out.Coverage.Started
	}
	if !out.Query.Since.Before(until) {
		return nil
	}
	query := legacyBuckets + `
 SELECT capability,implementation,provider,repository,count(*),count(*) FILTER(WHERE ok),coalesce(sum(duration_us) FILTER(WHERE ok),0),max(duration_us)
 FROM measurement,bounds WHERE NOT folded AND happened_at>=lo AND happened_at<hi GROUP BY 1,2,3,4
 UNION ALL SELECT capability,implementation,provider,repository,sum(attempts),sum(ok_attempts),sum(ok_duration_us_sum),max(duration_us_max)
 FROM buckets,bounds WHERE bucket>=lo AND ends<=hi GROUP BY 1,2,3,4
 UNION ALL SELECT m.capability,m.implementation,m.provider,m.repository,count(*),count(*) FILTER(WHERE m.ok),coalesce(sum(m.duration_us) FILTER(WHERE m.ok),0),max(m.duration_us)
 FROM measurement m JOIN buckets b ON m.capability=b.capability AND m.implementation=b.implementation
 AND m.repository=b.repository AND m.tool_version=b.tool_version AND m.happened_at>=b.bucket AND m.happened_at<b.ends,
 bounds WHERE m.folded AND m.happened_at>=lo AND m.happened_at<hi AND (b.bucket<lo OR b.ends>hi)
 AND b.detail_count=b.attempts GROUP BY 1,2,3,4`
	rows, err := db.QueryContext(ctx, query, out.Query.Since.UTC(), until.UTC())
	if err != nil {
		return err
	}
	type entry struct {
		row LegacyRow
		sum int64
	}
	grouped := map[string]*entry{}
	for rows.Next() {
		var r LegacyRow
		var capability string
		var sum, maxDuration int64
		if err = rows.Scan(&capability, &r.Tool, &r.Provider, &r.Repository, &r.Calls, &r.OK, &sum, &maxDuration); err != nil {
			_ = rows.Close()
			return err
		}
		if !out.Query.matches(r.Tool, r.Provider, r.Repository) && (out.Query.Tool == "" || !out.Query.matches(capability, r.Provider, r.Repository)) {
			continue
		}
		k := r.Provider + "\x00" + r.Tool + "\x00" + r.Repository
		e := grouped[k]
		if e == nil {
			e = &entry{row: LegacyRow{Tool: r.Tool, Provider: r.Provider, Repository: r.Repository}}
			grouped[k] = e
		}
		e.row.Calls += r.Calls
		e.row.OK += r.OK
		e.sum += sum
		if e.row.MaxUS == nil || maxDuration > *e.row.MaxUS {
			v := maxDuration
			e.row.MaxUS = &v
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, e := range grouped {
		e.row.Unclassified = e.row.Calls - e.row.OK
		if e.row.OK > 0 {
			v := e.sum / e.row.OK
			e.row.MeanUS = &v
		}
		out.Legacy = append(out.Legacy, e.row)
	}
	sort.Slice(out.Legacy, func(i, j int) bool {
		a, b := out.Legacy[i], out.Legacy[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Calls != b.Calls {
			return a.Calls > b.Calls
		}
		if a.Tool != b.Tool {
			return a.Tool < b.Tool
		}
		return a.Repository < b.Repository
	})
	rows, err = db.QueryContext(ctx, legacyBuckets+`SELECT bucket,ends,capability,implementation,provider,repository FROM buckets,bounds WHERE (bucket<lo OR ends>hi) AND detail_count!=attempts`, out.Query.Since.UTC(), until.UTC())
	if err != nil {
		return err
	}
	for rows.Next() {
		var start, end time.Time
		var capability, tool, provider, repo string
		if err = rows.Scan(&start, &end, &capability, &tool, &provider, &repo); err != nil {
			_ = rows.Close()
			return err
		}
		if out.Query.matches(tool, provider, repo) || (out.Query.Tool != "" && out.Query.matches(capability, provider, repo)) {
			out.Coverage.Partial = true
			out.Coverage.Notes = append(out.Coverage.Notes, "Omitted legacy interval "+start.UTC().Format(time.RFC3339)+" to "+end.UTC().Format(time.RFC3339)+": boundary detail unavailable.")
		}
	}
	err = rows.Err()
	_ = rows.Close()
	return err
}
