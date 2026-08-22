package selector_test

import (
	"fmt"
	"testing"

	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func BenchmarkSelectMediumCatalog(b *testing.B) {
	s, err := selector.New(selector.Config{})
	if err != nil {
		b.Fatal(err)
	}
	candidates := make([]contract.Implementation, 0, 64)
	reachable := make([]string, 0, 64)
	for i := range 64 {
		id := fmt.Sprintf("provider-%02d", i)
		candidates = append(candidates, impl(id))
		reachable = append(reachable, id)
	}
	req := selector.Request{
		Capability: "code.search",
		Repository: smallGoRepo(),
		Candidates: candidates,
		Reachable:  reachable,
		Measuring:  true,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := s.Select(req); err != nil {
			b.Fatal(err)
		}
	}
}
