package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// watched builds the wrapper over a real store and a real notebook, because
// what is being tested is which facts reach the disk and there is nothing here
// worth faking.
func watched(t *testing.T) (*maintenance, *notebook.Notebook, *metrics.Store) {
	t.Helper()
	dir := t.TempDir()
	book, err := notebook.New(filepath.Join(dir, notebook.FileName))
	if err != nil {
		t.Fatalf("notebook.New: %v", err)
	}
	// A ceiling of one is how the buffer is made to overflow on purpose: the
	// second measurement in already has nowhere to go.
	store, err := metrics.Open(filepath.Join(dir, "metrics.duckdb"), metrics.Options{BufferLimit: 1})
	if err != nil {
		t.Fatalf("metrics.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &maintenance{book: book, store: store}, book, store
}

func entries(t *testing.T, book *notebook.Notebook) []notebook.Incident {
	t.Helper()
	read, err := book.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return read.Incidents
}

// The gap this closes. A job on a beat has nobody waiting on its return value,
// so before the notebook a flush failing every thirty seconds for an hour and
// a flush succeeding every thirty seconds for an hour looked identical from
// outside.
func TestAFailedBackgroundJobIsWrittenDown(t *testing.T) {
	watch, book, _ := watched(t)
	run := watch.wrap(jobFlush, func(context.Context) error {
		return errors.New("database is locked")
	})
	if err := run(context.Background()); err == nil {
		t.Fatal("the wrapper swallowed the error it was meant to pass on")
	}
	got := entries(t, book)
	if len(got) != 1 {
		t.Fatalf("entries = %d, want the failure", len(got))
	}
	if got[0].Op != jobFlush {
		t.Errorf("op = %q, want the job that failed", got[0].Op)
	}
	if !strings.Contains(got[0].Detail, "database is locked") {
		t.Errorf("detail = %q", got[0].Detail)
	}
	if got[0].Stack != "" {
		t.Error("a reported failure carries a stack, so it reads like a panic")
	}
}

// The wrapper is a wire tap, not a handler: the caller's error comes back
// exactly as it was. Do returns it to whoever asked for a flush by hand, and
// that path still has to work.
func TestTheJobsErrorReachesTheCallerUnchanged(t *testing.T) {
	watch, _, _ := watched(t)
	sentinel := contract.Fail(contract.FailureUnavailable, "nope")
	err := watch.wrap(jobCompact, func(context.Context) error { return sentinel })(context.Background())
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the job's own", err)
	}
}

// Maintenance that worked is not an incident. A notebook that filled up with
// successful flushes would bury the one entry worth reading.
func TestASucceedingJobLeavesNothing(t *testing.T) {
	watch, book, _ := watched(t)
	run := watch.wrap(jobFlush, func(context.Context) error { return nil })
	for range 20 {
		if err := run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
	}
	if got := entries(t, book); len(got) != 0 {
		t.Errorf("a quiet hour wrote %d entries", len(got))
	}
}

// Measurements thrown away at the ceiling are the serious half, and a
// different fact from a job that failed: the job will try again, these are
// gone. The baseline the funnel ranks on is short by exactly this much and
// nothing else would ever say so.
func TestDroppedMeasurementsAreReported(t *testing.T) {
	watch, book, store := watched(t)
	for i := range 5 {
		store.Record(metrics.Measurement{
			At: time.Now(), RunID: "run-1", StepID: "step-1",
			Capability: "code.search", Implementation: "ripgrep",
			Provider: "ripgrep", Repository: "current", ToolVersion: "1.0",
			Spent: contract.Sample{Duration: time.Millisecond, Tokens: i},
			OK:    true,
		})
	}
	if store.Dropped() == 0 {
		t.Fatal("the ceiling did not drop anything, so there is nothing to report")
	}
	watch.checkDrops()

	got := entries(t, book)
	if len(got) != 1 {
		t.Fatalf("entries = %d, want the loss", len(got))
	}
	if got[0].Op != "metrics.dropped" {
		t.Errorf("op = %q", got[0].Op)
	}
	if !strings.Contains(got[0].Detail, "are gone") {
		t.Errorf("detail %q does not say the loss is permanent", got[0].Detail)
	}
}

// A ceiling that stays breached must not file the same loss on every beat. An
// hour of that would be two thousand entries about one problem, which is a
// notebook nobody opens twice.
func TestAStandingLossIsNotRefiledEveryBeat(t *testing.T) {
	watch, book, store := watched(t)
	record := func(n int) {
		for range n {
			store.Record(metrics.Measurement{
				At: time.Now(), RunID: "r", StepID: "s",
				Capability: "code.search", Implementation: "ripgrep",
				Provider: "ripgrep", Repository: "current", ToolVersion: "1.0",
				Spent: contract.Sample{Duration: time.Millisecond}, OK: true,
			})
		}
	}
	record(5)
	watch.checkDrops()
	for range 10 {
		watch.checkDrops()
	}
	if got := entries(t, book); len(got) != 1 {
		t.Fatalf("entries = %d, want one for the standing loss", len(got))
	}

	// New losses on top of old ones are new news, and the entry counts only
	// the ones since the last report.
	record(4)
	watch.checkDrops()
	got := entries(t, book)
	if len(got) != 2 {
		t.Fatalf("entries = %d, want the fresh loss reported", len(got))
	}
	if strings.Contains(got[1].Detail, "8 measurements") {
		t.Errorf("the second entry re-counted the first: %q", got[1].Detail)
	}
}
