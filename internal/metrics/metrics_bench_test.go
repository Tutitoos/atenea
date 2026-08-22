package metrics

import (
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
