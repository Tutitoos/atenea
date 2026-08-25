// Package notebook is the crash notebook: where Atenea writes down its own
// serious faults, the instant they happen.
//
// It is the measurement base's opposite on the one axis that matters. The base
// batches, because a lost measurement is one row out of thousands and writing
// it must never slow real work down. Here the entry you lose is precisely the
// one you needed -- the fault that killed the process is the one still sitting
// in a buffer when it dies -- so an incident is appended and synced to disk
// before Record returns, and the caller pays for it. Incidents are meant to be
// rare enough that nobody notices the bill.
//
// # What belongs in here
//
// Atenea's own faults, not the world's. A provider that is down, a repository
// that cannot be read, a model that refused the work: every one of those has a
// bin in pkg/contract, travels back to whoever asked, and lands on a receipt.
// That is the system working. An incident is the other thing -- a panic, a
// maintenance job nobody was waiting on that failed anyway, measurements
// thrown away because the disk would not take them. Nobody is waiting to be
// told about any of it, so without this file nobody ever is.
//
// # Names, never values
//
// An incident carries identifiers -- capability, implementation, repository,
// the names of the payload fields in play -- and never what was inside them.
// That is the rule the trace already follows, and here it is the shape of the
// type rather than a promise somebody has to keep: Fields is a list of names,
// so there is nowhere for a value to be put. Nothing in this package accepts a
// payload, and nothing reads one.
//
// # One line, one incident
//
// The file is JSON Lines, opened for append and closed again on every write.
// There is no handle to keep in sync and nothing buffered anywhere, which is
// what lets a `kill -9` one instruction later still leave a complete entry.
// Each incident is written with a single write syscall, so two Ateneas falling
// over at once do not interleave; the reader is built to survive it anyway,
// because a notebook that refuses to open after one bad line would fail at the
// only job it has.
//
// # Bounded, because nobody is watching
//
// The file is rotated at MaxBytes and exactly one previous notebook is kept.
// Nothing above this package suppresses a repeat -- a maintenance beat that
// fails every time it runs records an incident every time it runs -- so a
// fault left unattended over a weekend is otherwise a file with no ceiling.
// The cost lands on Read, which parses the whole notebook into memory on every
// status screen, and rotation is what keeps that a small cost rather than a
// growing one. Read looks at the live file alone: the rotated one is there for
// whoever arrives after a long outage and wants to see how far back it goes.
package notebook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Incident is one entry: what broke, where, and when.
//
// Every string on it is an identifier or a sentence Atenea's own code wrote.
// None of them is data that came out of a repository, a payload or a far side.
type Incident struct {
	At time.Time `json:"at"`
	// Op names what Atenea was doing, in Atenea's vocabulary rather than the
	// operating system's: "orchestrator.step", "metrics.flush". It is the
	// field the list is scanned on, so it is worth keeping short and stable.
	Op string `json:"op"`
	// Detail is the fault as the code reported it, untranslated. There are no
	// bins here on purpose: bins exist to let an adapter's error be routed
	// without reading it, and nobody routes on a bug.
	Detail string `json:"detail"`
	// Stack is the goroutine's stack at the moment of a panic, entire. Empty
	// on an incident that was reported rather than thrown, which is also how
	// the two are told apart on the way out.
	Stack string `json:"stack,omitempty"`

	// The identifiers in play, any of which may be empty. What is here is what
	// makes an incident actionable a week later: which run, which step, and
	// who was being asked to do what.
	RunID          string `json:"run_id,omitempty"`
	Step           string `json:"step,omitempty"`
	Capability     string `json:"capability,omitempty"`
	Implementation string `json:"implementation,omitempty"`
	Repository     string `json:"repository,omitempty"`

	// Fields names the payload fields that were in play. Names only: this is
	// where a query string or a file path would leak if the type allowed one,
	// and it does not.
	Fields []string `json:"fields,omitempty"`

	// PID and Version say which process this was and which build it was
	// running. A notebook is read long after the fact, often across an
	// upgrade, and "which binary did this" is the first question.
	PID     int    `json:"pid"`
	Version string `json:"version,omitempty"`
}

// Notebook is the file. It holds no handle and no buffer: every operation
// opens what it needs and closes it again, which is the only way an entry
// written a microsecond before a kill is still there afterwards.
type Notebook struct {
	path string
	mark string
}

// FileName is the notebook itself, and MarkName the watermark beside it.
//
// The mark is a separate file because the notebook is append-only and has to
// stay that way: marking an entry read by rewriting the line it is on would
// mean holding the whole file open for writing at the exact moment Atenea is
// least trustworthy.
const (
	FileName = "incidents.jsonl"
	MarkName = "incidents.mark"
)

// RotatedSuffix names the one previous notebook that is kept, and MaxBytes is
// the size the live one is allowed to reach before it becomes that.
//
// Something had to bound this. Nothing in the package ever removed a line, and
// nothing above it suppresses a repeat: a maintenance beat that fails every
// time it runs writes one incident per beat for as long as the fault lasts, so
// a provider left broken over a weekend is a file that grows without limit. It
// is Read that pays -- the status screen parses the whole notebook into memory
// on every call, so an unattended fault turns `atenea status` into a
// megabyte-per-invocation JSON parse -- and Record pays too, because every
// entry is synced to a file the filesystem keeps having to extend.
//
// Four megabytes is roughly ten thousand entries with a stack on each. That is
// far more than anybody reads and far less than anybody notices.
const (
	RotatedSuffix = ".1"
	MaxBytes      = 4 << 20
)

// DefaultPath is where the notebook lives when nobody says otherwise: beside
// the run receipts and the measurement base, under the same state root. All
// three are the same kind of thing -- what Atenea remembers about work it has
// done -- and the point of a crash notebook is that you can find it.
func DefaultPath() string { return filepath.Join(platform.StateDir(), FileName) }

// New prepares the notebook at path, creating the directory if it is missing.
//
// The file itself is not created. An Atenea that never fell over should leave
// no notebook at all: an empty one and a missing one mean the same thing, and
// only one of them can be confused with a notebook that was wiped.
func New(path string) (*Notebook, error) {
	if strings.TrimSpace(path) == "" {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"notebook: path is required")
	}
	// 0700, not 0755. The directory this creates is normally platform.StateDir
	// itself, and core.New builds the notebook before the measurement base and
	// before the receipt folder -- deliberately, so a crash during the rest of
	// the assembly still has somewhere to be written down. That order made
	// this MkdirAll the one that fixes the mode of the whole state root on a
	// fresh machine, and 0755 left every incident, receipt and measurement
	// under it readable by every account on the box. The stores that follow
	// ask for 0700 and 0750; the first one through the door has to ask for at
	// least as little.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"notebook: %s: %v", filepath.Dir(path), err)
	}
	return &Notebook{path: path, mark: markPath(path)}, nil
}

// markPath puts the watermark beside the notebook whatever the notebook is
// called, so a redirected file does not leave its mark behind in the default
// place, pointing at a line count from another file entirely.
func markPath(path string) string {
	return filepath.Join(filepath.Dir(path), MarkName)
}

// Path is where the notebook is written.
func (n *Notebook) Path() string {
	if n == nil {
		return ""
	}
	return n.path
}

// Record appends one incident and does not return until it is on the disk.
//
// A nil notebook records nothing and says so, rather than pretending. The core
// always builds one; the nil case is for callers assembled by hand in a test,
// where an unwritten incident is not a surprise.
func (n *Notebook) Record(in Incident) error {
	if n == nil {
		return contract.Fail(contract.FailureUnavailable, "notebook: not attached")
	}
	if in.At.IsZero() {
		in.At = time.Now()
	}
	if in.PID == 0 {
		in.PID = os.Getpid()
	}
	if strings.TrimSpace(in.Op) == "" {
		return contract.Fail(contract.FailureInvalidInput,
			"notebook: an incident needs an op")
	}
	line, err := json.Marshal(in)
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput, "notebook: %v", err)
	}
	if err := n.rotateIfFull(); err != nil {
		return err
	}
	// Read-write rather than write-only: the file is still append-only, but
	// the tail has to be looked at before adding to it. See below.
	f, err := os.OpenFile(n.path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "notebook: %s: %v", n.path, err)
	}
	line = append(line, '\n')
	// A file that does not end in a newline was cut off mid-entry: a disk that
	// filled, a process killed between the write starting and finishing. The
	// next entry would otherwise be glued onto the wound and both would be
	// lost. Opening with a newline closes it instead, so one torn line costs
	// exactly one torn line.
	if unterminated(f) {
		line = append([]byte{'\n'}, line...)
	}
	// One write for the whole thing. Splitting it would be the one way two
	// Ateneas could tangle their lines together, and it would show up only
	// during exactly the kind of failure this file exists to record.
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return contract.Fail(contract.FailureUnavailable, "notebook: %s: %v", n.path, err)
	}
	// The sync is the whole design. Without it the entry lives in the page
	// cache, which survives a panic but not the power cut or the OOM killer
	// that a crash notebook is meant to outlive.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return contract.Fail(contract.FailureUnavailable, "notebook: %s: %v", n.path, err)
	}
	return f.Close()
}

// rotateIfFull moves a notebook that has reached MaxBytes aside and starts a
// fresh one, so the live file stays the size the status screen can afford to
// parse on every invocation.
//
// Exactly one previous notebook is kept, under RotatedSuffix. A second one
// would be a retention ladder, and a crash notebook is not a history: the
// entries worth acting on are the recent ones, and the older file is there so
// that whoever arrives after a long outage can still see how far back it goes.
//
// The watermark goes back to zero with the file. It counts entries from the
// start of the notebook, so leaving it where it was would have it point into
// the middle of the new one and mark the first entries after a rotation as
// already read -- which is the one mistake this file must not make.
//
// A rotation that cannot happen is not worth losing an incident over: the
// error is dropped and the entry is appended to the oversized file, because a
// notebook that is too big is a nuisance and a notebook missing the entry
// somebody needed is the failure the package exists to prevent.
func (n *Notebook) rotateIfFull() error {
	info, err := os.Stat(n.path)
	if err != nil || info.Size() < MaxBytes {
		return nil //nolint:nilerr // a notebook that is not there yet is not full
	}
	if err := os.Rename(n.path, n.path+RotatedSuffix); err != nil {
		return nil //nolint:nilerr // see above: the entry matters more than the size
	}
	return n.writeMark(0)
}

// Catch writes down a panic and then lets it carry on killing the process.
//
// Deferred at a boundary where a panic would otherwise vanish into a goroutine
// or arrive at the top without saying which work it came from. It deliberately
// does not stop the panic. A broken invariant is not a failed step, and
// converting one into the other would file an Atenea bug under the same bin a
// provider being down uses -- the exact confusion this package exists to end.
// All Catch does is make sure the blow is on disk before the process goes.
//
// The identity is filled in by the caller at defer time, when it is known.
// Detail and Stack are overwritten here, because the panic is the fault.
func (n *Notebook) Catch(in Incident) {
	r := recover()
	if r == nil {
		return
	}
	in.Detail = fmt.Sprint(r)
	in.Stack = string(debug.Stack())
	if err := n.Record(in); err != nil {
		// There is no turtle below this one. Stderr is not durable and may go
		// nowhere, but a crash notebook that failed silently would be worse
		// than not having one, because the file would then be evidence of
		// nothing having happened.
		fmt.Fprintf(os.Stderr, "atenea: the crash notebook could not be written (%v)\n", err)
		fmt.Fprintf(os.Stderr, "atenea: unrecorded incident in %s: %v\n", in.Op, r)
	}
	panic(r)
}

// FieldsOf is how a payload becomes an incident: its keys, sorted, and
// nothing else.
//
// It lives here rather than at the call site because this is the package that
// makes the promise. A caller reaching for a payload has to come through this
// function to get anything out of it, and this function cannot return a value
// even if it wanted to.
func FieldsOf(payload map[string]any) []string {
	if len(payload) == 0 {
		return nil
	}
	out := make([]string, 0, len(payload))
	for name := range payload {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// unterminated reports whether the file ends mid-line. A file it cannot
// measure or read is treated as intact: guessing "torn" would put a stray
// blank line into a healthy notebook on every write.
func unterminated(f *os.File) bool {
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return false
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
		return false
	}
	return last[0] != '\n'
}
