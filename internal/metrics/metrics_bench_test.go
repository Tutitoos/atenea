package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func BenchmarkRecord(b *testing.B) {
	s := &Store{limit: DefaultBufferLimit}
	m := Measurement{
		At:             time.Unix(1, 0),
		Capability:     "code.search",
		Implementation: "ripgrep",
		Provider:       "omp",
		Repository:     "atenea",
		Spent:          contract.Sample{Duration: time.Millisecond, Tokens: 32},
		OK:             true,
		Raw:            "provider completed successfully",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.Record(m)
	}
}

func BenchmarkFlushMeasurement(b *testing.B) {
	s, err := Open(b.TempDir()+"/metrics.duckdb", Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	measurement := Measurement{At: time.Unix(1, 0), RunID: "benchmark", StepID: "flush", Capability: "code.search", Implementation: "ripgrep", Provider: "local", Repository: "current", ToolVersion: "benchmark", OK: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Record(measurement)
		if err := s.Flush(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
