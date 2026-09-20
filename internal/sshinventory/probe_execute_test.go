package sshinventory

import (
	"os"
	"path/filepath"
	"strings"
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

func TestProxyDiagnosticStderrIsBoundedAndNeverClassifiedAfterOverflow(t *testing.T) {
	var sink boundedProbeStderr
	input := strings.Repeat("x", 32<<10)
	if n, err := sink.Write([]byte(input)); err != nil || n != len(input) {
		t.Fatalf("bounded diagnostic write = %d, %v", n, err)
	}
	if n, err := sink.Write([]byte("y")); err != nil || n != 1 {
		t.Fatalf("overflow write = %d, %v", n, err)
	}
	if len(sink.data) > 32<<10 || !sink.overflow || sink.classify(nil) != ProbeFailureUnknown {
		t.Fatal("overflow diagnostic retained excess text or produced a trusted classification")
	}
}
