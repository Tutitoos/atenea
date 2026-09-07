package workflow_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/workflow"
)

func TestTelemetryLedgerIsIdempotentAndSeparatesOverhead(t *testing.T) {
	store, err := workflow.Open(t.Context(), filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	receipt := workflow.AgentRunReceipt{WorkflowID: "wf-1", PointID: "P06", Agent: "review", AgentRunID: "run-1", InvocationID: "run-1", ThreadID: "thread-1", TurnID: "turn-1", UsageRevision: 2, Model: "gpt-5.6-sol", Started: now.Add(-time.Second), Ended: now, Duration: time.Second, Tokens: workflow.TokenDelta{InputTokens: 10, OutputTokens: 5}, State: workflow.MeasurementMeasured, Savings: workflow.SavingsEstimate{EstimatedTokens: 30, ActualTokens: 15, EstimatedDuration: 2 * time.Second, ActualDuration: time.Second, State: workflow.MeasurementEstimated}}
	if inserted, err := store.RecordAgentRunReceipt(t.Context(), receipt); err != nil || !inserted {
		t.Fatalf("first=%v err=%v", inserted, err)
	}
	partial := receipt
	partial.AgentRunID = "run-2"
	partial.InvocationID = "run-2"
	partial.TurnID = "turn-2"
	partial.UsageRevision = 1
	partial.State = workflow.MeasurementPartial
	partial.Savings = workflow.SavingsEstimate{}
	if inserted, err := store.RecordAgentRunReceipt(t.Context(), partial); err != nil || !inserted {
		t.Fatalf("partial=%v err=%v", inserted, err)
	}
	if inserted, err := store.RecordAgentRunReceipt(t.Context(), receipt); err != nil || inserted {
		t.Fatalf("duplicate=%v err=%v", inserted, err)
	}
	conflict := receipt
	conflict.Tokens.OutputTokens++
	if _, err := store.RecordAgentRunReceipt(t.Context(), conflict); err == nil {
		t.Fatal("conflicting duplicate was accepted")
	}
	tool := workflow.ToolUseReceipt{WorkflowID: "wf-1", AgentRunID: "overhead-1", InvocationID: "tool-1", Tool: "workflow.status", Duration: 10 * time.Millisecond, State: workflow.MeasurementUnknown}
	if inserted, err := store.RecordToolUseReceipt(t.Context(), tool); err != nil || !inserted {
		t.Fatalf("tool=%v err=%v", inserted, err)
	}
	points, err := store.PointTelemetry(t.Context(), "wf-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].PointID != "P06" || points[0].AgentRuns != 2 || points[0].InputTokens != 20 || points[0].Partial != 1 || points[1].PointID != workflow.WorkflowOverheadPoint || points[1].ToolUses != 1 {
		t.Fatalf("points=%#v", points)
	}
	if points[0].Savings.EstimatedTokens != 30 || points[0].Savings.ActualTokens != 15 || points[0].Savings.EstimatedDuration != 2*time.Second || points[0].Savings.ActualDuration != time.Second {
		t.Fatalf("savings lost estimate/actual distinction: %#v", points[0].Savings)
	}
	score, err := store.AgentScorecard(t.Context())
	if err != nil || len(score) != 1 || score[0].Agent != "review" || score[0].Tokens != 30 || score[0].Partial != 1 {
		t.Fatalf("score=%#v err=%v", score, err)
	}
}
