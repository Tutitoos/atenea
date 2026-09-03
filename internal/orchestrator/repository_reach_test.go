package orchestrator_test

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestRealDispatchUsesRepositoryScopedReach(t *testing.T) {
	for _, available := range []bool{true, false} {
		runner := &fakeRunner{serves: []string{"ripgrep", "fixture.search"}}
		chooser, err := selector.New(selector.Config{})
		if err != nil {
			t.Fatal(err)
		}
		a, err := orchestrator.New(orchestrator.Config{Catalog: catalog(t), Chooser: chooser, Runner: runner,
			Reach: func(repo contract.Repository) ([]string, map[string]string) {
				if repo.ID != "api" {
					t.Fatalf("unexpected repository %s", repo.ID)
				}
				ids := []string{}
				if available {
					ids = append(ids, "ripgrep")
				}
				return ids, map[string]string{"fixture.search": "outside configured root"}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.Ask(t.Context(), orchestrator.Question{Capability: "code.search", Repository: "api", Prefer: "fixture", Payload: map[string]any{"query": "TODO"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Steps) != 1 {
			t.Fatalf("%v", result)
		}
		step := result.Steps[0]
		if step.Dispatched != available {
			t.Fatalf("dispatched=%v", step.Dispatched)
		}
		for _, req := range runner.requests() {
			if req.Implementation.ID != "ripgrep" {
				t.Fatal("out of scope runner invoked")
			}
		}
		if !available && len(runner.requests()) != 0 {
			t.Fatal("nothing should run")
		}
	}
}
