package sourceidentity

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNestedRepositoryRootDetectsContentChange(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	git("init", "-q")
	sub := filepath.Join(root, "app")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "source.txt")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("initial")
	git("add", ".")
	git("-c", "user.name=Audit", "-c", "user.email=audit@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
	write("first dirty contents")
	first, err := Discover(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	write("second dirty contents")
	second, err := Discover(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Fatalf("different contents under nested root have identical identity %s (git=%v)", first.Fingerprint, first.Git)
	}
	// Porcelain -z preserves spaces in filenames, including the edges.
	spaced := filepath.Join(sub, " spaced name .txt ")
	if err := os.WriteFile(spaced, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	third, err := Discover(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spaced, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	fourth, err := Discover(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if third.Fingerprint == fourth.Fingerprint || !fourth.Untracked {
		t.Fatalf("untracked spaced filename did not change nested identity: third=%+v fourth=%+v", third, fourth)
	}
	// Existing identities covered the entire Git repository; keep that
	// behavior when the configured workspace is nested.
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	fifth, err := Discover(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	sixth, err := Discover(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if fifth.Fingerprint == sixth.Fingerprint {
		t.Fatal("outside change did not invalidate repository-wide identity")
	}
	git("mv", "app/source.txt", "app/renamed.txt")
	renamed := filepath.Join(sub, "renamed.txt")
	if err := os.WriteFile(renamed, []byte("renamed first"), 0600); err != nil {
		t.Fatal(err)
	}
	seventh, err := Discover(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(renamed, []byte("renamed other"), 0600); err != nil {
		t.Fatal(err)
	}
	eighth, err := Discover(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if seventh.Fingerprint == eighth.Fingerprint {
		t.Fatal("editing a staged rename did not change nested identity")
	}
	if err := os.Remove(renamed); err != nil {
		t.Fatal(err)
	}
	ninth, err := Discover(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if eighth.Fingerprint == ninth.Fingerprint {
		t.Fatal("deleting a staged rename did not change nested identity")
	}
}
