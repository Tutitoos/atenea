package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/coordination"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestDecideStatusReadsCoordinatorReceiptByPersistentID(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "workflow.db")
	store, err := coordination.Open(coordination.PathFor(tracePath))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(context.Background(), "inspect", "all results are evidenced", []string{"repo"}, contract.Limits{MaxDuration: time.Minute}, 1, []string{"explore"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cmdDecideControl("", []string{"status", record.ID, "--traces", tracePath}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{record.ID, "inspect", "all results are evidenced", "repo", "explore"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status output = %q, missing %q", out.String(), want)
		}
	}
}

func TestCoordinatorReviewCycleUsesDurableWorkflowEvidenceAndIsIdempotent(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "workflow.db")
	store, err := coordination.Open(coordination.PathFor(tracePath))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(t.Context(), "change", "reviewed", []string{"repo"}, contract.Limits{MaxDuration: time.Minute}, 1, []string{"implementation", "verification"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	run := workflow.Run{ID: "wf-1", SourceFingerprint: "tree-1", Steps: []workflow.StepRow{
		{Step: workflow.Step{ID: "review", TypeName: "review", Route: &contract.Route{RequestedModel: "gpt-5.6-sol", ObservedModel: "gpt-5.6-sol", RequestedReasoningEffort: "medium", ObservedReasoningEffort: "medium"}}, Status: workflow.StatusOK, TraceID: "review-1", Attempt: 1, SourceFingerprint: "tree-1", Ended: time.Now().UTC()},
		{Step: workflow.Step{ID: "audit", TypeName: "audit", Route: &contract.Route{RequestedModel: "gpt-6-astra", ObservedModel: "gpt-6-astra", RequestedReasoningEffort: "medium", ObservedReasoningEffort: "medium"}}, Status: workflow.StatusOK, TraceID: "audit-1", Attempt: 1, SourceFingerprint: "tree-1", Ended: time.Now().UTC()},
	}}
	if _, err := store.BeginReviewCycle(t.Context(), record.ID, coordinatorReviewCycleID(run.ID, "tree-1"), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginAstraExecution(t.Context(), record.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := recordCoordinatorReviewCycle(t.Context(), store, record.ID, run); err != nil {
		t.Fatal(err)
	}
	if err := recordCoordinatorReviewCycle(t.Context(), store, record.ID, run); err != nil {
		t.Fatalf("replayed accepted cycle: %v", err)
	}
	loaded, err := store.Load(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AstraExecutions != 1 || len(loaded.AcceptedCycleIDs) != 1 || loaded.SolReview == nil || loaded.SolReview.WorkflowID != run.ID {
		t.Fatalf("review cycle = %+v", loaded)
	}
}
