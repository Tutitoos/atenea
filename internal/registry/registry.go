// Package registry holds Atenea's catalog: the capabilities it knows how to
// ask for, the implementations that answer them, and the repositories those
// implementations run against.
//
// It is deliberately a lookup table with validation, not a brain. Deciding
// which implementation to use is the selector's job.
package registry

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// Registry is safe for concurrent use: several chats can be open at once
// against the same core, and the catalog is shared between them.
type Registry struct {
	mu           sync.RWMutex
	capabilities map[string]contract.Capability
	implementers map[string]contract.Implementation
	repositories map[string]contract.Repository
	byCapability map[string][]string
	// observed holds what probing found, keyed by repository and then by
	// implementation. It is separate from the declared health on the
	// implementation itself because the two answer different questions: the
	// declaration is what the operator believes about the provider, and this
	// is what the last call against one repository actually got.
	observed map[string]map[string]contract.Health
	// indexOverrides stores probe results separately from the declared
	// repository copy so they can be restored before repositories are added.
	indexOverrides map[string]map[string]bool
	statePath      string
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		capabilities:   make(map[string]contract.Capability),
		implementers:   make(map[string]contract.Implementation),
		repositories:   make(map[string]contract.Repository),
		byCapability:   make(map[string][]string),
		observed:       make(map[string]map[string]contract.Health),
		indexOverrides: make(map[string]map[string]bool),
	}
}

type persistedState struct {
	Version   int                                   `json:"version"`
	Health    map[string]map[string]contract.Health `json:"health,omitempty"`
	IndexedBy map[string]map[string]bool            `json:"indexed_by,omitempty"`
}

// NewWithState restores runtime observations from a private state file.
// Settings remain authoritative for declarations; this file contains only
// probe evidence and may safely be removed to forget it.
func NewWithState(path string) (*Registry, error) {
	r := New()
	r.statePath = strings.TrimSpace(path)
	if r.statePath == "" {
		return r, nil
	}
	raw, err := os.ReadFile(r.statePath)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"registry state: reading %s: %v", r.statePath, err)
	}
	var state persistedState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, contract.Fail(contract.FailureUnavailable,
			"registry state: decoding %s: %v", r.statePath, err)
	}
	if state.Version != 0 && state.Version != 1 {
		return nil, contract.Fail(contract.FailureUnavailable,
			"registry state: unsupported version %d", state.Version)
	}
	if state.Health != nil {
		r.observed = state.Health
	}
	for repo, providers := range state.IndexedBy {
		r.indexOverrides[repo] = maps.Clone(providers)
	}
	return r, nil
}

// AddCapability registers a capability definition.
func (r *Registry) AddCapability(capability contract.Capability) error {
	if err := capability.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.capabilities[capability.ID]; exists {
		return contract.Fail(contract.FailureInvalidInput,
			"capability %s is already registered", capability.ID)
	}
	r.capabilities[capability.ID] = capability.Clone()
	return nil
}

// AddImplementation registers a provider for an already registered capability.
// Registering an implementation for an unknown capability is refused: an
// orphan implementation is a typo, and a typo that resolves to nothing is the
// kind of thing that only shows up when the selector mysteriously finds no
// candidates.
func (r *Registry) AddImplementation(impl contract.Implementation) error {
	if err := impl.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.implementers[impl.ID]; exists {
		return contract.Fail(contract.FailureInvalidInput,
			"implementation %s is already registered", impl.ID)
	}
	capability, known := r.capabilities[impl.Capability]
	if !known {
		return contract.Fail(contract.FailureInvalidInput,
			"implementation %s targets unknown capability %s", impl.ID, impl.Capability)
	}
	// A bound on an input the capability does not declare would never bind:
	// the funnel would read a name nothing sends, keep the implementation for
	// every request, and the narrowing the settings file asked for would
	// silently not exist. Only integers can be bounded, so a bound on any
	// other shape is the same mistake wearing a real name.
	for name := range impl.Constraints.MaxInput {
		field, found := inputNamed(capability, name)
		if !found {
			return contract.Fail(contract.FailureInvalidInput,
				"implementation %s: max_input names %q, which capability %s does not declare as an input",
				impl.ID, name, capability.ID)
		}
		if field.Type != contract.TypeInt {
			return contract.Fail(contract.FailureInvalidInput,
				"implementation %s: max_input bounds %q, which capability %s declares as %s, not int",
				impl.ID, name, capability.ID, field.Type)
		}
	}
	r.implementers[impl.ID] = impl.Clone()
	ids := append(r.byCapability[impl.Capability], impl.ID)
	slices.Sort(ids)
	r.byCapability[impl.Capability] = ids
	return nil
}

// inputNamed finds one of a capability's declared inputs by name.
func inputNamed(capability contract.Capability, name string) (contract.Field, bool) {
	for _, field := range capability.Inputs {
		if field.Name == name {
			return field, true
		}
	}
	return contract.Field{}, false
}

// AddRepository registers a unit of work.
func (r *Registry) AddRepository(repo contract.Repository) error {
	if err := repo.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.repositories[repo.ID]; exists {
		return contract.Fail(contract.FailureInvalidInput,
			"repository %s is already registered", repo.ID)
	}
	if overrides := r.indexOverrides[repo.ID]; len(overrides) > 0 {
		for provider, ready := range overrides {
			repo = repo.SetIndexed(provider, ready)
		}
	}
	r.repositories[repo.ID] = repo.Clone()
	return nil
}

// Capability looks one up by id. An unknown id that comes close to a real
// one names the near miss: a suggestion costs nothing to offer and saves
// whoever typed it a second round trip to find the same typo.
func (r *Registry) Capability(id string) (contract.Capability, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	capability, ok := r.capabilities[id]
	if !ok {
		if near, found := r.closestCapability(id); found {
			return contract.Capability{}, contract.Fail(contract.FailureNotFound,
				"unknown capability %s; did you mean %s?", id, near)
		}
		return contract.Capability{}, contract.Fail(contract.FailureNotFound,
			"unknown capability %s", id)
	}
	return capability.Clone(), nil
}

// maxSuggestDistance is how many single-character edits still reads as a
// typo of the same word rather than a different one -- code.serach to
// code.search is 2. Measured against the shipped catalog, no two real
// capability ids are anywhere near this close to each other, so this never
// fires between two names that both happen to exist.
const maxSuggestDistance = 3

// closestCapability finds the registered id nearest to id by edit distance.
// Called with the read lock already held. Past maxSuggestDistance a
// suggestion is a guess dressed as help, and worse than saying nothing.
func (r *Registry) closestCapability(id string) (string, bool) {
	best, bestDist := "", maxSuggestDistance+1
	for candidate := range r.capabilities {
		if dist := levenshtein(id, candidate); dist < bestDist {
			best, bestDist = candidate, dist
		}
	}
	return best, bestDist <= maxSuggestDistance
}

// levenshtein is the fewest single-character insertions, deletions or
// substitutions that turn a into b.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

// Capabilities lists every capability, sorted by id.
func (r *Registry) Capabilities() []contract.Capability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]contract.Capability, 0, len(r.capabilities))
	for _, capability := range r.capabilities {
		out = append(out, capability.Clone())
	}
	slices.SortFunc(out, func(a, b contract.Capability) int {
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// Implementation looks one up by id.
func (r *Registry) Implementation(id string) (contract.Implementation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	impl, ok := r.implementers[id]
	if !ok {
		return contract.Implementation{}, contract.Fail(contract.FailureNotFound,
			"unknown implementation %s", id)
	}
	return impl.Clone(), nil
}

// ImplementationsFor lists the providers of a capability, sorted by id. An
// unknown capability is an error; a known capability with no providers yet is
// an empty list.
func (r *Registry) ImplementationsFor(capabilityID string) ([]contract.Implementation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, known := r.capabilities[capabilityID]; !known {
		return nil, contract.Fail(contract.FailureNotFound,
			"unknown capability %s", capabilityID)
	}
	ids := r.byCapability[capabilityID]
	out := make([]contract.Implementation, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.implementers[id].Clone())
	}
	return out, nil
}

// Repository looks one up by id. If id is not a registered name but is an
// absolute path, the repository whose configured path is the longest prefix
// of id is returned instead, so a caller can pass its working directory
// without knowing the registered name.
func (r *Registry) Repository(id string) (contract.Repository, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	repo, ok := r.repositories[id]
	if !ok && filepath.IsAbs(id) {
		repo, ok = r.repositoryForCWD(id)
	}
	if !ok {
		return contract.Repository{}, contract.Fail(contract.FailureNotFound,
			"unknown repository %s", id)
	}
	return repo.Clone(), nil
}

// repositoryForCWD returns the repository whose configured path is the
// longest absolute-path prefix of cwd. Called under r.mu.RLock.
func (r *Registry) repositoryForCWD(cwd string) (contract.Repository, bool) {
	clean := filepath.Clean(cwd)
	var best contract.Repository
	bestLen := -1
	for _, repo := range r.repositories {
		abs, err := filepath.Abs(repo.Path)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if clean == abs || strings.HasPrefix(clean, abs+string(filepath.Separator)) {
			if len(abs) > bestLen {
				best = repo
				bestLen = len(abs)
			}
		}
	}
	return best, bestLen >= 0
}

// Repositories lists every repository, sorted by id.
func (r *Registry) Repositories() []contract.Repository {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]contract.Repository, 0, len(r.repositories))
	for _, repo := range r.repositories {
		out = append(out, repo.Clone())
	}
	slices.SortFunc(out, func(a, b contract.Repository) int {
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// SetHealth records what probing one repository found about an
// implementation.
//
// Health is the one block that changes while Atenea runs: the settings file
// declares a starting point, and from then on whoever probes the provider
// owns the value. Everything else in the catalog is declarative.
//
// It is keyed by repository because a provider is not up or down in the
// abstract. Serena with no TypeScript language server is down for a
// TypeScript repository and perfectly alive for a Go one, and under a
// per-repository instance policy the two are not even the same process. A
// single global verdict would let one repository's missing language server
// refuse work on every other repository on the machine.
func (r *Registry) SetHealth(repositoryID, implementationID string, health contract.Health) error {
	if health.Score < 0 || health.Score > 1 {
		return contract.Fail(contract.FailureInvalidInput,
			"implementation %s: health score %v is outside 0..1", implementationID, health.Score)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.implementers[implementationID]; !ok {
		return contract.Fail(contract.FailureNotFound,
			"unknown implementation %s", implementationID)
	}
	if _, ok := r.repositories[repositoryID]; !ok {
		return contract.Fail(contract.FailureNotFound,
			"unknown repository %s", repositoryID)
	}
	if health.ObservedAt.IsZero() {
		health.ObservedAt = time.Now().UTC()
	}
	byImpl, ok := r.observed[repositoryID]
	if !ok {
		byImpl = make(map[string]contract.Health)
		r.observed[repositoryID] = byImpl
	}
	byImpl[implementationID] = health
	return r.persistLocked()
}

// Observed returns the candidates as they stand for one repository: the
// declared health where nothing has been probed, and what probing found where
// it has.
//
// The overlay is applied here rather than inside ImplementationsFor because
// the catalog is also read with no repository in hand -- the status screen
// lists what was declared -- and a lookup that silently answered differently
// depending on who asked would be the harder of the two to reason about.
func (r *Registry) Observed(repositoryID string, impls []contract.Implementation) []contract.Implementation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byImpl := r.observed[repositoryID]
	if len(byImpl) == 0 {
		return impls
	}
	for i, impl := range impls {
		if health, ok := byImpl[impl.ID]; ok {
			impls[i].Health = health
		}
	}
	return impls
}

// Observations reports the worst thing probing found about an implementation
// and which repository found it, for the status screen.
//
// Worst rather than newest: a screen that showed the last call would flip
// between two repositories on every request, and the question an operator is
// asking a status screen is whether anything is wrong, not what happened most
// recently.
func (r *Registry) Observations(implementationID string) (contract.Health, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var (
		worst contract.Health
		where string
		found bool
	)
	for _, repositoryID := range slices.Sorted(maps.Keys(r.observed)) {
		health, ok := r.observed[repositoryID][implementationID]
		if !ok {
			continue
		}
		if !found || health.State.Rank() > worst.State.Rank() {
			worst, where, found = health, repositoryID, true
		}
	}
	return worst, where, found
}

// SetIndexed corrects one repository's belief about whether a provider has a
// ready index for it.
//
// Like SetHealth, this is one of the few places a catalog entry changes
// while Atenea runs: indexed_by is what the settings file declared as a
// starting point, and from here on whoever actually probes the provider owns
// the value.
func (r *Registry) SetIndexed(repositoryID, provider string, ready bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	repo, ok := r.repositories[repositoryID]
	if !ok {
		return contract.Fail(contract.FailureNotFound, "unknown repository %s", repositoryID)
	}
	r.repositories[repositoryID] = repo.SetIndexed(provider, ready)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if r.indexOverrides[repositoryID] == nil {
		r.indexOverrides[repositoryID] = make(map[string]bool)
	}
	r.indexOverrides[repositoryID][provider] = ready
	return r.persistLocked()
}

// persistLocked atomically writes runtime observations. Callers hold r.mu.
func (r *Registry) persistLocked() error {
	if r.statePath == "" {
		return nil
	}
	raw, err := json.MarshalIndent(persistedState{
		Version: 1, Health: r.observed, IndexedBy: r.indexOverrides,
	}, "", "  ")
	if err != nil {
		return contract.Fail(contract.FailureUnavailable, "registry state: encoding: %v", err)
	}
	dir := filepath.Dir(r.statePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"registry state: creating %s: %v", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".registry-state-*.tmp")
	if err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"registry state: creating temporary file: %v", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return contract.Fail(contract.FailurePermissionDenied,
			"registry state: securing temporary file: %v", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return contract.Fail(contract.FailurePermissionDenied, "registry state: writing: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return contract.Fail(contract.FailureUnavailable, "registry state: syncing: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return contract.Fail(contract.FailureUnavailable, "registry state: closing: %v", err)
	}
	if err := os.Rename(tmpName, r.statePath); err != nil {
		return contract.Fail(contract.FailurePermissionDenied,
			"registry state: replacing %s: %v", r.statePath, err)
	}
	return nil
}
