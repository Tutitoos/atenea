package workflow

import (
	"context"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// reconcileLegacyApproval handles records written before the application marker.
// Existing matching graph rows prove application. A latest, entirely absent
// addition can be recovered. Ambiguous historical replacements require review
// rather than risking replay of work that may already have run.
func (e *Engine) reconcileLegacyApproval(ctx context.Context, run Run, gate Gate) (bool, error) {
	current := make(map[string]Step, len(run.Steps))
	for _, row := range run.Steps {
		current[row.Step.ID] = row.Step
	}
	proposed := make(map[string]bool, len(gate.Proposal.Steps))
	var matching []Step
	present := 0
	for _, step := range gate.Proposal.Steps {
		proposed[step.ID] = true
		if old, ok := current[step.ID]; ok {
			present++
			matching = append(matching, old)
		}
	}
	removed := true
	for _, id := range gate.Proposal.Replaces {
		if _, exists := current[id]; exists && !proposed[id] {
			removed = false
		}
	}
	applied := removed && present == len(gate.Proposal.Steps) &&
		(Proposal{Steps: matching}).Digest() == (Proposal{Steps: gate.Proposal.Steps}).Digest()
	if !applied {
		var later int
		if err := e.store.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_gate WHERE workflow_id=? AND ordinal>? AND decision='approved'`, run.ID, gate.Ordinal).Scan(&later); err != nil {
			return false, err
		}
		if present != 0 || len(gate.Proposal.Steps) == 0 || len(gate.Proposal.Replaces) != 0 || later != 0 {
			return false, contract.Fail(contract.FailureUnavailable, "workflow %s gate %d: legacy application state is ambiguous; review the recorded graph before resuming", run.ID, gate.Ordinal)
		}
	}
	_, err := e.store.db.ExecContext(ctx, `UPDATE workflow_gate SET applied=? WHERE workflow_id=? AND ordinal=? AND digest=? AND applied IS NULL`, applied, run.ID, gate.Ordinal, gate.Digest)
	return applied, err
}
