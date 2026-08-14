package workflow_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func catalog(repos ...contract.Repository) config.Config {
	return config.Config{Repositories: repos}
}

// The regression, measured on a real run on 2026-08-14. This machine's
// settings declare a repository whose id is `current`, and `current` was also
// the name this function invented for "the tree you are standing in". A
// workflow launched in /tmp/e2e/repo was served /home/tutitoos/Desktop/atenea:
// every agent in that run was told it was somewhere it was not, and the
// planner correctly read the wrong repository's overlay. A sentinel a settings
// file may legally declare is not a sentinel.
func TestARepositoryNamedCurrentDoesNotCaptureARunSomewhereElse(t *testing.T) {
	elsewhere := t.TempDir()
	cfg := catalog(contract.Repository{ID: "current", Path: t.TempDir()})

	ws, err := workflow.WorkspaceFor(cfg, "", elsewhere)
	if err != nil {
		t.Fatalf("WorkspaceFor: %v", err)
	}
	if ws.RepositoryRoot != elsewhere {
		t.Errorf("root = %q, want the directory the run is in (%q)", ws.RepositoryRoot, elsewhere)
	}
	if ws.RepositoryID != "" {
		t.Errorf("id = %q, want empty: this tree is not one of the declared repositories", ws.RepositoryID)
	}
}

// Naming nothing asks about here, not about whichever repository the settings
// file happens to list first.
func TestNamingNothingResolvesTheTreeTheRunIsIn(t *testing.T) {
	first := t.TempDir()
	outer := t.TempDir()
	inner := filepath.Join(outer, "sub", "pkg")
	// The nested repository is declared BEFORE the one containing it, so a
	// loop that keeps the last match rather than the deepest one answers
	// `outer` for a directory inside `inner` and this test says so.
	cfg := catalog(
		contract.Repository{ID: "first-in-the-file", Path: first},
		contract.Repository{ID: "inner", Path: inner},
		contract.Repository{ID: "outer", Path: outer},
	)

	for _, tc := range []struct{ dir, want string }{
		{outer, "outer"},
		{inner, "inner"},
		{filepath.Join(inner, "deeper"), "inner"},
		{filepath.Join(outer, "sub"), "outer"},
	} {
		ws, err := workflow.WorkspaceFor(cfg, "", tc.dir)
		if err != nil {
			t.Fatalf("WorkspaceFor(%s): %v", tc.dir, err)
		}
		if ws.RepositoryID != tc.want {
			t.Errorf("in %s: id = %q, want %q", tc.dir, ws.RepositoryID, tc.want)
		}
	}
}

// A named repository that does not exist is a typo, and answering a typo with
// the working directory is how a run happens against a tree nobody named.
func TestAnUnknownRepositoryIsRefusedRatherThanSubstituted(t *testing.T) {
	cfg := catalog(contract.Repository{ID: "api", Path: t.TempDir()})

	_, err := workflow.WorkspaceFor(cfg, "apy", t.TempDir())
	if contract.KindOf(err) != contract.FailureNotFound {
		t.Fatalf("kind = %v, want not_found (err = %v)", contract.KindOf(err), err)
	}
	if got := err.Error(); !strings.Contains(got, "apy") || !strings.Contains(got, "api") {
		t.Errorf("the refusal names neither the typo nor what is declared: %v", err)
	}
}

// A declared repository is served by id, which is the one case where the name
// in the run record is meaningful later.
func TestANamedRepositoryIsServedWhereverTheRunStarts(t *testing.T) {
	root := t.TempDir()
	cfg := catalog(contract.Repository{ID: "api", Path: root})

	ws, err := workflow.WorkspaceFor(cfg, "api", t.TempDir())
	if err != nil {
		t.Fatalf("WorkspaceFor: %v", err)
	}
	if ws.RepositoryID != "api" || ws.RepositoryRoot != root {
		t.Errorf("served %q at %q, want api at %q", ws.RepositoryID, ws.RepositoryRoot, root)
	}
}
