package metrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// The tier names as they appear in the grain column, coarsest last.
const (
	grainHour  = "hour"
	grainDay   = "day"
	grainWeek  = "week"
	grainMonth = "month"
)

// rollupColumns is the insert order every fold shares.
const rollupColumns = `grain, bucket, capability, implementation, provider,
	repository, tool_version, attempts, failures, duration_us_sum,
	duration_us_max, tokens_sum, peak_rss_max, rss_samples,
	ok_attempts, ok_duration_us_sum, ok_tokens_sum`

// mergeRollup is what happens when a fold lands on a bucket that already
// exists. Counts add and maxima take the larger, which is why only mergeable
// figures are stored: these three lines are the whole reason the ladder can be
// walked twice without inventing numbers.
//
// Memory is the exception that needs spelling out. NULL there means nobody
// could weigh it, so it must not win a comparison against a real figure and
// must not be flattened to zero either -- zero bytes is a measurement, and this
// is the absence of one.
const mergeRollup = `ON CONFLICT (grain, bucket, capability, implementation, repository, tool_version)
DO UPDATE SET
	provider        = excluded.provider,
	attempts        = rollup.attempts + excluded.attempts,
	failures        = rollup.failures + excluded.failures,
	duration_us_sum = rollup.duration_us_sum + excluded.duration_us_sum,
	duration_us_max = greatest(rollup.duration_us_max, excluded.duration_us_max),
	tokens_sum      = rollup.tokens_sum + excluded.tokens_sum,
	peak_rss_max    = CASE
		WHEN rollup.peak_rss_max IS NULL THEN excluded.peak_rss_max
		WHEN excluded.peak_rss_max IS NULL THEN rollup.peak_rss_max
		ELSE greatest(rollup.peak_rss_max, excluded.peak_rss_max) END,
	rss_samples     = rollup.rss_samples + excluded.rss_samples,
	ok_attempts        = rollup.ok_attempts + excluded.ok_attempts,
	ok_duration_us_sum = rollup.ok_duration_us_sum + excluded.ok_duration_us_sum,
	ok_tokens_sum      = rollup.ok_tokens_sum + excluded.ok_tokens_sum`

// foldAttempts counts closed attempts into their hour.
//
// count(peak_rss_bytes) skips the NULLs, so the sample count is how many
// attempts could actually be weighed rather than how many happened. An average
// taken later divides by the right number.
//
// The three ok_ columns carry the successful half on its own, because that is
// the half allowed to be a price. Everything else here counts every attempt:
// how often a provider was tried and how slow its worst call was stay true
// whether or not the call worked.
const foldAttempts = `INSERT INTO rollup (` + rollupColumns + `)
SELECT '` + grainHour + `', date_trunc('hour', happened_at), capability, implementation,
       any_value(provider), repository, tool_version,
       count(*), count(*) FILTER (WHERE NOT ok),
       sum(duration_us), max(duration_us), sum(tokens),
       max(peak_rss_bytes), count(peak_rss_bytes),
       count(*) FILTER (WHERE ok),
       coalesce(sum(duration_us) FILTER (WHERE ok), 0),
       coalesce(sum(tokens) FILTER (WHERE ok), 0)
FROM measurement
WHERE NOT folded AND happened_at < ?
GROUP BY 2, 3, 4, 6, 7
` + mergeRollup

// promoteRollup folds one grain into the next coarser one.
const promoteRollup = `INSERT INTO rollup (` + rollupColumns + `)
SELECT ?, date_trunc(?, bucket), capability, implementation,
       any_value(provider), repository, tool_version,
       sum(attempts), sum(failures), sum(duration_us_sum), max(duration_us_max),
       sum(tokens_sum), max(peak_rss_max), sum(rss_samples),
       sum(ok_attempts), sum(ok_duration_us_sum), sum(ok_tokens_sum)
FROM rollup
WHERE grain = ? AND bucket < ?
GROUP BY 2, 3, 4, 6, 7
` + mergeRollup

// compactJob is the maintenance marker's key.
const compactJob = "compact"

// Compact walks the retention ladder once, whatever the marker says.
//
// It is safe to run at any time and safe to run twice: attempts carry a folded
// flag so they cannot be counted into an hour more than once, and every
// promotion deletes the rows it just merged inside the same transaction.
//
// Only closed periods are folded. An hour still in progress would be summarized
// halfway and then never revisited, which is worse than waiting an hour.
func (s *Store) Compact(ctx context.Context, now time.Time) error {
	_, err := s.compact(ctx, now, 0)
	return err
}

// CompactIfDue walks the ladder only when at least every has passed since the
// last pass, and reports whether it did.
//
// This is what a CLI calls. A command that lives for half a second has no
// rhythm of its own, so the rhythm lives on disk: the mark is read and written
// in the same transaction as the work, which is what stops two Ateneas
// starting together from both deciding they are the one to do it.
func (s *Store) CompactIfDue(ctx context.Context, now time.Time, every time.Duration) (bool, error) {
	if every <= 0 {
		return false, contract.Fail(contract.FailureInvalidInput,
			"metrics: compaction interval must be above 0, got %s", every)
	}
	return s.compact(ctx, now, every)
}

func (s *Store) compact(ctx context.Context, now time.Time, every time.Duration) (bool, error) {
	// Whatever is buffered belongs on disk before the history is reshaped,
	// otherwise this pass summarizes a period it has not been told about yet.
	if err := s.Flush(ctx); err != nil {
		return false, err
	}

	db, err := s.connect(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("metrics: compact begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	utc := now.UTC()
	if every > 0 {
		due, err := isDue(ctx, tx, utc, every)
		if err != nil || !due {
			return false, err
		}
	}
	closedHour := utc.Truncate(time.Hour)

	if _, err := tx.ExecContext(ctx, foldAttempts, closedHour); err != nil {
		return false, fmt.Errorf("metrics: fold attempts: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE measurement SET folded = TRUE WHERE NOT folded AND happened_at < ?",
		closedHour); err != nil {
		return false, fmt.Errorf("metrics: mark folded: %w", err)
	}
	// Attempts outlive their fold on purpose: for as long as the fine window
	// lasts, the failure reasons and the exact percentiles are still there to
	// read. Only then does the hour become the whole story.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM measurement WHERE folded AND happened_at < ?",
		utc.Add(-s.retention.Attempts)); err != nil {
		return false, fmt.Errorf("metrics: prune attempts: %w", err)
	}

	ladder := []struct {
		from, to string
		after    time.Duration
	}{
		{grainHour, grainDay, s.retention.Hour},
		{grainDay, grainWeek, s.retention.Day},
		{grainWeek, grainMonth, s.retention.Week},
	}
	for _, rung := range ladder {
		if err := promote(ctx, tx, rung.from, rung.to, utc.Add(-rung.after)); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO maintenance (job, last_run) VALUES (?, ?)
		 ON CONFLICT (job) DO UPDATE SET last_run = excluded.last_run`,
		compactJob, utc); err != nil {
		return false, fmt.Errorf("metrics: mark compaction: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("metrics: compact commit: %w", err)
	}
	return true, nil
}

// isDue reads the marker inside the caller's transaction.
func isDue(ctx context.Context, tx *sql.Tx, now time.Time, every time.Duration) (bool, error) {
	var last time.Time
	err := tx.QueryRowContext(ctx,
		"SELECT last_run FROM maintenance WHERE job = ?", compactJob).Scan(&last)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Never run on this database. The first pass is due immediately: a
		// fresh store has nothing to fold, so it costs nothing and it sets the
		// mark that spaces out every pass after it.
		return true, nil
	case err != nil:
		return false, fmt.Errorf("metrics: read maintenance mark: %w", err)
	}
	return !now.Before(last.Add(every)), nil
}

func promote(ctx context.Context, tx *sql.Tx, from, to string, before time.Time) error {
	if _, err := tx.ExecContext(ctx, promoteRollup, to, to, from, before); err != nil {
		return fmt.Errorf("metrics: promote %s to %s: %w", from, to, err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM rollup WHERE grain = ? AND bucket < ?", from, before); err != nil {
		return fmt.Errorf("metrics: prune %s: %w", from, err)
	}
	return nil
}
