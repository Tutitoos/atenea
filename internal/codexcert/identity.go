package codexcert

import (
	"context"
	"errors"
	"fmt"
	"time"

	adaptercodex "github.com/Tutitoos/atenea/internal/adapter/codex"
)

// Profile is one required ATENEA Codex role assignment.
type Profile struct{ Role, Model, Effort string }

// RequiredProfiles is the fixed certification matrix.
var RequiredProfiles = []Profile{
	{Role: "planning", Model: "gpt-5.6-sol", Effort: "medium"},
	{Role: "implementation", Model: "gpt-5.6-luna", Effort: "xhigh"},
	{Role: "review", Model: "gpt-5.6-sol", Effort: "medium"},
	{Role: "audit", Model: "gpt-6-astra", Effort: "medium"},
}

// AppServer is the observable protocol surface used by the identity gate.
type AppServer interface {
	Initialize(context.Context, adaptercodex.InitializeRequest) (adaptercodex.InitializeResult, error)
	AccountRead(context.Context) (adaptercodex.AccountReadResult, error)
	ModelList(context.Context) (adaptercodex.ModelListResult, error)
	ThreadStart(context.Context, adaptercodex.ThreadStartRequest) (adaptercodex.ThreadStarted, error)
	TurnStart(context.Context, adaptercodex.TurnStartRequest) (adaptercodex.TurnStarted, error)
	WaitTurn(context.Context, string, string) (adaptercodex.TurnObservation, error)
	ThreadRead(context.Context, string) (adaptercodex.Thread, error)
	Receipt() adaptercodex.ExecutionReceipt
}

// RunIdentityGate executes and correlates every required profile.
func RunIdentityGate(ctx context.Context, client AppServer, workdir string) ([]ProfileReceipt, error) {
	if _, err := client.Initialize(ctx, adaptercodex.InitializeRequest{ClientInfo: adaptercodex.ClientInfo{Name: "atenea-codex-certification", Version: "1"}}); err != nil {
		return nil, err
	}
	if _, err := client.AccountRead(ctx); err != nil {
		return nil, err
	}
	models, err := client.ModelList(ctx)
	if err != nil {
		return nil, err
	}
	available := map[string]map[string]bool{}
	for _, model := range models.Data {
		efforts := map[string]bool{}
		for _, effort := range model.SupportedReasoningEfforts {
			efforts[effort.ReasoningEffort] = true
		}
		available[model.Model] = efforts
	}
	for _, required := range RequiredProfiles {
		if !available[required.Model][required.Effort] {
			return nil, fmt.Errorf("codex profile unavailable: %s %s", required.Model, required.Effort)
		}
	}
	receipts := make([]ProfileReceipt, 0, len(RequiredProfiles))
	for _, profile := range RequiredProfiles {
		started, err := client.ThreadStart(ctx, adaptercodex.ThreadStartRequest{Model: profile.Model, Workdir: workdir, Sandbox: "read-only", ApprovalPolicy: "never", DeveloperInstructions: "Certification probe only. Do not call tools. Reply only with OK.", VisibilityRequired: true})
		if err != nil {
			return nil, err
		}
		if started.ModelProvider != "openai" || started.Model != profile.Model {
			return nil, fmt.Errorf("observable start identity mismatch for %s", profile.Role)
		}
		turn, err := client.TurnStart(ctx, adaptercodex.TurnStartRequest{ThreadID: started.Thread.ID, Prompt: "Reply only with OK.", Model: profile.Model, ReasoningEffort: profile.Effort, VisibilityRequired: true})
		if err != nil {
			return nil, err
		}
		if turn.Turn.ID == "" || turn.Turn.ThreadID != started.Thread.ID {
			return nil, fmt.Errorf("turn/start correlation mismatch for %s", profile.Role)
		}
		observation, err := client.WaitTurn(ctx, started.Thread.ID, turn.Turn.ID)
		if err != nil {
			return nil, err
		}
		if observation.Reroute != nil {
			return nil, errors.New("codex rerouted a certification turn")
		}
		thread, err := client.ThreadRead(ctx, started.Thread.ID)
		if err != nil {
			return nil, err
		}
		if thread.Model != profile.Model || thread.ReasoningEffort != profile.Effort || thread.ModelProvider != "openai" {
			return nil, fmt.Errorf("post-turn observable identity mismatch for %s", profile.Role)
		}
		receipt := client.Receipt()
		if receipt.ObservedModel != profile.Model || receipt.ObservedEffort != profile.Effort || receipt.ObservedProvider != "openai" || receipt.ObservedSource != "app_server_thread_receipt" || receipt.ModelRerouted {
			return nil, fmt.Errorf("incomplete App Server receipt for %s", profile.Role)
		}
		if receipt.ThreadID != started.Thread.ID || receipt.TurnID != turn.Turn.ID || observation.Completed == nil || observation.Completed.ThreadID != started.Thread.ID || observation.Completed.Turn.ID != turn.Turn.ID || observation.Completed.Turn.Status != "completed" || observation.UsageRevision == 0 {
			return nil, fmt.Errorf("uncorrelated App Server evidence for %s", profile.Role)
		}
		receipts = append(receipts, ProfileReceipt{Role: profile.Role, RequestedModel: profile.Model, ObservedModel: receipt.ObservedModel, RequestedEffort: profile.Effort, ObservedEffort: receipt.ObservedEffort, ObservedProvider: receipt.ObservedProvider, ObservedSource: receipt.ObservedSource, ThreadHash: Hash(receipt.ThreadID), TurnHash: Hash(receipt.TurnID), UsageRevision: observation.UsageRevision, Rerouted: false})
	}
	return receipts, nil
}

// PassedGate creates a timestamped successful gate.
func PassedGate(id string) Gate {
	return Gate{State: Passed, EvidenceID: id, CheckedAt: time.Now().UTC().Truncate(time.Second)}
}

// FailedGate creates a bounded, path-sanitized failure record.
func FailedGate(err error) Gate {
	reason := "gate_failed"
	if err != nil {
		reason += ":" + Hash(err.Error())[:16]
	}
	return Gate{State: Failed, CheckedAt: time.Now().UTC().Truncate(time.Second), Reason: reason}
}

// PendingGate records a retryable observation without accepting or permanently
// invalidating the gate. Desktop uses it when the expected chat is not visible.
func PendingGate(err error) Gate {
	reason := "gate_pending"
	if err != nil {
		reason += ":" + Hash(err.Error())[:16]
	}
	return Gate{State: Pending, CheckedAt: time.Now().UTC().Truncate(time.Second), Reason: reason}
}
