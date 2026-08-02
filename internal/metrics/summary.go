package metrics

import (
	"context"
	"fmt"
	"time"
)

// Row is what the store knows about one capability answered by one
// implementation on one repository, at one version of the tool.
//
// The key includes the tool version because that is the point of recording it:
// yesterday's numbers for yesterday's binary are history, not a baseline.
type Row struct {
	Capability     string
	Implementation string
	Provider       string
	Repository     string
	ToolVersion    string

	Attempts int64
	Failures int64
	// Successes is how many attempts worked, and the only count Mean divides
	// by. It is printed beside the other two because the gap between them is
	// the whole diagnosis: a provider with attempts and no successes has a
	// long record and no price at all.
	Successes int64
	// Mean is the average SUCCESSFUL call -- the figure the funnel ranks on.
	// Failures are counted above and priced nowhere: a tool that refuses
	// instantly is not the cheapest thing on the machine.
	Mean time.Duration
	// Slowest is the worst single call seen, successful or not. This one does
	// span the failures on purpose: a provider that hangs for thirty seconds
	// before giving up has still cost somebody thirty seconds, and that is
	// worth seeing even though it is not a price.
	Slowest time.Duration
	Tokens  int64
	// PeakRSS is the largest the far side ever grew, in bytes. Zero with
	// RSSSamples at zero means nobody could weigh it.
	PeakRSS    int64
	RSSSamples int64
}

// summarize reads the attempt table and the rollups together.
//
// A folded attempt is already counted in its hour, so the fine half of the
// union takes only the unfolded ones. Without that the week where both exist
// would count every recent call twice.
const summarize = `WITH parts AS (
	SELECT capability, implementation, any_value(provider) AS provider, repository, tool_version,
	       count(*) AS attempts, count(*) FILTER (WHERE NOT ok) AS failures,
	       count(*) FILTER (WHERE ok) AS wins,
	       coalesce(sum(duration_us) FILTER (WHERE ok), 0) AS dsum,
	       max(duration_us) AS dmax,
	       coalesce(sum(tokens) FILTER (WHERE ok), 0) AS tsum,
	       max(peak_rss_bytes) AS rmax, count(peak_rss_bytes) AS rn
	FROM measurement
	WHERE NOT folded AND happened_at >= ?
	GROUP BY 1, 2, 4, 5
	UNION ALL
	SELECT capability, implementation, any_value(provider), repository, tool_version,
	       sum(attempts), sum(failures), sum(ok_attempts),
	       sum(ok_duration_us_sum), max(duration_us_max),
	       sum(ok_tokens_sum), max(peak_rss_max), sum(rss_samples)
	FROM rollup
	WHERE bucket >= ?
	GROUP BY 1, 2, 4, 5
)
SELECT capability, implementation, any_value(provider), repository, tool_version,
       sum(attempts), sum(failures), sum(wins), sum(dsum), max(dmax), sum(tsum),
       max(rmax), sum(rn)
FROM parts
GROUP BY 1, 2, 4, 5
ORDER BY 1, 2, 4, 5`

// Summary reports everything measured at or after since, newest grain and
// oldest rollup folded into one answer.
//
// It flushes first. Reading a store while its own batch is still in memory
// would report a baseline that is stale by exactly the calls the caller most
// likely just made.
func (s *Store) Summary(ctx context.Context, since time.Time) ([]Row, error) {
	if err := s.Flush(ctx); err != nil {
		return nil, err
	}
	db, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	cut := since.UTC()
	rows, err := db.QueryContext(ctx, summarize, cut, cut)
	if err != nil {
		return nil, fmt.Errorf("metrics: summary: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Row
	for rows.Next() {
		var r Row
		var dsum, dmax int64
		var rmax *int64
		if err := rows.Scan(&r.Capability, &r.Implementation, &r.Provider,
			&r.Repository, &r.ToolVersion, &r.Attempts, &r.Failures, &r.Successes,
			&dsum, &dmax, &r.Tokens, &rmax, &r.RSSSamples); err != nil {
			return nil, fmt.Errorf("metrics: summary: %w", err)
		}
		if r.Successes > 0 {
			r.Mean = time.Duration(dsum/r.Successes) * time.Microsecond
		}
		r.Slowest = time.Duration(dmax) * time.Microsecond
		if rmax != nil {
			r.PeakRSS = *rmax
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metrics: summary: %w", err)
	}
	return out, nil
}
