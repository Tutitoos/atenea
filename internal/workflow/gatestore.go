package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// gateWire is the proposal as the record holds it.
//
// Its own shape rather than the Step struct, for the same reason the digest
// writes its fields out by hand: a stored JSON of a Go type turns every
// rename into a migration, and this one has to be readable by an Atenea built
// after the one that wrote it.
type gateWire struct {
	Steps []struct {
		ID                string     `json:"id"`
		Agent             string     `json:"agent"`
		Objective         string     `json:"objective"`
		Files             []string   `json:"files,omitempty"`
		Criterion         string     `json:"criterion,omitempty"`
		Needs             []string   `json:"needs,omitempty"`
		Subject           string     `json:"subject,omitempty"`
		On                string     `json:"on,omitempty"`
		Effects           []string   `json:"effects,omitempty"`
		Route             *routeWire `json:"route,omitempty"`
		BudgetEstimateUSD float64    `json:"budget_estimate_usd,omitempty"`
		BudgetMinimumUSD  float64    `json:"budget_minimum_usd,omitempty"`
		BudgetSource      string     `json:"budget_source,omitempty"`
		BudgetUSD         float64    `json:"budget_usd"`
	} `json:"steps"`
	Replaces []string `json:"replaces,omitempty"`
}

func encodeProposal(p Proposal) (string, error) {
	var wire gateWire
	for _, step := range p.Steps {
		effects := make([]string, 0, len(step.Permission.Effects))
		for _, effect := range step.Permission.Effects {
			effects = append(effects, effect.String())
		}
		wire.Steps = append(wire.Steps, struct {
			ID                string     `json:"id"`
			Agent             string     `json:"agent"`
			Objective         string     `json:"objective"`
			Files             []string   `json:"files,omitempty"`
			Criterion         string     `json:"criterion,omitempty"`
			Needs             []string   `json:"needs,omitempty"`
			Subject           string     `json:"subject,omitempty"`
			On                string     `json:"on,omitempty"`
			Effects           []string   `json:"effects,omitempty"`
			Route             *routeWire `json:"route,omitempty"`
			BudgetEstimateUSD float64    `json:"budget_estimate_usd,omitempty"`
			BudgetMinimumUSD  float64    `json:"budget_minimum_usd,omitempty"`
			BudgetSource      string     `json:"budget_source,omitempty"`
			BudgetUSD         float64    `json:"budget_usd"`
		}{
			ID: step.ID, Agent: step.TypeName, Objective: step.Task.Objective,
			Files: step.Task.Files, Criterion: step.Task.Criterion,
			Needs: step.Needs, Subject: step.Subject, On: step.On.String(),
			Effects: effects, Route: routeForGate(step.Route),
			BudgetEstimateUSD: step.BudgetEstimateUSD, BudgetMinimumUSD: step.BudgetMinimumUSD,
			BudgetSource: step.BudgetSource, BudgetUSD: step.Permission.BudgetUSD,
		})
	}
	wire.Replaces = p.Replaces
	raw, err := json.Marshal(wire)
	if err != nil {
		return "", contract.Fail(contract.FailureInvalidInput,
			"workflow: encoding proposal: %v", err)
	}
	return string(raw), nil
}

func decodeProposal(raw string) (Proposal, error) {
	var wire gateWire
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Proposal{}, contract.Fail(contract.FailureUnavailable,
			"workflow: reading proposal: %v", err)
	}
	out := Proposal{Replaces: wire.Replaces}
	for _, s := range wire.Steps {
		// An absent `on` is the default, the same as everywhere else it
		// is written. Only a word nobody recognizes is an error.
		on := OnAnswered
		if strings.TrimSpace(s.On) != "" {
			parsed, err := ParseRequirement(s.On)
			if err != nil {
				return Proposal{}, err
			}
			on = parsed
		}
		effects := make([]contract.Effect, 0, len(s.Effects))
		for _, name := range s.Effects {
			effect, err := contract.ParseEffect(name)
			if err != nil {
				return Proposal{}, err
			}
			effects = append(effects, effect)
		}
		out.Steps = append(out.Steps, Step{
			ID: s.ID, TypeName: s.Agent,
			Task:    contract.Task{Objective: s.Objective, Files: s.Files, Criterion: s.Criterion},
			Needs:   s.Needs,
			Subject: s.Subject, On: on,
			Permission:        contract.Permission{Effects: effects, BudgetUSD: s.BudgetUSD},
			BudgetEstimateUSD: s.BudgetEstimateUSD, BudgetMinimumUSD: s.BudgetMinimumUSD,
			BudgetSource: s.BudgetSource,
			Route:        routeFromGate(s.Route),
		})
	}
	return out, nil
}

// Ask opens a gate: it writes the question down and returns without waiting.
//
// The question lives in the record rather than on a channel, which is what
// lets it outlive the Atenea that asked. A gate whose process died is still a
// gate; the next `atenea workflow resume` takes the run over and finds it
// exactly where it was left.
//
// A proposal may only replace steps that have not STARTED. Not "not
// executed" -- a running step has begun to touch the world, and replanning it
// would be a decision about work already underway. Together with the freeze
// in the engine (nothing new dispatches while a gate is open) this makes a
// stale approval impossible to construct rather than a race to detect: no
// step named in a proposal can change state while somebody reads it.
func (s *Store) Ask(ctx context.Context, runID string, kind Kind, p Proposal, at time.Time) (Gate, error) {
	run, err := s.Load(ctx, runID)
	if err != nil {
		return Gate{}, err
	}
	// A finished run takes no more questions, with exactly one exception:
	// a redo is the act of reopening one. Refusing it here would leave the
	// operator with no way to dispatch a step that died -- which is the state
	// this project was in until 2026-08-16, when 150 steps had been cut at a
	// ceiling and 2 had ever been re-dispatched. The gate is written while the
	// run is still closed and the run is reopened after it is answered, so
	// there is no window in which a finished run is open with nothing blessing
	// the money.
	if run.Closed && kind != KindRedo {
		return Gate{}, contract.Fail(contract.FailureInvalidInput,
			"workflow %s already finished at %s", runID, run.Ended.Local().Format(time.RFC3339))
	}
	if open, ok, err := s.OpenGate(ctx, runID); err != nil {
		return Gate{}, err
	} else if ok {
		return open, contract.Fail(contract.FailureInvalidInput,
			"workflow %s: gate %d is already waiting", runID, open.Ordinal)
	}
	started := make(map[string]Status, len(run.Steps))
	for _, step := range run.Steps {
		started[step.Step.ID] = step.Status
	}
	for _, id := range p.Replaces {
		status, ok := started[id]
		if !ok {
			return Gate{}, contract.Fail(contract.FailureInvalidInput,
				"workflow %s: proposal replaces step %q, which is not in the graph", runID, id)
		}
		if status != StatusPending {
			return Gate{}, contract.Fail(contract.FailureInvalidInput,
				"workflow %s: proposal replaces step %q, which is %s: only a step that has not started may be replanned",
				runID, id, status)
		}
	}
	gates, err := s.Gates(ctx, runID)
	if err != nil {
		return Gate{}, err
	}
	// Expansions are counted, launches are not: the cap is on how far a
	// graph may grow, and the first plan is not growth.
	expansions := 0
	for _, g := range gates {
		if g.Kind == KindApprove {
			expansions++
		}
	}
	if kind == KindApprove && expansions >= MaxExpansions {
		return Gate{}, contract.Fail(contract.FailureInvalidInput,
			"workflow %s: expansions exhausted (%d of %d)", runID, expansions, MaxExpansions)
	}

	// Allocation, not spend. What the grant has left is what no step has
	// claimed; nothing on this machine can report a charge, so there is no
	// second number here and a run that has spent nothing looks exactly like
	// one that has spent everything. Refused here rather than at approval
	// time: a person should not be asked to bless a plan that cannot run.
	// A step the proposal itself names is not counted twice. The launch
	// gate is the whole graph, so without this every run would read as
	// asking for double its own plan.
	replaced := make(map[string]bool, len(p.Replaces)+len(p.Steps))
	for _, id := range p.Replaces {
		replaced[id] = true
	}
	for _, step := range p.Steps {
		replaced[step.ID] = true
	}
	var allocated float64
	for _, step := range run.Steps {
		if !replaced[step.Step.ID] {
			allocated += step.Step.Permission.BudgetUSD
		}
	}
	if want := allocated + p.AllocatedUSD(); want > run.GrantUSD+moneyEpsilon {
		left := run.GrantUSD - allocated
		// "Fully allocated" only when it is. A grant with half of itself
		// free, refusing a proposal that asks for more than the rest, is a
		// different fact and saying the stronger one would send a reader
		// looking for steps that do not exist.
		if left <= moneyEpsilon {
			return Gate{}, contract.Fail(contract.FailureInvalidInput,
				"workflow %s: grant fully allocated ($%.2f of $%.2f); this proposal asks for $%.2f more",
				runID, allocated, run.GrantUSD, p.AllocatedUSD())
		}
		return Gate{}, contract.Fail(contract.FailureInvalidInput,
			"workflow %s: this proposal asks for $%.2f and the grant has $%.2f left "+
				"($%.2f of $%.2f allocated)",
			runID, p.AllocatedUSD(), left, allocated, run.GrantUSD)
	}
	gate := Gate{
		RunID: runID, Ordinal: len(gates), Kind: kind,
		Proposal: p.Clone(), Digest: p.Digest(),
		Decision: DecisionWaiting, Asked: at.UTC(),
	}
	encoded, err := encodeProposal(gate.Proposal)
	if err != nil {
		return Gate{}, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workflow_gate (workflow_id, ordinal, kind, proposal, digest, decision, asked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		runID, gate.Ordinal, gate.Kind.String(), encoded, gate.Digest,
		gate.Decision.String(), stamp(gate.Asked))
	if err != nil {
		return Gate{}, unavailable(err, "workflow: opening gate on %s", runID)
	}
	return gate, nil
}

// Answer records a decision on the run's open gate.
//
// It refuses a gate that is already answered rather than overwriting it: two
// people answering the same question is a fact worth surfacing, and the
// second answer arriving silently would erase the first from the log that
// exists to show who said what.
//
// The read below is a courtesy, not the check. What decides the race is the
// UPDATE's own `decision = 'waiting'` predicate together with the row count it
// reports: between the read and the write there is nothing at all, so a second
// answer landing in that gap used to change zero rows, raise no error, and be
// handed back the Gate this call had assembled in memory -- carrying the
// loser's decision, the loser's hand and the loser's reason. Which is the
// exact opposite of what the paragraph above promises: `atenea workflow
// approve` printed its confirmation to the person whose answer was discarded,
// under a gate the record shows somebody else answered, possibly by rejecting
// it. Execute's own read of gate 0 is what still kept such a run from being
// dispatched; nothing kept the operator from being told otherwise.
//
// No transaction is needed for that guarantee. The UPDATE is the serialization
// point on its own; the earlier read only buys the better error message in the
// ordinary case where nobody is racing.
func (s *Store) Answer(ctx context.Context, runID string, ordinal int, d Decision, hand, reason string, at time.Time) (Gate, error) {
	if d == DecisionWaiting {
		return Gate{}, contract.Fail(contract.FailureInvalidInput,
			"workflow %s: waiting is not an answer", runID)
	}
	if d == DecisionRejected && strings.TrimSpace(reason) == "" {
		return Gate{}, contract.Fail(contract.FailureInvalidInput,
			"workflow %s: a rejection needs a reason", runID)
	}
	gate, err := s.Gate(ctx, runID, ordinal)
	if err != nil {
		return Gate{}, err
	}
	if !gate.Waiting() {
		return gate, alreadyAnswered(runID, ordinal, gate)
	}
	gate.Decision = d
	gate.Answered = at.UTC()
	gate.Hand = hand
	gate.Reason = reason
	result, err := s.db.ExecContext(ctx,
		`UPDATE workflow_gate SET decision = ?, answered_at = ?, hand = ?, reason = ?
		 WHERE workflow_id = ? AND ordinal = ? AND decision = 'waiting'`,
		gate.Decision.String(), stamp(gate.Answered), gate.Hand, gate.Reason, runID, ordinal)
	if err != nil {
		return Gate{}, unavailable(err, "workflow: answering gate %d on %s", ordinal, runID)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Gate{}, unavailable(err, "workflow: answering gate %d on %s", ordinal, runID)
	}
	if rows == 0 {
		// Somebody answered in the gap. Re-read rather than report the gate
		// this call assembled: the row is the only place the winning answer
		// exists, and naming who gave it and when is what lets the loser tell
		// a lost race from a gate that was never open.
		winner, err := s.Gate(ctx, runID, ordinal)
		if err != nil {
			return Gate{}, err
		}
		return winner, alreadyAnswered(runID, ordinal, winner)
	}
	return gate, nil
}

// alreadyAnswered is the refusal both of Answer's already-decided paths give,
// so the one a race produces and the one a plain re-answer produces cannot
// drift into saying different things about the same situation.
func alreadyAnswered(runID string, ordinal int, gate Gate) error {
	return contract.Fail(contract.FailureInvalidInput,
		"workflow %s: gate %d was already %s at %s by %s",
		runID, ordinal, gate.Decision, gate.Answered.Local().Format(time.RFC3339), gate.Hand)
}

// Gate reads one gate back.
func (s *Store) Gate(ctx context.Context, runID string, ordinal int) (Gate, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT workflow_id, ordinal, kind, proposal, digest, decision, asked_at, answered_at, hand, reason
		 FROM workflow_gate WHERE workflow_id = ? AND ordinal = ?`, runID, ordinal)
	gate, err := scanGate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Gate{}, contract.Fail(contract.FailureNotFound,
			"workflow %s has no gate %d", runID, ordinal)
	}
	return gate, err
}

// OpenGate is the run's waiting gate, if it has one. At most one is open at a
// time: a second question asked while the first is unanswered would let a
// person approve a graph built on an answer nobody gave.
func (s *Store) OpenGate(ctx context.Context, runID string) (Gate, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT workflow_id, ordinal, kind, proposal, digest, decision, asked_at, answered_at, hand, reason
		 FROM workflow_gate WHERE workflow_id = ? AND decision = 'waiting' ORDER BY ordinal LIMIT 1`, runID)
	gate, err := scanGate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Gate{}, false, nil
	}
	if err != nil {
		return Gate{}, false, err
	}
	return gate, true, nil
}

// Gates lists every gate on a run, in the order they were asked.
func (s *Store) Gates(ctx context.Context, runID string) ([]Gate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT workflow_id, ordinal, kind, proposal, digest, decision, asked_at, answered_at, hand, reason
		 FROM workflow_gate WHERE workflow_id = ? ORDER BY ordinal`, runID)
	if err != nil {
		return nil, unavailable(err, "workflow: reading gates on %s", runID)
	}
	defer func() { _ = rows.Close() }()
	var out []Gate
	for rows.Next() {
		gate, err := scanGate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, gate)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err, "workflow: reading gates on %s", runID)
	}
	return out, nil
}

// scanner is what Gate and Gates have in common: one row, however it arrived.
type scanner interface{ Scan(dest ...any) error }

func scanGate(row scanner) (Gate, error) {
	var (
		out                      Gate
		kind, proposal, decision string
		askedAt, answeredAt      string
	)
	if err := row.Scan(&out.RunID, &out.Ordinal, &kind, &proposal, &out.Digest,
		&decision, &askedAt, &answeredAt, &out.Hand, &out.Reason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Gate{}, err
		}
		return Gate{}, unavailable(err, "workflow: reading a gate")
	}
	var err error
	if out.Kind, err = ParseKind(kind); err != nil {
		return Gate{}, err
	}
	if out.Decision, err = ParseDecision(decision); err != nil {
		return Gate{}, err
	}
	if out.Proposal, err = decodeProposal(proposal); err != nil {
		return Gate{}, err
	}
	out.Asked = parseStamp(askedAt)
	out.Answered = parseStamp(answeredAt)
	return out, nil
}

// Apply puts an approved proposal into the graph.
//
// The digest is recomputed here, over exactly what is about to be written,
// and a difference is refused. An approval is of an artifact, not of a
// moment: a plan that changed between the reading and the running has not
// been approved, whatever the row says.
//
// Replaced steps are deleted rather than marked, because a step that never
// ran and never will is not a state a run should have to explain. What it was
// stays legible in the gate log, which holds the proposal that removed it.
func (s *Store) Apply(ctx context.Context, runID string, gate Gate, plan Plan) error {
	if gate.Decision != DecisionApproved {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow %s: gate %d is %s, not approved", runID, gate.Ordinal, gate.Decision)
	}
	if got := gate.Proposal.Digest(); got != gate.Digest {
		return contract.Fail(contract.FailureInvalidInput,
			"workflow %s: gate %d was approved as %s and now reads %s: "+
				"this is not the plan that was approved",
			runID, gate.Ordinal, Short(gate.Digest), Short(got))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: applying gate %d on %s", gate.Ordinal, runID)
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range gate.Proposal.Replaces {
		// Pending is checked again, inside the transaction. The freeze means
		// nothing can have started since the question was asked; this is the
		// guard on the freeze, and it costs one predicate.
		res, err := tx.ExecContext(ctx,
			`DELETE FROM workflow_step WHERE workflow_id = ? AND id = ? AND status = ?`,
			runID, id, StatusPending.String())
		if err != nil {
			return unavailable(err, "workflow: replacing %s on %s", id, runID)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return contract.Fail(contract.FailureInvalidInput,
				"workflow %s: step %s is no longer pending: only a step that has not started may be replanned",
				runID, id)
		}
	}
	var ordinal int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(ordinal), -1) + 1 FROM workflow_step WHERE workflow_id = ?`,
		runID).Scan(&ordinal); err != nil {
		return unavailable(err, "workflow: applying gate %d on %s", gate.Ordinal, runID)
	}
	for i, step := range gate.Proposal.Steps {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO workflow_step
			 (workflow_id, id, ordinal, type_name, pool, objective, files,
			  criterion, needs, subject, on_outcome, effects, route,
			  budget_estimate_usd, budget_minimum_usd, budget_source, grant_usd, status)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			runID, step.ID, ordinal+i, step.TypeName, plan.Pools[step.ID].String(),
			step.Task.Objective, jsonList(step.Task.Files), step.Task.Criterion,
			jsonList(step.Needs), step.Subject, step.On.String(),
			jsonEffects(step.Permission.Effects),
			jsonRoute(step.Route),
			step.BudgetEstimateUSD, step.BudgetMinimumUSD, step.BudgetSource,
			step.Permission.BudgetUSD, StatusPending.String()); err != nil {
			return unavailable(err, "workflow: adding %s to %s", step.ID, runID)
		}
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err, "workflow: applying gate %d on %s", gate.Ordinal, runID)
	}
	return nil
}
