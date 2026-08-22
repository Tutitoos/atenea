package metrics

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

const day = 24 * time.Hour

// tiers reports how many rows sit at each grain.
func tiers(t *testing.T, s *Store) map[string]int {
	t.Helper()
	db, err := s.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT grain, count(*) FROM rollup GROUP BY 1")
	if err != nil {
		t.Fatalf("tiers: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var grain string
		var n int
		if err := rows.Scan(&grain, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[grain] = n
	}
	var attempts int
	if err := db.QueryRow("SELECT count(*) FROM measurement").Scan(&attempts); err != nil {
		t.Fatalf("attempts: %v", err)
	}
	out["attempt"] = attempts
	return out
}

func bucketFor(t *testing.T, s *Store, grain string) (attempts, failures, rss int64) {
	t.Helper()
	db, err := s.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()
	var peak sql.NullInt64
	err = db.QueryRow(
		"SELECT sum(attempts), sum(failures), max(peak_rss_max) FROM rollup WHERE grain = ?",
		grain).Scan(&attempts, &failures, &peak)
	if err != nil {
		t.Fatalf("bucket %s: %v", grain, err)
	}
	return attempts, failures, peak.Int64
}

// Compaction has to be safe to run whenever the clock says so, which means
// running it twice cannot count the same call twice.
func TestFoldingTwiceCountsOnce(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC().Truncate(time.Hour)
	old := now.Add(-3 * time.Hour)
	s.Record(attempt(old, "code.search", "ripgrep"))
	s.Record(attempt(old.Add(10*time.Minute), "code.search", "ripgrep"))

	for range 3 {
		if err := s.Compact(context.Background(), now); err != nil {
			t.Fatalf("compact: %v", err)
		}
	}
	attempts, _, _ := bucketFor(t, s, grainHour)
	if attempts != 2 {
		t.Fatalf("hour bucket counted %d attempts after three passes, want 2", attempts)
	}
}

func TestCompactIfDueUsesTheOnDiskMaintenanceMark(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC().Truncate(time.Hour)
	due, err := s.CompactIfDue(context.Background(), now, time.Hour)
	if err != nil || !due {
		t.Fatalf("first compact due=%v err=%v", due, err)
	}
	due, err = s.CompactIfDue(context.Background(), now.Add(30*time.Minute), time.Hour)
	if err != nil || due {
		t.Fatalf("second compact due=%v err=%v", due, err)
	}
	if _, err := s.CompactIfDue(context.Background(), now, 0); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("zero interval kind = %v", contract.KindOf(err))
	}
}

// An hour that is still running would be summarized halfway and never revisited.
func TestTheCurrentHourIsLeftAlone(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	s.Record(attempt(now.Add(-5*time.Minute), "code.search", "ripgrep"))
	s.Record(attempt(now.Add(-90*time.Minute), "code.search", "ripgrep"))

	if err := s.Compact(context.Background(), now); err != nil {
		t.Fatalf("compact: %v", err)
	}
	attempts, _, _ := bucketFor(t, s, grainHour)
	if attempts != 1 {
		t.Fatalf("folded %d attempts, want only the one whose hour has closed", attempts)
	}
	// And the open one is still an attempt, not yet a shape.
	if got := tiers(t, s)["attempt"]; got != 2 {
		t.Fatalf("%d attempt rows, want both still on disk", got)
	}
}

// The whole point of the fine window: for a week you can still read why a
// particular call failed, and after it you have the shape and nothing else.
func TestAttemptsSurviveTheirFoldUntilTheWindowCloses(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC().Truncate(time.Hour)
	fresh := now.Add(-2 * day)
	stale := now.Add(-9 * day)
	s.Record(attempt(fresh, "code.search", "ripgrep"))
	s.Record(attempt(stale, "code.search", "ripgrep"))

	if err := s.Compact(context.Background(), now); err != nil {
		t.Fatalf("compact: %v", err)
	}
	got := tiers(t, s)
	if got["attempt"] != 1 {
		t.Fatalf("%d attempts left, want only the one inside the week", got["attempt"])
	}
	// Both were counted before either was pruned: the older one now lives on
	// as a day, the newer one is still an hour.
	total, _, _ := bucketFor(t, s, grainHour)
	dayTotal, _, _ := bucketFor(t, s, grainDay)
	if total+dayTotal != 2 {
		t.Fatalf("rollups hold %d attempts, want both counted before pruning", total+dayTotal)
	}
}

// The ladder, walked end to end: every rung waits longer than the last, and
// nothing is lost on the way up.
func TestTheLadderPromotesWithoutLosingAnything(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC().Truncate(time.Hour)
	ages := []time.Duration{
		2 * day,   // stays an hour
		20 * day,  // becomes a day
		90 * day,  // becomes a week
		300 * day, // becomes a month
	}
	for i, age := range ages {
		m := attempt(now.Add(-age), "code.search", "ripgrep")
		m.OK = i%2 == 0
		if !m.OK {
			m.FailureKind = "timeout"
			m.Failure = "took too long"
		}
		s.Record(m)
	}
	if err := s.Compact(context.Background(), now); err != nil {
		t.Fatalf("compact: %v", err)
	}

	got := tiers(t, s)
	for grain, want := range map[string]int{
		grainHour: 1, grainDay: 1, grainWeek: 1, grainMonth: 1,
	} {
		if got[grain] != want {
			t.Errorf("%s tier has %d buckets, want %d (all tiers: %v)", grain, got[grain], want, got)
		}
	}

	var attempts, failures int64
	for _, grain := range []string{grainHour, grainDay, grainWeek, grainMonth} {
		a, f, _ := bucketFor(t, s, grain)
		attempts += a
		failures += f
	}
	if attempts != 4 {
		t.Errorf("the ladder holds %d attempts, want the 4 that happened", attempts)
	}
	if failures != 2 {
		t.Errorf("the ladder holds %d failures, want 2", failures)
	}
}

// Memory does not add up across calls and an unweighed call is not a zero.
// Merging buckets must keep the largest real figure and ignore the gaps.
func TestMergingKeepsTheLargestRealMemoryFigure(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC().Truncate(time.Hour)

	small := attempt(now.Add(-2*time.Hour), "code.search", "ripgrep")
	small.Spent.PeakRSS = 3 << 20
	big := attempt(now.Add(-2*time.Hour).Add(time.Minute), "code.search", "ripgrep")
	big.Spent.PeakRSS = 9 << 20
	none := attempt(now.Add(-2*time.Hour).Add(2*time.Minute), "code.search", "ripgrep")
	none.Spent.PeakRSS = 0
	s.Record(small)
	s.Record(big)
	s.Record(none)

	if err := s.Compact(context.Background(), now); err != nil {
		t.Fatalf("compact: %v", err)
	}
	attempts, _, peak := bucketFor(t, s, grainHour)
	if attempts != 3 {
		t.Fatalf("hour holds %d attempts, want 3", attempts)
	}
	if peak != 9<<20 {
		t.Fatalf("peak = %d, want the largest real figure %d", peak, 9<<20)
	}

	db, err := s.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()
	var samples int64
	if err := db.QueryRow(
		"SELECT sum(rss_samples) FROM rollup WHERE grain = ?", grainHour).Scan(&samples); err != nil {
		t.Fatalf("samples: %v", err)
	}
	if samples != 2 {
		t.Fatalf("rss_samples = %d, want the 2 that could be weighed", samples)
	}
}

// Before the rollup carried the column, a stray count lived for exactly as
// long as its attempt stayed unfolded, and the first compaction pass rounded
// the whole history of it to zero without saying so. That was survivable while
// nothing read the number; a reader turns it into a figure that shrinks on a
// schedule for reasons its reader cannot see.
func TestStrayHitsSurviveTheFoldAndThePromotion(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC().Truncate(time.Hour)
	for i := range 3 {
		wandered := attempt(now.Add(-2*time.Hour).Add(time.Duration(i)*time.Minute),
			"code.search", "claude.search")
		wandered.OutOfScope = 4
		s.Record(wandered)
	}

	if err := s.Compact(context.Background(), now); err != nil {
		t.Fatalf("compact: %v", err)
	}
	rows, err := s.Summary(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want one", len(rows))
	}
	if rows[0].Wandered != 12 {
		t.Fatalf("Wandered = %d, want the 12 that were recorded before the fold", rows[0].Wandered)
	}

	// A second fold landing on a bucket that already exists takes the merge
	// path, not the insert. That branch adds every other count and had to be
	// taught to add this one too: a bucket touched twice must not keep only
	// the strays from whichever pass got there first.
	for i := range 2 {
		late := attempt(now.Add(-2*time.Hour).Add(time.Duration(10+i)*time.Minute),
			"code.search", "claude.search")
		late.OutOfScope = 5
		s.Record(late)
	}
	if err := s.Compact(context.Background(), now); err != nil {
		t.Fatalf("second fold: %v", err)
	}
	rows, err = s.Summary(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("summary after merge: %v", err)
	}
	if len(rows) != 1 || rows[0].Wandered != 22 {
		t.Fatalf("Wandered = %+v after a merge, want 22 in one row", rows)
	}

	// And again one grain coarser: the ladder is walked more than once, and a
	// sum that only survives the first rung is a sum that disappears tomorrow.
	if err := s.Compact(context.Background(), now.AddDate(0, 0, 8)); err != nil {
		t.Fatalf("third compact: %v", err)
	}
	rows, err = s.Summary(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("summary after promotion: %v", err)
	}
	var total int64
	for _, r := range rows {
		total += r.Wandered
	}
	if total != 22 {
		t.Fatalf("Wandered = %d after promotion, want 22", total)
	}
}

// A bucket where nothing could ever be weighed must stay empty rather than
// claiming the far side used no memory at all.
func TestABucketThatWasNeverWeighedStaysUnknown(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC().Truncate(time.Hour)
	for i := range 2 {
		m := attempt(now.Add(-2*time.Hour).Add(time.Duration(i)*time.Minute),
			"symbol.definition", "serena.definition")
		m.Spent.PeakRSS = 0
		s.Record(m)
	}
	if err := s.Compact(context.Background(), now); err != nil {
		t.Fatalf("compact: %v", err)
	}
	db, err := s.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()
	var peak sql.NullInt64
	if err := db.QueryRow("SELECT peak_rss_max FROM rollup").Scan(&peak); err != nil {
		t.Fatalf("read: %v", err)
	}
	if peak.Valid {
		t.Fatalf("peak = %d, want unknown rather than a figure nobody measured", peak.Int64)
	}
}

// The summary spans both halves of the store, and the week where an attempt is
// both a row and an hour must not be counted twice.
func TestTheSummarySpansAttemptsAndRollupsWithoutDoubleCounting(t *testing.T) {
	s := store(t, Options{})
	now := time.Now().UTC().Truncate(time.Hour)
	s.Record(attempt(now.Add(-3*time.Hour), "code.search", "ripgrep"))
	s.Record(attempt(now.Add(-30*time.Minute), "code.search", "ripgrep"))

	before, err := s.Summary(context.Background(), now.Add(-day))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(before) != 1 || before[0].Attempts != 2 {
		t.Fatalf("before compaction the summary says %+v, want 2 attempts", before)
	}

	if err := s.Compact(context.Background(), now); err != nil {
		t.Fatalf("compact: %v", err)
	}
	after, err := s.Summary(context.Background(), now.Add(-day))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(after) != 1 || after[0].Attempts != 2 {
		t.Fatalf("after compaction the summary says %+v, want the same 2 attempts", after)
	}
	if after[0].Mean != 100*time.Millisecond {
		t.Errorf("mean = %v, want 100ms", after[0].Mean)
	}
	if after[0].Slowest != 100*time.Millisecond {
		t.Errorf("slowest = %v, want 100ms", after[0].Slowest)
	}
}
