package trace_test

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func store(t *testing.T) *trace.Store {
	t.Helper()
	s, err := trace.Open(t.Context(), filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func row(id string, at time.Time) trace.Row {
	return trace.Row{
		ID:        id,
		TypeName:  "reader",
		Kind:      contract.AgentSpecialized,
		Objective: "read one file",
		Depth:     1,
		StartedAt: at,
	}
}

func TestBeginThenCompleteClosesTheRow(t *testing.T) {
	s := store(t)
	at := time.Now()
	if err := s.Begin(t.Context(), row("a", at)); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := s.Complete(t.Context(), "a", at.Add(time.Second),
		contract.VerdictOK, contract.Reason{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	rows, err := s.List(t.Context(), trace.Filter{ID: "a"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Verdict != contract.VerdictOK || rows[0].EndedAt.IsZero() {
		t.Fatalf("rows = %+v, want one closed ok row", rows)
	}
	if rows[0].Swept {
		t.Fatal("a row its own run closed must not read as swept")
	}
}

// Two endings for one run is two claims about the same fact, and the store
// cannot know which is true -- so it keeps the first and says so.
func TestCompleteRefusesASecondEnding(t *testing.T) {
	s := store(t)
	at := time.Now()
	if err := s.Begin(t.Context(), row("a", at)); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := s.Complete(t.Context(), "a", at, contract.VerdictOK, contract.Reason{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	err := s.Complete(t.Context(), "a", at, contract.VerdictFailed,
		contract.Reason{Kind: contract.FailureInvalidInput, Text: "second opinion"})
	if err == nil {
		t.Fatal("Complete: a row must not be closed twice")
	}
	rows, _ := s.List(t.Context(), trace.Filter{ID: "a"})
	if rows[0].Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v, want the first ending kept", rows[0].Verdict)
	}
}

// The sweep is the whole reason the row is written before the spawn: a run
// nobody saw finish is incomplete, never failed, and the reason bin is the
// one thing actually known -- whatever was going to answer is not there.
func TestSweepClosesARowWhoseWriterIsGone(t *testing.T) {
	s := store(t)

	// A pid that is really gone: spawn something, wait for it, then use its
	// pid. Inventing a number could collide with a live process.
	dead := exec.Command("/bin/true")
	if err := dead.Run(); err != nil {
		t.Fatalf("running the throwaway process: %v", err)
	}
	open := row("orphan", time.Now())
	open.WriterPID = dead.Process.Pid
	if err := s.Begin(t.Context(), open); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	closed, err := s.SweepOrphans(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed %d rows, want 1", closed)
	}
	rows, _ := s.List(t.Context(), trace.Filter{ID: "orphan"})
	if rows[0].Verdict != contract.VerdictIncomplete {
		t.Fatalf("verdict = %v, want incomplete", rows[0].Verdict)
	}
	if rows[0].Reason.Kind != contract.FailureUnavailable {
		t.Fatalf("reason kind = %v, want unavailable", rows[0].Reason.Kind)
	}
	if !rows[0].Swept {
		t.Fatal("a row the sweep closed must be distinguishable from one that reported")
	}
}

// The gate that keeps the sweep honest: another Atenea may be mid-run right
// now, and closing its row would be the sweep inventing the very thing it
// exists to record.
func TestSweepLeavesALiveWritersRowAlone(t *testing.T) {
	s := store(t)

	live := exec.Command("/bin/sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatalf("starting the live process: %v", err)
	}
	t.Cleanup(func() { _ = live.Process.Kill(); _ = live.Wait() })

	open := row("held", time.Now())
	open.WriterPID = live.Process.Pid
	if err := s.Begin(t.Context(), open); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	closed, err := s.SweepOrphans(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if closed != 0 {
		t.Fatalf("closed %d rows, want none while the writer is alive", closed)
	}
	rows, _ := s.List(t.Context(), trace.Filter{ID: "held"})
	if !rows[0].EndedAt.IsZero() {
		t.Fatalf("row = %+v, want it still open", rows[0])
	}
}

func TestFilters(t *testing.T) {
	s := store(t)
	at := time.Now()
	for _, id := range []string{"a", "b"} {
		if err := s.Begin(t.Context(), row(id, at)); err != nil {
			t.Fatalf("Begin: %v", err)
		}
	}
	if err := s.Complete(t.Context(), "a", at, contract.VerdictOK, contract.Reason{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	open, err := s.List(t.Context(), trace.Filter{OpenOnly: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(open) != 1 || open[0].ID != "b" {
		t.Fatalf("open rows = %+v, want only b", open)
	}
	ok, err := s.List(t.Context(), trace.Filter{Verdict: contract.VerdictOK})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ok) != 1 || ok[0].ID != "a" {
		t.Fatalf("ok rows = %+v, want only a", ok)
	}
	none, err := s.List(t.Context(), trace.Filter{TypeName: "writer"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("rows = %+v, want none for an undeclared type", none)
	}
}

func TestBeginNeedsAnIDAndAStart(t *testing.T) {
	s := store(t)
	if err := s.Begin(t.Context(), trace.Row{StartedAt: time.Now()}); err == nil {
		t.Fatal("Begin: a row with no id must be refused")
	}
	if err := s.Begin(t.Context(), trace.Row{ID: "a"}); err == nil {
		t.Fatal("Begin: a row with no start must be refused")
	}
}
