package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// MeasurementState says how a receipt was obtained. Unknown is retained as a
// first-class value: a missing provider usage event is not a measured zero.
type MeasurementState string

const (
	// MeasurementMeasured is part of ATENEA's public orchestration contract.
	MeasurementMeasured MeasurementState = "measured"
	// MeasurementEstimated is part of ATENEA's public orchestration contract.
	MeasurementEstimated MeasurementState = "estimated"
	// MeasurementPartial is part of ATENEA's public orchestration contract.
	MeasurementPartial MeasurementState = "partial"
	// MeasurementUnknown is part of ATENEA's public orchestration contract.
	MeasurementUnknown MeasurementState = "unknown"
	// WorkflowOverheadPoint is part of ATENEA's public orchestration contract.
	WorkflowOverheadPoint = "workflow-overhead"
)

func (s MeasurementState) valid() bool {
	switch s {
	case MeasurementMeasured, MeasurementEstimated, MeasurementPartial, MeasurementUnknown:
		return true
	default:
		return false
	}
}

// TokenDelta is the usage increment represented by one receipt. A Codex
// usage revision is cumulative on the wire; adapters convert it to this
// delta before recording it.
type TokenDelta struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

func (d TokenDelta) valid() bool {
	return d.InputTokens >= 0 && d.OutputTokens >= 0 && d.CacheReadTokens >= 0 && d.CacheWriteTokens >= 0
}

// SavingsEstimate keeps estimated savings separate from measured spend.
type SavingsEstimate struct {
	EstimatedTokens   int64            `json:"estimated_tokens,omitempty"`
	ActualTokens      int64            `json:"actual_tokens,omitempty"`
	EstimatedDuration time.Duration    `json:"estimated_duration,omitempty"`
	ActualDuration    time.Duration    `json:"actual_duration,omitempty"`
	Basis             string           `json:"basis,omitempty"`
	State             MeasurementState `json:"state"`
}

// AgentRunReceipt is the durable receipt for one physical agent invocation.
// Its identity tuple is also the idempotency key for the usage ledger.
type AgentRunReceipt struct {
	WorkflowID    string
	PointID       string
	Agent         string
	AgentRunID    string
	InvocationID  string
	ThreadID      string
	TurnID        string
	UsageRevision uint64
	Model         string
	Started       time.Time
	Ended         time.Time
	Duration      time.Duration
	Tokens        TokenDelta
	State         MeasurementState
	Savings       SavingsEstimate
}

// ToolUseReceipt records a tool call independently from the model turn that
// requested it. This prevents tool latency from being hidden in model cost.
type ToolUseReceipt struct {
	WorkflowID    string
	PointID       string
	AgentRunID    string
	InvocationID  string
	ThreadID      string
	TurnID        string
	UsageRevision uint64
	Tool          string
	Provider      string
	Started       time.Time
	Ended         time.Time
	Duration      time.Duration
	Tokens        TokenDelta
	State         MeasurementState
	Savings       SavingsEstimate
}

// PlanPointTelemetry is a deterministic aggregate returned by status/export.
type PlanPointTelemetry struct {
	WorkflowID       string          `json:"workflow_id"`
	PointID          string          `json:"point_id"`
	AgentRuns        int             `json:"agent_runs"`
	ToolUses         int             `json:"tool_uses"`
	AgentDuration    time.Duration   `json:"agent_duration"`
	ToolDuration     time.Duration   `json:"tool_duration"`
	InputTokens      int64           `json:"input_tokens"`
	OutputTokens     int64           `json:"output_tokens"`
	CacheReadTokens  int64           `json:"cache_read_tokens"`
	CacheWriteTokens int64           `json:"cache_write_tokens"`
	Measured         int             `json:"measured"`
	Estimated        int             `json:"estimated"`
	Unknown          int             `json:"unknown"`
	Partial          int             `json:"partial"`
	Savings          SavingsEstimate `json:"savings"`
}

// AgentScorecardRow is the compact cross-workflow view used by agents
// scorecard. Rows are sorted by agent name before being returned.
type AgentScorecardRow struct {
	Agent     string        `json:"agent"`
	Runs      int           `json:"runs"`
	Duration  time.Duration `json:"duration"`
	Tokens    int64         `json:"tokens"`
	Measured  int           `json:"measured"`
	Estimated int           `json:"estimated"`
	Partial   int           `json:"partial"`
	Unknown   int           `json:"unknown"`
}

const telemetrySchema = `
CREATE TABLE IF NOT EXISTS workflow_telemetry (
 receipt_kind TEXT NOT NULL,
 workflow_id TEXT NOT NULL,
 point_id TEXT NOT NULL,
 agent TEXT NOT NULL DEFAULT '',
 agent_run_id TEXT NOT NULL,
 invocation_id TEXT NOT NULL,
 thread_id TEXT NOT NULL DEFAULT '',
 turn_id TEXT NOT NULL DEFAULT '',
 usage_revision INTEGER NOT NULL DEFAULT 0,
 model TEXT NOT NULL DEFAULT '',
 tool TEXT NOT NULL DEFAULT '',
 provider TEXT NOT NULL DEFAULT '',
 started_at TEXT NOT NULL DEFAULT '',
 ended_at TEXT NOT NULL DEFAULT '',
 duration_ns INTEGER NOT NULL DEFAULT 0,
 input_tokens INTEGER NOT NULL DEFAULT 0,
 output_tokens INTEGER NOT NULL DEFAULT 0,
 cache_read_tokens INTEGER NOT NULL DEFAULT 0,
 cache_write_tokens INTEGER NOT NULL DEFAULT 0,
 measurement_state TEXT NOT NULL,
 estimated_tokens INTEGER NOT NULL DEFAULT 0,
 actual_tokens INTEGER NOT NULL DEFAULT 0,
 estimated_duration_ns INTEGER NOT NULL DEFAULT 0,
 actual_duration_ns INTEGER NOT NULL DEFAULT 0,
 savings_basis TEXT NOT NULL DEFAULT '',
 payload_digest TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY (receipt_kind,workflow_id,agent_run_id,thread_id,turn_id,usage_revision,invocation_id)
);
CREATE INDEX IF NOT EXISTS workflow_telemetry_workflow ON workflow_telemetry(workflow_id,point_id);
CREATE INDEX IF NOT EXISTS workflow_telemetry_agent ON workflow_telemetry(agent,created_at);
`

// RecordAgentRunReceipt is part of ATENEA's public orchestration contract.
func (s *Store) RecordAgentRunReceipt(ctx context.Context, receipt AgentRunReceipt) (bool, error) {
	return s.recordTelemetry(ctx, "agent", receipt.WorkflowID, receipt.PointID, receipt.Agent,
		receipt.AgentRunID, receipt.InvocationID, receipt.ThreadID, receipt.TurnID, receipt.UsageRevision,
		receipt.Model, "", "", receipt.Started, receipt.Ended, receipt.Duration, receipt.Tokens,
		receipt.State, receipt.Savings)
}

// RecordToolUseReceipt is part of ATENEA's public orchestration contract.
func (s *Store) RecordToolUseReceipt(ctx context.Context, receipt ToolUseReceipt) (bool, error) {
	return s.recordTelemetry(ctx, "tool", receipt.WorkflowID, receipt.PointID, "", receipt.AgentRunID,
		receipt.InvocationID, receipt.ThreadID, receipt.TurnID, receipt.UsageRevision, "", receipt.Tool,
		receipt.Provider, receipt.Started, receipt.Ended, receipt.Duration, receipt.Tokens, receipt.State, receipt.Savings)
}

func (s *Store) recordTelemetry(ctx context.Context, kind, workflowID, pointID, agent, agentRunID, invocationID, threadID, turnID string,
	usageRevision uint64, model, tool, provider string, started, ended time.Time, duration time.Duration, tokens TokenDelta,
	state MeasurementState, savings SavingsEstimate) (bool, error) {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(agentRunID) == "" || strings.TrimSpace(invocationID) == "" {
		return false, contract.Fail(contract.FailureInvalidInput, "telemetry receipt requires workflow, agent run and invocation ids")
	}
	if strings.TrimSpace(pointID) == "" {
		pointID = WorkflowOverheadPoint
	}
	if duration < 0 || !tokens.valid() || !state.valid() || savings.State != "" && !savings.State.valid() {
		return false, contract.Fail(contract.FailureInvalidInput, "invalid telemetry receipt")
	}
	payloadRaw, err := json.Marshal(struct {
		Kind, WorkflowID, PointID, Agent, AgentRunID, InvocationID, ThreadID, TurnID string
		UsageRevision                                                                uint64
		Model, Tool, Provider                                                        string
		Started, Ended                                                               time.Time
		Duration                                                                     time.Duration
		Tokens                                                                       TokenDelta
		State                                                                        MeasurementState
		Savings                                                                      SavingsEstimate
	}{kind, workflowID, pointID, agent, agentRunID, invocationID, threadID, turnID, usageRevision, model, tool, provider, started, ended, duration, tokens, state, savings})
	if err != nil {
		return false, contract.Fail(contract.FailureInvalidInput, "invalid telemetry receipt: %v", err)
	}
	digest := sha256.Sum256(payloadRaw)
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM workflow_telemetry WHERE created_at<>'' AND created_at<?`, stamp(now.Add(-90*24*time.Hour))); err != nil {
		return false, unavailable(err, "workflow: pruning telemetry")
	}
	digestText := hex.EncodeToString(digest[:])
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_telemetry
		(receipt_kind,workflow_id,point_id,agent,agent_run_id,invocation_id,thread_id,turn_id,usage_revision,model,tool,provider,
		 started_at,ended_at,duration_ns,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,measurement_state,
		 estimated_tokens,actual_tokens,estimated_duration_ns,actual_duration_ns,savings_basis,payload_digest,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, kind, workflowID, pointID, agent, agentRunID, invocationID,
		threadID, turnID, usageRevision, model, tool, provider, stamp(started), stamp(ended), duration.Nanoseconds(), tokens.InputTokens,
		tokens.OutputTokens, tokens.CacheReadTokens, tokens.CacheWriteTokens, state, savings.EstimatedTokens, savings.ActualTokens,
		savings.EstimatedDuration.Nanoseconds(), savings.ActualDuration.Nanoseconds(), savings.Basis, digestText, stamp(now))
	if err != nil {
		return false, unavailable(err, "workflow: recording telemetry")
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 1 {
		return inserted == 1, err
	}
	var existing string
	err = s.db.QueryRowContext(ctx, `SELECT payload_digest FROM workflow_telemetry WHERE receipt_kind=? AND workflow_id=? AND agent_run_id=? AND thread_id=? AND turn_id=? AND usage_revision=? AND invocation_id=?`, kind, workflowID, agentRunID, threadID, turnID, usageRevision, invocationID).Scan(&existing)
	if err != nil {
		return false, unavailable(err, "workflow: reading duplicate telemetry")
	}
	if existing != digestText {
		return false, contract.Fail(contract.FailureInvalidInput, "telemetry identity was reused with a different payload")
	}
	return false, nil
}

// PointTelemetry reads only durable receipts and returns points in stable
// lexical order. No model or provider is queried to render this view.
func (s *Store) PointTelemetry(ctx context.Context, workflowID string) ([]PlanPointTelemetry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT point_id,
		SUM(receipt_kind='agent'),SUM(receipt_kind='tool'),
		SUM(CASE WHEN receipt_kind='agent' THEN duration_ns ELSE 0 END),SUM(CASE WHEN receipt_kind='tool' THEN duration_ns ELSE 0 END),
		SUM(input_tokens),SUM(output_tokens),SUM(cache_read_tokens),SUM(cache_write_tokens),
		SUM(measurement_state='measured'),SUM(measurement_state='estimated'),SUM(measurement_state='unknown'),SUM(measurement_state='partial'),
		SUM(estimated_tokens),SUM(actual_tokens),SUM(estimated_duration_ns),SUM(actual_duration_ns)
		FROM workflow_telemetry WHERE workflow_id=? GROUP BY point_id ORDER BY point_id`, workflowID)
	if err != nil {
		return nil, unavailable(err, "workflow: reading telemetry")
	}
	defer func() { _ = rows.Close() }()
	var out []PlanPointTelemetry
	for rows.Next() {
		var p PlanPointTelemetry
		var estimatedTokens, actualTokens, estimatedDuration, actualDuration int64
		if err := rows.Scan(&p.PointID, &p.AgentRuns, &p.ToolUses, &p.AgentDuration, &p.ToolDuration,
			&p.InputTokens, &p.OutputTokens, &p.CacheReadTokens, &p.CacheWriteTokens, &p.Measured, &p.Estimated, &p.Unknown, &p.Partial,
			&estimatedTokens, &actualTokens, &estimatedDuration, &actualDuration); err != nil {
			return nil, unavailable(err, "workflow: scanning telemetry")
		}
		p.WorkflowID = workflowID
		p.Savings = SavingsEstimate{EstimatedTokens: estimatedTokens, ActualTokens: actualTokens,
			EstimatedDuration: time.Duration(estimatedDuration), ActualDuration: time.Duration(actualDuration), State: MeasurementEstimated}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AgentScorecard is part of ATENEA's public orchestration contract.
func (s *Store) AgentScorecard(ctx context.Context) ([]AgentScorecardRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent,COUNT(*),SUM(duration_ns),SUM(input_tokens+output_tokens+cache_read_tokens+cache_write_tokens),
		SUM(measurement_state='measured'),SUM(measurement_state='estimated'),SUM(measurement_state='partial'),SUM(measurement_state='unknown')
		FROM workflow_telemetry WHERE receipt_kind='agent' AND agent<>'' GROUP BY agent ORDER BY agent`)
	if err != nil {
		return nil, unavailable(err, "workflow: reading agent scorecard")
	}
	defer func() { _ = rows.Close() }()
	var out []AgentScorecardRow
	for rows.Next() {
		var row AgentScorecardRow
		if err := rows.Scan(&row.Agent, &row.Runs, &row.Duration, &row.Tokens, &row.Measured, &row.Estimated, &row.Partial, &row.Unknown); err != nil {
			return nil, unavailable(err, "workflow: scanning agent scorecard")
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
