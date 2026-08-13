package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// MaxAttempts is how many times one piece of work may be run.
//
// Two, and the second only when a review asked for it. The first relaunch is
// the one that pays: the agent gets told what was wrong and usually fixes it.
// A third would be the same answer a third time in most cases and an
// unbounded bill in the rest, and "keep going until it passes" is how a loop
// that cannot pass burns a budget silently. When the second attempt is
// refused too, that IS the answer: the work is bad, said so twice, by a
// reviewer that saw both tries.
const MaxAttempts = 2

// Attempt is one run of the work together with the review of it. Review is
// zero when the run died -- there was no report to judge.
type Attempt struct {
	Work         contract.Assignment
	Report       contract.Report
	Review       contract.Assignment
	ReviewReport contract.Report
	// Reviewed is false when the run died before there was anything to audit.
	Reviewed bool
}

// Accepted reports whether the review passed this attempt.
func (a Attempt) Accepted() bool {
	return a.Reviewed && a.ReviewReport.Verdict == contract.VerdictOK
}

// ReviewedRun is the whole audited dispatch: every attempt, in order, and
// which one the reviewer accepted.
//
// Attempts is never empty on a successful call, and a caller reading only the
// last one still gets the truth -- but the earlier ones are kept because
// "passed on the second try" and "passed" are different facts about an agent,
// and the first is the one worth acting on.
type ReviewedRun struct {
	Attempts []Attempt
}

// Final is the report the caller should consume: the accepted answer, or the
// last one if none was accepted.
func (r ReviewedRun) Final() contract.Report {
	if len(r.Attempts) == 0 {
		return contract.Report{}
	}
	for _, a := range r.Attempts {
		if a.Accepted() {
			return a.Report
		}
	}
	return r.Attempts[len(r.Attempts)-1].Report
}

// Accepted reports whether any attempt passed its review.
func (r ReviewedRun) Accepted() bool {
	for _, a := range r.Attempts {
		if a.Accepted() {
			return true
		}
	}
	return false
}

// RunReviewed dispatches work, has a reviewer audit the answer, and relaunches
// the work once if the reviewer refuses it.
//
// Three rules, and the second is the one that matters:
//
//   - A death is not reviewed. There is no report to judge, and re-running a
//     process that crashed is a retry policy, not an audit. This loop exists
//     to catch answers that are wrong, not processes that are broken.
//   - A refusal relaunches ONCE, and the relaunch is handed the rejected
//     attempt together with the sentence that rejected it. An agent told only
//     to try again reruns the same mistake.
//   - A second refusal ends it. No third attempt: the reviewer has now seen
//     two tries and refused both, which is a finding, not a reason to keep
//     spending.
//
// The returned error is non-nil when nothing was accepted, and it carries the
// reviewer's own reason kind: the caller's exit code then says what a reader
// of the trace would say.
func (r *Runner) RunReviewed(ctx context.Context, typeName, reviewerName string,
	task contract.Task) (ReviewedRun, error) {
	if strings.TrimSpace(reviewerName) == "" {
		return ReviewedRun{}, contract.Fail(contract.FailureInvalidInput,
			"a reviewed run needs a reviewer type")
	}
	if _, err := r.resolve(reviewerName); err != nil {
		return ReviewedRun{}, err
	}

	var out ReviewedRun
	var previous *Attempt
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		current, err := r.attempt(ctx, typeName, reviewerName, task, attempt, previous)
		out.Attempts = append(out.Attempts, current)
		if err != nil {
			return out, err
		}
		if current.Accepted() {
			return out, nil
		}
		previous = &out.Attempts[len(out.Attempts)-1]
	}

	last := out.Attempts[len(out.Attempts)-1]
	return out, contract.Fail(last.ReviewReport.Reason.Kind,
		"agent %s: refused by %s on both attempts: %s",
		typeName, reviewerName, last.ReviewReport.Reason.Text)
}

// attempt runs the work once and reviews it.
func (r *Runner) attempt(ctx context.Context, typeName, reviewerName string,
	task contract.Task, number int, previous *Attempt) (Attempt, error) {
	work := Dispatch{TypeName: typeName, Task: task, Attempt: number}
	if previous != nil {
		// The relaunch is a second row linked to the first, never a fresh
		// run of the same type: two attempts at one piece of work and two
		// separate asks an hour apart must not read the same in the trace.
		work.RetryOf = previous.Work.ID
		subject := subjectOf(*previous, number-1)
		subject.ReviewID = previous.Review.ID
		subject.Rejection = previous.ReviewReport.Reason
		work.Subject = &subject
	}

	report, assignment, err := r.Dispatch(ctx, work)
	current := Attempt{Work: assignment, Report: report}
	if err != nil {
		// Died. Nothing to audit, and nothing invented to fill the gap.
		return current, err
	}

	subject := subjectOf(current, number)
	review, reviewCard, err := r.Dispatch(ctx, Dispatch{
		TypeName: reviewerName,
		Task:     reviewTask(typeName, task, number),
		Subject:  &subject,
		Attempt:  1,
		Reviews:  assignment.ID,
	})
	current.Review, current.ReviewReport = reviewCard, review
	if err != nil {
		// The reviewer died. The work's own answer stands unjudged, and
		// saying so is the honest outcome -- passing it off as accepted
		// would make an unreviewed answer indistinguishable from a reviewed
		// one, which is the failure this design exists to prevent.
		return current, err
	}
	current.Reviewed = true
	return current, nil
}

// subjectOf packs a finished attempt into the card a reviewer reads. The
// attempt number is passed in rather than read off the card: the loop is what
// knows which try this is, and a card that counted its own attempts would be
// a second place that fact lives.
//
// The packing itself belongs to the report -- a workflow's subject edge hands
// over the same card, and one answer must not get two different reviews
// depending on which caller assembled it.
func subjectOf(a Attempt, attempt int) contract.Subject {
	return a.Report.Subject(a.Work.ID, a.Work.TypeName, attempt, a.Work.Task)
}

// reviewTask states what the reviewer is being asked, in the same shape as
// any other task. The criterion is the work's own criterion, quoted: a review
// judges the answer against what was actually asked, not against the
// reviewer's idea of what should have been asked.
func reviewTask(typeName string, work contract.Task, attempt int) contract.Task {
	return contract.Task{
		Objective: fmt.Sprintf("audit attempt %d of %s against what it was asked",
			attempt, typeName),
		Files:     work.Files,
		Criterion: work.Criterion,
	}
}
