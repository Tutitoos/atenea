package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tutitoos/atenea/internal/workflow"
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
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file": map[string]any{
					"type":        "string",
					"description": "Path to the graph file.",
				},
			},
			"required": []string{"file"},
		},
	}, {
		"name": toolWorkflowLaunch,
		"description": "Launch a plan that " + toolWorkflowCreate + " wrote down, and run it to the end. " +
			"This commits the grant and lets the agents spawn, so it is a person's call and not a model's. " +
			"If the graph grows mid-run it stops and waits for an approval, indefinitely; " +
			"nothing new is dispatched while it waits.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The workflow id returned by " + toolWorkflowCreate + ".",
				},
			},
			"required": []string{"id"},
		},
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
	engine, closers, err := v.workflowEngine(ctx, args)
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
	engine, closers, err := v.workflowEngine(ctx, args)
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}
	defer closers()

	run, runErr := engine.Launch(ctx, strings.TrimSpace(id))
	if run.ID == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: runErr.Error()}
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

// workflowEngine builds the engine this connection's calls run through.
//
// Per call, and over the same trace database the CLI uses: a workflow held in
// one process's memory would be one nobody else could answer a gate on, and
// the gate outliving its asker is the whole design.
func (v *conversation) workflowEngine(ctx context.Context, args map[string]any) (*workflow.Engine, func(), error) {
	surface := "mcp"
	if v.session != nil {
		surface = "mcp session " + v.session.ID()
	}
	repository, _ := args[repositoryArg].(string)
	return workflow.Serve(ctx, v.core.settings, "", strings.TrimSpace(repository), surface, nil)
}
