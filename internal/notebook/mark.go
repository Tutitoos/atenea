package notebook

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Read is everything in the notebook and how much of it is new.
//
// Both halves come out of one pass, because the interesting question is
// nearly always both at once: the status screen wants the count, the incidents
// command wants the entries, and reading the file twice to answer them
// separately would leave a window where the two disagree.
type Read struct {
	// Incidents is every entry in the notebook, oldest first.
	Incidents []Incident
	// Unread is how many of them are new, counting from the end. Reading
	// never changes it; only Clear does.
	Unread int
	// Unreadable counts lines that were not valid JSON. It is reported rather
	// than hidden: a torn line is itself worth knowing about, and it is the
	// one thing that would make the counts above quietly wrong.
	Unreadable int
}

// New returns just the unread tail, oldest first.
func (r Read) New() []Incident {
	if r.Unread <= 0 {
		return nil
	}
	if r.Unread >= len(r.Incidents) {
		return r.Incidents
	}
	return r.Incidents[len(r.Incidents)-r.Unread:]
}

// Read opens the notebook and the mark beside it.
//
// It changes nothing. That is a promise the incidents command depends on:
// looking at a notebook must never be the reason a later look shows something
// different, or two people investigating the same crash would see two
// different files.
//
// A missing notebook is not an error. It is the normal state of an Atenea that
// has never fallen over, and the caller wants an empty answer rather than a
// condition to handle.
func (n *Notebook) Read() (Read, error) {
	if n == nil {
		return Read{}, nil
	}
	f, err := os.Open(n.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Read{}, nil
	}
	if err != nil {
		return Read{}, contract.Fail(contract.FailureUnavailable,
			"notebook: %s: %v", n.path, err)
	}
	defer func() { _ = f.Close() }()

	out := Read{}
	scan := bufio.NewScanner(f)
	// A panic stack is long, and a stack cut off at the default 64 KiB would
	// be a torn line in a file whose whole purpose is the untruncated blow.
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scan.Scan() {
		line := scan.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var in Incident
		if err := json.Unmarshal(line, &in); err != nil {
			out.Unreadable++
			continue
		}
		out.Incidents = append(out.Incidents, in)
	}
	if err := scan.Err(); err != nil {
		return Read{}, contract.Fail(contract.FailureUnavailable,
			"notebook: %s: %v", n.path, err)
	}

	read, err := n.readMark()
	if err != nil {
		return Read{}, err
	}
	// A mark ahead of the file means somebody shortened the notebook behind
	// our back. Treating that as "everything is read" would hide whatever is
	// left, so the remainder counts as new.
	if read > len(out.Incidents) {
		read = 0
	}
	out.Unread = len(out.Incidents) - read
	return out, nil
}

// Clear marks everything currently in the notebook as read and reports how
// many entries that was.
//
// This is the only call in the package that moves the mark, which is why it is
// its own verb on the command line. An incident that arrives between the read
// and the clear stays new, because the mark records a count rather than a
// moment: whoever clears has, by definition, only seen what was there.
func (n *Notebook) Clear() (int, error) {
	if n == nil {
		return 0, contract.Fail(contract.FailureUnavailable, "notebook: not attached")
	}
	current, err := n.Read()
	if err != nil {
		return 0, err
	}
	total := len(current.Incidents)
	// Written whole and then renamed, the same way a run receipt is: a mark
	// interrupted halfway would be a number nobody can read, and the notebook
	// would go back to looking entirely new.
	tmp := n.mark + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(total)+"\n"), 0o600); err != nil {
		return 0, contract.Fail(contract.FailureUnavailable,
			"notebook: %s: %v", n.mark, err)
	}
	if err := os.Rename(tmp, n.mark); err != nil {
		_ = os.Remove(tmp)
		return 0, contract.Fail(contract.FailureUnavailable,
			"notebook: %s: %v", n.mark, err)
	}
	return current.Unread, nil
}

// readMark is how many entries have been looked at. A mark that is missing,
// empty or nonsense reads as zero: the failure mode of a broken watermark
// should be showing an old incident twice, never hiding a new one.
func (n *Notebook) readMark() (int, error) {
	raw, err := os.ReadFile(n.mark)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, contract.Fail(contract.FailureUnavailable,
			"notebook: %s: %v", n.mark, err)
	}
	// Atoi already answers zero for anything it cannot read, which is the
	// answer this wants: a broken watermark shows an old incident twice, it
	// never hides a new one. A negative mark is somebody editing the file.
	read, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if read < 0 {
		return 0, nil
	}
	return read, nil
}

// Line is one incident rendered for a terminal, without the stack.
//
// The stack is the reason the entry exists and the reason it cannot go in a
// list: one blow is hundreds of lines. So the list is scannable and the stack
// is printed underneath it only when the whole notebook is being read out.
func (in Incident) Line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s", in.At.Format("2006-01-02 15:04:05"), in.Op)
	for _, id := range [][2]string{
		{"run", in.RunID},
		{"step", in.Step},
		{"capability", in.Capability},
		{"implementation", in.Implementation},
		{"repository", in.Repository},
	} {
		if id[1] != "" {
			fmt.Fprintf(&b, "  %s=%s", id[0], id[1])
		}
	}
	if len(in.Fields) > 0 {
		fmt.Fprintf(&b, "  fields=%s", strings.Join(in.Fields, ","))
	}
	if in.Detail != "" {
		fmt.Fprintf(&b, "\n    %s", in.Detail)
	}
	return b.String()
}
