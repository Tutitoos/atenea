package contract

import (
	"slices"
	"strings"
)

// Repository is Atenea's unit of work.
//
// Not the project: the repository. A workspace holding forty repositories is
// forty units, each with its own languages and its own size.
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
}

// NewRepository builds a repository descriptor.
func NewRepository(id, path string, languages []string, scale Scale, vcs VCS) Repository {
	repo := Repository{
		ID:        id,
		Path:      path,
		Languages: make([]string, 0, len(languages)),
		Scale:     scale,
		VCS:       vcs,
	}
	for _, lang := range languages {
		repo.Languages = append(repo.Languages, strings.ToLower(strings.TrimSpace(lang)))
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
	return r
}
