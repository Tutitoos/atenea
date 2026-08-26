package kivgraph

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The regression this file exists for, and it is not a hypothetical.
//
// gitTestRepo builds a repository in t.TempDir() and sets command.Dir to it,
// which reads as sufficient and is not. GIT_DIR overrides the working
// directory when git decides which repository it is in, and a git HOOK exports
// it -- so `go test ./...` invoked from lefthook's pre-push inherits a GIT_DIR
// pointing at the real checkout. The helper's `git init` then re-initialized
// the developer's own repository, marking it core.bare = true because no
// GIT_WORK_TREE stood beside it, and its `git config user.email` wrote a test
// identity into their config. Every worktree stopped working and the next
// commit would have been authored by "Test".
//
// The test below is the one that would have caught it: it puts a repository
// somewhere else, points GIT_DIR at that repository, runs the helper, and
// checks that the repository GIT_DIR named was not touched.
func TestAHostileGitDirCannotSteerTheTestRepositoryHelper(t *testing.T) {
	// A repository standing in for the developer's own, in a place no part of
	// this test is supposed to write to.
	bystander := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = dir
		command.Env = gitEnv()
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run(bystander, "init", "-q")

	config := filepath.Join(bystander, ".git", "config")
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatalf("reading the bystander's config: %v", err)
	}

	// Exactly what a git hook hands its children.
	t.Setenv("GIT_DIR", filepath.Join(bystander, ".git"))

	// The helper under test. Under the old code this call wrote into
	// bystander's config and re-initialized it.
	gitTestRepo(t)

	after, err := os.ReadFile(config)
	if err != nil {
		t.Fatalf("reading the bystander's config back: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("the helper wrote into the repository GIT_DIR named.\nbefore:\n%s\nafter:\n%s",
			before, after)
	}
	// Named separately from the byte comparison, because these two are the
	// specific damage that was done and a future change should fail on the
	// symptom people will recognize.
	if strings.Contains(string(after), "bare = true") {
		t.Error("the bystander was marked bare, which is what breaks every worktree")
	}
	if strings.Contains(string(after), "example.invalid") {
		t.Error("a test identity was written into the bystander's config")
	}
}

// The identity has to travel in the environment rather than through
// `git config`, because `git config` writes to a file and which file it writes
// to is exactly the thing that went wrong.
func TestTheTestIdentityIsNeverWrittenToAConfigFile(t *testing.T) {
	// Set in the ambient environment exactly as a hook would, so what is
	// checked is that gitEnv REMOVES them rather than that they happened to be
	// absent on this machine.
	t.Setenv("GIT_DIR", "/somewhere/else/.git")
	t.Setenv("GIT_WORK_TREE", "/somewhere/else")

	env := gitEnv()
	// Absent entirely, not present-and-empty: `GIT_DIR=` is a GIT_DIR holding
	// the empty string and git refuses it outright, which is a different bug
	// wearing the same fix.
	for _, entry := range env {
		for _, banned := range []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR="} {
			if strings.HasPrefix(entry, banned) {
				t.Errorf("gitEnv() still carries %q; it must be removed, not blanked", entry)
			}
		}
	}
	if !slices.Contains(env, "GIT_AUTHOR_EMAIL=atenea@example.invalid") {
		t.Error("gitEnv() does not carry the test identity, so git would fall back to the developer's")
	}
	source, err := os.ReadFile("impact_index_test.go")
	if err != nil {
		t.Fatalf("reading the helper back: %v", err)
	}
	if strings.Contains(string(source), `"config", "user.email"`) {
		t.Error("the helper sets an identity with `git config`, which writes to a file " +
			"chosen by the environment rather than by this test")
	}
}
