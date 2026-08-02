package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// FaultStreak is how many failures in a row make a provider unhealthy.
//
// Three rather than one because a single fault is ordinary -- a timeout, a
// racing index, a file that moved -- and rather than two because the funnel
// hands an unmeasured implementation the break-in turn twice before its
// numbers are believed, and a provider should not be condemned by the very
// calls that were buying its baseline.
const FaultStreak = 3

// FaultWindow is how long the newest failure of a streak keeps counting.
//
// Without it a provider condemned once is condemned forever: health drops it
// from the funnel, so nothing calls it, so the streak that damns it can never
// be broken by a success. The window is the way back. After it passes the
// streak lapses, the next call goes through, and one failure is enough to
// close it again -- the older failures are still on record, so recovery costs
// one call and relapse costs one call, not three either way.
const FaultWindow = 5 * time.Minute

// Fault is an unbroken run of failures at the newest end of the record: every
// attempt since the last successful one, or every attempt there has ever been
// when none succeeded.
//
// It is a fact, not a verdict. What it means for the funnel is decided by
// Health below, which is where the thresholds live.
type Fault struct {
	// Streak is how many attempts in a row failed.
	Streak int
	// Kind is the failure bin shared by all of them, and empty when they do
	// not share one. One bin repeated is an outage with a name; a scatter of
	// different bins is a provider in trouble without a diagnosis, and the two
	// deserve different answers.
	Kind string
	// Reason is the untranslated message of the newest failure, for whoever
	// has to read it.
	Reason string
	// Latest is when that newest failure happened.
	Latest time.Time
}

// Health reads a streak as a state the funnel can act on, or reports that
// there is nothing to say.
//
// A run of the same bin is an outage: named, diagnosable, and worth leaving
// the funnel over. A run of mixed bins is a provider that keeps breaking
// differently every time, which is a real finding but not a single fault, so
// it ranks below the healthy ones instead of being dropped.
func (f Fault) Health(now time.Time) (contract.Health, bool) {
	if f.Streak < FaultStreak || now.Sub(f.Latest) > FaultWindow {
		return contract.Health{}, false
	}
	h := contract.Health{ObservedAt: f.Latest}
	if f.Kind != "" {
		h.State = contract.HealthDown
		h.Reason = fmt.Sprintf("%d %s failures in a row, last one %s",
			f.Streak, f.Kind, said(f.Kind, f.Reason))
		return h, true
	}
	h.State = contract.HealthDegraded
	h.Reason = fmt.Sprintf("%d failures in a row with no single cause, last one %s",
		f.Streak, f.Reason)
	return h, true
}

// said drops the bin from the front of a failure message once the sentence
// around it has already named the bin. The stored text is the untranslated
// error, which by convention leads with its own bin, so quoting it whole reads
// "14 unavailable failures in a row, last one unavailable: not logged in".
func said(kind, reason string) string {
	return strings.TrimPrefix(reason, kind+": ")
}

// Baseline is what one implementation costs on one repository, right now.
//
// It is a different question from Summary, which reports everything the store
// ever saw, per version, for a human to read. This one answers the funnel:
// "what does this cost here, at the version that is running today". So it
// carries averages per call rather than totals, and it never mixes versions.
type Baseline struct {
	// Spent is the average SUCCESSFUL call: total over successes, per axis.
	Spent contract.Sample
	// Successes is how many measurements back Spent, and the only count that
	// divides it.
	//
	// Failures are deliberately not in here. The first version of this store
	// averaged every attempt together on the argument that a tool which hangs
	// before failing has still eaten the wait -- true, but it proves the
	// opposite of what it was used for. The same average also makes an
	// implementation that refuses INSTANTLY look like the fastest thing on the
	// machine, and the funnel then gives it everything. Failing cheaply was
	// rewarded. A failure is not a price: it is the absence of one.
	Successes int
	// Attempts is every try, successful or not, and Failures how many broke.
	// Neither divides Spent. They are the health question, and they are what
	// lets the trace explain an implementation with a long record and no
	// numbers.
	Attempts int
	Failures int
	// ToolVersion is the version those attempts belong to.
	ToolVersion string
	// Fault is the failure streak at the newest end of the record.
	Fault Fault
}

// costs reads the current figures for one capability on one repository.
//
// # Only successes are a price
//
// The cost columns count `ok` rows and divide by `ok` rows. Attempts and
// failures are still totalled, because how often something was tried and how
// often it broke is worth knowing -- just never as a number that makes a
// broken provider look cheap.
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
	       count(*) AS attempts,
	       count(*) FILTER (WHERE NOT ok) AS failures,
	       count(*) FILTER (WHERE ok) AS wins,
	       coalesce(sum(duration_us) FILTER (WHERE ok), 0) AS dsum,
	       coalesce(sum(tokens) FILTER (WHERE ok), 0) AS tsum,
	       max(peak_rss_bytes) FILTER (WHERE ok) AS rmax,
	       max(happened_at) AS last_seen
	FROM measurement
	WHERE NOT folded AND capability = ? AND repository = ?
	GROUP BY 1, 2
	UNION ALL
	SELECT implementation, tool_version,
	       sum(attempts), sum(failures), sum(ok_attempts),
	       sum(ok_duration_us_sum), sum(ok_tokens_sum),
	       max(peak_rss_max), max(bucket)
	FROM rollup
	WHERE capability = ? AND repository = ?
	GROUP BY 1, 2
), merged AS (
	SELECT implementation, tool_version, sum(attempts) AS attempts,
	       sum(failures) AS failures, sum(wins) AS wins, sum(dsum) AS dsum,
	       sum(tsum) AS tsum, max(rmax) AS rmax, max(last_seen) AS last_seen
	FROM parts
	GROUP BY 1, 2
)
SELECT implementation, tool_version, attempts, failures, wins, dsum, tsum, rmax
FROM merged
QUALIFY row_number() OVER (
	PARTITION BY implementation ORDER BY last_seen DESC, tool_version DESC
) = 1`

// faults finds the run of failures at the newest end of each implementation's
// record: every attempt after the last successful one.
//
// Unlike costs this reads folded rows too. Folding is a summary of what things
// cost, and it throws the ordering away; the attempt rows outlive it for the
// whole fine window precisely so questions like this one can still be asked.
// Nothing here crosses tool versions either way: a streak is about the binary
// installed right now, and an upgrade should be given the chance to be better.
const faults = `WITH recent AS (
	SELECT implementation, ok, failure_kind, failure, happened_at,
	       row_number() OVER (
	           PARTITION BY implementation ORDER BY happened_at DESC
	       ) AS rn
	FROM measurement
	WHERE capability = ? AND repository = ?
), stop AS (
	SELECT implementation,
	       coalesce(min(rn) FILTER (WHERE ok), 999999999) AS first_ok
	FROM recent
	GROUP BY 1
), run AS (
	SELECT r.implementation, r.failure_kind, r.failure, r.happened_at, r.rn
	FROM recent r JOIN stop s USING (implementation)
	WHERE r.rn < s.first_ok
)
SELECT implementation, count(*) AS streak,
       count(DISTINCT failure_kind) AS bins,
       arg_min(failure_kind, rn) AS kind,
       arg_min(failure, rn) AS reason,
       max(happened_at) AS latest
FROM run
GROUP BY 1`

// Baselines reports what each implementation of a capability has done on one
// repository: what its successful calls cost, and whether its recent ones
// failed.
//
// Cost is asked per repository because it is not a property of the tool: the
// same provider is cheap with a warm index and expensive without one. An
// implementation absent from the answer has never been tried here, which is
// not the same as being free.
func (s *Store) Baselines(ctx context.Context, capability, repository string) (map[string]Baseline, error) {
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

	out := make(map[string]Baseline)
	if err := s.readCosts(ctx, db, capability, repository, out); err != nil {
		return nil, err
	}
	if err := s.readFaults(ctx, db, capability, repository, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) readCosts(ctx context.Context, db *sql.DB, capability, repository string,
	out map[string]Baseline) error {
	rows, err := db.QueryContext(ctx, costs, capability, repository, capability, repository)
	if err != nil {
		return fmt.Errorf("metrics: costs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id string
		var b Baseline
		var attempts, failures, wins, dsum, tsum int64
		var rmax *int64
		if err := rows.Scan(&id, &b.ToolVersion, &attempts, &failures, &wins,
			&dsum, &tsum, &rmax); err != nil {
			return fmt.Errorf("metrics: costs: %w", err)
		}
		b.Attempts = int(attempts)
		b.Failures = int(failures)
		b.Successes = int(wins)
		// A row with attempts but no successes is the whole point of the
		// split: it keeps its record and holds no price at all, so the funnel
		// falls back to the declared estimate instead of believing a number
		// made of refusals.
		if wins > 0 {
			b.Spent = contract.Sample{
				Duration: time.Duration(dsum/wins) * time.Microsecond,
				Tokens:   int(tsum / wins),
			}
			if rmax != nil {
				b.Spent.PeakRSS = *rmax
			}
		}
		out[id] = b
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("metrics: costs: %w", err)
	}
	return nil
}

func (s *Store) readFaults(ctx context.Context, db *sql.DB, capability, repository string,
	out map[string]Baseline) error {
	rows, err := db.QueryContext(ctx, faults, capability, repository)
	if err != nil {
		return fmt.Errorf("metrics: faults: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id string
		var streak, bins int64
		var kind, reason string
		var latest time.Time
		if err := rows.Scan(&id, &streak, &bins, &kind, &reason, &latest); err != nil {
			return fmt.Errorf("metrics: faults: %w", err)
		}
		b := out[id]
		b.Fault = Fault{Streak: int(streak), Reason: reason, Latest: latest}
		// One bin repeated is an outage that can be named. Several bins is a
		// provider breaking differently every time, and naming one of them
		// would be picking a favorite out of a bag.
		if bins == 1 {
			b.Fault.Kind = kind
		}
		out[id] = b
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("metrics: faults: %w", err)
	}
	return nil
}

// Apply fills the measured half of each candidate's cost from the base, and
// its health from the base's recent failures.
//
// The declared estimate is left exactly as it was written. That is the hybrid:
// an implementation the base has never seen here keeps ranking on its guess,
// and one it has seen enough of ranks on what actually happened.
//
// Health is filled the same way and for the same reason. A probe answers "is
// this provider up" in the abstract; the record answers "did it work here,
// just now", which is the question the funnel is actually asking. The
// orchestrator already marks a provider down the moment one call reports
// itself unavailable, but that mark lives in a catalog held in memory, and
// Atenea is a CLI at least as often as it is a service: a fresh process starts
// with a clean catalog and forgets every fault before it. This is the half
// that survives, because it is on disk.
//
// A streak only ever makes health worse: an implementation nobody probed and
// nobody caught failing stays unknown rather than being promoted to alive by
// silence.
//
// Candidates are filled in place. They are the caller's own copies out of the
// catalog -- which stays declarative, and never learns a number from a run.
//
// The returned lines are for the trace. An implementation with a long record
// and no price is the one thing a reader cannot work out from the numbers
// alone -- the funnel shows it ranking on an estimate while the base plainly
// holds attempts for it -- so it is said out loud rather than left to look
// like a bug in the ranking.
func Apply(base map[string]Baseline, candidates []contract.Implementation, now time.Time) []string {
	var notices []string
	for i := range candidates {
		b, ok := base[candidates[i].ID]
		if !ok {
			continue
		}
		candidates[i].Cost.Measured = b.Spent
		candidates[i].Cost.Samples = b.Successes
		candidates[i].Cost.ToolVersion = b.ToolVersion
		if health, hurt := b.Fault.Health(now); hurt &&
			health.State.Rank() > candidates[i].Health.State.Rank() {
			candidates[i].Health = health
		}
		if b.Attempts > 0 && b.Successes == 0 {
			notices = append(notices, fmt.Sprintf(
				"%s: %s here, none of them successful, so it has no measured cost and ranks on its declared estimate",
				candidates[i].ID, plural(b.Attempts, "attempt")))
		}
	}
	return notices
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
