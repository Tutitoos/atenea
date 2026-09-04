package local_test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuditScopeThroughDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private.txt"), []byte("SYNTHETIC_OUTSIDE_MARKER"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	out, err := newRunner(t).Run(t.Context(), request(t, root, map[string]any{"query": "SYNTHETIC_OUTSIDE_MARKER", "scope": []string{"linked/private.txt"}}))
	if err == nil || out.Result != nil {
		t.Fatalf("expected refusal without content: %+v %v", out, err)
	}
}
