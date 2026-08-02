package notebook_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/notebook"
)

// unread is the number the status screen shows, fetched the way the status
// screen fetches it.
func unread(t *testing.T, n *notebook.Notebook) int {
	t.Helper()
	read, err := n.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return read.Unread
}

// Everything is new until somebody says otherwise. A notebook whose entries
// started life read would hide the first crash on a fresh machine, which is
// the one most worth seeing.
func TestEveryEntryStartsUnread(t *testing.T) {
	n, _ := open(t)
	for _, op := range []string{"a", "b", "c"} {
		note(t, n, notebook.Incident{Op: op})
	}
	if got := unread(t, n); got != 3 {
		t.Errorf("unread = %d, want 3", got)
	}
}

// The promise the incidents command is built on: reading changes nothing. Two
// people investigating the same crash have to see the same file, and the
// second one must not find it already marked because the first one looked.
func TestReadingNeverMovesTheMark(t *testing.T) {
	n, path := open(t)
	note(t, n, notebook.Incident{Op: "boom"})
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	for range 5 {
		if got := unread(t, n); got != 1 {
			t.Fatalf("a read changed the count to %d", got)
		}
	}
	mark := filepath.Join(filepath.Dir(path), notebook.MarkName)
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Error("reading created the watermark")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Error("reading touched the notebook itself")
	}
}

// Clear is the only thing that moves the mark, and it says how many it just
// took responsibility for.
func TestClearMarksWhatWasThereAndReportsIt(t *testing.T) {
	n, _ := open(t)
	note(t, n, notebook.Incident{Op: "a"})
	note(t, n, notebook.Incident{Op: "b"})

	cleared, err := n.Clear()
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if cleared != 2 {
		t.Errorf("Clear reported %d, want 2", cleared)
	}
	if got := unread(t, n); got != 0 {
		t.Errorf("unread after clear = %d", got)
	}
	// And clearing again is not a second announcement of the same two.
	if cleared, err = n.Clear(); err != nil || cleared != 0 {
		t.Errorf("clearing twice reported %d (%v)", cleared, err)
	}
}

// Clearing does not delete. The notebook is the record; the mark is only where
// somebody's attention got to, and in alpha the old entries are the archive.
func TestClearKeepsEveryEntry(t *testing.T) {
	n, _ := open(t)
	note(t, n, notebook.Incident{Op: "a"})
	if _, err := n.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	read, err := n.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read.Incidents) != 1 {
		t.Errorf("clearing threw the entry away: %+v", read)
	}
}

// An incident that lands after the clear is new, because the mark is a count
// of what was seen rather than a moment in time. Anything else would let a
// crash that happened while you were reading slip through unmentioned.
func TestAnIncidentAfterTheClearIsStillNew(t *testing.T) {
	n, _ := open(t)
	note(t, n, notebook.Incident{Op: "old"})
	if _, err := n.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	note(t, n, notebook.Incident{Op: "new"})

	read, err := n.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if read.Unread != 1 {
		t.Fatalf("unread = %d, want just the new one", read.Unread)
	}
	fresh := read.New()
	if len(fresh) != 1 || fresh[0].Op != "new" {
		t.Errorf("New() = %+v, want the one that arrived after the clear", fresh)
	}
}

// The mark is a watermark, and a broken one has to fail towards showing too
// much. Reading an old incident twice is a nuisance; hiding a new one is the
// failure the notebook exists to prevent.
func TestABrokenMarkShowsEverythingRatherThanNothing(t *testing.T) {
	n, path := open(t)
	note(t, n, notebook.Incident{Op: "a"})
	note(t, n, notebook.Incident{Op: "b"})
	if _, err := n.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	mark := filepath.Join(filepath.Dir(path), notebook.MarkName)

	for _, broken := range []string{"", "   ", "not a number", "-4", "\x00\x01"} {
		if err := os.WriteFile(mark, []byte(broken), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if got := unread(t, n); got != 2 {
			t.Errorf("mark %q left unread = %d, want everything shown", broken, got)
		}
	}
}

// A mark pointing past the end of the file means the notebook was shortened
// behind Atenea's back. Trusting it would hide whatever survived.
func TestAMarkPastTheEndShowsWhatIsLeft(t *testing.T) {
	n, path := open(t)
	note(t, n, notebook.Incident{Op: "only"})
	mark := filepath.Join(filepath.Dir(path), notebook.MarkName)
	if err := os.WriteFile(mark, []byte("99\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := unread(t, n); got != 1 {
		t.Errorf("unread = %d, want the surviving entry counted as new", got)
	}
}

// The mark lives beside the notebook, not at a fixed path. A redirected
// notebook with its mark left behind in the default place would be counted
// against a line total from a completely different file.
func TestTheMarkFollowsTheNotebook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "elsewhere", "other-name.jsonl")
	n, err := notebook.New(path)
	if err != nil {
		t.Fatalf("notebook.New: %v", err)
	}
	note(t, n, notebook.Incident{Op: "a"})
	if _, err := n.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "elsewhere", notebook.MarkName)); err != nil {
		t.Errorf("the mark is not beside the notebook: %v", err)
	}
}

// A path is required, and a notebook is prepared without being created.
func TestNewNeedsAPathAndCreatesNoFile(t *testing.T) {
	if _, err := notebook.New("  "); err == nil {
		t.Error("an empty path was accepted")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "down", notebook.FileName)
	if _, err := notebook.New(path); err != nil {
		t.Fatalf("notebook.New: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("the directory was not prepared: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("New created the notebook before anything went wrong")
	}
}

// The default path sits under the state root beside the receipts, and moves
// with XDG_STATE_HOME so a test or a container can put it somewhere else.
func TestTheDefaultPathIsBesideTheReceipts(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/somewhere")
	want := filepath.Join("/tmp/somewhere", "atenea", notebook.FileName)
	if got := notebook.DefaultPath(); got != want {
		t.Errorf("DefaultPath = %s, want %s", got, want)
	}
}
