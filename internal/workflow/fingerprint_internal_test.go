package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSourceFingerprintTracksUnicodeDirtyRename(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "test"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	old := filepath.Join(root, "café.go")
	if err := os.WriteFile(old, []byte("package café\n"), 0o600); err != nil {
		t.Fatalf("write unicode source: %v", err)
	}
	if out, err := exec.Command("git", "-C", root, "add", "--", "café.go").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", root, "commit", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v (%s)", err, out)
	}
	first, err := sourceFingerprint(root)
	if err != nil {
		t.Fatalf("initial fingerprint: %v", err)
	}
	newPath := filepath.Join(root, "renamed.go")
	if err := os.Rename(old, newPath); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := os.WriteFile(newPath, []byte("package café\n\nvar changed = true\n"), 0o600); err != nil {
		t.Fatalf("change renamed source: %v", err)
	}
	second, err := sourceFingerprint(root)
	if err != nil {
		t.Fatalf("dirty fingerprint: %v", err)
	}
	if first == second {
		t.Fatal("unicode rename and content change kept the same source fingerprint")
	}
}
