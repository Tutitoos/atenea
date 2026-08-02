package main

import (
	"bytes"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/omp"
	"github.com/Tutitoos/atenea/pkg/contract"
)

const settings = `
contract = "1.0.0"

[[capability]]
id = "code.search"
version = "1.0.0"
summary = "Find literal text in a repository."
effects = ["read"]

  [[capability.input]]
  name = "query"
  type = "string"
  required = true

[[implementation]]
id = "ripgrep"
provider = "ripgrep"
capability = "code.search"

  [implementation.health]
  state = "alive"

[[implementation]]
id = "serena.search"
provider = "serena"
capability = "code.search"

  [implementation.constraints]
  requires_index = true

[[repository]]
id = "api"
path = "/srv/api"
languages = ["go"]
scale = "small"
`

func settingsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func exec(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(args, &out)
	return out.String(), err
}

func TestVersionPrintsBothVersions(t *testing.T) {
	out, err := exec(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "contract "+contract.Current.String()) {
		t.Fatalf("out = %q", out)
	}
}

func TestNoArgumentsPrintsUsage(t *testing.T) {
	out, err := exec(t)
	if err != nil {
		t.Fatalf("no args: %v", err)
	}
	if !strings.Contains(out, "Usage:") {
		t.Fatalf("out = %q", out)
	}
}

func TestStatusShowsEveryProviderWithItsLight(t *testing.T) {
	out, err := exec(t, "--config", settingsFile(t), "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"code.search", "ripgrep", "serena.search", "AMBER", "repositories", "api"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
}

// ---------------------------------------------------------------------------
// End to end, against a real repository on disk
// ---------------------------------------------------------------------------

// realRepo lays down a small tree with the shape that matters: two areas that
// hold the text, one that does not, and a credentials file that must never be
// read no matter who asks.
func realRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"internal/auth/login.go":  "package auth\n\n// TODO: rotate the session key\nfunc Login() {}\n",
		"internal/http/route.go":  "package http\n\n// TODO: reject unknown verbs\nfunc Route() {}\n",
		"cmd/server/main.go":      "package main\n\nfunc main() {}\n",
		"docs/notes.md":           "nothing to do here\n",
		".env":                    "TODO_SECRET=hunter2\n",
		"node_modules/dep/pkg.go": "// TODO: this is vendored and must not count\n",
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// requireOMP skips a test that needs the real client.
//
// These tests boot the shipped defaults, and what ships dispatches to omp, so
// without the binary there is no far side to reach. Skipping is the honest
// answer: the adapter's translation is pinned hermetically by its own unit
// tests, and this file is where the claim is that the two really meet.
func requireOMP(t *testing.T) {
	t.Helper()
	if _, err := osexec.LookPath(omp.DefaultBinary); err != nil {
		t.Skipf("omp is not installed on this machine: %v", err)
	}
}

// freshInstall is the strongest end-to-end claim available: no settings file
// at all, the shipped defaults, and a real repository under the working
// directory that the default `current` entry points at. A hand-copied
// catalog here would drift from what actually ships and quietly stop
// testing it.
func freshInstall(t *testing.T) (repo, runs string) {
	t.Helper()
	requireOMP(t)
	repo = realRepo(t)
	state := t.TempDir()
	t.Chdir(repo)
	t.Setenv("XDG_STATE_HOME", state)
	// Nothing may leak in from the machine running the suite.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ATENEA_CONFIG", "")
	return repo, filepath.Join(state, "atenea", "runs")
}

// The brick-5 claim, stated where it cannot be satisfied by accident: the far
// side of the contract is the omp adapter driving the real client, and the
// skeleton still beats through it.
//
// Everything else in this file would pass just as happily against the
// stand-in, so a quiet revert of the runner would leave the suite green. This
// test is what refuses that.
func TestTheSkeletonBeatsThroughTheRealAdapter(t *testing.T) {
	freshInstall(t)

	// Who is behind the catalog, according to the core itself.
	status, err := exec(t, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, status)
	}
	if !strings.Contains(status, "runners    omp") {
		t.Fatalf("the far side is not the omp adapter:\n%s", status)
	}

	// And it is that far side that answers a real commission.
	out, err := exec(t, "task", "TODO", "--trace")
	if err != nil {
		t.Fatalf("task: %v\n%s", err, out)
	}
	for _, want := range []string{
		"verdict   ok",
		// Two source files hold the text. The vendored copy is gitignored to
		// omp and the credentials file is refused by the adapter, so neither
		// can be among them.
		"matches   2",
		"search-current       work     ripgrep",
		"review   child=ok parent=ok (output matches the capability)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}

	// `verdict ok` is a stronger claim than it looks. The adapter runs every
	// record it built through the capability's declared output schema before
	// handing it back, and that schema requires a column -- which omp never
	// prints. A verdict of ok therefore means the translation really happened:
	// had the column been dropped, this would read `failed invalid_input`.
	// That the column is also the RIGHT one is pinned next to the code, in
	// TestTheColumnIsRecoveredFromTheLineOmpReturned.
}

// The one test that proves the skeleton beats: a commission enters the CLI,
// the orchestrator looks at a real directory, narrows the work to what it
// found, dispatches it, reviews the answers and leaves a paper copy.
func TestTaskRunsAgainstARealRepositoryEndToEnd(t *testing.T) {
	freshInstall(t)

	out, err := exec(t, "task", "TODO", "--trace")
	if err != nil {
		t.Fatalf("task: %v\n%s", err, out)
	}

	// The verdict, the phases and the funnel all reached the screen.
	for _, want := range []string{
		"verdict   ok",
		"explore  1 step(s)",
		"work     1 step(s)",
		"wave 1  explore-current",
		"wave 2  search-current",
		"search-current       work     ripgrep",
		"review   child=ok parent=ok (output matches the capability)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}

	// The two providers that need a warm index are dropped on constraints, and
	// the trace says why rather than leaving the choice unexplained.
	if !strings.Contains(out, "needs an index") {
		t.Errorf("the funnel did not explain its drops:\n%s", out)
	}

	// The look found the one area that holds the text and narrowed the work to
	// it: docs and cmd were not searched again for nothing.
	if !strings.Contains(out, "scope    internal") {
		t.Errorf("the work was not narrowed to what the look found:\n%s", out)
	}

	// Two source files hold the text. The vendored copy and the credentials
	// file are not among them.
	if !strings.Contains(out, "matches   2") {
		t.Errorf("want the two source hits and nothing else:\n%s", out)
	}
}

// A commission is not allowed to read what the user did not offer, and the
// answer has to say so out loud rather than quietly returning less.
func TestTheCommissionNeverReadsASensitiveFile(t *testing.T) {
	freshInstall(t)

	out, err := exec(t, "task", "hunter2", "--trace")
	if err != nil {
		t.Fatalf("task: %v\n%s", err, out)
	}
	if strings.Contains(out, ".env") {
		t.Errorf("a sensitive file reached the answer:\n%s", out)
	}
	if !strings.Contains(out, "matches   0") {
		t.Errorf("the secret was found anyway:\n%s", out)
	}
}

// The paper copy is the whole point of checkpointing: after the process is
// gone, the run is still readable.
func TestTheRunSurvivesOnDisk(t *testing.T) {
	_, runs := freshInstall(t)

	out, err := exec(t, "task", "TODO")
	if err != nil {
		t.Fatalf("task: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(runs)
	if err != nil {
		t.Fatalf("the run directory was never created: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("run files = %d, want exactly the one run", len(entries))
	}

	body, err := os.ReadFile(filepath.Join(runs, entries[0].Name()))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var record struct {
		Task    string `json:"task"`
		Closed  bool   `json:"closed"`
		Verdict string `json:"verdict"`
		Steps   []struct {
			ID             string `json:"id"`
			Implementation string `json:"implementation"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatalf("the paper copy is not readable JSON: %v", err)
	}
	if record.Task != "TODO" || !record.Closed || record.Verdict != "ok" {
		t.Fatalf("record = %+v", record)
	}
	if len(record.Steps) != 2 {
		t.Fatalf("recorded steps = %d, want the look and the search", len(record.Steps))
	}
	for _, step := range record.Steps {
		if step.Implementation != "ripgrep" {
			t.Errorf("%s recorded as run by %q", step.ID, step.Implementation)
		}
	}
}

// Asking for a repository that is not in the settings is a typo, and a typo
// must not look like an empty answer.
func TestTaskAgainstAnUnknownRepositoryFails(t *testing.T) {
	freshInstall(t)
	_, err := exec(t, "task", "TODO", "--repo", "ghost")
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", got)
	}
}

func TestTaskWithoutACommissionIsRefused(t *testing.T) {
	freshInstall(t)
	_, err := exec(t, "task")
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// The trace is the deliverable, not a debug extra: a decision nobody can
// explain is a decision nobody can trust.
func TestSelectPrintsTheChoiceAndTheFunnel(t *testing.T) {
	out, err := exec(t, "--config", settingsFile(t), "select", "code.search", "--repo", "api")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	for _, want := range []string{
		"chosen      ripgrep",
		"2 in -> 1 out: ripgrep",
		"dropped serena.search: needs an index from provider serena",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("select output is missing %q:\n%s", want, out)
		}
	}
}

// With exactly one repository registered, naming it is ceremony.
func TestSelectDefaultsToTheOnlyRepository(t *testing.T) {
	out, err := exec(t, "--config", settingsFile(t), "select", "code.search")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !strings.Contains(out, "repository  api") {
		t.Fatalf("out = %q", out)
	}
}

func TestCatalogShowsTheFourBlocks(t *testing.T) {
	out, err := exec(t, "--config", settingsFile(t), "catalog")
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, want := range []string{"capability code.search 1.0.0", "constraints", "cost", "health"} {
		if !strings.Contains(out, want) {
			t.Errorf("catalog output is missing %q:\n%s", want, out)
		}
	}
}

func TestConfigInitWritesAUsableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if _, err := exec(t, "--config", path, "config", "init"); err != nil {
		t.Fatalf("config init: %v", err)
	}
	if _, err := exec(t, "--config", path, "config", "init"); err == nil {
		t.Fatal("a second init without --force should fail")
	}
	if _, err := exec(t, "--config", path, "config", "init", "--force"); err != nil {
		t.Fatalf("config init --force: %v", err)
	}
	out, err := exec(t, "--config", path, "status")
	if err != nil {
		t.Fatalf("status on the written file: %v", err)
	}
	if !strings.Contains(out, path) {
		t.Fatalf("status did not read the written file:\n%s", out)
	}
}

func TestConfigPathReportsTheResolvedLocation(t *testing.T) {
	out, err := exec(t, "--config", "/tmp/explicit.toml", "config", "path")
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	if strings.TrimSpace(out) != "/tmp/explicit.toml" {
		t.Fatalf("out = %q", out)
	}
}

// A shell script has to be able to tell a broken settings file from a provider
// that is simply down, so the bins map onto distinct exit codes.
func TestErrorsMapOntoDistinctExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"unknown command", []string{"explode"}, 2},
		{"select without a capability", []string{"select"}, 2},
		{"unknown capability", []string{"select", "code.impact"}, 3},
		{"unknown config subcommand", []string{"config", "wat"}, 2},
	}
	path := settingsFile(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := exec(t, append([]string{"--config", path}, tc.args...)...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := exitCode(err); got != tc.code {
				t.Fatalf("exit code = %d, want %d (err %v)", got, tc.code, err)
			}
		})
	}
}

// A commission that was carried out and came back failed is not a broken
// invocation: nothing about the call was wrong, and the report on stdout is
// complete. But a script that cannot tell it from a commission that worked
// would go on to use an answer nobody produced, so the verdict has to reach
// the shell on the one channel a script reads without parsing.
func TestAFailedVerdictLeavesThroughTheExitCode(t *testing.T) {
	// A binary that cannot exist makes every step fail as unavailable, which
	// needs no client installed and no repository on disk: the failure lands
	// before either is reached.
	path := filepath.Join(t.TempDir(), "atenea.toml")
	body := settings + "\n[orchestrator.omp]\nbinary = \"atenea-no-such-client\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := exec(t, "--config", path, "task", "TODO")
	if err == nil {
		t.Fatal("a failed commission left as if it had worked")
	}
	if got := exitCode(err); got != 6 {
		t.Errorf("exit code = %d, want 6 (err %v)", got, err)
	}
	// 1 means a bug and 2..5 are invocation failures. Borrowing any of them
	// would tell a script the wrong thing about what went wrong.
	if got := contract.KindOf(err); got != contract.FailureUnspecified {
		t.Errorf("kind = %v: a verdict is not a failure bin", got)
	}
	// The exit code replaces nothing. Whoever is reading the screen still
	// gets the whole report, which is where the reason actually lives.
	if !strings.Contains(out, "verdict   failed") {
		t.Errorf("the report was swallowed by the failure:\n%s", out)
	}
	if !strings.Contains(out, "run       ") {
		t.Errorf("the run id is missing, so the receipt cannot be found:\n%s", out)
	}
}

func TestBrokenSettingsFailLoudly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(path, []byte("contract = \"1.0.0\"\nnonsense = true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := exec(t, "--config", path, "status")
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}
