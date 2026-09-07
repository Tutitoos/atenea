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
	// StatusPending is part of ATENEA's public orchestration contract.
	StatusPending Status = iota
	// StatusRunning has a live process behind it -- or had one, until the
	// Atenea that owned it died. Which of the two is a question about a pid,
	// not about this column, and it is why WriterPID sits beside it.
	// StatusRunning is part of ATENEA's public orchestration contract.
	StatusRunning
	// StatusOK is the agent's own ok.
	StatusOK
	// StatusFailed is the agent reaching a judgement and the judgement being
	// no.
	// StatusFailed is part of ATENEA's public orchestration contract.
	StatusFailed
	// StatusIncomplete is the agent stopping short, or dying, and saying so
	// through its report.
	// StatusIncomplete is part of ATENEA's public orchestration contract.
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
	// StatusInterrupted is part of ATENEA's public orchestration contract.
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

// String returns the public textual representation.
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
	// StopAborted is part of ATENEA's public orchestration contract.
	StopAborted Stop = "aborted"
	// StopCrashed is what a resume infers: the record says running, and the
	// process it names is gone. Nobody wrote this; it is the absence of a
	// clean close, which is exactly why it must not read the same as abort.
	// StopCrashed is part of ATENEA's public orchestration contract.
	StopCrashed Stop = "crashed"
	// StopUnjudged is a run that ran out of things it may do on its own:
	// what is left is steps nobody judged, and repeating those could land an
	// effect twice. It is not finished and it is not aborted -- it is
	// waiting for a person to say --redo, and calling it finished would put
	// a completed run on the record with a hole in the middle of it.
	// StopUnjudged is part of ATENEA's public orchestration contract.
	StopUnjudged Stop = "unjudged"
	// StopRejected is a plan a person read and refused. Nothing ran: it is
	// not aborted, because nobody cut anything, and it is certainly not
	// finished. A run that never got permission to exist should not look
	// like one that did its work.
	// StopRejected is part of ATENEA's public orchestration contract.
	StopRejected Stop = "rejected"
	// StateRunning is part of ATENEA's public orchestration contract.
	StateRunning = "running"
	// StateCompleted is part of ATENEA's public orchestration contract.
	StateCompleted = "completed"
	// StateAttentionRequired is part of ATENEA's public orchestration contract.
	StateAttentionRequired = "attention_required"
	// StateUncertain is part of ATENEA's public orchestration contract.
	StateUncertain = "uncertain"
	// StateStopped is part of ATENEA's public orchestration contract.
	StateStopped = "stopped"
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
	// Effects is the workflow's persisted permission ceiling. A resumed
	// workflow may use only the intersection of this set and the current
	// session grant; it can never gain authority from a reconnect.
	Effects []contract.Effect
	// Policy is the complete versioned ceiling captured at creation.
	Policy WorkflowPolicy
	// Recovery is the durable, derived retry state shown by workflow status.
	// It is rebuilt from the persisted current and superseded attempts on every
	// load, so a reconnect cannot lose the retry ceiling or reason.
	Recovery *RecoveryState `json:"recovery,omitempty"`
	// ActiveDuration excludes time spent waiting for a human gate.
	ActiveDuration time.Duration
	ActiveStarted  time.Time
	// SourceFingerprint binds accepted results to the repository state that
	// produced them. Empty is retained for rows created before this check.
	SourceFingerprint string
	GrantUSD          float64
	Started           time.Time
	Ended             time.Time
	// Closed is set when the workflow reached its end, whatever the steps
	// did. An open run is one somebody may still resume.
	Closed bool
	Stop   Stop
	// WriterPID is the Atenea that owns this run. It answers the only
	// question a resume must not guess at: whether somebody else is running
	// this right now.
	WriterPID      int
	WatchdogState  string
	LastProgressAt time.Time
	Steps          []StepRow
	// Activity contains durable pre-invocation notices, oldest first. Cursor
	// lets a client reconnect and request only entries it has not displayed.
	Activity        []ActivityNotice
	ActivityCursor  int64
	ActivityHasMore bool
	PlanRevision    int
	Points          []PlanPoint
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

// RecoveryStepState is the per-step recovery receipt. Keeping this scoped to a
// step/route prevents a status reader from combining two different models into
// one apparently observed route after a reconnect.
type RecoveryStepState struct {
	StepID         string `json:"step_id"`
	RetryUsed      int    `json:"retry_used"`
	RetryLimit     int    `json:"retry_limit"`
	Reason         string `json:"reason,omitempty"`
	NextOrStopped  string `json:"next_or_stopped,omitempty"`
	RequestedModel string `json:"requested_model,omitempty"`
	ObservedModel  string `json:"observed_model,omitempty"`
	Backend        string `json:"backend,omitempty"`
	RecoveryKind   string `json:"recovery_kind,omitempty"`
}

// RecoveryState is the small audit view exposed by workflow status. RetryUsed
// counts all durable automatic dispatch repeats, including recovery and review
// corrections. The per-step receipt is authoritative when a run has multiple
// routes.
type RecoveryState struct {
	RetryUsed      int                 `json:"retry_used"`
	RetryLimit     int                 `json:"retry_limit"`
	Reason         string              `json:"reason"`
	NextOrStopped  string              `json:"next_or_stopped,omitempty"`
	RequestedModel string              `json:"requested_model"`
	ObservedModel  string              `json:"observed_model"`
	Backend        string              `json:"backend"`
	Steps          []RecoveryStepState `json:"steps,omitempty"`
}

// RecoveryState derives the retry receipt from the workflow record. It does
// not infer a successful retry from an empty index or from a provider status;
// only archived attempts and the current step's persisted reason count.
func (r Run) RecoveryState() RecoveryState {
	state := RecoveryState{RetryLimit: r.Policy.MaxRetries}
	identity := ""
	var reason string
	for _, row := range r.Steps {
		stepState := RecoveryStepState{StepID: row.Step.ID, RetryLimit: r.Policy.MaxRetries}
		stepState.RecoveryKind = row.RecoveryKind
		if row.Step.Route != nil {
			stepState.RequestedModel = row.Step.Route.RequestedModel
			if stepState.RequestedModel == "" {
				stepState.RequestedModel = row.Step.Route.Model
			}
			stepState.ObservedModel = row.Step.Route.ObservedModel
			stepState.Backend = row.Step.Route.Backend
			key := stepState.RequestedModel + "\x00" + stepState.ObservedModel + "\x00" + stepState.Backend
			if identity == "" {
				identity = key
				state.RequestedModel, state.ObservedModel, state.Backend = stepState.RequestedModel, stepState.ObservedModel, stepState.Backend
			} else if identity != key {
				// The legacy scalar fields cannot honestly represent more than one
				// route. Consumers must use Steps in that case.
				state.RequestedModel, state.ObservedModel, state.Backend = "", "", ""
			}
		}
		stepState.Reason = strings.TrimSpace(row.RecoveryReason)
		if stepState.Reason == "" {
			stepState.Reason = strings.TrimSpace(row.Reason.Text)
		}
		for _, attempt := range r.Superseded {
			if attempt.StepID != row.Step.ID {
				continue
			}
			if automaticRecoveryKind(attempt.RecoveryKind) {
				stepState.RetryUsed++
			}
			if stepState.Reason == "" && strings.TrimSpace(attempt.RecoveryReason) != "" {
				stepState.Reason = strings.TrimSpace(attempt.RecoveryReason)
			}
		}
		// A crash may occur after the marker is written and before Reset files
		// the replaced row. Count that marker once; an archived row already
		// represents the same automatic retry.
		if automaticRecoveryKind(row.RecoveryKind) && !hasArchivedRecovery(r.Superseded, row.Step.ID, row.Attempt-1) {
			stepState.RetryUsed++
		}
		if stepState.RetryUsed == 0 && row.Attempt > 1 && row.RecoveryKind == "" {
			// Rows written before recovery origins existed are conservatively
			// bounded by their old dispatch count. New rows never infer Redo
			// from Attempt-1.
			stepState.RetryUsed = row.Attempt - 1
		}
		state.RetryUsed += stepState.RetryUsed
		if row.CutAtItsCeiling() {
			stepState.Reason = "step spending ceiling reached"
		} else if row.InvokedKnown && !row.Invoked {
			stepState.Reason = "provider preflight did not invoke a provider"
		} else if row.InvokedKnown && row.Invoked && row.Spent.USD == nil && row.Status == StatusIncomplete {
			stepState.Reason = "provider invoked with unknown cost"
		}
		if stepState.Reason != "" {
			if reason == "" {
				reason = stepState.Reason
			} else if reason != stepState.Reason {
				reason = "multiple step recovery outcomes; inspect steps"
			}
		}
		if row.Status == StatusIncomplete && stepState.Reason != "" {
			stepState.NextOrStopped = "stopped:" + stepState.Reason
		} else if stepState.RetryUsed < r.Policy.MaxRetries {
			stepState.NextOrStopped = "next"
		}
		state.Steps = append(state.Steps, stepState)
	}
	state.Reason = reason
	if r.Stop != StopNone {
		state.NextOrStopped = "stopped:" + string(r.Stop)
	} else if state.RetryUsed >= state.RetryLimit && state.RetryLimit >= 0 && state.RetryUsed > 0 {
		state.NextOrStopped = "stopped:automatic retry limit reached"
	} else if state.RetryUsed > 0 {
		state.NextOrStopped = "next"
	}
	return state
}

func automaticRecoveryKind(kind string) bool {
	return strings.HasPrefix(kind, "automatic")
}

func hasArchivedRecovery(attempts []AttemptRow, stepID string, attempt int) bool {
	if attempt <= 0 {
		return false
	}
	for _, archived := range attempts {
		if archived.StepID == stepID && archived.Attempt == attempt && automaticRecoveryKind(archived.RecoveryKind) {
			return true
		}
	}
	return false
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
	// SourceFingerprint is captured at claim time and is immutable evidence of
	// the repository state that produced this attempt. It must not be rebound
	// to a later run-level fingerprint after acceptance.
	SourceFingerprint string
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
	// Notices are caveats attached to the report. They are durable because a
	// workflow status request may be served by a different process after the
	// dispatch that produced the report has ended.
	Notices []string
	// Spent is what this step was charged. Its zero value reads as
	// unmeasured -- see [contract.Charge.Measured] -- which is the ordinary
	// case today: the agent report wire carries no money and nothing on
	// this machine can report a charge. A step that did report, even a real
	// zero (a $0.00 turn with a priced_by beside it), is measured, and the
	// two must never collapse into the same "$0.00" on a receipt -- the
	// same lie as list-price cost on subscription traffic.
	Spent contract.Charge
	// Invocation evidence is persisted with the outcome. Known false means a
	// preflight failure before provider start; known true with an unknown
	// charge means the provider may already have spent money.
	Invoked        bool
	InvokedKnown   bool
	RecoveryKind   string
	RecoveryReason string
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
	// ObservedSteps and EstimatedSteps separate provider-observed money from
	// conservative allowance pricing. UnknownSteps includes unmeasured rows
	// and measured rows without a monetary observation; neither can release a
	// reservation.
	ObservedSteps  int
	EstimatedSteps int
	UnknownSteps   int
	ObservedUSD    *float64
	EstimatedUSD   *float64
	// TruncatedSteps is how many of MeasuredSteps were charged with no token
	// count behind them -- see [StepRow.Truncated]. They are counted as
	// measured because their dollars are real; they are counted again here
	// because Tokens is a lower bound while any of them exist, and a total
	// that cannot say so is the same lie in a different column.
	TruncatedSteps int
	// SupersededAttempts is every replaced dispatch, while SupersededUSD is
	// the total for those with a monetary observation. Held apart from the
	// step totals on purpose: a
	// superseded attempt is not a step, and folding it into MeasuredSteps
	// would double-count the step it belongs to in every per-step figure that
	// reads this -- CostByType and the admission rule among them. It is money
	// the grant paid all the same, so a balance that omits it is wrong.
	SupersededAttempts int
	SupersededUSD      float64
	// These counters classify archived attempts separately from live steps.
	// Keeping the live counters above unchanged preserves their step-level
	// meaning, while status readers can add these values to expose the money
	// and uncertainty accumulated across retries.
	SupersededObservedSteps  int
	SupersededEstimatedSteps int
	SupersededUnknownSteps   int
	SupersededObservedUSD    *float64
	SupersededEstimatedUSD   *float64
	Tokens                   int
	USD                      *float64
	PricedBy                 []string
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
	StepID            string
	Attempt           int
	TraceID           string
	SourceFingerprint string
	Status            Status
	Verdict           contract.Verdict
	Reason            contract.Reason
	// GrantUSD is the share THIS attempt ran under, which is the figure a
	// later attempt may have been given more of.
	GrantUSD       float64
	Spent          contract.Charge
	Invoked        bool
	InvokedKnown   bool
	RecoveryKind   string
	RecoveryReason string
	// Completeness is nil when the attempt made no claim -- and it is nil on
	// every attempt cut at its ceiling, because a turn that never answered
	// never reported coverage.
	Completeness *float64
	StoppedAt    string
	Result       map[string]any
	Notices      []string
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
	var observedUSD, estimatedUSD float64
	var supersededObservedUSD, supersededEstimatedUSD float64
	fullyPriced := true
	labels := map[string]bool{}
	for _, step := range r.Steps {
		if !step.Spent.Measured() {
			out.UnmeasuredSteps++
			out.UnknownSteps++
			continue
		}
		out.MeasuredSteps++
		if step.Truncated() {
			out.TruncatedSteps++
		}
		out.Tokens += step.Spent.Tokens()
		if step.Spent.USD == nil {
			fullyPriced = false
			out.UnknownSteps++
			continue
		}
		usd += *step.Spent.USD
		labels[step.Spent.PricedBy] = true
		if estimatedPrice(step.Spent.PricedBy) {
			out.EstimatedSteps++
			estimatedUSD += *step.Spent.USD
		} else {
			out.ObservedSteps++
			observedUSD += *step.Spent.USD
		}
	}
	// The archive contributes dollars and never tokens: an attempt that was
	// cut kept a token record its own receipt already contradicts (measured
	// 2026-08-16: $0.62 against $0.02 of tokens, 30x), so adding those counts
	// to a total would import the error the live rows were fixed of.
	for _, attempt := range r.Superseded {
		out.SupersededAttempts++
		if !attempt.Spent.Measured() || attempt.Spent.USD == nil {
			out.SupersededUnknownSteps++
			continue
		}
		if estimatedPrice(attempt.Spent.PricedBy) {
			out.SupersededEstimatedSteps++
			supersededEstimatedUSD += *attempt.Spent.USD
		} else {
			out.SupersededObservedSteps++
			supersededObservedUSD += *attempt.Spent.USD
		}
		out.SupersededUSD += *attempt.Spent.USD
		labels[attempt.Spent.PricedBy] = true
	}
	if out.MeasuredSteps > 0 && fullyPriced {
		out.USD = &usd
	}
	if out.ObservedSteps > 0 {
		out.ObservedUSD = &observedUSD
	}
	if out.EstimatedSteps > 0 {
		out.EstimatedUSD = &estimatedUSD
	}
	if out.SupersededObservedSteps > 0 {
		out.SupersededObservedUSD = &supersededObservedUSD
	}
	if out.SupersededEstimatedSteps > 0 {
		out.SupersededEstimatedUSD = &supersededEstimatedUSD
	}
	out.PricedBy = sortedKeys(labels)
	return out
}

// AccumulatedUSD is the complete priced spend, including attempts a retry or
// redo replaced. A nil result means at least one live or archived attempt has
// no monetary observation, so returning a partial total would understate the
// run.
func (s Spend) AccumulatedUSD() *float64 {
	if s.USD == nil || s.SupersededUnknownSteps > 0 {
		return nil
	}
	total := *s.USD + s.SupersededUSD
	return &total
}

func estimatedPrice(pricedBy string) bool {
	for _, source := range strings.Split(pricedBy, " and ") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(source)), "estimate:") {
			return true
		}
	}
	return false
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
CREATE TABLE IF NOT EXISTS workflow_reservation (
 workflow_id TEXT NOT NULL, trace_id TEXT NOT NULL, reserved_usd REAL NOT NULL,
 PRIMARY KEY(workflow_id, trace_id)
);

CREATE TABLE IF NOT EXISTS workflow_activity (
 cursor INTEGER PRIMARY KEY AUTOINCREMENT,
 workflow_id TEXT NOT NULL,
	point_id TEXT NOT NULL DEFAULT '',
	agent_run_id TEXT NOT NULL DEFAULT '',
 invocation_id TEXT NOT NULL,
	thread_id TEXT NOT NULL DEFAULT '',
	turn_id TEXT NOT NULL DEFAULT '',
	usage_revision INTEGER NOT NULL DEFAULT 0,
	requested_model TEXT NOT NULL DEFAULT '',
	observed_model TEXT NOT NULL DEFAULT '',
	requested_reasoning_effort TEXT NOT NULL DEFAULT '',
	observed_reasoning_effort TEXT NOT NULL DEFAULT '',
 kind TEXT NOT NULL,
 tool TEXT NOT NULL,
 action TEXT NOT NULL,
 objective TEXT NOT NULL,
 purpose TEXT NOT NULL,
 markdown TEXT NOT NULL,
 created_at TEXT NOT NULL,
 delivered_at TEXT NOT NULL DEFAULT '',
 UNIQUE(workflow_id, invocation_id),
 FOREIGN KEY (workflow_id) REFERENCES workflow(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS workflow_activity_cursor ON workflow_activity(workflow_id, cursor);

CREATE TABLE IF NOT EXISTS workflow_point (
 workflow_id TEXT NOT NULL, id TEXT NOT NULL, ordinal INTEGER NOT NULL,
 title TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending',
 evidence TEXT NOT NULL DEFAULT '[]', retired INTEGER NOT NULL DEFAULT 0,
 updated_at TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(workflow_id,id),
 FOREIGN KEY (workflow_id) REFERENCES workflow(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS workflow (
    id          TEXT    NOT NULL PRIMARY KEY,
    task        TEXT    NOT NULL,
    -- Which repository the run was about, resolved the same way every agent
    -- resolves it (WorkspaceFor). Empty on rows written before this column
    -- existed, and that emptiness is load-bearing: a cost read back from
    -- those rows is machine-wide and has to say so rather than claim a scope
    -- it cannot support.
    repository  TEXT    NOT NULL DEFAULT '',
    effects     TEXT    NOT NULL DEFAULT '[]',
    policy      TEXT    NOT NULL DEFAULT '{}',
    active_seconds REAL NOT NULL DEFAULT 0,
    active_started_at TEXT NOT NULL DEFAULT '',
    source_fingerprint TEXT NOT NULL DEFAULT '',
    plan_revision INTEGER NOT NULL DEFAULT 1,
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
    ,state      TEXT NOT NULL DEFAULT 'running'
    ,last_progress_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS workflow_step (
    workflow_id TEXT    NOT NULL,
    id          TEXT    NOT NULL,
    ordinal     INTEGER NOT NULL,
    type_name   TEXT    NOT NULL,
    point_id    TEXT    NOT NULL DEFAULT '',
    point_title TEXT    NOT NULL DEFAULT '',
    pool        TEXT    NOT NULL,
    objective   TEXT    NOT NULL,
    files       TEXT    NOT NULL DEFAULT '[]',
	criterion   TEXT    NOT NULL DEFAULT '',
	max_duration_ns INTEGER NOT NULL DEFAULT 0,
	max_tokens INTEGER NOT NULL DEFAULT 0,
    needs       TEXT    NOT NULL DEFAULT '[]',
    -- The step whose answer this one is handed, and how much of that outcome
    -- it demanded. Empty subject is the ordinary case: most steps are handed
    -- nothing. The bar is stored with it because a resumed run must apply the
    -- same one the author wrote, not today's default.
    subject     TEXT    NOT NULL DEFAULT '',
		 on_outcome  TEXT    NOT NULL DEFAULT 'answered',
		 effects     TEXT    NOT NULL DEFAULT '[]',
		 operations  TEXT    NOT NULL DEFAULT '[]',
		 route       TEXT    NOT NULL DEFAULT '',
    budget_estimate_usd REAL NOT NULL DEFAULT 0,
    budget_minimum_usd  REAL NOT NULL DEFAULT 0,
    budget_source TEXT NOT NULL DEFAULT '',
    grant_usd   REAL    NOT NULL DEFAULT 0,
    status      TEXT    NOT NULL,
    trace_id    TEXT    NOT NULL DEFAULT '',
	source_fingerprint TEXT NOT NULL DEFAULT '',
    attempt     INTEGER NOT NULL DEFAULT 0,
    writer_pid  INTEGER NOT NULL DEFAULT 0,
    started_at  TEXT    NOT NULL DEFAULT '',
    ended_at    TEXT    NOT NULL DEFAULT '',
    verdict     TEXT    NOT NULL DEFAULT '',
    reason_kind TEXT    NOT NULL DEFAULT '',
    reason_text TEXT    NOT NULL DEFAULT '',
    result      TEXT    NOT NULL DEFAULT '',
    discovered  TEXT    NOT NULL DEFAULT '',
    notices     TEXT    NOT NULL DEFAULT '[]',
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
	invoked     INTEGER NOT NULL DEFAULT 0,
	invoked_known INTEGER NOT NULL DEFAULT 0,
	recovery_kind TEXT NOT NULL DEFAULT '',
	recovery_reason TEXT NOT NULL DEFAULT '',
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
	source_fingerprint TEXT NOT NULL DEFAULT '',
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
	invoked     INTEGER NOT NULL DEFAULT 0,
	invoked_known INTEGER NOT NULL DEFAULT 0,
	recovery_kind TEXT NOT NULL DEFAULT '',
	recovery_reason TEXT NOT NULL DEFAULT '',
    completeness REAL,
    stopped_at  TEXT    NOT NULL DEFAULT '',
    -- The answer this attempt gave. A review that refuses one hands the
    -- sentence back to the retry, and that card is process-local today: a
    -- run resumed in another process cannot rebuild it. Kept here so it can.
    result      TEXT    NOT NULL DEFAULT '',
    notices     TEXT    NOT NULL DEFAULT '[]',
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

// Open opens (creating if needed) the store at path and migrates it. Immediate
// transactions take the shared trace database's write turn before reading, so
// a concurrent trace writer cannot make a later workflow update fail to upgrade.
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
		"&_txlock=immediate" +
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
	if _, err := db.ExecContext(ctx, telemetrySchema); err != nil {
		_ = db.Close()
		return nil, contract.Fail(contract.FailureUnavailable,
			"workflow: telemetry schema %s: %v", path, err)
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
		{"workflow", "effects", "ALTER TABLE workflow ADD COLUMN effects TEXT NOT NULL DEFAULT '[]'"},
		{"workflow", "policy", "ALTER TABLE workflow ADD COLUMN policy TEXT NOT NULL DEFAULT '{}'"},
		{"workflow", "active_seconds", "ALTER TABLE workflow ADD COLUMN active_seconds REAL NOT NULL DEFAULT 0"},
		{"workflow", "active_started_at", "ALTER TABLE workflow ADD COLUMN active_started_at TEXT NOT NULL DEFAULT ''"},
		{"workflow", "state", "ALTER TABLE workflow ADD COLUMN state TEXT NOT NULL DEFAULT 'running'"},
		{"workflow", "last_progress_at", "ALTER TABLE workflow ADD COLUMN last_progress_at TEXT NOT NULL DEFAULT ''"},
		{"workflow", "source_fingerprint", "ALTER TABLE workflow ADD COLUMN source_fingerprint TEXT NOT NULL DEFAULT ''"},
		{"workflow", "plan_revision", "ALTER TABLE workflow ADD COLUMN plan_revision INTEGER NOT NULL DEFAULT 1"},
		{"workflow_step", "completeness", "ALTER TABLE workflow_step ADD COLUMN completeness REAL"},
		{"workflow_step", "operations", "ALTER TABLE workflow_step ADD COLUMN operations TEXT NOT NULL DEFAULT '[]'"},
		{"workflow_step", "stopped_at", "ALTER TABLE workflow_step ADD COLUMN stopped_at TEXT NOT NULL DEFAULT ''"},
		{"workflow_step", "route", "ALTER TABLE workflow_step ADD COLUMN route TEXT NOT NULL DEFAULT ''"},
		{"workflow_step", "budget_estimate_usd", "ALTER TABLE workflow_step ADD COLUMN budget_estimate_usd REAL NOT NULL DEFAULT 0"},
		{"workflow_step", "budget_minimum_usd", "ALTER TABLE workflow_step ADD COLUMN budget_minimum_usd REAL NOT NULL DEFAULT 0"},
		{"workflow_step", "budget_source", "ALTER TABLE workflow_step ADD COLUMN budget_source TEXT NOT NULL DEFAULT ''"},
		{"workflow_step", "notices", "ALTER TABLE workflow_step ADD COLUMN notices TEXT NOT NULL DEFAULT '[]'"},
		{"workflow_step", "point_id", "ALTER TABLE workflow_step ADD COLUMN point_id TEXT NOT NULL DEFAULT ''"},
		{"workflow_step", "point_title", "ALTER TABLE workflow_step ADD COLUMN point_title TEXT NOT NULL DEFAULT ''"},
		{"workflow_step", "max_duration_ns", "ALTER TABLE workflow_step ADD COLUMN max_duration_ns INTEGER NOT NULL DEFAULT 0"},
		{"workflow_step", "max_tokens", "ALTER TABLE workflow_step ADD COLUMN max_tokens INTEGER NOT NULL DEFAULT 0"},
		{"workflow_attempt", "notices", "ALTER TABLE workflow_attempt ADD COLUMN notices TEXT NOT NULL DEFAULT '[]'"},
		{"workflow_step", "invoked", "ALTER TABLE workflow_step ADD COLUMN invoked INTEGER NOT NULL DEFAULT 0"},
		{"workflow_step", "invoked_known", "ALTER TABLE workflow_step ADD COLUMN invoked_known INTEGER NOT NULL DEFAULT 0"},
		{"workflow_attempt", "invoked", "ALTER TABLE workflow_attempt ADD COLUMN invoked INTEGER NOT NULL DEFAULT 0"},
		{"workflow_attempt", "invoked_known", "ALTER TABLE workflow_attempt ADD COLUMN invoked_known INTEGER NOT NULL DEFAULT 0"},
		{"workflow_step", "recovery_kind", "ALTER TABLE workflow_step ADD COLUMN recovery_kind TEXT NOT NULL DEFAULT ''"},
		{"workflow_step", "recovery_reason", "ALTER TABLE workflow_step ADD COLUMN recovery_reason TEXT NOT NULL DEFAULT ''"},
		{"workflow_step", "source_fingerprint", "ALTER TABLE workflow_step ADD COLUMN source_fingerprint TEXT NOT NULL DEFAULT ''"},
		{"workflow_attempt", "source_fingerprint", "ALTER TABLE workflow_attempt ADD COLUMN source_fingerprint TEXT NOT NULL DEFAULT ''"},
		{"workflow_attempt", "recovery_kind", "ALTER TABLE workflow_attempt ADD COLUMN recovery_kind TEXT NOT NULL DEFAULT ''"},
		{"workflow_attempt", "recovery_reason", "ALTER TABLE workflow_attempt ADD COLUMN recovery_reason TEXT NOT NULL DEFAULT ''"},
		{"workflow_activity", "delivered_at", "ALTER TABLE workflow_activity ADD COLUMN delivered_at TEXT NOT NULL DEFAULT ''"},
		{"workflow_activity", "point_id", "ALTER TABLE workflow_activity ADD COLUMN point_id TEXT NOT NULL DEFAULT ''"},
		{"workflow_activity", "agent_run_id", "ALTER TABLE workflow_activity ADD COLUMN agent_run_id TEXT NOT NULL DEFAULT ''"},
		{"workflow_activity", "thread_id", "ALTER TABLE workflow_activity ADD COLUMN thread_id TEXT NOT NULL DEFAULT ''"},
		{"workflow_activity", "turn_id", "ALTER TABLE workflow_activity ADD COLUMN turn_id TEXT NOT NULL DEFAULT ''"},
		{"workflow_activity", "usage_revision", "ALTER TABLE workflow_activity ADD COLUMN usage_revision INTEGER NOT NULL DEFAULT 0"},
		{"workflow_activity", "requested_model", "ALTER TABLE workflow_activity ADD COLUMN requested_model TEXT NOT NULL DEFAULT ''"},
		{"workflow_activity", "observed_model", "ALTER TABLE workflow_activity ADD COLUMN observed_model TEXT NOT NULL DEFAULT ''"},
		{"workflow_activity", "requested_reasoning_effort", "ALTER TABLE workflow_activity ADD COLUMN requested_reasoning_effort TEXT NOT NULL DEFAULT ''"},
		{"workflow_activity", "observed_reasoning_effort", "ALTER TABLE workflow_activity ADD COLUMN observed_reasoning_effort TEXT NOT NULL DEFAULT ''"},
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
	return s.CreateWithFingerprint(ctx, id, plan, repository, "", at, pid)
}

// CreateWithFingerprint writes a plan together with the source state it was
// planned against. The wrapper above preserves the store API for old callers
// and fixtures that do not have a repository root.
func (s *Store) CreateWithFingerprint(ctx context.Context, id string, plan Plan, repository, fingerprint string, at time.Time, pid int) error {
	return s.CreateWithPolicy(ctx, id, plan, repository, fingerprint, WorkflowPolicy{}, at, pid)
}

// CreateWithPolicy persists the immutable execution ceiling with the plan.
func (s *Store) CreateWithPolicy(ctx context.Context, id string, plan Plan, repository, fingerprint string, policy WorkflowPolicy, at time.Time, pid int) error {
	if err := policy.valid(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: begin %s", id)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workflow (id, task, repository, effects, policy, source_fingerprint, grant_usd, started_at, writer_pid, state, last_progress_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, plan.Graph.Task, repository, jsonEffects(plan.Graph.Effects()), jsonPolicy(policy), fingerprint, plan.Graph.GrantUSD, stamp(at), pid, StateRunning, stamp(at)); err != nil {
		return unavailable(err, "workflow: opening %s", id)
	}
	for i, step := range plan.Graph.Steps {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO workflow_step
			 (workflow_id, id, ordinal, type_name, point_id, point_title, pool, objective, files,
			  criterion, max_duration_ns, max_tokens, needs, subject, on_outcome, effects, operations, route,
			  budget_estimate_usd, budget_minimum_usd, budget_source, grant_usd, status)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, step.ID, i, step.TypeName, step.PointID, step.PointTitle, plan.Pools[step.ID].String(),
			step.Task.Objective, jsonList(step.Task.Files), step.Task.Criterion, step.Limits.MaxDuration.Nanoseconds(), step.Limits.MaxTokens,
			jsonList(step.Needs), step.Subject, step.On.String(),
			jsonEffects(step.Permission.Effects),
			jsonOperations(step.Permission.Operations),
			jsonRoute(step.Route),
			step.BudgetEstimateUSD, step.BudgetMinimumUSD, step.BudgetSource,
			step.Permission.BudgetUSD, StatusPending.String()); err != nil {
			return unavailable(err, "workflow: opening %s step %s", id, step.ID)
		}
		if step.PointID != "" {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_point
				(workflow_id,id,ordinal,title,state,evidence,updated_at) VALUES(?,?,?,?,?,?,?)`,
				id, step.PointID, i, step.PointTitle, PointPending, "[]", stamp(at)); err != nil {
				return unavailable(err, "workflow: opening %s point %s", id, step.PointID)
			}
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
    workflow_id, step_id, attempt, trace_id, source_fingerprint, status, verdict,
    reason_kind, reason_text, grant_usd, spent_usd,
    spent_input_tokens, spent_output_tokens,
    spent_cache_read_tokens, spent_cache_write_tokens, priced_by,
    invoked, invoked_known, recovery_kind, recovery_reason,
    completeness, stopped_at, result, notices, started_at, ended_at, replaced_at)
SELECT workflow_id, id, attempt, trace_id, source_fingerprint, status, verdict,
       reason_kind, reason_text, grant_usd, spent_usd,
       spent_input_tokens, spent_output_tokens,
       spent_cache_read_tokens, spent_cache_write_tokens, priced_by,
       invoked, invoked_known, recovery_kind, recovery_reason,
       completeness, stopped_at, result, notices, started_at, ended_at, ?
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
	attempt int, at time.Time, pid int, budget ...float64) error {
	return s.claim(ctx, id, stepID, traceID, attempt, at, pid, nil, budget...)
}

// ClaimWithActivity atomically saves the pre-invocation notice and the claim.
// A process can therefore never start from a claim whose intent was not saved.
func (s *Store) ClaimWithActivity(ctx context.Context, id, stepID, traceID string,
	attempt int, at time.Time, pid int, notice ActivityNotice, budget ...float64) error {
	return s.claim(ctx, id, stepID, traceID, attempt, at, pid, &notice, budget...)
}

func (s *Store) claim(ctx context.Context, id, stepID, traceID string,
	attempt int, at time.Time, pid int, notice *ActivityNotice, budget ...float64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: claiming %s step %s", id, stepID)
	}
	defer func() { _ = tx.Rollback() }()
	var stop string
	if err := tx.QueryRowContext(ctx, `SELECT stop FROM workflow WHERE id = ?`, id).Scan(&stop); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return contract.Fail(contract.FailureNotFound, "no workflow %s in %s", id, s.path)
		}
		return unavailable(err, "workflow: reading %s before claim", id)
	}
	if stop == string(StopAborted) {
		return contract.Fail(contract.FailureCanceled, "workflow %s was canceled before step %s started", id, stepID)
	}
	var sourceFingerprint string
	if err := tx.QueryRowContext(ctx, `SELECT source_fingerprint FROM workflow WHERE id=?`, id).Scan(&sourceFingerprint); err != nil {
		return unavailable(err, "workflow: reading source fingerprint before claim")
	}

	if len(budget) > 0 {
		// Take the SQLite writer lock before reading the balance.
		if _, err := tx.ExecContext(ctx, `UPDATE workflow SET grant_usd=grant_usd WHERE id=?`, id); err != nil {
			return err
		}
		for _, table := range []string{"workflow_attempt", "workflow_step"} {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_reservation SELECT workflow_id,trace_id,CASE WHEN spent_usd IS NOT NULL AND TRIM(COALESCE(priced_by,'')) <> '' AND LOWER(priced_by) NOT LIKE '%estimate:%' THEN spent_usd ELSE grant_usd END FROM `+table+` WHERE workflow_id=? AND trace_id<>''`, id); err != nil {
				return err
			}
		}
		// Observed spend replaces a reservation; unknown spend keeps its full hold.
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_reservation SET reserved_usd=(SELECT spent_usd FROM workflow_step WHERE workflow_id=? AND trace_id=workflow_reservation.trace_id) WHERE workflow_id=? AND EXISTS (SELECT 1 FROM workflow_step WHERE workflow_id=? AND trace_id=workflow_reservation.trace_id AND status<>'running' AND spent_usd IS NOT NULL AND TRIM(COALESCE(priced_by,'')) <> '' AND LOWER(priced_by) NOT LIKE '%estimate:%')`, id, id, id); err != nil {
			return err
		}
		var grant, used float64
		if err := tx.QueryRowContext(ctx, `SELECT grant_usd FROM workflow WHERE id=?`, id).Scan(&grant); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(reserved_usd),0) FROM workflow_reservation WHERE workflow_id=?`, id).Scan(&used); err != nil {
			return err
		}
		if budget[0] < 0 || used+budget[0] > grant+1e-9 {
			return contract.Fail(contract.FailurePermissionDenied, "workflow budget exhausted: grant %.4f, spent/reserved %.4f, requested %.4f", grant, used, budget[0])
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_reservation VALUES(?,?,?)`, id, traceID, budget[0]); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, fileAttempt, stamp(at), id, stepID); err != nil {
		return unavailable(err, "workflow: filing %s step %s attempt", id, stepID)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE workflow_step
		 SET status = ?, trace_id = ?, source_fingerprint = ?, attempt = ?, writer_pid = ?, started_at = ?,
		     ended_at = '', verdict = '', reason_kind = '', reason_text = '',
		     result = '', discovered = '', spent_usd = NULL,
		     spent_input_tokens = NULL, spent_output_tokens = NULL,
		     spent_cache_read_tokens = NULL, spent_cache_write_tokens = NULL,
			priced_by = '', completeness = NULL, stopped_at = '', invoked = 0, invoked_known = 0
		 WHERE workflow_id = ? AND id = ?`,
		StatusRunning.String(), traceID, sourceFingerprint, attempt, pid, stamp(at), id, stepID); err != nil {
		return unavailable(err, "workflow: claiming %s step %s", id, stepID)
	}
	if notice != nil {
		notice.WorkflowID, notice.InvocationID, notice.At = id, traceID, at
		if _, err := recordActivity(ctx, tx, *notice); err != nil {
			return unavailable(err, "workflow: recording activity for %s step %s", id, stepID)
		}
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err, "workflow: claiming %s step %s", id, stepID)
	}
	return nil
}

func recordActivity(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, notice ActivityNotice) (bool, error) {
	result, err := exec.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_activity
		(workflow_id,point_id,agent_run_id,invocation_id,thread_id,turn_id,usage_revision,requested_model,observed_model,requested_reasoning_effort,observed_reasoning_effort,kind,tool,action,objective,purpose,markdown,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, notice.WorkflowID, notice.PointID, notice.AgentRunID, notice.InvocationID,
		notice.ThreadID, notice.TurnID, notice.UsageRevision, notice.RequestedModel, notice.ObservedModel, notice.RequestedReasoningEffort, notice.ObservedReasoningEffort, notice.Kind,
		notice.Tool, notice.Action, notice.Objective, notice.Purpose, notice.Markdown, stamp(notice.At))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

// RecordActivity saves a non-dispatch intent, such as an approved plan change.
func (s *Store) RecordActivity(ctx context.Context, notice ActivityNotice) error {
	_, err := s.RecordActivityOnce(ctx, notice)
	return err
}

// RecordActivityOnce reports whether this invocation was newly inserted.
func (s *Store) RecordActivityOnce(ctx context.Context, notice ActivityNotice) (bool, error) {
	if notice.WorkflowID == "" || notice.InvocationID == "" {
		return false, contract.Fail(contract.FailureInvalidInput, "workflow activity requires workflow and invocation ids")
	}
	if notice.At.IsZero() {
		notice.At = time.Now()
	}
	inserted, err := recordActivity(ctx, s.db, notice)
	if err != nil {
		return false, unavailable(err, "workflow: recording activity for %s", notice.WorkflowID)
	}
	return inserted, nil
}

// ActivityByInvocation returns the durable identity assigned at insert time.
func (s *Store) ActivityByInvocation(ctx context.Context, workflowID, invocationID string) (ActivityNotice, error) {
	row := s.db.QueryRowContext(ctx, `SELECT cursor,workflow_id,point_id,agent_run_id,invocation_id,thread_id,turn_id,usage_revision,requested_model,observed_model,requested_reasoning_effort,observed_reasoning_effort,kind,tool,action,objective,purpose,markdown,created_at
		FROM workflow_activity WHERE workflow_id=? AND invocation_id=?`, workflowID, invocationID)
	var n ActivityNotice
	var at string
	if err := row.Scan(&n.Cursor, &n.WorkflowID, &n.PointID, &n.AgentRunID, &n.InvocationID, &n.ThreadID, &n.TurnID, &n.UsageRevision, &n.RequestedModel, &n.ObservedModel, &n.RequestedReasoningEffort, &n.ObservedReasoningEffort, &n.Kind, &n.Tool, &n.Action,
		&n.Objective, &n.Purpose, &n.Markdown, &at); err != nil {
		return ActivityNotice{}, unavailable(err, "workflow: reading activity %s for %s", invocationID, workflowID)
	}
	n.At = parseStamp(at)
	return n, nil
}

// ToolActivitiesByAgentRun returns every internal tool intent belonging to a
// completed physical agent invocation. Finish enriches these rows with the
// observed turn identity before telemetry reads them.
func (s *Store) ToolActivitiesByAgentRun(ctx context.Context, workflowID, agentRunID string) ([]ActivityNotice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cursor,workflow_id,point_id,agent_run_id,invocation_id,thread_id,turn_id,usage_revision,requested_model,observed_model,requested_reasoning_effort,observed_reasoning_effort,kind,tool,action,objective,purpose,markdown,created_at
		FROM workflow_activity WHERE workflow_id=? AND agent_run_id=? AND invocation_id<>agent_run_id ORDER BY cursor`, workflowID, agentRunID)
	if err != nil {
		return nil, unavailable(err, "workflow: reading tool activity for %s", agentRunID)
	}
	defer func() { _ = rows.Close() }()
	var out []ActivityNotice
	for rows.Next() {
		var notice ActivityNotice
		var at string
		if err := rows.Scan(&notice.Cursor, &notice.WorkflowID, &notice.PointID, &notice.AgentRunID, &notice.InvocationID, &notice.ThreadID, &notice.TurnID, &notice.UsageRevision, &notice.RequestedModel, &notice.ObservedModel, &notice.RequestedReasoningEffort, &notice.ObservedReasoningEffort, &notice.Kind, &notice.Tool, &notice.Action, &notice.Objective, &notice.Purpose, &notice.Markdown, &at); err != nil {
			return nil, unavailable(err, "workflow: scanning tool activity for %s", agentRunID)
		}
		notice.At = parseStamp(at)
		out = append(out, notice)
	}
	return out, rows.Err()
}

// PendingActivities returns durable notices that have not received a
// successful publication acknowledgement yet.
func (s *Store) PendingActivities(ctx context.Context, workflowID string, limit int) ([]ActivityNotice, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT cursor,workflow_id,point_id,agent_run_id,invocation_id,thread_id,turn_id,usage_revision,requested_model,observed_model,requested_reasoning_effort,observed_reasoning_effort,kind,tool,action,objective,purpose,markdown,created_at
		FROM workflow_activity WHERE workflow_id=? AND delivered_at='' ORDER BY cursor LIMIT ?`, workflowID, limit)
	if err != nil {
		return nil, unavailable(err, "workflow: reading pending activity for %s", workflowID)
	}
	defer func() { _ = rows.Close() }()
	out := make([]ActivityNotice, 0)
	for rows.Next() {
		var n ActivityNotice
		var at string
		if err := rows.Scan(&n.Cursor, &n.WorkflowID, &n.PointID, &n.AgentRunID, &n.InvocationID, &n.ThreadID, &n.TurnID, &n.UsageRevision, &n.RequestedModel, &n.ObservedModel, &n.RequestedReasoningEffort, &n.ObservedReasoningEffort, &n.Kind, &n.Tool, &n.Action,
			&n.Objective, &n.Purpose, &n.Markdown, &at); err != nil {
			return nil, unavailable(err, "workflow: reading pending activity for %s", workflowID)
		}
		n.At = parseStamp(at)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err, "workflow: reading pending activity for %s", workflowID)
	}
	return out, nil
}

// MarkActivitiesDelivered records the acknowledgement after publication.
func (s *Store) MarkActivitiesDelivered(ctx context.Context, workflowID string, invocationIDs []string, at time.Time) error {
	if len(invocationIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: acknowledging activity for %s", workflowID)
	}
	defer func() { _ = tx.Rollback() }()
	for _, invocationID := range invocationIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_activity SET delivered_at=?
			WHERE workflow_id=? AND invocation_id=? AND delivered_at=''`, stamp(at), workflowID, invocationID); err != nil {
			return unavailable(err, "workflow: acknowledging activity for %s", workflowID)
		}
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err, "workflow: acknowledging activity for %s", workflowID)
	}
	return nil
}

// Activities returns one bounded page after an opaque durable cursor, oldest first.
func (s *Store) Activities(ctx context.Context, workflowID string, after int64, limit int) ([]ActivityNotice, int64, error) {
	out, cursor, _, err := s.ActivitiesPage(ctx, workflowID, after, limit)
	return out, cursor, err
}

// ActivitiesPage also says whether another durable page exists. It reads one
// extra row so clients never infer completeness from a truncated response.
func (s *Store) ActivitiesPage(ctx context.Context, workflowID string, after int64, limit int) ([]ActivityNotice, int64, bool, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT cursor,workflow_id,point_id,agent_run_id,invocation_id,thread_id,turn_id,usage_revision,requested_model,observed_model,requested_reasoning_effort,observed_reasoning_effort,kind,tool,action,objective,purpose,markdown,created_at
		FROM workflow_activity WHERE workflow_id=? AND cursor>? ORDER BY cursor LIMIT ?`, workflowID, after, limit+1)
	if err != nil {
		return nil, after, false, unavailable(err, "workflow: reading activity for %s", workflowID)
	}
	defer func() { _ = rows.Close() }()
	out, cursor := []ActivityNotice{}, after
	for rows.Next() {
		var n ActivityNotice
		var at string
		if err := rows.Scan(&n.Cursor, &n.WorkflowID, &n.PointID, &n.AgentRunID, &n.InvocationID, &n.ThreadID, &n.TurnID, &n.UsageRevision, &n.RequestedModel, &n.ObservedModel, &n.RequestedReasoningEffort, &n.ObservedReasoningEffort, &n.Kind, &n.Tool, &n.Action,
			&n.Objective, &n.Purpose, &n.Markdown, &at); err != nil {
			return nil, after, false, unavailable(err, "workflow: reading activity for %s", workflowID)
		}
		n.At = parseStamp(at)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, after, false, unavailable(err, "workflow: reading activity for %s", workflowID)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	if len(out) > 0 {
		cursor = out[len(out)-1].Cursor
	}
	return out, cursor, hasMore, nil
}

// Finish writes what a step ended as, together with the answer it gave.
func (s *Store) Finish(ctx context.Context, id, stepID string, status Status,
	report contract.Report, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: starting settlement")
	}
	defer func() { _ = tx.Rollback() }()
	if err := finishStep(ctx, tx, id, stepID, status, report, at); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err, "workflow: committing settlement")
	}
	return nil
}

// finishStep atomically records an outcome and settles its reservation.
func finishStep(ctx context.Context, tx *sql.Tx, id, stepID string, status Status, report contract.Report, at time.Time) error {
	var typeName, routeJSON, traceID, pointID string
	if err := tx.QueryRowContext(ctx, `SELECT type_name, route, trace_id, point_id FROM workflow_step WHERE workflow_id=? AND id=?`, id, stepID).Scan(&typeName, &routeJSON, &traceID, &pointID); err != nil {
		return unavailable(err, "workflow: reading %s step %s before settlement", id, stepID)
	}
	route, err := readRoute(routeJSON)
	if err != nil {
		return err
	}
	if route != nil {
		requestedModel := strings.TrimSpace(report.RequestedModel)
		if requestedModel != "" && route.RequestedModel != "" && requestedModel != route.RequestedModel {
			return contract.Fail(contract.FailureInvalidInput, "workflow: %s step %s: reported requested model %q differs from route %q", id, stepID, requestedModel, route.RequestedModel)
		}
		requestedEffort := strings.TrimSpace(report.RequestedReasoningEffort)
		if requestedEffort != "" && route.RequestedReasoningEffort != "" && requestedEffort != route.RequestedReasoningEffort {
			return contract.Fail(contract.FailureInvalidInput, "workflow: %s step %s: reported requested effort %q differs from route %q", id, stepID, requestedEffort, route.RequestedReasoningEffort)
		}
		if report.ObservedModel != "" {
			route.ObservedModel = report.ObservedModel
		}
		if report.ObservedReasoningEffort != "" {
			route.ObservedReasoningEffort = report.ObservedReasoningEffort
		}
		if report.ThreadID != "" {
			route.ThreadID = report.ThreadID
		}
		routeJSON = jsonRoute(route)
	}
	if status == StatusOK && report.Verdict != contract.VerdictOK {
		return contract.Fail(contract.FailureInvalidInput, "workflow: %s step %s: ok status requires an ok report", id, stepID)
	}
	role := strings.ToLower(strings.TrimSpace(typeName))
	if status == StatusOK && (role == "review" || role == "audit") && len(report.Result) == 0 {
		return contract.Fail(contract.FailureInvalidInput, "workflow: %s step %s: %s reports cannot be empty", id, stepID, role)
	}
	if err := report.Spent.Validate(); err != nil {
		return err
	}
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
	_, err = tx.ExecContext(ctx,
		`UPDATE workflow_step
		 SET status = ?, ended_at = ?, verdict = ?, reason_kind = ?, reason_text = ?,
			 result = ?, discovered = ?, notices = ?, writer_pid = 0, spent_usd = ?,
		     spent_input_tokens = ?, spent_output_tokens = ?,
		     spent_cache_read_tokens = ?, spent_cache_write_tokens = ?, priced_by = ?,
		     invoked = ?, invoked_known = ?,
		     completeness = ?, stopped_at = ?, route = ?
		 WHERE workflow_id = ? AND id = ?`,
		status.String(), stamp(at), report.Verdict.String(),
		report.Reason.Kind.String(), report.Reason.Text,
		result, jsonDiscoveries(report.Discovered), jsonList(report.Notices), usd,
		input, output, cacheRead, cacheWrite, pricedBy,
		boolInt(report.Invoked), boolInt(report.InvokedKnown),
		completeness, report.StoppedAt, routeJSON, id, stepID)
	if err != nil {
		return unavailable(err, "workflow: closing %s step %s", id, stepID)
	}
	requestedModel, observedModel := report.RequestedModel, report.ObservedModel
	requestedEffort, observedEffort := report.RequestedReasoningEffort, report.ObservedReasoningEffort
	if route != nil {
		if requestedModel == "" {
			requestedModel = route.RequestedModel
		}
		if observedModel == "" {
			observedModel = route.ObservedModel
		}
		if requestedEffort == "" {
			requestedEffort = route.RequestedReasoningEffort
		}
		if observedEffort == "" {
			observedEffort = route.ObservedReasoningEffort
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_activity SET point_id=?,agent_run_id=?,thread_id=?,turn_id=?,usage_revision=?,requested_model=?,observed_model=?,requested_reasoning_effort=?,observed_reasoning_effort=? WHERE workflow_id=? AND (invocation_id=? OR agent_run_id=?)`,
		pointID, traceID, report.ThreadID, report.TurnID, report.UsageRevision, requestedModel, observedModel, requestedEffort, observedEffort, id, traceID, traceID); err != nil {
		return unavailable(err, "workflow: correlating activity for %s step %s", id, stepID)
	}
	if observedCharge(report.Spent) {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_reservation SET reserved_usd=? WHERE workflow_id=? AND trace_id=(SELECT trace_id FROM workflow_step WHERE workflow_id=? AND id=?)`, *report.Spent.USD, id, id, stepID); err != nil {
			return unavailable(err, "workflow: settling reservation")
		}
	}
	return nil
}

// observedCharge is the only charge allowed to settle a reservation. An
// allowance estimate is useful evidence for a report but cannot prove what
// the provider billed, so it keeps the original reservation and blocks a
// paid retry until the caller expands or authorizes the budget.
func observedCharge(spent contract.Charge) bool {
	return spent.USD != nil && strings.TrimSpace(spent.PricedBy) != "" && !estimatedPrice(spent.PricedBy)
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

// InterruptBeforeDispatch records that a claimed invocation never started and
// releases its budget reservation. The durable activity remains as evidence
// of the attempted publication; resume gives the step a new invocation id.
func (s *Store) InterruptBeforeDispatch(ctx context.Context, id, stepID, traceID, why string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: interrupting unpublished %s step %s", id, stepID)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_reservation WHERE workflow_id=? AND trace_id=?`, id, traceID); err != nil {
		return unavailable(err, "workflow: releasing unpublished %s step %s", id, stepID)
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_step
		SET status=?, ended_at=?, verdict='', reason_kind=?, reason_text=?, writer_pid=0
		WHERE workflow_id=? AND id=? AND trace_id=? AND status=?`,
		StatusInterrupted.String(), stamp(at), contract.FailureUnavailable.String(), why,
		id, stepID, traceID, StatusRunning.String())
	if err != nil {
		return unavailable(err, "workflow: interrupting unpublished %s step %s", id, stepID)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return contract.Fail(contract.FailureUnavailable, "workflow: unpublished claim changed before it could be interrupted")
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err, "workflow: interrupting unpublished %s step %s", id, stepID)
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
			priced_by = '', completeness = NULL, stopped_at = '', invoked = 0, invoked_known = 0
		 WHERE workflow_id = ? AND id = ?`,
		StatusPending.String(), id, stepID); err != nil {
		return unavailable(err, "workflow: resetting %s step %s", id, stepID)
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err, "workflow: resetting %s step %s", id, stepID)
	}
	return nil
}

// MarkRecovery records why the current dispatch is being replaced before
// Reset files it. Claim intentionally preserves these columns, so a later
// successful retry still carries the outage/timeout that caused it and a
// reconnect can distinguish automatic recovery from an explicit Redo.
func (s *Store) MarkRecovery(ctx context.Context, id, stepID, kind, reason string) error {
	if strings.TrimSpace(kind) == "" {
		return contract.Fail(contract.FailureInvalidInput, "workflow recovery kind is required")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_step SET recovery_kind=?, recovery_reason=? WHERE workflow_id=? AND id=?`, kind, reason, id, stepID)
	if err != nil {
		return unavailable(err, "workflow: recording recovery for %s step %s", id, stepID)
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
		 WHERE id = ? AND writer_pid = ? AND stop <> 'aborted'`, pid, id, held)
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
		if current.Stop == StopAborted {
			return contract.Fail(contract.FailureCanceled,
				"workflow %s was canceled before it could start", id)
		}
		return contract.Fail(contract.FailureUnavailable,
			"workflow %s was taken over by pid %d while this one was starting it",
			id, current.WriterPID)
	}
	return nil
}

// ResumeOwn is the explicit counterpart to Own. It is used only by resume:
// clearing an aborted marker is an operator action, whereas ordinary launch
// and run must lose a race with cancellation rather than erase it.
func (s *Store) ResumeOwn(ctx context.Context, id string, pid, held int, expected Stop) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE workflow SET writer_pid = ?, closed = 0, ended_at = '', stop = ''
		 WHERE id = ? AND writer_pid = ? AND closed = 0 AND stop = ?`, pid, id, held, expected)
	if err != nil {
		return unavailable(err, "workflow: resuming %s", id)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return unavailable(err, "workflow: resuming %s", id)
	}
	if rows == 0 {
		current, loadErr := s.Load(ctx, id)
		if loadErr != nil {
			return loadErr
		}
		if current.Stop == StopAborted {
			return contract.Fail(contract.FailureCanceled,
				"workflow %s was canceled while it was being resumed", id)
		}
		return contract.Fail(contract.FailureUnavailable,
			"workflow %s was taken over by pid %d while resuming it", id, current.WriterPID)
	}
	return nil
}

// ReleaseOwn drops a durable resume claim. If stop is still empty, restore is
// written back; that preserves an abort which ResumeOwn cleared before a
// pre-execution validation failed. A cancellation arriving after the claim
// wins the CASE and is never overwritten.
func (s *Store) ReleaseOwn(ctx context.Context, id string, pid int, restore Stop) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE workflow SET writer_pid = 0,
			stop = CASE WHEN stop = '' THEN ? ELSE stop END
		 WHERE id = ? AND writer_pid = ?`, string(restore), id, pid)
	if err != nil {
		return unavailable(err, "workflow: releasing %s", id)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return unavailable(err, "workflow: releasing %s", id)
	} else if rows == 0 {
		current, loadErr := s.Load(ctx, id)
		if loadErr != nil {
			return loadErr
		}
		if current.WriterPID != 0 && current.WriterPID != pid {
			return contract.Fail(contract.FailureUnavailable,
				"workflow %s was taken over by pid %d while releasing it", id, current.WriterPID)
		}
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
	if run.Policy.MaxBudgetUSD > 0 && usd > run.Policy.MaxBudgetUSD+moneyEpsilon {
		return contract.Fail(contract.FailurePermissionDenied,
			"workflow %s policy budget ceiling is $%.4f; requested grant is $%.4f",
			id, run.Policy.MaxBudgetUSD, usd)
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
		`UPDATE workflow SET
			closed = CASE WHEN stop = 'aborted' THEN 0 ELSE ? END,
			ended_at = ?,
			stop = CASE WHEN stop = 'aborted' THEN 'aborted' ELSE ? END,
			state = CASE WHEN state IN ('attention_required','uncertain') THEN state WHEN ? = '' THEN 'completed' ELSE 'stopped' END,
			last_progress_at = ?,
			writer_pid = 0 WHERE id = ?`,
		closed, stamp(at), string(stop), string(stop), stamp(at), id)
	if err != nil {
		return unavailable(err, "workflow: closing %s", id)
	}
	return nil
}

// Cancel durably requests that a workflow stop. It is idempotent: a second
// request returns the persisted snapshot and does not disturb a finished run.
// A running engine observes the marker and cancels its in-process context;
// resume may explicitly clear it after taking ownership.
func (s *Store) Cancel(ctx context.Context, id string, at time.Time) (Run, error) {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE workflow SET stop = 'aborted', closed = 0, ended_at = ?
		 WHERE id = ? AND closed = 0 AND stop <> 'aborted'`, stamp(at), id); err != nil {
		return Run{}, unavailable(err, "workflow: canceling %s", id)
	}
	if _, _, _, err := s.SyncPlan(ctx, id, at); err != nil {
		return Run{}, err
	}
	return s.Load(ctx, id)
}

// Aborted is the narrow read used by an executing engine's cancellation
// watcher. It avoids loading all steps merely to observe the durable marker.
func (s *Store) Aborted(ctx context.Context, id string) (bool, error) {
	var stop string
	if err := s.db.QueryRowContext(ctx, `SELECT stop FROM workflow WHERE id = ?`, id).Scan(&stop); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, contract.Fail(contract.FailureNotFound, "no workflow %s in %s", id, s.path)
		}
		return false, unavailable(err, "workflow: reading cancellation for %s", id)
	}
	return stop == string(StopAborted), nil
}

// SetSourceFingerprint advances the source evidence after a step with an
// authorized write effect has been accepted. The update is deliberately
// explicit: read-only results keep the original fingerprint so an external
// edit cannot be mistaken for work performed by the workflow.
func (s *Store) SetSourceFingerprint(ctx context.Context, id, fingerprint string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE workflow SET source_fingerprint = ? WHERE id = ?`, fingerprint, id)
	if err != nil {
		return unavailable(err, "workflow: recording source state for %s", id)
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return contract.Fail(contract.FailureNotFound, "no workflow %s in %s", id, s.path)
	}
	return nil
}

// SetAcceptedWriteFingerprint advances both the workflow and the writer's
// evidence atomically. Later review and audit claims inherit this same tree.
func (s *Store) SetAcceptedWriteFingerprint(ctx context.Context, id, stepID, fingerprint string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: recording accepted write source")
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE workflow_step SET source_fingerprint=? WHERE workflow_id=? AND id=? AND status='ok'`, fingerprint, id, stepID)
	if err != nil {
		return unavailable(err, "workflow: recording %s step %s source", id, stepID)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return contract.Fail(contract.FailureInvalidInput, "workflow %s step %s is not an accepted write", id, stepID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow SET source_fingerprint=? WHERE id=?`, fingerprint, id); err != nil {
		return unavailable(err, "workflow: recording source state for %s", id)
	}
	return tx.Commit()
}

// BindKnowledgeCandidate adds the host-derived candidate identity to an
// already accepted implementation result without replacing any agent output.
func (s *Store) BindKnowledgeCandidate(ctx context.Context, id, stepID, candidateID, digest string) error {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT result FROM workflow_step WHERE workflow_id=? AND id=? AND status='ok'`, id, stepID).Scan(&raw); err != nil {
		return unavailable(err, "workflow: reading %s step %s knowledge result", id, stepID)
	}
	result := readMap(raw)
	if result == nil {
		result = make(map[string]any)
	}
	result["knowledge_candidate_id"], result["knowledge_digest"] = candidateID, digest
	encoded, err := jsonMap(result)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_step SET result=? WHERE workflow_id=? AND id=? AND status='ok'`, encoded, id, stepID)
	return err
}

// RejectKnowledgeCapture prevents a malformed explicit candidate from leaving
// its implementation accepted. Later reviews must not inherit that result.
func (s *Store) RejectKnowledgeCapture(ctx context.Context, id, stepID string, cause error) error {
	result, err := s.db.ExecContext(ctx, `UPDATE workflow_step SET status='failed',verdict='failed',reason_kind=?,reason_text=? WHERE workflow_id=? AND id=? AND status='ok'`, contract.FailureInvalidInput.String(), "knowledge candidate rejected: "+cause.Error(), id, stepID)
	if err != nil {
		return unavailable(err, "workflow: rejecting %s step %s knowledge candidate", id, stepID)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return contract.Fail(contract.FailureInvalidInput, "workflow %s step %s is not an accepted implementation", id, stepID)
	}
	return nil
}

// TouchProgress advances the watchdog cursor after a durable workflow event.
func (s *Store) TouchProgress(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workflow SET last_progress_at=? WHERE id=? AND closed=0`, stamp(at), id)
	if err != nil {
		return unavailable(err, "workflow: recording progress for %s", id)
	}
	return nil
}

// Watchdog marks a stalled workflow before canceling its active context. It
// never retries: uncertainty is a human decision, especially when a running
// step carried a write, external, or device effect.
func (s *Store) Watchdog(ctx context.Context, id string, now time.Time, timeout time.Duration) (Run, bool, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	run, err := s.Load(ctx, id)
	if err != nil {
		return Run{}, false, err
	}
	// A workflow can spend an arbitrary amount of time waiting for a human
	// gate.  That interval is deliberately excluded from the watchdog: there
	// is no provider effect whose outcome could become uncertain while the
	// active interval is closed.
	if run.Closed || run.WatchdogState != StateRunning || run.ActiveStarted.IsZero() || run.LastProgressAt.IsZero() || now.Sub(run.LastProgressAt) < timeout {
		return run, false, nil
	}
	uncertain := false
	for _, step := range run.Steps {
		if step.Status != StatusRunning {
			continue
		}
		for _, effect := range step.Step.Permission.Effects {
			if effect == contract.EffectWrite || effect == contract.EffectExternal || effect == contract.EffectDevice {
				uncertain = true
			}
		}
	}
	state := StateAttentionRequired
	if uncertain {
		state = StateUncertain
	}
	_, err = s.db.ExecContext(ctx, `UPDATE workflow SET state=?, stop=?, closed=0, ended_at=?, last_progress_at=? WHERE id=? AND closed=0 AND state='running'`, state, string(StopUnjudged), stamp(now), stamp(now), id)
	if err != nil {
		return Run{}, false, unavailable(err, "workflow: watchdog %s", id)
	}
	run, err = s.Load(ctx, id)
	return run, true, err
}

// StartActive closes any abandoned active interval and starts a new one for
// the current owner. Gate waiting is therefore excluded from the deadline.
func (s *Store) StartActive(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workflow SET
		active_seconds = active_seconds + CASE WHEN active_started_at <> '' THEN MAX(0, (julianday(?) - julianday(active_started_at)) * 86400) ELSE 0 END,
		active_started_at = ? WHERE id = ?`, stamp(at), stamp(at), id)
	if err != nil {
		return unavailable(err, "workflow: starting active interval for %s", id)
	}
	return nil
}

// EndActive closes the current interval durably. It is idempotent when a
// crashed process was already recovered by StartActive.
func (s *Store) EndActive(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workflow SET
		active_seconds = active_seconds + CASE WHEN active_started_at <> '' THEN MAX(0, (julianday(?) - julianday(active_started_at)) * 86400) ELSE 0 END,
		active_started_at = '' WHERE id = ?`, stamp(at), id)
	if err != nil {
		return unavailable(err, "workflow: ending active interval for %s", id)
	}
	return nil
}

// Load reads one run back whole.
func (s *Store) Load(ctx context.Context, id string) (Run, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task, repository, effects, policy, active_seconds, active_started_at, source_fingerprint, plan_revision, grant_usd, started_at, ended_at, closed, stop, writer_pid, state, last_progress_at
		 FROM workflow WHERE id = ?`, id)
	var (
		out                               Run
		started, ended, progressAt        string
		closed                            int
		stop                              string
		effects, policyRaw, activeStarted string
		activeSeconds                     float64
	)
	switch err := row.Scan(&out.ID, &out.Task, &out.Repository, &effects, &policyRaw, &activeSeconds, &activeStarted, &out.SourceFingerprint, &out.PlanRevision, &out.GrantUSD, &started, &ended,
		&closed, &stop, &out.WriterPID, &out.WatchdogState, &progressAt); {
	case errors.Is(err, sql.ErrNoRows):
		return Run{}, contract.Fail(contract.FailureNotFound,
			"no workflow %s in %s", id, s.path)
	case err != nil:
		return Run{}, unavailable(err, "workflow: reading %s", id)
	}
	out.Started = parseStamp(started)
	out.Ended = parseStamp(ended)
	out.ActiveDuration = time.Duration(activeSeconds * float64(time.Second))
	out.ActiveStarted = parseStamp(activeStarted)
	out.Closed = closed != 0
	out.Stop = Stop(stop)
	out.LastProgressAt = parseStamp(progressAt)
	if out.WatchdogState == "" {
		out.WatchdogState = StateRunning
	}
	var err error
	if out.Effects, err = readEffects(effects); err != nil {
		return Run{}, err
	}
	if out.Policy, err = readPolicy(policyRaw); err != nil {
		return Run{}, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type_name, point_id, point_title, pool, objective, files, criterion, max_duration_ns, max_tokens, needs, subject,
				on_outcome, effects, operations, route, budget_estimate_usd, budget_minimum_usd, budget_source,
				grant_usd, status, trace_id, source_fingerprint, attempt, writer_pid,
		        started_at, ended_at, verdict, reason_kind, reason_text, result,
		        discovered, notices, spent_usd, spent_input_tokens, spent_output_tokens,
				spent_cache_read_tokens, spent_cache_write_tokens, priced_by, invoked, invoked_known, recovery_kind, recovery_reason,
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
	if len(out.Effects) == 0 {
		// Rows written before the run-level ceiling existed derive it from the
		// immutable step permissions. New rows always persist it at Create.
		seen := make(map[contract.Effect]bool)
		for _, row := range out.Steps {
			for _, effect := range row.Step.Permission.Effects {
				if !seen[effect] {
					seen[effect] = true
					out.Effects = append(out.Effects, effect)
				}
			}
		}
		slices.Sort(out.Effects)
	}
	if len(out.Policy.Effects) == 0 {
		out.Policy.Effects = slices.Clone(out.Effects)
	}
	if len(out.Policy.Operations) == 0 {
		seen := make(map[contract.Operation]bool)
		for _, row := range out.Steps {
			for _, operation := range row.Step.Permission.Operations {
				seen[operation] = true
			}
		}
		for operation := range seen {
			out.Policy.Operations = append(out.Policy.Operations, operation)
		}
		slices.SortFunc(out.Policy.Operations, func(a, b contract.Operation) int { return strings.Compare(a.String(), b.String()) })
	}
	// Closed before the next query rather than at return: the archive read
	// below would otherwise hold a second connection open for the length of
	// this one, and WAL only makes that work, not free.
	if err := rows.Close(); err != nil {
		return Run{}, unavailable(err, "workflow: reading %s steps", id)
	}
	points, err := s.PlanPoints(ctx, id)
	if err != nil {
		return Run{}, err
	}
	out.Points = points
	// The archive is read here rather than left to a caller who remembers to
	// ask, because Run.Spend is the only place a balance comes from and it
	// cannot see money it was not handed. Empty for every run nobody redid,
	// which is almost all of them -- one indexed lookup on workflow_id.
	superseded, err := s.Attempts(ctx, id, "")
	if err != nil {
		return Run{}, err
	}
	out.Superseded = superseded
	recovery := out.RecoveryState()
	out.Recovery = &recovery
	activity, cursor, hasMore, err := s.ActivitiesPage(ctx, id, 0, 200)
	if err != nil {
		return Run{}, err
	}
	out.Activity, out.ActivityCursor, out.ActivityHasMore = activity, cursor, hasMore
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
	query := `SELECT step_id, attempt, trace_id, source_fingerprint, status, verdict, reason_kind,
	                 reason_text, grant_usd, spent_usd, spent_input_tokens,
	                 spent_output_tokens, spent_cache_read_tokens,
		                 spent_cache_write_tokens, priced_by, invoked, invoked_known, recovery_kind, recovery_reason, completeness,
	                 stopped_at, result, notices, started_at, ended_at, replaced_at
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
		out                          AttemptRow
		status, verdict              string
		reasonKind, reasonText       string
		usd                          sql.NullFloat64
		inputTok, outputTok          sql.NullInt64
		cacheReadTok, cacheWriteTok  sql.NullInt64
		pricedBy                     string
		invoked, invokedKnown        int
		recoveryKind, recoveryReason string
		completeness                 sql.NullFloat64
		result, notices              string
		started, ended, replaced     string
	)
	if err := rows.Scan(&out.StepID, &out.Attempt, &out.TraceID, &out.SourceFingerprint, &status, &verdict,
		&reasonKind, &reasonText, &out.GrantUSD, &usd, &inputTok, &outputTok,
		&cacheReadTok, &cacheWriteTok, &pricedBy, &invoked, &invokedKnown, &recoveryKind, &recoveryReason, &completeness,
		&out.StoppedAt, &result, &notices, &started, &ended, &replaced); err != nil {
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
	out.Invoked, out.InvokedKnown = invoked != 0, invokedKnown != 0
	out.RecoveryKind, out.RecoveryReason = recoveryKind, recoveryReason
	if completeness.Valid {
		v := completeness.Float64
		out.Completeness = &v
	}
	out.Result = readMap(result)
	out.Notices = readList(notices)
	out.Started = parseStamp(started)
	out.Ended = parseStamp(ended)
	out.Replaced = parseStamp(replaced)
	return out, nil
}

// List names the runs, newest first.
func (s *Store) List(ctx context.Context, limit int) ([]Run, error) {
	query := `SELECT id, task, grant_usd, started_at, ended_at, closed, stop, writer_pid, state, last_progress_at
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
			run                        Run
			started, ended, progressAt string
			closed                     int
			stop                       string
		)
		if err := rows.Scan(&run.ID, &run.Task, &run.GrantUSD, &started, &ended,
			&closed, &stop, &run.WriterPID, &run.WatchdogState, &progressAt); err != nil {
			return nil, unavailable(err, "workflow: list")
		}
		run.Started = parseStamp(started)
		run.Ended = parseStamp(ended)
		run.Closed = closed != 0
		run.Stop = Stop(stop)
		run.LastProgressAt = parseStamp(progressAt)
		if run.WatchdogState == "" {
			run.WatchdogState = StateRunning
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err, "workflow: list")
	}
	return out, nil
}

func scanStep(rows *sql.Rows) (StepRow, error) {
	var (
		out                                               StepRow
		pool, files, needs, effects, operations, routeRaw string
		onOutcome                                         string
		status, verdict                                   string
		reasonKind, reasonText                            string
		result, discovered, notices                       string
		started, ended                                    string
		usd                                               sql.NullFloat64
		budgetEstimate, budgetMinimum                     float64
		budgetSource                                      string
		inputTok, outputTok                               sql.NullInt64
		cacheReadTok, cacheWriteTok                       sql.NullInt64
		pricedBy                                          string
		invoked, invokedKnown                             int
		recoveryKind, recoveryReason                      string
		completeness                                      sql.NullFloat64
		maxDurationNS                                     int64
		maxTokens                                         int
		stoppedAt                                         string
	)
	if err := rows.Scan(&out.Step.ID, &out.Step.TypeName, &out.Step.PointID, &out.Step.PointTitle, &pool,
		&out.Step.Task.Objective, &files, &out.Step.Task.Criterion, &maxDurationNS, &maxTokens, &needs,
		&out.Step.Subject, &onOutcome, &effects, &operations, &routeRaw,
		&budgetEstimate, &budgetMinimum, &budgetSource,
		&out.Step.Permission.BudgetUSD, &status, &out.TraceID, &out.SourceFingerprint, &out.Attempt,
		&out.WriterPID, &started, &ended, &verdict, &reasonKind, &reasonText,
		&result, &discovered, &notices, &usd,
		&inputTok, &outputTok, &cacheReadTok, &cacheWriteTok, &pricedBy,
		&invoked, &invokedKnown, &recoveryKind, &recoveryReason,
		&completeness, &stoppedAt); err != nil {
		return StepRow{}, unavailable(err, "workflow: reading a step")
	}
	out.Step.BudgetEstimateUSD = budgetEstimate
	out.Step.Limits = contract.Limits{MaxDuration: time.Duration(maxDurationNS), MaxTokens: maxTokens}
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
	if out.Step.Permission.Operations, err = readOperations(operations); err != nil {
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
	out.Notices = readList(notices)
	out.Spent = contract.Charge{
		InputTokens:      int(inputTok.Int64),
		OutputTokens:     int(outputTok.Int64),
		CacheReadTokens:  int(cacheReadTok.Int64),
		CacheWriteTokens: int(cacheWriteTok.Int64),
		PricedBy:         pricedBy,
	}
	out.Invoked, out.InvokedKnown = invoked != 0, invokedKnown != 0
	out.RecoveryKind, out.RecoveryReason = recoveryKind, recoveryReason
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

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
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

func jsonOperations(operations []contract.Operation) string {
	names := make([]string, 0, len(operations))
	for _, operation := range operations {
		names = append(names, operation.String())
	}
	return jsonList(names)
}

func readOperations(raw string) ([]contract.Operation, error) {
	names := readList(raw)
	out := make([]contract.Operation, 0, len(names))
	for _, name := range names {
		operation, err := contract.ParseOperation(name)
		if err != nil {
			return nil, err
		}
		out = append(out, operation)
	}
	return out, nil
}

func jsonPolicy(policy WorkflowPolicy) string {
	raw, err := json.Marshal(struct {
		Name              string   `json:"name,omitempty"`
		Version           string   `json:"version,omitempty"`
		Digest            string   `json:"digest,omitempty"`
		Criterion         string   `json:"criterion,omitempty"`
		Effects           []string `json:"effects,omitempty"`
		Operations        []string `json:"operations,omitempty"`
		MaxBudgetUSD      float64  `json:"max_budget_usd,omitempty"`
		MaxDurationNS     int64    `json:"max_duration_ns,omitempty"`
		MaxTokens         int      `json:"max_tokens,omitempty"`
		MaxRetries        int      `json:"max_retries,omitempty"`
		MaxParallelAgent  int      `json:"max_parallel_agent,omitempty"`
		MaxParallelReview int      `json:"max_parallel_review,omitempty"`
	}{
		Name: policy.Name, Version: policy.Version, Digest: policy.Digest, Criterion: policy.Criterion,
		Effects: effectNames(policy.Effects), Operations: operationNames(policy.Operations), MaxBudgetUSD: policy.MaxBudgetUSD,
		MaxDurationNS: policy.MaxDuration.Nanoseconds(), MaxTokens: policy.MaxTokens, MaxRetries: policy.MaxRetries,
		MaxParallelAgent: policy.MaxParallelAgent, MaxParallelReview: policy.MaxParallelReview,
	})
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func readPolicy(raw string) (WorkflowPolicy, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return WorkflowPolicy{}, nil
	}
	var wire struct {
		Name              string   `json:"name"`
		Version           string   `json:"version"`
		Digest            string   `json:"digest"`
		Criterion         string   `json:"criterion"`
		Effects           []string `json:"effects"`
		Operations        []string `json:"operations"`
		MaxBudgetUSD      float64  `json:"max_budget_usd"`
		MaxDurationNS     int64    `json:"max_duration_ns"`
		MaxTokens         int      `json:"max_tokens"`
		MaxRetries        int      `json:"max_retries"`
		MaxParallelAgent  int      `json:"max_parallel_agent"`
		MaxParallelReview int      `json:"max_parallel_review"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return WorkflowPolicy{}, contract.Fail(contract.FailureUnavailable, "workflow: reading policy: %v", err)
	}
	effects, err := readEffects(jsonList(wire.Effects))
	if err != nil {
		return WorkflowPolicy{}, err
	}
	operations, err := readOperations(jsonList(wire.Operations))
	if err != nil {
		return WorkflowPolicy{}, err
	}
	policy := WorkflowPolicy{Name: wire.Name, Version: wire.Version, Digest: wire.Digest, Criterion: wire.Criterion, MaxBudgetUSD: wire.MaxBudgetUSD,
		Effects: effects, Operations: operations,
		MaxDuration: time.Duration(wire.MaxDurationNS), MaxTokens: wire.MaxTokens, MaxRetries: wire.MaxRetries,
		MaxParallelAgent: wire.MaxParallelAgent, MaxParallelReview: wire.MaxParallelReview}
	if err := policy.valid(); err != nil {
		return WorkflowPolicy{}, err
	}
	return policy, nil
}

func effectNames(values []contract.Effect) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.String())
	}
	return out
}

func operationNames(values []contract.Operation) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.String())
	}
	return out
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
