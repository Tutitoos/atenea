package notebook_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func open(t *testing.T) (*notebook.Notebook, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, notebook.FileName)
	n, err := notebook.New(path)
	if err != nil {
		t.Fatalf("notebook.New: %v", err)
	}
	return n, path
}

// note records one incident and fails the test if it could not be written,
// because every assertion below is about what reached the disk.
func note(t *testing.T, n *notebook.Notebook, in notebook.Incident) {
	t.Helper()
	if err := n.Record(in); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

// The property the whole package exists for: when Record returns, the entry is
// already on the disk. Everything else here is arrangement; this is the one
// that would make the notebook pointless if it broke.
func TestAnEntryIsOnDiskBeforeRecordReturns(t *testing.T) {
	n, path := open(t)
	note(t, n, notebook.Incident{Op: "orchestrator.step", Detail: "boom"})

	// Read the file with something that shares no state with the notebook: if
	// the entry were buffered anywhere, this is where it would not be.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file is not there at all: %v", err)
	}
	var got notebook.Incident
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &got); err != nil {
		t.Fatalf("the line on disk is not an incident: %v (%q)", err, raw)
	}
	if got.Op != "orchestrator.step" || got.Detail != "boom" {
		t.Errorf("on disk: %+v", got)
	}
	if got.At.IsZero() {
		t.Error("the entry carries no time, so nobody can order two of them")
	}
	if got.PID == 0 {
		t.Error("the entry does not say which process it came from")
	}
}

// A notebook that has never been written to must not exist. An empty file and
// a missing one mean the same thing, and only one of them can be mistaken for
// a notebook somebody emptied.
func TestAnAteneaThatNeverFellOverLeavesNoFile(t *testing.T) {
	n, path := open(t)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the notebook exists before anything went wrong: %v", err)
	}
	read, err := n.Read()
	if err != nil {
		t.Fatalf("reading a notebook that is not there: %v", err)
	}
	if len(read.Incidents) != 0 || read.Unread != 0 {
		t.Errorf("an absent notebook read as %+v", read)
	}
}

// Entries accumulate in the order they happened. A notebook is read to
// reconstruct a sequence, so the order is the content.
func TestEntriesAccumulateOldestFirst(t *testing.T) {
	n, _ := open(t)
	for _, op := range []string{"first", "second", "third"} {
		note(t, n, notebook.Incident{Op: op})
	}
	read, err := n.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var ops []string
	for _, in := range read.Incidents {
		ops = append(ops, in.Op)
	}
	if strings.Join(ops, ",") != "first,second,third" {
		t.Errorf("order = %v", ops)
	}
}

// An incident with nothing to say about what was happening is not an incident,
// it is noise that will be unreadable in a week.
func TestAnIncidentWithoutAnOpIsRefused(t *testing.T) {
	n, path := open(t)
	err := n.Record(notebook.Incident{Detail: "something went wrong somewhere"})
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a refused incident still created the file")
	}
}

// The whole sensitive rule, made structural. This test reads the JSON on disk
// looking for anything that could carry a value, because the guarantee is
// about the bytes that get written, not about the intent of the caller.
func TestTheEntryHasNowhereToPutAValue(t *testing.T) {
	n, path := open(t)
	note(t, n, notebook.Incident{
		Op:             "orchestrator.step",
		Capability:     "code.search",
		Implementation: "ripgrep",
		Repository:     "api",
		Fields:         []string{"query", "scope"},
	})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &generic); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// Every key on the wire is accounted for. A future field that could hold a
	// payload has to be added here deliberately, which is the point: the test
	// fails when the shape changes, not when somebody misuses it.
	allowed := map[string]bool{
		"at": true, "op": true, "detail": true, "stack": true,
		"run_id": true, "step": true, "capability": true,
		"implementation": true, "repository": true, "fields": true,
		"pid": true, "version": true,
	}
	for key := range generic {
		if !allowed[key] {
			t.Errorf("the incident carries an unvetted key %q, which is where a value would leak", key)
		}
	}
	// Fields is names. If it ever became a map, this is what would catch it.
	fields, ok := generic["fields"].([]any)
	if !ok {
		t.Fatalf("fields is %T, want a list of names", generic["fields"])
	}
	for _, f := range fields {
		if _, ok := f.(string); !ok {
			t.Errorf("a field name is %T, so something other than a name fits in there", f)
		}
	}
}

// A panic is recorded and then goes on killing the process. Recovering it into
// a normal failure would file an Atenea bug under the bin a provider being
// down uses, which is the confusion the notebook exists to end.
func TestAPanicIsWrittenDownAndStillKills(t *testing.T) {
	n, _ := open(t)

	survived := func() (again any) {
		defer func() { again = recover() }()
		func() {
			defer n.Catch(notebook.Incident{Op: "orchestrator.step", Step: "search-1"})
			panic("invariant broken")
		}()
		return nil
	}()

	if survived == nil {
		t.Fatal("Catch swallowed the panic; a broken invariant is not a failed step")
	}
	if fmt := survived.(string); fmt != "invariant broken" {
		t.Errorf("the panic came back changed: %q", fmt)
	}
	read, err := n.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read.Incidents) != 1 {
		t.Fatalf("the notebook holds %d entries, want the panic", len(read.Incidents))
	}
	got := read.Incidents[0]
	if got.Detail != "invariant broken" {
		t.Errorf("detail = %q", got.Detail)
	}
	if got.Step != "search-1" {
		t.Errorf("the entry does not say which step panicked: %+v", got)
	}
	if !strings.Contains(got.Stack, "notebook_test.go") {
		t.Errorf("the stack does not reach the panic site:\n%s", got.Stack)
	}
}

// Catch on a clean return must do nothing at all -- not an entry, not a file.
// It is deferred on every step, so a notebook that filled up with successes
// would bury the one entry that mattered.
func TestCatchIsSilentWhenNothingWentWrong(t *testing.T) {
	n, path := open(t)
	func() {
		defer n.Catch(notebook.Incident{Op: "orchestrator.step"})
	}()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a step that returned cleanly wrote to the crash notebook")
	}
}

// A nil notebook is what a hand-assembled test wires. It has to be harmless --
// but a panic still has to kill, or the nil case would quietly change
// behavior instead of quietly recording nothing.
func TestANilNotebookRecordsNothingAndStillLetsAPanicThrough(t *testing.T) {
	var n *notebook.Notebook
	if err := n.Record(notebook.Incident{Op: "x"}); err == nil {
		t.Error("a nil notebook claimed to have written something")
	}
	if read, err := n.Read(); err != nil || len(read.Incidents) != 0 {
		t.Errorf("reading a nil notebook: %+v %v", read, err)
	}
	again := func() (r any) {
		defer func() { r = recover() }()
		func() {
			defer n.Catch(notebook.Incident{Op: "x"})
			panic("still fatal")
		}()
		return nil
	}()
	if again == nil {
		t.Error("a nil notebook swallowed a panic")
	}
}

// A stack is thousands of bytes and it all has to come back. Truncating it
// would leave the notebook holding the top of a blow whose cause is in the
// part that was dropped.
func TestALongStackSurvivesTheRoundTrip(t *testing.T) {
	n, _ := open(t)
	long := strings.Repeat("goroutine 1 [running]:\nmain.deep(0x1, 0x2)\n\t/src/main.go:42 +0x1f\n", 4000)
	note(t, n, notebook.Incident{Op: "panic", Stack: long})
	read, err := n.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(read.Incidents) != 1 {
		t.Fatalf("entries = %d", len(read.Incidents))
	}
	if read.Incidents[0].Stack != long {
		t.Errorf("the stack came back %d bytes, want %d",
			len(read.Incidents[0].Stack), len(long))
	}
}

// One torn line must not cost the reader the entries around it. This is the
// failure mode that would matter most: a notebook that refuses to open is a
// notebook that failed at the only job it has.
func TestATornLineIsCountedNotFatal(t *testing.T) {
	n, path := open(t)
	note(t, n, notebook.Incident{Op: "before"})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString(`{"op":"half of a line`); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The next incident must not inherit the wound.
	note(t, n, notebook.Incident{Op: "after"})

	read, err := n.Read()
	if err != nil {
		t.Fatalf("a torn line made the notebook unreadable: %v", err)
	}
	if read.Unreadable != 1 {
		t.Errorf("unreadable = %d, want the torn line counted", read.Unreadable)
	}
	if len(read.Incidents) != 2 {
		t.Fatalf("entries = %d, want the two good ones", len(read.Incidents))
	}
}

// A time and an op are enough to render a line; identifiers show up only when
// they were known. A list padded with empty fields is a list nobody scans.
func TestTheListLineShowsOnlyWhatWasKnown(t *testing.T) {
	bare := notebook.Incident{
		At: time.Date(2026, 8, 2, 11, 30, 0, 0, time.UTC),
		Op: "metrics.flush", Detail: "database is locked",
	}
	line := bare.Line()
	if !strings.Contains(line, "2026-08-02 11:30:00") || !strings.Contains(line, "metrics.flush") {
		t.Errorf("line = %q", line)
	}
	if strings.Contains(line, "repository=") || strings.Contains(line, "step=") {
		t.Errorf("the line padded itself with empty identifiers: %q", line)
	}

	full := bare
	full.Repository, full.Implementation, full.Fields = "api", "ripgrep", []string{"query"}
	line = full.Line()
	for _, want := range []string{"repository=api", "implementation=ripgrep", "fields=query"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q is missing %q", line, want)
		}
	}
	if strings.Contains(line, "goroutine") {
		t.Error("the list line carries a stack; one blow would bury the list")
	}
}

// core.New builds the notebook before the measurement base and before the
// receipt folder, deliberately, so that a crash during the rest of the
// assembly still has somewhere to be written down. The consequence is that on
// a machine with no state root yet, this MkdirAll is the call that fixes the
// mode of the root every other artifact then lands in -- and it asked for
// 0755, so every incident, run receipt and measurement underneath was readable
// by every account on the box.
func TestTheStateRootIsNotCreatedReadableByOtherAccounts(t *testing.T) {
	// A directory that does not exist yet: t.TempDir is already 0700, so
	// creating the notebook straight into it would prove nothing.
	root := filepath.Join(t.TempDir(), "state")
	if _, err := notebook.New(filepath.Join(root, notebook.FileName)); err != nil {
		t.Fatalf("notebook.New: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("the state root was not created at all: %v", err)
	}
	// Group and other, rather than an exact mode, because the umask can only
	// take permissions away and the property being asserted is that none were
	// offered in the first place.
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("state root mode = %04o, want nothing outside the owner: "+
			"the notebook creates this directory before anything else does", mode)
	}
}

// Nothing in this package ever removed a line, and nothing above it suppresses
// a repeat: one incident per failed maintenance beat, for as long as the fault
// lasts. A provider left broken over a weekend therefore grew a file with no
// ceiling, and it is Read that paid for it -- the status screen parses the
// whole notebook into memory every time somebody asks how the machine is.
func TestAFullNotebookIsRotatedInsteadOfGrowing(t *testing.T) {
	n, path := open(t)
	detail := strings.Repeat("x", 64<<10)
	beat := func() {
		note(t, n, notebook.Incident{Op: "maintenance.beat", Detail: detail})
	}

	// Five entries the operator has seen, so the watermark is somewhere real
	// before the rotation moves the file out from under it.
	for range 5 {
		beat()
	}
	if _, err := n.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	rotated := path + notebook.RotatedSuffix
	for i := 0; ; i++ {
		if i > 4*(notebook.MaxBytes/len(detail)) {
			t.Fatal("the notebook grew past four times its ceiling without rotating")
		}
		beat()
		if _, err := os.Stat(rotated); err == nil {
			break
		}
	}
	// Ten more after the rotation, so the live notebook holds more entries
	// than the stale watermark counted. Fewer and the reader's own guard
	// against a mark that overruns the file would hide whether the mark was
	// reset at all.
	for range 10 {
		beat()
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the live notebook is gone: %v", err)
	}
	if info.Size() >= notebook.MaxBytes {
		t.Errorf("the live notebook is %d bytes, want it back under the %d ceiling",
			info.Size(), notebook.MaxBytes)
	}
	if _, err := os.Stat(rotated); err != nil {
		t.Errorf("the previous notebook was not kept beside the new one: %v", err)
	}

	read, err := n.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if read.Unread != len(read.Incidents) {
		t.Errorf("%d of %d entries read as already seen: the watermark counts from the "+
			"start of the notebook and the notebook is a new one", len(read.Incidents)-read.Unread, len(read.Incidents))
	}
}
