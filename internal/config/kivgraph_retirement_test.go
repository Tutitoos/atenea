package config_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// The capability ids whose only implementations were Kivgraph's (#208). They
// left the shipped catalog with the provider.
var removedWithKivgraph = []string{
	"symbol.search", "symbol.intent_search", "symbol.definition", "symbol.references",
	"symbol.implementations", "symbol.dependencies", "symbol.consumers", "symbol.get",
	"symbol.unresolved", "symbol.source", "symbol.impact", "graph.status",
	"graph.repositories", "graph.ensure_fresh", "repository.index", "code.impact",
}

// The Kivgraph provider is gone (#208). A settings file that still configures
// it must fail at load with the edit that fixes it, not run as if it worked or
// fail with the bare "unknown key" a typo gets.
func TestRemovedKivgraphSettingsAreRefusedWithTheirRemedy(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"runner":         {"\n[orchestrator]\nrunners = [\"omp\", \"kivgraph\"]\n", `runner "kivgraph" was removed in contract 5.0.0`},
		"table":          {"\n[orchestrator.kivgraph]\nendpoint = \"http://127.0.0.1:7788/mcp\"\n", "[orchestrator.kivgraph] was removed in contract 5.0.0"},
		"bare table":     {"\n[orchestrator.kivgraph]\n", "[orchestrator.kivgraph] was removed in contract 5.0.0"},
		"process":        {"\n[orchestrator.kivgraph.process]\ncommand = \"kivgraph\"\nargs = [\"serve\"]\n", "[orchestrator.kivgraph] was removed in contract 5.0.0"},
		"dashboard":      {"\n[orchestrator.kivgraph]\ndashboard = \"http://127.0.0.1:7777\"\n\n[orchestrator.kivgraph.dashboard_process]\ncommand = \"kivgraph\"\nport = 7777\n", "[orchestrator.kivgraph] was removed in contract 5.0.0"},
		"implementation": {"\n[[implementation]]\nid = \"kivgraph.overview\"\nprovider = \"kivgraph\"\ncapability = \"code.search\"\n", "[[implementation]] kivgraph.overview was removed in contract 5.0.0"},
		"provider only":  {"\n[[implementation]]\nid = \"graph.overview\"\nprovider = \"kivgraph\"\ncapability = \"code.search\"\n", "[[implementation]] graph.overview was removed in contract 5.0.0"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(write(t, minimal+tc.body))
			refusedWith(t, err, tc.want)
		})
	}
}

// indexed_by named the providers holding a ready index for a repository, and
// Kivgraph was the only provider that indexed repositories.
func TestRemovedIndexedByKeyIsRefusedWithItsRemedy(t *testing.T) {
	for name, body := range map[string]string{
		"empty list": strings.Replace(minimal, `vcs = "present"`, "vcs = \"present\"\nindexed_by = []", 1),
		"named":      strings.Replace(minimal, `vcs = "present"`, "vcs = \"present\"\nindexed_by = [\"ripgrep\"]", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(write(t, body))
			refusedWith(t, err, "indexed_by was removed in contract 5.0.0")
		})
	}
}

// requires_index asked the funnel to drop an implementation until the provider
// held a ready index for the repository, and Kivgraph was the only provider that
// needed one. Every shipped file written before the removal still carries it.
func TestRemovedRequiresIndexKeyIsRefusedWithItsRemedy(t *testing.T) {
	for name, body := range map[string]string{
		"false": "\n[[implementation]]\nid = \"fixture.one\"\nprovider = \"fixture\"\ncapability = \"code.search\"\n\n  [implementation.constraints]\n  requires_index = false\n",
		"true":  "\n[[implementation]]\nid = \"fixture.one\"\nprovider = \"fixture\"\ncapability = \"code.search\"\n\n  [implementation.constraints]\n  languages = []\n  requires_index = true\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(write(t, minimal+body))
			refusedWith(t, err, "requires_index was removed in contract 5.0.0")
		})
	}
}

// A capability left with no implementation is not kept as a declared but
// unserved capability. A file that still declares, implements or routes one
// is refused, whichever block names it.
func TestRemovedCapabilitiesAreRefusedWhereverAFileNamesThem(t *testing.T) {
	for _, id := range removedWithKivgraph {
		want := `capability "` + id + `" was removed in contract 5.0.0`
		for name, body := range map[string]string{
			"capability":     "\n[[capability]]\nid = \"" + id + "\"\nversion = \"1.0.0\"\nsummary = \"x\"\neffects = [\"read\"]\n",
			"implementation": "\n[[implementation]]\nid = \"fixture.one\"\nprovider = \"fixture\"\ncapability = \"" + id + "\"\n",
			"selector rule":  "\n[[selector.rule]]\ncapability = \"" + id + "\"\nprefer = \"ripgrep\"\n",
		} {
			t.Run(id+" "+name, func(t *testing.T) {
				_, err := config.Load(write(t, minimal+body))
				refusedWith(t, err, want)
			})
		}
	}
}

// Several retired things in one file are all named, in one refusal.
func TestAFileWithSeveralRemovedKivgraphSettingsIsToldAboutAll(t *testing.T) {
	body := strings.Replace(minimal, `vcs = "present"`, "vcs = \"present\"\nindexed_by = []", 1) +
		"\n[orchestrator.kivgraph]\nendpoint = \"http://127.0.0.1:7788/mcp\"\n"
	_, err := config.Load(write(t, body))
	if err == nil {
		t.Fatal("file accepted")
	}
	for _, want := range []string{"indexed_by was removed", "[orchestrator.kivgraph] was removed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}

// A file written for the old shipped catalog names the runner, the table, the
// implementation blocks and the capabilities together, and is told about all of
// them in one refusal instead of one per edit.
func TestAFileWrittenForTheOldCatalogIsToldAboutEverythingAtOnce(t *testing.T) {
	body := minimal + `
[orchestrator]
runners = ["omp", "kivgraph"]

  [orchestrator.kivgraph]
  endpoint = "http://127.0.0.1:7788/mcp"

[[capability]]
id = "symbol.definition"
version = "1.0.0"
summary = "x"
effects = ["read"]

[[implementation]]
id = "kivgraph.definition"
provider = "kivgraph"
capability = "symbol.definition"

[[implementation]]
id = "kivgraph.references"
provider = "kivgraph"
capability = "symbol.references"

[[selector.rule]]
capability = "symbol.get"
prefer = "kivgraph.get"
`
	_, err := config.Load(write(t, body))
	if err == nil {
		t.Fatal("file accepted")
	}
	for _, want := range []string{
		`runner "kivgraph" was removed`,
		"[orchestrator.kivgraph] was removed",
		"[[implementation]] kivgraph.definition, kivgraph.references was removed",
		`capabilities "symbol.definition", "symbol.get", "symbol.references" was removed`,
		"migration-5.md",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}

// A 4.x file is told, before anything else, which of the retired settings to
// delete, and the sentence now names the Kivgraph ones too.
func TestTheContractRemedyNamesTheKivgraphSettings(t *testing.T) {
	_, err := config.Load(write(t, strings.Replace(minimal, `contract = "5.0.0"`, `contract = "4.1.0"`, 1)))
	if err == nil {
		t.Fatal("4.x file accepted")
	}
	for _, want := range []string{"Kivgraph", "[orchestrator.kivgraph]", `"kivgraph" runner`, "indexed_by", "migration-5.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}

// The shipped catalog carries nothing of Kivgraph and nothing that no
// provider serves: every declared capability has an implementation, and the
// four capabilities another provider still answers are still there.
func TestTheShippedCatalogHasNoKivgraphAndNoUnservedCapability(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	for _, impl := range cfg.Implementations {
		if impl.Provider == "kivgraph" || strings.HasPrefix(impl.ID, "kivgraph.") {
			t.Errorf("shipped implementation %s still belongs to kivgraph", impl.ID)
		}
		served[impl.Capability] = true
	}
	declared := map[string]bool{}
	for _, capability := range cfg.Capabilities {
		declared[capability.ID] = true
		if slices.Contains(removedWithKivgraph, capability.ID) {
			t.Errorf("shipped catalog still declares the removed capability %s", capability.ID)
		}
		if !served[capability.ID] {
			t.Errorf("capability %s has no implementation", capability.ID)
		}
	}
	for _, id := range []string{"code.search", "code.context", "symbol.overview", "symbol.calls"} {
		if !declared[id] || !served[id] {
			t.Errorf("%s must stay declared and served by a provider other than kivgraph", id)
		}
	}
	for _, runner := range cfg.Orchestrator.Runners {
		if runner == "kivgraph" {
			t.Error("the default runners still name kivgraph")
		}
	}
}

// refusedWith asserts the refusal is invalid_input, carries the named remedy
// and the document that explains it, and is not the bare typo message.
func refusedWith(t *testing.T, err error, want string) {
	t.Helper()
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("removed setting was not refused as invalid_input: %v", err)
	}
	for _, need := range []string{want, "migration-5.md"} {
		if !strings.Contains(err.Error(), need) {
			t.Fatalf("error %q lacks %q", err, need)
		}
	}
	if strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("error %q is the bare typo message", err)
	}
}
