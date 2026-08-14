package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/internal/agent"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/trace"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// servedWorkspace runs one agent that declared the workspace level and hands
// back the level exactly as the agent received it on stdin.
func servedWorkspace(t *testing.T, costs func(context.Context) (agent.CostTable, error)) map[string]any {
	t.Helper()
	captured := filepath.Join(t.TempDir(), "assignment.json")
	spec := declared(stub(t, "cat >"+captured+"\ncat <<'REPORT'\n"+
		`{"result":{"path":"a.txt"},"verdict":"ok"}`+"\nREPORT"))
	spec.Context = []contract.ContextLevel{contract.ContextWorkspace}

	store, err := trace.Open(t.Context(), filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("opening the trace store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	r, err := agent.New(agent.Options{
		Types:     []config.AgentType{spec},
		Store:     store,
		Self:      "/nonexistent/atenea",
		Workspace: agent.Workspace{RepositoryID: "current", Repositories: []string{"current"}},
		Costs:     costs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, err := r.Run(t.Context(), "reader", task(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("reading what the agent was handed: %v", err)
	}
	var payload struct {
		Context struct {
			Workspace map[string]any `json:"workspace"`
		} `json:"context"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("the assignment is not json: %v", err)
	}
	return payload.Context.Workspace
}

// The cost table reaches the agent's stdin, counts and all. This is the seam
// the first attempt got wrong: everything below it was tested, the type that
// needed the numbers had not declared the level, and the table was measured,
// converted, and thrown away in silence.
func TestTheCostTableReachesTheAgent(t *testing.T) {
	workspace := servedWorkspace(t, func(context.Context) (agent.CostTable, error) {
		return agent.CostTable{
			Repository: "atenea",
			Types: map[string]agent.Cost{
				"explore": {MedianUSD: 1.63, MinUSD: 1.26, MaxUSD: 2.16, N: 3, AtCeiling: 1, Unmeasured: 2},
			},
		}, nil
	})

	costs, ok := workspace["costs"].(map[string]any)
	if !ok {
		t.Fatalf("workspace = %v, want a cost table", workspace)
	}
	if costs["repository"] != "atenea" {
		t.Errorf("repository = %v, want the scope named", costs["repository"])
	}
	if costs["covers"] != "workflow steps only" {
		t.Errorf("covers = %v: the payload must not imply it prices single agent runs", costs["covers"])
	}
	explore, ok := costs["types"].(map[string]any)["explore"].(map[string]any)
	if !ok {
		t.Fatalf("types = %v, want explore", costs["types"])
	}
	for field, want := range map[string]float64{
		"median_usd": 1.63, "min_usd": 1.26, "max_usd": 2.16,
		"n": 3, "at_ceiling": 1, "unmeasured": 2,
	} {
		if got, _ := explore[field].(float64); got != want {
			t.Errorf("%s = %v, want %v", field, explore[field], want)
		}
	}
}

// A machine that has measured nothing serves no table at all. Zeros here would
// read as "everything is free", which is the failure this whole path exists to
// prevent.
func TestAMachineWithNoMeasurementsServesNoTable(t *testing.T) {
	workspace := servedWorkspace(t, func(context.Context) (agent.CostTable, error) {
		return agent.CostTable{}, nil
	})
	if _, present := workspace["costs"]; present {
		t.Errorf("workspace = %v, want no cost table on a machine with no rows", workspace)
	}
	if _, present := workspace["repositories"]; !present {
		t.Error("the workspace level lost its repositories")
	}
}
