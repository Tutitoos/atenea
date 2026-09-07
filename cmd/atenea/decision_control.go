package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/coordination"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func isDecideControl(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "status", "show", "cancel", "resume", "answer", "approve", "reject":
		return true
	default:
		return false
	}
}

// cmdDecideControl keeps durable lifecycle operations close to the `decide`
// entry point while delegating execution to workflow's existing authority.
func cmdDecideControl(settingsPath string, args []string, out io.Writer) error {
	if len(args) < 2 {
		return contract.Fail(contract.FailureInvalidInput, "decide %s takes one persistent workflow or coordinator id", args[0])
	}
	verb := strings.ToLower(args[0])
	tracePath := decideTracePath(args[1:])
	coordStore, err := coordination.Open(coordination.PathFor(tracePath))
	if err != nil {
		return err
	}
	record, loadErr := coordStore.Load(context.Background(), args[1])
	if loadErr != nil && contract.KindOf(loadErr) != contract.FailureNotFound {
		return loadErr
	}
	if loadErr == nil {
		return controlCoordinator(settingsPath, verb, args[1], tracePath, coordStore, record, out)
	}
	// A regular repository workflow keeps its original durable id and command
	// surface. This branch is the compatibility path for IDs created by
	// `workflow create` or the MCP tools.
	return cmdWorkflow(settingsPath, append([]string{verb}, args[1:]...), out)
}

func decideTracePath(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--traces" && i+1 < len(args) && strings.TrimSpace(args[i+1]) != "" {
			return args[i+1]
		}
	}
	return workflow.DefaultPath()
}

func controlCoordinator(settingsPath, verb, id, tracePath string, coordStore *coordination.Store, record coordination.Record, out io.Writer) error {
	ctx := context.Background()
	switch verb {
	case "status", "show":
		var err error
		record, err = syncCoordinatorManifest(ctx, tracePath, coordStore, record)
		if err != nil {
			return err
		}
		printCoordinator(out, record)
		return nil
	case "cancel":
		store, err := workflow.Open(ctx, tracePath)
		if err != nil {
			return err
		}
		defer func() { _ = store.Close() }()
		var first error
		if record.CoordinatorWorkflowID != "" {
			if _, err := store.Cancel(ctx, record.CoordinatorWorkflowID, time.Now()); err != nil && contract.KindOf(err) != contract.FailureInvalidInput {
				first = err
			}
		}
		for _, child := range record.Children {
			if child.Status == coordination.StatusCompleted {
				continue
			}
			if _, err := store.Cancel(ctx, child.WorkflowID, time.Now()); err != nil && contract.KindOf(err) != contract.FailureInvalidInput {
				if first == nil {
					first = err
				}
				continue
			}
			_, _ = coordStore.FinishChild(ctx, id, child.Repository, coordination.StatusStopped, "canceled", time.Now().UTC())
		}
		_, _ = coordStore.SetStatus(ctx, id, coordination.StatusStopped, "canceled")
		if first != nil {
			return first
		}
		fmt.Fprintf(out, "coordinator %s stopped\n", id)
		return nil
	case "resume":
		var err error
		record, err = syncCoordinatorManifest(ctx, tracePath, coordStore, record)
		if err != nil {
			return err
		}
		if record.State != coordination.StateRunning {
			return contract.Fail(contract.FailurePermissionDenied, "coordinator %s requires attention before resume: %s", id, record.State)
		}
		if record, err = coordStore.RecordFollowUp(ctx, id, time.Now().UTC()); err != nil {
			return err
		}
		atenea, err := load(settingsPath)
		if err != nil {
			return err
		}
		defer func() { _ = atenea.Shutdown() }()
		cfg := atenea.Settings()
		if record.CoordinatorWorkflowID == "" {
			return contract.Fail(contract.FailureUnavailable, "coordinator %s has no durable root workflow", id)
		}
		rootEngine, closeRoot, err := workflow.Serve(ctx, cfg, tracePath, record.Repositories[0], "cli", out)
		if err != nil {
			return err
		}
		if _, loadErr := rootEngine.Load(ctx, record.CoordinatorWorkflowID); contract.KindOf(loadErr) == contract.FailureNotFound {
			var graph workflow.Graph
			if len(record.CoordinatorGraph) == 0 || json.Unmarshal(record.CoordinatorGraph, &graph) != nil {
				closeRoot()
				return contract.Fail(contract.FailureUnavailable, "coordinator %s has an incomplete root reservation", id)
			}
			if _, _, err = rootEngine.CreateWithID(ctx, record.CoordinatorWorkflowID, graph); err != nil {
				closeRoot()
				return err
			}
		} else if loadErr != nil {
			closeRoot()
			return loadErr
		}
		if _, err = rootEngine.RecoverAuthorized(ctx, record.CoordinatorWorkflowID, nil); err != nil {
			closeRoot()
			return err
		}
		parent, err := rootEngine.CoordinatorAssignment(ctx, record.CoordinatorWorkflowID)
		closeRoot()
		if err != nil {
			return err
		}
		if record.CoordinatorThreadID != "" && (parent.Route == nil || parent.Route.ThreadID != record.CoordinatorThreadID) {
			return contract.Fail(contract.FailureUnavailable, "coordinator %s thread identity changed", id)
		}
		allDone := true
		for _, child := range record.Children {
			if child.Status == coordination.StatusCompleted {
				engine, closeEngine, verifyErr := workflow.Serve(ctx, cfg, tracePath, child.Repository, "cli", out)
				if verifyErr != nil {
					return verifyErr
				}
				_, verifyErr = engine.VerifyCompletedSources(ctx, child.WorkflowID)
				closeEngine()
				if verifyErr != nil {
					_, _ = coordStore.FinishChild(ctx, id, child.Repository, coordination.StatusStopped, verifyErr.Error(), time.Now().UTC())
					_, _ = coordStore.SetStatus(ctx, id, coordination.StatusStopped, "completed evidence requires re-evaluation")
					return verifyErr
				}
				continue
			}
			allDone = false
			store, err := workflow.Open(ctx, tracePath)
			if err != nil {
				return err
			}
			run, loadErr := store.Load(ctx, child.WorkflowID)
			_ = store.Close()
			if contract.KindOf(loadErr) == contract.FailureNotFound {
				var graph workflow.Graph
				if len(child.Graph) == 0 || json.Unmarshal(child.Graph, &graph) != nil {
					return contract.Fail(contract.FailureUnavailable, "workflow %s has an incomplete reservation", child.WorkflowID)
				}
				engine, closeEngine, openErr := workflow.ServeWithParent(ctx, cfg, tracePath, child.Repository, "cli", out, &parent)
				if openErr != nil {
					return openErr
				}
				run, _, loadErr = engine.CreateWithID(ctx, child.WorkflowID, graph)
				closeEngine()
			}
			if loadErr != nil {
				return loadErr
			}
			if run.Closed {
				if err := recordCoordinatorReviewCycle(ctx, coordStore, id, run); err != nil {
					return err
				}
				if _, err := coordStore.FinishChild(ctx, id, child.Repository, coordination.StatusCompleted, "", time.Now().UTC()); err != nil {
					return err
				}
				continue
			}
			store, err = workflow.Open(ctx, tracePath)
			if err != nil {
				return err
			}
			gate, gateErr := store.Gate(ctx, child.WorkflowID, 0)
			_ = store.Close()
			if gateErr == nil && gate.Waiting() {
				engine, closeEngine, openErr := workflow.ServeWithParent(ctx, cfg, tracePath, child.Repository, "cli", out, &parent)
				if openErr != nil {
					return openErr
				}
				configureCoordinatorLimits(engine, coordStore, id, child.WorkflowID)
				launched, launchErr := engine.LaunchAuthorized(ctx, child.WorkflowID, nil)
				closeEngine()
				if launchErr != nil {
					return launchErr
				}
				if launched.ID != "" {
					printRun(out, launched)
				}
				if launched.ID == "" {
					return contract.Fail(contract.FailureUnavailable, "workflow %s launch returned no durable id", child.WorkflowID)
				}
				store, err = workflow.Open(ctx, tracePath)
				if err != nil {
					return err
				}
				launched, err := store.Load(ctx, child.WorkflowID)
				_ = store.Close()
				if err != nil {
					return err
				}
				status := coordination.StatusRunning
				if launched.Closed {
					status = coordination.StatusCompleted
				}
				if _, err := coordStore.FinishChild(ctx, id, child.Repository, status, "", time.Now().UTC()); err != nil {
					return err
				}
				continue
			}
			engine, closeEngine, openErr := workflow.ServeWithParent(ctx, cfg, tracePath, child.Repository, "cli", out, &parent)
			if openErr != nil {
				return openErr
			}
			configureCoordinatorLimits(engine, coordStore, id, child.WorkflowID)
			resumed, resumeErr := engine.ResumeAuthorized(ctx, child.WorkflowID, nil, nil)
			closeEngine()
			if resumed.ID != "" {
				printRun(out, resumed)
			}
			if resumeErr != nil {
				return resumeErr
			}
			store, err = workflow.Open(ctx, tracePath)
			if err != nil {
				return err
			}
			run, err = store.Load(ctx, child.WorkflowID)
			_ = store.Close()
			if err != nil {
				return err
			}
			status := coordination.StatusRunning
			if run.Closed {
				if err := recordCoordinatorReviewCycle(ctx, coordStore, id, run); err != nil {
					return err
				}
				status = coordination.StatusCompleted
			}
			if _, err := coordStore.FinishChild(ctx, id, child.Repository, status, "", time.Now().UTC()); err != nil {
				return err
			}
		}
		if allDone {
			return contract.Fail(contract.FailureInvalidInput, "coordinator %s has no resumable children", id)
		}
		latest, err := coordStore.Load(ctx, id)
		if err != nil {
			return err
		}
		for _, child := range latest.Children {
			if child.Status != coordination.StatusCompleted {
				return nil
			}
		}
		_, err = coordStore.SetStatus(ctx, id, coordination.StatusCompleted, "")
		return err
	case "answer", "approve", "reject":
		return contract.Fail(contract.FailureInvalidInput, "coordinator %s has no pending question; answer its child workflow id", id)
	default:
		return contract.Fail(contract.FailureInvalidInput, "unknown decide control %q", verb)
	}
}

func syncCoordinatorManifest(ctx context.Context, tracePath string, coordStore *coordination.Store, record coordination.Record) (coordination.Record, error) {
	store, err := workflow.Open(ctx, tracePath)
	if err != nil {
		return record, err
	}
	defer func() { _ = store.Close() }()
	state := coordination.StateRunning
	message := ""
	for _, child := range record.Children {
		run, loadErr := store.Load(ctx, child.WorkflowID)
		if contract.KindOf(loadErr) == contract.FailureNotFound {
			continue
		}
		if loadErr != nil {
			return record, loadErr
		}
		if run.WatchdogState == workflow.StateUncertain {
			state, message = coordination.StateUncertain, "child workflow has an uncertain in-flight effect"
			break
		}
		if run.WatchdogState == workflow.StateAttentionRequired {
			state, message = coordination.StateAttentionRequired, "child workflow requires attention after watchdog timeout"
		}
	}
	if state == record.State {
		return record, nil
	}
	return coordStore.SetState(ctx, record.ID, state, message, time.Now().UTC())
}

func printCoordinator(out io.Writer, record coordination.Record) {
	fmt.Fprintf(out, "%s  %s\n", record.ID, record.Status)
	fmt.Fprintf(out, "state  %s  follow_ups=%d/%d astra=%d/%d last_progress_at=%s\n",
		record.State, record.FollowUps, coordination.MaxFollowUps, record.AstraExecutions, coordination.MaxAstraExecutions,
		record.LastProgressAt.Format(time.RFC3339))
	fmt.Fprintf(out, "objective  %s\ncriterion  %s\n", record.Objective, record.Criterion)
	fmt.Fprintf(out, "repositories  %s\ncoordinator  %s\nspecialists  %s\n", strings.Join(record.Repositories, ", "), record.Coordinator, strings.Join(record.Specialists, ", "))
	fmt.Fprintf(out, "coordinator_workflow  %s\ncoordinator_thread  %s\n", record.CoordinatorWorkflowID, record.CoordinatorThreadID)
	fmt.Fprintf(out, "limits  duration=%s tokens=%d budget=$%.2f\n", record.Limits.MaxDuration, record.Limits.MaxTokens, record.BudgetUSD)
	for _, child := range record.Children {
		fmt.Fprintf(out, "  %s  %s  %s\n", child.Repository, child.WorkflowID, child.Status)
	}
}
