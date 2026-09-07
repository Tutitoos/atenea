package workflow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/knowledge"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestKnowledgeResolverReadsAcceptedWorkflowAndPromotesFreshContext(t *testing.T) {
	ctx := context.Background()
	workflowStore, err := Open(ctx, filepath.Join(t.TempDir(), "workflow.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer workflowStore.Close()
	route := func(role string) *contract.Route {
		model, effort := "gpt-5.6-luna", "xhigh"
		if role == "review" {
			model, effort = "gpt-5.6-sol", "medium"
		}
		if role == "audit" {
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
	started := time.Now().UTC()
	if err := workflowStore.CreateWithFingerprint(ctx, "run-p15", plan, "repo-a", "tree-a", started, 0); err != nil {
		t.Fatal(err)
	}
	scope := knowledge.Scope{ProjectID: "run-p15", RepositoryID: "repo-a"}
	source := knowledge.Source{ID: "workflow:run-p15:P15", Kind: "workflow", Locator: "workflow/run-p15/P15", Digest: "tree-a"}
	dependency := knowledge.Dependency{Source: knowledge.Source{ID: "workflow-state:run-p15", Kind: "workflow", Locator: "workflow/status", Digest: "tree-a"}, Provider: knowledge.ProviderIdentity{Name: "fixture", Version: "gpt-5.6-luna", Instance: "run-p15", ConfigDigest: "tree-a"}, Generation: 1, Snapshot: "tree-a", Freshness: "fresh", CheckedAt: started, TTLSeconds: 600}
	entry := knowledge.Entry{ID: "decision", OwnerID: "owner", Scope: scope, Kind: knowledge.Decision, Title: "bounded", Body: "bounded", Visibility: knowledge.Project, Sources: []knowledge.Source{source}, Dependencies: []knowledge.Dependency{dependency}}
	digest := knowledge.KnowledgeDigest(entry)
	for _, id := range []string{"implementation", "review", "audit"} {
		result := `{"decision":"approved"}`
		if id == "implementation" {
			result = `{"knowledge_digest":"` + digest + `"}`
		}
		if _, err := workflowStore.db.ExecContext(ctx, `UPDATE workflow_step SET status='ok',verdict='ok',invoked=1,invoked_known=1,trace_id=?,source_fingerprint='tree-a',result=? WHERE workflow_id='run-p15' AND id=?`, "trace-"+id, result, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := workflowStore.db.ExecContext(ctx, `UPDATE workflow_point SET state='accepted',evidence=? WHERE workflow_id='run-p15' AND id='P15'`, `[`+`"trace-implementation","trace-review","trace-audit","knowledge_digest:`+digest+`"`+`]`); err != nil {
		t.Fatal(err)
	}
	resolver := KnowledgeEvidenceResolver{Store: workflowStore}
	bundle, err := resolver.Resolve(ctx, "run-p15", scope, "P15", digest)
	if err != nil {
		t.Fatal(err)
	}
	if got := bundle.Acceptance.Dependencies[0].Provider.Name; got != "atenea-workflow" {
		t.Fatalf("workflow dependency provider = %q, want atenea-workflow", got)
	}
	if _, err := workflowStore.db.ExecContext(ctx, `UPDATE workflow_step SET result='{}' WHERE workflow_id='run-p15' AND id='review'`); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(ctx, "run-p15", scope, "P15", digest); err == nil {
		t.Fatal("empty review report was accepted")
	}
	if _, err := workflowStore.db.ExecContext(ctx, `UPDATE workflow_step SET result='{"decision":"approved"}' WHERE workflow_id='run-p15' AND id='review'`); err != nil {
		t.Fatal(err)
	}
	knowledgeStore, err := knowledge.Open(filepath.Join(t.TempDir(), "knowledge.sqlite"), knowledge.WithEvidenceResolver(resolver))
	if err != nil {
		t.Fatal(err)
	}
	defer knowledgeStore.Close()
	perm := knowledge.Permission{SubjectID: "owner", ProjectID: "run-p15", RepositoryID: "repo-a", Read: true, Write: true, Promote: true, ProjectMember: true}
	if err := knowledgeStore.PutCandidate(ctx, entry, perm); err != nil {
		t.Fatal(err)
	}
	if err := knowledgeStore.PromoteVerified(ctx, entry.ID, knowledge.PromotionGate{WorkflowID: "run-p15", Scope: entry.Scope, PointID: "P15"}, perm); err != nil {
		t.Fatal(err)
	}
	prepared, err := (knowledge.ContextProvider{Store: knowledgeStore, Scope: entry.Scope, Permission: perm, Probe: func(context.Context, knowledge.Entry) (knowledge.ProbeResult, error) {
		return knowledge.ProbeResult{Complete: true, Success: true, Sources: entry.Sources, Dependencies: entry.Dependencies}, nil
	}}).Prepare(ctx)
	if err != nil || len(prepared) != 1 || prepared[0].Status != knowledge.Verified {
		t.Fatalf("prepared=%+v err=%v", prepared, err)
	}
}
