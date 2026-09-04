package workflow

import (
	"context"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// reconcileAccountingFailure settles returned costs and releases ownership in one transaction.
func (s *Store) reconcileAccountingFailure(ctx context.Context, id string, completed []done, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err, "workflow: starting error reconciliation")
	}
	defer func() { _ = tx.Rollback() }()
	for _, item := range completed {
		report, status := item.report, item.status
		invalid := report.Spent.Validate() != nil
		if invalid {
			report.Spent = contract.Charge{}
		}
		if _, err := jsonMap(report.Result); invalid || err != nil {
			report = contract.Report{Spent: report.Spent, Reason: contract.Reason{Kind: contract.FailureUnavailable, Text: "interrupted by accounting failure"}}
			status = StatusInterrupted
		}
		if err := finishStep(ctx, tx, id, item.stepID, status, report, at); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_step SET status=?, writer_pid=0, ended_at=?, reason_kind=?, reason_text=? WHERE workflow_id=? AND status=?`, StatusInterrupted.String(), stamp(at), contract.FailureUnavailable.String(), "interrupted by accounting failure", id, StatusRunning.String()); err != nil {
		return unavailable(err, "workflow: releasing interrupted steps")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow SET closed=0, ended_at=?, stop=?, writer_pid=0 WHERE id=?`, stamp(at), string(StopUnjudged), id); err != nil {
		return unavailable(err, "workflow: ending after accounting failure")
	}
	if err = tx.Commit(); err != nil {
		return unavailable(err, "workflow: committing error reconciliation")
	}
	return nil
}
