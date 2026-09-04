package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestValidEnvelopeCannotHideOutputOverflow refuses a valid answer with oversized stderr.
func TestValidEnvelopeCannotHideOutputOverflow(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
echo '{"type":"result","is_error":false,"result":"done"}'
head -c 9000000 /dev/zero >&2
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{Binary: bin, Explore: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.invoke(t.Context(), t.TempDir(), time.Minute, baseRequest())
	if err == nil || !strings.Contains(err.Error(), "exceeds 8 MiB") {
		t.Fatalf("oversized answer accepted: %v", err)
	}
}
