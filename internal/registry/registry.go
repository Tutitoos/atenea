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
	if _, known := r.capabilities[impl.Capability]; !known {
		return contract.Fail(contract.FailureInvalidInput,
			"implementation %s targets unknown capability %s", impl.ID, impl.Capability)
	}
	r.implementers[impl.ID] = impl.Clone()
	ids := append(r.byCapability[impl.Capability], impl.ID)
	slices.Sort(ids)
	r.byCapability[impl.Capability] = ids
	return nil
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

// Capability looks one up by id.
func (r *Registry) Capability(id string) (contract.Capability, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	capability, ok := r.capabilities[id]
	if !ok {
		return contract.Capability{}, contract.Fail(contract.FailureNotFound,
			"unknown capability %s", id)
	}
	return capability.Clone(), nil
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
