package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// slowFor answers every step, taking longer for one named repository than for
// the other. Two repositories in one wave with very different durations is the
// shape that tells a truthful clock from a convenient one.
func slowFor(repo string, slow, quick time.Duration) *fakeRunner {
	return &fakeRunner{answer: func(req contract.RunRequest) (contract.Outcome, error) {
		if req.Repository.ID == repo {
			time.Sleep(slow)
		} else {
			time.Sleep(quick)
		}
		return hits("cmd/main.go"), nil
	}}
}

// readRun returns the one receipt the store wrote.
func readRun(t *testing.T, dir string) checkpoint.Run {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("receipts in %s: %v (%v)", dir, files, err)
	}
	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	var run checkpoint.Run
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	return run
}

// A step's close time belongs to the step. The recorder runs after the whole
// wave has returned, so a clock read there hands every step in the wave the
// same instant -- and since closed_at sits beside duration_ms, subtracting one
// from the other is how anybody reads back when a step ran. Under a shared
// stamp the quick step of a wave appears to have started when the slow one was
// nearly done, which is exactly backwards: they started together.
func TestAStepClosesWhenItFinishedNotWhenTheWaveDid(t *testing.T) {
	dir := t.TempDir()
	agent, _ := build(t, slowFor("api", 300*time.Millisecond, 20*time.Millisecond), 0, dir)
	if _, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	run := readRun(t, dir)
	starts := map[string]time.Time{}
	for _, step := range run.Steps {
		if step.ClosedAt.IsZero() {
			t.Fatalf("step %s closed at the zero time", step.ID)
		}
		starts[step.ID] = step.ClosedAt.Add(-time.Duration(step.DurationMS) * time.Millisecond)
	}
	slow, quick := starts["explore-api"], starts["explore-web"]
	if slow.IsZero() || quick.IsZero() {
		t.Fatalf("the wave's two steps are not both on the receipt: %v", starts)
	}
	// They were dispatched together, so their reconstructed starts have to be
	// close. The tolerance is generous next to the 280ms of difference a
	// wave-end stamp produces.
	if gap := quick.Sub(slow); gap > 100*time.Millisecond || gap < -100*time.Millisecond {
		t.Errorf("the two steps of one wave start %s apart on the receipt; they were dispatched together",
			gap.Round(time.Millisecond))
	}
}

// The run reports the time it took, not only the time it cost. On a wave two
// steps wide those are different numbers, and the difference is the entire
// point of the wave.
func TestARunReportsTheTimeItActuallyTook(t *testing.T) {
	agent, _ := build(t, &fakeRunner{delay: 150 * time.Millisecond}, 0, "")
	result, err := agent.Run(t.Context(), orchestrator.Task{Text: "login"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Elapsed <= 0 {
		t.Fatal("the run does not report how long it took")
	}
	if result.Elapsed >= result.Spent.Duration {
		t.Errorf("elapsed %s is not below the %s it cost: two repositories ran in each wave, so the wall has to be shorter than the sum",
			result.Elapsed.Round(time.Millisecond), result.Spent.Duration.Round(time.Millisecond))
	}
	for _, phase := range result.Phases {
		if phase.Elapsed <= 0 {
			t.Errorf("phase %s does not report how long it took", phase.Name)
			continue
		}
		if phase.Elapsed >= phase.Spent.Duration {
			t.Errorf("phase %s: elapsed %s is not below the %s it cost", phase.Name,
				phase.Elapsed.Round(time.Millisecond), phase.Spent.Duration.Round(time.Millisecond))
		}
	}
}

// Every way into the orchestrator closes a result, and each one has to stamp
// the wall. A single question is the cheap case and a resumed commission is the
// one that catches a stamp written in only one of the three: resume dispatches
// real work in a new process and reported 0s elapsed beside a step that took
// 714ms, which reads as a run that took no time at all.
func TestEveryWayIntoTheOrchestratorReportsTheWall(t *testing.T) {
	dir := t.TempDir()
	agent, _ := build(t, &fakeRunner{delay: 40 * time.Millisecond}, 0, dir)

	asked, err := agent.Ask(t.Context(), orchestrator.Question{
		Capability: "code.search", Repository: "api", Payload: map[string]any{"query": "login"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if asked.Elapsed <= 0 {
		t.Error("a question does not report how long it took")
	}

	// A run interrupted mid-flight, then resumed: the resumed attempt does the
	// remaining work, so it has a wall of its own to report.
	ctx, cancel := context.WithCancel(t.Context())
	slow, _ := build(t, &fakeRunner{delay: time.Minute}, 0, dir)
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	stopped, _ := slow.Run(ctx, orchestrator.Task{Text: "login"})
	if stopped == nil || stopped.Verdict != contract.VerdictCanceled {
		t.Skipf("nothing was interrupted, so there is nothing to resume: %v", stopped)
	}

	back, err := agent.Resume(t.Context(), stopped.RunID, orchestrator.ResumeOptions{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(back.Steps) == 0 {
		t.Skip("the resumed attempt had nothing left to dispatch")
	}
	if back.Elapsed <= 0 {
		t.Errorf("a resumed run redispatched %d step(s) and reports no time at all", len(back.Steps))
	}
}
