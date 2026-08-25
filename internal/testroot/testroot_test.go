package testroot_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/testroot"
)

// A root that already fits is kept. Moving a Linux runner's files somewhere it
// did not ask for would be a change with no defect behind it.
func TestAShortEnoughRootIsKept(t *testing.T) {
	t.Setenv(testroot.Override, "")
	t.Setenv("TMPDIR", "/tmp")

	got, err := testroot.Pin()
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	if got != "/tmp" {
		t.Errorf("root = %q, want the inherited /tmp", got)
	}
}

// The defect this package exists for: macOS hands every process a temporary
// root long enough to spend the socket allowance before a test writes anything.
func TestALongRootFallsBackAndIsPublished(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 40))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv(testroot.Override, "")
	t.Setenv("TMPDIR", long)

	got, err := testroot.Pin()
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	if got != "/tmp" {
		t.Errorf("root = %q, want the fallback", got)
	}
	// Publishing it is the whole point: t.TempDir() reads the variable, not
	// the return value, so a pin that only reported would fix nothing.
	if env := os.Getenv("TMPDIR"); env != "/tmp" {
		t.Errorf("TMPDIR = %q, want the pinned root", env)
	}
}

// An override is a decision, so it is taken at its word rather than silently
// improved upon.
func TestAnOverrideWins(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	t.Setenv(testroot.Override, "/var/tmp")

	got, err := testroot.Pin()
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	if got != "/var/tmp" {
		t.Errorf("root = %q, want the override", got)
	}
	if env := os.Getenv("TMPDIR"); env != "/var/tmp" {
		t.Errorf("TMPDIR = %q, want the override", env)
	}
}

// An override that cannot be used is an error, not a reason to pick something
// else: quietly writing somewhere other than where the caller said would make
// the next failure a mystery.
func TestAnUnusableOverrideIsRefusedRatherThanReplaced(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	t.Setenv(testroot.Override, filepath.Join(t.TempDir(), "absent"))

	if _, err := testroot.Pin(); err == nil {
		t.Fatal("an override naming a directory that does not exist was accepted")
	}
}

// A path within budget but unwritable is refused for the same reason a long one
// is: the test that lands there fails on something other than what it checks.
func TestAnOverrideThatCannotBeWrittenIsRefused(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	t.Setenv(testroot.Override, "/proc/atenea")

	if _, err := testroot.Pin(); err == nil {
		t.Fatal("an override that cannot be written was accepted")
	}
}

// The budget is a property of the socket, not of the caller's taste, so a root
// one byte over it is refused exactly like one far over.
func TestTheBudgetIsMeasuredInBytesOfPath(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	over := "/" + strings.Repeat("x", 24)
	t.Setenv(testroot.Override, over)

	_, err := testroot.Pin()
	if err == nil {
		t.Fatal("a root over the budget was accepted")
	}
	if !strings.Contains(err.Error(), "bytes") {
		t.Errorf("error = %q, want it to say what the measurement is", err)
	}
}
