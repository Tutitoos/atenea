package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"

	_ "modernc.org/sqlite" // database/sql driver
)

// Status is where a step stands. Only these six are written down; everything
// else a reader wants to know is worked out from them and the graph.
type Status uint8

// The statuses a step can hold on disk.
const (
	// StatusPending is declared and not started. It is also where a step
	// goes back to when a resume decides to redo it.
	StatusPending Status = iota
	// StatusRunning has a live process behind it -- or had one, until the
	// Atenea that owned it died. Which of the two is a question about a pid,
	// not about this column, and it is why WriterPID sits beside it.
	StatusRunning
	// StatusOK is the agent's own ok.
	StatusOK
	// StatusFailed is the agent reaching a judgement and the judgement being
	// no.
	StatusFailed
	// StatusIncomplete is the agent stopping short, or dying, and saying so
	// through its report.
	StatusIncomplete
	// StatusInterrupted is a step nobody judged: it was running when the
	// operator cut it or when Atenea died, and no report was ever read.
	//
	// It is its own state rather than a flavor of failed because the two are
	// different facts, and only one of them is about the agent. A step that
	// did badly has been measured; a step nobody watched has not, and calling
	// it failed would put a judgement on the record that nothing supports --
	// the same distinction `incomplete` already draws for an answer that
	// stopped short.
	StatusInterrupted
)

var statusNames = map[Status]string{
	StatusPending:     "pending",
	StatusRunning:     "running",
	StatusOK:          "ok",
	StatusFailed:      "failed",
	StatusIncomplete:  "incomplete",
	StatusInterrupted: "interrupted",
}

func (s Status) String() string {
	if name, ok := statusNames[s]; ok {
		return name
	}
	return "status(" + itoa(int(s)) + ")"
}

// Done reports whether this step is finished, whatever it finished as. An
// interrupted step is NOT done: nobody has judged it, and a resume may yet.
func (s Status) Done() bool {
	return s == StatusOK || s == StatusFailed || s == StatusIncomplete
}

// ParseStatus reads a status name back off the record.
func ParseStatus(s string) (Status, error) {
	for status, name := range statusNames {
		if name == s {
			return status, nil
		}
	}
	return StatusPending, contract.Fail(contract.FailureInvalidInput,
		"unknown step status %q", s)
}

// Stop is why a workflow is not running: the difference between somebody
// cutting it and the process dying under it.
type Stop string

// The three ways a workflow stops.
const (
	// StopNone is a workflow still running, or one that finished.
	StopNone Stop = ""
	// StopAborted is deliberate: the operator cut it and Atenea wrote that
	// down on the way out.
	StopAborted Stop = "aborted"
	// StopCrashed is what a resume infers: the record says running, and the
	// process it names is gone. Nobody wrote this; it is the absence of a
	// clean close, which is exactly why it must not read the same as abort.
	StopCrashed Stop = "crashed"
	// StopUnjudged is a run that ran out of things it may do on its own:
	// what is left is steps nobody judged, and repeating those could land an
	// effect twice. It is not finished and it is not aborted -- it is
	// waiting for a person to say --redo, and calling it finished would put
	// a completed run on the record with a hole in the middle of it.
	StopUnjudged Stop = "unjudged"
	// StopRejected is a plan a person read and refused. Nothing ran: it is
	// not aborted, because nobody cut anything, and it is certainly not
	// finished. A run that never got permission to exist should not look
	// like one that did its work.
	StopRejected Stop = "rejected"
)

// Run is one workflow as the record holds it.
type Run struct {
	ID   string
	Task string
	// Repository is the repository this run was created against, as the
	// `--repository` flag named it. It is on the record because funding is
	// keyed on it -- checkFunding prices every step's floor against this
	// id -- and a run that priced one repository must not be executed
	// against another. Empty on rows written before the column existed,
	// and on a run created without the flag; both mean "nothing was
	// recorded", never "the empty repository".
	Repository string
	GrantUSD   float64
	Started    time.Time
	Ended      time.Time
	// Closed is set when the workflow reached its end, whatever the steps
	// did. An open run is one somebody may still resume.
	Closed bool
	Stop   Stop
	// WriterPID is the Atenea that owns this run. It answers the only
	// question a resume must not guess at: whether somebody else is running
	// this right now.
	WriterPID int
	Steps     []StepRow
	// Superseded is every dispatch a later attempt replaced, oldest first.
	//
	// It is on the Run rather than left to a caller who asks for it because
	// the money it carries is the run's money: a redo overwrites the live
	// row, so without this the sum over Steps is smaller than what the grant
	// actually paid for. Measured 2026-08-16 on the first real redo: the
	// report said "$6.70 spent, $2.30 left" of a $9.00 grant while $7.32 had
	// gone -- the archived attempt's $0.62 was invisible to the only line
	// that reports a balance.
	Superseded []AttemptRow
}

// StepRow is one node of the graph as the record holds it: the declaration,
// and whatever has happened to it.
type StepRow struct {
	Step   Step
	Pool   config.Pool
	Status Status
	// TraceID is the agent execution this step ran as, empty until it runs.
	// Ids, not a foreign key: the trace rows already link to each other this
	// way, and a step whose trace was pruned should still read.
	TraceID string
	// Attempt counts dispatches of this step, from 1. A resume that redoes an
	// interrupted step increments it, and the new trace row redoes the old.
	Attempt   int
	WriterPID int
	Started   time.Time
	Ended     time.Time
	Verdict   contract.Verdict
	Reason    contract.Reason
	// Result is the agent's answer, kept because a resumed workflow must be
	// able to report the steps it did NOT re-run. Without it the second half
	// of a resumed run comes back with holes exactly where the work
	// succeeded.
	Result     map[string]any
	Discovered []contract.Discovery
	// Spent is what this step was charged. Its zero value reads as
	// unmeasured -- see [contract.Charge.Measured] -- which is the ordinary
	// case today: the agent report wire carries no money and nothing on
	// this machine can report a charge. A step that did report, even a real
	// zero (a $0.00 turn with a priced_by beside it), is measured, and the
	// two must never collapse into the same "$0.00" on a receipt -- the
	// same lie as list-price cost on subscription traffic.
	Spent contract.Charge
	// Completeness is how much of the objective this step's answer covers,
	// nil when the report made no claim about it. A full ok never sets
	// this -- see [contract.Report.Partial] -- so nil is both "no report
	// yet" and "the report was whole", and a reader wanting the second
	// without the first already has Status for that.
	Completeness *float64
	// StoppedAt is what a partial answer did not reach. Empty on a full
	// answer, and required on any answer whose Completeness is under 1 --
	// see contract.Report.Validate.
	StoppedAt string
}

// CutAtItsCeiling reports whether this step stopped because it ran out of the
// share it was given, rather than because anything went wrong.
//
// Derived, never a stored flag, and it has to be: the provider does not name
// this. `internal/agent/model` files a ceiling stop as FailureUnavailable on
// purpose -- the ceiling was this one call's own, not a refusal by anybody --
// so the reason kind a real outage writes and the reason kind a ceiling death
// writes are the same word. What separates them is the pair of numbers: the
// share, and what the row spent of it.
//
// The band is [ceilingBand], the same 0.98 CostByType already excludes rows
// by, and for the same reason: a turn stopped at its ceiling stops on the turn
// that crossed it, so the figure lands just under or just over, never on it.
// Sharing the constant is the point -- a step this reports true for is exactly
// a step the admission rule refuses to price against, and two thresholds that
// drifted apart would let a row be re-dispatchable and countable at once.
//
// Measured 2026-08-16: over the whole record, 29 rows carry a real charge with
// no tokens recorded, and this predicate selects 29 of 29 -- every one
// `incomplete` with `unavailable` beside it. No prose is read to get there.
func (r StepRow) CutAtItsCeiling() bool {
	if r.Status != StatusIncomplete || r.Spent.USD == nil {
		return false
	}
	share := r.Step.Permission.BudgetUSD
	return share > 0 && *r.Spent.USD >= share*ceilingBand
}

// Truncated reports whether this row was charged real money and kept no token
// count -- so its tokens are missing, not zero.
//
// A turn that used no tokens cannot cost anything, which is what makes the
// pair decidable from the row alone with nothing stored and nothing to
// migrate. Every such row on this machine predates the fix in
// `conversation.charge`, which now takes the larger of the two accumulators
// the CLI keeps rather than preferring the result event a killed turn never
// prints: 29 rows, $9.61, all of them ceiling deaths.
//
// It exists because the alternative reading is worse than useless. Tokens sum
// as zero and the dollars are real, so a reader totalling a run is told those
// steps did almost nothing -- when what actually happened is that they spent
// their whole share and the receipt lost the evidence.
func (r StepRow) Truncated() bool {
	return r.Spent.USD != nil && *r.Spent.USD > 0 && r.Spent.Tokens() == 0
}

// Spend is what a run's steps have been charged, split by whether anything
// could say.
//
// One number blending measured and unmeasured steps is the exact mistake
// this project keeps finding: a run half of whose steps can report a cost
// and half of which cannot has to say so, not launder the silent half into
// the total. MeasuredSteps and UnmeasuredSteps are that split. Tokens sums
// only the steps that measured -- a step that reported nothing contributes
// nothing, not a zero, and folding it in as zero would understate a run
// that may have cost real money nobody could see. USD is nil unless every
// measured step named a price: a partial dollar figure implies a complete
// one, and this project has already found list-price arithmetic on
// subscription traffic to disagree with what was actually billed. PricedBy
// is every distinct source behind USD, because a total blending two
// providers' price lists is not one bill.
type Spend struct {
	MeasuredSteps   int
	UnmeasuredSteps int
	// TruncatedSteps is how many of MeasuredSteps were charged with no token
	// count behind them -- see [StepRow.Truncated]. They are counted as
	// measured because their dollars are real; they are counted again here
	// because Tokens is a lower bound while any of them exist, and a total
	// that cannot say so is the same lie in a different column.
	TruncatedSteps int
	// SupersededAttempts and SupersededUSD are the dispatches a redo replaced
	// and what they cost. Held apart from the step totals on purpose: a
	// superseded attempt is not a step, and folding it into MeasuredSteps
	// would double-count the step it belongs to in every per-step figure that
	// reads this -- CostByType and the admission rule among them. It is money
	// the grant paid all the same, so a balance that omits it is wrong.
	SupersededAttempts int
	SupersededUSD      float64
	Tokens             int
	USD                *float64
	PricedBy           []string
}

// AttemptRow is a dispatch of a step that a later one replaced.
//
// It is deliberately not a StepRow: what a superseded attempt can answer is
// narrower. The objective, the files and the criterion belong to the step and
// are the same on every attempt, so keeping copies would invite a reader to
// diff two things that cannot differ. What does differ, and is the reason
// this type exists, is the pair (GrantUSD, Spent): the share it ran under and
// what it cost before it stopped.
type AttemptRow struct {
	StepID  string
	Attempt int
	TraceID string
	Status  Status
	Verdict contract.Verdict
	Reason  contract.Reason
	// GrantUSD is the share THIS attempt ran under, which is the figure a
	// later attempt may have been given more of.
	GrantUSD float64
	Spent    contract.Charge
	// Completeness is nil when the attempt made no claim -- and it is nil on
	// every attempt cut at its ceiling, because a turn that never answered
	// never reported coverage.
	Completeness *float64
	StoppedAt    string
	Result       map[string]any
	Started      time.Time
	Ended        time.Time
	// Replaced is when Claim superseded this attempt, not when it ended.
	Replaced time.Time
}

// Cut reports whether this attempt stopped without answering, which is what
// makes its Spent a lower bound rather than a measurement. A reader pricing
// work must not average these in; a reader asking what a share failed to buy
// must read nothing else.
func (a AttemptRow) Cut() bool { return a.Status != StatusOK }

// Spend totals what this run's steps were charged. See [Spend] for why a run
// with both measured and unmeasured steps comes back as a split rather than
// one number.
func (r Run) Spend() Spend {
	var out Spend
	var usd float64
	fullyPriced := true
	labels := map[string]bool{}
	for _, step := range r.Steps {
		if !step.Spent.Measured() {
			out.UnmeasuredSteps++
			continue
		}
		out.MeasuredSteps++
		if step.Truncated() {
			out.TruncatedSteps++
		}
		out.Tokens += step.Spent.Tokens()
		if step.Spent.USD == nil {
			fullyPriced = false
			continue
		}
		usd += *step.Spent.USD
		labels[step.Spent.PricedBy] = true
	}
	// The archive contributes dollars and never tokens: an attempt that was
	// cut kept a token record its own receipt already contradicts (measured
	// 2026-08-16: $0.62 against $0.02 of tokens, 30x), so adding those counts
	// to a total would import the error the live rows were fixed of.
	for _, attempt := range r.Superseded {
		if attempt.Spent.USD == nil {
			continue
		}
		out.SupersededAttempts++
		out.SupersededUSD += *attempt.Spent.USD
		labels[attempt.Spent.PricedBy] = true
	}
	if out.MeasuredSteps > 0 && fullyPriced {
		out.USD = &usd
	}
	out.PricedBy = sortedKeys(labels)
	return out
}

// Store is the workflow record.
//
// It lives in the same file as the traces, in tables of its own. The agent
// side of Atenea already keeps its state here, and a workflow step points at
// the trace row it ran as; splitting them would put the two halves of one
// history in two files that can be restored to different days.
type Store struct {
	db   *sql.DB
	path string
}

const schema = `
CREATE TABLE IF NOT EXISTS workflow (
    id          TEXT    NOT NULL PRIMARY KEY,
    task        TEXT    NOT NULL,
    -- Which repository the run was about, resolved the same way every agent
    -- resolves it (WorkspaceFor). Empty on rows written before this column
    -- existed, and that emptiness is load-bearing: a cost read back from
    -- those rows is machine-wide and has to say so rather than claim a scope
    -- it cannot support.
    repository  TEXT    NOT NULL DEFAULT '',
    grant_usd   REAL    NOT NULL DEFAULT 0,
    started_at  TEXT    NOT NULL,
    ended_at    TEXT    NOT NULL DEFAULT '',
    closed      INTEGER NOT NULL DEFAULT 0,
    -- Why it is not running. Empty on a live run and on one that finished;
    -- 'aborted' is written by the process being cut. Nothing ever writes
    -- 'crashed' -- that one is read off a dead pid, because a process that
    -- died had no chance to record anything.
    stop        TEXT    NOT NULL DEFAULT '',
    writer_pid  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS workflow_step (
    workflow_id TEXT    NOT NULL,
    id          TEXT    NOT NULL,
    ordinal     INTEGER NOT NULL,
    type_name   TEXT    NOT NULL,
    pool        TEXT    NOT NULL,
    objective   TEXT    NOT NULL,
    files       TEXT    NOT NULL DEFAULT '[]',
    criterion   TEXT    NOT NULL DEFAULT '',
    needs       TEXT    NOT NULL DEFAULT '[]',
    -- The step whose answer this one is handed, and how much of that outcome
    -- it demanded. Empty subject is the ordinary case: most steps are handed
    -- nothing. The bar is stored with it because a resumed run must apply the
    -- same one the author wrote, not today's default.
    subject     TEXT    NOT NULL DEFAULT '',
    on_outcome  TEXT    NOT NULL DEFAULT 'answered',
    effects     TEXT    NOT NULL DEFAULT '[]',
    route       TEXT    NOT NULL DEFAULT '',
    budget_estimate_usd REAL NOT NULL DEFAULT 0,
    budget_minimum_usd  REAL NOT NULL DEFAULT 0,
    budget_source TEXT NOT NULL DEFAULT '',
    grant_usd   REAL    NOT NULL DEFAULT 0,
    status      TEXT    NOT NULL,
    trace_id    TEXT    NOT NULL DEFAULT '',
    attempt     INTEGER NOT NULL DEFAULT 0,
    writer_pid  INTEGER NOT NULL DEFAULT 0,
    started_at  TEXT    NOT NULL DEFAULT '',
    ended_at    TEXT    NOT NULL DEFAULT '',
    verdict     TEXT    NOT NULL DEFAULT '',
    reason_kind TEXT    NOT NULL DEFAULT '',
    reason_text TEXT    NOT NULL DEFAULT '',
    result      TEXT    NOT NULL DEFAULT '',
    discovered  TEXT    NOT NULL DEFAULT '',
    -- NULL means unmeasured, and it is the only honest value while no agent
    -- can report a charge. NOT NULL DEFAULT 0 here would turn every free run
    -- into a receipt claiming it was weighed and came to nothing.
    spent_usd   REAL,
    -- Same nullability, same reason: a turn nobody could meter must not
    -- collapse into a turn that used none. contract.Charge treats a zero
    -- charge as unmeasured for the identical reason.
    spent_input_tokens       INTEGER,
    spent_output_tokens      INTEGER,
    spent_cache_read_tokens  INTEGER,
    spent_cache_write_tokens INTEGER,
    -- Whose price produced spent_usd. contract.Charge.Validate refuses a
    -- report where the two disagree before it ever reaches here; this
    -- column only has to keep what it was handed.
    priced_by   TEXT    NOT NULL DEFAULT '',
    -- NULL means the report made no claim about coverage -- the ordinary
    -- case, and the only reading a full ok has ever had. Same nullability
    -- as spent_usd, same reason: a step that never measured its own
    -- completeness must not collapse into one that measured a perfect 1.
    completeness REAL,
    -- What a partial answer did not reach. Empty on a full answer;
    -- contract.Report.Validate requires it non-empty whenever completeness
    -- is under 1, so the two columns are read together or not at all.
    stopped_at  TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (workflow_id, id),
    FOREIGN KEY (workflow_id) REFERENCES workflow(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS workflow_step_status ON workflow_step(status);
CREATE INDEX IF NOT EXISTS workflow_open ON workflow(closed);

-- What a step was, on an attempt that has been superseded.
--
-- workflow_step holds exactly one attempt: Claim clears the outcome columns
-- on every re-claim, which is right for the live row and is why a redo does
-- not inherit a charge it did not incur. The cost of that is what this table
-- exists for. Measured 2026-08-16 on wf1786845363956-1: four steps were cut
-- at their ceiling having spent $0.49-$0.62 against a $0.45 share, and what
-- any of them would have cost to FINISH is unanswerable, because a redo would
-- have overwritten the only row that said they were cut at all.
--
-- Append-only, and written by Claim inside the same transaction as the clear,
-- so the archive and the erasure cannot come apart. A first dispatch archives
-- nothing: attempt 0 means the row has never held an outcome.
--
-- grant_usd is kept per attempt because it is the number that can differ
-- between them -- a step cut at $0.62 and re-run at $0.90 is one measurement
-- of what the work costs, and the pair is unreadable if only the last share
-- survives.
CREATE TABLE IF NOT EXISTS workflow_attempt (
    workflow_id TEXT    NOT NULL,
    step_id     TEXT    NOT NULL,
    attempt     INTEGER NOT NULL,
    trace_id    TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL,
    verdict     TEXT    NOT NULL DEFAULT '',
    reason_kind TEXT    NOT NULL DEFAULT '',
    reason_text TEXT    NOT NULL DEFAULT '',
    grant_usd   REAL    NOT NULL DEFAULT 0,
    -- Same nullability as workflow_step, for the same reason: an attempt
    -- nobody could meter must not read as one that cost nothing.
    spent_usd   REAL,
    spent_input_tokens       INTEGER,
    spent_output_tokens      INTEGER,
    spent_cache_read_tokens  INTEGER,
    spent_cache_write_tokens INTEGER,
    priced_by   TEXT    NOT NULL DEFAULT '',
    completeness REAL,
    stopped_at  TEXT    NOT NULL DEFAULT '',
    -- The answer this attempt gave. A review that refuses one hands the
    -- sentence back to the retry, and that card is process-local today: a
    -- run resumed in another process cannot rebuild it. Kept here so it can.
    result      TEXT    NOT NULL DEFAULT '',
    started_at  TEXT    NOT NULL DEFAULT '',
    ended_at    TEXT    NOT NULL DEFAULT '',
    -- When Claim superseded it, which is a different fact from ended_at: a
    -- step interrupted on Monday and redone on Friday has both.
    replaced_at TEXT    NOT NULL,
    PRIMARY KEY (workflow_id, step_id, attempt),
    FOREIGN KEY (workflow_id) REFERENCES workflow(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS workflow_gate (
    applied INTEGER, -- NULL means an older writer did not record application.
    workflow_id TEXT    NOT NULL,
    -- Gates are numbered within a run from 0, and 0 is always the launch.
    ordinal     INTEGER NOT NULL,
    kind        TEXT    NOT NULL,
    -- The proposal exactly as it was put, and a digest of it. The approval
    -- binds to the digest: the engine recomputes it over what it is about to
    -- apply and refuses on any difference, so an approval names an artifact
    -- rather than a moment.
    proposal    TEXT    NOT NULL,
    digest      TEXT    NOT NULL,
    decision    TEXT    NOT NULL DEFAULT 'waiting',
    asked_at    TEXT    NOT NULL,
    answered_at TEXT    NOT NULL DEFAULT '',
    -- Who answered, as far as this machine can tell: an OS user and the
    -- surface it arrived through. Nothing authenticates anybody here, so this
    -- is a description and not a credential.
    hand        TEXT    NOT NULL DEFAULT '',
    reason      TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (workflow_id, ordinal),
    FOREIGN KEY (workflow_id) REFERENCES workflow(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS workflow_gate_open ON workflow_gate(decision);
`

// DefaultPath is the trace database: the workflow tables live beside the agent
// runs they dispatch.
func DefaultPath() string { return trace.DefaultPath() }

// Open opens (creating if needed) the store at path and migrates it.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"workflow: cannot create %s: %v", filepath.Dir(path), err)
	}
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"workflow: open %s: %v", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, contract.Fail(contract.FailureUnavailable,
			"workflow: open %s: %v", path, err)
	}
	store := &Store{db: db, path: path}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, contract.Fail(contract.FailureUnavailable,
			"workflow: schema %s: %v", path, err)
	}
	if err := addColumns(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// addColumns brings an existing database up to the schema above.
//
// `CREATE TABLE IF NOT EXISTS` does nothing to a table that already exists,
// so a column added after a machine started recording is invisible there
// forever. This is deliberately not a version ledger: one additive column
// with a default is described completely by "is it there", and a ledger for
// that is machinery whose failure modes outnumber the change's.
//
// Every entry must stay additive and defaulted. A migration that rewrites or
// drops belongs somewhere it can be reviewed as a migration, not in a list
// that runs silently on open.
func addColumns(ctx context.Context, db *sql.DB) error {
	wanted := []struct{ table, column, ddl string }{
		{"workflow_gate", "applied", "ALTER TABLE workflow_gate ADD COLUMN applied INTEGER"},
		{"workflow", "repository", "ALTER TABLE workflow ADD COLUMN repository TEXT NOT NULL DEFAULT ''"},
		{"workflow_step", "completeness", "ALTER TABLE workflow_step ADD COLUMN completeness REAL"},
		{"workflow_step", "stopped_at", "ALTER TABLE workflow_step ADD COLUMN stopped_at TEXT NOT NULL DEFAULT ''"},
		{"workflow_step", "route", "ALTER TABLE workflow_step ADD COLUMN route TEXT NOT NULL DEFAULT ''"},
		{"workflow_step", "budget_estimate_usd", "ALTER TABLE workflow_step ADD COLUMN budget_estimate_usd REAL NOT NULL DEFAULT 0"},
		{"workflow_step", "budget_minimum_usd", "ALTER TABLE workflow_step ADD COLUMN budget_minimum_usd REAL NOT NULL DEFAULT 0"},
		{"workflow_step", "budget_source", "ALTER TABLE workflow_step ADD COLUMN budget_source TEXT NOT NULL DEFAULT ''"},
	}
	for _, add := range wanted {
		rows, err := db.QueryContext(ctx, "SELECT 1 FROM pragma_table_info(?) WHERE name = ?",
			add.table, add.column)
		if err != nil {
			return contract.Fail(contract.FailureUnavailable,
				"workflow: reading %s columns: %v", add.table, err)
		}
		present := rows.Next()
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return contract.Fail(contract.FailureUnavailable,
				"workflow: reading %s columns: %v", add.table, err)
		}
		if err := rows.Close(); err != nil {
			return contract.Fail(contract.FailureUnavailable,
				"workflow: reading %s columns: %v", add.table, err)
		}
		if present {
			continue
		}
		if _, err := db.ExecContext(ctx, add.ddl); err != nil {
			return contract.Fail(contract.FailureUnavailable,
				"workflow: adding %s.%s: %v", add.table, add.column, err)
		}
	}
	return nil
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

// Create writes a compiled plan down as a new run, every step pending.
//
// The whole graph, in one transaction, before anything spawns. A workflow
// half on disk is one a resume would continue with steps it never knew about.
func (s *Store) Create(ctx context.Context, id string, plan Plan, repository string, at time.Time, pid int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: begin %s", id)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workflow (id, task, repository, grant_usd, started_at, writer_pid)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, plan.Graph.Task, repository, plan.Graph.GrantUSD, stamp(at), pid); err != nil {
		return unavailable(err, "workflow: opening %s", id)
	}
	for i, step := range plan.Graph.Steps {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO workflow_step
			 (workflow_id, id, ordinal, type_name, pool, objective, files,
			  criterion, needs, subject, on_outcome, effects, route,
			  budget_estimate_usd, budget_minimum_usd, budget_source, grant_usd, status)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, step.ID, i, step.TypeName, plan.Pools[step.ID].String(),
			step.Task.Objective, jsonList(step.Task.Files), step.Task.Criterion,
			jsonList(step.Needs), step.Subject, step.On.String(),
			jsonEffects(step.Permission.Effects),
			jsonRoute(step.Route),
			step.BudgetEstimateUSD, step.BudgetMinimumUSD, step.BudgetSource,
			step.Permission.BudgetUSD, StatusPending.String()); err != nil {
			return unavailable(err, "workflow: opening %s step %s", id, step.ID)
		}
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err, "workflow: opening %s", id)
	}
	return nil
}

// fileAttempt copies a step's current row into the archive, verbatim. It
// takes (replaced_at, workflow_id, step_id).
//
// Two callers, and they are the two writes that make an attempt unreadable:
// Reset, which drops the status a resume is about to replace, and Claim,
// which clears the outcome columns. Reset runs first when a resume redoes an
// interrupted step, so it is the one that files the TRUE terminal status --
// measured 2026-08-16: with the copy in Claim alone, every redone attempt was
// filed as `pending`, because Reset had already written that over
// `interrupted`. INSERT OR IGNORE is what lets both call it: the first copy
// of an attempt number wins, and the second is a no-op rather than a
// correction.
//
// attempt > 0 is the whole guard: a step dispatched for the first time has
// never held an outcome, and filing its empty row would put a pending step in
// a table of finished ones.
//
// It selects from the row itself, so what is filed is exactly what was there;
// it cannot drift from the columns Finish wrote, because it never passes
// through Go.
const fileAttempt = `
INSERT OR IGNORE INTO workflow_attempt (
    workflow_id, step_id, attempt, trace_id, status, verdict,
    reason_kind, reason_text, grant_usd, spent_usd,
    spent_input_tokens, spent_output_tokens,
    spent_cache_read_tokens, spent_cache_write_tokens, priced_by,
    completeness, stopped_at, result, started_at, ended_at, replaced_at)
SELECT workflow_id, id, attempt, trace_id, status, verdict,
       reason_kind, reason_text, grant_usd, spent_usd,
       spent_input_tokens, spent_output_tokens,
       spent_cache_read_tokens, spent_cache_write_tokens, priced_by,
       completeness, stopped_at, result, started_at, ended_at, ?
FROM workflow_step
WHERE workflow_id = ? AND id = ? AND attempt > 0`

// Discard removes a run that was written and then refused before anything
// could authorize it.
//
// It exists for exactly one caller: Create writes the run, then asks the launch
// gate, and the ask can refuse. What that used to leave behind was a workflow
// row with every step pending and no gate at all -- visible to `list`, and
// runnable by `resume`, with nobody having approved a penny of it.
//
// Deliberately narrow. It refuses a run that has any gate, because a run
// somebody has been asked about is a run with a record, and a record is not
// something a cleanup path gets to delete. Nothing else in this package
// removes a run, and nothing else should.
func (s *Store) Discard(ctx context.Context, id string) error {
	gates, err := s.Gates(ctx, id)
	if err != nil {
		return err
	}
	if len(gates) > 0 {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow %s has %d gate(s) on the record and is not a run to discard", id, len(gates))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: discarding %s", id)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DELETE FROM workflow_step WHERE workflow_id = ?`,
		`DELETE FROM workflow_attempt WHERE workflow_id = ?`,
		`DELETE FROM workflow WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, id); err != nil {
			return unavailable(err, "workflow: discarding %s", id)
		}
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err, "workflow: discarding %s", id)
	}
	return nil
}

// Claim marks a step running, with the pid that owns it, and files the
// attempt it is replacing.
//
// One transaction, for two reasons rather than one. A reader must never see a
// step running under nobody -- that was the original -- and the archive must
// never come apart from the erasure it records. A copy taken outside this
// boundary would leave two ways to lose an attempt: the copy failing after the
// clear, and a crash between them.
//
// The INSERT selects from the row itself, so what is filed is exactly what was
// there. It cannot drift from the columns Finish wrote, because it never
// passes through Go.
func (s *Store) Claim(ctx context.Context, id, stepID, traceID string,
	attempt int, at time.Time, pid int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: claiming %s step %s", id, stepID)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, fileAttempt, stamp(at), id, stepID); err != nil {
		return unavailable(err, "workflow: filing %s step %s attempt", id, stepID)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE workflow_step
		 SET status = ?, trace_id = ?, attempt = ?, writer_pid = ?, started_at = ?,
		     ended_at = '', verdict = '', reason_kind = '', reason_text = '',
		     result = '', discovered = '', spent_usd = NULL,
		     spent_input_tokens = NULL, spent_output_tokens = NULL,
		     spent_cache_read_tokens = NULL, spent_cache_write_tokens = NULL,
		     priced_by = '', completeness = NULL, stopped_at = ''
		 WHERE workflow_id = ? AND id = ?`,
		StatusRunning.String(), traceID, attempt, pid, stamp(at), id, stepID); err != nil {
		return unavailable(err, "workflow: claiming %s step %s", id, stepID)
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err, "workflow: claiming %s step %s", id, stepID)
	}
	return nil
}

// Finish writes what a step ended as, together with the answer it gave.
func (s *Store) Finish(ctx context.Context, id, stepID string, status Status,
	report contract.Report, at time.Time) error {
	result, err := jsonMap(report.Result)
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow: %s step %s: result cannot be recorded: %v", id, stepID, err)
	}
	input, output, cacheRead, cacheWrite, usd, pricedBy := spentColumns(report.Spent)
	var completeness any
	if report.Completeness != nil {
		completeness = *report.Completeness
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE workflow_step
		 SET status = ?, ended_at = ?, verdict = ?, reason_kind = ?, reason_text = ?,
		     result = ?, discovered = ?, writer_pid = 0, spent_usd = ?,
		     spent_input_tokens = ?, spent_output_tokens = ?,
		     spent_cache_read_tokens = ?, spent_cache_write_tokens = ?, priced_by = ?,
		     completeness = ?, stopped_at = ?
		 WHERE workflow_id = ? AND id = ?`,
		status.String(), stamp(at), report.Verdict.String(),
		report.Reason.Kind.String(), report.Reason.Text,
		result, jsonDiscoveries(report.Discovered), usd,
		input, output, cacheRead, cacheWrite, pricedBy,
		completeness, report.StoppedAt, id, stepID)
	if err != nil {
		return unavailable(err, "workflow: closing %s step %s", id, stepID)
	}
	return nil
}

// spentColumns turns a Charge into the six values Finish writes. Nil
// everywhere is the unmeasured row: writing real zeros in its place would
// turn a step nobody could meter into a receipt claiming it cost nothing.
func spentColumns(spent contract.Charge) (input, output, cacheRead, cacheWrite, usd any, pricedBy string) {
	if !spent.Measured() {
		return nil, nil, nil, nil, nil, ""
	}
	if spent.USD != nil {
		usd = *spent.USD
	}
	return spent.InputTokens, spent.OutputTokens, spent.CacheReadTokens, spent.CacheWriteTokens, usd, spent.PricedBy
}

// Interrupt marks a step as one nobody judged, with the reason it was left
// that way. It keeps whatever the step already had on it: the trace id and the
// attempt are what a reader follows to see how far it got.
func (s *Store) Interrupt(ctx context.Context, id, stepID, why string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE workflow_step
		 SET status = ?, ended_at = ?, verdict = '', reason_kind = ?, reason_text = ?,
		     writer_pid = 0
		 WHERE workflow_id = ? AND id = ?`,
		StatusInterrupted.String(), stamp(at),
		contract.FailureCanceled.String(), why, id, stepID)
	if err != nil {
		return unavailable(err, "workflow: interrupting %s step %s", id, stepID)
	}
	return nil
}

// Reset puts a step back to pending so a resume can dispatch it again, and
// files the attempt it is about to make unreadable.
//
// The filing belongs here and not only in Claim because this is the write
// that destroys the interesting half. Claim clears the outcome columns, which
// is visible; Reset overwrites `interrupted` with `pending`, which is not --
// and `pending` is the one status that says nothing about how the attempt
// ended. One transaction, so the copy and the overwrite cannot come apart.
func (s *Store) Reset(ctx context.Context, id, stepID string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: resetting %s step %s", id, stepID)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, fileAttempt, stamp(at), id, stepID); err != nil {
		return unavailable(err, "workflow: filing %s step %s attempt", id, stepID)
	}
	// The same columns Claim clears, and for the same reason: the attempt has
	// just been filed, so leaving its outcome on the live row makes the run
	// hold one attempt's money twice -- once in workflow_attempt and once
	// here -- and Run.Budget() sums both. It also left `verdict = ok` on a
	// step whose status now says pending, a row that reads as a finished step
	// nobody has started.
	//
	// Claim did clear them, which hid this: in the ordinary path a reset step
	// is claimed moments later. A reset that is never followed by a claim --
	// a redo the funding check refuses, a run somebody abandons -- kept the
	// lie permanently.
	if _, err := tx.ExecContext(ctx,
		`UPDATE workflow_step
		 SET status = ?, ended_at = '', writer_pid = 0,
		     verdict = '', reason_kind = '', reason_text = '',
		     result = '', discovered = '', spent_usd = NULL,
		     spent_input_tokens = NULL, spent_output_tokens = NULL,
		     spent_cache_read_tokens = NULL, spent_cache_write_tokens = NULL,
		     priced_by = '', completeness = NULL, stopped_at = ''
		 WHERE workflow_id = ? AND id = ?`,
		StatusPending.String(), id, stepID); err != nil {
		return unavailable(err, "workflow: resetting %s step %s", id, stepID)
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err, "workflow: resetting %s step %s", id, stepID)
	}
	return nil
}

// Own records which Atenea is running this workflow now, and refuses if
// somebody else got there first.
//
// The predicate is the point. takeOver reads writer_pid, checks the process is
// gone, and calls this -- and between the read and the write there was nothing
// at all. Two Ateneas resuming the same id milliseconds apart both saw a free
// run, both wrote their own pid, and both executed the graph: every step
// dispatched twice, the grant charged twice, both write effects applied, and
// the two processes overwriting each other's Finish on the same row. `held` is
// what the caller believed when it decided, so the UPDATE only lands if that is
// still true.
//
// Passing the caller's own pid as held is how a re-entry keeps working: a
// process taking over a run it already owns is not a race with itself.
func (s *Store) Own(ctx context.Context, id string, pid, held int) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE workflow SET writer_pid = ?, closed = 0, ended_at = '', stop = ''
		 WHERE id = ? AND writer_pid = ?`, pid, id, held)
	if err != nil {
		return unavailable(err, "workflow: claiming %s", id)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return unavailable(err, "workflow: claiming %s", id)
	}
	if rows == 0 {
		// Either another Atenea took it in the gap, or the id is gone. Load
		// says which, and naming the winner is what turns "try again" into
		// something the reader can act on.
		current, loadErr := s.Load(ctx, id)
		if loadErr != nil {
			return loadErr
		}
		return contract.Fail(contract.FailureUnavailable,
			"workflow %s was taken over by pid %d while this one was starting it",
			id, current.WriterPID)
	}
	return nil
}

// Reshare changes what one step is allowed to spend on its NEXT dispatch.
//
// It writes the step's declaration, not its outcome, so the row a resume
// compiles from carries the new figure and [Engine.replan] needs to know
// nothing about redoing. What it must never do is edit an attempt already on
// the record: the share a dead attempt ran under is the measurement, and a
// caller that rewrote it would destroy the only half of "cut at $0.62,
// finished at $0.80" that has actually been observed. That ordering is the
// caller's to keep -- Reset files the archive copy, so Reset comes first --
// and it is asserted by a test rather than trusted here, because nothing in
// one UPDATE can see it.
func (s *Store) Reshare(ctx context.Context, id, stepID string, usd float64) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE workflow_step SET grant_usd = ? WHERE workflow_id = ? AND id = ?`,
		usd, id, stepID)
	if err != nil {
		return unavailable(err, "workflow: resharing %s step %s", id, stepID)
	}
	// A silent no-op here would leave a step running on its old share while
	// the gate log says otherwise, which is the one outcome worth a refusal.
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return contract.Fail(contract.FailureNotFound,
			"workflow %s has no step %s", id, stepID)
	}
	return nil
}

// Regrant raises what the whole run may spend.
//
// Only ever up. A grant is what somebody authorized, and lowering it under
// steps that already ran would make the record claim they were never allowed
// to -- so the refusal is here, where the number is written, and not only in
// the command that asks for it.
func (s *Store) Regrant(ctx context.Context, id string, usd float64) error {
	run, err := s.Load(ctx, id)
	if err != nil {
		return err
	}
	if usd < run.GrantUSD-moneyEpsilon {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow %s is granted $%.2f: a grant is raised, never lowered, and $%.2f is less",
			id, run.GrantUSD, usd)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE workflow SET grant_usd = ? WHERE id = ?`, usd, id); err != nil {
		return unavailable(err, "workflow: regranting %s", id)
	}
	return nil
}

// End closes a workflow: finished, or stopped for the reason given.
func (s *Store) End(ctx context.Context, id string, stop Stop, at time.Time) error {
	// Only a run that stopped for no reason is finished. Anything with a
	// reason is one somebody may still resume, and leaving it open is what
	// makes `atenea workflow resume` the obvious next move rather than an
	// override of something the record calls complete.
	closed := 1
	if stop != StopNone {
		closed = 0
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE workflow SET closed = ?, ended_at = ?, stop = ?, writer_pid = 0 WHERE id = ?`,
		closed, stamp(at), string(stop), id)
	if err != nil {
		return unavailable(err, "workflow: closing %s", id)
	}
	return nil
}

// Load reads one run back whole.
func (s *Store) Load(ctx context.Context, id string) (Run, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task, repository, grant_usd, started_at, ended_at, closed, stop, writer_pid
		 FROM workflow WHERE id = ?`, id)
	var (
		out            Run
		started, ended string
		closed         int
		stop           string
	)
	switch err := row.Scan(&out.ID, &out.Task, &out.Repository, &out.GrantUSD, &started, &ended,
		&closed, &stop, &out.WriterPID); {
	case errors.Is(err, sql.ErrNoRows):
		return Run{}, contract.Fail(contract.FailureNotFound,
			"no workflow %s in %s", id, s.path)
	case err != nil:
		return Run{}, unavailable(err, "workflow: reading %s", id)
	}
	out.Started = parseStamp(started)
	out.Ended = parseStamp(ended)
	out.Closed = closed != 0
	out.Stop = Stop(stop)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type_name, pool, objective, files, criterion, needs, subject,
				on_outcome, effects, route, budget_estimate_usd, budget_minimum_usd, budget_source,
				grant_usd, status, trace_id, attempt, writer_pid,
		        started_at, ended_at, verdict, reason_kind, reason_text, result,
		        discovered, spent_usd, spent_input_tokens, spent_output_tokens,
		        spent_cache_read_tokens, spent_cache_write_tokens, priced_by,
		        completeness, stopped_at
			 FROM workflow_step WHERE workflow_id = ? ORDER BY ordinal`, id)
	if err != nil {
		return Run{}, unavailable(err, "workflow: reading %s steps", id)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return Run{}, err
		}
		step.Step.Permission.Task = out.Task
		out.Steps = append(out.Steps, step)
	}
	if err := rows.Err(); err != nil {
		return Run{}, unavailable(err, "workflow: reading %s steps", id)
	}
	// Closed before the next query rather than at return: the archive read
	// below would otherwise hold a second connection open for the length of
	// this one, and WAL only makes that work, not free.
	if err := rows.Close(); err != nil {
		return Run{}, unavailable(err, "workflow: reading %s steps", id)
	}
	// The archive is read here rather than left to a caller who remembers to
	// ask, because Run.Spend is the only place a balance comes from and it
	// cannot see money it was not handed. Empty for every run nobody redid,
	// which is almost all of them -- one indexed lookup on workflow_id.
	superseded, err := s.Attempts(ctx, id, "")
	if err != nil {
		return Run{}, err
	}
	out.Superseded = superseded
	return out, nil
}

// Attempts reads the superseded dispatches of one step, oldest first by the
// moment each was replaced. An empty stepID reads every step of the run, in
// that same one chronological order rather than grouped by step.
//
// The live attempt is NOT here -- it is the workflow_step row, and a caller
// wanting the whole history reads both. Keeping the current one out of this
// table is what makes "was this ever redone" answerable by asking whether the
// slice is empty, rather than by comparing a count against an attempt number
// that a crash could have advanced without a dispatch.
func (s *Store) Attempts(ctx context.Context, id, stepID string) ([]AttemptRow, error) {
	query := `SELECT step_id, attempt, trace_id, status, verdict, reason_kind,
	                 reason_text, grant_usd, spent_usd, spent_input_tokens,
	                 spent_output_tokens, spent_cache_read_tokens,
	                 spent_cache_write_tokens, priced_by, completeness,
	                 stopped_at, result, started_at, ended_at, replaced_at
	          FROM workflow_attempt WHERE workflow_id = ?`
	args := []any{id}
	if stepID != "" {
		query += " AND step_id = ?"
		args = append(args, stepID)
	}
	query += " ORDER BY step_id, attempt"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, unavailable(err, "workflow: reading %s attempts", id)
	}
	defer func() { _ = rows.Close() }()

	var out []AttemptRow
	for rows.Next() {
		row, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err, "workflow: reading %s attempts", id)
	}
	// Chronological, which is what this function and [Run.Superseded] both
	// promise and what neither delivered. `ORDER BY step_id, attempt` is
	// chronological only within one step: over a whole run it put step z's
	// Monday attempts after step a's Friday ones, so a reader walking the
	// archive of a graph that was corrected twice saw the two corrections in
	// alphabetical order and read them as one sequence of events.
	//
	// Sorted here rather than in the query because replaced_at is stored as
	// RFC3339Nano, and that layout drops trailing zeros from the fraction:
	// "12:00:00.5Z" sorts AFTER "12:00:00.50000001Z" as text, and a whole
	// second written with no fraction at all sorts after both. ORDER BY
	// replaced_at would put those three in exactly the wrong order while
	// looking like it had fixed this.
	//
	// Stable, so the SQL order above remains the tiebreak: attempts filed at
	// the same instant stay grouped by step and ordered by attempt number.
	slices.SortStableFunc(out, func(a, b AttemptRow) int {
		return a.Replaced.Compare(b.Replaced)
	})
	return out, nil
}

func scanAttempt(rows *sql.Rows) (AttemptRow, error) {
	var (
		out                         AttemptRow
		status, verdict             string
		reasonKind, reasonText      string
		usd                         sql.NullFloat64
		inputTok, outputTok         sql.NullInt64
		cacheReadTok, cacheWriteTok sql.NullInt64
		pricedBy                    string
		completeness                sql.NullFloat64
		result                      string
		started, ended, replaced    string
	)
	if err := rows.Scan(&out.StepID, &out.Attempt, &out.TraceID, &status, &verdict,
		&reasonKind, &reasonText, &out.GrantUSD, &usd, &inputTok, &outputTok,
		&cacheReadTok, &cacheWriteTok, &pricedBy, &completeness,
		&out.StoppedAt, &result, &started, &ended, &replaced); err != nil {
		return AttemptRow{}, unavailable(err, "workflow: reading an attempt")
	}
	var err error
	if out.Status, err = ParseStatus(status); err != nil {
		return AttemptRow{}, err
	}
	if verdict != "" {
		if out.Verdict, err = contract.ParseVerdict(verdict); err != nil {
			return AttemptRow{}, err
		}
	}
	if reasonKind != "" {
		if out.Reason.Kind, err = contract.ParseFailureKind(reasonKind); err != nil {
			return AttemptRow{}, err
		}
		out.Reason.Text = reasonText
	}
	// Same convention as the step row: a null is a figure nobody measured,
	// and a zero would read as a turn that was weighed and cost nothing.
	if usd.Valid {
		v := usd.Float64
		out.Spent.USD = &v
	}
	out.Spent.InputTokens = int(inputTok.Int64)
	out.Spent.OutputTokens = int(outputTok.Int64)
	out.Spent.CacheReadTokens = int(cacheReadTok.Int64)
	out.Spent.CacheWriteTokens = int(cacheWriteTok.Int64)
	out.Spent.PricedBy = pricedBy
	if completeness.Valid {
		v := completeness.Float64
		out.Completeness = &v
	}
	out.Result = readMap(result)
	out.Started = parseStamp(started)
	out.Ended = parseStamp(ended)
	out.Replaced = parseStamp(replaced)
	return out, nil
}

// List names the runs, newest first.
func (s *Store) List(ctx context.Context, limit int) ([]Run, error) {
	query := `SELECT id, task, grant_usd, started_at, ended_at, closed, stop, writer_pid
	          FROM workflow ORDER BY started_at DESC`
	args := []any{}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, unavailable(err, "workflow: list")
	}
	defer func() { _ = rows.Close() }()

	var out []Run
	for rows.Next() {
		var (
			run            Run
			started, ended string
			closed         int
			stop           string
		)
		if err := rows.Scan(&run.ID, &run.Task, &run.GrantUSD, &started, &ended,
			&closed, &stop, &run.WriterPID); err != nil {
			return nil, unavailable(err, "workflow: list")
		}
		run.Started = parseStamp(started)
		run.Ended = parseStamp(ended)
		run.Closed = closed != 0
		run.Stop = Stop(stop)
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err, "workflow: list")
	}
	return out, nil
}

func scanStep(rows *sql.Rows) (StepRow, error) {
	var (
		out                                   StepRow
		pool, files, needs, effects, routeRaw string
		onOutcome                             string
		status, verdict                       string
		reasonKind, reasonText                string
		result, discovered                    string
		started, ended                        string
		usd                                   sql.NullFloat64
		budgetEstimate, budgetMinimum         float64
		budgetSource                          string
		inputTok, outputTok                   sql.NullInt64
		cacheReadTok, cacheWriteTok           sql.NullInt64
		pricedBy                              string
		completeness                          sql.NullFloat64
		stoppedAt                             string
	)
	if err := rows.Scan(&out.Step.ID, &out.Step.TypeName, &pool,
		&out.Step.Task.Objective, &files, &out.Step.Task.Criterion, &needs,
		&out.Step.Subject, &onOutcome, &effects, &routeRaw,
		&budgetEstimate, &budgetMinimum, &budgetSource,
		&out.Step.Permission.BudgetUSD, &status, &out.TraceID, &out.Attempt,
		&out.WriterPID, &started, &ended, &verdict, &reasonKind, &reasonText,
		&result, &discovered, &usd,
		&inputTok, &outputTok, &cacheReadTok, &cacheWriteTok, &pricedBy,
		&completeness, &stoppedAt); err != nil {
		return StepRow{}, unavailable(err, "workflow: reading a step")
	}
	out.Step.BudgetEstimateUSD = budgetEstimate
	out.Step.BudgetMinimumUSD = budgetMinimum
	out.Step.BudgetSource = budgetSource
	var err error
	if out.Pool, err = config.ParsePool(pool); err != nil {
		return StepRow{}, err
	}
	if out.Status, err = ParseStatus(status); err != nil {
		return StepRow{}, err
	}
	// The bar the author wrote, applied again on resume. Defaulting it here
	// would silently widen a graph that asked for successes only.
	if out.Step.On, err = ParseRequirement(onOutcome); err != nil {
		return StepRow{}, err
	}
	out.Step.Task.Files = readList(files)
	out.Step.Needs = readList(needs)
	if out.Step.Permission.Effects, err = readEffects(effects); err != nil {
		return StepRow{}, err
	}
	if out.Step.Route, err = readRoute(routeRaw); err != nil {
		return StepRow{}, err
	}
	out.Started = parseStamp(started)
	out.Ended = parseStamp(ended)
	if verdict != "" {
		if out.Verdict, err = contract.ParseVerdict(verdict); err != nil {
			return StepRow{}, err
		}
	}
	if reasonKind != "" || reasonText != "" {
		kind, err := contract.ParseFailureKind(reasonKind)
		if err != nil {
			return StepRow{}, err
		}
		out.Reason = contract.Reason{Kind: kind, Text: reasonText}
	}
	out.Result = readMap(result)
	out.Discovered = readDiscoveries(discovered)
	out.Spent = contract.Charge{
		InputTokens:      int(inputTok.Int64),
		OutputTokens:     int(outputTok.Int64),
		CacheReadTokens:  int(cacheReadTok.Int64),
		CacheWriteTokens: int(cacheWriteTok.Int64),
		PricedBy:         pricedBy,
	}
	if usd.Valid {
		value := usd.Float64
		out.Spent.USD = &value
	}
	if completeness.Valid {
		value := completeness.Float64
		out.Completeness = &value
	}
	out.StoppedAt = stoppedAt
	return out, nil
}

func unavailable(err error, format string, args ...any) error {
	return contract.Fail(contract.FailureUnavailable, format+": %v",
		append(args, err)...)
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseStamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func jsonList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func readList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func jsonEffects(effects []contract.Effect) string {
	names := make([]string, 0, len(effects))
	for _, effect := range effects {
		names = append(names, effect.String())
	}
	return jsonList(names)
}

func readEffects(raw string) ([]contract.Effect, error) {
	names := readList(raw)
	out := make([]contract.Effect, 0, len(names))
	for _, name := range names {
		effect, err := contract.ParseEffect(name)
		if err != nil {
			return nil, err
		}
		out = append(out, effect)
	}
	return out, nil
}

func jsonMap(values map[string]any) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func readMap(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func jsonDiscoveries(found []contract.Discovery) string {
	if len(found) == 0 {
		return ""
	}
	type wire struct {
		Level string `json:"level"`
		Note  string `json:"note"`
	}
	out := make([]wire, 0, len(found))
	for _, d := range found {
		out = append(out, wire{Level: d.Level.String(), Note: d.Note})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(raw)
}

func readDiscoveries(raw string) []contract.Discovery {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var wire []struct {
		Level string `json:"level"`
		Note  string `json:"note"`
	}
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ---------------------------------------------------------------------------
// What things have cost
// ---------------------------------------------------------------------------

// Observed is what one agent type has actually cost on this machine.
//
// It was evidence and nothing else until 2026-08-16, when checkFunding began
// refusing a share below MedianUSD. It is still not a ceiling -- nothing here
// stops a running turn -- but it is now an admission requirement, and it is
// the only one derived from steps that FINISHED rather than from a probe that
// started a turn and stopped. See engine.go's observedMinRows for how many
// rows it takes before this number is allowed to refuse anybody.
//
// Measured 2026-08-16: the probe-derived rules asked $0.06 of a reader step
// on taxiprime-backend while five completed reader steps cost $0.30-$0.44,
// and eighteen of twenty-three steps died in the gap having read real files
// and written no answer.
type Observed struct {
	// TypeName is the agent type these rows ran as.
	TypeName string
	// MedianUSD is the middle of the clean rows. Median rather than mean
	// because one run stopped at its ceiling drags a mean toward exactly the
	// under-estimate this table exists to prevent.
	MedianUSD float64
	// MinUSD and MaxUSD are the range of the same clean rows, so a reader can
	// see whether the median means anything.
	MinUSD, MaxUSD float64
	// N is how many clean rows the median is built from. Printed, always: a
	// median of two is a rumor and the reader is entitled to discount it.
	N int
	// AtCeiling counts rows excluded because the run spent its whole grant.
	// Those are censored observations -- "at least this much", not "this
	// much" -- and averaging them in is how a measurement quietly becomes the
	// under-estimate it was meant to replace.
	AtCeiling int
	// Unmeasured counts rows that ran and reported no price at all: a turn
	// killed at its timeout, or an agent that never called a model. Counted
	// rather than dropped, because "we have no number" and "the number is
	// small" are different facts.
	Unmeasured int
}

// CostTable is what CostByType read back, and the scope it could support.
type CostTable struct {
	// Repository is the repository the rows were scoped to. Empty means the
	// table is machine-wide -- either because nothing has been recorded
	// against this repository yet, or because the rows predate the column.
	Repository string
	// Types is keyed by agent type name. A type absent from this map has
	// never been measured here, which is a fact the caller must pass on in
	// those words rather than substituting a default.
	Types map[string]Observed
}

// ModelObserved is successful cost and latency history for one routed model.
// It is separate from Observed because admission is keyed by agent type while
// model choice compares explicit primary/fallback candidates.
type ModelObserved struct {
	Role           string
	Model          string
	MedianUSD      float64
	MinUSD         float64
	MaxUSD         float64
	MedianDuration time.Duration
	N              int
}

// ModelCostTable is the model history read by the decision router.
type ModelCostTable struct {
	Repository string
	Models     map[string]ModelObserved
}

func modelKey(role, model string) string { return role + "\x00" + model }

// Performance returns evidence for one routed role/model pair.
func (t ModelCostTable) Performance(role, model string) (ModelObserved, bool) {
	got, ok := t.Models[modelKey(role, model)]
	return got, ok
}

// CostByModel reads successful workflow steps whose persisted route names a
// model. It falls back to machine-wide history when a repository has no rows.
func (s *Store) CostByModel(ctx context.Context, repository string) (ModelCostTable, error) {
	out := ModelCostTable{Repository: repository, Models: map[string]ModelObserved{}}
	rows, err := s.modelCostRows(ctx, repository)
	if err != nil {
		return ModelCostTable{}, err
	}
	if len(rows) == 0 && repository != "" {
		out.Repository = ""
		if rows, err = s.modelCostRows(ctx, ""); err != nil {
			return ModelCostTable{}, err
		}
	}
	spends := map[string][]float64{}
	durations := map[string][]time.Duration{}
	for _, row := range rows {
		route, err := readRoute(row.route)
		if err != nil || route == nil || strings.TrimSpace(route.Model) == "" || row.spent == nil {
			continue
		}
		key := modelKey(modelRoleName(row.role), route.Model)
		spends[key] = append(spends[key], *row.spent)
		if !row.started.IsZero() && !row.ended.IsZero() && row.ended.After(row.started) {
			durations[key] = append(durations[key], row.ended.Sub(row.started))
		}
	}
	for key, values := range spends {
		sort.Float64s(values)
		role, model, _ := strings.Cut(key, "\x00")
		seen := ModelObserved{Role: role, Model: model, N: len(values),
			MedianUSD: median(values), MinUSD: values[0], MaxUSD: values[len(values)-1]}
		if ds := durations[key]; len(ds) > 0 {
			sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
			seen.MedianDuration = durationMedian(ds)
		}
		out.Models[key] = seen
	}
	return out, nil
}

func modelRoleName(agent string) string {
	if agent == "reader" {
		return "explore"
	}
	return agent
}

type modelCostRow struct {
	role    string
	route   string
	spent   *float64
	started time.Time
	ended   time.Time
}

func (s *Store) modelCostRows(ctx context.Context, repository string) ([]modelCostRow, error) {
	query := `SELECT s.type_name, s.route, s.spent_usd, s.started_at, s.ended_at
	          FROM workflow_step s JOIN workflow w ON w.id = s.workflow_id
	          WHERE s.verdict = ?`
	args := []any{contract.VerdictOK.String()}
	if repository != "" {
		query += " AND w.repository = ?"
		args = append(args, repository)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, unavailable(err, "workflow: reading model costs")
	}
	defer func() { _ = rows.Close() }()
	var out []modelCostRow
	for rows.Next() {
		var row modelCostRow
		var started, ended string
		if err := rows.Scan(&row.role, &row.route, &row.spent, &started, &ended); err != nil {
			return nil, unavailable(err, "workflow: reading model costs")
		}
		row.started, row.ended = parseStamp(started), parseStamp(ended)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err, "workflow: reading model costs")
	}
	return out, nil
}

func durationMedian(sorted []time.Duration) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// CostByType reads back what each agent type has cost, scoped to repository
// when that repository has rows and machine-wide when it does not.
//
// Workflow steps only. A single `atenea agent` run is not priced anywhere --
// agent_trace has no spend column -- so this table cannot see one, and every
// caller has to say so rather than let a reader assume it covers them.
func (s *Store) CostByType(ctx context.Context, repository string) (CostTable, error) {
	out := CostTable{Repository: repository, Types: map[string]Observed{}}
	rows, err := s.costRows(ctx, repository)
	if err != nil {
		return CostTable{}, err
	}
	// Falling back is not the same as finding nothing: a machine that has
	// never run anything against this repository still knows what exploring
	// costs, and saying so with the scope named is more useful than silence.
	if len(rows) == 0 && repository != "" {
		out.Repository = ""
		if rows, err = s.costRows(ctx, ""); err != nil {
			return CostTable{}, err
		}
	}

	clean := map[string][]float64{}
	for _, row := range rows {
		seen := out.Types[row.typeName]
		seen.TypeName = row.typeName
		switch {
		case row.spent == nil:
			seen.Unmeasured++
		case row.grant > 0 && *row.spent >= row.grant*ceilingBand:
			seen.AtCeiling++
		default:
			clean[row.typeName] = append(clean[row.typeName], *row.spent)
		}
		out.Types[row.typeName] = seen
	}
	for name, spends := range clean {
		sort.Float64s(spends)
		seen := out.Types[name]
		seen.N = len(spends)
		seen.MinUSD, seen.MaxUSD = spends[0], spends[len(spends)-1]
		seen.MedianUSD = median(spends)
		out.Types[name] = seen
	}
	return out, nil
}

// ceilingBand is how close to its grant a run has to land before its spend is
// read as censored. Not equality: a run stopped at its ceiling stops on the
// turn that crossed it, so the recorded figure lands just under or just over.
const ceilingBand = 0.98

type costRow struct {
	typeName string
	grant    float64
	spent    *float64
}

// costRows reads finished steps. Only `ok` rows: a step that failed spent what
// it spent, but it is not evidence of what the work costs when it works.
func (s *Store) costRows(ctx context.Context, repository string) ([]costRow, error) {
	query := `SELECT s.type_name, s.grant_usd, s.spent_usd
	          FROM workflow_step s JOIN workflow w ON w.id = s.workflow_id
	          WHERE s.verdict = ?`
	args := []any{contract.VerdictOK.String()}
	if repository != "" {
		query += " AND w.repository = ?"
		args = append(args, repository)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, unavailable(err, "workflow: reading costs")
	}
	defer func() { _ = rows.Close() }()

	var out []costRow
	for rows.Next() {
		var row costRow
		if err := rows.Scan(&row.typeName, &row.grant, &row.spent); err != nil {
			return nil, unavailable(err, "workflow: reading costs")
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err, "workflow: reading costs")
	}
	return out, nil
}

// median of a sorted slice. Even counts take the mean of the middle pair,
// which is the ordinary definition and not a decision worth a knob.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
