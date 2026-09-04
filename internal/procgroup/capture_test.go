package procgroup

import (
	"strings"
	"testing"
)

// TestCaptureBoundsOutput checks the regression scenario: capture bounds output.
func TestCaptureBoundsOutput(t *testing.T) {
	stopped := 0
	c := NewCapture(func() { stopped++ })
	_, _ = c.WriteString(strings.Repeat("x", MaxOutput+100))
	_, _ = c.WriteString("more")
	if c.Len() != MaxOutput || stopped != 1 {
		t.Fatal(c.Len(), stopped)
	}
}
