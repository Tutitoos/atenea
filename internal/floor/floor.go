// Package floor stores what starting a single turn already costs, before any
// file is read and before a step has done anything: the tokens the CLI
// spends writing its system prompt and tool schemas into cache, priced in
// dollars.
//
// A row here is a cache of a measurement, not a settings file. Nobody edits
// it by hand -- there is no field a person would know the right value for --
// and every row records what produced it (CLIVersion, MeasuredAt) because the
// number is neither a constant nor stable: it is per repository (tool
// schemas differ by what a repository's providers expose) and per model, and
// it drifts every time the CLI's system prompt or its tool definitions
// change. The only way a row changes is by measuring again with
// `atenea floor measure`, which replaces it outright -- there is no partial
// update, because a figure half from one CLI version and half from another
// would not be a measurement of anything.
package floor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Measurement is what one real turn cost, spent once to find out what a turn
// costs on one repository for one model.
type Measurement struct {
	Repository       string    `json:"repository"`
	Model            string    `json:"model"`
	USD              float64   `json:"usd"`
	CacheWriteTokens int       `json:"cache_write_tokens"`
	InputTokens      int       `json:"input_tokens"`
	OutputTokens     int       `json:"output_tokens"`
	CLIVersion       string    `json:"cli_version"`
	MeasuredAt       time.Time `json:"measured_at"`
}

// DefaultPath is where the cache lives when nothing overrides it: beside
// everything else Atenea has learned about this machine.
func DefaultPath() string {
	return filepath.Join(platform.StateDir(), "floors.json")
}

// Store is a JSON file of measurements, one row per (repository, model) pair.
//
// It holds no open handle and no in-memory copy between calls: every method
// reads the file, does its work and, for a write, renames a replacement into
// place before returning. Nothing here outlives one call of `atenea floor`,
// one call of `atenea floor measure`, or one refusal check inside a workflow
// step -- a handle any of those would have to remember to close is a handle
// this package does not need.
type Store struct {
	path string
}

// Open returns a Store backed by path, or DefaultPath() when path is empty.
// Nothing is read yet: a file that does not exist is not a fault until
// something asks what is in it, and even then it answers "nothing measured
// yet" rather than an error.
func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	return &Store{path: path}, nil
}

// Get returns the stored measurement for one (repository, model) pair, and
// whether one exists. A pair nobody has measured -- including every pair on a
// machine whose cache file does not exist yet -- is not an error: it is the
// ordinary state of "the check is off for this pair", and a caller (the
// workflow engine's refusal included) is expected to treat it that way rather
// than fail the operation it is guarding.
func (s *Store) Get(repository, model string) (Measurement, bool, error) {
	rows, err := s.load()
	if err != nil {
		return Measurement{}, false, err
	}
	for _, m := range rows {
		if m.Repository == repository && m.Model == model {
			return m, true, nil
		}
	}
	return Measurement{}, false, nil
}

// Put stores m, replacing whatever was measured before for the same
// (repository, model) and leaving every other row untouched.
func (s *Store) Put(m Measurement) error {
	rows, err := s.load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range rows {
		if rows[i].Repository == m.Repository && rows[i].Model == m.Model {
			rows[i] = m
			replaced = true
			break
		}
	}
	if !replaced {
		rows = append(rows, m)
	}
	sortMeasurements(rows)
	return s.write(rows)
}

// List returns every stored measurement, sorted by repository then model so
// two readings of the same file always print in the same order.
func (s *Store) List() ([]Measurement, error) {
	rows, err := s.load()
	if err != nil {
		return nil, err
	}
	sortMeasurements(rows)
	return rows, nil
}

func sortMeasurements(rows []Measurement) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Repository != rows[j].Repository {
			return rows[i].Repository < rows[j].Repository
		}
		return rows[i].Model < rows[j].Model
	})
}

// load reads every row on disk. A file that has never been written reads as
// zero rows, not an error -- Open promised as much. A file that exists but is
// not the JSON this package writes is refused by name: silently treating it
// as empty would erase whatever a person or another tool put there, and
// silently reading garbage into a Measurement would print a floor nobody
// measured.
func (s *Store) load() ([]Measurement, error) {
	body, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, contract.Fail(contract.FailureUnavailable,
			"floor: cannot read %s: %v", s.path, err)
	}
	var rows []Measurement
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"floor: %s is not a measurement file Atenea wrote: %v", s.path, err)
	}
	return rows, nil
}

// write replaces the file with rows, atomically: a temporary file in the same
// directory, written and closed in full, then renamed over the target. A
// dump interrupted halfway through a plain os.WriteFile would leave a
// half-written floors.json that the next read refuses as corrupt, which is
// worse than losing the write outright -- it takes every row down with it,
// including ones this call never touched.
func (s *Store) write(rows []Measurement) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return contract.Fail(contract.FailureUnavailable,
			"floor: cannot create %s: %v", dir, err)
	}
	body, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return contract.Fail(contract.FailureInvalidInput, "floor: %v", err)
	}
	body = append(body, '\n')

	temp, err := os.CreateTemp(dir, filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "floor: %v", err)
	}
	name := temp.Name()
	// Every exit past this point either renames the temp file into place or
	// removes it: nothing that calls write leaves a stray *.tmp beside
	// floors.json for the next Store to trip over.
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(name)
		}
	}()

	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return contract.Fail(contract.FailureUnavailable, "floor: %v", err)
	}
	if err := temp.Close(); err != nil {
		return contract.Fail(contract.FailureUnavailable, "floor: %v", err)
	}
	// os.CreateTemp makes the file 0600; floors.json is a cache an operator
	// may reasonably want to cat, not a secret.
	if err := os.Chmod(name, 0o644); err != nil {
		return contract.Fail(contract.FailureUnavailable, "floor: %v", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return contract.Fail(contract.FailureUnavailable, "floor: %v", err)
	}
	renamed = true
	return nil
}
