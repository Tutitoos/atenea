package notebook_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Tutitoos/atenea/internal/notebook"
)

const killEnv = "ATENEA_NOTEBOOK_KILL_CHILD"

// The durability claim, tested against the worst death there is.
//
// A panic still unwinds; a deferred flush somewhere would still get its
// chance, and a test built on one could pass while hiding a buffer. SIGKILL
// gives the process nothing: no defers, no exit handlers, no runtime cleanup,
// not one more instruction after the syscall. If the entry is on the disk
// afterwards, it was on the disk before Record returned, which is the whole
// reason this file is written the way it is instead of the way metrics is.
func TestAnEntrySurvivesSIGKILLOneInstructionLater(t *testing.T) {
	if os.Getenv(killEnv) == "1" {
		recordThenDie(t)
		t.Fatal("the child outlived its own SIGKILL")
	}

	state := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=TestAnEntrySurvivesSIGKILLOneInstructionLater")
	child.Env = append(os.Environ(), killEnv+"=1", "XDG_STATE_HOME="+state)
	err := child.Run()

	// Killed, not exited: an ordinary non-zero exit would mean the child got
	// to run code after the record, and the test would prove less than it
	// claims.
	var exit *exec.ExitError
	if !asExit(err, &exit) {
		t.Fatalf("the child returned %v, want a signal", err)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("the child died of %v, want SIGKILL", err)
	}

	raw, err := os.ReadFile(filepath.Join(state, "atenea", notebook.FileName))
	if err != nil {
		t.Fatalf("nothing survived: %v", err)
	}
	if !strings.Contains(string(raw), "about to be killed") {
		t.Errorf("the entry is not the one that was written:\n%s", raw)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Error("the entry on disk is unterminated, so the next one would glue onto it")
	}
}

// recordThenDie writes one incident and then removes itself from the world
// with no chance to tidy up.
func recordThenDie(t *testing.T) {
	t.Helper()
	book, err := notebook.New(notebook.DefaultPath())
	if err != nil {
		t.Fatalf("notebook.New: %v", err)
	}
	if err := book.Record(notebook.Incident{
		Op:     "notebook.durability",
		Detail: "about to be killed",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}

// asExit is errors.As without dragging the import in for one call.
func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
