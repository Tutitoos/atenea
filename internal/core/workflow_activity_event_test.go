package core

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/observability"
	"github.com/Tutitoos/atenea/internal/workflow"
)

func TestActivityNotificationPublishesWorkflowRefreshEvent(t *testing.T) {
	hub := observability.New(8)
	w := activityNotificationWriter{events: hub}
	if err := w.WriteActivity([]workflow.ActivityNotice{{WorkflowID: "workflow-1", PointID: "P06", Kind: "atenea", InvocationID: "call-1", Markdown: "> **ATENEA · test** — verifico."}}); err != nil {
		t.Fatal(err)
	}
	sub := hub.Subscribe(0)
	defer sub.Cancel()
	if len(sub.Replay) != 1 {
		t.Fatalf("replay events = %d, want 1", len(sub.Replay))
	}
	got := sub.Replay[0]
	if got.Kind != "workflow.activity" || got.RunID != "workflow-1" || got.StepID != "P06" || got.Reason != "" {
		t.Fatalf("event = %#v", got)
	}
}
