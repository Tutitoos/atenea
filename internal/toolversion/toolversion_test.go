package toolversion_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/toolversion"
)

// The banner is a grouping key, so what matters is that two runs of the same
// binary produce the same string and two different binaries do not.
func TestCleanKeepsTheFirstRealLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"a bare number", "14.1.0\n", "14.1.0"},
		{"a name and a number", "ripgrep 14.1.0\n\nfeatures: +pcre2\n", "ripgrep 14.1.0"},
		{"leading blank lines", "\n\n  fixture 0.1.4  \n", "fixture 0.1.4"},
		{"a paragraph", "go version go1.24.4 linux/amd64\nbuilt with gc\n", "go version go1.24.4 linux/amd64"},
		{"nothing at all", "", ""},
		{"only whitespace", "\n \t\n", ""},
		{"carriage returns", "claude 2.0.1\r\nnode 22\r\n", "claude 2.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolversion.Clean(tc.raw); got != tc.want {
				t.Errorf("Clean(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// A key nobody can ever match twice is not a key, so a tool that answers with
// an essay is cut rather than stored whole.
func TestCleanCutsALineNobodyWouldMatchTwice(t *testing.T) {
	got := toolversion.Clean(strings.Repeat("x", 400))
	if len(got) > 120 {
		t.Fatalf("kept %d characters, want at most 120", len(got))
	}
	if got == "" {
		t.Fatal("a long banner became no banner at all")
	}
}

// Silence is an answer: a tool that is not installed leaves the version empty
// rather than failing the dispatch that asked.
func TestAMissingBinaryAnswersEmpty(t *testing.T) {
	p := toolversion.New("atenea-no-such-tool-anywhere", "--version")
	if got := p.Version(context.Background()); got != "" {
		t.Errorf("version = %q, want empty for a binary that does not exist", got)
	}
}

// Same for a binary that is there but refuses the flag: an exit code is not a
// version, and guessing one would poison the grouping key.
func TestAToolThatRefusesTheFlagAnswersEmpty(t *testing.T) {
	p := toolversion.New("false", "--version")
	if got := p.Version(context.Background()); got != "" {
		t.Errorf("version = %q, want empty when the tool exits non-zero", got)
	}
}

// A real binary that does answer is read and cleaned.
func TestARealToolIsRead(t *testing.T) {
	p := toolversion.New("go", "version")
	got := p.Version(context.Background())
	if !strings.HasPrefix(got, "go version") {
		t.Fatalf("version = %q, want the go banner", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("version = %q, want a single line", got)
	}
}

// The probe runs once per process. A dispatch that asks a hundred times must
// spawn one child, not a hundred.
func TestTheProbeOnlyAsksOnce(t *testing.T) {
	p := toolversion.New("go", "version")
	first := p.Version(context.Background())
	for range 100 {
		if got := p.Version(context.Background()); got != first {
			t.Fatalf("version changed under a stable binary: %q then %q", first, got)
		}
	}
}

// An already-dead context must not stop the probe. A dispatch whose deadline
// has nearly run out still has a version worth filing, and the probe carries
// its own clock precisely so the caller's does not reach it.
func TestACanceledCallerStillGetsAVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := toolversion.New("go", "version")
	if got := p.Version(ctx); !strings.HasPrefix(got, "go version") {
		t.Fatalf("version = %q under a canceled caller, want the real banner", got)
	}
}

// An unnamed binary is not an error, it is a runner with nothing to probe --
// the stand-in that runs inside Atenea itself, for one.
func TestAnEmptyBinaryIsNotProbed(t *testing.T) {
	if got := toolversion.New("").Version(context.Background()); got != "" {
		t.Errorf("version = %q, want empty", got)
	}
}
