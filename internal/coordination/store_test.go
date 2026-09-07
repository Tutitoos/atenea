package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestCoordinatorReceiptIsDurableAndChildBindingIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "coordination.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(10, 0).UTC()
	record, err := store.Create(context.Background(), "inspect repositories", "all claims have evidence", []string{"b", "a"}, contract.Limits{MaxDuration: time.Minute, MaxTokens: 100}, 4, []string{"explore", "plan"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := record.Repositories; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("repositories = %v, want deterministic order", got)
	}
	if _, err := store.BindChild(context.Background(), record.ID, "a", "wf-a", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindChild(context.Background(), record.ID, "a", "wf-a", now); err != nil {
		t.Fatalf("same binding should be idempotent: %v", err)
	}
	if _, err := store.BindChild(context.Background(), record.ID, "a", "wf-other", now); err == nil {
		t.Fatal("conflicting workflow binding was accepted")
	}
	if _, err := store.FinishChild(context.Background(), record.ID, "a", StatusRunning, "", now); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("FinishChild running = %v, want invalid_input", err)
	}
	if _, err := store.FinishChild(context.Background(), record.ID, "a", StatusCompleted, "", now); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Load(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Children) != 1 || loaded.Children[0].WorkflowID != "wf-a" || loaded.Children[0].Status != StatusCompleted {
		t.Fatalf("loaded coordinator = %+v", loaded)
	}
	if !json.Valid(mustRead(t, path)) {
		t.Fatal("coordination state is not valid JSON")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestCoordinatorRejectsMoreThanTwoSpecialistsAndDuplicateRepositories(t *testing.T) {
	record := Record{ID: "coord", Objective: "objective", Criterion: "criterion", Repositories: []string{"repo", "repo"}, Coordinator: "atenea-coordinator", Specialists: []string{"one", "two", "three"}, Status: StatusRunning}
	if err := record.Validate(); err == nil {
		t.Fatal("invalid coordinator topology was accepted")
	}
}

func TestReviewCycleLimitsIdentityAndAcceptanceSurviveReconnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coord.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	record, err := store.Create(t.Context(), "change", "tests pass", []string{"repo"}, contract.Limits{MaxDuration: time.Minute}, 1, []string{"implement", "review"}, now)
	if err != nil {
		t.Fatal(err)
	}
	cycleID := "wf-1:review-1:audit-1"
	if _, err := store.BeginReviewCycle(t.Context(), record.ID, cycleID, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxFollowUps; i++ {
		if _, err := store.RecordFollowUp(t.Context(), record.ID, now.Add(time.Duration(i+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RecordFollowUp(t.Context(), record.ID, now.Add(10*time.Second)); err == nil {
		t.Fatal("fourth follow-up was accepted")
	}
	for i := 0; i < MaxAstraExecutions; i++ {
		if _, err := store.BeginAstraExecution(t.Context(), record.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.BeginAstraExecution(t.Context(), record.ID, now); err == nil {
		t.Fatal("third Astra execution was accepted")
	}
	sol := ReviewReceipt{CycleID: cycleID, WorkflowID: "wf-1", Role: "sol", RunID: "sol-1", Attempt: 1, SourceFingerprint: "tree-1", RequestedModel: "gpt-5.6-sol", ObservedModel: "gpt-5.6-sol", RequestedReasoningEffort: "medium", ObservedReasoningEffort: "medium", Verdict: "ok", Fresh: true, At: now}
	if _, err := store.RecordSolReview(t.Context(), record.ID, sol, now); err != nil {
		t.Fatal(err)
	}
	audit := ReviewReceipt{CycleID: cycleID, WorkflowID: "wf-1", Role: "astra", RunID: "astra-1", Attempt: 1, SourceFingerprint: "tree-1", RequestedModel: "gpt-6-astra", ObservedModel: "gpt-6-astra", RequestedReasoningEffort: "medium", ObservedReasoningEffort: "medium", Verdict: "ok", Fresh: true, At: now}
	if _, err := store.RecordAstraAudit(t.Context(), record.ID, audit, now); err != nil {
		t.Fatal(err)
	}
	accepted, err := store.AcceptReviews(t.Context(), record.ID, now)
	if err != nil || len(accepted.AcceptedCycleIDs) != 1 {
		t.Fatalf("accepted = %+v err=%v", accepted, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Load(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.FollowUps != MaxFollowUps || loaded.AstraExecutions != MaxAstraExecutions || loaded.SolReview == nil || len(loaded.AstraAudits) != 1 {
		t.Fatalf("reloaded cycle = %+v", loaded)
	}
}

func TestReviewCycleRejectsSubstitutionAndMixedCycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coord.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	record, err := store.Create(t.Context(), "change", "tests pass", []string{"repo"}, contract.Limits{MaxDuration: time.Minute}, 1, []string{"implement"}, now)
	if err != nil {
		t.Fatal(err)
	}
	cycleID := "wf-1:review-1:audit-1"
	if _, err := store.BeginReviewCycle(t.Context(), record.ID, cycleID, now); err != nil {
		t.Fatal(err)
	}
	sol := ReviewReceipt{CycleID: cycleID, WorkflowID: "wf-1", Role: "sol", RunID: "sol-1", Attempt: 1, SourceFingerprint: "tree-1", RequestedModel: "gpt-5.6-sol", ObservedModel: "gpt-6-astra", RequestedReasoningEffort: "medium", ObservedReasoningEffort: "medium", Verdict: "ok", Fresh: true, At: now}
	if _, err := store.RecordSolReview(t.Context(), record.ID, sol, now); err == nil {
		t.Fatal("substituted Sol model accepted")
	}
	sol.ObservedModel = sol.RequestedModel
	if _, err := store.RecordSolReview(t.Context(), record.ID, sol, now); err != nil {
		t.Fatal(err)
	}
	audit := ReviewReceipt{CycleID: "older", WorkflowID: "wf-1", Role: "astra", RunID: "a", Attempt: 1, SourceFingerprint: "tree-1", RequestedModel: "gpt-6-astra", ObservedModel: "gpt-6-astra", RequestedReasoningEffort: "medium", ObservedReasoningEffort: "medium", Verdict: "ok", Fresh: true, At: now}
	if _, err := store.RecordAstraAudit(t.Context(), record.ID, audit, now); err == nil {
		t.Fatal("mixed-cycle audit accepted")
	}

	canonicalSol := sol
	canonicalSol.RequestedModel = "gpt-5.6-luna"
	canonicalSol.ObservedModel = "gpt-5.6-luna"
	if _, err := store.RecordSolReview(t.Context(), record.ID, canonicalSol, now); err == nil {
		t.Fatal("Luna receipt accepted for the canonical Sol role")
	}

	audit.CycleID = cycleID
	audit.WorkflowID = "wf-other"
	audit.SourceFingerprint = "tree-other"
	if _, err := store.BeginAstraExecution(t.Context(), record.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordAstraAudit(t.Context(), record.ID, audit, now); err == nil {
		t.Fatal("audit for a different workflow and source accepted")
	}
}

func TestAstraAuditRequiresAPredispatchReservation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "coord.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(250, 0).UTC()
	record, err := store.Create(t.Context(), "change", "tests pass", []string{"repo"}, contract.Limits{MaxDuration: time.Minute}, 1, []string{"review"}, now)
	if err != nil {
		t.Fatal(err)
	}
	cycleID := "wf-1:tree-1"
	if _, err := store.BeginReviewCycle(t.Context(), record.ID, cycleID, now); err != nil {
		t.Fatal(err)
	}
	sol := ReviewReceipt{CycleID: cycleID, WorkflowID: "wf-1", Role: "sol", RunID: "sol-1", Attempt: 1, SourceFingerprint: "tree-1", RequestedModel: "gpt-5.6-sol", ObservedModel: "gpt-5.6-sol", RequestedReasoningEffort: "medium", ObservedReasoningEffort: "medium", Verdict: "ok", Fresh: true, At: now}
	if _, err := store.RecordSolReview(t.Context(), record.ID, sol, now); err != nil {
		t.Fatal(err)
	}
	audit := ReviewReceipt{CycleID: cycleID, WorkflowID: "wf-1", Role: "astra", RunID: "astra-1", Attempt: 1, SourceFingerprint: "tree-1", RequestedModel: "gpt-6-astra", ObservedModel: "gpt-6-astra", RequestedReasoningEffort: "medium", ObservedReasoningEffort: "medium", Verdict: "ok", Fresh: true, At: now}
	if _, err := store.RecordAstraAudit(t.Context(), record.ID, audit, now); err == nil {
		t.Fatal("Astra audit was stored before its execution slot was reserved")
	}
	loaded, err := store.Load(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.AstraAudits) != 0 || loaded.AstraExecutions != 0 {
		t.Fatalf("unreserved audit changed durable state: %+v", loaded)
	}
}

func TestWatchdogPersistsAttentionAndUncertainty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coord.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(300, 0).UTC()
	record, err := store.Create(t.Context(), "read", "answer", []string{"repo"}, contract.Limits{MaxDuration: time.Minute}, 1, []string{"reader"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, tripped, err := store.Watchdog(t.Context(), record.ID, now.Add(WatchdogTimeout+time.Second), WatchdogTimeout, true); err != nil || !tripped {
		t.Fatalf("watchdog tripped=%v err=%v", tripped, err)
	}
	loaded, err := store.Load(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateUncertain || loaded.Status != StatusStopped {
		t.Fatalf("uncertain watchdog record = %+v", loaded)
	}
	if _, tripped, err := store.Watchdog(t.Context(), record.ID, now.Add(2*WatchdogTimeout), WatchdogTimeout, false); err != nil || tripped {
		t.Fatalf("watchdog retried uncertain record: tripped=%v err=%v", tripped, err)
	}
}

func TestCoordinatorStoreSerializesIndependentProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordination.json")
	first, _ := Open(path)
	second, _ := Open(path)
	repositories := make([]string, 20)
	for i := range repositories {
		repositories[i] = fmt.Sprintf("repo-%02d", i)
	}
	record, err := first.Create(t.Context(), "inspect", "complete", repositories,
		contract.Limits{MaxDuration: time.Minute}, 1, []string{"explore"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i, repository := range repositories {
		wg.Add(1)
		go func(i int, repository string) {
			defer wg.Done()
			store := first
			if i%2 == 1 {
				store = second
			}
			if _, err := store.BindChild(t.Context(), record.ID, repository, "wf-"+repository, time.Now()); err != nil {
				t.Errorf("bind %s: %v", repository, err)
			}
		}(i, repository)
	}
	wg.Wait()
	loaded, err := first.Load(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Children) != len(repositories) {
		t.Fatalf("children = %d, want %d", len(loaded.Children), len(repositories))
	}
}

func TestPreparedGraphsSurviveRestartBeforeWorkflowCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordination.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(400, 0).UTC()
	record, err := store.Create(t.Context(), "change", "verified", []string{"repo"}, contract.Limits{MaxDuration: time.Minute}, 1, []string{"implementation", "verification"}, now)
	if err != nil {
		t.Fatal(err)
	}
	root := json.RawMessage(`{"task":"coordinate"}`)
	child := json.RawMessage(`{"task":"implement"}`)
	if _, err := store.BindCoordinatorPrepared(t.Context(), record.ID, "wf-root", root); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindChildPrepared(t.Context(), record.ID, "repo", "wf-child", child, now); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Load(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	var loadedRoot, loadedChild map[string]any
	if len(loaded.Children) != 1 || json.Unmarshal(loaded.CoordinatorGraph, &loadedRoot) != nil || json.Unmarshal(loaded.Children[0].Graph, &loadedChild) != nil || loadedRoot["task"] != "coordinate" || loadedChild["task"] != "implement" {
		t.Fatalf("prepared graphs were not durable: %+v", loaded)
	}
}

func TestPrepareReservesEveryRepositoryAtomicallyAndCompletionRequiresAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordination.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(500, 0).UTC()
	record, err := store.Create(t.Context(), "inspect", "all repositories", []string{"a", "b"}, contract.Limits{MaxDuration: time.Minute}, 1, []string{"explore"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare(t.Context(), record.ID, "wf-root", json.RawMessage(`{"task":"root"}`), []Child{{Repository: "a", WorkflowID: "wf-a", Graph: json.RawMessage(`{"task":"a"}`)}}, now); err == nil {
		t.Fatal("partial preparation was accepted")
	}
	prepared, err := store.Prepare(t.Context(), record.ID, "wf-root", json.RawMessage(`{"task":"root"}`), []Child{
		{Repository: "a", WorkflowID: "wf-a", Graph: json.RawMessage(`{"task":"a"}`)},
		{Repository: "b", WorkflowID: "wf-b", Graph: json.RawMessage(`{"task":"b"}`)},
	}, now)
	if err != nil || len(prepared.Children) != 2 {
		t.Fatalf("prepare = %+v err=%v", prepared, err)
	}
	if _, err := store.SetStatus(t.Context(), record.ID, StatusCompleted, ""); err == nil {
		t.Fatal("coordination completed before its children")
	}
	for _, repository := range []string{"a", "b"} {
		if _, err := store.FinishChild(t.Context(), record.ID, repository, StatusCompleted, "", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SetStatus(t.Context(), record.ID, StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
