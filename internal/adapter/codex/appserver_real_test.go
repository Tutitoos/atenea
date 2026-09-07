package codex

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestRealAppServerThreadFork is an opt-in provider-real gate. It creates a
// durable parent, completes the minimal turn needed to persist it, and forks a
// child without sending repository content.
func TestRealAppServerThreadFork(t *testing.T) {
	if os.Getenv("ATENEA_TEST_REAL_CODEX_APP_SERVER") != "1" {
		t.Skip("set ATENEA_TEST_REAL_CODEX_APP_SERVER=1 for provider-real validation")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	client, err := NewAppServerClient(AppServerOptions{
		Binary: binary, IsolateAmbientHooks: true, TrustAteneaHook: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Initialize(ctx, InitializeRequest{ClientInfo: ClientInfo{Name: "atenea-provider-real-test", Version: "1"}}); err != nil {
		t.Fatal(err)
	}
	models, err := client.ModelList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const model = "gpt-5.6-sol"
	found := false
	for _, candidate := range models.Data {
		if candidate.Model == model {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("required model %q is unavailable", model)
	}
	parent, err := client.ThreadStart(ctx, ThreadStartRequest{Model: model, Workdir: t.TempDir(), Sandbox: "read-only", ApprovalPolicy: "never", DeveloperInstructions: "Provider-real protocol validation only; do not run tools.", VisibilityRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := client.TurnStart(ctx, TurnStartRequest{ThreadID: parent.Thread.ID, Prompt: "Reply only with OK.", Model: model, ReasoningEffort: "medium", VisibilityRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WaitTurn(ctx, parent.Thread.ID, turn.Turn.ID); err != nil {
		t.Fatal(err)
	}
	child, err := client.ThreadFork(ctx, ThreadForkRequest{ParentThreadID: parent.Thread.ID, Model: model, Sandbox: "read-only", ApprovalPolicy: "never", DeveloperInstructions: "Provider-real protocol validation only; do not run tools.", VisibilityRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	if child.Thread.ID == "" || child.Thread.ID == parent.Thread.ID {
		t.Fatalf("invalid child relationship: parent=%q child=%q", parent.Thread.ID, child.Thread.ID)
	}
}
