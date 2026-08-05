package codebasememory

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// --- payload parsing -------------------------------------------------------

func TestReadIndexRepositoryAskDefaultsMode(t *testing.T) {
	got, err := readIndexRepositoryAsk(map[string]any{})
	if err != nil {
		t.Fatalf("readIndexRepositoryAsk: %v", err)
	}
	if got.mode != defaultIndexMode {
		t.Errorf("mode = %q, want the default %q", got.mode, defaultIndexMode)
	}
}

func TestReadIndexRepositoryAskAcceptsEachDeclaredMode(t *testing.T) {
	for _, mode := range []string{"fast", "moderate", "full"} {
		got, err := readIndexRepositoryAsk(map[string]any{"mode": " " + mode + " "})
		if err != nil {
			t.Fatalf("readIndexRepositoryAsk(%q): %v", mode, err)
		}
		if got.mode != mode {
			t.Errorf("mode = %q, want %q (trimmed and lowercased)", got.mode, mode)
		}
	}
}

func TestReadIndexRepositoryAskRejectsAnUnknownMode(t *testing.T) {
	if _, err := readIndexRepositoryAsk(map[string]any{"mode": "thorough"}); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want invalid input", contract.KindOf(err))
	}
}

// --- Run ---------------------------------------------------------------

// repositoryIndexCapability mirrors the shipped capability, the same reason
// codeImpactCapability does.
func repositoryIndexCapability() contract.Capability {
	return contract.Capability{
		ID:      CapabilityRepositoryIndex,
		Version: contract.Version{Major: 1},
		Summary: "Build or refresh a provider's index for a repository.",
		Effects: []contract.Effect{contract.EffectWrite, contract.EffectProcess},
		Inputs: []contract.Field{
			{Name: "mode", Type: contract.TypeString},
		},
		Outputs: []contract.Field{
			{Name: "status", Type: contract.TypeString, Required: true},
			{Name: "nodes", Type: contract.TypeInt, Required: true},
			{Name: "edges", Type: contract.TypeInt, Required: true},
		},
	}
}

func TestRunRepositoryIndexBuildsAndReportsTheGraph(t *testing.T) {
	path := fakeCodebaseMemory(t, map[string]string{
		"index_repository": `{"status":"indexed","nodes":42,"edges":97}`,
	})
	runner, err := New(Options{Binary: path, Implementations: []string{ImplIndex}, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := requestAt(t, t.TempDir(), repositoryIndexCapability(), ImplIndex, map[string]any{"mode": "fast"})
	req.Permission = contract.Permission{Task: "build", Effects: []contract.Effect{contract.EffectWrite, contract.EffectProcess}}
	outcome, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.Verdict != contract.VerdictOK {
		t.Fatalf("verdict = %v", outcome.Verdict)
	}
	if got := outcome.Result["status"]; got != "indexed" {
		t.Errorf("status = %v, want %q", got, "indexed")
	}
	if got := outcome.Result["nodes"]; got != 42 {
		t.Errorf("nodes = %v, want 42", got)
	}
	if got := outcome.Result["edges"]; got != 97 {
		t.Errorf("edges = %v, want 97", got)
	}
}

// index_repository takes repo_path, not project -- the one tool in this
// family that names its path argument differently. A regression here would
// still pass a JSON-shape assertion against the fake binary's canned reply,
// so the fake has to fail unless it sees the argument it was actually sent.
func TestRunRepositoryIndexSendsRepoPathNotProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary below is a POSIX shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "codebase-memory-mcp")
	script := "#!/bin/sh\nbody=\"$(cat)\"\ncase \"$body\" in\n" +
		`*repo_path*) echo '{"status":"indexed","nodes":1,"edges":0}'; exit 0 ;;` + "\n" +
		`*) echo '{"error":"missing repo_path"}' >&2; exit 1 ;;` + "\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the fake binary: %v", err)
	}
	runner, err := New(Options{Binary: path, Implementations: []string{ImplIndex}, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := requestAt(t, t.TempDir(), repositoryIndexCapability(), ImplIndex, nil)
	req.Permission = contract.Permission{Task: "build", Effects: []contract.Effect{contract.EffectWrite, contract.EffectProcess}}
	if _, err := runner.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v (repo_path was not sent as expected)", err)
	}
}
