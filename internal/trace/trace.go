// Package trace is the record of which agents ran, when, and how they ended.
//
// It is written by Atenea and by nothing else. An agent that filed its own
// trace could file a clean one and exit, or die before filing anything at
// all, and the row a reader most needs -- the one for the run that went wrong
// -- is exactly the row that would be missing. So the writer is the process
// that outlives the agent.
//
// # Two writes, and the gap between them is the point
//
// A row is opened before the process starts and closed after its answer has
// been validated. Nothing the agent does touches it. That gap is not an
// implementation detail: it is how "it died" is detected at all. Atenea does
// not have to notice a crash, a kill, a machine losing power, or its own
// death -- any of them simply leaves a row with no ending, which the next
// start finds and closes.
//
// An orphan is closed as INCOMPLETE, never as failed. Failed is a claim about
// the work, and nobody watched this work: it may have read every file it was
// asked for and died on the way to saying so. Recording that as failure would
// be inventing evidence.
//
// # Metadata only
//
// Who, when, and how it ended. No results, no payloads, no file contents. A
// trace is read while something is wrong, which is the worst moment to be
// paging through megabytes -- and a store that keeps answers is a store that
// grows without bound and leaks whatever the agent happened to read.
//
// # Discovered facts, filed on the row that earned them
//
// One exception to "no payloads": Discovered rides on the row it closed
// with. It is not the work's result -- pkg/contract already caps one note at
// MaxDiscoveryLength characters, so what a row can carry here is small and
// fixed, never the megabytes the rest of this comment is written against.
// It belongs here because the row IS the fact's home: the next dispatch of
// the same agent type reads the trace back rather than pay to learn it
// again, and a second table for the same handful of sentences would just be
// a second place for them to drift from this one.
package trace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// FileName is the database, one per machine.
const FileName = "traces.db"

// DefaultPath is where the trace database lives.
func DefaultPath() string { return filepath.Join(platform.StateDir(), FileName) }

// Row is one agent execution as the store holds it.
type Row struct {
	ID       string
	ParentID string
	TypeName string
	Kind     contract.AgentType
	// Objective is the one sentence the agent was asked, kept because a
	// trace nobody can tell apart from the next one answers nothing. It is
	// the only free text on the row and it is what was ASKED, never what
	// came back.
	Objective string
	Depth     int
	StartedAt time.Time
	// EndedAt is zero while the row is open.
	EndedAt time.Time
	Verdict contract.Verdict
	Reason  contract.Reason
	// Discovered is what this run reported finding when it closed. Empty on
	// a row still open and on one that reported nothing. It travels with the
	// row rather than a table of its own because the row is the only reason
	// to keep it at all: the next dispatch of this agent type reads the
	// trace back so the fact is paid for once, not on every run that could
	// have used it.
	Discovered []contract.Discovery
	// WriterPID is the Atenea that opened the row. Zero on a row written
	// before this column existed, which reads as "not alive" and so is
	// sweepable -- the right answer for a row that old.
	WriterPID int
	// Swept marks a row closed by the orphan sweep rather than by the run
	// itself. Both read `incomplete`, and a reader has to be able to tell
	// "the agent said it stopped short" from "nobody ever heard back".
	Swept bool
	// Attempt counts this run within its retry chain, starting at 1. It is
	// materialized rather than derived so a listing does not need a
	// recursive query, and it is computed from the row RetryOf names, never
	// typed by a caller.
	Attempt int
	// RetryOf is the run this one redoes, empty on a first attempt. A
	// relaunch is a second real process with its own cost and its own
	// answer, so it is a second row -- folding it into the first would make
	// the trace understate what the machine actually ran. This is the link
	// that keeps the two from reading as unrelated runs of the same type.
	RetryOf string
	// Reviews is the run this one audits, empty when it is not a review. A
	// reviewer is dispatched by whoever dispatched the work, not by the work
	// itself, so it is not a child of what it judges: ParentID would be the
	// wrong word for it and this is the right one.
	Reviews string
}

// Open reports whether this row is still waiting for its ending.
func (r Row) Open() bool { return r.EndedAt.IsZero() }

// Duration is how long the run took, or zero while it is still open.
func (r Row) Duration() time.Duration {
	if r.Open() {
		return 0
	}
	return r.EndedAt.Sub(r.StartedAt)
}

// Store is the trace database.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if needed) the database at path and migrates it.
//
// WAL, because the reader is a person running `atenea traces` while an agent
// is mid-run, and the default journal makes that reader wait on a writer or
// the writer wait on the reader. A busy timeout on top: two Ateneas starting
// at once is ordinary on a machine where this is a CLI as often as a service.
// Immediate transactions reserve their write turn before reading because the
// workflow store uses this same file and a deferred lock upgrade can fail busy.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"trace: cannot create %s: %v", filepath.Dir(path), err)
	}
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_txlock=immediate" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"trace: open %s: %v", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, contract.Fail(contract.FailureUnavailable,
			"trace: open %s: %v", path, err)
	}
	store := &Store{db: db, path: path}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Path is the file this store is backed by.
func (s *Store) Path() string { return s.path }

// Close releases the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

const schema = `
CREATE TABLE IF NOT EXISTS agent_trace (
    id          TEXT    NOT NULL PRIMARY KEY,
    parent_id   TEXT    NOT NULL,
    type_name   TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    objective   TEXT    NOT NULL,
    depth       INTEGER NOT NULL,
    started_at  TEXT    NOT NULL,
    -- The Atenea that opened this row. The sweep asks whether it is still
    -- alive before closing anything: another Atenea may be mid-run right
    -- now, and closing a live run as incomplete would be the sweep inventing
    -- the very thing it exists to record honestly.
    writer_pid  INTEGER NOT NULL DEFAULT 0,
    -- NULL is the whole mechanism: a row with no ending is a run nobody saw
    -- finish, whether the agent died, the machine did, or Atenea did.
    ended_at    TEXT,
    verdict     TEXT,
    reason_kind TEXT    NOT NULL DEFAULT '',
    reason_text TEXT    NOT NULL DEFAULT '',
    -- What this run reported finding, as a JSON array. NULL exactly when
    -- ended_at is NULL: a discovery is part of the answer, and there is no
    -- answer before the row closes.
    discovered  TEXT,
    -- A relaunch is a second process, so it is a second row. attempt counts
    -- it within its chain and retry_of names the exact row it redoes: two
    -- runs of one type an hour apart and two attempts at one piece of work
    -- must not read the same.
    attempt     INTEGER NOT NULL DEFAULT 1,
    retry_of    TEXT    NOT NULL DEFAULT '',
    -- The run this one audits. A review is about another run, not under it.
    reviews     TEXT    NOT NULL DEFAULT '',
    swept       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS agent_trace_open ON agent_trace(ended_at);
CREATE INDEX IF NOT EXISTS agent_trace_started ON agent_trace(started_at);
CREATE INDEX IF NOT EXISTS agent_trace_type ON agent_trace(type_name);
CREATE INDEX IF NOT EXISTS agent_trace_retry ON agent_trace(retry_of);
CREATE INDEX IF NOT EXISTS agent_trace_reviews ON agent_trace(reviews);
-- The mark for the retention pass, in the same database as the rows it
-- removes. On disk rather than on a beat, and read in the same transaction as
-- the delete, for the reason metrics.CompactIfDue gives: most Atenea processes
-- are a command that lives for a second and has no rhythm of its own, and two
-- of them starting together must not both decide they are the one to do it.
CREATE TABLE IF NOT EXISTS maintenance (
    job     TEXT NOT NULL PRIMARY KEY,
    last_at TEXT NOT NULL
);
`

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return contract.Fail(contract.FailureUnavailable, "trace: schema: %v", err)
	}
	return nil
}

// Begin opens a row for a run that is about to start.
//
// Call it BEFORE spawning. A row written after the process starts would miss
// exactly the runs worth tracing: the ones that died in their first second.
//
// WriterPID left at zero means this process, which is the only honest answer
// for a normal run: whoever opens the row is the one whose disappearance
// makes it an orphan. It is settable so a test can stand a row up behind a
// pid it controls -- the sweep's liveness gate cannot be exercised any other
// way without killing the test binary.
func (s *Store) Begin(ctx context.Context, row Row) error {
	if strings.TrimSpace(row.ID) == "" {
		return contract.Fail(contract.FailureInvalidInput, "trace: id is required")
	}
	if row.StartedAt.IsZero() {
		return contract.Fail(contract.FailureInvalidInput,
			"trace %s: started_at is required", row.ID)
	}
	writer := row.WriterPID
	if writer == 0 {
		writer = os.Getpid()
	}
	attempt := row.Attempt
	if attempt < 1 {
		attempt = 1
	}
	if row.RetryOf == row.ID && row.RetryOf != "" {
		return contract.Fail(contract.FailureInvalidInput,
			"trace %s: is its own retry", row.ID)
	}
	if row.Reviews == row.ID && row.Reviews != "" {
		return contract.Fail(contract.FailureInvalidInput,
			"trace %s: reviews itself", row.ID)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_trace
		   (id, parent_id, type_name, kind, objective, depth, started_at,
		    writer_pid, attempt, retry_of, reviews)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.ParentID, row.TypeName, row.Kind.String(),
		row.Objective, row.Depth, stamp(row.StartedAt), writer,
		attempt, row.RetryOf, row.Reviews)
	if err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"trace: opening row %s: %v", row.ID, err)
	}
	return nil
}

// Complete closes a row with the verdict the run reached, and records what
// it reported finding along the way.
//
// It refuses to close a row twice. A second close would mean two things
// claimed to be the end of one run, and the store cannot know which is the
// truth -- so it keeps the first and says so.
func (s *Store) Complete(ctx context.Context, id string, at time.Time,
	verdict contract.Verdict, reason contract.Reason, discovered []contract.Discovery) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_trace
		    SET ended_at = ?, verdict = ?, reason_kind = ?, reason_text = ?,
		        discovered = ?, swept = 0
		  WHERE id = ? AND ended_at IS NULL`,
		stamp(at), verdict.String(), reasonKind(reason), reason.Text,
		encodeDiscovered(discovered), id)
	if err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"trace: closing row %s: %v", id, err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"trace: closing row %s: %v", id, err)
	}
	if changed == 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"trace: row %s is not open", id)
	}
	return nil
}

// SweepOrphans closes every row left open by a run nobody saw finish, and
// returns how many it closed.
//
// Called at start, before anything new is dispatched. Incomplete, never
// failed: an orphan is the absence of a judgement, not a bad one. The reason
// bin is `unavailable`, which is the one thing actually known -- whatever was
// going to answer is not there.
//
// A row whose writer is still alive is left alone. On this machine Atenea is
// a CLI as often as a service, so a second Atenea starting while the first is
// mid-run is ordinary, and a sweep that closed those rows would manufacture
// incompletes for agents that were working perfectly. The check is per row
// and it errs toward leaving a row open: an open row is visible and can be
// swept later, while a wrongly closed one is a lie already written down.
func (s *Store) SweepOrphans(ctx context.Context, at time.Time) (int, error) {
	rows, err := s.List(ctx, Filter{OpenOnly: true, Oldest: true, Limit: sweepCeiling})
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, row := range rows {
		if pidlock.Alive(row.WriterPID) {
			continue
		}
		res, err := s.db.ExecContext(ctx,
			`UPDATE agent_trace
			    SET ended_at = ?, verdict = ?, reason_kind = ?, reason_text = ?, swept = 1
			  WHERE id = ? AND ended_at IS NULL`,
			stamp(at), contract.VerdictIncomplete.String(),
			contract.FailureUnavailable.String(),
			"the agent never reported back; closed by the sweep at "+stamp(at),
			row.ID)
		if err != nil {
			return closed, contract.Fail(contract.FailureUnavailable,
				"trace: sweep %s: %v", row.ID, err)
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			closed++
		}
	}
	return closed, nil
}

// sweepCeiling bounds one sweep. A machine that somehow accumulated more open
// rows than this closes the oldest batch now and the rest on the next start,
// which is better than one start doing unbounded work before it answers.
//
// Which end the ceiling cuts is the whole point, and the sweep asks for the
// oldest first to get it. A listing is newest-first for every reader that is a
// person, so the sweep inherited that order and the ceiling took the newest
// rows -- meaning the oldest orphans, the ones whose writer has most certainly
// been gone the longest, were the ones left open, start after start, while
// each start re-examined rows from runs that had barely finished.
const sweepCeiling = 10_000

// Filter narrows a listing. Every field is optional and they compose.
type Filter struct {
	ID       string
	TypeName string
	// Verdict selects one verdict. VerdictUnspecified means any.
	Verdict contract.Verdict
	// OpenOnly keeps only rows still waiting for an ending.
	OpenOnly bool
	// Since keeps rows started at or after this instant.
	Since time.Time
	// RetryOf keeps only the runs redoing this one.
	RetryOf string
	// Reviews keeps only the runs auditing this one.
	Reviews string
	// Limit caps the result, newest first. Zero means DefaultLimit.
	Limit int
	// Oldest reverses the order, so a Limit that has to cut something cuts
	// the newest rows instead of the oldest. Only the sweep asks for this:
	// every reader wants the newest first.
	Oldest bool
}

// Checkpoint folds the write-ahead log into the database file.
//
// This store runs in WAL mode, so a copy of traces.db taken on its own is a
// copy of whatever had been folded in by then -- and the -wal beside it is a
// separate file the copier may or may not have reached at the same instant.
// TRUNCATE rather than PASSIVE: a passive checkpoint gives up when a reader
// is in the way, and giving up quietly is what leaves the copy inconsistent.
func (s *Store) Checkpoint(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return contract.Fail(contract.FailureUnavailable, "trace: checkpoint: %v", err)
	}
	return nil
}

// PruneIfDue removes closed traces older than keep, at most once per every,
// and reports how many rows went.
//
// Closed only, and deliberately. An open row is a run nobody saw finish, which
// is the one kind of trace worth keeping past its age: SweepOrphans exists to
// turn those into closed ones with a reason, and deleting them first would
// remove exactly the evidence that something died. A row is old by when it
// ENDED, not when it started, so a long run is measured from the point it
// stopped being interesting.
//
// keep of zero means keep everything: a machine whose state root is managed
// elsewhere has no use for this, and an absent policy must not be a silent one.
func (s *Store) PruneIfDue(ctx context.Context, now time.Time, keep, every time.Duration) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	if every <= 0 {
		return 0, contract.Fail(contract.FailureInvalidInput,
			"trace: retention interval must be above 0, got %s", every)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, contract.Fail(contract.FailureUnavailable, "trace: prune begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	utc := now.UTC()
	var last string
	switch err := tx.QueryRowContext(ctx,
		`SELECT last_at FROM maintenance WHERE job = 'retention'`).Scan(&last); {
	case errors.Is(err, sql.ErrNoRows):
		// Never run here. The first pass is due, which is what makes a fresh
		// install tidy itself rather than wait a day to start.
	case err != nil:
		return 0, contract.Fail(contract.FailureUnavailable, "trace: prune mark: %v", err)
	default:
		when, parseErr := time.Parse(time.RFC3339Nano, last)
		// A mark this store cannot read is treated as due rather than as a
		// reason to stop: the alternative is a corrupt timestamp switching
		// retention off for good, silently.
		if parseErr == nil && utc.Sub(when) < every {
			return 0, nil
		}
	}

	cutoff := utc.Add(-keep).Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx,
		`DELETE FROM agent_trace WHERE ended_at IS NOT NULL AND ended_at < ?`, cutoff)
	if err != nil {
		return 0, contract.Fail(contract.FailureUnavailable, "trace: prune: %v", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, contract.Fail(contract.FailureUnavailable, "trace: prune count: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO maintenance (job, last_at) VALUES ('retention', ?)
		 ON CONFLICT(job) DO UPDATE SET last_at = excluded.last_at`,
		utc.Format(time.RFC3339Nano)); err != nil {
		return 0, contract.Fail(contract.FailureUnavailable, "trace: prune mark: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, contract.Fail(contract.FailureUnavailable, "trace: prune commit: %v", err)
	}
	return int(removed), nil
}

// DefaultLimit is how many rows a listing returns when nobody says.
const DefaultLimit = 50

// List returns matching rows, newest first.
func (s *Store) List(ctx context.Context, f Filter) ([]Row, error) {
	where := make([]string, 0, 5)
	args := make([]any, 0, 5)
	if f.ID != "" {
		where = append(where, "id = ?")
		args = append(args, f.ID)
	}
	if f.TypeName != "" {
		where = append(where, "type_name = ?")
		args = append(args, f.TypeName)
	}
	if f.Verdict != contract.VerdictUnspecified {
		where = append(where, "verdict = ?")
		args = append(args, f.Verdict.String())
	}
	if f.OpenOnly {
		where = append(where, "ended_at IS NULL")
	}
	if !f.Since.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, stamp(f.Since))
	}
	if f.RetryOf != "" {
		where = append(where, "retry_of = ?")
		args = append(args, f.RetryOf)
	}
	if f.Reviews != "" {
		where = append(where, "reviews = ?")
		args = append(args, f.Reviews)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	query := `SELECT id, parent_id, type_name, kind, objective, depth,
	                 started_at, ended_at, verdict, reason_kind, reason_text,
	                 discovered, swept, writer_pid, attempt, retry_of, reviews
	            FROM agent_trace`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	if f.Oldest {
		query += " ORDER BY started_at ASC, id ASC LIMIT ?"
	} else {
		query += " ORDER BY started_at DESC, id DESC LIMIT ?"
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, contract.Fail(contract.FailureUnavailable, "trace: list: %v", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Row, 0, limit)
	for rows.Next() {
		row, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, contract.Fail(contract.FailureUnavailable, "trace: list: %v", err)
	}
	return out, nil
}

func scan(rows *sql.Rows) (Row, error) {
	var (
		row            Row
		kind           string
		startedAt      string
		endedAt        sql.NullString
		verdictName    sql.NullString
		reasonKindName string
		discovered     sql.NullString
		swept          int
	)
	if err := rows.Scan(&row.ID, &row.ParentID, &row.TypeName, &kind, &row.Objective,
		&row.Depth, &startedAt, &endedAt, &verdictName, &reasonKindName,
		&row.Reason.Text, &discovered, &swept, &row.WriterPID,
		&row.Attempt, &row.RetryOf, &row.Reviews); err != nil {
		return Row{}, contract.Fail(contract.FailureUnavailable, "trace: list: %v", err)
	}
	if parsed, err := contract.ParseAgentType(kind); err == nil {
		row.Kind = parsed
	}
	row.StartedAt = parseStamp(startedAt)
	if endedAt.Valid {
		row.EndedAt = parseStamp(endedAt.String)
	}
	if verdictName.Valid {
		row.Verdict = parseVerdictName(verdictName.String)
	}
	row.Reason.Kind = parseFailureName(reasonKindName)
	if discovered.Valid {
		row.Discovered = parseDiscovered(discovered.String)
	}
	row.Swept = swept != 0
	return row, nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseStamp(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// reasonKind renders the bin, or the empty string when there is no reason at
// all -- which is the ordinary shape of a plain ok.
func reasonKind(r contract.Reason) string {
	if r.Empty() {
		return ""
	}
	return r.Kind.String()
}

// parseKind reads a verdict or failure name back, tolerating a row written by
// a newer Atenea than the one reading it.
func parseVerdictName(s string) contract.Verdict {
	if s == "" {
		return contract.VerdictUnspecified
	}
	v, err := contract.ParseVerdict(s)
	if err != nil {
		return contract.VerdictUnspecified
	}
	return v
}

func parseFailureName(s string) contract.FailureKind {
	kind, err := contract.ParseFailureKind(s)
	if err != nil {
		return contract.FailureUnspecified
	}
	return kind
}

// discoveredWire is the JSON shape one discovery takes in the column: the
// same {level, note} pair pkg/contract validates, so a row this store wrote
// and a row read back need only agree on those two names.
type discoveredWire struct {
	Level string `json:"level"`
	Note  string `json:"note"`
}

// encodeDiscovered renders what a run reported as one JSON array, or nil --
// which the driver writes as SQL NULL -- when it reported nothing. An open
// row and a closed row that discovered nothing have to read back the same
// way, and NULL is the value that already means "nothing here" everywhere
// else in this table.
func encodeDiscovered(found []contract.Discovery) any {
	if len(found) == 0 {
		return nil
	}
	wire := make([]discoveredWire, len(found))
	for i, d := range found {
		wire[i] = discoveredWire{Level: d.Level.String(), Note: d.Note}
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		// Never happens for a slice of two plain strings. A row closed
		// without its discoveries is still an honest ending; refusing the
		// close over a field this store could not even read back would not
		// be.
		return nil
	}
	return string(raw)
}

// parseDiscovered reads discoveries back, tolerating a row a newer Atenea
// wrote: an entry naming a level this binary does not know is dropped
// rather than failing the whole row, the same tolerance parseVerdictName and
// parseFailureName give the columns next to it.
func parseDiscovered(raw string) []contract.Discovery {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var wire []discoveredWire
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return nil
	}
	out := make([]contract.Discovery, 0, len(wire))
	for _, d := range wire {
		level, err := contract.ParseContextLevel(d.Level)
		if err != nil {
			continue
		}
		out = append(out, contract.Discovery{Level: level, Note: d.Note})
	}
	return out
}
