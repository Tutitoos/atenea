package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Baseline is what one implementation costs on one repository, right now.
//
// It is a different question from Summary, which reports everything the store
// ever saw, per version, for a human to read. This one answers the funnel:
// "what does this cost here, at the version that is running today". So it
// carries averages per call rather than totals, and it never mixes versions.
type Baseline struct {
	// Spent is the average call: total over attempts, on each axis.
	Spent contract.Sample
	// Attempts is how many measurements back Spent. Failed attempts count --
	// a tool that gives up quickly is not fast, and one that hangs before
	// failing has still eaten the wait.
	Attempts int
	// ToolVersion is the version those attempts belong to.
	ToolVersion string
}

// costs reads the current figures for one capability on one repository.
//
// # Only the version running now
//
// An upgraded tool starts a fresh baseline rather than averaging into the old
// binary's numbers: otherwise the slow numbers of the version before drag the
// average down and the funnel takes weeks to notice the improvement. "Current"
// is the version of the most recent attempt, because that is the one the
// machine actually has installed -- version strings are vendor prose and
// cannot be ordered, but timestamps can.
//
// The consequence is deliberate: an upgrade puts that implementation back into
// break-in, and it earns its numbers again.
const costs = `WITH parts AS (
	SELECT implementation, tool_version,
	       count(*) AS attempts, sum(duration_us) AS dsum, sum(tokens) AS tsum,
	       max(peak_rss_bytes) AS rmax, max(happened_at) AS last_seen
	FROM measurement
	WHERE NOT folded AND capability = ? AND repository = ?
	GROUP BY 1, 2
	UNION ALL
	SELECT implementation, tool_version,
	       sum(attempts), sum(duration_us_sum), sum(tokens_sum),
	       max(peak_rss_max), max(bucket)
	FROM rollup
	WHERE capability = ? AND repository = ?
	GROUP BY 1, 2
), merged AS (
	SELECT implementation, tool_version, sum(attempts) AS attempts,
	       sum(dsum) AS dsum, sum(tsum) AS tsum, max(rmax) AS rmax,
	       max(last_seen) AS last_seen
	FROM parts
	GROUP BY 1, 2
)
SELECT implementation, tool_version, attempts, dsum, tsum, rmax
FROM merged
QUALIFY row_number() OVER (
	PARTITION BY implementation ORDER BY last_seen DESC, tool_version DESC
) = 1`

// Costs reports what each implementation of a capability costs on one
// repository, keyed by implementation id.
//
// Cost is asked per repository because it is not a property of the tool: the
// same provider is cheap with a warm index and expensive without one. An
// implementation absent from the answer has never been measured here, which is
// not the same as being free.
func (s *Store) Costs(ctx context.Context, capability, repository string) (map[string]Baseline, error) {
	// Flushing first matters more here than anywhere else: a funnel reading a
	// store with its own batch still in memory would rank on a baseline stale
	// by exactly the calls that just ran.
	if err := s.Flush(ctx); err != nil {
		return nil, err
	}
	db, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, costs, capability, repository, capability, repository)
	if err != nil {
		return nil, fmt.Errorf("metrics: costs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]Baseline)
	for rows.Next() {
		var id string
		var b Baseline
		var attempts, dsum, tsum int64
		var rmax *int64
		if err := rows.Scan(&id, &b.ToolVersion, &attempts, &dsum, &tsum, &rmax); err != nil {
			return nil, fmt.Errorf("metrics: costs: %w", err)
		}
		if attempts <= 0 {
			// A key with no attempts behind it is arithmetic waiting to
			// divide by zero, and a row nobody measured has nothing to say.
			continue
		}
		b.Attempts = int(attempts)
		b.Spent = contract.Sample{
			Duration: time.Duration(dsum/attempts) * time.Microsecond,
			Tokens:   int(tsum / attempts),
		}
		if rmax != nil {
			b.Spent.PeakRSS = *rmax
		}
		out[id] = b
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metrics: costs: %w", err)
	}
	return out, nil
}

// Apply fills the measured half of each candidate's cost from the base.
//
// The declared estimate is left exactly as it was written. That is the hybrid:
// an implementation the base has never seen here keeps ranking on its guess,
// and one it has seen enough of ranks on what actually happened.
//
// Candidates are filled in place. They are the caller's own copies out of the
// catalog -- which stays declarative, and never learns a number from a run.
func Apply(base map[string]Baseline, candidates []contract.Implementation) {
	for i := range candidates {
		b, ok := base[candidates[i].ID]
		if !ok {
			continue
		}
		candidates[i].Cost.Measured = b.Spent
		candidates[i].Cost.Samples = b.Attempts
		candidates[i].Cost.ToolVersion = b.ToolVersion
	}
}
