package codexcert

import (
	"context"
	"errors"
	"testing"

	adaptercodex "github.com/Tutitoos/atenea/internal/adapter/codex"
)

type identityFake struct {
	accountErr       error
	missingModel     string
	missingEffort    string
	mismatchedEffort bool
	rerouted         bool
	turnStatus       string
	completedThread  string
	receiptTurn      string
	current          Profile
	threadID         string
	turnID           string
}

func (f *identityFake) Initialize(context.Context, adaptercodex.InitializeRequest) (adaptercodex.InitializeResult, error) {
	return adaptercodex.InitializeResult{CodexHome: "/isolated", PlatformFamily: "unix", PlatformOS: "macos", UserAgent: "codex-test"}, nil
}
func (f *identityFake) AccountRead(context.Context) (adaptercodex.AccountReadResult, error) {
	if f.accountErr != nil {
		return adaptercodex.AccountReadResult{}, f.accountErr
	}
	return adaptercodex.AccountReadResult{}, nil
}
func (f *identityFake) ModelList(context.Context) (adaptercodex.ModelListResult, error) {
	seen := map[string]*adaptercodex.Model{}
	for _, p := range RequiredProfiles {
		if p.Model == f.missingModel {
			continue
		}
		model := seen[p.Model]
		if model == nil {
			seen[p.Model] = &adaptercodex.Model{Model: p.Model}
			model = seen[p.Model]
		}
		if p.Effort != f.missingEffort {
			model.SupportedReasoningEfforts = append(model.SupportedReasoningEfforts, adaptercodex.ReasoningEffortOption{ReasoningEffort: p.Effort})
		}
	}
	out := adaptercodex.ModelListResult{}
	for _, model := range seen {
		out.Data = append(out.Data, *model)
	}
	return out, nil
}
func (f *identityFake) ThreadStart(_ context.Context, req adaptercodex.ThreadStartRequest) (adaptercodex.ThreadStarted, error) {
	f.current.Model = req.Model
	f.threadID = "thread-" + req.Model
	return adaptercodex.ThreadStarted{Thread: adaptercodex.Thread{ID: f.threadID, Status: adaptercodex.ThreadStatus{Type: "idle"}}, Model: req.Model, ModelProvider: "openai"}, nil
}
func (f *identityFake) TurnStart(_ context.Context, req adaptercodex.TurnStartRequest) (adaptercodex.TurnStarted, error) {
	f.current.Effort = req.ReasoningEffort
	f.turnID = "turn-" + req.ReasoningEffort
	return adaptercodex.TurnStarted{Turn: adaptercodex.Turn{ID: f.turnID, ThreadID: f.threadID}}, nil
}
func (f *identityFake) WaitTurn(context.Context, string, string) (adaptercodex.TurnObservation, error) {
	status := f.turnStatus
	if status == "" {
		status = "completed"
	}
	completedThread := f.threadID
	if f.completedThread != "" {
		completedThread = f.completedThread
	}
	obs := adaptercodex.TurnObservation{Completed: &adaptercodex.TurnCompleted{ThreadID: completedThread, Turn: adaptercodex.Turn{ID: f.turnID, Status: status}}, UsageRevision: 1}
	if f.rerouted {
		obs.Reroute = &adaptercodex.ModelRerouted{FromModel: f.current.Model, ToModel: "other"}
	}
	return obs, nil
}
func (f *identityFake) ThreadRead(context.Context, string) (adaptercodex.Thread, error) {
	effort := f.current.Effort
	if f.mismatchedEffort {
		effort = "low"
	}
	return adaptercodex.Thread{ID: f.threadID, Status: adaptercodex.ThreadStatus{Type: "idle"}, Model: f.current.Model, ModelProvider: "openai", ReasoningEffort: effort}, nil
}
func (f *identityFake) Receipt() adaptercodex.ExecutionReceipt {
	effort := f.current.Effort
	if f.mismatchedEffort {
		effort = "low"
	}
	receiptTurn := f.turnID
	if f.receiptTurn != "" {
		receiptTurn = f.receiptTurn
	}
	return adaptercodex.ExecutionReceipt{ObservedModel: f.current.Model, ObservedEffort: effort, ObservedProvider: "openai", ObservedSource: "app_server_thread_receipt", ThreadID: f.threadID, TurnID: receiptTurn, ModelRerouted: f.rerouted}
}

func TestIdentityGateRequiresEveryObservableProfile(t *testing.T) {
	receipts, err := RunIdentityGate(t.Context(), &identityFake{}, t.TempDir())
	if err != nil || len(receipts) != len(RequiredProfiles) {
		t.Fatalf("receipts=%d err=%v", len(receipts), err)
	}
	tests := []struct {
		name string
		fake identityFake
	}{
		{"account", identityFake{accountErr: errors.New("not authenticated")}},
		{"model", identityFake{missingModel: "gpt-6-astra"}},
		{"effort", identityFake{missingEffort: "xhigh"}},
		{"observed effort", identityFake{mismatchedEffort: true}},
		{"reroute", identityFake{rerouted: true}},
		{"failed turn", identityFake{turnStatus: "failed"}},
		{"completed thread mismatch", identityFake{completedThread: "other"}},
		{"receipt turn mismatch", identityFake{receiptTurn: "other"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := RunIdentityGate(t.Context(), &tt.fake, t.TempDir()); err == nil {
				t.Fatal("invalid identity evidence passed")
			}
		})
	}
}
