package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestRenderWorkflowStatusFormatsAreDeterministic(t *testing.T) {
	run := workflow.Run{ID: "wf-1", PlanRevision: 2, Points: []workflow.PlanPoint{{ID: "P06", Title: "Progreso", State: workflow.PointAccepted}}}
	telemetry := []workflow.PlanPointTelemetry{{WorkflowID: "wf-1", PointID: "P06", AgentRuns: 1, InputTokens: 10, OutputTokens: 2, AgentDuration: time.Second, Measured: 1}}
	for _, format := range []string{"compact", "markdown", "json"} {
		var first, second bytes.Buffer
		if err := renderWorkflowStatus(&first, run, telemetry, format); err != nil {
			t.Fatal(err)
		}
		if err := renderWorkflowStatus(&second, run, telemetry, format); err != nil {
			t.Fatal(err)
		}
		if first.String() != second.String() || !strings.Contains(first.String(), "P06") && format == "markdown" {
			t.Fatalf("%s output=%q", format, first.String())
		}
	}
}

func TestRenderWorkflowStatusCompactPreservesUnknownMeasurement(t *testing.T) {
	var out bytes.Buffer
	run := workflow.Run{ID: "wf-unknown"}
	telemetry := []workflow.PlanPointTelemetry{{WorkflowID: run.ID, PointID: "P06", Unknown: 1}}
	if err := renderWorkflowStatus(&out, run, telemetry, "compact"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "tokens=unknown duration=unknown") {
		t.Fatalf("compact status lost unknown measurement: %q", got)
	}
}

func TestRenderWorkflowStatusSeparatesKnownDurationFromUnknownTokens(t *testing.T) {
	var out bytes.Buffer
	run := workflow.Run{ID: "wf-duration"}
	telemetry := []workflow.PlanPointTelemetry{{WorkflowID: run.ID, PointID: "P06", AgentDuration: time.Second, Unknown: 1}}
	if err := renderWorkflowStatus(&out, run, telemetry, "compact"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "tokens=unknown duration=1s") {
		t.Fatalf("duration inherited token measurement state: %q", got)
	}
}

func TestWorkflowCompareRequiresExistingMeasuredWorkflows(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "workflow.db")
	store, err := workflow.Open(t.Context(), tracePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := workflow.Plan{Graph: workflow.Graph{Task: "compare", Steps: []workflow.Step{{ID: "one", PointID: "P17", PointTitle: "Compare", TypeName: "worker", Task: contract.Task{Objective: "compare"}}}}, Pools: map[string]config.Pool{"one": config.PoolAgent}}
	for _, id := range []string{"wf-a", "wf-b"} {
		if err := store.CreateWithFingerprint(t.Context(), id, plan, "repo", "tree", time.Now(), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := workflowCompare([]string{"wf-a", "wf-b", "--traces", tracePath}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "workflow_a=wf-a tokens=unknown duration=unknown") || !strings.Contains(got, "delta_tokens=unknown delta_duration=unknown") {
		t.Fatalf("unmeasured workflows were rendered as measured: %q", got)
	}
	out.Reset()
	if err := workflowCompare([]string{"wf-a", "missing", "--traces", tracePath}, &out); err == nil {
		t.Fatal("nonexistent workflow was accepted as a zero-valued comparison")
	}
}

func TestWorkflowPanelRequiresEnabledDashboardAndExistingWorkflow(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "atenea.toml")
	body := settings + "\n[dashboard]\nenabled = true\nlisten = \"127.0.0.1:8788\"\naccess = \"tailscale\"\n"
	if err := os.WriteFile(settingsPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store, err := workflow.Open(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	plan := workflow.Plan{Graph: workflow.Graph{Task: "panel", Steps: []workflow.Step{{ID: "one", PointID: "P06", PointTitle: "Panel", TypeName: "worker", Task: contract.Task{Objective: "show"}}}}, Pools: map[string]config.Pool{"one": config.PoolAgent}}
	if err := store.CreateWithFingerprint(t.Context(), "wf-panel", plan, "repo", "tree", time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := workflowPanel(settingsPath, []string{"wf-panel"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "/workflows/wf-panel") {
		t.Fatalf("panel url = %q", out.String())
	}
	if err := workflowPanel(settingsPath, []string{"missing"}, &out); err == nil {
		t.Fatal("missing workflow received a panel URL")
	}
	disabled := filepath.Join(t.TempDir(), "atenea.toml")
	if err := os.WriteFile(disabled, []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workflowPanel(disabled, []string{"wf-panel"}, &out); err == nil {
		t.Fatal("disabled dashboard received a panel URL")
	}
}

func TestFlagsBeforeArgsAcceptsDocumentedTrailingFormat(t *testing.T) {
	got := flagsBeforeArgs([]string{"wf-1", "--format", "json", "--activity-after", "4"}, map[string]bool{"--format": true, "--activity-after": true})
	want := []string{"--format", "json", "--activity-after", "4", "wf-1"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("flags=%v", got)
	}
}
