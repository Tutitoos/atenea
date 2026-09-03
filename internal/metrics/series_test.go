package metrics_test

import (
	"context"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestSeriesBucketsRetainedAttemptsAndCoverage(t *testing.T) {
	store, err := metrics.Open(t.TempDir()+"/metrics.duckdb", metrics.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Keep both attempts in one bucket even when the test starts on a minute
	// boundary; using raw time.Now() made the assertion flaky under -race.
	at := time.Now().UTC().Truncate(time.Minute).Add(-2*time.Minute + 5*time.Second)
	store.Record(metrics.Measurement{At: at, RunID: "r1", StepID: "s1", Capability: "code.search", Implementation: "ripgrep", Provider: "local", Repository: "atenea", Spent: contract.Sample{Duration: 40 * time.Millisecond, Tokens: 12}, OK: true})
	store.Record(metrics.Measurement{At: at.Add(10 * time.Second), RunID: "r2", StepID: "s2", Capability: "code.search", Implementation: "ripgrep", Provider: "local", Repository: "atenea", OK: false})
	points, err := store.Series(context.Background(), at.Add(-time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Attempts != 2 || points[0].Successes != 1 || points[0].Failures != 1 || points[0].Tokens != 12 || points[0].MeasuredRows != 2 {
		t.Fatalf("series = %+v", points)
	}
}
