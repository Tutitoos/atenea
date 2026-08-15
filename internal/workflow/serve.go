package workflow

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/floor"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
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
	here, err := os.Getwd()
	if err != nil {
		_ = state.Close()
		_ = traces.Close()
		return nil, nil, contract.Fail(contract.FailureUnavailable,
			"cannot read the working directory to resolve which repository this run is about: %v", err)
	}
	workspace, err := WorkspaceFor(cfg, repository, here)
	if err != nil {
		_ = state.Close()
		_ = traces.Close()
		return nil, nil, err
	}
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
	// A measurement cache that will not open is not a reason to stop somebody
	// launching work: the check goes off and the notice says so, so a person
	// who expected a plan to be refused knows why it was not. Nothing here
	// invents a floor to stand in for the one it could not read.
	var measured Floors
	if store, floorErr := floor.Open(""); floorErr != nil {
		if notices != nil {
			fmt.Fprintf(notices, "cannot read the measured cost of starting a turn (%v): "+
				"plans will not be checked against it\n", floorErr)
		}
	} else {
		measured = floors{store: store}
	}
	engine, err := New(Options{
		Runner:     runner,
		Store:      state,
		Types:      cfg.Agents,
		Lanes:      cfg.Workflow,
		Surface:    surface,
		Repository: workspace.RepositoryID,
		Floors:     measured,
		ModelFor:   modelFor(cfg),
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
// context level: the one named, or the one `dir` is inside.
//
// The directory is an argument because the answer is about where the work is,
// and a long-lived process serving many runs must not answer from wherever it
// was started.
//
// Two silent substitutions were removed here on 2026-08-14, both measured on
// one real run. This machine's settings declare a repository whose id is
// `current`, which was also the name this function invented for "the tree you
// are standing in" -- so a run launched in /tmp/e2e/repo was served
// /home/tutitoos/Desktop/atenea, and every agent in it was told it was
// somewhere it was not. A sentinel that a settings file may legally declare is
// not a sentinel. There is no invented name now: an unregistered tree serves
// its own root under an empty id, which reads as "not one of the declared
// repositories" and cannot be captured by declaring anything.
//
// The other was `id == ""` taking the first repository in the file. Order in a
// settings file is not a statement about where anyone is working, and a
// caller that named nothing is asking about here.
func WorkspaceFor(cfg config.Config, id, dir string) (agent.Workspace, error) {
	ws := agent.Workspace{AteneaVersion: buildinfo.Version}
	for _, repo := range cfg.Repositories {
		ws.Repositories = append(ws.Repositories, repo.ID)
	}
	sort.Strings(ws.Repositories)

	if id != "" {
		for _, repo := range cfg.Repositories {
			if repo.ID == id {
				ws.RepositoryID, ws.RepositoryRoot = repo.ID, repo.Path
				return ws, nil
			}
		}
		// Falling back to the working directory here is how a typo becomes a
		// run against a tree nobody named.
		return agent.Workspace{}, contract.Fail(contract.FailureNotFound,
			"no repository %q is declared: this machine declares %s",
			id, strings.Join(ws.Repositories, ", "))
	}

	// Deepest wins: a repository nested inside another is the more specific
	// answer about where this directory is.
	for _, repo := range cfg.Repositories {
		if !within(dir, repo.Path) {
			continue
		}
		if len(repo.Path) > len(ws.RepositoryRoot) {
			ws.RepositoryID, ws.RepositoryRoot = repo.ID, repo.Path
		}
	}
	if ws.RepositoryRoot != "" {
		return ws, nil
	}
	// Not a declared repository. The work still happens in a tree, and the
	// agents still need a root, but no id is invented for it.
	if root, ok := config.RepoRoot(dir); ok {
		ws.RepositoryRoot = root
		return ws, nil
	}
	ws.RepositoryRoot = dir
	return ws, nil
}

// within reports whether dir is root or sits inside it.
func within(dir, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
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

// floors adapts the measured floors on disk to what the engine asks of them.
//
// A conversion for the same reason costsFor above is one: the store's row is a
// record of a probe -- tokens in and out, the client that answered, when --
// and the engine needs the figure plus enough provenance to print in a
// refusal. One struct being both is how a field added for the record turns up
// in a message to a person.
type floors struct{ store *floor.Store }

// Floor answers what starting a turn costs, from what was measured. The
// context is unused: the store is a file this process reads, with nothing to
// cancel.
func (f floors) Floor(_ context.Context, repository, agent, model string) (Floor, bool, error) {
	measured, ok, err := f.store.Get(repository, agent, model)
	if err != nil || !ok {
		return Floor{}, false, err
	}
	return Floor{
		USD:              measured.USD,
		MeasuredAt:       measured.MeasuredAt,
		CLIVersion:       measured.CLIVersion,
		CacheWriteTokens: measured.CacheWriteTokens,
	}, true, nil
}

// modelFor resolves which model an agent type spends against, out of the two
// the settings file fixes -- see internal/config's Model.
//
// `reader` answers with the explore model because it IS the explore
// implementation, spawned with no MCP config: same role, same client, same
// two configured names, and a floor measured for one is the floor of the
// other's model. A settings file that runs `agent-exec explore` under some
// other type name of its own is unpriced rather than mispriced, which is the
// direction to be wrong in.
//
// Everything else answers "", and the two readings of that are different.
// For `filereader`, `reviewer` and `plan-check` it is final and correct:
// those three answer in deterministic Go, call no model at all, and have no
// model-shaped floor to look up. For anything else it means nobody
// configured a model for that type, so nobody measured a turn for it either,
// and guessing here would put a floor on a step whose price was never taken.
func modelFor(cfg config.Config) func(agentType string) string {
	return func(agentType string) string {
		switch agentType {
		case "explore", "reader":
			return cfg.Model.Explore
		case "plan":
			return cfg.Model.Plan
		}
		return ""
	}
}
