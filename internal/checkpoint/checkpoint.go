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
	// Kind tells a task-shaped commission apart from a single ask or a
	// caller-supplied plan: task rebuilds explore-then-split, ask is one step,
	// and plan resumes directly from its supplied DAG.
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

// The four values Kind takes.
const (
	KindTask = "task"
	KindAsk  = "ask"
	// KindPlan is a caller-supplied multi-capability DAG. It resumes directly
	// from the graph on file; unlike KindTask it has no implicit exploration
	// phase to reconstruct.
	KindPlan = "plan"
	// KindRaw is a passthrough: one tool on a declared backend, forwarded
	// verbatim. It resumes like neither of the others because it does not
	// resume at all -- there is no plan to rebuild and no step to redispatch,
	// so a raw receipt is written once, already closed.
	KindRaw = "raw"
)

// StepState is one node of the plan as it stood when the dump was taken.
type StepState struct {
	ID string `json:"id"`
	// Capability is empty on a passthrough step, which is the shape of the
	// thing rather than a gap in it: a raw tool answers no capability, and
	// naming one here would be the first lie in a record kept for auditing.
	Capability     string `json:"capability,omitempty"`
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
	// SpentUSDKnown says whether the zero above is a price or an absence.
	//
	// `omitempty` on SpentUSD means a receipt for work through ripgrep and a
	// receipt for work whose provider reported nothing look identical on
	// disk: both simply have no price. This is the field that tells them
	// apart, and it is written only on the receipts that do know, so an old
	// receipt reads as "nobody said" -- which is what it is, since nothing
	// could say so when it was written.
	SpentUSDKnown bool `json:"spent_usd_known,omitempty"`
	// OverspendUSD is how far this step's charge ran past the share it was
	// granted, when it ran past it at all. Omitted when it stayed inside its
	// share, which is the ordinary case: a receipt with every step reading
	// "$0 over" would be a receipt nobody reads, same reasoning as SpentUSD
	// above.
	OverspendUSD float64 `json:"overspend_usd,omitempty"`
	// Funnel is who else was in the running, or the reason nobody was. See
	// the type: the field is written on every step from the release that
	// introduced it, so an absent one dates the receipt rather than
	// describing the step.
	Funnel   Funnel    `json:"funnel"`
	ClosedAt time.Time `json:"closed_at"`
}

// Funnel is what the selection did, kept on the step it decided.
//
// A receipt has always named the implementation that ran and never who else
// was in the running, which is enough to observe a live run and not enough to
// audit a past one. It is one field rather than a trace beside a flag because
// two of its three states are silences that would otherwise look identical: a
// step whose trace was not kept and a step that never had a funnel both read
// as an absent trace, and a reader cannot tell a missing record from a
// decision that never happened.
//
// A receipt written before this field existed carries none of it at all, which
// is a fourth state and an honest one: it says the run predates the record
// rather than claiming anything about its funnel.
type Funnel struct {
	State string `json:"state"`
	// Stages is the trace, and only FunnelKept carries one. The other two
	// states are about the absence of a trace, so a stage list beside them
	// would be a contradiction on the page.
	Stages []FunnelStage `json:"stages,omitempty"`
}

// The three states, and the whole point is that they are distinguishable.
const (
	// FunnelKept means the funnel ran and its trace is below.
	FunnelKept = "kept"
	// FunnelNotKept means a funnel decided this step and nothing recorded
	// how -- a step rebuilt from an older receipt, or one that never
	// dispatched and so never reached the selector.
	FunnelNotKept = "not_kept"
	// FunnelNone means there was no funnel to keep, because the step was a
	// passthrough to a declared backend: one provider, nothing to choose
	// between, and no decision that could have gone another way.
	FunnelNone = "none"
)

// FunnelStage is one narrowing, with what it threw out.
type FunnelStage struct {
	Name string `json:"name"`
	// In and Out are counts rather than the surviving names: the survivors
	// of the last stage are derivable from the drops and the chosen
	// implementation, and writing every candidate at every stage would grow
	// the file with a list that repeats itself four times.
	In      int          `json:"in"`
	Out     int          `json:"out"`
	Dropped []FunnelDrop `json:"dropped,omitempty"`
}

// FunnelDrop is one candidate that did not survive a stage, and why.
type FunnelDrop struct {
	Implementation string `json:"implementation"`
	Reason         string `json:"reason"`
	// Raw is the provider's own words when the drop came from a health
	// record rather than the core's own bookkeeping, kept for the same
	// reason StepState.Raw is: a summary without the evidence is not
	// something a human can act on.
	Raw string `json:"raw,omitempty"`
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
	run = sanitizedRun(run)
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
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return diskFailure(err, "run %s: syncing checkpoint: %v", run.ID, err)
	}
	if err := temp.Close(); err != nil {
		return diskFailure(err, "run %s: %v", run.ID, err)
	}
	if err := os.Rename(name, final); err != nil {
		return diskFailure(err, "run %s: %v", run.ID, err)
	}
	if err := syncDirectory(s.dir); err != nil {
		return diskFailure(err, "run %s: syncing checkpoint directory: %v", run.ID, err)
	}
	renamed = true
	return nil
}

// sanitizedRun copies the pieces that can carry provider output before the
// receipt is serialized. The caller keeps the original in-memory evidence;
// only the durable copy is redacted.
func sanitizedRun(run Run) Run {
	run.Steps = append([]StepState(nil), run.Steps...)
	for i := range run.Steps {
		step := &run.Steps[i]
		step.Raw = contract.RedactRaw(step.Raw)
		step.Funnel.Stages = append([]FunnelStage(nil), step.Funnel.Stages...)
		for j := range step.Funnel.Stages {
			stage := &step.Funnel.Stages[j]
			stage.Dropped = append([]FunnelDrop(nil), stage.Dropped...)
			for k := range stage.Dropped {
				stage.Dropped[k].Raw = contract.RedactRaw(stage.Dropped[k].Raw)
			}
		}
	}
	return run
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
		// A receipt that will not parse may have been torn by an ugly close even
		// though Save flushes the file before renaming it. Setting it aside is the
		// service's job, and only the service's, so on a machine where the service
		// has not started since
		// the cut nothing has done it yet -- and a parse error on its own is a
		// dead end. It names a real fault and no way out of it, which is the
		// same shape as reporting the missing .json above: honest, and no use
		// to the person reading it. One sentence closes it.
		return Run{}, contract.Fail(contract.FailureInvalidInput,
			"run %s was torn by an ugly close and cannot be resumed: %v; the service sets a torn receipt aside on its next start (atenea run), and what survived it will be at %s",
			id, err, filepath.Join(s.dir, id+".json.torn"))
	}
	return run, nil
}

// Prune removes closed receipts that stopped being updated before cutoff, and
// reports how many went.
//
// Closed only. An open receipt is a commission somebody may still resume --
// Candidates is built out of exactly those -- so removing one by age would
// take away the resume rather than the record of it. A run left open by a
// machine that died is recovered by Recover, which closes it with a reason;
// after that it ages like any other.
//
// Read before removed, and read fully. A receipt whose age is judged from its
// file mtime would be judged from whenever the disk last touched it -- a
// restore, a backup, an editor -- and Updated is the run's own answer. A file
// this store cannot read is left alone: an unreadable receipt is a thing to
// look at, not a thing to delete.
func (s *Store) Prune(cutoff time.Time) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	ids, err := s.List()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, id := range ids {
		run, err := s.Load(id)
		if err != nil || !run.Closed || !run.Updated.Before(cutoff) {
			continue
		}
		// Under the same lock a writer takes. A receipt being resumed right
		// now is one somebody is about to write to, and deleting it from
		// under them would turn a resume into a run with no record.
		release, err := s.Lock(id)
		if err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, id+".json")); err == nil {
			removed++
		}
		release()
	}
	if removed > 0 {
		// The directory entry has to reach disk too, or a power cut leaves
		// names pointing at files that are gone -- the same reasoning Save
		// gives for syncing after a write.
		_ = syncDirectory(s.dir)
	}
	return removed, nil
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
