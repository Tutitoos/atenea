package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// isolated points the state root at a temp directory, which is where the
// notebook goes looking, and hands back a settings file to run against.
func isolated(t *testing.T) (settingsPath, state string) {
	t.Helper()
	state = t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ATENEA_CONFIG", "")
	return settingsFile(t), state
}

// book opens the same notebook the running command will, so a test can put a
// fault in front of the command the way a crash would have.
func book(t *testing.T, state string) *notebook.Notebook {
	t.Helper()
	n, err := notebook.New(filepath.Join(state, "atenea", notebook.FileName))
	if err != nil {
		t.Fatalf("notebook.New: %v", err)
	}
	return n
}

func fault(t *testing.T, n *notebook.Notebook, in notebook.Incident) {
	t.Helper()
	if err := n.Record(in); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

// The normal state costs no attention. A screen carrying a permanent
// "incidents 0" trains the eye to skip the one line it exists to catch.
func TestAQuietStatusScreenSaysNothingAboutIncidents(t *testing.T) {
	settings, _ := isolated(t)
	out, err := exec(t, "--config", settings, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if strings.Contains(out, "incident") {
		t.Errorf("a clean install mentions incidents:\n%s", out)
	}
}

// The fourth thing the short screen owes the design, after the light, the
// providers and the work in flight.
func TestTheStatusScreenShowsUnreadIncidents(t *testing.T) {
	settings, state := isolated(t)
	n := book(t, state)
	fault(t, n, notebook.Incident{
		Op: "orchestrator.step", Detail: "invariant broken",
		At: time.Date(2026, 8, 2, 9, 15, 0, 0, time.UTC),
	})
	fault(t, n, notebook.Incident{
		Op: "metrics.flush", Detail: "database is locked",
		At: time.Date(2026, 8, 2, 17, 40, 0, 0, time.UTC),
	})

	out, err := exec(t, "--config", settings, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "incidents 2 unread") {
		t.Errorf("the count is missing:\n%s", out)
	}
	// The date is what tells "two from last month" apart from "two just now".
	if !strings.Contains(out, "2026-08-02 17:40:00") {
		t.Errorf("the screen does not say when the latest one was:\n%s", out)
	}
	if !strings.Contains(out, "atenea incidents") {
		t.Errorf("a count with no address is a nag:\n%s", out)
	}
	// The short screen stays short: no stacks, no details.
	if strings.Contains(out, "invariant broken") {
		t.Errorf("the short screen printed an incident's detail:\n%s", out)
	}
}

// Reading is the default and it changes nothing. Two people investigating the
// same crash have to see the same file.
func TestReadingTheNotebookLeavesItAlone(t *testing.T) {
	settings, state := isolated(t)
	fault(t, book(t, state), notebook.Incident{
		Op: "orchestrator.step", Detail: "invariant broken",
		Step: "search-1", Capability: "code.search", Repository: "current",
		Stack: "goroutine 1 [running]:\nmain.boom()\n\t/src/main.go:9 +0x1f",
	})

	for range 3 {
		out, err := exec(t, "--config", settings, "incidents")
		if err != nil {
			t.Fatalf("incidents: %v\n%s", err, out)
		}
		if !strings.Contains(out, "orchestrator.step") ||
			!strings.Contains(out, "step=search-1") ||
			!strings.Contains(out, "invariant broken") {
			t.Fatalf("the entry did not come out whole:\n%s", out)
		}
		// The long height: the stack is the reason the entry exists.
		if !strings.Contains(out, "main.boom()") {
			t.Fatalf("the stack was not printed:\n%s", out)
		}
	}
	// Three reads later the status screen still says it is new.
	status, err := exec(t, "--config", settings, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, status)
	}
	if !strings.Contains(status, "incidents 1 unread") {
		t.Errorf("reading moved the mark:\n%s", status)
	}
}

// Clear is the only verb that changes anything, and it has to be typed.
func TestClearIsTheOnlyThingThatMovesTheMark(t *testing.T) {
	settings, state := isolated(t)
	fault(t, book(t, state), notebook.Incident{Op: "orchestrator.step", Detail: "boom"})

	out, err := exec(t, "--config", settings, "incidents", "clear")
	if err != nil {
		t.Fatalf("clear: %v\n%s", err, out)
	}
	if !strings.Contains(out, "marked 1 incident(s) read") {
		t.Errorf("clear did not say what it took responsibility for: %q", out)
	}
	status, err := exec(t, "--config", settings, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, status)
	}
	if strings.Contains(status, "incident") {
		t.Errorf("the screen still nags after a clear:\n%s", status)
	}
	// Cleared is not deleted. The notebook is the record.
	out, err = exec(t, "--config", settings, "incidents", "--all")
	if err != nil {
		t.Fatalf("incidents --all: %v\n%s", err, out)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("clearing threw the entry away:\n%s", out)
	}
	// And the default view now says so rather than looking empty.
	out, err = exec(t, "--config", settings, "incidents")
	if err != nil {
		t.Fatalf("incidents: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing new") || !strings.Contains(out, "--all") {
		t.Errorf("a read notebook reads as empty rather than as read:\n%s", out)
	}
}

// An Atenea that never fell over says so plainly, rather than printing an
// empty list that reads like a bug.
func TestAnEmptyNotebookSaysItIsEmpty(t *testing.T) {
	settings, _ := isolated(t)
	out, err := exec(t, "--config", settings, "incidents")
	if err != nil {
		t.Fatalf("incidents: %v\n%s", err, out)
	}
	if !strings.Contains(out, "empty") {
		t.Errorf("out = %q", out)
	}
}

// A word the command does not know is a typo, and a typo that silently did
// something else would be the worst possible behavior for the one command
// with a destructive verb in it.
func TestAnUnknownWordIsRefused(t *testing.T) {
	settings, state := isolated(t)
	fault(t, book(t, state), notebook.Incident{Op: "orchestrator.step", Detail: "boom"})

	out, err := exec(t, "--config", settings, "incidents", "cler")
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input (%q)", contract.KindOf(err), out)
	}
	status, _ := exec(t, "--config", settings, "status")
	if !strings.Contains(status, "1 unread") {
		t.Errorf("a misspelled clear cleared anyway:\n%s", status)
	}
}

// A torn line is itself worth knowing about: it is the one thing that would
// make the counts quietly wrong, and it is reported at both heights.
func TestATornEntryIsReportedNotHidden(t *testing.T) {
	settings, state := isolated(t)
	n := book(t, state)
	fault(t, n, notebook.Incident{Op: "orchestrator.step", Detail: "boom"})
	path := filepath.Join(state, "atenea", notebook.FileName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString("{\"op\":\"torn\n"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	status, err := exec(t, "--config", settings, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, status)
	}
	if !strings.Contains(status, "1 unreadable") {
		t.Errorf("the status screen hid the torn line:\n%s", status)
	}
	out, err := exec(t, "--config", settings, "incidents")
	if err != nil {
		t.Fatalf("incidents: %v\n%s", err, out)
	}
	if !strings.Contains(out, "torn mid-entry") {
		t.Errorf("the read hid the torn line:\n%s", out)
	}
}

// The command is on the front page. A tool nobody can find is a tool nobody
// uses, and this one only matters on the day something has gone wrong.
func TestTheUsageMentionsTheNotebook(t *testing.T) {
	out, err := exec(t, "help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(out, "incidents") || !strings.Contains(out, "crash notebook") {
		t.Errorf("usage does not mention the notebook:\n%s", out)
	}
}
