package orchestrator_test

import (
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
