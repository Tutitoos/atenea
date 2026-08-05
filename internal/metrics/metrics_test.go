package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	defer db.Close()

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
	defer db.Close()
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

// Zero bytes is a measurement; the absence of one is not. An adapter talking to
// a server has no process to weigh, and the store has to be able to say so.
func TestUnweighedAttemptsAreNullNotZero(t *testing.T) {
	s := store(t, Options{})
	weighed := attempt(time.Now(), "code.search", "ripgrep")
	unweighed := attempt(time.Now(), "symbol.definition", "serena.definition")
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
	defer db.Close()
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
		if r.Implementation == "serena.definition" {
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
	defer db.Close()
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

// Two providers a hair apart still have to be tellable apart: a resolution
// that collapses them is a funnel that picks by luck.
func TestTwoFastProvidersStayDistinguishable(t *testing.T) {
	s := store(t, Options{})
	quick := attempt(time.Now(), "code.search", "ripgrep")
	quick.Spent.Duration = 40 * time.Microsecond
	slow := attempt(time.Now(), "code.search", "serena.search")
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
	if means["ripgrep"] >= means["serena.search"] {
		t.Fatalf("ripgrep %v is not below serena %v", means["ripgrep"], means["serena.search"])
	}
}
