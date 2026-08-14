package workflow

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/trace"
)

// Serve builds an engine over the same trace database the agents write to,
// and hands back the closer for both.
//
// It lives here rather than in either caller because the CLI and the MCP
// server must build the SAME engine. Two copies of this wiring is two places
// for a lane ceiling or a workspace root to drift, and the gate log would
// then record approvals of plans that ran under settings the reader cannot
// see.
func Serve(ctx context.Context, cfg config.Config, tracePath, repository, surface string,
	notices io.Writer) (*Engine, func(), error) {
	traces, err := trace.Open(ctx, tracePath)
	if err != nil {
		return nil, nil, err
	}
	closed, err := traces.SweepOrphans(ctx, time.Now())
	if err != nil {
		_ = traces.Close()
		return nil, nil, err
	}
	if closed > 0 && notices != nil {
		noun := "traces"
		if closed == 1 {
			noun = "trace"
		}
		fmt.Fprintf(notices, "closed %d %s left open by a previous run, as incomplete\n", closed, noun)
	}
	state, err := Open(ctx, traces.Path())
	if err != nil {
		_ = traces.Close()
		return nil, nil, err
	}
	workspace := WorkspaceFor(cfg, repository)
	runner, err := agent.New(agent.Options{
		Types:     cfg.Agents,
		Store:     traces,
		Workspace: workspace,
		History: func(ctx context.Context, name string, limit int) ([]trace.Row, error) {
			return traces.List(ctx, trace.Filter{TypeName: name, Limit: limit})
		},
		Costs: func(ctx context.Context) (agent.CostTable, error) {
			table, err := state.CostByType(ctx, workspace.RepositoryID)
			if err != nil {
				return agent.CostTable{}, err
			}
			return costsFor(table), nil
		},
	})
	if err != nil {
		_ = state.Close()
		_ = traces.Close()
		return nil, nil, err
	}
	engine, err := New(Options{
		Runner:     runner,
		Store:      state,
		Types:      cfg.Agents,
		Lanes:      cfg.Workflow,
		Surface:    surface,
		Repository: workspace.RepositoryID,
	})
	if err != nil {
		_ = state.Close()
		_ = traces.Close()
		return nil, nil, err
	}
	return engine, func() {
		_ = state.Close()
		_ = traces.Close()
	}, nil
}

// WorkspaceFor resolves which repository an agent serves at the repository
// context level.
func WorkspaceFor(cfg config.Config, id string) agent.Workspace {
	ws := agent.Workspace{AteneaVersion: buildinfo.Version}
	for _, repo := range cfg.Repositories {
		ws.Repositories = append(ws.Repositories, repo.ID)
		if repo.ID == id || (id == "" && ws.RepositoryID == "") {
			ws.RepositoryID, ws.RepositoryRoot = repo.ID, repo.Path
		}
	}
	sort.Strings(ws.Repositories)
	if ws.RepositoryRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			ws.RepositoryID, ws.RepositoryRoot = "current", cwd
		}
	}
	return ws
}

// costsFor converts what the store measured into what an agent is served.
//
// A conversion and not a shared type on purpose: the store's row counts are a
// record, the agent's copy is evidence handed to a model, and letting one
// struct be both is how a field added for the record quietly reaches a prompt.
func costsFor(table CostTable) agent.CostTable {
	out := agent.CostTable{
		Repository: table.Repository,
		Types:      make(map[string]agent.Cost, len(table.Types)),
	}
	for name, observed := range table.Types {
		out.Types[name] = agent.Cost{
			MedianUSD:  observed.MedianUSD,
			MinUSD:     observed.MinUSD,
			MaxUSD:     observed.MaxUSD,
			N:          observed.N,
			AtCeiling:  observed.AtCeiling,
			Unmeasured: observed.Unmeasured,
		}
	}
	return out
}
