package codebasememory

// identifierAt and snippetFor are the two places this adapter touches the
// filesystem directly; both are pure enough, against a real temp directory,
// that faking anything would only add distance from what they actually do.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestIdentifierAtReadsTheWordUnderTheColumn(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package auth\n\nfunc Login(user string) {}\n")

	cases := []struct {
		name         string
		line, column int
		want         string
	}{
		{"the start of the identifier", 3, 6, "Login"},
		{"the middle of the identifier", 3, 8, "Login"},
		{"the end of the identifier", 3, 10, "Login"},
		{"the first word on the first line", 1, 1, "package"},
		{"a parameter name", 3, 12, "user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newTestRunner(t)
			got, err := runner.identifierAt(root, "main.go", tc.line, tc.column)
			if err != nil {
				t.Fatalf("identifierAt: %v", err)
			}
			if got != tc.want {
				t.Errorf("identifierAt(%d,%d) = %q, want %q", tc.line, tc.column, got, tc.want)
			}
		})
	}
}

func TestIdentifierAtReadsAUnicodeIdentifier(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.py", "def café_niño():\n    pass\n")
	got, err := newTestRunner(t).identifierAt(root, "main.py", 1, 6)
	if err != nil {
		t.Fatalf("identifierAt: %v", err)
	}
	if got != "café_niño" {
		t.Errorf("identifierAt = %q, want café_niño", got)
	}
}

func TestIdentifierAtRefusesAColumnPastTheEndOfTheLine(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "x := 1\n")
	_, err := newTestRunner(t).identifierAt(root, "main.go", 1, 50)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err = %v)", got, err)
	}
}

func TestIdentifierAtRefusesANonWordColumn(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "x := 1\n")
	// Column 3 is the space between "x" and ":=".
	_, err := newTestRunner(t).identifierAt(root, "main.go", 1, 3)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err = %v)", got, err)
	}
}

func TestIdentifierAtRefusesTooFewLines(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "one line only\n")
	_, err := newTestRunner(t).identifierAt(root, "main.go", 5, 1)
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (err = %v)", got, err)
	}
}

func TestIdentifierAtRefusesASensitiveFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".env", "SECRET=hunter2\n")
	_, err := newTestRunner(t).identifierAt(root, ".env", 1, 1)
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied (err = %v)", got, err)
	}
}

func TestIdentifierAtRefusesAMissingFile(t *testing.T) {
	root := t.TempDir()
	_, err := newTestRunner(t).identifierAt(root, "nowhere.go", 1, 1)
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found (err = %v)", got, err)
	}
}

func TestSnippetForReadsAForwardWindowNotCentered(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "1\n2\n3\n4\n5\n6\n")
	got, err := newTestRunner(t).snippetFor(root, "main.go", 3, 3)
	if err != nil {
		t.Fatalf("snippetFor: %v", err)
	}
	// Starting AT line 3, not centered on it: lines 3, 4 and 5.
	if want := "3\n4\n5"; got != want {
		t.Errorf("snippetFor = %q, want %q", got, want)
	}
}

func TestSnippetForClampsAtEndOfFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "1\n2\n3\n")
	got, err := newTestRunner(t).snippetFor(root, "main.go", 2, 10)
	if err != nil {
		t.Fatalf("snippetFor: %v", err)
	}
	if want := "2\n3"; got != want {
		t.Errorf("snippetFor = %q, want %q", got, want)
	}
}

func TestSnippetForRefusesASensitiveFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "id_rsa", "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	_, err := newTestRunner(t).snippetFor(root, "id_rsa", 1, 3)
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied (err = %v)", got, err)
	}
}

func TestWithinRefusesPathsThatEscapeTheRepository(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		"../outside.go",
		"a/../../outside.go",
		"/etc/passwd",
		"",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := within(root, name); contract.KindOf(err) != contract.FailureInvalidInput {
				t.Errorf("within(%q) did not refuse an escape (err = %v)", name, err)
			}
		})
	}
}

func TestWithinAllowsAPathThatDoesNotExistYet(t *testing.T) {
	root := t.TempDir()
	resolved, err := within(root, "not/written/yet.go")
	if err != nil {
		t.Fatalf("within: %v", err)
	}
	want := filepath.Join(root, "not", "written", "yet.go")
	if resolved != want {
		t.Errorf("within = %q, want %q", resolved, want)
	}
}

func TestWithinRefusesASymlinkThatResolvesOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt", "hunter2\n")
	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := within(root, "escape.txt"); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("within did not refuse a symlink resolving outside the repository (err = %v)", err)
	}
}

func TestWithinAllowsASymlinkThatResolvesInside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on windows")
	}
	root := t.TempDir()
	writeFile(t, root, "real.txt", "hi\n")
	link := filepath.Join(root, "alias.txt")
	if err := os.Symlink(filepath.Join(root, "real.txt"), link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := within(root, "alias.txt"); err != nil {
		t.Errorf("within refused a symlink that stays inside the repository: %v", err)
	}
}

func TestIsWordAcceptsUnicodeAndUnderscoreButNotPunctuation(t *testing.T) {
	cases := map[rune]bool{
		'a': true, 'Z': true, '_': true, '9': true,
		'é': true, '日': true,
		' ': false, '.': false, '(': false, '\t': false,
	}
	for r, want := range cases {
		if got := isWord(r); got != want {
			t.Errorf("isWord(%q) = %v, want %v", r, got, want)
		}
	}
}

// writeFile writes body under root/name, creating parent directories as
// needed.
func writeFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
