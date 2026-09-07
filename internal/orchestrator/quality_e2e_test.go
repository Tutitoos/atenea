package orchestrator_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type qualityRouteRunner struct {
	mu       sync.Mutex
	results  map[string][]bool
	identity map[string]contract.CacheIdentity
}

func (r *qualityRouteRunner) ID() string                { return "quality-routes" }
func (r *qualityRouteRunner) Serves(id string) bool     { return id == "route-a" || id == "route-b" }
func (r *qualityRouteRunner) Implementations() []string { return []string{"route-a", "route-b"} }
func (r *qualityRouteRunner) Capabilities() []string    { return []string{"code.context"} }
func (r *qualityRouteRunner) RuntimeIdentity(_ context.Context, req contract.RunRequest) (contract.CacheIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	identity, ok := r.identity[req.Implementation.ID]
	if !ok {
		return contract.CacheIdentity{}, errors.New("missing runtime identity")
	}
	return identity, nil
}
func (r *qualityRouteRunner) Run(_ context.Context, req contract.RunRequest) (contract.Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	identity := r.identity[req.Implementation.ID]
	queue := r.results[req.Implementation.ID]
	if len(queue) == 0 {
		return contract.Outcome{}, errors.New("fixture route exhausted")
	}
	ok := queue[0]
	r.results[req.Implementation.ID] = queue[1:]
	if !ok {
		return contract.Outcome{Verdict: contract.VerdictFailed, ToolVersion: identity.ToolVersion, ToolInstance: identity.Instance}, errors.New("fixture route unavailable")
	}
	return contract.Outcome{Verdict: contract.VerdictOK, ToolVersion: identity.ToolVersion, ToolInstance: identity.Instance,
		Result: map[string]any{"matches": []any{}}, Evidence: []contract.QueryEvidence{{Freshness: "fresh", ContentGeneration: 1, SnapshotID: 1, Completeness: "complete"}}}, nil
}

func qualityContextFixture(t *testing.T, root string) *registry.Registry {
	t.Helper()
	reg := registry.New()
	capability := contract.Capability{ID: "code.context", Version: contract.Version{Major: 1}, Summary: "fixture context", Effects: []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{{Name: "query", Type: contract.TypeString, Required: true}}, Outputs: []contract.Field{{Name: "matches", Type: contract.TypeRecordList, Required: true, Fields: []contract.Field{{Name: "path", Type: contract.TypeString, Required: true}}}}}
	if err := reg.AddCapability(capability); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"route-a", "route-b"} {
		if err := reg.AddImplementation(contract.Implementation{ID: id, Provider: id, Capability: "code.context", ConfigDigest: "cfg-" + id,
			Health: contract.Health{State: contract.HealthAlive, Score: 0.8}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.AddRepository(contract.NewRepository("repo", root, []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil)); err != nil {
		t.Fatal(err)
	}
	return reg
}

// TestAgentRuntimeIdentityQualityRestart exercises the complete productive
// route: Agent identity probe, Selector choice, runner outcome, RecordOutcome,
// durable ledger and a new Agent after restart. A one-call preference still
// overrides the durable quality decision.
func TestAgentRuntimeIdentityQualityRestart(t *testing.T) {
	root := t.TempDir()
	ledger := filepath.Join(root, "quality.json")
	runner := &qualityRouteRunner{
		results: map[string][]bool{"route-a": {true, false}, "route-b": {true, true, true}},
		identity: map[string]contract.CacheIdentity{
			"route-a": {Observed: true, ToolVersion: "v1", Instance: "cfg-route-a@session", Provider: "route-a", Tool: "graph_status"},
			"route-b": {Observed: true, ToolVersion: "v1", Instance: "cfg-route-b@session", Provider: "route-b", Tool: "tokensave_status"},
		},
	}
	reg := qualityContextFixture(t, root)
	newAgent := func() *orchestrator.Agent {
		chooser, err := selector.New(selector.Config{QualityPath: ledger, QualityMinimumSamples: 2})
		if err != nil {
			t.Fatal(err)
		}
		checks, err := checkpoint.New(filepath.Join(root, "checkpoints"))
		if err != nil {
			t.Fatal(err)
		}
		agent, err := orchestrator.New(orchestrator.Config{Catalog: reg, Chooser: chooser, Runner: runner, Checkpoints: checks,
			Identity: runner.RuntimeIdentity})
		if err != nil {
			t.Fatal(err)
		}
		return agent
	}
	first := newAgent()
	ask := func(agent *orchestrator.Agent, prefer string) {
		t.Helper()
		if _, err := agent.Ask(context.Background(), orchestrator.Question{Capability: "code.context", Repository: "repo", Prefer: prefer, Payload: map[string]any{"query": "TODO"}}); err != nil {
			t.Fatal(err)
		}
	}
	// Initial tie goes to route-a; then both routes receive enough real
	// outcomes for quality to be comparable after a restart.
	ask(first, "")
	ask(first, "route-a")
	ask(first, "route-b")
	ask(first, "route-b")
	second := newAgent()
	result, err := second.Ask(context.Background(), orchestrator.Question{Capability: "code.context", Repository: "repo", Payload: map[string]any{"query": "TODO"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Steps) != 1 || result.Steps[0].Decision.Chosen.ID != "route-b" {
		t.Fatalf("durable quality choice = %+v, want route-b", result.Steps)
	}
	result, err = second.Ask(context.Background(), orchestrator.Question{Capability: "code.context", Repository: "repo", Prefer: "route-a", Payload: map[string]any{"query": "TODO"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps[0].Decision.Chosen.ID != "route-a" {
		t.Fatalf("preference choice = %s, want route-a", result.Steps[0].Decision.Chosen.ID)
	}
}
