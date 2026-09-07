package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/internal/workspacecontext"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type workspaceFixtureRunner struct {
	mu       sync.Mutex
	calls    []contract.RunRequest
	sequence *[]string
}

func (r *workspaceFixtureRunner) ID() string                { return "fixture" }
func (r *workspaceFixtureRunner) Serves(id string) bool     { return id == "fixture.context" }
func (r *workspaceFixtureRunner) Implementations() []string { return []string{"fixture.context"} }
func (r *workspaceFixtureRunner) Capabilities() []string    { return []string{"code.context"} }
func (r *workspaceFixtureRunner) Run(_ context.Context, req contract.RunRequest) (contract.Outcome, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req)
	if r.sequence != nil {
		*r.sequence = append(*r.sequence, "run:"+req.Repository.ID)
	}
	r.mu.Unlock()
	return contract.Outcome{
		Result:   map[string]any{"symbols": []any{map[string]any{"name": req.Repository.ID}}},
		Evidence: []contract.QueryEvidence{{Completeness: "complete", Freshness: "fresh", ContentGeneration: 1, SnapshotID: 1}},
		Verdict:  contract.VerdictOK,
	}, nil
}

func workspaceFixtureCapability() contract.Capability {
	return contract.Capability{
		ID: "code.context", Version: contract.Version{Major: 1}, Summary: "fixture context", Effects: []contract.Effect{contract.EffectRead},
		Inputs:  []contract.Field{{Name: "task", Type: contract.TypeString, Required: true}},
		Outputs: []contract.Field{{Name: "symbols", Type: contract.TypeRecordList, Required: true, Fields: []contract.Field{{Name: "name", Type: contract.TypeString, Required: true}}}},
	}
}

func workspaceFixtureCore(t *testing.T, runner *workspaceFixtureRunner, repos ...contract.Repository) *Core {
	t.Helper()
	catalog := registry.New()
	if err := catalog.AddCapability(workspaceFixtureCapability()); err != nil {
		t.Fatal(err)
	}
	if err := catalog.AddImplementation(contract.Implementation{ID: "fixture.context", Provider: "fixture", Capability: "code.context", Health: contract.Health{State: contract.HealthAlive}}); err != nil {
		t.Fatal(err)
	}
	for _, repo := range repos {
		if err := catalog.AddRepository(repo); err != nil {
			t.Fatal(err)
		}
	}
	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := orchestrator.New(orchestrator.Config{Catalog: catalog, Chooser: chooser, Runner: runner, BudgetUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	return &Core{catalog: catalog, agent: agent, settings: config.Config{Orchestrator: config.Orchestrator{MaxParallel: 2, BudgetUSD: 1}}}
}

func TestWorkspaceContextCoreUsesOrdinarySingularChildren(t *testing.T) {
	api := contract.NewRepository("api", filepath.Join(t.TempDir(), "api"), []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
	web := contract.NewRepository("web", filepath.Join(t.TempDir(), "web"), []string{"typescript"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
	for _, repo := range []contract.Repository{api, web} {
		if err := os.MkdirAll(repo.Path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var sequence []string
	runner := &workspaceFixtureRunner{sequence: &sequence}
	core := workspaceFixtureCore(t, runner, api, web)
	result, err := core.WorkspaceContext(context.Background(), nil, workspacecontext.Request{
		Targets: []workspacecontext.Target{{ID: "api"}, {ID: "web"}}, Payload: map[string]any{"task": "find entry points"},
		Permission: contract.Permission{Task: workspacecontext.Capability, Effects: []contract.Effect{contract.EffectRead}},
		BeforeDispatch: func(_ context.Context, target workspacecontext.Target) error {
			sequence = append(sequence, "notice:"+target.ID)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Partial || len(result.Repositories) != 2 {
		t.Fatalf("workspace result = %+v", result)
	}
	runner.mu.Lock()
	calls := append([]contract.RunRequest(nil), runner.calls...)
	runner.mu.Unlock()
	if len(calls) != 2 || calls[0].Capability.ID != "code.context" || calls[1].Capability.ID != "code.context" || calls[0].Repository.ID == calls[1].Repository.ID {
		t.Fatalf("singular child calls = %+v", calls)
	}
	for _, row := range result.Repositories {
		if !row.Started || row.Error != "" || row.Result == nil || row.Provider != "fixture" || row.Implementation != "fixture.context" {
			t.Fatalf("row = %+v", row)
		}
	}
	if len(sequence) != 4 {
		t.Fatalf("activity/dispatch sequence = %v", sequence)
	}
	if sequence[0] != "notice:api" || sequence[1] != "notice:web" {
		t.Fatalf("notices were not ordered before dispatch: %v", sequence)
	}
	for _, call := range calls {
		if call.Permission.BudgetUSD <= 0 || call.Permission.BudgetUSD != 0.5 {
			t.Fatalf("budget share for %s = %v", call.Repository.ID, call.Permission.BudgetUSD)
		}
	}
}

func TestWorkspaceContextMCPUsesFixtureCoreAndEmitsNotice(t *testing.T) {
	api := contract.NewRepository("api", filepath.Join(t.TempDir(), "api"), []string{"go"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
	web := contract.NewRepository("web", filepath.Join(t.TempDir(), "web"), []string{"typescript"}, contract.ScaleSmall, contract.VCSUnspecified, nil)
	for _, repo := range []contract.Repository{api, web} {
		if err := os.MkdirAll(repo.Path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var sequence []string
	runner := &workspaceFixtureRunner{sequence: &sequence}
	fixtureCore := workspaceFixtureCore(t, runner, api, web)
	conversation := &conversation{core: fixtureCore, notify: func(method string, params any) error {
		if method != "notifications/message" {
			return fmt.Errorf("unexpected notification %s", method)
		}
		sequence = append(sequence, "notice")
		if !strings.Contains(fmt.Sprint(params), "code.context") {
			return fmt.Errorf("notification missing code.context: %v", params)
		}
		return nil
	}}
	raw, rpcErr := conversation.workspaceContext(context.Background(), map[string]any{
		"targets": []any{map[string]any{"id": "api"}, map[string]any{"id": "web"}},
		"payload": map[string]any{"task": "find entry points"},
	})
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	body := raw.(map[string]any)["structuredContent"].(map[string]any)
	rows, ok := body["repositories"].([]any)
	if !ok {
		t.Fatalf("MCP repositories shape = %#v", body["repositories"])
	}
	if len(rows) != 2 {
		t.Fatalf("MCP rows = %#v", body["repositories"])
	}
	for _, rawRow := range rows {
		row, ok := rawRow.(map[string]any)
		if !ok || row["started"] != true || row["error"] != nil || row["result"] == nil || row["provider"] != "fixture" || row["implementation"] != "fixture.context" {
			t.Fatalf("MCP row = %+v", row)
		}
	}
	if len(sequence) < 3 || sequence[0] != "notice" {
		t.Fatalf("MCP notice/dispatch sequence = %v", sequence)
	}
}
