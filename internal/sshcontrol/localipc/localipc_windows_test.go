//go:build windows

package localipc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootIdentityUsesDirectoryNotPathSpelling(t *testing.T) {
	root := t.TempDir()
	want, err := rootIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	caseAlias := strings.ToUpper(root)
	got, err := rootIdentity(caseAlias)
	if err != nil || got != want {
		t.Fatalf("case alias has a different identity: %q, %v", got, err)
	}
	t.Run("symlink alias", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(root, alias); err != nil {
			t.Skipf("directory symlink requires permission on this Windows runner: %v", err)
		}
		got, err := rootIdentity(alias)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("one installation has two pipe identities: %q and %q", want, got)
		}
	})
}

func TestRootIdentityRejectsNonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rootIdentity(file); !errors.Is(err, ErrPrivateRoot) {
		t.Fatalf("ordinary file accepted as an installation root: %v", err)
	}
}

func TestNamedPipeHasOneOwner(t *testing.T) {
	root := t.TempDir()
	first, err := Listen(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	second, err := Listen(root)
	if err == nil {
		_ = second.Close()
		t.Fatal("second controller took the same endpoint")
	}
}
