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
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/allowance"
	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Measurement is what one real turn cost, spent once to find out what a turn
// costs on one repository, for one agent, with one model.
//
// Agent is part of the key alongside Repository and Model, not a label
// beside it, because what a floor actually varies with is the tool surface
// a turn starts with -- and that surface is fixed by which built-in agent
// is running, not by which model answers it. Measured 2026-08-14, same
// repository, same model (claude-opus-5): agent explore (Atenea's MCP
// tools plus Read and Glob) cost $0.28, 27,666 cache-write tokens; agent
// plan (no tools at all) cost $0.06, 4,991 cache-write tokens -- 81% of
// the $0.28 floor is the tool definitions written into cache before the
// model reads a single file. Keying on (Repository, Model) alone let a
// measurement of plan silently replace the row for explore: the two
// shared a key, so the cheap figure ended up governing the expensive
// steps it was supposed to catch.
//
// A row whose Agent is empty predates this field and does not describe
// any agent that exists today: see Store.Get and Store.List for how it is
// treated as unmeasured rather than guessed at.
type Measurement struct {
	Repository string `json:"repository"`
	Agent      string `json:"agent"`
	Model      string `json:"model"`

	// USD is the cold-equivalent cost of starting a turn on this
	// (repository, agent, model) triple -- PrefixTokens * USDPerToken --
	// not whatever a single receipt happened to say Anthropic billed for
	// the turn that produced this row. The two disagree in exactly the
	// case that matters: measured 2026-08-14/15 on taxiprime-backend,
	// claude-opus-5, agent explore, cold -- the CLI wrote 26,603 tokens to
	// cache and the receipt said $0.27, giving $1.0149e-5 per token
	// ($0.27 / 26,603). Warm an hour later, the same tool surface read
	// 23,278 tokens from cache and wrote only 3,325 -- the same 26,603
	// total, split differently -- and that turn's own receipt said $0.01,
	// because Anthropic bills a cache read far below a cache write.
	// Pricing the same 26,603-token total at the stored rate reproduces
	// $0.27, the number a caller deciding "can this turn afford to start
	// cold" actually needs; recording the $0.01 receipt instead would say
	// the floor had dropped when nothing but cache state had changed.
	USD float64 `json:"usd"`

	// PrefixTokens is the cache-write and cache-read tokens this
	// measurement's turn spent together: the total size of what the CLI
	// puts in front of the model before it does anything, regardless of
	// cache state. It is the invariant the USD doc above demonstrates --
	// 26,603 cold (all write) or 23,278 + 3,325 warm (mostly read) -- and
	// it is what USDPerToken converts into USD.
	PrefixTokens int `json:"prefix_tokens"`

	// USDPerToken is the rate PrefixTokens is priced at. For a cold row
	// (Cold == true) it is derived from this row's own receipt: USD /
	// PrefixTokens at measurement time, $1.0149e-5 in the example above.
	// For a warm row it is copied from PriceForModel -- the most recent
	// cold rate on record for the same model -- because a warm receipt's
	// own price-per-token is not the cold price and using it would
	// silently mix the two.
	USDPerToken float64 `json:"usd_per_token"`

	CacheWriteTokens int `json:"cache_write_tokens"`
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`

	// FirstCallTokens is the block that arrives with a turn's FIRST TOOL
	// CALL, written or read -- cache-state invariant for the same reason
	// PrefixTokens is, and measured off the same single run, message by
	// message. See model.Client.FirstCall.
	//
	// Zero means no probe has priced this row's first tool call: either the
	// row predates the measurement, or the agent type carries no tools and
	// has no first call to price. The two are told apart by whether the
	// surface has tools at all, never by this field, and a caller that needs
	// the distinction asks the settings file rather than guessing here.
	FirstCallTokens int `json:"first_call_tokens"`

	// Cold reports whether the turn that produced this row was a
	// cache-write measurement (CacheReadTokens == 0 upstream) rather than
	// a cache-read one. Only a cold row's USDPerToken was derived from its
	// own receipt rather than borrowed from PriceForModel, which is why
	// PriceForModel considers cold rows only -- chaining a warm row's rate
	// into a later warm row would drift away from any real measurement.
	// A row with USD == 0 and PrefixTokens == 0 is a legitimate cold
	// reading, not an unmeasured one: reviewer and filereader call no
	// model at all, so a zero floor is what measuring them correctly
	// produces, and Get must hand it back rather than treat zero as
	// absence.
	Cold bool `json:"cold"`

	CLIVersion string    `json:"cli_version"`
	MeasuredAt time.Time `json:"measured_at"`
}

// Prefix is PrefixTokens, falling back to CacheWriteTokens when it is zero.
// A row Put before PrefixTokens existed carries the same number in
// CacheWriteTokens alone -- see the printing path in cmd/atenea/floor.go's
// floorList, which already treats that case specially -- and the row on
// this machine for taxiprime-backend/explore is exactly one of them
// (prefix_tokens: 0, cache_write_tokens: 26603).
func (m Measurement) Prefix() int {
	if m.PrefixTokens != 0 {
		return m.PrefixTokens
	}
	return m.CacheWriteTokens
}

// StartWeight is the input-equivalent weight of everything this measurement's
// turn pays for before it reads anything of its own -- its prefix and the
// block its first tool call brings -- priced cold, as a cache write. See
// allowance.StartWeight for the arithmetic and the receipts.
//
// It reads StartTokens rather than Prefix, which is the whole point: a row
// that has been probed answers 7.4x what its prefix alone would say, and the
// prefix alone was never the quantity that stopped a step from reading.
//
// Zero means this row carries no token count at all, so the weight is
// unknown -- not the same as a weight of zero, which would mean a turn that
// costs nothing to start. A caller must refuse rather than pass a zero
// weight on as an answer.
func (m Measurement) StartWeight() int {
	return allowance.StartWeight(m.StartTokens(), m.InputTokens, m.OutputTokens)
}

// WarmStartWeight is the same start once this machine's cache holds both
// blocks -- the per-step reading, twenty times smaller than StartWeight's
// one-time cold one. Both come off the same stored counts, which are
// cache-state invariant by construction: see PrefixTokens and FirstCallTokens,
// and allowance.WarmStartWeight for the matched runs that measured the split.
//
// Zero means the same thing it means above: no token count on this row, so
// the weight is unknown rather than free, and a caller must refuse rather
// than pass it on.
func (m Measurement) WarmStartWeight() int {
	return allowance.WarmStartWeight(m.StartTokens(), m.InputTokens, m.OutputTokens)
}

// StartTokens is everything a step pays for before it does any work of its
// own: the prefix that arrives with the prompt plus the block that arrives
// with its first tool call. Both counts are cache-state invariant, so this
// total is too, and it is the quantity every price below is derived from.
//
// A row with no first-call measurement answers its prefix alone, which is
// what it was measured to be -- honest for an agent type with no tools, and
// an understatement for one whose first call nobody has priced yet. USD's own
// doc says which rows those are.
func (m Measurement) StartTokens() int {
	return m.Prefix() + m.FirstCallTokens
}

// ColdStartUSD is what ONE TURN that establishes the cache is billed: the
// prefix and the block arriving with the first tool call, both at cold price.
//
// For any row a FirstCall probe wrote this is not an estimate but the receipt
// that probe paid, recovered exactly. USD is defined as Prefix x USDPerToken
// and USDPerToken as receipt / StartTokens (see cmd/atenea.coldEquivalentUSD),
// so scaling USD back up by StartTokens/Prefix cancels the prefix and returns
// the receipt itself. Measured 2026-08-16 by paying for two: $0.2724 and
// $0.4477 against $0.2704 and $0.4464 priced lane by lane off the recovered
// list -- 1% on both.
//
// It exists because the stored USD is the PREFIX's slice of that receipt and
// reads, to anybody who has not read its doc, like the price of the turn. Two
// probes quoted off it were authorized at $0.31 and cost $1.09. Any line that
// tells a person what a probe is about to cost must use this figure.
//
// Zero when there is nothing to derive from -- no dollar figure, or no prefix
// -- and a caller must read that as unknown, never as free.
func (m Measurement) ColdStartUSD() float64 {
	prefix := m.Prefix()
	if m.USD <= 0 || prefix == 0 {
		return 0
	}
	return m.USD * float64(m.StartTokens()) / float64(prefix)
}

// WarmUSD is what starting a step costs on a machine whose cache already
// holds this prefix and this first-call block: the ordinary case, and the
// figure an admission rule should refuse a per-step share against.
//
// Derived from USD rather than from USDPerToken, deliberately: USD is the
// cold-equivalent price of the PREFIX, every row that carries a real dollar
// figure has one (some carry no rate at all -- taxiprime-backend/explore is
// priced from another row's), and scaling it by StartTokens/Prefix reaches
// the same answer with one fewer field that can be missing. Cross-checked
// 2026-08-15 against a live probe that made one tool call: the arithmetic
// says $0.47 cold, the receipt said $0.4935.
//
// Zero when there is nothing to derive from -- no dollar figure, or no
// prefix -- and a caller must read that as unknown, never as free.
func (m Measurement) WarmUSD() float64 {
	return allowance.WarmDiscount(m.ColdStartUSD())
}

// DefaultPath is where the cache lives when nothing overrides it: beside
// everything else Atenea has learned about this machine.
func DefaultPath() string {
	return filepath.Join(platform.StateDir(), "floors.json")
}

// Store is a JSON file of measurements, one row per (repository, agent,
// model) triple.
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

// putting serializes Put inside one process. It is package-level rather than
// a field on Store because Open hands out a fresh Store per call and holds
// nothing between them -- a mutex on the struct would be a different mutex
// for each of two callers writing the same file, which is no mutex at all.
// Writes are rare enough (one per `floor measure`) that one lock for every
// path costs nothing worth splitting it up for.
var putting sync.Mutex

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

// Get returns the stored measurement for one (repository, agent, model)
// triple, and whether one exists. A triple nobody has measured -- including
// every triple on a machine whose cache file does not exist yet, and every
// row Put before Agent became part of the key (see Measurement's own doc)
// -- is not an error: it is the ordinary state of "the check is off for
// this triple", and a caller (the workflow engine's refusal included) is
// expected to treat it that way rather than fail the operation it is
// guarding. A legacy row's Agent is "", never a real agent name, so it
// cannot match a query for "explore" or "plan" and Get reads it as
// unmeasured without any special-casing here.
func (s *Store) Get(repository, agent, model string) (Measurement, bool, error) {
	rows, err := s.load()
	if err != nil {
		return Measurement{}, false, err
	}
	for _, m := range rows {
		if m.Repository == repository && m.Agent == agent && m.Model == model {
			return m, true, nil
		}
	}
	return Measurement{}, false, nil
}

// PriceForModel returns the USD-per-token rate from the most recently
// measured cold row for model, across every repository and every agent,
// and whether one exists.
//
// The key is the model alone, not (repository, model) or (repository,
// agent, model): Get and Put key on the full triple because the *floor* --
// the tool surface, the token count a turn starts with -- varies with the
// repository's providers and the agent's capability set, but the *rate*
// that turns a token count into dollars is Anthropic's price for that
// model, the same number whichever repository or agent is asking. A warm
// reading on a repository nobody has ever measured cold still needs a
// rate to convert its tokens with, and the only sound source is a cold
// measurement of the same model taken anywhere -- there is nothing
// repository- or agent-specific to look up.
//
// Only cold rows are considered (see Measurement.Cold): a warm row's own
// USDPerToken was itself borrowed from an earlier PriceForModel call, and
// chaining through it would drift the rate away from any receipt that
// actually priced a token. Errors reading the store are reported as "no
// price on record" rather than surfaced, matching the two-value contract
// callers rely on to decide cleanly whether a rate exists.
func (s *Store) PriceForModel(model string) (usdPerToken float64, measuredAt time.Time, ok bool) {
	rows, err := s.load()
	if err != nil {
		return 0, time.Time{}, false
	}
	for _, m := range rows {
		if !m.Cold || m.Model != model {
			continue
		}
		if !ok || m.MeasuredAt.After(measuredAt) {
			usdPerToken = m.USDPerToken
			measuredAt = m.MeasuredAt
			ok = true
		}
	}
	return usdPerToken, measuredAt, ok
}

// Put stores m, replacing whatever was measured before for the same
// (repository, agent, model) triple and leaving every other row untouched.
//
// The whole read-modify-write is serialized, because "leaving every other row
// untouched" is a promise about the file and not about this process's copy of
// it. Two measurements finishing at once each read the file as it was before
// either of them, each added its own row, and each wrote the whole list back:
// the second rename won and the first row was gone, with no error anywhere to
// say a measurement somebody paid a real turn for had been dropped. The
// in-process lock covers the workflow engine and a `floor measure` in the
// same binary; the file lock covers two `atenea` processes, and it refuses
// rather than queues -- a measurement is a command somebody typed, and being
// told another one is running beats waiting for it in silence.
func (s *Store) Put(m Measurement) error {
	putting.Lock()
	defer putting.Unlock()
	release, err := pidlock.Claim(s.path + ".lock")
	switch {
	case errors.Is(err, pidlock.ErrHeld):
		return contract.Fail(contract.FailureUnavailable,
			"floor: another process is writing %s", s.path)
	case err != nil:
		return contract.Fail(contract.FailurePermissionDenied,
			"floor: locking %s: %v", s.path, err)
	}
	defer release()

	rows, err := s.load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range rows {
		if rows[i].Repository == m.Repository && rows[i].Agent == m.Agent && rows[i].Model == m.Model {
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

// List returns every stored measurement, sorted by repository then agent
// then model so two readings of the same file always print in the same
// order. A legacy row with no Agent sorts first within its repository
// (empty precedes every real agent name) rather than being dropped -- it is
// still on disk and still money somebody spent, so a caller (floorList) has
// to be able to point at it and say it needs re-measuring, not pretend it
// was never there.
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
		if rows[i].Agent != rows[j].Agent {
			return rows[i].Agent < rows[j].Agent
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
	// Renaming an unsynced file is atomic about the name and says nothing
	// about the contents: a machine that loses power after the rename can
	// come back with floors.json in place and empty, which the next load
	// refuses as a file Atenea did not write. registry.persistLocked already
	// pays this cost for the same file-shaped cache.
	if err := temp.Sync(); err != nil {
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
