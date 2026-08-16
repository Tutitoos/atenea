package workflow

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Blocked reports whether this step never ran because something it needs did
// not succeed.
//
// Derived, never stored. A blocked step is a pending step with a dependency
// that ended badly, and both halves of that sentence are already on the
// record: writing it down as a third status would be a second copy of a fact
// that can go stale the moment a resume redoes the step that blocked it.
func (r Run) Blocked(id string) bool {
	_, _, blocked := r.blockedBy(id)
	return blocked
}

// BlockReason says why a step never ran, and what would clear it.
//
// A step held up by one nobody judged gets the cure in the sentence. "waits
// on read-a" is true there and useless: it leaves the reader to already know
// that an unjudged run cannot be handed on, and that `--redo` is what judges
// one. Both facts are in the line instead, for ordering edges as much as for
// subject edges -- `--redo` clears either.
//
// A step held up by a failure is a different sentence, because a failure was
// judged and nothing here will change it.
func (r Run) BlockReason(id string) string {
	edge, upstream, blocked := r.blockedBy(id)
	if !blocked {
		return ""
	}
	if upstream != StatusInterrupted {
		return "waits on " + edge.ID
	}
	what := "nothing to hand on"
	if edge.Subject {
		what = "no answer to review"
	}
	// The minimal cure, not the heaviest one. A resume redoes an interrupted
	// step that only read, by itself; `--redo` is for the ones it deliberately
	// leaves alone. Printing --redo for both would teach the bigger hammer
	// and quietly suggest that resume on its own does not work here.
	cure := "atenea workflow resume " + r.ID
	if row, ok := stepRow(r, edge.ID); ok && touchesTheWorld(row.Step.Permission.Effects) {
		cure += " --redo " + edge.ID
	}
	return fmt.Sprintf("%s was never judged, so there is %s: %s", edge.ID, what, cure)
}

// blockedBy finds the edge that shut this step out, and how the step on the
// far side of it ended.
func (r Run) blockedBy(id string) (Edge, Status, bool) {
	status := make(map[string]Status, len(r.Steps))
	for _, step := range r.Steps {
		status[step.Step.ID] = step.Status
	}
	seen := make(map[string]bool, len(r.Steps))
	var walk func(string) (Edge, Status, bool)
	walk = func(id string) (Edge, Status, bool) {
		if seen[id] {
			return Edge{}, StatusPending, false
		}
		seen[id] = true
		step, ok := stepRow(r, id)
		if !ok || step.Status != StatusPending {
			return Edge{}, StatusPending, false
		}
		for _, edge := range step.Step.Edges() {
			upstream := status[edge.ID]
			if upstream == StatusPending {
				if found, was, blocked := walk(edge.ID); blocked {
					return found, was, true
				}
				continue
			}
			// Still running, or finished in a way this edge accepts.
			if upstream == StatusRunning || edge.On.satisfiedBy(upstream) {
				continue
			}
			return edge, upstream, true
		}
		return Edge{}, StatusPending, false
	}
	return walk(id)
}

// Needs is what this step waits on, answer-carrying edge included.
func (s StepRow) Needs() []string {
	out := make([]string, 0, len(s.Step.Needs)+1)
	for _, edge := range s.Step.Edges() {
		out = append(out, edge.ID)
	}
	return out
}

// Report reassembles what the agent handed back. The store keeps the fields
// apart because they are queried apart; a reader wanting the answer wants it
// whole.
func (s StepRow) Report() contract.Report {
	return contract.Report{
		Result:       s.Result,
		Verdict:      s.Verdict,
		Reason:       s.Reason,
		Discovered:   s.Discovered,
		Spent:        s.Spent,
		Completeness: s.Completeness,
		StoppedAt:    s.StoppedAt,
	}
}

// subjectFrom packs a finished step into the card its reader is handed.
//
// It goes through [contract.Report.Subject], the same constructor
// `atenea agent --review` uses, so one answer cannot get two different
// reviews depending on which door the caller came through.
//
// The validation is the load-bearing part. A step nobody judged has no
// verdict, and the contract refuses to build a card from it -- so a subject
// fabricated from a run nobody watched cannot reach an agent even if a gate
// upstream of here were wrong. Reaching this error is a bug in the readiness
// rule, and it says so.
func subjectFrom(up StepRow) (contract.Subject, error) {
	subject := up.Report().Subject(up.TraceID, up.Step.TypeName, up.Attempt, up.Step.Task)
	if err := subject.Validate(); err != nil {
		return contract.Subject{}, contract.Fail(contract.FailureInvalidInput,
			"workflow: nothing reviewable from step %s: %v", up.Step.ID, err)
	}
	return subject, nil
}

// Label is what to call this step in a listing: its status, or `blocked` for a
// pending step whose way is shut.
func (r Run) Label(step StepRow) string {
	if step.Status == StatusPending && r.Blocked(step.Step.ID) {
		return "blocked"
	}
	return step.Status.String()
}

// State is what to print in a listing's STATE column: the label, with the
// coverage appended when the step's own report claims less than the whole
// objective. Separate from Label -- which Counts and Summary group on --
// because folding "ok (0.55)" and "ok (0.90)" into distinct groups would
// make a run that mostly succeeded look like it barely finished.
func (r Run) State(step StepRow) string {
	label := r.Label(step)
	report := step.Report()
	if !report.Partial() {
		return label
	}
	return fmt.Sprintf("%s (%.2f)", label, *report.Completeness)
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
// It never blends a run's steps into one figure unless that figure is
// honest about every one of them. Nothing measured reads `unmeasured` -- a
// receipt printing a measured-looking zero for a run that really did cost
// something upstream is the same lie as list-price cost on subscription
// traffic, worse, because it looks audited. Every step measured reports the
// tokens, and a dollar figure only once every one of them named a price,
// with that price's source attached: an unattributed dollar amount is the
// same unsupported claim contract.Charge refuses to carry. Some steps
// measured and some not is the shape a single total would misrepresent as a
// complete accounting, so [Run.Spend] is asked for the split and the line
// says which steps counted instead of folding the silent half in.
func (r Run) Budget() string {
	granted := fmt.Sprintf("$%.2f granted", r.GrantUSD)
	if r.GrantUSD == 0 {
		granted = "no grant"
	}
	spend := r.Spend()
	if spend.MeasuredSteps == 0 {
		// Not "no agent reports a charge yet": they can now, and a run whose
		// steps all refused before spending is a different fact from a
		// machine that cannot meter. Both read as unmeasured, and neither
		// reads as zero.
		return granted + "; spent unmeasured (nothing on this run reported a charge)"
	}

	tokens := plural(spend.Tokens, "token", "tokens")
	if spend.TruncatedSteps > 0 {
		// A floor, and it has to say so. These rows were charged real money
		// and kept no token count, so they contribute zero to a sum whose
		// dollars they do contribute to -- see [StepRow.Truncated]. Printed
		// bare, the total tells a reader those steps did almost nothing, when
		// what happened is that they spent their whole share and the receipt
		// lost the evidence.
		tokens = "at least " + tokens
	}
	detail := tokens + " spent"
	if spend.USD != nil {
		detail = fmt.Sprintf("%s and $%.2f spent (priced by %s)",
			tokens, *spend.USD, strings.Join(spend.PricedBy, ", "))
		if spend.UnmeasuredSteps == 0 {
			detail += fmt.Sprintf(", $%.2f left", r.GrantUSD-*spend.USD)
		}
	}
	if spend.TruncatedSteps > 0 {
		detail += fmt.Sprintf("; %s charged with no token record",
			plural(spend.TruncatedSteps, "step", "steps"))
	}
	if spend.UnmeasuredSteps == 0 {
		return fmt.Sprintf("%s; %s", granted, detail)
	}
	return fmt.Sprintf("%s; %d of %d steps measured (%s)", granted,
		spend.MeasuredSteps, spend.MeasuredSteps+spend.UnmeasuredSteps, detail)
}

// plural picks the word that agrees with n.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
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
