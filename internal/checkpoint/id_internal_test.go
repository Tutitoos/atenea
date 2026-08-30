package checkpoint

import (
	"errors"
	"testing"
	"time"
)

func TestNewIDFallbackRemainsUniqueWhenEntropyFails(t *testing.T) {
	previous := randomRead
	randomRead = func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
	t.Cleanup(func() { randomRead = previous })

	now := time.Unix(1_700_000_000, 123)
	seen := make(map[string]struct{}, 5000)
	for range 5000 {
		id := NewID(now)
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate fallback run id %s", id)
		}
		seen[id] = struct{}{}
	}
}
