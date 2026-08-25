package core

import (
	"context"
	"fmt"
	"strings"

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
)

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
	if refusal := v.authorize(toolWorkflowCreate, graph.Effects()); refusal != nil {
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
		"digest": workflow.Short(gate.Digest),
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
	if refusal := v.authorize(toolWorkflowLaunch, effects); refusal != nil {
		return refusal, nil
	}

	run, runErr := engine.Launch(ctx, id)
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
func (v *conversation) authorize(tool string, effects []contract.Effect) any {
	if len(effects) == 0 {
		return nil
	}
	if err := v.session.entitled(effects); err != nil {
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
	return workflow.Serve(ctx, v.core.settings, "", repository, surface, nil)
}
