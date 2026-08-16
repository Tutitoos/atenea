package workflow_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
)

// What a run was created for is on its record because funding is keyed on it.
// These tests are about the one thing nobody was checking: that the repository
// the record names is the repository the steps will actually run in.
//
// The measurement behind them, 2026-08-16: a 23-step plan created with
// `--repository taxiprime-backend` and launched WITHOUT the flag, from a shell
// sitting in Atenea's own checkout. Every reader was handed no root, inherited
// the launching process's working directory, and went looking for a Fastify
// surface inside a Go orchestrator. Eight said the files did not exist, fifteen
// died searching, $5.88 of a $5.22 grant went, and zero routes were inventoried.

// launchable is a run created against one repository, on a database a second
// engine can be pointed at -- which is the shape of the defect: `create` and
// `launch` are two processes, and only the first one was told the repository.
func launchable(t *testing.T, dir, repository string) (*harness, string) {
	t.Helper()
	h := newHarnessWith(t, workflow.Options{Lanes: noCeiling(), Repository: repository}, dir,
		declared("worker", answers(t, dir, "worker"), config.PoolAgent))
	run, _, err := h.engine.Create(t.Context(), graphOf(step("a", "worker", nil)))
	if err != nil {
		t.Fatalf("Create against %q: %v", repository, err)
	}
	return h, run.ID
}

// serving builds the engine that launches: same database, its own idea of which
// repository it serves, and optionally the tree that id stands for.
func serving(t *testing.T, dir, repository string, root ...string) *harness {
	t.Helper()
	opts := workflow.Options{Lanes: noCeiling(), Repository: repository}
	if len(root) == 1 {
		opts.RepositoryRoot = root[0]
	}
	return newHarnessWith(t, opts, dir,
		declared("worker", answers(t, dir, "worker"), config.PoolAgent))
}

// Tonight's exact run: created for taxiprime-backend, launched with no flag, and
// what the launch then resolved was REAL -- the settings declare `current` at
// Atenea's own path, so a valid repository was served and it was the wrong one.
// The id alone names no tree, which is why the root has to be in the message.
func TestALaunchServingAValidButWrongRepositoryNamesTheTree(t *testing.T) {
	dir := t.TempDir()
	_, id := launchable(t, dir, "taxiprime-backend")

	second := serving(t, dir, "current", "/home/tutitoos/Desktop/atenea")
	_, err := second.engine.Launch(t.Context(), id)
	if err == nil {
		t.Fatal("a run created for taxiprime-backend launched against current")
	}
	message := err.Error()

	// Both sides, because either alone sends the reader somewhere useless.
	if !strings.Contains(message, `"taxiprime-backend"`) {
		t.Errorf("the refusal does not name what the run recorded:\n%s", message)
	}
	if !strings.Contains(message, `"current"`) {
		t.Errorf("the refusal does not name the id that would be served:\n%s", message)
	}
	// The id is true and useless on its own. The tree is the fact that decides
	// where twenty-three readers actually read.
	if !strings.Contains(message, "/home/tutitoos/Desktop/atenea") {
		t.Errorf("the refusal names an id but not the tree it stands for:\n%s", message)
	}
	// The fix, in the message, because this is a mistake somebody repairs by
	// typing the command again.
	if !strings.Contains(message, "--repository taxiprime-backend") {
		t.Errorf("the refusal does not say how to run it correctly:\n%s", message)
	}
}

// The other half: nothing served at all. Not tonight's case, and it must not be
// reported as if a tree had been named.
func TestALaunchServingNoRepositorySaysSoRatherThanNamingNothing(t *testing.T) {
	dir := t.TempDir()
	_, id := launchable(t, dir, "taxiprime-backend")

	_, err := serving(t, dir, "").engine.Launch(t.Context(), id)
	if err == nil {
		t.Fatal("a run created for taxiprime-backend launched with no repository served")
	}
	if !strings.Contains(err.Error(), "no repository at all") {
		t.Errorf("the refusal does not say that nothing was served:\n%s", err)
	}
	if strings.Contains(err.Error(), `""`) {
		t.Errorf("the refusal prints an empty id as if it were one:\n%s", err)
	}
}

func TestALaunchServingAnotherRepositoryIsRefusedAndNamesBoth(t *testing.T) {
	dir := t.TempDir()
	_, id := launchable(t, dir, "taxiprime-backend")

	second := serving(t, dir, "atenea")
	_, err := second.engine.Launch(t.Context(), id)
	if err == nil {
		t.Fatal("a run created for one repository launched against another")
	}
	message := err.Error()
	for _, want := range []string{`"taxiprime-backend"`, `"atenea"`} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal does not name %s:\n%s", want, message)
		}
	}
}

// The refusal has to cost nothing. Reaching it through the gate would commit
// the grant and leave the retry reading "launched already" -- the mistake would
// be unrepairable by the one action that repairs it.
func TestARefusedRepositoryLeavesTheRunLaunchable(t *testing.T) {
	dir := t.TempDir()
	_, id := launchable(t, dir, "taxiprime-backend")

	if _, err := serving(t, dir, "").engine.Launch(t.Context(), id); err == nil {
		t.Fatal("the mismatched launch was accepted")
	}

	// The gate is still waiting: nothing was answered, nothing spawned.
	right := serving(t, dir, "taxiprime-backend")
	gate, open, err := right.state.OpenGate(t.Context(), id)
	if err != nil {
		t.Fatalf("OpenGate: %v", err)
	}
	if !open {
		t.Fatal("the refused launch consumed the gate; the run can no longer be launched")
	}
	if gate.Kind != workflow.KindLaunch {
		t.Errorf("gate is %s, want a launch still waiting", gate.Kind)
	}

	// And the same run launches once the repository agrees.
	run, err := right.engine.Launch(t.Context(), id)
	if err != nil {
		t.Fatalf("relaunch with the right repository: %v", err)
	}
	if got := statuses(t, run)["a"]; got != "ok" {
		t.Errorf("step a is %q after the corrected launch, want ok", got)
	}
}

// A row that recorded no repository cannot say what it was created for, so it
// is not refused. Those rows predate the column, and refusing them would break
// resuming work nobody can re-create.
func TestARunThatRecordedNoRepositoryLaunchesAnywhere(t *testing.T) {
	dir := t.TempDir()
	_, id := launchable(t, dir, "")

	run, err := serving(t, dir, "taxiprime-backend").engine.Launch(t.Context(), id)
	if err != nil {
		t.Fatalf("a run with no recorded repository was refused: %v", err)
	}
	if got := statuses(t, run)["a"]; got != "ok" {
		t.Errorf("step a is %q, want ok", got)
	}
}

// Resume and run reach takeOver rather than Launch, and the same fact has to
// stop them: a cut run picked up in the wrong tree would finish it there.
//
// The run here is created and not launched, so it is still open. A closed run
// is refused for being closed, which takeOver checks first and rightly so.
func TestResumingInTheWrongRepositoryIsRefused(t *testing.T) {
	dir := t.TempDir()
	_, id := launchable(t, dir, "taxiprime-backend")

	_, err := serving(t, dir, "atenea").engine.Resume(t.Context(), id, nil)
	if err == nil {
		t.Fatal("a run created for taxiprime-backend resumed against atenea")
	}
	message := err.Error()
	for _, want := range []string{`"taxiprime-backend"`, `"atenea"`} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal does not name %s:\n%s", want, message)
		}
	}
}
