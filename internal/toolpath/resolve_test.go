package toolpath

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func goBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveAutoUsesDeclaredOrder(t *testing.T) {
	got, err := Resolve("auto", []Candidate{
		{Source: "terminal", Binary: "definitely-not-installed"},
		{Source: "app", Binary: filepath.Join(filepath.Dir(goBinary(t)), "go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "app" || got.Path == "" {
		t.Fatalf("resolved = %+v, want the available app candidate", got)
	}
}

func TestResolveExplicitSourceDoesNotFallback(t *testing.T) {
	_, err := Resolve("terminal", []Candidate{
		{Source: "terminal", Binary: "definitely-not-installed"},
		{Source: "app", Binary: filepath.Join(filepath.Dir(goBinary(t)), "go")},
	})
	if err == nil {
		t.Fatal("explicit terminal source unexpectedly fell back to app")
	}
}

func TestResolveRejectsUnknownSource(t *testing.T) {
	_, err := Resolve("desktop", []Candidate{{Source: "terminal", Binary: "go"}})
	if err == nil {
		t.Fatal("unknown source was accepted")
	}
}

func TestValidateSourceAndResolveEmptyCandidates(t *testing.T) {
	candidates := []Candidate{{Source: "terminal", Binary: "go"}}
	for _, source := range []string{"", "auto", " terminal "} {
		if err := ValidateSource(source, candidates); err != nil {
			t.Fatalf("ValidateSource(%q): %v", source, err)
		}
	}
	if err := ValidateSource("desktop", candidates); err == nil {
		t.Fatal("ValidateSource accepted an unknown source")
	}
	if _, err := Resolve("auto", nil); err == nil {
		t.Fatal("Resolve accepted an empty candidate list")
	}
	if _, err := Resolve("terminal", []Candidate{{Source: "terminal", Binary: ""}}); err == nil {
		t.Fatal("Resolve accepted an unavailable explicit candidate")
	}
}
