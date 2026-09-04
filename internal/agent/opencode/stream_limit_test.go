package opencode

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/procgroup"
)

// TestToolEventsShareTheStreamLimit prevents small tool events from growing memory indefinitely.
func TestToolEventsShareTheStreamLimit(t *testing.T) {
	var s eventStream
	event := []byte(`{"type":"tool_use","part":{"tool":"read"}}`)
	count := procgroup.MaxOutput / (len(event) + 1)
	for range count {
		if err := s.accept(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.accept(event); err == nil {
		t.Fatal("aggregate tool event limit bypassed")
	}
	if len(s.toolCalls) != count {
		t.Fatal("over-limit event retained")
	}
}
