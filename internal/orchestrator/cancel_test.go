package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// stoppedRunner answers the way a real adapter now does when the caller goes
// away: the canceled bin, and numbers that describe the wait rather than the
// tool.
func stoppedRunner(waited time.Duration) *fakeRunner {
	return &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{Spent: contract.Sample{Duration: waited}},
				contract.Fail(contract.FailureCanceled, "claude code was stopped before it answered")
		},
	}
}

// The base prices tools. A canceled call has numbers attached and they are
// not about the tool: the clock was timing how long somebody waited before
// changing their mind, and the failure is one nobody's provider committed.
// Filed, they would price a provider by the patience of whoever ran it.
func TestACanceledStepIsNotMeasured(t *testing.T) {
	agent, counter := metered(t, stoppedRunner(27*time.Second))

	if _, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	rows, _ := counter.taken()
	if len(rows) != 0 {
		t.Errorf("%d row(s) filed for a canceled call: %+v", len(rows), rows[0])
	}
}

// The comparison that makes the rule a rule rather than a special case: the
// same shape of failure, in a bin that IS about the provider, is still filed.
// A timeout is evidence and has to survive.
func TestATimedOutStepIsStillMeasured(t *testing.T) {
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{Spent: contract.Sample{Duration: time.Second}},
				contract.Fail(contract.FailureTimeout, "claude code took longer than 5m0s")
		},
	}
	agent, counter := metered(t, runner)

	if _, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	rows, _ := counter.taken()
	if len(rows) == 0 {
		t.Fatal("a timeout was not measured: the funnel needs to know a provider hangs")
	}
	for _, row := range rows {
		if row.OK {
			t.Errorf("a timeout was filed as a success: %+v", row)
		}
		if row.FailureKind != contract.FailureTimeout.String() {
			t.Errorf("bin = %q, want timeout", row.FailureKind)
		}
	}
}

// The safety net under batching, and ctrl-c is exactly when a net is wanted.
// Inheriting the caller's cancellation meant this flush failed with "context
// canceled", filed an incident saying so, and dropped every measurement the
// run had earned before the interruption -- work that was already paid for.
func TestTheBatchIsFlushedEvenWhenTheCallerIsGone(t *testing.T) {
	agent, counter := metered(t, &fakeRunner{})
	ctx, cancel := context.WithCancel(context.Background())

	// Canceled before a single step runs, which is the harshest version of
	// the same story: whatever the run manages to earn still has to land.
	cancel()
	_, _ = agent.Run(ctx, orchestrator.Task{Text: "find TODO"})

	if _, settled := counter.taken(); settled == 0 {
		t.Fatal("the batch was never pushed: a canceled run takes its measurements with it")
	}
	// Being called is not enough. A flush handed the caller's dead context
	// cannot open the database, which is exactly how this failed before: the
	// call happened, the write did not, and an incident was filed about it.
	if doomed := counter.doomed(); doomed != 0 {
		t.Errorf("%d flush(es) got a context that was already dead", doomed)
	}
}

// A run the caller stopped reports the bin for stopping. This is the core's
// own sentence rather than an adapter's, and it went wrong the same way: the
// early exit called every unfinished run a timeout, so a script could not tell
// ctrl-c from a provider that hung.
func TestStoppingARunIsNotATimeout(t *testing.T) {
	agent, _ := metered(t, &fakeRunner{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := agent.Run(ctx, orchestrator.Task{Text: "find TODO"})

	if err == nil {
		t.Fatal("a stopped run came back clean")
	}
	if got := contract.KindOf(err); got != contract.FailureCanceled {
		t.Errorf("kind = %v, want canceled", got)
	}
}

// And a run that really did run out of time keeps the bin that says so: that
// one is a fact about how long the work took, and a script is right to treat
// it differently.
func TestARunThatRanOutOfTimeIsStillATimeout(t *testing.T) {
	agent, _ := metered(t, &fakeRunner{delay: time.Minute})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	_, err := agent.Run(ctx, orchestrator.Task{Text: "find TODO"})

	if got := contract.KindOf(err); got != contract.FailureTimeout {
		t.Errorf("kind = %v, want timeout", got)
	}
}
