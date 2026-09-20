package sshinventory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNoAvailableExplicitIdentity(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	existing := filepath.Join(root, "existing")
	if err := os.WriteFile(existing, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		paths []string
		want  bool
	}{
		{"no selected identity", nil, true},
		{"explicitly disabled identity", []string{"none"}, true},
		{"disabled and existing identity", []string{"none", existing}, false},
		{"selected file missing", []string{missing}, true},
		{"all selected files missing", []string{missing, missing + "-two"}, true},
		{"one selected file exists", []string{missing, existing}, false},
		{"unresolved token", []string{missing, "%d/key"}, false},
		{"other-user home", []string{missing, "~other/key"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := noAvailableExplicitIdentity(tt.paths); got != tt.want {
				t.Fatalf("no available selected identity = %v, want %v", got, tt.want)
			}
		})
	}
}
