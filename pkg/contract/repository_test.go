package contract_test

import (
	"slices"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestNewRepositoryNormalisesInput(t *testing.T) {
	repo := contract.NewRepository("web", "/srv/web", []string{" TypeScript ", "CSS"}, contract.ScaleMedium, contract.VCSUnspecified, []string{"Serena"})
	if !slices.Equal(repo.Languages, []string{"typescript", "css"}) {
		t.Fatalf("languages = %v", repo.Languages)
	}
	if !repo.IndexedBy("serena") {
		t.Fatal("provider index should be found case-insensitively")
	}
	if repo.IndexedBy("codebase-memory") {
		t.Fatal("unindexed provider reported as indexed")
	}
}

// An index belongs to the tool that built it, not to one implementation of it:
// two implementations of the same provider share the same warm index.
func TestIndexesAreKeyedByProvider(t *testing.T) {
	repo := contract.NewRepository("api", "/srv/api", nil, contract.ScaleLarge, contract.VCSUnspecified, []string{"serena", "codebase-memory"})
	got := repo.Indexes()
	if !slices.Equal(got, []string{"codebase-memory", "serena"}) {
		t.Fatalf("Indexes() = %v, want sorted providers", got)
	}
}

// An empty language list means the caller does not parse anything, so it fits
// every repository. That is what makes ripgrep universal.
func TestSpeaksAnyTreatsEmptyAsAgnostic(t *testing.T) {
	repo := contract.NewRepository("api", "/srv/api", []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
	if !repo.SpeaksAny(nil) {
		t.Error("an agnostic caller must match every repository")
	}
	if !repo.SpeaksAny([]string{"rust", "Go"}) {
		t.Error("one overlapping language is enough")
	}
	if repo.SpeaksAny([]string{"rust", "php"}) {
		t.Error("no overlap must not match")
	}
}

func TestRepositoryValidate(t *testing.T) {
	if err := contract.NewRepository("web", "/srv/web", nil, contract.ScaleSmall, contract.VCSUnspecified, nil).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := contract.NewRepository("Web", "/srv/web", nil, contract.ScaleSmall, contract.VCSUnspecified, nil).Validate(); err == nil {
		t.Error("uppercase id should fail")
	}
	if err := contract.NewRepository("web", "  ", nil, contract.ScaleSmall, contract.VCSUnspecified, nil).Validate(); err == nil {
		t.Error("empty path should fail")
	}
}

func TestRepositoryCloneDoesNotShareState(t *testing.T) {
	original := contract.NewRepository("web", "/srv/web", []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, []string{"serena"})
	clone := original.Clone()
	clone.Languages[0] = "rust"
	if original.Languages[0] != "go" {
		t.Fatal("clone shared the language slice")
	}
}
