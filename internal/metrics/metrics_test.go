package metrics

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func store(t *testing.T, opts Options) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "metrics.duckdb"), opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func attempt(at time.Time, capability, impl string) Measurement {
	return Measurement{
		At:             at,
		RunID:          "run-1",
		StepID:         "step-1",
		Capability:     capability,
		Implementation: impl,
		Provider:       strings.Split(impl, ".")[0],
		Repository:     "current",
		ToolVersion:    "14.1.0",
		Spent:          contract.Sample{Duration: 100 * time.Millisecond, Tokens: 0, PeakRSS: 5 << 20},
		OK:             true,
	}
}

// The numbered files are the whole ordering, so a store that has been opened
// must carry every step in its ledger and must not re-run them next time.
func TestMigrationsAreRecordedOnceInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.duckdb")
	first, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = first.Close()

	second, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	db, err := second.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query("SELECT version, name FROM schema_migration ORDER BY version")
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer rows.Close()
	var got []int
	for rows.Next() {
		var v int
		var name string
		if err := rows.Scan(&v, &name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	steps, err := migrations()
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if len(got) != len(steps) {
		t.Fatalf("ledger has %d rows after two opens, want %d", len(got), len(steps))
	}
	for i, step := range steps {
		if got[i] != step.version {
			t.Errorf("ledger[%d] = %d, want %d", i, got[i], step.version)
		}
	}
}

// A migration nobody can order is a step that would silently never run.
func TestEveryMigrationAnnouncesItsPlace(t *testing.T) {
	steps, err := migrations()
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if len(steps) == 0 {
		t.Fatal("no migrations are embedded")
	}
	for i := 1; i < len(steps); i++ {
		if steps[i].version <= steps[i-1].version {
			t.Errorf("migration %d is not after %d", steps[i].version, steps[i-1].version)
		}
	}
}

// Recording is not writing. The batch is the design; a store that hit the disk
// on every attempt would be paying for durability nobody asked for on the hot
// path of real work.
func TestRecordBuffersUntilFlush(t *testing.T) {
	s := store(t, Options{})
	s.Record(attempt(time.Now(), "code.search", "ripgrep"))
	s.Record(attempt(time.Now(), "code.search", "ripgrep"))

	if s.Pending() != 2 {
		t.Fatalf("pending = %d, want 2", s.Pending())
	}
	if s.Written() != 0 {
		t.Fatalf("written = %d before a flush", s.Written())
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if s.Pending() != 0 || s.Written() != 2 {
		t.Fatalf("after flush pending = %d written = %d, want 0 and 2", s.Pending(), s.Written())
	}
}

// A tool that is quick when it works and fails half the time is not quick. The
// failed attempt is a measurement, and its reason has to survive to be read.
func TestFailedAttemptsAreKeptWithTheirReason(t *testing.T) {
	s := store(t, Options{})
	bad := attempt(time.Now(), "code.search", "ripgrep")
	bad.OK = false
	bad.FailureKind = "timeout"
	bad.Failure = "provider took too long"
	bad.Raw = "rg: operation timed out after 30s"
	s.Record(bad)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	db, err := s.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = db.Close() }()
	var kind, reason, raw string
	var ok bool
	row := db.QueryRow("SELECT ok, failure_kind, failure, raw FROM measurement")
	if err := row.Scan(&ok, &kind, &reason, &raw); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if ok || kind != "timeout" || reason != "provider took too long" {
		t.Fatalf("stored ok=%v kind=%q reason=%q", ok, kind, reason)
	}
	if raw != "rg: operation timed out after 30s" {
		t.Fatalf("stored raw = %q, want the provider's own text", raw)
	}

	rows, err := s.Summary(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(rows) != 1 || rows[0].Attempts != 1 || rows[0].Failures != 1 {
		t.Fatalf("summary = %+v, want one attempt counted as one failure", rows)
	}
}

func TestPersistedRawMeasurementIsRedacted(t *testing.T) {
	s := store(t, Options{})
	bad := attempt(time.Now(), "code.search", "ripgrep")
	bad.OK = false
	bad.Raw = "Authorization: Bearer live-token api_key=secret-value"
	s.Record(bad)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	db, err := s.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = db.Close() }()
	var raw string
	if err := db.QueryRow("SELECT raw FROM measurement").Scan(&raw); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(raw, "live-token") || strings.Contains(raw, "secret-value") {
		t.Fatalf("stored raw contains a credential: %q", raw)
	}
	if !strings.Contains(raw, "[REDACTED]") {
		t.Fatalf("stored raw lacks redaction marker: %q", raw)
	}
}

// The count used to live for the length of one sentence: the adapter wrote
// "N match(es) fell outside the requested scope and were dropped" onto the
// answer, whoever asked read it once, and nothing that ranks providers ever
// saw the number. This is the half that makes it a fact instead of prose.
func TestOutOfScopeHitsAreRecordedAsANumber(t *testing.T) {
	s := store(t, Options{})
	wandered := attempt(time.Now(), "code.search", "claude.search")
	wandered.OutOfScope = 7
	s.Record(wandered)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	db, err := s.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = db.Close() }()
	var stored int64
	if err := db.QueryRow("SELECT out_of_scope FROM measurement").Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != 7 {
		t.Fatalf("out_of_scope = %d, want 7", stored)
	}
}

// The decision this column exists under: wandering is recorded and never
// scored. Health answers "can this provider answer at all", and one that
// strays still answers -- the core drops the strays and the caller gets a
// clean result. Scoring it here would mark a working provider down and the
// funnel would then report no implementation available for something that
// demonstrably works.
func TestWanderingOutOfScopeNeverDemotesAProvider(t *testing.T) {
	s := store(t, Options{})
	now := time.Now()
	for i := range 5 {
		bad := attempt(now.Add(time.Duration(i)*time.Second), "code.search", "claude.search")
		bad.OutOfScope = 100
		s.Record(bad)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	verdicts, err := s.Health(context.Background(), now)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	verdict, seen := verdicts["claude.search"]
	if !seen {
		t.Fatal("the attempts were not measured at all: this test would pass for the wrong reason")
	}
	if verdict.Health.State == contract.HealthDown {
		t.Errorf("five all-wandering calls marked the provider down: %+v", verdict.Health)
	}
}

// Zero bytes is a measurement; the absence of one is not. An adapter talking to
// a server has no process to weigh, and the store has to be able to say so.
func TestUnweighedAttemptsAreNullNotZero(t *testing.T) {
	s := store(t, Options{})
	weighed := attempt(time.Now(), "code.search", "ripgrep")
	unweighed := attempt(time.Now(), "symbol.definition", "fixture.definition")
	unweighed.Spent.PeakRSS = 0
	s.Record(weighed)
	s.Record(unweighed)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	db, err := s.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = db.Close() }()
	var nulls int
	if err := db.QueryRow(
		"SELECT count(*) FROM measurement WHERE peak_rss_bytes IS NULL").Scan(&nulls); err != nil {
		t.Fatalf("count: %v", err)
	}
	if nulls != 1 {
		t.Fatalf("%d rows have no memory figure, want exactly the unweighed one", nulls)
	}

	rows, err := s.Summary(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	for _, r := range rows {
		want := int64(1)
		if r.Implementation == "fixture.definition" {
			want = 0
		}
		if r.RSSSamples != want {
			t.Errorf("%s weighed %d of %d attempts, want %d",
				r.Implementation, r.RSSSamples, r.Attempts, want)
		}
	}
}

// A store that cannot reach its file must not eat the batch. The rows go back
// where they were so the next attempt carries them: a slow store, not a lossy
// one.
func TestAFailedFlushKeepsTheRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.duckdb")
	s, err := Open(path, Options{LockWait: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.Record(attempt(time.Now(), "code.search", "ripgrep"))

	// Something that is not a database where the database was.
	if err := os.WriteFile(path, []byte("not a database at all"), 0o600); err != nil {
		t.Fatalf("clobber: %v", err)
	}
	if err := s.Flush(context.Background()); err == nil {
		t.Fatal("flush onto a broken file reported success")
	}
	if s.Pending() != 1 {
		t.Fatalf("pending = %d after a failed flush, want the row kept", s.Pending())
	}
	if s.Written() != 0 {
		t.Fatalf("written = %d after a failed flush", s.Written())
	}
}

// The ceiling exists so a wedged store cannot eat the process. What it drops it
// counts, because the one thing worse than losing a measurement is not knowing.
func TestAFullBufferDropsTheOldestAndSaysSo(t *testing.T) {
	s := store(t, Options{BufferLimit: 3})
	base := time.Now()
	for i := range 5 {
		m := attempt(base.Add(time.Duration(i)*time.Second), "code.search", "ripgrep")
		m.StepID = string(rune('a' + i))
		s.Record(m)
	}
	if s.Pending() != 3 {
		t.Fatalf("pending = %d, want the ceiling of 3", s.Pending())
	}
	if s.Dropped() != 2 {
		t.Fatalf("dropped = %d, want 2", s.Dropped())
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	db, err := s.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = db.Close() }()
	var first string
	if err := db.QueryRow("SELECT min(step_id) FROM measurement").Scan(&first); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if first != "c" {
		t.Fatalf("oldest surviving step is %q, want the two oldest dropped", first)
	}
}

// Opening does not hold the file. Two Ateneas run side by side all the time,
// and DuckDB allows one writer, so the lock has to be held for the length of a
// flush and no longer.
func TestTheFileIsFreeBetweenFlushes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.duckdb")
	first, err := Open(path, Options{LockWait: 2 * time.Second})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer first.Close()
	first.Record(attempt(time.Now(), "code.search", "ripgrep"))
	if err := first.Flush(context.Background()); err != nil {
		t.Fatalf("first flush: %v", err)
	}

	second, err := Open(path, Options{LockWait: 2 * time.Second})
	if err != nil {
		t.Fatalf("a second store could not open the same file: %v", err)
	}
	defer second.Close()
	second.Record(attempt(time.Now(), "code.search", "ripgrep"))
	if err := second.Flush(context.Background()); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	rows, err := second.Summary(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(rows) != 1 || rows[0].Attempts != 2 {
		t.Fatalf("summary = %+v, want both stores' attempts", rows)
	}
}

// The band where the funnel has the most to decide is the cheap one, and a
// warm text search over a small repository runs in tens of microseconds. A
// store that rounds those to zero cannot rank the providers it most needs to.
func TestAFastCallIsNotRecordedAsFree(t *testing.T) {
	s := store(t, Options{})
	fast := attempt(time.Now(), "code.search", "ripgrep")
	fast.Spent.Duration = 77 * time.Microsecond
	s.Record(fast)

	rows, err := s.Summary(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	if rows[0].Mean != 77*time.Microsecond {
		t.Errorf("mean = %v, want 77µs", rows[0].Mean)
	}
	if rows[0].Slowest != 77*time.Microsecond {
		t.Errorf("slowest = %v, want 77µs", rows[0].Slowest)
	}
}

func TestFilterAndClearDescribeAndRemoveOnlyTheirRows(t *testing.T) {
	if (Filter{}).Empty() == false {
		t.Fatal("zero filter is not empty")
	}
	filter := Filter{Capability: "code.search", Repository: "current"}
	if got := filter.String(); got != "capability code.search, repository current" {
		t.Fatalf("filter string = %q", got)
	}
	if got, args := filter.where(); got != "capability = ? AND repository = ?" || len(args) != 2 {
		t.Fatalf("where = %q, %v", got, args)
	}
	if got := (Cleared{Attempts: 2, Rollups: 3}).Total(); got != 5 {
		t.Fatalf("cleared total = %d", got)
	}

	s := store(t, Options{})
	s.Record(attempt(time.Now(), "code.search", "ripgrep"))
	s.Record(attempt(time.Now(), "other", "ripgrep"))
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	cleared, err := s.Clear(context.Background(), filter)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.Attempts != 1 || cleared.Total() != 1 {
		t.Fatalf("cleared = %+v", cleared)
	}
}

// Two providers a hair apart still have to be tellable apart: a resolution
// that collapses them is a funnel that picks by luck.
func TestTwoFastProvidersStayDistinguishable(t *testing.T) {
	s := store(t, Options{})
	quick := attempt(time.Now(), "code.search", "ripgrep")
	quick.Spent.Duration = 40 * time.Microsecond
	slow := attempt(time.Now(), "code.search", "fixture.search")
	slow.Spent.Duration = 900 * time.Microsecond
	s.Record(quick)
	s.Record(slow)

	rows, err := s.Summary(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	means := map[string]time.Duration{}
	for _, row := range rows {
		means[row.Implementation] = row.Mean
	}
	if means["ripgrep"] >= means["fixture.search"] {
		t.Fatalf("ripgrep %v is not below fixture %v", means["ripgrep"], means["fixture.search"])
	}
}

// Clear used to empty the buffer as its very first act and only then try to
// open the base. A connect that failed therefore answered with an error having
// destroyed every buffered measurement and deleted nothing at all -- the one
// outcome worse than either half on its own.
func TestAClearThatCannotOpenTheBaseKeepsTheBuffer(t *testing.T) {
	// A directory is a path DuckDB cannot open, and it fails for a reason
	// isLocked does not recognize, so connect gives up at once instead of
	// spending LockWait on it.
	s := &Store{path: t.TempDir(), limit: DefaultBufferLimit, lockWait: 50 * time.Millisecond}
	s.Record(attempt(time.Now(), "code.search", "ripgrep"))
	s.Record(attempt(time.Now(), "code.search", "fixture.search"))

	if _, err := s.Clear(context.Background(), Filter{}); err == nil {
		t.Fatal("clearing a base that cannot be opened reported success")
	}
	if pending := s.Pending(); pending != 2 {
		t.Errorf("pending = %d, want 2: the buffer was thrown away for a clear that removed nothing", pending)
	}
}

// A clear narrowed to one implementation used to take the whole buffer with
// it: the rows on disk were filtered and the rows still in memory were not, so
// clearing one poisoned provider silently erased the last half-minute of every
// other provider's measurements too.
func TestANarrowedClearLeavesTheOtherImplementationsBuffered(t *testing.T) {
	s := store(t, Options{})
	s.Record(attempt(time.Now(), "code.search", "ripgrep"))
	s.Record(attempt(time.Now(), "code.search", "fixture.search"))

	cleared, err := s.Clear(context.Background(), Filter{Implementation: "ripgrep"})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.Attempts != 1 {
		t.Errorf("cleared %d attempts, want the 1 buffered row the filter names", cleared.Attempts)
	}
	if pending := s.Pending(); pending != 1 {
		t.Fatalf("pending = %d, want 1: fixture's measurement was not what the caller asked to be rid of", pending)
	}

	rows, err := s.Summary(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(rows) != 1 || rows[0].Implementation != "fixture.search" {
		t.Fatalf("summary = %+v, want fixture's attempt alone", rows)
	}
}

// Every caller here flushes in order to read back what it just recorded --
// Baselines, Health, Summary, Compact and Close all do. A flush that found the
// buffer already drained by another goroutine used to return nil immediately,
// which told its caller the rows were durable while they were still in the
// other goroutine's batch on its way to the disk.
func TestAFlushWaitsForTheOneAlreadyWriting(t *testing.T) {
	s := store(t, Options{})
	const rows = 3000
	now := time.Now()
	for i := range rows {
		s.Record(attempt(now.Add(time.Duration(i)*time.Millisecond), "code.search", "ripgrep"))
	}

	first := make(chan error, 1)
	go func() { first <- s.Flush(context.Background()) }()
	// Wait for the other goroutine to take the batch, so the flush below is
	// certainly the second one and certainly finds nothing buffered.
	for s.Pending() != 0 {
		runtime.Gosched()
	}

	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if written := s.Written(); written != rows {
		t.Errorf("Flush returned with %d of %d measurements written: its caller is about to query a base missing exactly the rows it flushed for", written, rows)
	}
	if err := <-first; err != nil {
		t.Fatalf("first flush: %v", err)
	}
}

// Overflow must overwrite the oldest slot without shifting surviving entries.
// Verify the ring and its chronological drain directly; wall-clock ratios at
// nanosecond scale depend on runner scheduling and cache behavior. Performance
// remains covered by BenchmarkRecord and scripts/benchmark-check.sh.
func TestRecordOverflowKeepsSurvivorsInPlace(t *testing.T) {
	for _, limit := range []int{1, 200, 20000} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			s := &Store{limit: limit}
			measurement := func(id int) Measurement {
				m := attempt(time.Unix(1, 0), "code.search", "ripgrep")
				m.StepID = strconv.Itoa(id)
				return m
			}
			expected := make([]string, limit)
			for i := range limit {
				m := measurement(i)
				s.Record(m)
				expected[i] = m.StepID
			}
			backing := &s.buf[0]
			capacity := cap(s.buf)
			// Cross a full rotation and keep going to exercise wrapped heads.
			for i := range limit + 3 {
				m := measurement(limit + i)
				s.Record(m)
				expected[i%limit] = m.StepID
				if len(s.buf) != limit || cap(s.buf) != capacity || &s.buf[0] != backing {
					t.Fatal("overflow changed the backing array or buffer size")
				}
				if s.Pending() != limit || s.Dropped() != i+1 {
					t.Fatalf("overflow %d: pending=%d dropped=%d", i+1, s.Pending(), s.Dropped())
				}
				if i == 0 || i == limit-1 || i == limit+2 {
					for slot, want := range expected {
						if got := s.buf[slot].StepID; got != want {
							t.Fatalf("overflow %d moved slot %d: got %s, want %s", i+1, slot, got, want)
						}
					}
				}
			}
			batch := s.drain()
			if len(batch) != limit || s.Pending() != 0 {
				t.Fatalf("drain: batch=%d pending=%d", len(batch), s.Pending())
			}
			for i, m := range batch {
				if want := strconv.Itoa(limit + 3 + i); m.StepID != want {
					t.Fatalf("drain row %d: got %s, want %s", i, m.StepID, want)
				}
			}
		})
	}
}

// Several goroutines of one runWave ask for a baseline at the same moment.
//
// The exclusive lock DuckDB takes belongs to the process, not to the handle:
// connections opened from one Atenea share its instance and all answer, which
// is why nothing here serializes connect and why the funnel does not have to
// take turns with itself. That is a property of the driver rather than of this
// package, so it is asserted rather than assumed -- the day it stops holding,
// every parallel wave starts failing on a message match in isLocked.
func TestConcurrentReadersQueueForTheFile(t *testing.T) {
	s := store(t, Options{LockWait: 100 * time.Millisecond})
	s.Record(attempt(time.Now(), "code.search", "ripgrep"))

	const readers = 8
	var start sync.WaitGroup
	start.Add(1)
	errs := make(chan error, readers)
	for range readers {
		go func() {
			start.Wait()
			_, err := s.Baselines(context.Background(), "code.search", "current", "")
			errs <- err
		}()
	}
	start.Done()
	for range readers {
		if err := <-errs; err != nil {
			t.Errorf("a concurrent reader was turned away by its own process: %v", err)
		}
	}
}

// A measurement recorded after the final flush used to sit in a buffer nothing
// would ever drain.
//
// That is the shutdown that overran: work is still running while the store is
// being settled, and it goes on calling Record. Those rows went with the
// process, and Dropped -- the number that exists to say how much was lost --
// reported zero, so nothing anywhere said they had ever existed.
func TestASealedStoreCountsWhatItCanNoLongerKeep(t *testing.T) {
	store := store(t, Options{})

	store.Record(Measurement{Capability: "code.search", Implementation: "ripgrep"})
	if got := store.Pending(); got != 1 {
		t.Fatalf("pending = %d, want the one measurement recorded", got)
	}

	store.Seal()
	before := store.Dropped()
	store.Record(Measurement{Capability: "code.search", Implementation: "ripgrep"})
	store.Record(Measurement{Capability: "code.search", Implementation: "ripgrep"})

	if got := store.Pending(); got != 1 {
		t.Errorf("pending = %d after sealing, want the one that was already there: "+
			"a sealed store must not queue work nothing will write", got)
	}
	if got := store.Dropped() - before; got != 2 {
		t.Errorf("dropped = %d, want the 2 recorded after the seal", got)
	}
}

// Sealing is idempotent and does not itself write: the caller decides how hard
// to try, which is why the core seals before its retry loop rather than after.
func TestSealingTwiceIsTheSameAsSealingOnce(t *testing.T) {
	store := store(t, Options{})
	store.Record(Measurement{Capability: "code.search", Implementation: "ripgrep"})

	store.Seal()
	store.Seal()
	if got := store.Pending(); got != 1 {
		t.Errorf("pending = %d, want the measurement still waiting: Seal does not flush", got)
	}
	if err := store.Flush(t.Context()); err != nil {
		t.Fatalf("Flush after Seal: %v", err)
	}
	if got := store.Written(); got != 1 {
		t.Errorf("written = %d, want the sealed store's backlog to still reach disk", got)
	}
}
