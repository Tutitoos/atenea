package selector_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestProviderPreferenceAndExactID(t *testing.T) {
	a := impl("graph.a", health(contract.HealthAlive, 0.5))
	b := impl("graph.b", health(contract.HealthAlive, 1))
	a.Provider, b.Provider = "graph", "graph"
	other := impl("other", health(contract.HealthAlive, 1))
	for _, tc := range []struct{ preference, want string }{{"graph", "graph.b"}, {"graph.a", "graph.a"}, {" graph ", "graph.b"}} {
		d, err := mustSelector(t).Select(selector.Request{Capability: "code.search", Repository: smallGoRepo(), Candidates: []contract.Implementation{other, a, b}, Prefer: tc.preference})
		if err != nil || d.Chosen.ID != tc.want {
			t.Fatalf("%s: %s, %v", tc.preference, d.Chosen.ID, err)
		}
	}
	// An exact ID takes precedence even if a different provider has that name.
	other.ID = "graph"
	d, err := mustSelector(t).Select(selector.Request{Capability: "code.search", Repository: smallGoRepo(), Candidates: []contract.Implementation{other, a, b}, Prefer: "graph"})
	if err != nil || d.Chosen.ID != "graph" {
		t.Fatalf("exact ID: %v %v", d, err)
	}
}

func TestUnknownPreferenceDoesNotSilentlyFallback(t *testing.T) {
	d, err := mustSelector(t).Select(selector.Request{Capability: "code.search", Repository: smallGoRepo(), Candidates: []contract.Implementation{impl("ripgrep")}, Prefer: "kivgraph"})
	if contract.KindOf(err) != contract.FailureInvalidInput || d.Chosen.ID != "" || !strings.Contains(err.Error(), "available implementations: ripgrep") {
		t.Fatalf("%v %v", d, err)
	}
}

func TestUnavailableProviderFallbackExplainsRepositoryScope(t *testing.T) {
	a := impl("graph.a")
	a.Provider = "graph"
	d, err := mustSelector(t).Select(selector.Request{Capability: "code.search", Repository: smallGoRepo(), Candidates: []contract.Implementation{a, impl("ripgrep")}, Prefer: "graph", Reachable: []string{"ripgrep"}, Unreachable: map[string]string{"graph.a": "outside root"}})
	if err != nil || d.Chosen.ID != "ripgrep" || len(d.Notices) == 0 {
		t.Fatalf("%v %v", d, err)
	}
	if d.Stages[1].Dropped[0].Code != "repository_scope" {
		t.Fatalf("%v", d.Stages)
	}
}
