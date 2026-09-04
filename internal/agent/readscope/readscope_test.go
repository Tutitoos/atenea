package readscope

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRootAndAssignedFiles checks the regression scenario: root and assigned files.
func TestRootAndAssignedFiles(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "yes"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if body, err := ReadFile(root, "yes", []string{"yes"}); err != nil || string(body) != "yes" {
		t.Fatal(string(body), err)
	}
	for _, name := range []string{"linked/secret", filepath.Join(outside, "secret")} {
		if _, err := ReadFile(root, name, []string{name}); err == nil {
			t.Fatal("escaped", name)
		}
	}
	if _, err := ReadFile(root, "yes", []string{"other"}); err == nil {
		t.Fatal("unassigned read")
	}
}
