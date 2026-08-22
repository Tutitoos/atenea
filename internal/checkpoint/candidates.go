package checkpoint

import (
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// OK lists the step ids that have at least one closed attempt whose parent
// review was ok, in the order they were first earned.
//
// This -- and only this -- is what a resumed run treats as already done. A
// step that failed, was canceled, or was blocked by one of those is retried,
// because a bin the parent did not approve is not evidence the work cannot be
// done, only that this attempt did not do it.
func (r Run) OK() []string {
	seen := make(map[string]struct{}, len(r.Steps))
	ok := make([]string, 0, len(r.Steps))
	for _, step := range r.Steps {
		if step.Review != contract.VerdictOK.String() {
			continue
		}
		if _, dup := seen[step.ID]; dup {
			continue
		}
		seen[step.ID] = struct{}{}
		ok = append(ok, step.ID)
	}
	return ok
}

// Remaining reports how many steps the plan on file still owes once every
// step OK already covers is treated as done. Zero is what "nothing left to
// continue" looks like, whether that is because every step passed review or
// because the run was never handed a plan to begin with.
//
// A plan that never grew past the look is the one case LayersAfter cannot
// answer this from: splitting itself is the work still owed, and nothing on
// file counts it, so an already-ok look would otherwise read as nothing
// left. Resuming such a run always redoes the look whole rather than trust
// it (see orchestrator.Resume), so it is never done merely because the look
// already closed once. The look's own step count stands in for the unknown
// total: honest about there being more, silent about how much.
func (r Run) Remaining() (int, error) {
	if len(r.Plan.Steps) == 0 {
		return 0, nil
	}
	// A single ask has no split to redo, so the question below -- whether
	// the look ever finished -- does not apply to it. Resume's own KindAsk
	// branch treats the one step as done once it is OK; this has to agree,
	// or a listing and the command it is advertising a candidate for would
	// disagree about the same receipt.
	if r.Kind == KindAsk {
		if len(r.OK()) > 0 {
			return 0, nil
		}
		return len(r.Plan.Steps), nil
	}
	if r.Kind == KindPlan {
		waves, err := r.Plan.LayersAfter(r.OK())
		if err != nil {
			return 0, err
		}
		n := 0
		for _, wave := range waves {
			n += len(wave)
		}
		return n, nil
	}
	hasWork := false
	for _, step := range r.Plan.Steps {
		if len(step.Needs) > 0 {
			hasWork = true
			break
		}
	}
	if !hasWork {
		return len(r.Plan.Steps), nil
	}
	waves, err := r.Plan.LayersAfter(r.OK())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, wave := range waves {
		n += len(wave)
	}
	return n, nil
}

// Candidate is one run a listing can show without dispatching anything.
type Candidate struct {
	ID        string
	Task      string
	Started   time.Time
	Updated   time.Time
	Verdict   string
	Remaining int
}

// Candidates lists the runs that still have work to continue, oldest first
// because List already orders them that way. A run with nothing left does
// not belong on a list of things worth resuming; a receipt that no longer
// parses is skipped rather than failing the whole listing over one entry.
func (s *Store) Candidates() ([]Candidate, error) {
	ids, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		run, loadErr := s.Load(id)
		if loadErr != nil {
			continue
		}
		remaining, remErr := run.Remaining()
		if remErr != nil || remaining == 0 {
			continue
		}
		out = append(out, Candidate{
			ID:        run.ID,
			Task:      run.Task,
			Started:   run.Started,
			Updated:   run.Updated,
			Verdict:   run.Verdict,
			Remaining: remaining,
		})
	}
	return out, nil
}
