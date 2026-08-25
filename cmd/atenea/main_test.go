package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/adapter/omp"
	"github.com/Tutitoos/atenea/internal/backup"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/core"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/internal/testroot"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// TestMain puts a floor under the whole package: no test may inherit the state
// directory, settings directory or config path of the machine running the
// suite.
//
// Pinning per test is one line and every test that forgets it writes real
// failures into a real measurement base -- which is now a health input, so a
// suite run could talk a developer's funnel out of a provider that works.
// That already happened once. A test that wants its own state still calls
// t.Setenv and wins; this only decides where the ones that say nothing land.
func TestMain(m *testing.M) {
	// Pinned before the helper branch, and deliberately: the state root below
	// is carved out of the temporary root, so the socket the helper binds is
	// only short enough to bind if this ran first. The helper inherits the
	// result rather than repeating it.
	if _, err := testroot.Pin(); err != nil {
		panic(err)
	}
	// The service helper is not a test. It is this binary re-executed as a
	// real process so that its environment can differ from its caller's,
	// which is the only way to prove whose environment a verdict came from.
	// Overwriting the state root here would put its socket somewhere the
	// parent does not look, and the parent would report "no service" about a
	// service that is running -- the exact confusion under test.
	if os.Getenv(serviceHelperEnv) == "1" {
		os.Exit(m.Run())
	}
	root, err := os.MkdirTemp("", "atenea-cli-suite")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	os.Setenv("ATENEA_CONFIG", "")
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}

const settings = `
contract = "3.0.0"

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

// settingsFile writes the fixture catalog somewhere disposable, with its
// measurement base pinned to the same throwaway directory.
//
// The base matters as much as the catalog does. Without pinning it these tests
// read and write the base of whoever is running them: the fixture searches
// /srv/api, which exists nowhere, so every run files another failure, and once
// enough pile up the health rule stops choosing ripgrep on a developer's
// machine and nowhere else. Found exactly that way.
//
// Only the base is redirected, not the whole state root: tests that care about
// the crash notebook set that up themselves, and a helper that quietly moved
// it under them would undo their arrangements.
func settingsFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atenea.toml")
	body := settings + "\n[metrics]\npath = \"" + filepath.Join(dir, "base.duckdb") + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func cli(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(args, &out)
	return out.String(), err
}

func TestVersionPrintsBothVersions(t *testing.T) {
	out, err := cli(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "contract "+contract.Current.String()) {
		t.Fatalf("out = %q", out)
	}
}

func TestNoArgumentsPrintsUsage(t *testing.T) {
	out, err := cli(t)
	if err != nil {
		t.Fatalf("no args: %v", err)
	}
	if !strings.Contains(out, "Usage:") {
		t.Fatalf("out = %q", out)
	}
}

// Every subcommand answers --help/-h the same way run's own dispatch does,
// which is the whole point of routing it there instead of leaving each
// command to notice the flag on its own. None of these need a settings file:
// help is answered before load ever runs.
func TestEverySubcommandHasHelp(t *testing.T) {
	for command, want := range commandHelp {
		t.Run(command, func(t *testing.T) {
			out, err := cli(t, command, "--help")
			if err != nil {
				t.Fatalf("%s --help: %v", command, err)
			}
			if out != want {
				t.Errorf("%s --help =\n%s\nwant\n%s", command, out, want)
			}
		})
	}
}

// Several subcommands read a positional argument before their own flags do
// -- "-h" landing there would otherwise be swallowed as the capability id or
// commission text rather than recognized as a request for help.
func TestHelpWinsEvenWhereAPositionalArgumentComesFirst(t *testing.T) {
	for _, command := range []string{"ask", "select", "task", "resume", "agent"} {
		t.Run(command, func(t *testing.T) {
			out, err := cli(t, command, "-h")
			if err != nil {
				t.Fatalf("%s -h: %v", command, err)
			}
			if !strings.HasPrefix(out, "Usage: atenea "+command) {
				t.Errorf("%s -h out = %q, want its own usage", command, out)
			}
		})
	}
}

// Help wins wherever it lands in the argument list, not only as the first
// token: a run that would have spent something must not start just because
// --help came after the flags that would have paid for it.
func TestHelpWinsEvenTrailingAfterRealFlags(t *testing.T) {
	out, err := cli(t, "ask", "code.search", "--repo", "current", "--set", "query=TODO", "-h")
	if err != nil {
		t.Fatalf("ask ... -h: %v", err)
	}
	if !strings.HasPrefix(out, "Usage: atenea ask") {
		t.Errorf("out = %q, want ask usage", out)
	}
}

// An unknown command asking for help is still an unknown command: the help
// map only intercepts names dispatch actually recognizes.
func TestHelpOnAnUnknownCommandStillErrors(t *testing.T) {
	_, err := cli(t, "bogus", "--help")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := exitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (err %v)", got, err)
	}
}

func TestStatusShowsEveryProviderWithItsLight(t *testing.T) {
	out, err := cli(t, "--config", settingsFile(t), "status")
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
	status, err := cli(t, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, status)
	}
	if !strings.Contains(status, "runners    omp") {
		t.Fatalf("the far side is not the omp adapter:\n%s", status)
	}

	// And it is that far side that answers a real commission.
	out, err := cli(t, "task", "TODO", "--trace")
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

	out, err := cli(t, "task", "TODO", "--trace")
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

	// Providers without an attached runner are dropped explicitly, and the
	// trace says why rather than leaving the choice unexplained.
	if !strings.Contains(out, "dropped in every step") {
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

	out, err := cli(t, "task", "hunter2", "--trace")
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

	out, err := cli(t, "task", "TODO")
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
	_, err := cli(t, "task", "TODO", "--repo", "ghost")
	if got := contract.KindOf(err); got != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found", got)
	}
}

func TestTaskWithoutACommissionIsRefused(t *testing.T) {
	freshInstall(t)
	_, err := cli(t, "task")
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
}

// A one-off order beats the standing grant, and a typo in it must not quietly
// mean "spend nothing": that would read as an outage the next time a paid
// provider refused, and the operator would go looking for a broken tool.
func TestANonsenseBudgetOnTheCommandLineIsRefused(t *testing.T) {
	freshInstall(t)
	for _, arg := range []string{"-1", "-0.01"} {
		if _, err := cli(t, "task", "TODO", "--budget", arg); contract.KindOf(err) != contract.FailureInvalidInput {
			t.Errorf("--budget %s was filed as %v", arg, contract.KindOf(err))
		}
		if _, err := cli(t, "ask", "code.search", "--repo", "current",
			"--set", "query=TODO", "--budget", arg); contract.KindOf(err) != contract.FailureInvalidInput {
			t.Errorf("ask --budget %s was filed as %v", arg, contract.KindOf(err))
		}
	}
}

// The flag has to reach the run, not just parse. A commission the operator
// funded by hand is the one case where the settings file is not the answer.
func TestTheCommandLineBudgetReachesTheRun(t *testing.T) {
	freshInstall(t)
	if _, err := cli(t, "task", "TODO", "--budget", "5"); err != nil {
		t.Fatalf("a funded commission was refused: %v", err)
	}
}

// An unknown effect name must be refused at the flag, not silently dropped
// or forwarded to a step that then fails for a reason nobody can trace back
// to the typo.
func TestAnUnknownAllowValueOnTheCommandLineIsRefused(t *testing.T) {
	freshInstall(t)
	if _, err := cli(t, "task", "TODO", "--allow", "ghost"); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Errorf("--allow ghost was filed as %v", contract.KindOf(err))
	}
	if _, err := cli(t, "ask", "code.search", "--repo", "current",
		"--set", "query=TODO", "--allow", "ghost"); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Errorf("ask --allow ghost was filed as %v", contract.KindOf(err))
	}
}

// The flag has to reach the run, not just parse: a name this build
// recognizes must not be refused for a reason unrelated to permission.
func TestTheCommandLineAllowFlagReachesTheRun(t *testing.T) {
	freshInstall(t)
	if _, err := cli(t, "task", "TODO", "--allow", "write"); err != nil {
		t.Fatalf("a commission granted write was refused: %v", err)
	}
}

// The trace is the deliverable, not a debug extra: a decision nobody can
// explain is a decision nobody can trust.
func TestSelectPrintsTheChoiceAndTheFunnel(t *testing.T) {
	out, err := cli(t, "--config", settingsFile(t), "select", "code.search", "--repo", "api")
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
	out, err := cli(t, "--config", settingsFile(t), "select", "code.search")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !strings.Contains(out, "repository  api") {
		t.Fatalf("out = %q", out)
	}
}

func TestCatalogShowsTheFourBlocks(t *testing.T) {
	out, err := cli(t, "--config", settingsFile(t), "catalog")
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
	if _, err := cli(t, "--config", path, "config", "init"); err != nil {
		t.Fatalf("config init: %v", err)
	}
	if _, err := cli(t, "--config", path, "config", "init"); err == nil {
		t.Fatal("a second init without --force should fail")
	}
	if _, err := cli(t, "--config", path, "config", "init", "--force"); err != nil {
		t.Fatalf("config init --force: %v", err)
	}
	out, err := cli(t, "--config", path, "status")
	if err != nil {
		t.Fatalf("status on the written file: %v", err)
	}
	if !strings.Contains(out, path) {
		t.Fatalf("status did not read the written file:\n%s", out)
	}
}

func TestConfigPathReportsTheResolvedLocation(t *testing.T) {
	out, err := cli(t, "--config", "/tmp/explicit.toml", "config", "path")
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
		{"unknown capability", []string{"select", "graph.impact"}, 3},
		{"unknown config subcommand", []string{"config", "wat"}, 2},
	}
	path := settingsFile(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cli(t, append([]string{"--config", path}, tc.args...)...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := exitCode(err); got != tc.code {
				t.Fatalf("exit code = %d, want %d (err %v)", got, tc.code, err)
			}
		})
	}
}

// A missing required field is caught before anything is spent: the funnel
// never runs, and the exit code says so -- 2, the same bin as any other
// malformed invocation, not 6 (a commission that was dispatched and failed).
func TestAMissingRequiredFieldNeverDispatches(t *testing.T) {
	out, err := cli(t, "--config", settingsFile(t), "ask", "code.search", "--repo", "api")
	if err == nil {
		t.Fatalf("expected an error, out:\n%s", out)
	}
	if got := exitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (err %v)", got, err)
	}
	if !strings.Contains(err.Error(), `"query" is required`) {
		t.Errorf("err = %v, want it to name the missing field", err)
	}
	// Never dispatched: no run receipt, because there was no run.
	if out != "" {
		t.Errorf("a pre-flight rejection printed a receipt:\n%s", out)
	}
}

// The exact repro from the friction report: type "code.serach" for
// "code.search" and the CLI names the near miss instead of a flat refusal.
func TestATypodCapabilitySuggestsTheRealOne(t *testing.T) {
	_, err := cli(t, "--config", settingsFile(t), "ask", "code.serach", "--repo", "api", "--set", "query=TODO")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := exitCode(err); got != 3 {
		t.Fatalf("exit code = %d, want 3 (err %v)", got, err)
	}
	if !strings.Contains(err.Error(), "did you mean code.search?") {
		t.Errorf("err = %v, want a suggestion", err)
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

	out, err := cli(t, "--config", path, "task", "TODO")
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
	if err := os.WriteFile(path, []byte("contract = \"2.0.0\"\nnonsense = true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := cli(t, "--config", path, "status")
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}

// The background section is Atenea's own house. Every line has to be true in
// whichever process prints it, which is why the rhythms come from the settings
// and the copies from the disk, and why nothing here reports a tally this
// one-second process keeps in its own memory.
func TestTheScreenShowsTheBackgroundRhythmsAndTheCopies(t *testing.T) {
	freshInstall(t)
	out, err := cli(t, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{
		"background",
		"metrics.flush 30s",
		"metrics.compact 1h",
		"backup 6h",
		"copies",
		"of 5 kept in",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the screen is missing %q:\n%s", want, out)
		}
	}
	// "not yet" is what a per-process lane tally would print here, and it
	// would be a lie about a machine whose service has been copying all day.
	if strings.Contains(out, "not yet") {
		t.Errorf("the screen reports this process's own lane state:\n%s", out)
	}
}

// Whole hours print as hours. The obvious implementation trims the text and
// turns a thirty-second rhythm into "3", which is the kind of wrong that reads
// as right.
func TestARoundRhythmDropsItsZeroTailWithoutLosingDigits(t *testing.T) {
	for every, want := range map[time.Duration]string{
		6 * time.Hour:           "6h",
		time.Hour:               "1h",
		90 * time.Minute:        "90m",
		30 * time.Second:        "30s",
		1500 * time.Millisecond: "1.5s",
	} {
		if got := rhythm(every); got != want {
			t.Errorf("rhythm(%v) = %q, want %q", every, got, want)
		}
	}
}

// Copying switched off says so. A screen that simply omitted the line would be
// indistinguishable from one where copying is on and working.
func TestTheScreenSaysWhenCopyingIsOff(t *testing.T) {
	freshInstall(t)
	// The shipped file, written out and then amended: a hand-written catalog
	// here would drift from what actually ships.
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if _, err := cli(t, "config", "init"); err != nil {
		t.Fatalf("config init: %v", err)
	}
	path := filepath.Join(cfg, "atenea", "atenea.toml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The shipped block already exists, so this switches the word rather than
	// writing a second [backup] the parser would refuse.
	head, tail, found := strings.Cut(string(body), "[backup]")
	if !found {
		t.Fatal("the shipped settings no longer have a [backup] block")
	}
	off := head + "[backup]" + strings.Replace(tail, "enabled = true", "enabled = false", 1)
	if err := os.WriteFile(path, []byte(off), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := cli(t, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "copies       off") {
		t.Errorf("the screen does not say copying is off:\n%s", out)
	}
}

func backupCLISetup(t *testing.T) (string, string, backup.Snapshot) {
	t.Helper()
	root := t.TempDir()
	stateHome := filepath.Join(root, "state-home")
	t.Setenv("XDG_STATE_HOME", stateHome)
	state := filepath.Join(stateHome, "atenea")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(state, "marker.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatalf("marker: %v", err)
	}
	backupDir := filepath.Join(root, "backups")
	configPath := filepath.Join(root, "settings.toml")
	body := settings + "\n[metrics]\npath = \"" + filepath.Join(root, "metrics.duckdb") + "\"\n" +
		"[backup]\ndir = \"" + backupDir + "\"\nkeep = 5\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("settings: %v", err)
	}
	store, err := backup.New(backup.Options{Source: state, Dir: backupDir, Keep: 5})
	if err != nil {
		t.Fatalf("backup store: %v", err)
	}
	snapshot, err := store.Snapshot(context.Background(), time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Name == "" {
		t.Fatal("snapshot has no name")
	}
	return configPath, state, snapshot
}

func TestBackupCLIControlsSnapshotLifecycle(t *testing.T) {
	configPath, state, snapshot := backupCLISetup(t)

	out, err := cli(t, "--config", configPath, "backup", "list")
	if err != nil {
		t.Fatalf("backup list: %v", err)
	}
	if !strings.Contains(out, snapshot.Name) || !strings.Contains(out, snapshot.Path) {
		t.Fatalf("backup list = %q", out)
	}

	restored := filepath.Join(t.TempDir(), "restored")
	out, err = cli(t, "--config", configPath, "backup", "restore", snapshot.Name, restored)
	if err != nil {
		t.Fatalf("backup restore: %v", err)
	}
	if !strings.Contains(out, "restored "+snapshot.Name) {
		t.Fatalf("backup restore output = %q", out)
	}
	got, err := os.ReadFile(filepath.Join(restored, "marker.txt"))
	if err != nil || string(got) != "before\n" {
		t.Fatalf("restored marker = %q, err=%v", got, err)
	}

	if err := os.WriteFile(filepath.Join(state, "marker.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatalf("change marker: %v", err)
	}
	out, err = cli(t, "--config", configPath, "backup", "restore", snapshot.Name, state, "--replace")
	if err != nil {
		t.Fatalf("backup replace: %v", err)
	}
	if !strings.Contains(out, ".atenea-previous") {
		t.Fatalf("backup replace output = %q", out)
	}
	got, err = os.ReadFile(filepath.Join(state, "marker.txt"))
	if err != nil || string(got) != "before\n" {
		t.Fatalf("replaced marker = %q, err=%v", got, err)
	}

	out, err = cli(t, "--config", configPath, "backup", "promote", state)
	if err != nil {
		t.Fatalf("backup promote: %v", err)
	}
	if !strings.Contains(out, "promoted previous state") {
		t.Fatalf("backup promote output = %q", out)
	}
	got, err = os.ReadFile(filepath.Join(state, "marker.txt"))
	if err != nil || string(got) != "after\n" {
		t.Fatalf("promoted marker = %q, err=%v", got, err)
	}

	if err := os.WriteFile(filepath.Join(state, "marker.txt"), []byte("latest\n"), 0o600); err != nil {
		t.Fatalf("change marker again: %v", err)
	}
	if _, err = cli(t, "--config", configPath, "backup", "restore", snapshot.Name, state, "--replace"); err != nil {
		t.Fatalf("backup replace before discard: %v", err)
	}
	out, err = cli(t, "--config", configPath, "backup", "discard", state, "--confirm")
	if err != nil {
		t.Fatalf("backup discard: %v", err)
	}
	if !strings.Contains(out, "discarded previous state") {
		t.Fatalf("backup discard output = %q", out)
	}
	if _, err := os.Stat(state + ".atenea-previous"); !os.IsNotExist(err) {
		t.Fatalf("previous state remains, stat err=%v", err)
	}
}

func TestBackupCLIReturnsExplicitInputErrors(t *testing.T) {
	configPath, _, _ := backupCLISetup(t)
	for _, args := range [][]string{
		{"backup"},
		{"backup", "list", "extra"},
		{"backup", "promote"},
		{"backup", "discard", "target"},
		{"backup", "unknown"},
	} {
		_, err := cli(t, append([]string{"--config", configPath}, args...)...)
		if contract.KindOf(err) != contract.FailureInvalidInput {
			t.Errorf("args %v: kind=%v, want invalid_input", args, contract.KindOf(err))
		}
	}
}

func TestConfigAndIntentReportsExposeTheirSources(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("repo dir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatalf("git marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{
  "mcpServers": {
    "ripgrep": {"type": "stdio", "command": "not-executed"},
    "outside": {"type": "http", "url": "https://not-executed.invalid/mcp"}
  }
}`), 0o600); err != nil {
		t.Fatalf("mcp config: %v", err)
	}
	skill := filepath.Join(root, ".claude", "skills", "release", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatalf("skill dir: %v", err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: release\ndescription: ship safely\n---\n"), 0o600); err != nil {
		t.Fatalf("skill: %v", err)
	}
	configPath := settingsFile(t)
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("cwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(old) }()

	out, err := cli(t, "--config", configPath, "config", "path")
	if err != nil || !strings.Contains(out, configPath) {
		t.Fatalf("config path = %q, err=%v", out, err)
	}
	out, err = cli(t, "--config", configPath, "config", "show")
	if err != nil {
		t.Fatalf("config show: %v", err)
	}
	for _, want := range []string{"global", "overlay  none", "security", "client config", "intent"} {
		if !strings.Contains(out, want) {
			t.Errorf("config show missing %q:\n%s", want, out)
		}
	}
	overlay := filepath.Join(root, ".atenea", "config.toml")
	if err := os.MkdirAll(filepath.Dir(overlay), 0o700); err != nil {
		t.Fatalf("overlay dir: %v", err)
	}
	if err := os.WriteFile(overlay, []byte(`[[repository]]
languages = ["go"]
scale = "small"
vcs = "present"
indexed_by = ["ripgrep"]

[[selector.rule]]
capability = "code.search"
prefer = "ripgrep"

[security]
sensitive = ["*.local"]
`), 0o600); err != nil {
		t.Fatalf("overlay: %v", err)
	}
	out, err = cli(t, "--config", configPath, "config", "show")
	if err != nil {
		t.Fatalf("config show with overlay: %v", err)
	}
	for _, want := range []string{"overlay  ", "adds repository", "selector rules", "local", "+1 local sensitive   12 pattern(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay config show missing %q:\n%s", want, out)
		}
	}

	out, err = cli(t, "--config", configPath, "intent")
	if err != nil {
		t.Fatalf("intent: %v", err)
	}
	for _, want := range []string{"asked for", "ripgrep", "outside", "also carried: 1 skill(s)", "unmatched"} {
		if !strings.Contains(out, want) {
			t.Errorf("intent missing %q:\n%s", want, out)
		}
	}
	out, err = cli(t, "--config", configPath, "intent", "--json")
	if err != nil {
		t.Fatalf("intent json: %v", err)
	}
	var report struct {
		Items     []map[string]any `json:"items"`
		Unmatched int              `json:"unmatched"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("intent json %q: %v", out, err)
	}
	if len(report.Items) != 3 || report.Unmatched != 1 {
		t.Fatalf("intent json report = %+v", report)
	}
}

func TestConfigInspectionRejectsUnknownActions(t *testing.T) {
	path := settingsFile(t)
	for _, args := range [][]string{{"config"}, {"config", "nope"}, {"intent", "--bad"}, {"resume"}, {"resume", "--list", "extra"}} {
		_, err := cli(t, append([]string{"--config", path}, args...)...)
		if contract.KindOf(err) != contract.FailureInvalidInput {
			t.Errorf("args %v: kind=%v, want invalid_input", args, contract.KindOf(err))
		}
	}
}

func TestResumeListAndDisplayHelpers(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state-home"))
	if err := os.MkdirAll(filepath.Join(os.Getenv("XDG_STATE_HOME"), "atenea", "runs"), 0o700); err != nil {
		t.Fatalf("runs directory: %v", err)
	}
	out, err := cli(t, "--config", settingsFile(t), "resume", "--list")
	if err != nil || !strings.Contains(out, "nothing to resume") {
		t.Fatalf("resume list = %q, err=%v", out, err)
	}
	if got := removedOrAbsent(true, "/tmp/file"); got != "removed /tmp/file" {
		t.Fatalf("removedOrAbsent true = %q", got)
	}
	if got := removedOrAbsent(false, "/tmp/file"); got != "was not there: /tmp/file" {
		t.Fatalf("removedOrAbsent false = %q", got)
	}
	if shortDigest("") != "-" || shortDigest("0123456789abcdef") != "0123456789ab" {
		t.Fatal("shortDigest did not use the display forms")
	}
	if orNone(nil) != "(none)" || orNone([]string{"go"}) != "go" ||
		orUnset("") != "(unspecified)" || orAny("") != "every repository" {
		t.Fatal("optional display helpers returned the wrong empty form")
	}
	if yesNo(true) != "yes" || yesNo(false) != "no" || ceiling(0) != "unlimited" || ceiling(2) != "2 step(s) at a time" {
		t.Fatal("boolean and ceiling helpers returned the wrong form")
	}
	if clip(strings.Repeat("x", 161)) != strings.Repeat("x", 160)+"..." || clip("short") != "short" {
		t.Fatal("clip did not preserve its limit")
	}
}

func TestCommandArgumentGuardsDoNotStartExternalWork(t *testing.T) {
	var out bytes.Buffer
	guards := []struct {
		name string
		call func() error
	}{
		{name: "dashboard missing", call: func() error { return cmdDashboard("", nil, &out) }},
		{name: "dashboard flag first", call: func() error { return cmdDashboard("", []string{"--check"}, &out) }},
		{name: "dashboard hosts flag", call: func() error { return cmdDashboardHosts(config.Config{}, []string{"--bad"}, &out) }},
		{name: "incidents unknown", call: func() error { return cmdIncidents("", []string{"unknown"}, &out) }},
		{name: "metrics bad flag", call: func() error { return cmdMetrics("", []string{"--bad"}, &out) }},
		{name: "backup missing", call: func() error { return cmdBackup("", nil, &out) }},
		{name: "detect bad flag", call: func() error { return cmdDetect("", []string{"--bad"}, &out) }},
		{name: "select missing", call: func() error { return cmdSelect("", nil, &out) }},
		{name: "task missing", call: func() error { return cmdTask("", nil, &out) }},
		{name: "ask missing", call: func() error { return cmdAsk("", nil, &out) }},
		{name: "resume extra", call: func() error { return cmdResume("", []string{"run", "extra"}, &out) }},
		{name: "resume bad flag", call: func() error { return cmdResume("", []string{"run", "--bad"}, &out) }},
		{name: "service missing", call: func() error { return cmdService("", nil, &out) }},
		{name: "service unknown", call: func() error { return cmdService("", []string{"unknown"}, &out) }},
		{name: "statusline missing", call: func() error { return cmdStatusLine(nil, &out) }},
		{name: "statusline unknown", call: func() error { return cmdStatusLine([]string{"unknown"}, &out) }},
		{name: "statusline too many", call: func() error { return cmdStatusLine([]string{"status", "atenea", "extra"}, &out) }},
		{name: "wrap missing", call: func() error { return cmdWrap("", nil, &out) }},
		{name: "wrap unknown", call: func() error { return cmdWrap("", []string{"unknown"}, &out) }},
	}
	for _, guard := range guards {
		t.Run(guard.name, func(t *testing.T) {
			if err := guard.call(); err == nil {
				t.Fatalf("%s unexpectedly succeeded", guard.name)
			}
		})
	}
	if err := cmdStatusLine([]string{"widgets"}, &out); err != nil {
		t.Fatalf("statusline widgets: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("statusline widgets printed nothing")
	}
}

func TestReadOnlyStatusAndIndexRenderers(t *testing.T) {
	var out bytes.Buffer
	printIndexReports(&out, nil)
	if !strings.Contains(out.String(), "no attached provider") {
		t.Fatalf("empty index report = %q", out.String())
	}
	out.Reset()
	printIndexReports(&out, []core.IndexReport{
		{Repository: "api", Provider: "serena", Err: "probe failed"},
		{Repository: "web", Provider: "serena", Ready: true},
		{Repository: "docs", Provider: "serena", Hint: "index missing"},
	})
	for _, want := range []string{"could not tell: probe failed", "ready", "not ready: index missing"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("index report missing %q: %s", want, out.String())
		}
	}
	out.Reset()
	if err := cmdService("", []string{"status"}, &out); err != nil {
		t.Fatalf("service status: %v", err)
	}
	if !strings.Contains(out.String(), "unit") {
		t.Fatalf("service status = %q", out.String())
	}
	out.Reset()
	if err := cmdStatusLine([]string{"status"}, &out); err != nil {
		t.Fatalf("statusline status: %v", err)
	}
	if !strings.Contains(out.String(), "widget") {
		t.Fatalf("statusline status = %q", out.String())
	}
}

func TestDecisionAndAnswerRenderersShowTheirCompleteFacts(t *testing.T) {
	var out bytes.Buffer
	decision := selector.Decision{
		Capability: "code.search", Repository: "api",
		Chosen: contract.Implementation{ID: "ripgrep"}, Reason: "health",
		Notices: []string{"preferred provider unavailable"},
		Stages:  []selector.Stage{{Name: "reach", In: []string{"ripgrep", "codex.search"}, Out: []string{"ripgrep"}, Dropped: []selector.Drop{{Implementation: "codex.search", Reason: "not installed", Raw: "raw detail"}}}},
	}
	printDecision(&out, decision, nil)
	for _, want := range []string{"capability  code.search", "chosen      ripgrep", "notice      preferred provider unavailable", "funnel", "dropped codex.search", "raw: raw detail"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("decision output missing %q: %s", want, out.String())
		}
	}
	out.Reset()
	printDecision(&out, decision, contract.Fail(contract.FailureUnavailable, "selection failed"))
	if strings.Contains(out.String(), "chosen") {
		t.Fatalf("failed decision still printed chosen implementation: %s", out.String())
	}
	out.Reset()
	printDecision(&out, selector.Decision{}, nil)
	if out.Len() != 0 {
		t.Fatalf("empty decision output = %q", out.String())
	}

	result := &orchestrator.Result{Steps: []orchestrator.StepResult{{
		Review:  orchestrator.Review{Parent: contract.VerdictOK},
		Outcome: contract.Outcome{Result: map[string]any{"matches": []any{map[string]any{"path": "main.go", "line": 4}}}, Notices: []string{"one result was narrowed"}},
	}}}
	out.Reset()
	printAnswer(&out, result, false)
	for _, want := range []string{"notice   one result was narrowed", "answer", "matches (1)", "path", "main.go"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("answer output missing %q: %s", want, out.String())
		}
	}
	out.Reset()
	printAnswer(&out, result, true)
	if strings.Contains(out.String(), "one result was narrowed") {
		t.Fatalf("trace answer repeated notice: %s", out.String())
	}
	out.Reset()
	printAnswer(&out, &orchestrator.Result{Steps: []orchestrator.StepResult{{Review: orchestrator.Review{Parent: contract.VerdictFailed}}}}, false)
	if out.Len() != 0 {
		t.Fatalf("failed answer output = %q", out.String())
	}
}
