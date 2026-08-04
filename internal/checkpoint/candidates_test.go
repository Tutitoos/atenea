package checkpoint_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// twoStepPlan is a plan with one repository's search waiting on its own look,
// the shape split() actually produces.
func twoStepPlan(task string) contract.Plan {
	return contract.Plan{Task: task, Steps: []contract.Step{
		{ID: "explore-api", Capability: "code.search", Repository: "api",
			Permission: contract.Permission{Task: task}},
		{ID: "search-api", Capability: "code.search", Repository: "api",
			Needs: []string{"explore-api"}, Permission: contract.Permission{Task: task}},
	}}
}

// exploreOnlyPlan is a plan that never grew past the look: split never ran,
// so no step needs another.
func exploreOnlyPlan(task string) contract.Plan {
	return contract.Plan{Task: task, Steps: []contract.Step{
		{ID: "explore-api", Capability: "code.search", Repository: "api",
			Permission: contract.Permission{Task: task}},
	}}
}

func TestOKCollectsOnlyStepsThatPassedReview(t *testing.T) {
	r := checkpoint.Run{Steps: []checkpoint.StepState{
		{ID: "explore-api", Review: "ok"},
		{ID: "search-api", Review: "failed"},
		{ID: "search-web", Review: "canceled"},
		// A step retried after resume: the same id closes twice, and the
		// second attempt is the one that earned it.
		{ID: "search-api", Review: "failed"},
		{ID: "search-api", Review: "ok"},
	}}
	got := r.OK()
	want := []string{"explore-api", "search-api"}
	if len(got) != len(want) {
		t.Fatalf("OK() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("OK() = %v, want %v", got, want)
		}
	}
}

func TestRemainingIsZeroWithNoPlan(t *testing.T) {
	remaining, err := (checkpoint.Run{Task: "find every TODO"}).Remaining()
	if err != nil {
		t.Fatalf("Remaining: %v", err)
	}
	if remaining != 0 {
		t.Errorf("remaining = %d, want 0: a run never handed a plan has nothing to continue", remaining)
	}
}

func TestRemainingCountsWhatLayersAfterStillOwes(t *testing.T) {
	r := checkpoint.Run{
		Task: "find every TODO",
		Plan: twoStepPlan("find every TODO"),
		Steps: []checkpoint.StepState{
			{ID: "explore-api", Review: "ok"},
		},
	}
	remaining, err := r.Remaining()
	if err != nil {
		t.Fatalf("Remaining: %v", err)
	}
	if remaining != 1 {
		t.Errorf("remaining = %d, want 1: only search-api is left", remaining)
	}
}

func TestRemainingIsZeroOnceEveryStepPassedReview(t *testing.T) {
	r := checkpoint.Run{
		Task: "find every TODO",
		Plan: twoStepPlan("find every TODO"),
		Steps: []checkpoint.StepState{
			{ID: "explore-api", Review: "ok"},
			{ID: "search-api", Review: "ok"},
		},
	}
	remaining, err := r.Remaining()
	if err != nil {
		t.Fatalf("Remaining: %v", err)
	}
	if remaining != 0 {
		t.Errorf("remaining = %d, want 0", remaining)
	}
}

// A plan that never grew past the look is never "nothing left" merely
// because the look already closed once -- Resume always redoes it (see
// orchestrator.Resume), so LayersAfter's usual accounting would undercount
// exactly the run that most needs to be found by a listing.
func TestRemainingIsNeverZeroWhenSplittingNeverRan(t *testing.T) {
	r := checkpoint.Run{
		Task: "find every TODO",
		Plan: exploreOnlyPlan("find every TODO"),
		Steps: []checkpoint.StepState{
			{ID: "explore-api", Review: "ok"},
		},
	}
	remaining, err := r.Remaining()
	if err != nil {
		t.Fatalf("Remaining: %v", err)
	}
	if remaining != 1 {
		t.Errorf("remaining = %d, want 1: the look must be redone even though it already passed review", remaining)
	}
}

// askPlan is the one-step shape Ask() builds: no Needs, because there is no
// look to wait on -- the whole point of ask is dispatching a single
// capability directly.
func askPlan(task string) contract.Plan {
	return contract.Plan{Task: task, Steps: []contract.Step{
		{ID: "ask-api", Capability: "code.search", Repository: "api",
			Permission: contract.Permission{Task: task}},
	}}
}

// An ask has no split to redo, so it must not fall into the
// splitting-never-ran case above merely because its one step also has no
// Needs: Resume's own KindAsk branch treats that step as done once it is
// OK, and a listing that disagreed would advertise a candidate whose own
// resume does nothing.
func TestRemainingIsZeroForAClosedAsk(t *testing.T) {
	r := checkpoint.Run{
		Kind: checkpoint.KindAsk, Task: "code.search in api",
		Plan:  askPlan("code.search in api"),
		Steps: []checkpoint.StepState{{ID: "ask-api", Review: "ok"}},
	}
	remaining, err := r.Remaining()
	if err != nil {
		t.Fatalf("Remaining: %v", err)
	}
	if remaining != 0 {
		t.Errorf("remaining = %d, want 0: the ask already passed review", remaining)
	}
}

func TestRemainingIsOneForAnAskThatNeverClosed(t *testing.T) {
	r := checkpoint.Run{
		Kind: checkpoint.KindAsk, Task: "code.search in api",
		Plan: askPlan("code.search in api"),
	}
	remaining, err := r.Remaining()
	if err != nil {
		t.Fatalf("Remaining: %v", err)
	}
	if remaining != 1 {
		t.Errorf("remaining = %d, want 1: the ask never closed", remaining)
	}
}

func TestCandidatesSkipsAClosedAsk(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := checkpoint.Run{
		ID: "20260804T120000-askdone", Kind: checkpoint.KindAsk, Task: "code.search in api",
		Closed: true, Verdict: "ok",
		Plan:  askPlan("code.search in api"),
		Steps: []checkpoint.StepState{{ID: "ask-api", Review: "ok"}},
	}
	if err := store.Save(done); err != nil {
		t.Fatalf("Save: %v", err)
	}
	candidates, err := store.Candidates()
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v, want none: this ask already passed review", candidates)
	}
}

func TestCandidatesSkipsRunsWithNothingLeft(t *testing.T) {
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := checkpoint.Run{
		ID: "20260802T120000-done01", Task: "find every TODO", Closed: true, Verdict: "ok",
		Plan:  twoStepPlan("find every TODO"),
		Steps: []checkpoint.StepState{{ID: "explore-api", Review: "ok"}, {ID: "search-api", Review: "ok"}},
	}
	stuck := checkpoint.Run{
		ID: "20260802T130000-stuck1", Task: "find every TODO", Closed: true, Verdict: "failed",
		Plan:  twoStepPlan("find every TODO"),
		Steps: []checkpoint.StepState{{ID: "explore-api", Review: "ok"}, {ID: "search-api", Review: "failed"}},
	}
	if err := store.Save(done); err != nil {
		t.Fatalf("Save done: %v", err)
	}
	if err := store.Save(stuck); err != nil {
		t.Fatalf("Save stuck: %v", err)
	}

	candidates, err := store.Candidates()
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != stuck.ID {
		t.Fatalf("candidates = %+v, want only %s", candidates, stuck.ID)
	}
	if candidates[0].Remaining != 1 {
		t.Errorf("remaining = %d, want 1", candidates[0].Remaining)
	}
	// A failed review is still worth another look: nothing here claims to
	// know why it failed, only that it did not pass.
	if candidates[0].Verdict != "failed" {
		t.Errorf("verdict = %q, want failed", candidates[0].Verdict)
	}
}

func TestCandidatesSkipsReceiptsThatNoLongerParse(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20260802T140000-broken.json"),
		[]byte("{not json"), 0o600); err != nil {
		t.Fatalf("seeding a broken receipt: %v", err)
	}
	candidates, err := store.Candidates()
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none", candidates)
	}
}
