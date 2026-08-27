package agent_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// TestRealReviewerRunsThroughRunner exercises the shipped reviewer at the
// same boundary a workflow uses: a work process writes prose, Runner starts
// the real `agent-exec reviewer` binary, and the strict result schema accepts
// the citation evidence it returns.
func TestRealReviewerRunsThroughRunner(t *testing.T) {
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "a.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatalf("writing repository fixture: %v", err)
	}

	root := sourceRoot(t)
	reviewerConfig := shippedReviewer(t, root)
	reviewerConfig.Command = reviewerCommand(t, root)
	reviewerConfig.Args = nil
	work := proseReader(t)
	store, err := trace.Open(t.Context(), filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("opening trace store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	runner, err := agent.New(agent.Options{
		Types: []config.AgentType{work, reviewerConfig},
		Store: store,
		Self:  "/nonexistent/atenea",
		Workspace: agent.Workspace{
			RepositoryID:   "current",
			RepositoryRoot: repository,
		},
	})
	if err != nil {
		t.Fatalf("creating runner: %v", err)
	}

	run, err := runner.RunReviewed(t.Context(), "prose-reader", "reviewer", contract.Task{
		Objective: "read a.txt and cite every prose field",
		Files:     []string{"a.txt"},
		Criterion: "every claim has a verifiable file and line",
	})
	if err != nil {
		t.Fatalf("RunReviewed: %v", err)
	}
	if !run.Accepted() || len(run.Attempts) != 1 {
		t.Fatalf("run = %+v, want one accepted attempt", run)
	}

	review := run.Attempts[0].ReviewReport
	if review.Result["citation_count"] != float64(2) {
		t.Fatalf("citation_count = %v, want 2", review.Result["citation_count"])
	}
	if review.Result["content_checked"] != float64(2) {
		t.Fatalf("content_checked = %v, want 2", review.Result["content_checked"])
	}
	if review.Result["checked"] != float64(2) {
		t.Fatalf("checked = %v, want 2", review.Result["checked"])
	}
	if fields, ok := review.Result["uncited_fields"].([]any); !ok || len(fields) != 0 {
		t.Fatalf("uncited_fields = %#v, want an empty list", review.Result["uncited_fields"])
	}
	rows, ok := review.Result["citations"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("citations = %#v, want two evidence rows", review.Result["citations"])
	}
	for _, raw := range rows {
		evidence, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("citation row = %#v, want object", raw)
		}
		if evidence["resolved_path"] != "a.txt" || evidence["outcome"] != "content_checked" {
			t.Fatalf("citation evidence = %#v, want a.txt/content_checked", evidence)
		}
	}

	traceRows, err := store.List(t.Context(), trace.Filter{})
	if err != nil {
		t.Fatalf("listing traces: %v", err)
	}
	if len(traceRows) != 2 {
		t.Fatalf("trace rows = %d, want work plus reviewer", len(traceRows))
	}
	if got := rowByID(t, traceRows, run.Attempts[0].Review.ID); got.Reviews != run.Attempts[0].Work.ID {
		t.Fatalf("review trace reviews %q, want %q", got.Reviews, run.Attempts[0].Work.ID)
	}
}

func proseReader(t *testing.T) config.AgentType {
	t.Helper()
	return config.AgentType{
		Spec: contract.AgentTypeSpec{
			Name: "prose-reader",
			Kind: contract.AgentSpecialized,
			Result: []contract.Field{
				{Name: "summary", Type: contract.TypeString, Required: true, Summary: "cited summary"},
				{Name: "findings", Type: contract.TypeString, Required: true, Summary: "cited findings"},
			},
		},
		Summary: "fixture prose reader",
		Command: proseReaderCommand(t),
		Effects: []contract.Effect{contract.EffectRead},
		Limits:  contract.Limits{MaxDuration: 10 * time.Second, MaxTokens: 1},
	}
}

func proseReaderCommand(t *testing.T) string {
	t.Helper()
	return executableScript(t, "cat >/dev/null\n"+
		"cat <<'REPORT'\n"+
		"{\"result\":{\"summary\":\"The file starts at a.txt:1, which reads `one`.\",\"findings\":\"The second line is at a.txt:2, which reads `two`.\"},\"verdict\":\"ok\"}\n"+
		"REPORT")
}

func reviewerCommand(t *testing.T, root string) string {
	t.Helper()
	return executableScript(t, "cd "+shellQuote(root)+` || exit 1
exec go run ./cmd/atenea agent-exec reviewer`)
}

func shippedReviewer(t *testing.T, root string) config.AgentType {
	t.Helper()
	cfg, err := config.Load(filepath.Join(root, "internal", "config", "default.toml"))
	if err != nil {
		t.Fatalf("loading shipped settings: %v", err)
	}
	reviewer, err := cfg.AgentTypeByName("reviewer")
	if err != nil {
		t.Fatalf("loading shipped reviewer: %v", err)
	}
	// The shipped limit is 30 seconds and it is not being changed -- what
	// ships is what ships, and a product number moved to make a test pass is
	// a number that stopped meaning anything.
	//
	// This raises it for THIS RUN only, because the deadline is measured
	// against a machine and the machine here is running the whole suite under
	// -race at once. Measured: 4.8s for this agent on its own, and a failure
	// at 30s inside a full parallel run -- six times slower, which says
	// everything about the runner and nothing about the reviewer. A test that
	// fails on a busy laptop is a test people learn to re-run.
	reviewer.Limits.MaxDuration = 3 * time.Minute
	return reviewer
}

func sourceRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not return the integration test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func executableScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture-agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("writing fixture agent: %v", err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
