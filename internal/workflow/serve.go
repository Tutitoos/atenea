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
	runner, err := agent.New(agent.Options{
		Types:     cfg.Agents,
		Store:     traces,
		Workspace: WorkspaceFor(cfg, repository),
		History: func(ctx context.Context, name string, limit int) ([]trace.Row, error) {
			return traces.List(ctx, trace.Filter{TypeName: name, Limit: limit})
		},
	})
	if err != nil {
		_ = state.Close()
		_ = traces.Close()
		return nil, nil, err
	}
	engine, err := New(Options{
		Runner:  runner,
		Store:   state,
		Types:   cfg.Agents,
		Lanes:   cfg.Workflow,
		Surface: surface,
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
