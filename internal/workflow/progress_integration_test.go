package workflow_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
)

func TestCompletedPointIsPersistedThenPublishedAsOneChecklistItem(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var published []string
	h := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Activity: func(batch []workflow.ActivityNotice) error {
		mu.Lock()
		defer mu.Unlock()
		for _, notice := range batch {
			if strings.Contains(notice.Markdown, "**Progreso:**") {
				published = append(published, notice.Markdown)
			}
		}
		return nil
	}}, dir,
		declared("worker", answers(t, dir, "worker"), config.PoolAgent),
		declared("reviewer", answers(t, dir, "reviewer"), config.PoolReview))

	implementation := step("implementation", "worker", nil)
	implementation.PointID, implementation.PointTitle = "P06", "Checklist and progress"
	review := reviewing(step("review", "reviewer", []string{"implementation"}), "implementation")
	review.PointID, review.PointTitle = "P06", "Checklist and progress"
	run, err := h.engine.Start(t.Context(), graphOf(implementation, review))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(run.Points) != 1 || run.Points[0].State != workflow.PointAccepted {
		t.Fatalf("persisted points = %+v", run.Points)
	}
	if len(run.Points[0].Evidence) != 2 {
		t.Fatalf("evidence = %v, want both internal traces", run.Points[0].Evidence)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(published) == 0 {
		t.Fatal("no progress update reached the activity callback")
	}
	last := published[len(published)-1]
	seenRunning := false
	for _, update := range published[:len(published)-1] {
		if strings.Contains(update, "(en curso)") {
			seenRunning = true
		}
	}
	if !seenRunning {
		t.Fatalf("progress never exposed an in-progress state: %#v", published)
	}
	if !strings.Contains(last, "> - [x] **P06.** Checklist and progress") || strings.Count(last, "> - [") != 1 {
		t.Fatalf("final checklist =\n%s", last)
	}
	if !strings.Contains(last, "100 % · 1/1") {
		t.Fatalf("final progress =\n%s", last)
	}
}

func TestCancelPersistsUncheckedPointAsBlocked(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, noCeiling(), declared("worker", answers(t, dir, "worker"), config.PoolAgent))
	pending := step("pending", "worker", nil)
	pending.PointID, pending.PointTitle = "P03", "Persistent workflows"
	run, _, err := h.engine.Create(t.Context(), graphOf(pending))
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.state.Cancel(t.Context(), run.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Points) != 1 || run.Points[0].State != workflow.PointBlocked {
		t.Fatalf("points after cancel = %+v", run.Points)
	}
}
