// Package scripts_test covers the shell that gates this repository.
//
// These files decide whether a release happens and whether a commit is allowed,
// and until now nothing exercised them: their defects were found by reading
// them, or by a release. A directory of Go tests next to the scripts gives
// `go test ./...` -- which every gate already runs -- a way to fail first.
package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the checkout, from the scripts/ directory these tests run in.
const repoRoot = ".."

// The opt-in guard is the first thing this script does, and under `set -u` it
// used to be the thing that never ran: expanding an undefined ATENEA_MCP_CHECK
// aborted the shell with "unbound variable" and status 1, so an operator who
// ran the script bare was told nothing about how to run it on purpose, and the
// status they got was the one that means "the smoke failed".
func TestTheLiveMCPSmokeRefusesWithItsOptInMessageWhenNothingIsSet(t *testing.T) {
	for _, value := range []struct {
		name    string
		environ []string
	}{
		{"unset", nil},
		{"set to something other than 1", []string{"ATENEA_MCP_CHECK=0"}},
	} {
		t.Run(value.name, func(t *testing.T) {
			command := exec.Command("bash", "mcp-live-check.sh")
			command.Env = append(environWithout("ATENEA_MCP_CHECK"), value.environ...)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("the smoke ran without the opt-in; output:\n%s", output)
			}
			var exit *exec.ExitError
			if !asExitError(err, &exit) {
				t.Fatalf("run: %v", err)
			}
			if code := exit.ExitCode(); code != 2 {
				t.Errorf("exit status %d, want the documented 2; output:\n%s", code, output)
			}
			if !strings.Contains(string(output), "Set ATENEA_MCP_CHECK=1") {
				t.Errorf("the refusal never says how to opt in; output:\n%s", output)
			}
		})
	}
}

// The readiness gate is the only thing that parses these scripts before a human
// runs one in anger, and it used to name them one by one. build-claude-mcpb.sh
// was written, committed and used for a release while absent from that list. A
// gate whose coverage is maintained by remembering to edit it covers whatever
// was last remembered.
func TestTheReadinessGateSyntaxChecksEveryShellScript(t *testing.T) {
	body, err := os.ReadFile("v1-readiness.sh")
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var arguments string
	for _, line := range strings.Split(string(body), "\n") {
		if rest, found := strings.CutPrefix(line, "bash -n "); found {
			arguments = rest
			break
		}
	}
	if arguments == "" {
		t.Fatal("v1-readiness.sh no longer parses the scripts at all")
	}

	// Expanded by a shell, so that a glob, a list or any mixture of the two is
	// judged by what it actually covers rather than by how it is written.
	expansion := exec.Command("bash", "-c", "printf '%s\n' "+arguments)
	expansion.Dir = repoRoot
	expanded, err := expansion.Output()
	if err != nil {
		t.Fatalf("expand %q: %v", arguments, err)
	}
	checked := make(map[string]bool)
	for _, path := range strings.Fields(string(expanded)) {
		checked[path] = true
	}

	present, err := filepath.Glob(filepath.Join(repoRoot, "scripts", "*.sh"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(present) == 0 {
		t.Fatal("no shell scripts found; this test would pass for an empty tree")
	}
	for _, path := range present {
		name := "scripts/" + filepath.Base(path)
		if !checked[name] {
			t.Errorf("%s never reaches `bash -n` in the readiness gate", name)
		}
	}
}

// A path with a space in it used to break this hook, and break it the wrong
// way round: `gofmt -l {staged_files}` handed gofmt the two halves of the name,
// neither of which exists, so a correctly formatted tree was rejected with "no
// such file or directory". A pre-commit hook that fails for a reason the author
// cannot act on is a pre-commit hook that teaches people --no-verify.
func TestTheCommitHookJudgesStagedFilesWhateverTheirPathsContain(t *testing.T) {
	repository := newRepository(t)
	repository.write(t, "a file with spaces.go", "package p\n\nfunc  F( ) {}\n")
	repository.git(t, "add", "--", "a file with spaces.go")

	output, err := repository.hook(t)
	if err == nil {
		t.Fatalf("the hook accepted an unformatted file; output:\n%s", output)
	}
	if !strings.Contains(output, "a file with spaces.go") {
		t.Errorf("the hook does not name the file it rejected; output:\n%s", output)
	}
	if strings.Contains(output, "No such file or directory") ||
		strings.Contains(output, "no such file or directory") {
		t.Errorf("the path was split before gofmt saw it; output:\n%s", output)
	}

	repository.write(t, "a file with spaces.go", "package p\n\nfunc F() {}\n")
	repository.git(t, "add", "--", "a file with spaces.go")
	if output, err := repository.hook(t); err != nil {
		t.Errorf("the hook rejected a formatted file: %v; output:\n%s", err, output)
	}
}

// gofmt cannot read a file that a commit is about to delete, and the old hook
// asked it to: the staged-file template included deletions, so removing a Go
// file failed the commit that removed it.
func TestTheCommitHookLetsADeletionThrough(t *testing.T) {
	repository := newRepository(t)
	repository.write(t, "gone.go", "package p\n\nfunc G() {}\n")
	repository.git(t, "add", "--", "gone.go")
	repository.git(t, "commit", "-m", "add")
	repository.git(t, "rm", "-q", "--", "gone.go")

	if output, err := repository.hook(t); err != nil {
		t.Errorf("the hook rejected a staged deletion: %v; output:\n%s", err, output)
	}
}

// The hook body belongs in a file the tests above can run. Left inline in
// lefthook.yml it was reachable only by making a commit, which is why it went
// four releases with a quoting bug nobody could have noticed on a repository
// whose paths happen to have no spaces.
func TestTheCommitHookIsTheScriptTheseTestsExercise(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot, "lefthook.yml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "scripts/gofmt-staged.sh") {
		t.Error("lefthook.yml does not run scripts/gofmt-staged.sh")
	}
	// Comments are allowed to quote the template -- the one above the hook
	// explains what it used to do -- so only what lefthook executes counts.
	for number, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.Contains(line, "{staged_files}") {
			t.Errorf("lefthook.yml:%d interpolates staged paths into a command line: %s",
				number+1, strings.TrimSpace(line))
		}
	}
}

// The pre-push hook runs the whole suite, and a hook runs with GIT_DIR
// exported. Several tests in this tree shell out to git inside a temp
// directory; GIT_DIR beats their working directory when git decides which
// repository it is in, so the suite invoked from here operated on the real
// checkout instead. One `git init` marked it core.bare = true, which breaks
// every worktree, and a `git config user.email` wrote a test identity into it.
//
// The tests scrub the variables themselves and that is the fix that matters,
// because it holds however the suite is invoked. This is the second net, and
// it is a net worth having: the failure was silent, it arrived through a
// command nobody thinks of as dangerous, and it left a repository to repair by
// hand.
func TestThePrePushHookDoesNotHandGitDirToTheSuite(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot, "lefthook.yml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var line string
	for _, candidate := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(candidate)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "go test") && strings.Contains(trimmed, "-race") {
			line = trimmed
			break
		}
	}
	if line == "" {
		t.Fatal("lefthook.yml no longer runs the race suite before a push")
	}
	for _, variable := range []string{"GIT_DIR", "GIT_WORK_TREE"} {
		if !strings.Contains(line, "-u "+variable) {
			t.Errorf("the pre-push suite inherits %s: %s", variable, line)
		}
	}
}

// A workflow that installs a floating tag runs code nobody chose and leaves no
// record of which code that was. host-footer.yml installed opencode-ai@latest
// on a daily schedule and then executed the binary inside it, while the other
// workflow using the same package pinned the version -- so the practice existed
// and simply had not been applied here. Watching the newest release is a fine
// purpose; it is served by resolving the number first and installing that.
func TestNoWorkflowInstallsAnNpmPackageAtAFloatingTag(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(repoRoot, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no workflows found; this test would pass for an empty tree")
	}
	install := regexp.MustCompile(`npm\s+(i|install|add)\b`)
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		for number, line := range strings.Split(string(body), "\n") {
			if install.MatchString(line) && strings.Contains(line, "@latest") {
				t.Errorf("%s:%d installs a floating version: %s",
					filepath.Base(path), number+1, strings.TrimSpace(line))
			}
		}
	}
}

// The matrix gate checks arcs, and it used to check only where they land. It
// searched the whole catalog for the implementation's line and never asked
// which capability that line sat under, so an implementation registered against
// the wrong capability -- the one failure a provider matrix exists to catch --
// satisfied the requirement while the error message claimed the whole edge had
// been verified.
func TestTheMatrixEdgesCarryTheCapabilityTheyWereDeclaredUnder(t *testing.T) {
	catalog := strings.Join([]string{
		"capability code.search 1.0.0",
		"  summary   Find code by meaning",
		"  implementations",
		"    codex.search (provider codex)",
		"      constraints  languages=- index=false vcs=false scale=-..-",
		"      health       unknown",
		"    ripgrep (provider ripgrep)",
		"      health       unknown",
		"",
		"capability code.context 1.0.0",
		"  implementations",
		"    claude.search (provider claude)",
		"      health       unknown",
		"",
	}, "\n")

	command := exec.Command("awk", "-f", "provider-matrix-edges.awk")
	command.Stdin = strings.NewReader(catalog)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	edges := strings.Fields(string(output))
	sort.Strings(edges)

	want := []string{
		"code.context|claude.search",
		"code.search|codex.search",
		"code.search|ripgrep",
	}
	if strings.Join(edges, " ") != strings.Join(want, " ") {
		t.Fatalf("edges = %v, want %v", edges, want)
	}

	// The regression stated as its own assertion, because it is the whole
	// point: claude.search is in this catalog, and it is not on code.search.
	for _, edge := range edges {
		if edge == "code.search|claude.search" {
			t.Fatal("an implementation was credited to a capability it is not declared under")
		}
	}
}

// environWithout is the process environment minus one name, so a test can
// assert what a script does when an operator has not set it.
func environWithout(name string) []string {
	prefix := name + "="
	kept := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, prefix) {
			kept = append(kept, entry)
		}
	}
	return kept
}

func asExitError(err error, target **exec.ExitError) bool {
	exit, ok := err.(*exec.ExitError)
	if ok {
		*target = exit
	}
	return ok
}

// repository is a throwaway git checkout the pre-commit hook can be pointed at.
// The hook reads the index, so nothing short of a real one would exercise it.
type repository struct {
	dir string
}

func newRepository(t *testing.T) *repository {
	t.Helper()
	created := &repository{dir: t.TempDir()}
	created.git(t, "init", "-q")
	return created
}

// withoutGitSteering is os.Environ() with every variable that decides WHICH
// repository git operates on taken out.
//
// Removed rather than set empty: `GIT_DIR=` is a GIT_DIR holding the empty
// string, and git answers "fatal: The empty string is not a valid path".
func withoutGitSteering() []string {
	steering := []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=",
		"GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES="}
	out := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		if slices.ContainsFunc(steering, func(prefix string) bool {
			return strings.HasPrefix(entry, prefix)
		}) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func (r *repository) git(t *testing.T, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = r.dir
	// An identity on the command line rather than in a config file: the test
	// must not depend on, or write to, the developer's global git settings.
	//
	// The cleared variables are the other half of that sentence and were the
	// half missing. command.Dir says where to RUN; GIT_DIR says which
	// repository git is IN, and it wins. A git hook exports it, so a suite run
	// from lefthook's pre-push inherits one pointing at the real checkout and
	// every `git init` here lands there instead. Measured the hard way -- see
	// gitEnv in internal/adapter/kivgraph/impact_index_test.go for what it
	// cost.
	command.Env = append(withoutGitSteering(),
		"GIT_AUTHOR_NAME=atenea", "GIT_AUTHOR_EMAIL=atenea@example.invalid",
		"GIT_COMMITTER_NAME=atenea", "GIT_COMMITTER_EMAIL=atenea@example.invalid",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func (r *repository) write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func (r *repository) hook(t *testing.T) (string, error) {
	t.Helper()
	script, err := filepath.Abs("gofmt-staged.sh")
	if err != nil {
		t.Fatalf("resolve hook: %v", err)
	}
	command := exec.Command("bash", script)
	command.Dir = r.dir
	// The script shells out to git itself, so it needs the same scrubbing as
	// the calls above. Without it this test passed or failed depending on
	// whether whoever ran the suite happened to have GIT_DIR set -- which, run
	// from the pre-push hook, they always do.
	command.Env = withoutGitSteering()
	output, err := command.CombinedOutput()
	return string(output), err
}
