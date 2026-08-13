package workflow

import (
	"fmt"
	"sort"
	"strings"
)

// Blocked reports whether this step never ran because something it needs did
// not succeed.
//
// Derived, never stored. A blocked step is a pending step with a dependency
// that ended badly, and both halves of that sentence are already on the
// record: writing it down as a third status would be a second copy of a fact
// that can go stale the moment a resume redoes the step that blocked it.
func (r Run) Blocked(id string) bool {
	status := make(map[string]Status, len(r.Steps))
	for _, step := range r.Steps {
		status[step.Step.ID] = step.Status
	}
	seen := make(map[string]bool, len(r.Steps))
	var blocked func(string) bool
	blocked = func(id string) bool {
		if seen[id] {
			return false
		}
		seen[id] = true
		step, ok := stepRow(r, id)
		if !ok || step.Status != StatusPending {
			return false
		}
		for _, need := range step.Needs() {
			switch status[need] {
			case StatusFailed, StatusIncomplete, StatusInterrupted:
				return true
			case StatusPending:
				if blocked(need) {
					return true
				}
			}
		}
		return false
	}
	return blocked(id)
}

// Needs is what this step waits on.
func (s StepRow) Needs() []string { return s.Step.Needs }

// Label is what to call this step in a listing: its status, or `blocked` for a
// pending step whose way is shut.
func (r Run) Label(step StepRow) string {
	if step.Status == StatusPending && r.Blocked(step.Step.ID) {
		return "blocked"
	}
	return step.Status.String()
}

// Counts totals the steps by label.
func (r Run) Counts() map[string]int {
	out := make(map[string]int, len(r.Steps))
	for _, step := range r.Steps {
		out[r.Label(step)]++
	}
	return out
}

// Summary is one line: what happened to the steps.
func (r Run) Summary() string {
	counts := r.Counts()
	labels := make([]string, 0, len(counts))
	for label := range counts {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, fmt.Sprintf("%d %s", counts[label], label))
	}
	if len(parts) == 0 {
		return "no steps"
	}
	return strings.Join(parts, ", ")
}

// Budget is the money line.
//
// It says `unmeasured` rather than $0.00 whenever nothing could report a
// charge, which today is always: the agent report wire carries no money and
// no token count, so there is no number to add up. A receipt printing a
// measured-looking zero for a run that really did cost something upstream is
// the same lie as list price on subscription traffic -- worse, because it
// looks audited.
func (r Run) Budget() string {
	spent, measured := r.Spent()
	granted := fmt.Sprintf("$%.2f granted", r.GrantUSD)
	if r.GrantUSD == 0 {
		granted = "no grant"
	}
	if !measured {
		return granted + "; spent unmeasured (no agent reports a charge yet)"
	}
	return fmt.Sprintf("%s; $%.2f spent, $%.2f left", granted, spent, r.GrantUSD-spent)
}

// Done reports whether nothing is left that this run could do: every step has
// either finished, been left unjudged, or been shut out by one that did.
func (r Run) Done() bool {
	for _, step := range r.Steps {
		if step.Status == StatusRunning {
			return false
		}
		if step.Status == StatusPending && !r.Blocked(step.Step.ID) {
			return false
		}
	}
	return true
}

// Interrupted lists the steps nobody judged, in graph order. These are what a
// resume needs a decision about.
func (r Run) Interrupted() []StepRow {
	var out []StepRow
	for _, step := range r.Steps {
		if step.Status == StatusInterrupted {
			out = append(out, step)
		}
	}
	return out
}
