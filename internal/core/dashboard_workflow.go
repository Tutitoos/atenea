package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/workflow"
)

type dashboardWorkflowView struct {
	ID               string                        `json:"id"`
	Task             string                        `json:"task"`
	Repository       string                        `json:"repository,omitempty"`
	State            string                        `json:"state"`
	StartedAt        time.Time                     `json:"started_at,omitempty"`
	FinishedAt       time.Time                     `json:"finished_at,omitempty"`
	ActiveDurationMS int64                         `json:"active_duration_ms"`
	Progress         workflow.PlanProgress         `json:"progress"`
	Telemetry        []workflow.PlanPointTelemetry `json:"telemetry"`
	Activity         []workflow.ActivityNotice     `json:"activity"`
	ActivityCursor   int64                         `json:"activity_cursor"`
	ActivityHasMore  bool                          `json:"activity_has_more"`
	Recovery         *workflow.RecoveryState       `json:"recovery,omitempty"`
}

func (c *Core) dashboardWorkflow(id string) (any, error) {
	store, err := workflow.Open(context.Background(), "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()
	return dashboardWorkflowFromStore(context.Background(), store, id)
}

func dashboardWorkflowFromStore(ctx context.Context, store *workflow.Store, id string) (dashboardWorkflowView, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, `/\\`) {
		return dashboardWorkflowView{}, fmt.Errorf("invalid workflow id")
	}
	run, err := store.Load(ctx, id)
	if err != nil {
		return dashboardWorkflowView{}, err
	}
	telemetry, err := store.PointTelemetry(ctx, id)
	if err != nil {
		return dashboardWorkflowView{}, err
	}
	activity, cursor, more, err := store.ActivitiesPage(ctx, id, 0, 200)
	if err != nil {
		return dashboardWorkflowView{}, err
	}
	state := workflow.StateRunning
	switch {
	case run.WatchdogState != "":
		state = run.WatchdogState
	case run.Stop != workflow.StopNone:
		state = string(run.Stop)
	case run.Closed:
		state = workflow.StateCompleted
	}
	return dashboardWorkflowView{
		ID: id, Task: safeDashboardText(run.Task, 160), Repository: safeDashboardText(run.Repository, 100),
		State: state, StartedAt: run.Started, FinishedAt: run.Ended,
		ActiveDurationMS: run.ActiveDuration.Milliseconds(), Progress: workflow.PlanProgress{Revision: run.PlanRevision, Points: run.Points},
		Telemetry: telemetry, Activity: activity, ActivityCursor: cursor, ActivityHasMore: more, Recovery: run.Recovery,
	}, nil
}
