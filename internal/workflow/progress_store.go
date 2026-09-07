package workflow

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// PlanPoints reads the durable, ordered checklist for a workflow.
func (s *Store) PlanPoints(ctx context.Context, id string) ([]PlanPoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,state,evidence,retired,updated_at
		FROM workflow_point WHERE workflow_id=? ORDER BY ordinal,id`, id)
	if err != nil {
		return nil, unavailable(err, "workflow: reading %s plan points", id)
	}
	defer func() { _ = rows.Close() }()
	var out []PlanPoint
	for rows.Next() {
		var point PlanPoint
		var state, evidence, updated string
		var retired int
		if err := rows.Scan(&point.ID, &point.Title, &state, &evidence, &retired, &updated); err != nil {
			return nil, unavailable(err, "workflow: reading %s plan point", id)
		}
		point.State, point.Evidence, point.Retired, point.Updated = PointState(state), readList(evidence), retired != 0, parseStamp(updated)
		out = append(out, point)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err, "workflow: reading %s plan points", id)
	}
	return out, nil
}

// SyncPlan derives checklist state only from durable step results, then saves
// every changed point in one transaction. Callers may publish only after it
// returns, so chat can never lead the record it describes.
func (s *Store) SyncPlan(ctx context.Context, id string, at time.Time) (PlanProgress, bool, string, error) {
	run, err := s.Load(ctx, id)
	if err != nil {
		return PlanProgress{}, false, "", err
	}
	derived := derivePlanPoints(run.Steps, run.Points, at)
	if run.Stop != StopNone {
		for index := range derived {
			if !derived[index].Retired && derived[index].State != PointAccepted {
				derived[index].State = PointBlocked
			}
		}
	}
	prior := make(map[string]PlanPoint, len(run.Points))
	for _, point := range run.Points {
		prior[point.ID] = point
	}
	changed := false
	events := make([]string, 0)
	for index := range derived {
		before, exists := prior[derived[index].ID]
		if exists && before.Title == derived[index].Title && before.State == derived[index].State &&
			before.Retired == derived[index].Retired && slices.Equal(before.Evidence, derived[index].Evidence) {
			derived[index].Updated = before.Updated
			continue
		}
		changed = true
		switch {
		case derived[index].State == PointAccepted:
			events = append(events, derived[index].ID+" completado")
		case exists && before.State == PointAccepted:
			events = append(events, derived[index].ID+" reabierto")
		case derived[index].State == PointBlocked:
			events = append(events, derived[index].ID+" bloqueado")
		case !exists || derived[index].Retired != before.Retired:
			events = append(events, "alcance actualizado")
		}
	}
	progress := PlanProgress{Revision: run.PlanRevision, Points: derived}
	if !changed {
		return progress, false, "", nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanProgress{}, false, "", unavailable(err, "workflow: syncing %s plan", id)
	}
	defer func() { _ = tx.Rollback() }()
	for ordinal, point := range derived {
		retired := 0
		if point.Retired {
			retired = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_point
			(workflow_id,id,ordinal,title,state,evidence,retired,updated_at) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(workflow_id,id) DO UPDATE SET ordinal=excluded.ordinal,title=excluded.title,
			state=excluded.state,evidence=excluded.evidence,retired=excluded.retired,updated_at=excluded.updated_at`,
			id, point.ID, ordinal, point.Title, point.State, jsonList(point.Evidence), retired, stamp(point.Updated)); err != nil {
			return PlanProgress{}, false, "", unavailable(err, "workflow: syncing %s point %s", id, point.ID)
		}
	}
	// The revision is the durable publication boundary for the checklist. A
	// reader that reconnects must be able to tell an older snapshot from the
	// one whose evidence was just saved. Keep the increment in the same
	// transaction as the point rows, and protect it with the revision observed
	// above so a concurrent writer can never publish a stale derivation over a
	// newer one.
	result, err := tx.ExecContext(ctx,
		`UPDATE workflow SET plan_revision=plan_revision+1 WHERE id=? AND plan_revision=?`,
		id, run.PlanRevision)
	if err != nil {
		return PlanProgress{}, false, "", unavailable(err, "workflow: advancing %s plan revision", id)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return PlanProgress{}, false, "", unavailable(err, "workflow: reading %s plan revision", id)
	} else if rows != 1 {
		return PlanProgress{}, false, "", contract.Fail(contract.FailureUnavailable,
			"workflow %s plan changed while its checklist was being saved", id)
	}
	if err := tx.Commit(); err != nil {
		return PlanProgress{}, false, "", unavailable(err, "workflow: syncing %s plan", id)
	}
	progress.Revision++
	event := strings.Join(events, ", ")
	if event == "" {
		event = "Plan actualizado"
	}
	return progress, true, event, nil
}
