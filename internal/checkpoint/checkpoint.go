// Package checkpoint writes the state of a run to disk so a long commission
// survives the machine going away.
//
// Memory is a whiteboard: the power goes and it is blank. Disk is paper. The
// dump is deliberately narrow -- it happens when a step closes and again on a
// clean stop, and at no other moment. Writing more often would cost more than
// it saves, and writing less would mean the paper is out of date exactly when
// it is needed.
package checkpoint

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Run is the paper copy of one commission in flight.
type Run struct {
	ID string `json:"id"`
	// Kind tells a task-shaped commission apart from a single ask: the two
	// resume differently, one through explore-then-split and the other as
	// one step, and there is nothing else on a receipt that says which it
	// was. "task" and "ask" are the only values.
	Kind string `json:"kind"`
	// Session is the chat this run belongs to, when one owns it. History is
	// common property across chats; knowing whose it was is what keeps it
	// readable rather than a pile.
	Session string `json:"session,omitempty"`
	Task    string `json:"task"`
	// Repositories is the commission's own narrowing, kept so a resumed run
	// can rebuild the exploration it lost rather than guessing at it from
	// the catalog as it stands today. Empty means every repository, exactly
	// as it does on the commission itself.
	Repositories []string `json:"repositories,omitempty"`
	// Effects the commission authorized beyond reading. Kept for the same
	// reason as Repositories: rebuilding a step's permission on resume has
	// to match what was actually granted, not what a fresh commission would
	// default to.
	Effects []contract.Effect `json:"effects,omitempty"`
	// BudgetUSD is the grant this commission opened with, not what is left
	// of it. What remains is this minus what the steps on file were
	// charged, so this number plus the receipt is the whole story.
	BudgetUSD float64 `json:"budget_usd"`
	// ContractVersion is stamped once, when the run opens. Resuming checks
	// it the same way a peer adapter is checked: the core continuing this
	// receipt has to still understand the shape the one that opened it used.
	ContractVersion string    `json:"contract_version,omitempty"`
	Started         time.Time `json:"started"`
	Updated         time.Time `json:"updated"`
	Closed          bool      `json:"closed"`
	Verdict         string    `json:"verdict"`
	// Plan is the graph as currently known. A resumed run dispatches
	// straight from this instead of replanning: once a step's payload is
	// computed it is fixed here, because recomputing it would mean reading
	// what a step this plan depends on discovered, and that answer only
	// lives in memory for as long as the process that found it stays up.
	Plan  contract.Plan `json:"plan"`
	Steps []StepState   `json:"steps"`
}

// The two values Kind takes.
const (
	KindTask = "task"
	KindAsk  = "ask"
)

// StepState is one node of the plan as it stood when the dump was taken.
type StepState struct {
	ID             string `json:"id"`
	Capability     string `json:"capability"`
	Repository     string `json:"repository"`
	Implementation string `json:"implementation,omitempty"`
	Verdict        string `json:"verdict"`
	Review         string `json:"review,omitempty"`
	Failure        string `json:"failure,omitempty"`
	// Raw is the provider's own text behind Failure, kept for the same
	// reason Failure itself is: a receipt that summarizes what happened
	// without keeping the evidence is not a receipt a human can act on.
	Raw string `json:"raw,omitempty"`
	// Discoveries is what the far side reported finding, carried over from
	// contract.Outcome. A step that already passed review is never
	// redispatched on resume, so this is the only copy of what it found that
	// survives the process that ran it -- without it, a crash between two
	// steps would cost the discoveries of every step that had already
	// closed, not just the ones still to come.
	Discoveries []contract.Discovery `json:"discoveries,omitempty"`
	DurationMS  int64                `json:"duration_ms"`
	// SpentUSD is what this step was charged, when anything was. It is here
	// and not in the measurement base on purpose: the base ranks providers
	// and money must never rank, but a receipt with no price on it is not a
	// receipt. Omitted when zero so free work does not carry a price tag.
	SpentUSD float64 `json:"spent_usd,omitempty"`
	// OverspendUSD is how far this step's charge ran past the share it was
	// granted, when it ran past it at all. Omitted when it stayed inside its
	// share, which is the ordinary case: a receipt with every step reading
	// "$0 over" would be a receipt nobody reads, same reasoning as SpentUSD
	// above.
	OverspendUSD float64   `json:"overspend_usd,omitempty"`
	ClosedAt     time.Time `json:"closed_at"`
}

// DefaultDir is where runs are kept when the settings file says nothing.
func DefaultDir() string { return filepath.Join(platform.StateDir(), "runs") }

var runID = regexp.MustCompile(`^[0-9a-zA-Z._-]+$`)

// NewID mints a run id that sorts by time and does not collide between two
// Atenea processes started in the same second.
func NewID(now time.Time) string {
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// Unreachable in practice; a timestamp alone is still a usable id.
		return now.UTC().Format("20060102T150405")
	}
	return now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(suffix[:])
}

// Store is a directory of run files. It is safe for concurrent use: steps
// close in parallel and each one asks for a dump.
type Store struct {
	dir string
	mu  sync.Mutex
}

// New prepares the store. The directory itself is not created here: a core
// that never receives a commission should not leave a trail of empty folders
// on the machine. A store with an empty directory is disabled and silently
// drops every dump, which is what turning checkpointing off in the settings is
// supposed to feel like.
func New(dir string) (*Store, error) {
	return &Store{dir: dir}, nil
}

// Dir reports where runs are written, or the empty string when disabled.
func (s *Store) Dir() string { return s.dir }

// Enabled reports whether dumps actually reach the disk.
func (s *Store) Enabled() bool { return s.dir != "" }

// Save writes the run, replacing whatever was there before.
//
// The write goes to a temporary file and is renamed into place, because a dump
// interrupted halfway is worse than no dump at all: it looks like a valid
// record of a run that never happened that way.
func (s *Store) Save(run Run) error {
	if !s.Enabled() {
		return nil
	}
	if !runID.MatchString(run.ID) {
		return contract.Fail(contract.FailureInvalidInput, "run id %q is not a safe file name", run.ID)
	}
	body, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput, "run %s: %v", run.ID, err)
	}
	body = append(body, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return diskFailure(err, "checkpoint directory %s: %v", s.dir, err)
	}
	final := filepath.Join(s.dir, run.ID+".json")
	temp, err := os.CreateTemp(s.dir, run.ID+".*.tmp")
	if err != nil {
		return diskFailure(err, "run %s: %v", run.ID, err)
	}
	name := temp.Name()
	// The temporary file leaves this function in exactly one shape: renamed
	// into place. Every other exit takes it with it, so an interrupted dump
	// never leaves a half-written record lying next to the real ones.
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(name)
		}
	}()

	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return diskFailure(err, "run %s: %v", run.ID, err)
	}
	if err := temp.Close(); err != nil {
		return diskFailure(err, "run %s: %v", run.ID, err)
	}
	if err := os.Rename(name, final); err != nil {
		return diskFailure(err, "run %s: %v", run.ID, err)
	}
	renamed = true
	return nil
}

// diskFailure describes a filesystem write that did not happen.
//
// The bin stays permission_denied. A full disk is not the provider's doing and
// not the caller's either, and the health record exempts this bin for exactly
// that reason (internal/metrics/baseline.go), so promoting it to a bin of its
// own would start condemning providers for the state of the machine. What has
// to change is the sentence: measured on a filled disk, a torn dump reported
// `permission_denied` with the ENOSPC text trailing at the end of a line about
// a run id, and the first place anybody takes permission_denied is `ls -l`.
func diskFailure(err error, format string, args ...any) error {
	if errors.Is(err, syscall.ENOSPC) {
		return contract.Fail(contract.FailurePermissionDenied,
			"no space left on device: "+format, args...)
	}
	return contract.Fail(contract.FailurePermissionDenied, format, args...)
}

// Load reads one run back.
func (s *Store) Load(id string) (Run, error) {
	if !s.Enabled() {
		return Run{}, contract.Fail(contract.FailureNotFound, "checkpointing is off")
	}
	if !runID.MatchString(id) {
		return Run{}, contract.Fail(contract.FailureInvalidInput, "run id %q is not a safe file name", id)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		// A receipt torn by an ugly close is set aside as .json.torn rather
		// than deleted, precisely so somebody can read what was lost.
		// Reporting the missing .json sends that reader to a file that was
		// never there and says nothing about the one that is. Measured:
		// `atenea resume <id>` on a torn receipt answered "no such file or
		// directory" with the evidence sitting beside it in the same
		// directory.
		if errors.Is(err, fs.ErrNotExist) {
			aside := filepath.Join(s.dir, id+".json.torn")
			if _, statErr := os.Stat(aside); statErr == nil {
				return Run{}, contract.Fail(contract.FailureNotFound,
					"run %s was torn by an ugly close and cannot be resumed; what survived it is at %s",
					id, aside)
			}
		}
		return Run{}, contract.Fail(contract.FailureNotFound, "run %s: %v", id, err)
	}
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return Run{}, contract.Fail(contract.FailureInvalidInput, "run %s: %v", id, err)
	}
	return run, nil
}

// List returns the run ids on disk, oldest first because the ids sort by time.
func (s *Store) List() ([]string, error) {
	if !s.Enabled() {
		return nil, nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, contract.Fail(contract.FailureNotFound, "checkpoint directory %s: %v", s.dir, err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		ids = append(ids, name[:len(name)-len(".json")])
	}
	return ids, nil
}
