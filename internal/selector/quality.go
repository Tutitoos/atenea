package selector

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// QualityObservation is an outcome-backed quality sample. Declared, wired and
// connected states never create one: only a dispatched, validated outcome does.
type QualityObservation struct {
	Capability     string
	Repository     string
	RepositoryRoot string
	Implementation string
	Language       string
	ToolVersion    string
	Instance       string
	ConfigDigest   string
	Samples        int
	// Total is every validated invocation, including failures. Samples is kept
	// as the compatibility alias used by older callers and is always kept in
	// step with Total by the book.
	Total          int
	ValidCount     int
	AcceptedCount  int
	CompleteCount  int
	PartialCount   int
	TruncatedCount int
	FailureCount   int
	Accepted       bool
	Complete       bool
	Truncated      bool
	OutOfScope     int
	ValidOutcome   bool
	Validated      bool
	Score          float64
	ObservedAt     time.Time
}

// Tested is part of ATENEA's public orchestration contract.
func (q QualityObservation) Tested(minSamples int) bool {
	if minSamples < 1 {
		minSamples = 1
	}
	total := q.Total
	if total == 0 {
		total = q.Samples
	}
	complete := q.CompleteCount
	if complete == 0 && q.Complete {
		complete = 1
	}
	valid := q.ValidCount
	if valid == 0 && q.ValidOutcome {
		valid = 1
	}
	return total >= minSamples && complete > 0 && valid > 0 && (q.Validated || q.ValidOutcome || q.Samples >= minSamples)
}

// Observe turns one provider result into a validated sample. Failures and
// partial answers count toward the denominator but never alter health or remove
// a provider from the funnel; they lower only the explainable quality ratio.
func Observe(capability, repository, implementation, language, toolVersion string, out contract.Outcome, runErr error) QualityObservation {
	valid := runErr == nil && out.Verdict == contract.VerdictOK && out.Result != nil
	validated := runErr != nil || out.Verdict != 0 || out.Result != nil
	structuralPartial := contract.StructuralPartial(out.Result)
	// Category counters describe validated successful results only. A failed
	// provider may still return a diagnostic payload, but it must contribute
	// solely to FailureCount rather than being classified as complete/partial.
	complete := valid && !structuralPartial
	truncated := valid && structuralPartial
	for _, evidence := range out.Evidence {
		if valid && evidence.Truncated {
			truncated = true
		}
		if valid && evidence.Completeness != "" && !strings.EqualFold(evidence.Completeness, "complete") {
			complete = false
		}
	}
	if truncated {
		complete = false
	}
	accepted := valid && complete && !truncated && out.OutOfScope == 0
	providerFailure := runErr != nil || out.Verdict != contract.VerdictOK || out.Result == nil
	score := 0.0
	if accepted {
		score = 1
	}
	return QualityObservation{
		Capability: capability, Repository: repository, Implementation: implementation,
		Language: language, ToolVersion: toolVersion, Samples: 1, Total: 1,
		ValidCount: boolInt(valid), AcceptedCount: boolInt(accepted), CompleteCount: boolInt(complete),
		PartialCount:   boolInt(valid && !complete),
		TruncatedCount: boolInt(truncated), FailureCount: boolInt(providerFailure),
		Accepted: accepted, Complete: complete, Truncated: truncated,
		OutOfScope: out.OutOfScope, ValidOutcome: valid, Validated: validated, Score: score,
		ObservedAt: time.Now(),
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

type qualityKey struct{ capability, repository, implementation, language, version, instance, configDigest string }

// QualityBook aggregates validated observations by capability, repository,
// language, implementation and observed tool version.
type QualityBook struct {
	mu      sync.RWMutex
	byKey   map[qualityKey]QualityObservation
	pending map[qualityKey]QualityObservation
	minimum int
	path    string
	loadErr error
}

// NewQualityBook is part of ATENEA's public orchestration contract.
func NewQualityBook(minimum int) *QualityBook {
	return NewQualityBookWithPath(minimum, "")
}

// NewQualityBookWithPath opens the durable quality ledger when path is set.
// The selector remains usable in tests and embedded callers with an empty
// path; Core supplies a state-root path so a restart retains the evidence.
func NewQualityBookWithPath(minimum int, path string) *QualityBook {
	if minimum < 1 {
		minimum = 2
	}
	b := &QualityBook{byKey: make(map[qualityKey]QualityObservation), pending: make(map[qualityKey]QualityObservation), minimum: minimum, path: path}
	if path != "" {
		b.loadErr = b.load()
	}
	return b
}

// OpenQualityBookWithPath is the strict constructor used by production
// selectors. A corrupt or unreadable ledger is an initialization error; an
// empty in-memory book must never be presented as valid history.
func OpenQualityBookWithPath(minimum int, path string) (*QualityBook, error) {
	b := NewQualityBookWithPath(minimum, path)
	if b.loadErr != nil {
		return nil, b.loadErr
	}
	return b, nil
}

// LoadError exposes a compatibility-safe diagnostic for callers that still
// use NewQualityBookWithPath.
func (b *QualityBook) LoadError() error {
	if b == nil {
		return nil
	}
	return b.loadErr
}

// Record is part of ATENEA's public orchestration contract.
func (b *QualityBook) Record(q QualityObservation) error {
	if b == nil || q.Capability == "" || q.Repository == "" || q.Implementation == "" || (!q.ValidOutcome && !q.Validated) {
		return nil
	}
	repository := canonicalRepositoryRoot(q.RepositoryRoot)
	if repository == "" {
		repository = canonicalRepositoryRoot(q.Repository)
	}
	q.RepositoryRoot = repository
	k := qualityKey{q.Capability, repository, q.Implementation, q.Language, q.ToolVersion, q.Instance, q.ConfigDigest}
	b.mu.Lock()
	defer b.mu.Unlock()
	old := b.byKey[k]
	old.normalize()
	q.normalize()
	q.ObservedAt = time.Now()
	b.byKey[k] = mergeQuality(old, q)
	b.pending[k] = mergeQuality(b.pending[k], q)
	return b.persistLocked()
}

func mergeQuality(a, b QualityObservation) QualityObservation {
	if a.Capability == "" {
		a = b
		a.normalize()
		return a
	}
	b.normalize()
	a.Total += b.Total
	a.Samples = a.Total
	a.AcceptedCount += b.AcceptedCount
	a.ValidCount += b.ValidCount
	a.CompleteCount += b.CompleteCount
	a.PartialCount += b.PartialCount
	a.TruncatedCount += b.TruncatedCount
	a.FailureCount += b.FailureCount
	a.OutOfScope += b.OutOfScope
	a.Accepted = a.AcceptedCount == a.Total && a.Total > 0
	a.Complete = a.CompleteCount == a.Total && a.Total > 0
	a.Truncated = a.TruncatedCount > 0
	a.ValidOutcome = a.ValidCount > 0
	a.Validated = a.Total > 0
	a.Score = float64(a.AcceptedCount) / float64(a.Total)
	if b.ObservedAt.After(a.ObservedAt) {
		a.ObservedAt = b.ObservedAt
	}
	return a
}

func (q *QualityObservation) normalize() {
	if q.Total == 0 {
		q.Total = q.Samples
	}
	if q.Samples == 0 {
		q.Samples = q.Total
	}
	if q.Total == 1 && q.AcceptedCount == 0 && q.Accepted {
		q.AcceptedCount = 1
	}
	if q.ValidCount == 0 && q.ValidOutcome {
		q.ValidCount = 1
	}
	if q.Total == 1 && q.CompleteCount == 0 && q.Complete {
		q.CompleteCount = 1
	}
	if q.Total == 1 && q.TruncatedCount == 0 && q.Truncated {
		q.TruncatedCount = 1
	}
	if q.PartialCount == 0 && q.ValidCount > 0 && q.CompleteCount == 0 {
		q.PartialCount = q.ValidCount
	}
	q.ValidOutcome = q.ValidCount > 0
	q.Validated = q.Total > 0
}

// Snapshot is part of ATENEA's public orchestration contract.
func (b *QualityBook) Snapshot(capability, repository, implementation, language string) (QualityObservation, bool) {
	if b == nil {
		return QualityObservation{}, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	var best QualityObservation
	found := false
	repository = canonicalRepositoryRoot(repository)
	for k, q := range b.byKey {
		if k.capability != capability || k.repository != repository || k.implementation != implementation || (language != "" && k.language != language) {
			continue
		}
		if !found || q.Samples > best.Samples || (q.Samples == best.Samples && q.ObservedAt.After(best.ObservedAt)) {
			best, found = q, true
		}
	}
	return best, found
}

// SnapshotVersion returns only the currently observed tool version. An empty
// version is intentionally not substituted with the version having the most
// samples: that would make an old binary's quality certify a new one.
func (b *QualityBook) SnapshotVersion(capability, repository, implementation, language, version string, instances ...string) (QualityObservation, bool) {
	if b == nil {
		return QualityObservation{}, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	instance := ""
	if len(instances) > 0 {
		instance = instances[0]
	}
	configDigest := ""
	if len(instances) > 1 {
		configDigest = instances[1]
	}
	repository = canonicalRepositoryRoot(repository)
	q, ok := b.byKey[qualityKey{capability, repository, implementation, language, version, instance, configDigest}]
	return q, ok
}

func (b *QualityBook) load() error {
	rows, err := readQualityRows(b.path)
	if err != nil {
		return err
	}
	for k, q := range rows {
		b.byKey[k] = q
	}
	return nil
}

func (b *QualityBook) persistLocked() error {
	if b.path == "" {
		return nil
	}
	unlock, err := acquireQualityFileLock(b.path)
	if err != nil {
		return err
	}
	defer unlock()
	disk, err := readQualityRows(b.path)
	if err != nil {
		return err
	}
	for k, delta := range b.pending {
		disk[k] = mergeQuality(disk[k], delta)
	}
	rows := make([]QualityObservation, 0, len(disk))
	for k, q := range disk {
		b.byKey[k] = q
		rows = append(rows, q)
	}
	// Stable ordering makes the durable artifact reviewable and avoids noisy
	// rewrites across restarts.
	slices.SortFunc(rows, func(a, c QualityObservation) int {
		return strings.Compare(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", a.Capability, a.Repository, a.Implementation, a.Language, a.ToolVersion, a.Instance, a.ConfigDigest), fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", c.Capability, c.Repository, c.Implementation, c.Language, c.ToolVersion, c.Instance, c.ConfigDigest))
	})
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o750); err != nil {
		return err
	}
	dir := filepath.Dir(b.path)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(b.path)+".tmp-")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := tmpFile.Chmod(0o640); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if _, err := tmpFile.Write(append(data, '\n')); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, b.path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	clear(b.pending)
	return nil
}

func readQualityRows(path string) (map[qualityKey]QualityObservation, error) {
	out := make(map[qualityKey]QualityObservation)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	var rows []QualityObservation
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	for _, q := range rows {
		// These fields are derived but their explicit false values on a
		// non-empty current row are corruption, not a migration case. Check
		// before normalize can repair legacy omissions.
		if q.Total > 0 && !q.Validated {
			return nil, errors.New("quality ledger: validated flag disagrees with total")
		}
		if q.ValidCount > 0 && !q.ValidOutcome {
			return nil, errors.New("quality ledger: valid outcome flag disagrees with valid count")
		}
		q.normalize()
		if err := validateQualityObservation(q); err != nil {
			return nil, fmt.Errorf("quality ledger: %w", err)
		}
		repository := canonicalRepositoryRoot(q.RepositoryRoot)
		if repository == "" {
			repository = canonicalRepositoryRoot(q.Repository)
		}
		if q.Capability == "" || q.Implementation == "" || repository == "" {
			return nil, errors.New("quality ledger: row has incomplete identity")
		}
		q.RepositoryRoot = repository
		key := qualityKey{q.Capability, repository, q.Implementation, q.Language, q.ToolVersion, q.Instance, q.ConfigDigest}
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("quality ledger: duplicate row for %s/%s/%s", q.Capability, q.Implementation, repository)
		}
		// Score is derived state. Never let a stale or manipulated persisted
		// value influence ranking after the structural counters are validated.
		q.Score = float64(q.AcceptedCount) / float64(q.Total)
		out[key] = q
	}
	return out, nil
}

func validateQualityObservation(q QualityObservation) error {
	if q.Total <= 0 {
		return fmt.Errorf("total %d must be positive", q.Total)
	}
	if q.Total < 0 || q.Samples < 0 || q.ValidCount < 0 || q.AcceptedCount < 0 || q.CompleteCount < 0 || q.PartialCount < 0 || q.TruncatedCount < 0 || q.FailureCount < 0 || q.OutOfScope < 0 {
		return errors.New("negative quality counter")
	}
	if q.Samples != q.Total {
		return fmt.Errorf("samples %d does not equal total %d", q.Samples, q.Total)
	}
	for name, count := range map[string]int{
		"valid": q.ValidCount, "accepted": q.AcceptedCount, "complete": q.CompleteCount,
		"partial": q.PartialCount, "truncated": q.TruncatedCount, "failures": q.FailureCount,
	} {
		if count > q.Total {
			return fmt.Errorf("%s count %d exceeds total %d", name, count, q.Total)
		}
	}
	if q.AcceptedCount > q.ValidCount || q.AcceptedCount > q.CompleteCount {
		return fmt.Errorf("accepted count %d exceeds valid/complete counts %d/%d", q.AcceptedCount, q.ValidCount, q.CompleteCount)
	}
	if q.ValidCount+q.FailureCount != q.Total {
		return fmt.Errorf("valid plus failure counts %d/%d do not equal total %d", q.ValidCount, q.FailureCount, q.Total)
	}
	if q.CompleteCount+q.PartialCount != q.ValidCount {
		return fmt.Errorf("complete plus partial counts %d/%d do not equal valid count %d", q.CompleteCount, q.PartialCount, q.ValidCount)
	}
	if q.TruncatedCount > q.ValidCount {
		return fmt.Errorf("truncated count %d exceeds valid count %d", q.TruncatedCount, q.ValidCount)
	}
	if q.TruncatedCount > q.PartialCount {
		return fmt.Errorf("truncated count %d exceeds partial count %d", q.TruncatedCount, q.PartialCount)
	}
	if !math.IsNaN(q.Score) && !math.IsInf(q.Score, 0) {
		if q.Score < 0 || q.Score > 1 {
			return fmt.Errorf("score %v is outside [0,1]", q.Score)
		}
	} else {
		return fmt.Errorf("score %v is not finite", q.Score)
	}
	expectedAccepted := q.AcceptedCount == q.Total && q.Total > 0
	expectedComplete := q.CompleteCount == q.Total && q.Total > 0
	expectedTruncated := q.TruncatedCount > 0
	if q.Accepted != expectedAccepted || q.Complete != expectedComplete || q.Truncated != expectedTruncated {
		return errors.New("quality summary flags disagree with counters")
	}
	return nil
}

// canonicalRepositoryRoot is the sole scope-key normalizer. Repository IDs
// remain display metadata; quality evidence is isolated by the physical root
// so relative paths, absolute paths and symlinks address the same checkout.
func canonicalRepositoryRoot(repository string) string {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return ""
	}
	root, err := filepath.Abs(repository)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		if absolute, err := filepath.Abs(resolved); err == nil {
			return absolute
		}
	}
	return filepath.Clean(root)
}

func acquireQualityFileLock(path string) (func(), error) {
	lockPath := path + ".lock"
	deadline := time.Now().Add(2 * time.Second)
	for {
		unlock, err := pidlock.Claim(lockPath)
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, pidlock.ErrHeld) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// MinimumSamples is part of ATENEA's public orchestration contract.
func (b *QualityBook) MinimumSamples() int {
	if b == nil || b.minimum < 1 {
		return 2
	}
	return b.minimum
}
