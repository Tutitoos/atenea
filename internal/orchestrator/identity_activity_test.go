package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/observability"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/internal/toolstats"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestRuntimeIdentityActivityPrecedesProviderAndPersists(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stats := toolstats.New(filepath.Join(dir, "activity.sqlite"))
	checks, err := checkpoint.New(filepath.Join(dir, "checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{serves: []string{"ripgrep", "fixture.search"}}
	hub := observability.New(64)
	sub := hub.Subscribe(0)
	defer sub.Cancel()

	var mu sync.Mutex
	order := []string{}
	appendOrder := func(value string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, value)
	}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog: catalog(t), Chooser: chooser, Runner: runner, Checkpoints: checks, Events: hub,
		Identity: func(context.Context, contract.RunRequest) (contract.CacheIdentity, error) {
			appendOrder("provider")
			return contract.CacheIdentity{Observed: true, ToolVersion: "fixture-v1", Instance: "fixture-instance"}, nil
		},
		IdentityActivity: func(ctx context.Context, req contract.RunRequest) func(error) {
			appendOrder("activity-start")
			_, call := stats.Begin(ctx, toolstats.Event{Level: "identity", Tool: "provider.identity", Provider: req.Implementation.Provider, Repository: req.Repository.ID})
			return func(err error) {
				call.End(err)
				appendOrder("activity-end")
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Ask(context.Background(), orchestrator.Question{Capability: "code.search", Repository: "web", Payload: map[string]any{"query": "TODO"}}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	gotOrder := append([]string(nil), order...)
	mu.Unlock()
	if len(gotOrder) < 3 || gotOrder[0] != "activity-start" || gotOrder[1] != "provider" || gotOrder[2] != "activity-end" {
		t.Fatalf("identity activity order = %v", gotOrder)
	}
	snapshot, err := stats.Read(context.Background(), toolstats.Query{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range snapshot.Rows {
		if row.Level == "identity" && row.Name == "provider.identity" && row.Calls == 1 && row.OK == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("identity activity was not durable: %+v", snapshot.Rows)
	}

	// The live stream must expose the preamble and its completion in order.
	var events []observability.Event
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-sub.Events:
			events = append(events, event)
		case <-deadline:
			goto checked
		}
	}
checked:
	started, completed := -1, -1
	for i, event := range events {
		if event.Kind == "provider.identity.started" {
			started = i
		}
		if event.Kind == "provider.identity" {
			completed = i
		}
	}
	if started < 0 || completed < 0 || started >= completed {
		t.Fatalf("identity event order missing: %+v", events)
	}
}
