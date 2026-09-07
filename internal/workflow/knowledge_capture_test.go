package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/knowledge"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestKnowledgeCaptureCreatesCandidateAndPromotesOnlyAcceptedEvidence(t *testing.T) {
	ctx := context.Background()
	workflowStore, err := Open(ctx, filepath.Join(t.TempDir(), "workflow.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer workflowStore.Close()
	route := func(role string) *contract.Route {
		model, effort := "gpt-5.6-luna", "xhigh"
		switch role {
		case "review":
			model, effort = "gpt-5.6-sol", "medium"
		case "audit":
			model, effort = "gpt-6-astra", "medium"
		}
		return &contract.Route{Model: model, RequestedModel: model, ObservedModel: model, Role: role, RequestedReasoningEffort: effort, ObservedReasoningEffort: effort, Backend: "fixture"}
	}
	steps := []Step{
		{ID: "implementation", PointID: "P15", PointTitle: "knowledge", TypeName: "implementation", Route: route("implement"), Task: contract.Task{Objective: "implement"}},
		{ID: "review", PointID: "P15", PointTitle: "knowledge", TypeName: "review", Route: route("review"), Subject: "implementation", Task: contract.Task{Objective: "review"}},
		{ID: "audit", PointID: "P15", PointTitle: "knowledge", TypeName: "audit", Route: route("audit"), Subject: "review", Task: contract.Task{Objective: "audit"}},
	}
	plan := Plan{Graph: Graph{Task: "P15", Steps: steps}, Pools: map[string]config.Pool{"implementation": config.PoolAgent, "review": config.PoolReview, "audit": config.PoolReview}}
	if err := workflowStore.CreateWithFingerprint(ctx, "run-p15-capture", plan, "repo-a", "tree-a", time.Now().UTC(), 0); err != nil {
		t.Fatal(err)
	}
	knowledgeStore, err := knowledge.Open(filepath.Join(t.TempDir(), "knowledge.sqlite"), knowledge.WithEvidenceResolver(KnowledgeEvidenceResolver{Store: workflowStore}))
	if err != nil {
		t.Fatal(err)
	}
	defer knowledgeStore.Close()
	capture := knowledgeCapture{store: knowledgeStore, repository: "repo-a"}
	report := contract.Report{Verdict: contract.VerdictOK, Completeness: floatPtr(1), Result: map[string]any{"knowledge_candidate": map[string]any{"kind": "hypothesis", "title": "Keep one writer", "body": "One writer owns each worktree.", "visibility": "project"}}}
	candidateID, digest, err := capture.prepare(ctx, "run-p15-capture", steps[0], "tree-a", report)
	if err != nil || candidateID == "" || digest == "" {
		t.Fatalf("candidate id=%q digest=%q err=%v", candidateID, digest, err)
	}
	if _, err := workflowStore.db.ExecContext(ctx, `UPDATE workflow_step SET status='ok',verdict='ok',invoked=1,invoked_known=1,trace_id='trace-implementation',source_fingerprint='tree-a',result='{}' WHERE workflow_id='run-p15-capture' AND id='implementation'`); err != nil {
		t.Fatal(err)
	}
	if err := workflowStore.BindKnowledgeCandidate(ctx, "run-p15-capture", "implementation", candidateID, digest); err != nil {
		t.Fatal(err)
	}
	permission := knowledge.Permission{SubjectID: "workflow:run-p15-capture", ProjectID: "repo-a", RepositoryID: "repo-a", Read: true, Write: true, Promote: true, ProjectMember: true}
	entries, err := knowledgeStore.Query(ctx, knowledge.Query{Scope: knowledge.Scope{ProjectID: "repo-a", RepositoryID: "repo-a"}, Permission: permission})
	if err != nil || len(entries) != 1 || entries[0].Status != knowledge.Candidate {
		t.Fatalf("candidate entries=%+v err=%v", entries, err)
	}
	if err := capture.promote(ctx, mustLoadRun(t, workflowStore, "run-p15-capture")); err != nil {
		t.Fatal(err)
	}
	entries, _ = knowledgeStore.Query(ctx, knowledge.Query{Scope: entries[0].Scope, Permission: permission})
	if entries[0].Status != knowledge.Candidate {
		t.Fatalf("unreviewed candidate promoted: %+v", entries[0])
	}
	for _, id := range []string{"implementation", "review", "audit"} {
		result := `{"decision":"approved"}`
		if id == "implementation" {
			result = `{"knowledge_candidate_id":"` + candidateID + `","knowledge_digest":"` + digest + `"}`
		}
		if _, err := workflowStore.db.ExecContext(ctx, `UPDATE workflow_step SET status='ok',verdict='ok',invoked=1,invoked_known=1,trace_id=?,source_fingerprint='tree-a',result=? WHERE workflow_id='run-p15-capture' AND id=?`, "trace-"+id, result, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := workflowStore.db.ExecContext(ctx, `UPDATE workflow_point SET state='accepted',evidence=? WHERE workflow_id='run-p15-capture' AND id='P15'`, `["trace-implementation","trace-review","trace-audit","knowledge_digest:`+digest+`"]`); err != nil {
		t.Fatal(err)
	}
	if err := capture.promote(ctx, mustLoadRun(t, workflowStore, "run-p15-capture")); err != nil {
		t.Fatal(err)
	}
	entries, _ = knowledgeStore.Query(ctx, knowledge.Query{Scope: entries[0].Scope, Permission: permission})
	if entries[0].Status != knowledge.Verified {
		t.Fatalf("accepted candidate not promoted: %+v", entries[0])
	}
	if err := capture.promote(ctx, mustLoadRun(t, workflowStore, "run-p15-capture")); err != nil {
		t.Fatalf("replayed promotion was not idempotent: %v", err)
	}
}

func TestRejectedKnowledgeCandidateCannotRemainAccepted(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workflow.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	step := Step{ID: "implementation", PointID: "P15", PointTitle: "knowledge", TypeName: "implementation", Task: contract.Task{Objective: "implement"}}
	plan := Plan{Graph: Graph{Task: "P15", Steps: []Step{step}}, Pools: map[string]config.Pool{"implementation": config.PoolAgent}}
	if err := store.CreateWithFingerprint(ctx, "run-p15-reject", plan, "repo-a", "tree-a", time.Now().UTC(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE workflow_step SET status='ok',verdict='ok',trace_id='trace' WHERE workflow_id='run-p15-reject' AND id='implementation'`); err != nil {
		t.Fatal(err)
	}
	if err := store.RejectKnowledgeCapture(ctx, "run-p15-reject", "implementation", context.Canceled); err != nil {
		t.Fatal(err)
	}
	run := mustLoadRun(t, store, "run-p15-reject")
	if run.Steps[0].Status != StatusFailed || run.Steps[0].Reason.Kind != contract.FailureInvalidInput || !strings.Contains(run.Steps[0].Reason.Text, "knowledge candidate rejected") {
		t.Fatalf("rejected step = %+v", run.Steps[0])
	}
}

func floatPtr(value float64) *float64 { return &value }

func mustLoadRun(t *testing.T, store *Store, id string) Run {
	t.Helper()
	run, err := store.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}
