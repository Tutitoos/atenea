package sourceidentity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDiscoverTracksHeadDirtyAndUntrackedWithoutSourceText(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "test"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "add", "main.go").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", root, "commit", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v (%s)", err, out)
	}
	first, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Head == "" || first.Fingerprint == "" || first.Dirty || first.Untracked {
		t.Fatalf("initial identity = %+v", first)
	}
	if err := os.WriteFile(path, []byte("package main\nvar changed = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Dirty || second.Fingerprint == first.Fingerprint {
		t.Fatalf("dirty identity = %+v; first=%+v", second, first)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("secret-shaped content"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !third.Untracked || third.Fingerprint == second.Fingerprint {
		t.Fatalf("untracked identity = %+v; second=%+v", third, second)
	}
}

func TestDiscoverNonGitRootIsDeterministicAndChangesWithContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Git || first.Fingerprint == "" {
		t.Fatalf("non-git identity = %+v", first)
	}
	if err := os.WriteFile(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if second.Fingerprint == first.Fingerprint {
		t.Fatal("non-git content change did not invalidate identity")
	}
}
