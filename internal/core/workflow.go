package core

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/observability"
	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The workflow surface: two tools, and deliberately two.
//
// Creating a plan and running it are separate calls because they are separate
// acts, and only one of them commits money and effects. A single tool that
// did both would let whatever is driving this connection commission work by
// describing it -- the plan and the permission would arrive in the same
// message, which is exactly the arrangement a gate exists to prevent.
//
// `workflow.launch` is not a model's call to make on its own behalf: it is
// the person's, arriving over whichever surface they have. It is exposed here
// because a chat is a place a person answers from, and refusing it here would
// only mean the answer had to come from a terminal that may not be open.
const (
	toolWorkflowCreate = "workflow.create"
	toolWorkflowLaunch = "workflow.launch"
	toolWorkflowStatus = "workflow.status"
	toolWorkflowCancel = "workflow.cancel"
	toolWorkflowResume = "workflow.resume"
	toolWorkflowAnswer = "workflow.answer"
)

const mcpWorkflowOperationsMessage = "sensitive operations require local human approval via the CLI"

func rejectMCPWorkflowOperations(tool string, args map[string]any) *rpcError {
	if _, ok := args["operations"]; !ok {
		return nil
	}
	return &rpcError{
		Code:    codeInvalidParams,
		Message: tool + ": operations are not accepted over MCP; " + mcpWorkflowOperationsMessage,
	}
}

// workflowTools are the schema entries, inserted beside catalog.repositories
// and never mixed into the capability list: nothing behind them is ranked on
// cost or health, because there is only one thing each can mean.
func (v *conversation) workflowTools() []map[string]any {
	return []map[string]any{{
		"name": toolWorkflowCreate,
		"description": "Write down a graph of agent steps and return the plan, WITHOUT running it. " +
			"The graph comes from a TOML file: nothing here invents one. " +
			"The answer names every step, the agent each runs, what share of the grant each claims, " +
			"and a digest. Nothing spawns until a person calls " + toolWorkflowLaunch + ".",
		"inputSchema": v.aimedAt(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file": map[string]any{
					"type":        "string",
					"description": "Path to the graph file.",
				},
			},
			"required": []string{"file"},
		}),
	}, {
		"name": toolWorkflowLaunch,
		"description": "Launch a plan that " + toolWorkflowCreate + " wrote down, and run it to the end. " +
			"This commits the grant and lets the agents spawn, so it is a person's call and not a model's. " +
			"If the graph grows mid-run it stops and waits for an approval, indefinitely; " +
			"nothing new is dispatched while it waits.",
		"inputSchema": v.aimedAt(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The workflow id returned by " + toolWorkflowCreate + ".",
				},
			},
			"required": []string{"id"},
		}),
	}, {
		"name": toolWorkflowStatus,
		"description": "Read the persisted workflow snapshot, including steps, gates, ownership and durable stop state. " +
			"This is read-only and survives reconnects.",
		"inputSchema": map[string]any{
			"type": "object", "properties": map[string]any{
				"id":             map[string]any{"type": "string", "description": "Persistent workflow id."},
				"activity_after": map[string]any{"type": "integer", "minimum": 0, "description": "Return only activity after this durable cursor."},
			}, "required": []string{"id"},
		},
	}, {
		"name":        toolWorkflowCancel,
		"description": "Durably cancel a workflow by id. The running owner observes the marker and stops; repeating this call is safe.",
		"inputSchema": v.aimedAt(map[string]any{
			"type": "object", "properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Persistent workflow id."},
			}, "required": []string{"id"},
		}),
	}, {
		"name":        toolWorkflowResume,
		"description": "Resume a persisted workflow after cancellation or an orphaned owner, checking its repository and prior results first.",
		"inputSchema": v.aimedAt(map[string]any{
			"type": "object", "properties": map[string]any{
				"id":   map[string]any{"type": "string", "description": "Persistent workflow id."},
				"redo": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Interrupted steps with explicit permission to repeat."},
			}, "required": []string{"id"},
		}),
	}, {
		"name":        toolWorkflowAnswer,
		"description": "Answer the currently open workflow gate. This records only approved or rejected gate decisions; it does not invent a free-form question.",
		"inputSchema": v.aimedAt(map[string]any{
			"type": "object", "properties": map[string]any{
				"id":       map[string]any{"type": "string", "description": "Persistent workflow id."},
				"ordinal":  map[string]any{"type": "integer", "minimum": 0, "description": "Exact gate ordinal returned by the workflow snapshot."},
				"digest":   map[string]any{"type": "string", "description": "Full proposal digest returned when the gate was opened."},
				"decision": map[string]any{"type": "string", "enum": []string{"approved", "rejected"}},
				"reason":   map[string]any{"type": "string", "description": "Required when rejecting."},
			}, "required": []string{"id", "ordinal", "digest", "decision"},
		}),
	}}
}

// workflowCreate answers workflow.create: it writes the graph down, opens the
// launch gate, and returns the plan for somebody to read.
func (v *conversation) workflowCreate(ctx context.Context, args map[string]any) (any, *rpcError) {
	file, _ := args["file"].(string)
	if strings.TrimSpace(file) == "" {
		return nil, &rpcError{Code: codeInvalidParams,
			Message: toolWorkflowCreate + ": file is required: the graph comes from a file"}
	}
	graph, err := workflow.ReadFile(file)
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	// Before the engine, because Create writes the run. A chat that may not
	// authorize these effects should not leave a plan on disk it can never
	// launch, and the refusal is more useful here than after the id exists.
	if refusal := v.authorize(ctx, toolWorkflowCreate, graph.Effects()); refusal != nil {
		return refusal, nil
	}
	repository, aimErr := v.workflowRepository(toolWorkflowCreate, args)
	if aimErr != nil {
		return nil, aimErr
	}
	engine, closers, err := v.workflowEngine(ctx, repository)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	defer closers()

	run, gate, err := engine.Create(ctx, graph)
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if v.core.events != nil {
		v.core.events.Publish(observability.Event{Kind: "gate.waiting", RunID: run.ID, State: "waiting", Count: len(gate.Proposal.Steps)})
	}
	steps := make([]map[string]any, 0, len(gate.Proposal.Steps))
	for _, step := range gate.Proposal.Steps {
		entry := map[string]any{
			"id":        step.ID,
			"agent":     step.TypeName,
			"objective": step.Task.Objective,
			"share_usd": step.Permission.BudgetUSD,
		}
		// Omitted rather than null. A step that waits on nothing is the
		// ordinary case, and "needs": null reads as a field somebody failed
		// to fill in.
		if len(step.Needs) > 0 {
			entry["needs"] = step.Needs
		}
		if step.Subject != "" {
			entry["subject"] = step.Subject
		}
		steps = append(steps, entry)
	}
	return toolResult(map[string]any{
		"id":     run.ID,
		"task":   run.Task,
		"steps":  steps,
		"digest": gate.Digest,
		// Allocated, not spent. Nothing on this machine can report a
		// charge, so there is no second number here and there will not be
		// one until an agent can measure what it used.
		"allocated_usd": gate.Proposal.AllocatedUSD(),
		"grant_usd":     run.GrantUSD,
		"waiting_on": fmt.Sprintf("%s, or `atenea workflow launch %s`",
			toolWorkflowLaunch, run.ID),
	})
}

// workflowLaunch answers workflow.launch: it records the approval and runs the
// graph to the end.
func (v *conversation) workflowLaunch(ctx context.Context, args map[string]any) (any, *rpcError) {
	if refusal := rejectMCPWorkflowOperations(toolWorkflowLaunch, args); refusal != nil {
		return nil, refusal
	}
	id, _ := args["id"].(string)
	if strings.TrimSpace(id) == "" {
		return nil, &rpcError{Code: codeInvalidParams,
			Message: toolWorkflowLaunch + ": id is required"}
	}
	repository, aimErr := v.workflowRepository(toolWorkflowLaunch, args)
	if aimErr != nil {
		return nil, aimErr
	}
	engine, closers, err := v.workflowEngine(ctx, repository)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	defer closers()

	id = strings.TrimSpace(id)
	// Re-read from the run rather than trusting the create that wrote it.
	// A launch may arrive on a different connection, with a different grant,
	// long after the plan was drawn -- and the effects that matter are the
	// ones about to happen, not the ones somebody was entitled to describe.
	effects, err := engine.Effects(ctx, id)
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if refusal := v.authorize(ctx, toolWorkflowLaunch, effects); refusal != nil {
		return refusal, nil
	}

	run, runErr := engine.LaunchAuthorized(ctx, id, nil)
	setStatsError(ctx, runErr)
	if v.core.events != nil && run.ID != "" {
		state := "approved"
		if runErr != nil {
			state = "failed"
		}
		v.core.events.Publish(observability.Event{Kind: "gate.completed", RunID: run.ID, State: state, Reason: errorText(runErr)})
	}
	if run.ID == "" {
		// A run with no id is a launch that never started, and the reason is
		// runErr. The nil check is not defensive noise: the two returns are
		// independent, so an engine that ever answers "nothing to run" without
		// an error would crash the whole service on the .Error() rather than
		// refuse one call -- a panic in a dispatch that is holding a chat's
		// connection, from the one tool that commits money.
		message := toolWorkflowLaunch + ": " + id + " did not start and gave no reason"
		if runErr != nil {
			message = runErr.Error()
		}
		return nil, &rpcError{Code: codeInvalidParams, Message: message}
	}
	counts := run.Counts()
	out := map[string]any{
		"id":      run.ID,
		"state":   string(run.Stop),
		"summary": run.Summary(),
		"steps":   counts,
	}
	if run.Closed {
		out["state"] = "finished"
	}
	if runErr != nil {
		out["stopped"] = runErr.Error()
	}
	return toolResult(out)
}

func (v *conversation) workflowStatus(ctx context.Context, args map[string]any) (any, *rpcError) {
	id, _ := args["id"].(string)
	if strings.TrimSpace(id) == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolWorkflowStatus + ": id is required"}
	}
	if v.session == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolWorkflowStatus + ": no session is initialized"}
	}
	if refusal := v.authorize(ctx, toolWorkflowStatus, []contract.Effect{contract.EffectRead}); refusal != nil {
		return refusal, nil
	}
	store, err := workflow.Open(ctx, "")
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	defer func() { _ = store.Close() }()
	run, err := store.Load(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if refusal := v.workflowRunInScope(run); refusal != nil {
		return nil, refusal
	}
	gates, err := store.Gates(ctx, run.ID)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	after, afterErr := workflowActivityAfter(args["activity_after"])
	if afterErr != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolWorkflowStatus + ": " + afterErr.Error()}
	}
	activity, cursor, hasMore, err := store.ActivitiesPage(ctx, run.ID, after, 200)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	out := workflowSnapshot(run, gates)
	out["activity"] = activity
	out["activity_cursor"] = cursor
	out["activity_has_more"] = hasMore
	return toolResult(out)
}

func workflowActivityAfter(raw any) (int64, error) {
	if raw == nil {
		return 0, nil
	}
	switch value := raw.(type) {
	case int:
		if value >= 0 {
			return int64(value), nil
		}
	case int64:
		if value >= 0 {
			return value, nil
		}
	case float64:
		if value >= 0 && value == float64(int64(value)) {
			return int64(value), nil
		}
	case float32:
		if value >= 0 && value == float32(int64(value)) {
			return int64(value), nil
		}
	}
	return 0, fmt.Errorf("activity_after must be a non-negative integer")
}

func (v *conversation) workflowRunInScope(run workflow.Run) *rpcError {
	if strings.TrimSpace(run.Repository) == "" {
		return &rpcError{Code: codeInvalidParams, Message: toolWorkflowStatus + ": workflow has no repository scope"}
	}
	found := false
	for _, repository := range v.core.catalog.Repositories() {
		if repository.ID == run.Repository {
			found = true
			break
		}
	}
	if !found {
		return &rpcError{Code: codeInvalidParams, Message: toolWorkflowStatus + ": workflow repository is outside this core scope"}
	}
	_, _, project, _, _ := v.session.DashboardMetadata()
	if project != "" && project != run.Repository {
		return &rpcError{Code: codeInvalidParams, Message: toolWorkflowStatus + ": workflow is outside this session scope"}
	}
	return nil
}

func (v *conversation) workflowCancel(ctx context.Context, args map[string]any) (any, *rpcError) {
	id, _ := args["id"].(string)
	if strings.TrimSpace(id) == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolWorkflowCancel + ": id is required"}
	}
	repository, aimErr := v.workflowRepository(toolWorkflowCancel, args)
	if aimErr != nil {
		return nil, aimErr
	}
	// Cancel changes durable workflow state. A read-only MCP session may
	// inspect the run and its gates, but it must hold the process effect before
	// it can abort one; repository and aim scope remain checked below.
	if refusal := v.authorize(ctx, toolWorkflowCancel, []contract.Effect{contract.EffectProcess}); refusal != nil {
		return refusal, nil
	}
	store, err := workflow.Open(ctx, "")
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	defer func() { _ = store.Close() }()
	run, err := store.Load(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if run.Repository != "" && run.Repository != repository {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf(
			"%s: workflow belongs to repository %q", toolWorkflowCancel, run.Repository)}
	}
	run, err = store.Cancel(ctx, strings.TrimSpace(id), time.Now())
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if v.core.events != nil {
		v.core.events.Publish(observability.Event{Kind: "workflow.canceled", RunID: run.ID, State: string(workflow.StopAborted)})
	}
	gates, _ := store.Gates(ctx, run.ID)
	return toolResult(workflowSnapshot(run, gates))
}

func (v *conversation) workflowResume(ctx context.Context, args map[string]any) (any, *rpcError) {
	if refusal := rejectMCPWorkflowOperations(toolWorkflowResume, args); refusal != nil {
		return nil, refusal
	}
	id, _ := args["id"].(string)
	if strings.TrimSpace(id) == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolWorkflowResume + ": id is required"}
	}
	redo, err := workflowRedoNames(args["redo"])
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolWorkflowResume + ": " + err.Error()}
	}
	repository, aimErr := v.workflowRepository(toolWorkflowResume, args)
	if aimErr != nil {
		return nil, aimErr
	}
	engine, closers, err := v.workflowEngine(ctx, repository)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	defer closers()
	effects, err := engine.Effects(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if refusal := v.authorize(ctx, toolWorkflowResume, effects); refusal != nil {
		return refusal, nil
	}
	run, runErr := engine.ResumeAuthorized(ctx, strings.TrimSpace(id), redo, nil)
	setStatsError(ctx, runErr)
	if runErr != nil && run.ID == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: runErr.Error()}
	}
	out := workflowSnapshot(run, nil)
	if runErr != nil {
		out["stopped"] = runErr.Error()
	}
	return toolResult(out)
}

func workflowRedoNames(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		if typed, ok := raw.([]string); ok {
			return slices.Clone(typed), nil
		}
		return nil, fmt.Errorf("redo must be an array of step ids")
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		name, ok := value.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("redo must contain non-empty step ids")
		}
		out = append(out, strings.TrimSpace(name))
	}
	return out, nil
}

func (v *conversation) workflowAnswer(ctx context.Context, args map[string]any) (any, *rpcError) {
	if refusal := rejectMCPWorkflowOperations(toolWorkflowAnswer, args); refusal != nil {
		return nil, refusal
	}
	id, _ := args["id"].(string)
	decision, _ := args["decision"].(string)
	reason, _ := args["reason"].(string)
	ordinal, ok := workflowGateOrdinal(args["ordinal"])
	if strings.TrimSpace(id) == "" || strings.TrimSpace(decision) == "" || !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolWorkflowAnswer + ": id, ordinal, digest and decision are required"}
	}
	digest, _ := args["digest"].(string)
	if strings.TrimSpace(digest) == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolWorkflowAnswer + ": digest is required"}
	}
	var d workflow.Decision
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "approved", "approve":
		d = workflow.DecisionApproved
	case "rejected", "reject":
		d = workflow.DecisionRejected
	default:
		return nil, &rpcError{Code: codeInvalidParams, Message: toolWorkflowAnswer + ": decision must be approved or rejected"}
	}
	store, err := workflow.Open(ctx, "")
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	defer func() { _ = store.Close() }()
	id = strings.TrimSpace(id)
	repository, aimErr := v.workflowRepository(toolWorkflowAnswer, args)
	if aimErr != nil {
		return nil, aimErr
	}
	run, err := store.Load(ctx, id)
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if run.Repository != "" && run.Repository != repository {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf(
			"%s: workflow belongs to repository %q", toolWorkflowAnswer, run.Repository)}
	}
	gate, err := store.Gate(ctx, id, ordinal)
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if gate.Digest != digest {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolWorkflowAnswer + ": stale gate digest"}
	}
	if gate.Waiting() && d == workflow.DecisionApproved && len(gate.Proposal.Operations()) > 0 {
		return nil, &rpcError{Code: codeInvalidParams, Message: toolWorkflowAnswer + ": this gate requires sensitive operations; it remains waiting because " + mcpWorkflowOperationsMessage}
	}
	if gate.Kind == workflow.KindLaunch && d == workflow.DecisionApproved {
		return nil, &rpcError{Code: codeInvalidParams, Message: "the launch gate is answered by workflow.launch"}
	}
	if !gate.Waiting() {
		if gate.Decision != d || (d == workflow.DecisionRejected && gate.Reason != reason) {
			return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf(
				"%s: gate %d was already answered %s", toolWorkflowAnswer, gate.Ordinal, gate.Decision)}
		}
		return toolResult(map[string]any{
			"id": id, "ordinal": gate.Ordinal, "kind": gate.Kind.String(),
			"decision": gate.Decision.String(), "digest": gate.Digest,
			"state": workflowState(run), "replayed": true,
		})
	}
	// Authorization is checked for every decision before a possible automatic
	// resume. A rejection still resumes the workflow to persist its terminal
	// state, and must not bypass the session's effect boundary.
	engine, closers, err := v.workflowEngine(ctx, repository)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	defer closers()
	effects, err := engine.Effects(ctx, id)
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if d == workflow.DecisionApproved {
		effects = unionEffects(effects, proposalEffects(gate.Proposal))
	}
	if refusal := v.authorize(ctx, toolWorkflowAnswer, effects); refusal != nil {
		return refusal, nil
	}
	answered, err := store.Answer(ctx, id, gate.Ordinal, d, workflow.Hand("mcp"), reason, time.Now())
	if err != nil {
		current, readErr := store.Gate(ctx, id, gate.Ordinal)
		if readErr != nil || current.Digest != digest || current.Decision != d ||
			(d == workflow.DecisionRejected && current.Reason != reason) {
			return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
		}
		return toolResult(map[string]any{
			"id": id, "ordinal": current.Ordinal, "kind": current.Kind.String(),
			"decision": current.Decision.String(), "digest": current.Digest,
			"state": workflowState(run), "replayed": true,
		})
	}
	// If the original owner disappeared while the gate was open, take the run
	// over now. A live owner will observe the same persisted answer itself.
	run, _ = store.Load(ctx, id)
	var resumeErr error
	if run.WriterPID == 0 || !pidlock.Alive(run.WriterPID) {
		var resumed workflow.Run
		resumed, resumeErr = engine.ResumeAuthorized(ctx, id, nil, nil)
		if resumed.ID != "" {
			run = resumed
			if resumeErr != nil {
				setStatsError(ctx, resumeErr)
			}
		}
		if resumed.ID == "" && resumeErr != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: resumeErr.Error()}
		}
	}
	out := map[string]any{
		"id": id, "ordinal": answered.Ordinal, "kind": answered.Kind.String(),
		"decision": answered.Decision.String(), "digest": answered.Digest,
		"state": workflowState(run),
	}
	if resumeErr != nil {
		out["stopped"] = resumeErr.Error()
	}
	return toolResult(out)
}

func workflowGateOrdinal(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, value >= 0
	case int64:
		return int(value), value >= 0 && int64(int(value)) == value
	case float64:
		return int(value), value >= 0 && value == float64(int(value))
	default:
		return 0, false
	}
}

func proposalEffects(proposal workflow.Proposal) []contract.Effect {
	seen := make(map[contract.Effect]bool)
	for _, step := range proposal.Steps {
		for _, effect := range step.Permission.Effects {
			seen[effect] = true
		}
	}
	out := make([]contract.Effect, 0, len(seen))
	for effect := range seen {
		out = append(out, effect)
	}
	slices.Sort(out)
	return out
}

func unionEffects(groups ...[]contract.Effect) []contract.Effect {
	seen := make(map[contract.Effect]bool)
	for _, group := range groups {
		for _, effect := range group {
			seen[effect] = true
		}
	}
	out := make([]contract.Effect, 0, len(seen))
	for effect := range seen {
		out = append(out, effect)
	}
	slices.Sort(out)
	return out
}

func workflowState(run workflow.Run) string {
	if run.Closed {
		return "finished"
	}
	if run.Stop != workflow.StopNone {
		return string(run.Stop)
	}
	if run.WriterPID == 0 && runHasNotStarted(run) {
		return "unlaunched"
	}
	if run.WriterPID != 0 && pidlock.Alive(run.WriterPID) {
		return "running"
	}
	return "orphaned"
}

func runHasNotStarted(run workflow.Run) bool {
	for _, row := range run.Steps {
		if row.Attempt != 0 || row.Status != workflow.StatusPending {
			return false
		}
	}
	return true
}

func workflowSnapshot(run workflow.Run, gates []workflow.Gate) map[string]any {
	progress := workflow.PlanProgress{Revision: run.PlanRevision, Points: run.Points}
	steps := make([]map[string]any, 0, len(run.Steps))
	for _, row := range run.Steps {
		steps = append(steps, map[string]any{"id": row.Step.ID, "agent": row.Step.TypeName,
			"point_id": row.Step.PointID, "point_title": row.Step.PointTitle,
			"status": row.Status.String(), "attempt": row.Attempt, "trace_id": row.TraceID,
			"cost_state": workflowCostState(row.Spent), "spent_usd": row.Spent.USD,
			"priced_by": row.Spent.PricedBy, "notices": slices.Clone(row.Notices)})
	}
	ownership := "none"
	if run.WriterPID != 0 {
		ownership = "orphaned"
		if pidlock.Alive(run.WriterPID) {
			ownership = "owned"
		}
	}
	out := map[string]any{"id": run.ID, "task": run.Task, "repository": run.Repository,
		"state": workflowState(run), "writer_pid": run.WriterPID, "ownership": ownership,
		"summary": run.Summary(), "grant_usd": run.GrantUSD, "steps": steps,
		"activity": run.Activity, "activity_cursor": run.ActivityCursor,
		"activity_has_more": run.ActivityHasMore,
		"plan": map[string]any{"revision": run.PlanRevision, "points": run.Points,
			"accepted": progress.Accepted(), "total": progress.Total(),
			"percent": progress.Percent(), "bar": progress.Bar(),
			"markdown": progress.Markdown("")},
		"spend": workflowSpendSnapshot(run.Spend()),
		"policy": map[string]any{
			"name": run.Policy.Name, "version": run.Policy.Version, "digest": run.Policy.Digest,
			"max_budget_usd":      run.Policy.MaxBudgetUSD,
			"effects":             effectStrings(run.Policy.Effects),
			"operations":          operationStrings(run.Policy.Operations),
			"max_duration_ms":     run.Policy.MaxDuration.Milliseconds(),
			"max_retries":         run.Policy.MaxRetries,
			"max_parallel_agent":  run.Policy.MaxParallelAgent,
			"max_parallel_review": run.Policy.MaxParallelReview,
		}}
	if len(gates) > 0 {
		entries := make([]map[string]any, 0, len(gates))
		for _, gate := range gates {
			entry := map[string]any{
				"ordinal": gate.Ordinal, "kind": gate.Kind.String(),
				"digest": gate.Digest, "decision": gate.Decision.String(),
				"asked_at":    gate.Asked.UTC().Format(time.RFC3339Nano),
				"answered_at": "", "hand": gate.Hand, "reason": gate.Reason,
			}
			if !gate.Answered.IsZero() {
				entry["answered_at"] = gate.Answered.UTC().Format(time.RFC3339Nano)
			}
			entries = append(entries, entry)
		}
		out["gates"] = entries
	}
	return out
}

func workflowSpendSnapshot(spend workflow.Spend) map[string]any {
	observedSteps := spend.ObservedSteps + spend.SupersededObservedSteps
	estimatedSteps := spend.EstimatedSteps + spend.SupersededEstimatedSteps
	unknownSteps := spend.UnknownSteps + spend.SupersededUnknownSteps
	observedUSD := addUSD(spend.ObservedUSD, spend.SupersededObservedUSD)
	estimatedUSD := addUSD(spend.EstimatedUSD, spend.SupersededEstimatedUSD)
	return map[string]any{
		"tokens":                     spend.Tokens,
		"usd":                        spend.AccumulatedUSD(),
		"priced_by":                  spend.PricedBy,
		"observed_steps":             observedSteps,
		"estimated_steps":            estimatedSteps,
		"unknown_steps":              unknownSteps,
		"observed_usd":               observedUSD,
		"estimated_usd":              estimatedUSD,
		"unmeasured_steps":           spend.UnmeasuredSteps,
		"superseded_attempts":        spend.SupersededAttempts,
		"superseded_usd":             spend.SupersededUSD,
		"superseded_observed_steps":  spend.SupersededObservedSteps,
		"superseded_estimated_steps": spend.SupersededEstimatedSteps,
		"superseded_unknown_steps":   spend.SupersededUnknownSteps,
	}
}

func addUSD(left, right *float64) *float64 {
	if left == nil && right == nil {
		return nil
	}
	var total float64
	if left != nil {
		total += *left
	}
	if right != nil {
		total += *right
	}
	return &total
}

func workflowCostState(spent contract.Charge) string {
	if spent.USD == nil {
		return "unknown"
	}
	for _, source := range strings.Split(spent.PricedBy, " and ") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(source)), "estimate:") {
			return "estimated"
		}
	}
	return "observed"
}

func effectStrings(values []contract.Effect) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.String())
	}
	return out
}

func operationStrings(values []contract.Operation) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.String())
	}
	return out
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// authorize holds a workflow's effects against what this chat may grant.
//
// Every other tools/call path crosses this seam: the capability path through
// Session.Ask, the raw path explicitly in rawCall. These two did not, and the
// gap was not academic -- a chat opened with `grant = []`, a client saying it
// will only read, could describe a graph whose steps declare write and
// external and then launch it. The effects arrive inside a file rather than
// inside the request, which is exactly why the schema cannot catch this and
// the check has to be here.
//
// A refusal is an answer, not a protocol error, for the same reason rawCall's
// is: the caller asked for something real and can read why it did not work.
func (v *conversation) authorize(ctx context.Context, tool string, effects []contract.Effect) any {
	if len(effects) == 0 {
		return nil
	}
	if err := v.session.entitled(effects); err != nil {
		setStatsError(ctx, err)
		return toolFailure(tool + ": " + err.Error())
	}
	return nil
}

// workflowRepository resolves which repository a workflow call is about.
//
// The same rule the capability path uses, and for the same reason. An empty id
// reached WorkspaceFor, which falls back to the working directory of the
// PROCESS -- and here the process is the service, started from wherever its
// unit file left it. On a machine with several repositories that silently ran
// agents against $HOME, or against whatever git root contains it, under the
// grant of a run nobody had aimed.
func (v *conversation) workflowRepository(tool string, args map[string]any) (string, *rpcError) {
	repository, _ := args[repositoryArg].(string)
	if repository = strings.TrimSpace(repository); repository != "" {
		return repository, nil
	}
	repos := v.core.catalog.Repositories()
	if len(repos) != 1 {
		return "", &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf(
			"%s: %s is required: %d repositories are registered", tool, repositoryArg, len(repos))}
	}
	return repos[0].ID, nil
}

// workflowEngine builds the engine this connection's calls run through.
//
// Per call, and over the same trace database the CLI uses: a workflow held in
// one process's memory would be one nobody else could answer a gate on, and
// the gate outliving its asker is the whole design.
func (v *conversation) workflowEngine(ctx context.Context, repository string) (*workflow.Engine, func(), error) {
	surface := "mcp"
	if v.session != nil {
		surface = "mcp session " + v.session.ID()
	}
	var notices io.Writer
	if v.notify != nil || v.core.events != nil {
		notices = activityNotificationWriter{notify: v.notify, events: v.core.events}
	}
	return workflow.Serve(ctx, v.core.settings, "", repository, surface, notices)
}

type activityNotificationWriter struct {
	notify func(string, any) error
	events *observability.Hub
}

func (w activityNotificationWriter) WriteActivity(activity []workflow.ActivityNotice) error {
	lines := make([]string, 0, len(activity))
	items := make([]map[string]any, 0, len(activity))
	for _, item := range activity {
		lines = append(lines, item.Markdown)
		items = append(items, map[string]any{"cursor": item.Cursor,
			"invocation_id": item.InvocationID, "markdown": item.Markdown})
		if w.events != nil {
			w.events.Publish(observability.Event{Kind: "workflow.activity", RunID: item.WorkflowID,
				StepID: item.PointID, State: item.Kind, Count: 1})
		}
	}
	if w.notify == nil {
		return nil
	}
	return w.notify("notifications/message", map[string]any{"level": "info", "logger": "atenea.activity",
		"data": strings.Join(lines, "\n"), "activity": items})
}

func (w activityNotificationWriter) Write(p []byte) (int, error) {
	lines := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(p)), "\n") {
		if strings.HasPrefix(line, "> **ATENEA · ") || strings.HasPrefix(line, "> **PLAN · ") {
			lines = append(lines, line)
		}
	}
	if len(lines) > 0 {
		if w.notify == nil {
			return len(p), nil
		}
		if err := w.notify("notifications/message", map[string]any{"level": "info", "logger": "atenea.activity", "data": strings.Join(lines, "\n")}); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
