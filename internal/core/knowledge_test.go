package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/knowledge"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestCoreOptionalKnowledgeGateAndVerifiedContext(t *testing.T) {
	ctx := context.Background()
	scope := knowledge.Scope{ProjectID: "run-p15", RepositoryID: "repo"}
	wf, err := workflow.Open(ctx, filepath.Join(t.TempDir(), "workflow.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer wf.Close()
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
	steps := []workflow.Step{
		{ID: "implementation", PointID: "P15", PointTitle: "knowledge", TypeName: "implementation", Route: route("implement"), Task: contract.Task{Objective: "implement"}},
		{ID: "review", PointID: "P15", PointTitle: "knowledge", TypeName: "review", Route: route("review"), Subject: "implementation", Task: contract.Task{Objective: "review"}},
		{ID: "audit", PointID: "P15", PointTitle: "knowledge", TypeName: "audit", Route: route("audit"), Subject: "review", Task: contract.Task{Objective: "audit"}},
	}
	plan := workflow.Plan{Graph: workflow.Graph{Task: "P15", Steps: steps}, Pools: map[string]config.Pool{"implementation": config.PoolAgent, "review": config.PoolReview, "audit": config.PoolReview}}
	started := time.Now().UTC()
	if err := wf.CreateWithFingerprint(ctx, scope.ProjectID, plan, scope.RepositoryID, "tree", started, 0); err != nil {
		t.Fatal(err)
	}
	source := knowledge.Source{ID: "workflow:" + scope.ProjectID + ":P15", Kind: "workflow", Locator: "workflow/" + scope.ProjectID + "/P15", Digest: "tree"}
	dependency := knowledge.Dependency{Source: knowledge.Source{ID: "workflow-state:" + scope.ProjectID, Kind: "workflow", Locator: "workflow/status", Digest: "tree"}, Provider: knowledge.ProviderIdentity{Name: "fixture", Version: "gpt-5.6-luna", Instance: scope.ProjectID, ConfigDigest: "tree"}, Generation: 1, Snapshot: "tree", Freshness: "fresh", CheckedAt: started, TTLSeconds: 600}
	entry := knowledge.Entry{ID: "decision", OwnerID: "owner", Scope: scope, Kind: knowledge.Decision, Title: "bounded", Body: "bounded", Visibility: knowledge.Project, Sources: []knowledge.Source{source}, Dependencies: []knowledge.Dependency{dependency}}
	digest := knowledge.KnowledgeDigest(entry)
	for _, id := range []string{"implementation", "review", "audit"} {
		if err := wf.Claim(ctx, scope.ProjectID, id, "trace-"+id, 1, time.Now().UTC(), 1); err != nil {
			t.Fatal(err)
		}
		result := map[string]any{"ok": true}
		if id == "implementation" {
			result["knowledge_digest"] = digest
		}
		if err := wf.Finish(ctx, scope.ProjectID, id, workflow.StatusOK, contract.Report{Result: result, Verdict: contract.VerdictOK, Invoked: true, InvokedKnown: true}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := wf.SyncPlan(ctx, scope.ProjectID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	resolver := workflow.KnowledgeEvidenceResolver{Store: wf}
	if _, _, _, err := wf.SyncPlan(ctx, scope.ProjectID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(ctx, scope.ProjectID, scope, "P15", digest)
	if err != nil {
		t.Fatal(err)
	}
	store, err := knowledge.Open(filepath.Join(t.TempDir(), "knowledge.sqlite"), knowledge.WithEvidenceResolver(resolver))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := &Core{}
	c.ConfigureKnowledge(store)
	perm := knowledge.Permission{SubjectID: "owner", ProjectID: scope.ProjectID, RepositoryID: scope.RepositoryID, Read: true, Write: true, Promote: true, ProjectMember: true}
	if err := store.PutCandidate(ctx, entry, perm); err != nil {
		t.Fatal(err)
	}
	if err := c.PromoteKnowledge(ctx, entry.ID, knowledge.PromotionGate{WorkflowID: scope.ProjectID, Scope: scope, PointID: "P15"}, perm); err != nil {
		t.Fatal(err)
	}
	got, err := c.PrepareKnowledge(ctx, scope, perm, func(context.Context, knowledge.Entry) (knowledge.ProbeResult, error) {
		return knowledge.ProbeResult{Complete: true, Success: true, Sources: entry.Sources, Dependencies: entry.Dependencies}, nil
	})
	if err != nil || len(got) != 1 || got[0].Status != knowledge.Verified {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if got, err := (&Core{}).PrepareKnowledge(ctx, scope, perm, nil); err != nil || got != nil {
		t.Fatalf("unconfigured core got=%v err=%v", got, err)
	}
}

func TestWorkflowKnowledgeProbeRejectsHistoricalUnacceptedRun(t *testing.T) {
	ctx := context.Background()
	wf, err := workflow.Open(ctx, filepath.Join(t.TempDir(), "workflow.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer wf.Close()
	step := workflow.Step{ID: "implementation", PointID: "P15", PointTitle: "knowledge", TypeName: "implementation", Task: contract.Task{Objective: "implement"}}
	plan := workflow.Plan{Graph: workflow.Graph{Task: "P15", Steps: []workflow.Step{step}}, Pools: map[string]config.Pool{"implementation": config.PoolAgent}}
	if err := wf.CreateWithFingerprint(ctx, "unaccepted", plan, "repo", "tree", time.Now().UTC(), 0); err != nil {
		t.Fatal(err)
	}
	entry := knowledge.Entry{ID: "fact", Scope: knowledge.Scope{ProjectID: "repo", RepositoryID: "repo"}, OwnerID: "workflow:unaccepted", Kind: knowledge.Fact, Title: "fact", Body: "body", Visibility: knowledge.Project, Sources: []knowledge.Source{{ID: "workflow:unaccepted:P15", Kind: "workflow", Locator: "workflow/unaccepted/P15", Digest: "tree"}}, Dependencies: []knowledge.Dependency{{Source: knowledge.Source{ID: "workflow-state:unaccepted", Kind: "workflow", Locator: "workflow/status", Digest: "tree"}, Provider: knowledge.ProviderIdentity{Name: "atenea-workflow", Version: "1", Instance: "unaccepted", ConfigDigest: "tree"}, Generation: 1, Snapshot: "tree", Freshness: "fresh", CheckedAt: time.Now().UTC(), TTLSeconds: 600}}}
	entry.KnowledgeDigest = knowledge.KnowledgeDigest(entry)
	if result, err := (&Core{}).probeWorkflowKnowledge(ctx, wf, entry); err == nil || result.Success {
		t.Fatalf("unaccepted workflow renewed knowledge: result=%+v err=%v", result, err)
	}
}
