// Package registry holds Atenea's catalog: the capabilities it knows how to
// ask for, the implementations that answer them, and the repositories those
// implementations run against.
//
// It is deliberately a lookup table with validation, not a brain. Deciding
// which implementation to use is the selector's job.
package registry

import (
	"slices"
	"strings"
	"sync"

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
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		capabilities: make(map[string]contract.Capability),
		implementers: make(map[string]contract.Implementation),
		repositories: make(map[string]contract.Repository),
		byCapability: make(map[string][]string),
	}
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

// Repository looks one up by id.
func (r *Registry) Repository(id string) (contract.Repository, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	repo, ok := r.repositories[id]
	if !ok {
		return contract.Repository{}, contract.Fail(contract.FailureNotFound,
			"unknown repository %s", id)
	}
	return repo.Clone(), nil
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

// SetHealth replaces the health block of an implementation.
//
// Health is the one block that changes while Atenea runs: the settings file
// declares a starting point, and from then on whoever probes the provider owns
// the value. Everything else in the catalog is declarative.
func (r *Registry) SetHealth(implementationID string, health contract.Health) error {
	if health.Score < 0 || health.Score > 1 {
		return contract.Fail(contract.FailureInvalidInput,
			"implementation %s: health score %v is outside 0..1", implementationID, health.Score)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	impl, ok := r.implementers[implementationID]
	if !ok {
		return contract.Fail(contract.FailureNotFound,
			"unknown implementation %s", implementationID)
	}
	impl.Health = health
	r.implementers[implementationID] = impl
	return nil
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
	return nil
}
