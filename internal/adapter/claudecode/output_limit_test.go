package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStreamResultCannotHideOverflow refuses output beyond a previously valid result.
func TestStreamResultCannotHideOverflow(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
echo '{"type":"result","is_error":false,"result":"done"}'
awk 'BEGIN { for(i=0;i<9000;i++){for(j=0;j<1024;j++)printf "x";printf "\n"} }'
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := New(Options{Binary: bin, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	req := request(t, map[string]any{"query": "needle"})
	_, _, err = r.invoke(t.Context(), t.TempDir(), req, newSearch(t, req.Payload))
	if err == nil || !strings.Contains(err.Error(), "exceeds 8 MiB") {
		t.Fatalf("oversized stream accepted: %v", err)
	}
}
