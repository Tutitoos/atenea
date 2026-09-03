package metrics

import (
	"context"
	"time"
)

// SeriesPoint is a measured time bucket. It deliberately carries coverage
// rather than treating missing rows as zero; old rollups do not retain the
// per-attempt distribution needed to recreate a precise series.
type SeriesPoint struct {
	Bucket         time.Time `json:"bucket"`
	Capability     string    `json:"capability"`
	Implementation string    `json:"implementation"`
	Provider       string    `json:"provider"`
	Repository     string    `json:"repository"`
	Attempts       int64     `json:"attempts"`
	Successes      int64     `json:"successes"`
	Failures       int64     `json:"failures"`
	Tokens         int64     `json:"tokens"`
	DurationMS     int64     `json:"duration_ms"`
	MeasuredRows   int64     `json:"measured_rows"`
	OutOfScope     int64     `json:"out_of_scope"`
}

// Series returns exact attempt buckets still retained by DuckDB. It is a
// read-only companion to Summary and is safe to call while another Atenea is
// flushing because the store's normal lock/retry path is used.
func (s *Store) Series(ctx context.Context, since time.Time, bucket time.Duration) ([]SeriesPoint, error) {
	if bucket <= 0 {
		bucket = 5 * time.Minute
	}
	if err := s.Flush(ctx); err != nil {
		return nil, err
	}
	db, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT happened_at, capability, implementation, provider, repository, duration_us, tokens, ok, out_of_scope FROM measurement WHERE happened_at >= ? ORDER BY happened_at ASC`, since.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type key struct {
		bucket                           time.Time
		capability, impl, provider, repo string
	}
	grouped := make(map[key]*SeriesPoint)
	for rows.Next() {
		var at time.Time
		var capability, impl, provider, repo string
		var durationUS, tokens, wandered int64
		var ok bool
		if err := rows.Scan(&at, &capability, &impl, &provider, &repo, &durationUS, &tokens, &ok, &wandered); err != nil {
			return nil, err
		}
		stamp := at.UTC().Truncate(bucket)
		k := key{stamp, capability, impl, provider, repo}
		p := grouped[k]
		if p == nil {
			p = &SeriesPoint{Bucket: stamp, Capability: capability, Implementation: impl, Provider: provider, Repository: repo}
			grouped[k] = p
		}
		p.Attempts++
		if ok {
			p.Successes++
			p.DurationMS += durationUS / 1000
		} else {
			p.Failures++
		}
		p.Tokens += tokens
		p.MeasuredRows++
		p.OutOfScope += wandered
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SeriesPoint, 0, len(grouped))
	for _, point := range grouped {
		out = append(out, *point)
	}
	// Stable order without importing a second sorting policy into callers.
	sortSeries(out)
	return out, nil
}

func sortSeries(points []SeriesPoint) {
	for i := 1; i < len(points); i++ {
		v := points[i]
		j := i - 1
		for j >= 0 && seriesBefore(v, points[j]) {
			points[j+1] = points[j]
			j--
		}
		points[j+1] = v
	}
}

func seriesBefore(a, b SeriesPoint) bool {
	if !a.Bucket.Equal(b.Bucket) {
		return a.Bucket.Before(b.Bucket)
	}
	if a.Capability != b.Capability {
		return a.Capability < b.Capability
	}
	if a.Implementation != b.Implementation {
		return a.Implementation < b.Implementation
	}
	if a.Provider != b.Provider {
		return a.Provider < b.Provider
	}
	return a.Repository < b.Repository
}
