package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// A stopped run is not a failed one, and the word on the screen is the whole
// point: "failed" sends a reader looking for a fault that is not there, and it
// blames the work for a decision the reader made themselves.
func TestAStoppedRunIsNotAFailedOne(t *testing.T) {
	agent, _ := metered(t, &fakeRunner{delay: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	result, _ := agent.Run(ctx, orchestrator.Task{Text: "find TODO"})

	if result == nil {
		t.Fatal("a stopped run came back with no report at all")
	}
	if result.Verdict != contract.VerdictCanceled {
		t.Errorf("verdict = %v, want canceled", result.Verdict)
	}
}

// The step that was interrupted is not reviewed either. Nothing came back, so
// neither the child nor the parent has seen anything to have an opinion about,
// and a review printed here would be two opinions nobody holds.
func TestAnInterruptedStepIsNotReviewed(t *testing.T) {
	agent, _ := metered(t, &fakeRunner{delay: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	result, _ := agent.Run(ctx, orchestrator.Task{Text: "find TODO"})

	if result == nil || len(result.Steps) == 0 {
		t.Skip("nothing was dispatched before the stop, so there is no step to check")
	}
	for _, step := range result.Steps {
		if step.FailureKind != contract.FailureCanceled {
			continue
		}
		if step.Review.Parent == contract.VerdictFailed || step.Review.Child == contract.VerdictFailed {
			t.Errorf("step %s: review = child=%v parent=%v, want neither to be failed",
				step.Step.ID, step.Review.Child, step.Review.Parent)
		}
		if step.Review.Disagreed {
			t.Errorf("step %s: a step nobody reviewed cannot have a disagreement", step.Step.ID)
		}
		if strings.Contains(step.Review.Reply, "no reply") {
			t.Errorf("step %s: reply = %q, invented for a review that never happened",
				step.Step.ID, step.Review.Reply)
		}
	}
}

// A real fault outranks an interruption. If one step failed on its own and a
// later one was stopped, the run failed: reporting "canceled" would bury the
// fault behind the interruption, and the fault is the half worth reading.
func TestAFaultOutranksAnInterruption(t *testing.T) {
	agent, _ := metered(t, &fakeRunner{answer: func(contract.RunRequest) (contract.Outcome, error) {
		return contract.Outcome{}, contract.Fail(contract.FailureUnavailable, "the provider is down")
	}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result, _ := agent.Run(ctx, orchestrator.Task{Text: "find TODO"})

	if result == nil {
		t.Fatal("no report at all")
	}
	if result.Verdict != contract.VerdictFailed {
		t.Errorf("verdict = %v, want failed: a fault happened and it must not be hidden behind the stop",
			result.Verdict)
	}
}
