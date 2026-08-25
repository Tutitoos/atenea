// Package metrics is Atenea's measurement store: what every attempt cost, on
// three axes, filed under the capability that was asked for and the
// implementation that answered.
//
// It exists because the selector is only as good as its numbers. Until real
// measurements exist the funnel ranks on estimates somebody typed into a
// settings file, which is guesswork wearing a decimal point.
//
// # One writer, briefly
//
// DuckDB takes an exclusive lock on the database file, so two processes cannot
// hold it open at once -- and Atenea is a CLI at least as often as it is a
// service, so two of them running side by side is ordinary. The store therefore
// keeps the batch in memory and holds the file only for the length of a flush:
// opening costs single-digit milliseconds, a flush is a handful of rows, and
// between flushes the file belongs to whoever wants it next. Batching was
// already the design; this makes it the thing that keeps concurrent Ateneas out
// of each other's way.
//
// Nothing is dropped quietly. A flush that cannot get the lock puts its rows
// back and tries again on the next beat; only a buffer pushed past its ceiling
// loses anything, and it counts what it lost.
//
// # What this is not
//
// This is the baseline, not the log. It answers "what does this implementation
// usually cost on this repository", which is a question about averages, and
// averages are why batching is safe here: losing the last half-minute of rows
// moves a mean by nothing.
//
// The crash notebook the design also asks for is a different artifact with the
// opposite trade-off -- written the instant something goes wrong, because the
// one entry you lose is the one you needed. It lives in internal/notebook and
// is deliberately not this store; putting it here would either make every
// measurement pay for a disk write or make the notebook lossy, and both defeat
// the point.
package metrics

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2" // database/sql driver "duckdb"

	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// DefaultBufferLimit caps how many measurements wait in memory when flushes
// keep failing. Roughly a megabyte of rows: enough to ride out a long lock
// standoff, small enough that a wedged store cannot eat the process.
const DefaultBufferLimit = 10000

// DefaultLockWait is how long an operation keeps trying for the file lock
// before giving up. Another Atenea's flush is measured in milliseconds, so this
// is generous by two orders of magnitude.
const DefaultLockWait = 5 * time.Second

// Retention is the four-tier ladder from the design: fine detail about the
// recent past, shapes about the distant one.
//
// Every tier waits longer than the last before folding into the next, and each
// window is measured from now backwards.
type Retention struct {
	// Attempts is how long the per-attempt rows survive, failure reasons and
	// all. Once past it they have already been counted into their hour.
	Attempts time.Duration
	// Hour rows older than this become days, days older than Day become
	// weeks, weeks older than Week become months. Months are kept.
	Hour time.Duration
	Day  time.Duration
	Week time.Duration
}

// DefaultRetention is the ladder as the design fixed it: a week of attempts and
// hours, a month of days, six months of weeks, months forever.
func DefaultRetention() Retention {
	const day = 24 * time.Hour
	return Retention{
		Attempts: 7 * day,
		Hour:     7 * day,
		Day:      30 * day,
		Week:     180 * day,
	}
}

// Measurement is one attempt, successful or not.
type Measurement struct {
	At             time.Time
	RunID          string
	StepID         string
	Capability     string
	Implementation string
	Provider       string
	Repository     string
	// ToolVersion is what the far side said it is, empty when it would not say.
	ToolVersion string
	Spent       contract.Sample
	OK          bool
	// FailureKind is the shared bin and Failure the untranslated reason. Both
	// empty on success.
	FailureKind string
	Failure     string
	// Raw is the provider's own text behind Failure, kept verbatim so a
	// human can search for it later instead of re-triggering the same
	// failure just to see what it actually said. Empty on success, and empty
	// on a failure the core raised itself with nothing to quote.
	Raw string
	// OutOfScope is how many results this attempt returned that fell outside
	// the requested scope and were dropped before the answer came back.
	//
	// It is recorded and never scored. Health answers "can this provider
	// answer at all", and a provider that wanders still answers -- the core
	// filters the strays and the caller gets a clean result, so demoting it
	// would drop a working provider over a defect already neutralized. What
	// wandering costs is tokens and time on results nobody could use, which
	// is a cost fact, and cost is the funnel stage that chooses between
	// providers that all work. This column is the evidence that stage will
	// need, gathered from the first call rather than from the day somebody
	// decides to wire it.
	OutOfScope int
}

// DefaultPath is where the database lives when the settings file says nothing.
// It sits beside the run receipts, under the same state root, because both are
// the same kind of thing: what Atenea remembers about work it has done.
func DefaultPath() string { return filepath.Join(platform.StateDir(), "metrics.duckdb") }

// Options configure a store. The zero value is usable: it means the defaults.
type Options struct {
	BufferLimit int
	LockWait    time.Duration
	Retention   Retention
}

// Store buffers measurements and writes them in batches.
type Store struct {
	path      string
	limit     int
	lockWait  time.Duration
	retention Retention

	// flushMu serializes whole flushes, buffer drain and disk write together.
	// Without it a Flush that arrived while another was mid-write found the
	// buffer already drained and returned nil, telling its caller the rows
	// were durable while they were still in the other goroutine's batch. It is
	// taken before mu, never after it.
	flushMu sync.Mutex
	mu      sync.Mutex
	// buf is a ring once it reaches limit, which is what keeps Record O(1)
	// there. Discarding the oldest by re-slicing copied the whole buffer on
	// every call past the ceiling: measured at 74us per Record with the
	// default limit of 10000, against roughly 40ns before the buffer filled.
	// A ring moves head instead of the rows.
	//
	// head is the oldest live entry and count how many are live. head only
	// ever leaves zero once len(buf) has reached limit, so the growth path
	// below can keep using append.
	buf     []Measurement
	head    int
	count   int
	dropped int
	// sealed stops the buffer accepting work after the final flush. See Seal.
	sealed  bool
	written int
}

// Open prepares the database at path and returns a store with an empty buffer.
//
// The file is migrated and released again immediately: the store does not hold
// it between flushes.
func Open(path string, opts Options) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, contract.Fail(contract.FailureInvalidInput, "metrics: no database path")
	}
	s := &Store{
		path:      path,
		limit:     opts.BufferLimit,
		lockWait:  opts.LockWait,
		retention: opts.Retention,
	}
	if s.limit <= 0 {
		s.limit = DefaultBufferLimit
	}
	if s.lockWait <= 0 {
		s.lockWait = DefaultLockWait
	}
	if s.retention == (Retention{}) {
		s.retention = DefaultRetention()
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := ensureDir(dir); err != nil {
			return nil, err
		}
	}
	db, err := s.connect(context.Background())
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	if err := migrate(context.Background(), db); err != nil {
		return nil, err
	}
	return s, nil
}

// Path is where the database lives.
func (s *Store) Path() string { return s.path }

// Record buffers one attempt. It never blocks on disk and never fails: the
// caller is on the hot path of real work and a measurement is not worth
// stalling it for.
func (s *Store) Record(m Measurement) {
	if m.At.IsZero() {
		m.At = time.Now()
	}
	// Provider output is useful while the call is alive, but metrics are
	// durable. Keep the diagnostic shape while removing common credentials and
	// bounding the retained text.
	m.Raw = contract.RedactRaw(m.Raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		// The last flush has already happened. Buffering here would put the
		// measurement in a queue nothing will ever drain, and it would leave
		// no trace of having done so -- the one shape this package refuses,
		// because a measurement that vanishes silently is worse than one
		// counted as lost.
		s.dropped++
		return
	}
	s.push(m)
}

// Seal closes the store to new measurements, counting anything that arrives
// afterwards as dropped.
//
// It exists for the shutdown that overran. Work still in flight goes on calling
// Record after the final flush, and without this those rows sat in a buffer
// nobody would ever write: gone with the process and absent from Dropped, so
// the count that exists to say how much was lost said zero. Sealing before the
// last flush rather than after is deliberate -- a measurement arriving during
// the last stand is one the last stand was never going to carry.
//
// Idempotent, and it does not flush: the caller decides how hard to try.
func (s *Store) Seal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealed = true
}

// push adds one measurement to the ring, dropping the oldest when the ring is
// already at its ceiling. Every branch is constant time; that is the whole
// point of the shape.
//
// The caller holds mu.
func (s *Store) push(m Measurement) {
	if s.count < len(s.buf) {
		s.buf[(s.head+s.count)%len(s.buf)] = m
		s.count++
		return
	}
	if len(s.buf) < s.limit {
		// The ring has not reached its ceiling yet, so it grows rather than
		// dropping anything. append can only put the new entry at the end,
		// which is where the newest one belongs precisely because head is
		// still zero: it moves only on the branch below, and that branch is
		// reachable only once len(buf) is limit.
		s.buf = append(s.buf, m)
		s.count++
		return
	}
	// The oldest goes first: recent measurements are the ones the selector is
	// about to ask about.
	s.buf[s.head] = m
	s.head = (s.head + 1) % len(s.buf)
	s.dropped++
}

// drain copies the live entries out in order and empties the ring.
//
// It copies rather than handing the backing array over, because the ring keeps
// its array and would otherwise write new measurements over rows a flush still
// has in flight. The copy costs one pass over a batch that is about to be
// written to disk, which is not where the time goes.
//
// The caller holds mu.
func (s *Store) drain() []Measurement {
	if s.count == 0 {
		return nil
	}
	batch := make([]Measurement, s.count)
	for i := range batch {
		batch[i] = s.buf[(s.head+i)%len(s.buf)]
	}
	// Length back to zero, capacity kept: the next fill reuses the same array
	// through the growth branch of push, with head at zero again.
	s.buf = s.buf[:0]
	s.head = 0
	s.count = 0
	return batch
}

// restore puts a failed batch back in front of whatever arrived while it was
// being written, so the next flush carries the rows in the order they
// happened. That is the difference between a slow store and a lossy one.
//
// The caller holds mu.
func (s *Store) restore(batch []Measurement) {
	all := append(batch, s.drain()...)
	if over := len(all) - s.limit; over > 0 {
		all = all[over:]
		s.dropped += over
	}
	s.buf = all
	s.head = 0
	s.count = len(all)
}

// Pending is how many measurements are waiting to be written.
func (s *Store) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// Dropped is how many measurements were lost to a full buffer, ever.
func (s *Store) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Written is how many measurements have reached the disk, ever.
func (s *Store) Written() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written
}

const insertMeasurement = `INSERT INTO measurement
	(happened_at, run_id, step_id, capability, implementation, provider,
	 repository, tool_version, duration_us, tokens, peak_rss_bytes,
	 ok, failure_kind, failure, raw, out_of_scope)
	VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// Flush writes the buffered batch. An empty buffer is not an error and does not
// touch the disk.
//
// If the write fails the rows go back where they were, in order, so the next
// attempt carries them. That is the difference between a slow store and a lossy
// one.
//
// A flush that finds another one already running waits for it instead of
// returning. Returning was the bug: every caller here flushes in order to read
// what it just recorded -- Baselines, Health, Compact, Close all do -- and a
// nil that meant "somebody else took my rows and has not written them yet"
// sent them on to query a database missing exactly those rows.
func (s *Store) Flush(ctx context.Context) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()
	batch := s.drain()
	s.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}

	err := s.write(ctx, batch)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.restore(batch)
		return err
	}
	s.written += len(batch)
	return nil
}

func (s *Store) write(ctx context.Context, batch []Measurement) error {
	db, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("metrics: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, insertMeasurement)
	if err != nil {
		return fmt.Errorf("metrics: prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, m := range batch {
		var rss any
		if m.Spent.PeakRSS > 0 {
			rss = m.Spent.PeakRSS
		}
		_, err := stmt.ExecContext(ctx,
			m.At.UTC(), m.RunID, m.StepID, m.Capability, m.Implementation,
			m.Provider, m.Repository, m.ToolVersion,
			m.Spent.Duration.Microseconds(), int64(m.Spent.Tokens), rss,
			m.OK, m.FailureKind, m.Failure, m.Raw, int64(m.OutOfScope))
		if err != nil {
			return fmt.Errorf("metrics: insert: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("metrics: commit: %w", err)
	}
	return nil
}

// Close flushes whatever is left. It is the last of the two safety nets the
// batching design asks for, the other being the end of a phase.
func (s *Store) Close() error { return s.Flush(context.Background()) }

// connect opens the file, waiting out another process's flush if one is in
// progress. DuckDB allows a single writer, which is the shape the design wanted
// anyway; the wait is what turns that from a crash into a queue.
//
// Nothing serializes this inside the process, on purpose. The exclusive lock
// DuckDB takes is held by the process, not by the handle: two connections to
// the same file from one Atenea share its instance and both answer, which is
// what lets several goroutines of a runWave read baselines at once. The retry
// loop below is for the other Atenea on the machine, and a mutex here could
// not have helped with that one.
func (s *Store) connect(ctx context.Context) (*sql.DB, error) {
	deadline := time.Now().Add(s.lockWait)
	backoff := 5 * time.Millisecond
	for {
		db, err := sql.Open("duckdb", s.path)
		if err == nil {
			// One connection, because there is one writer. It also keeps the
			// driver from opening a second handle onto a file it already
			// holds.
			db.SetMaxOpenConns(1)
			if err = db.PingContext(ctx); err == nil {
				return db, nil
			}
			_ = db.Close()
		}
		if !isLocked(err) || time.Now().After(deadline) {
			return nil, fmt.Errorf("metrics: open %s: %w", s.path, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 200*time.Millisecond {
			backoff *= 2
		}
	}
}

// isLocked recognizes the one error worth waiting on. The driver reports it as
// text, so this reads text; every other failure is returned as it is rather
// than retried into a timeout.
func isLocked(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Conflicting lock") ||
		strings.Contains(msg, "lock on file")
}

func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return contract.Fail(contract.FailureInvalidInput,
			"metrics: cannot create %s: %v", dir, err)
	}
	return nil
}

// migrate applies every numbered step the database has not seen, in order.
//
// The number is the whole ordering: files are named NNNN_name.sql and applied
// ascending, each in its own transaction, and the ledger row lands in the same
// transaction as the change it describes. A half-applied step cannot be
// recorded as done.
func migrate(ctx context.Context, db *sql.DB) error {
	const ledger = `CREATE TABLE IF NOT EXISTS schema_migration (
		version    INTEGER   NOT NULL PRIMARY KEY,
		name       VARCHAR   NOT NULL,
		applied_at TIMESTAMP NOT NULL)`
	if _, err := db.ExecContext(ctx, ledger); err != nil {
		return fmt.Errorf("metrics: migration ledger: %w", err)
	}
	steps, err := migrations()
	if err != nil {
		return err
	}
	for _, step := range steps {
		var seen int
		row := db.QueryRowContext(ctx,
			"SELECT count(*) FROM schema_migration WHERE version = ?", step.version)
		if err := row.Scan(&seen); err != nil {
			return fmt.Errorf("metrics: migration ledger: %w", err)
		}
		if seen > 0 {
			continue
		}
		if err := apply(ctx, db, step); err != nil {
			return err
		}
	}
	return nil
}

type migration struct {
	version int
	name    string
	body    string
}

func apply(ctx context.Context, db *sql.DB, step migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("metrics: migration %d: %w", step.version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, step.body); err != nil {
		return fmt.Errorf("metrics: migration %d %s: %w", step.version, step.name, err)
	}
	_, err = tx.ExecContext(ctx,
		"INSERT INTO schema_migration (version, name, applied_at) VALUES (?,?,?)",
		step.version, step.name, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("metrics: migration %d ledger: %w", step.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("metrics: migration %d commit: %w", step.version, err)
	}
	return nil
}

// migrations reads the embedded steps and refuses anything it cannot order. A
// file that does not announce its place in the sequence is a mistake caught
// here rather than a step that silently never runs.
func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("metrics: migrations: %w", err)
	}
	out := make([]migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, rest, found := strings.Cut(entry.Name(), "_")
		version, convErr := strconv.Atoi(prefix)
		if !found || convErr != nil || version <= 0 {
			return nil, fmt.Errorf(
				"metrics: migration %q must be named NNNN_name.sql", entry.Name())
		}
		if clash, dup := seen[version]; dup {
			return nil, fmt.Errorf(
				"metrics: migrations %q and %q share number %d", clash, entry.Name(), version)
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("metrics: migration %q: %w", entry.Name(), err)
		}
		seen[version] = entry.Name()
		out = append(out, migration{
			version: version,
			name:    strings.TrimSuffix(rest, ".sql"),
			body:    string(body),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
