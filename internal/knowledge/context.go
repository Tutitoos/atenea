package knowledge

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ContextProvider is the narrow production seam used while preparing a
// repository context. It deliberately exposes only accepted, fresh entries.
// Legacy history and candidate notes are not part of this read path.
type ContextProvider struct {
	Store      *Store
	Scope      Scope
	Permission Permission
	Probe      func(context.Context, Entry) (ProbeResult, error)
}

// Prepare is part of ATENEA's public orchestration contract.
func (p ContextProvider) Prepare(ctx context.Context) ([]Entry, error) {
	if p.Store == nil {
		return nil, errors.New("knowledge: context provider has no store")
	}
	if p.Probe == nil {
		return nil, errors.New("knowledge: context probe is required")
	}
	// Query all verified rows first, including expired ones. Expiration is a
	// reason to probe, not a reason to skip the only observation that can
	// refresh the row.
	entries, err := p.Store.Query(ctx, Query{Scope: p.Scope, Permission: p.Permission, Statuses: []Status{Verified}})
	if err != nil {
		return nil, err
	}
	prepared := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		observation, err := p.Probe(ctx, entry)
		if err != nil || !observation.Complete || !observation.Success {
			continue
		}
		if len(observation.Sources) == 0 || len(observation.Dependencies) == 0 || !freshObservation(observation.Dependencies, time.Now().UTC()) {
			continue
		}
		if err := validateSources(observation.Sources); err != nil || validateDependencies(observation.Dependencies) != nil {
			continue
		}
		refreshed, err := p.Store.refreshVerified(ctx, entry, observation, p.Permission)
		if err != nil {
			continue
		}
		prepared = append(prepared, refreshed)
	}
	return prepared, nil
}

func freshObservation(dependencies []Dependency, now time.Time) bool {
	if len(dependencies) == 0 {
		return false
	}
	for _, dependency := range dependencies {
		if dependency.Provider == (ProviderIdentity{}) || dependency.Generation < 1 || strings.TrimSpace(dependency.Snapshot) == "" ||
			!strings.EqualFold(strings.TrimSpace(dependency.Freshness), "fresh") || dependency.CheckedAt.IsZero() || dependency.TTLSeconds <= 0 {
			return false
		}
		if now.After(dependency.CheckedAt.Add(time.Duration(dependency.TTLSeconds) * time.Second)) {
			return false
		}
	}
	return true
}
