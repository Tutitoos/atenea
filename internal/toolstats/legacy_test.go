package toolstats_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/toolstats"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// TestLegacyCutoffAndBoundarySummaries verifies historical cutoffs and incomplete summary intervals.
func TestLegacyCutoffAndBoundarySummaries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "base.duckdb")
	store, err := metrics.Open(path, metrics.Options{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cut := now.Add(-time.Hour)
	old := now.AddDate(0, 0, -12).Truncate(24 * time.Hour).Add(time.Hour)
	for _, at := range []time.Time{old, cut.Add(-time.Minute), cut.Add(time.Minute)} {
		store.Record(metrics.Measurement{At: at, Capability: "code.search", Implementation: "impl", Provider: "p", Repository: "app", OK: true, Spent: contract.Sample{Duration: 10 * time.Millisecond}})
	}
	if err = store.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Compact(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	out := toolstats.Snapshot{Query: toolstats.Query{Until: now}, Coverage: toolstats.Coverage{Started: &cut}}
	if err = toolstats.Legacy(ctx, path, &out); err != nil {
		t.Fatal(err)
	}
	var calls int64
	for _, r := range out.Legacy {
		calls += r.Calls
	}
	if calls != 2 {
		t.Fatalf("cutoff counted %d calls, want two", calls)
	}
	out = toolstats.Snapshot{Query: toolstats.Query{Since: old.Add(-time.Minute), Until: cut}, Coverage: toolstats.Coverage{Started: &cut}}
	if err = toolstats.Legacy(ctx, path, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Coverage.Partial {
		t.Fatal("straddling summary was silently counted")
	}
}

// TestConcurrentLegacyReadersQueueByPath prevents overlapping DuckDB handles.
func TestConcurrentLegacyReadersQueueByPath(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "base.duckdb")
	store, err := metrics.Open(path, metrics.Options{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store.Record(metrics.Measurement{At: now, Capability: "code.search", Implementation: "impl", Provider: "p", Repository: "app", OK: true})
	if err = store.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	const readers = 8
	var start sync.WaitGroup
	start.Add(1)
	errs := make(chan error, readers)
	for range readers {
		go func() {
			start.Wait()
			out := toolstats.Snapshot{Query: toolstats.Query{Since: now.Add(-time.Minute), Until: now.Add(time.Minute)}}
			err := toolstats.Legacy(ctx, path, &out)
			if err == nil && (len(out.Legacy) != 1 || out.Legacy[0].Calls != 1) {
				err = fmt.Errorf("unexpected legacy rows: %+v", out.Legacy)
			}
			errs <- err
		}()
	}
	start.Done()
	for range readers {
		if err := <-errs; err != nil {
			t.Errorf("concurrent legacy reader: %v", err)
		}
	}
}
