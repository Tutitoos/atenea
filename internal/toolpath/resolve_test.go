package toolpath

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveAutoUsesDeclaredOrder(t *testing.T) {
	got, err := Resolve("auto", []Candidate{
		{Source: "terminal", Binary: "definitely-not-installed"},
		{Source: "app", Binary: filepath.Join(runtime.GOROOT(), "bin", "go")},
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
		{Source: "app", Binary: filepath.Join(runtime.GOROOT(), "bin", "go")},
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
