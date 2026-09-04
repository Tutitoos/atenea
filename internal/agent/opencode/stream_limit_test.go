package opencode

import (
	"bufio"
	"strings"
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

// TestCRLFEventsCountBothDelimiterBytes verifies the scanner cannot hide one
// byte per Windows-style event delimiter from the aggregate limit.
func TestCRLFEventsCountBothDelimiterBytes(t *testing.T) {
	event := `{"type":"tool_use","part":{"tool":"read"}}`
	for _, test := range []struct {
		name      string
		delimiter string
		wantError bool
	}{
		{name: "LF fits exactly", delimiter: "\n"},
		{name: "CRLF exceeds by one", delimiter: "\r\n", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stream eventStream
			stream.bytes = procgroup.MaxOutput - len(event) - 1
			scanner := bufio.NewScanner(strings.NewReader(event + test.delimiter))
			scanner.Split(scanEventFrame)
			if !scanner.Scan() {
				t.Fatalf("Scan: %v", scanner.Err())
			}
			err := stream.acceptFrame(scanner.Bytes())
			if (err != nil) != test.wantError {
				t.Fatalf("acceptFrame error = %v, wantError %v", err, test.wantError)
			}
			if scanner.Scan() || scanner.Err() != nil {
				t.Fatalf("unexpected trailing scan: %v", scanner.Err())
			}
		})
	}
}
