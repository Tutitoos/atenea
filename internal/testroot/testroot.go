// Package testroot keeps a test binary's temporary files inside a path short
// enough to still name a unix socket.
//
// The kernel bounds a socket path by sun_path: 104 bytes on the BSDs, 108 on
// Linux, which internal/ipc rounds down to one number that works everywhere.
// That budget is spent before a test writes anything: macOS hands every process
// a per-user TMPDIR under /var/folders/<hash>/T/ that is already 49 bytes, and
// t.TempDir() appends the test's own name to it. A test whose name says what it
// checks -- which is the naming this suite uses on purpose -- then cannot bind.
//
// The suite used to answer that with TMPDIR=/tmp, set in the CI workflow. That
// makes the green run a property of the workflow file rather than of the code:
// the same suite fails on a developer's Mac, the pre-push hook fails with it,
// and the hook gets skipped. A suite that only passes under an environment
// variable somebody else remembered to set is not a suite that passes.
//
// So the binary pins its own temporary root, before the first test runs. It is
// still an environment variable underneath, because that is the only thing
// t.TempDir() reads -- the difference is who sets it, and that nobody has to.
package testroot

import (
	"fmt"
	"os"
	"path/filepath"
)

// budget is the room a pinned root may take out of the socket path.
//
// It is not derived from maxPath by arithmetic, because what a test appends
// afterwards is not knowable here: t.TempDir() adds the test's name and an
// ordinal, and the code under test adds its own layout beneath that. 24 bytes
// leaves the rest of the allowance to them and is wide enough for the roots
// that are actually offered -- /tmp, /var/tmp, and whatever a caller names
// explicitly.
const budget = 24

// Override is the environment variable a caller uses to name the root itself.
//
// It exists for the machine where neither the inherited temporary directory nor
// /tmp is the right answer -- a sandbox with a read-only /tmp, or a filesystem
// the caller needs the test files to land on. Naming it is a decision, so it is
// taken at its word: an override that cannot be used is an error rather than a
// reason to quietly pick something else.
const Override = "ATENEA_TEST_TMPDIR"

// Pin points this test binary's temporary root at a directory short enough to
// hold a socket, and reports which one it picked.
//
// Call it from TestMain before m.Run. It sets TMPDIR for the whole process,
// which is what t.TempDir() reads on every call, so every test in the package
// is covered without any of them saying so.
//
// An inherited root that is already short enough is kept. Pinning is meant to
// repair a path that cannot work, not to move a Linux runner's files somewhere
// it did not ask for.
func Pin() (string, error) {
	if named := os.Getenv(Override); named != "" {
		if err := usable(named); err != nil {
			return "", fmt.Errorf("%s=%s: %w", Override, named, err)
		}
		if err := os.Setenv("TMPDIR", named); err != nil {
			return "", fmt.Errorf("pinning TMPDIR to %s: %w", named, err)
		}
		return named, nil
	}
	inherited := os.TempDir()
	if usable(inherited) == nil {
		return inherited, nil
	}
	const fallback = "/tmp"
	if err := usable(fallback); err != nil {
		return "", fmt.Errorf(
			"temporary root %s is %d bytes, more than the %d a socket path can spare, and %s: %w",
			inherited, len(inherited), budget, fallback, err)
	}
	if err := os.Setenv("TMPDIR", fallback); err != nil {
		return "", fmt.Errorf("pinning TMPDIR to %s: %w", fallback, err)
	}
	return fallback, nil
}

// usable reports whether a root is both short enough and writable. Both are
// checked here because a root that fails either one is equally unusable, and a
// caller that had to ask twice would have to decide what a half-answer means.
func usable(root string) error {
	if len(root) > budget {
		return fmt.Errorf("path is %d bytes and the socket allowance here is %d", len(root), budget)
	}
	probe, err := os.MkdirTemp(root, "at")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(probe) }()
	return os.WriteFile(filepath.Join(probe, "w"), nil, 0o600)
}
