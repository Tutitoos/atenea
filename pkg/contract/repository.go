package contract

import (
	"maps"
	"slices"
	"strings"
)

// Repository is Atenea's unit of work.
//
// Not the project: the repository. A workspace holding forty repositories is
// forty units, each with its own languages, its own size and its own indexes.
// One global index over the lot was the alternative, and it was refused for
// being slow to build and impossible to keep fresh.
type Repository struct {
	ID   string
	Path string
	// Languages present in the repository, lowercase.
	Languages []string
	Scale     Scale
	// VCS says whether the repository sits under version control, as far as
	// anyone has declared. VCSUnspecified never disqualifies a provider that
	// requires it -- see selector.fits.
	VCS VCS
	// indexes holds the providers that have a ready index for this repository.
	indexes map[string]struct{}
}

// NewRepository builds a repository descriptor. indexedBy lists the providers
// holding a ready index for it.
func NewRepository(id, path string, languages []string, scale Scale, vcs VCS, indexedBy []string) Repository {
	repo := Repository{
		ID:        id,
		Path:      path,
		Languages: make([]string, 0, len(languages)),
		Scale:     scale,
		VCS:       vcs,
		indexes:   make(map[string]struct{}, len(indexedBy)),
	}
	for _, lang := range languages {
		repo.Languages = append(repo.Languages, strings.ToLower(strings.TrimSpace(lang)))
	}
	for _, provider := range indexedBy {
		repo.indexes[strings.ToLower(strings.TrimSpace(provider))] = struct{}{}
	}
	return repo
}

// Validate checks the descriptor.
func (r Repository) Validate() error {
	if !slugID.MatchString(r.ID) {
		return Fail(FailureInvalidInput, "repository id %q must be lowercase", r.ID)
	}
	if strings.TrimSpace(r.Path) == "" {
		return Fail(FailureInvalidInput, "repository %s: path is required", r.ID)
	}
	for _, lang := range r.Languages {
		if lang == "" {
			return Fail(FailureInvalidInput, "repository %s: empty language entry", r.ID)
		}
	}
	return nil
}

// IndexedBy reports whether the given provider has a ready index here.
func (r Repository) IndexedBy(provider string) bool {
	_, ok := r.indexes[strings.ToLower(provider)]
	return ok
}

// Indexes lists the providers with a ready index here, sorted.
func (r Repository) Indexes() []string {
	out := slices.Collect(maps.Keys(r.indexes))
	slices.Sort(out)
	return out
}

// SetIndexed returns a copy of the repository with one provider's index
// readiness corrected.
//
// indexed_by, as the settings file declares it, is a starting point the
// operator typed by hand; a detector that has actually asked the provider is
// evidence, and evidence wins. This is the one place a repository's declared
// shape changes while Atenea runs, the same exception SetHealth already is
// for an implementation's health.
func (r Repository) SetIndexed(provider string, ready bool) Repository {
	out := r.Clone()
	provider = strings.ToLower(strings.TrimSpace(provider))
	if out.indexes == nil {
		out.indexes = make(map[string]struct{}, 1)
	}
	if ready {
		out.indexes[provider] = struct{}{}
	} else {
		delete(out.indexes, provider)
	}
	return out
}

// SpeaksAny reports whether the repository contains any of the given languages.
// An empty list means the caller is language-agnostic, which always matches.
func (r Repository) SpeaksAny(languages []string) bool {
	if len(languages) == 0 {
		return true
	}
	for _, want := range languages {
		if slices.Contains(r.Languages, strings.ToLower(want)) {
			return true
		}
	}
	return false
}

// Clone returns a deep copy.
func (r Repository) Clone() Repository {
	r.Languages = slices.Clone(r.Languages)
	r.indexes = maps.Clone(r.indexes)
	return r
}
