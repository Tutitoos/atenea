package core_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestMCPExcludesTokensaveOutsideRootAndExplainsAvailability(t *testing.T) {
	settings := strings.Replace(mcpSettings(t), `runners = ["local"]`, `runners = ["local", "tokensave"]`, 1)
	settings += fmt.Sprintf(`
[orchestrator.tokensave]
root = %q
implementations = ["tokensave.overview"]
[orchestrator.tokensave.process]
command = "/nonexistent/atenea-should-never-launch"
lifecycle = "on_demand"
[[capability]]
id = "symbol.overview"
version = "1.0.0"
summary = "List symbols."
effects = ["read"]
[[implementation]]
id = "tokensave.overview"
provider = "tokensave"
capability = "symbol.overview"
[implementation.health]
state = "alive"
`, t.TempDir())
	atenea := buildService(t, settings)
	defer serve(t, atenea)()
	c := dial(t)
	c.handshake("codex")
	discovery := result(t, c.call("tools/call", map[string]any{"name": "catalog.repositories"}), "repositories")
	if !strings.Contains(fmt.Sprint(discovery["structuredContent"]), "repository_scope") {
		t.Fatalf("missing scope diagnostic: %v", discovery)
	}
	diagnosis, err := atenea.Select("symbol.overview", "work")
	if err == nil || diagnosis.Chosen.ID != "" {
		t.Fatal("diagnosis offered out-of-scope runner")
	}
	got := result(t, c.call("tools/call", map[string]any{"name": "symbol.overview", "arguments": map[string]any{"repository": "work", "_atenea_prefer": "tokensave"}}), "overview")
	if got["isError"] != true {
		t.Fatalf("expected unavailable: %v", got)
	}
	body := fmt.Sprint(got["content"])
	for _, want := range []string{`"invoked":false`, `"reason":"repository_scope"`, `"atenea_usage"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "did not come up") {
		t.Fatal("out of scope process was started")
	}
}

func TestMCPUnknownPreferenceNeverInvokesAlternative(t *testing.T) {
	atenea := buildService(t, mcpSettings(t))
	defer serve(t, atenea)()
	c := dial(t)
	c.handshake("codex")
	got := result(t, c.call("tools/call", map[string]any{"name": "code.search", "arguments": map[string]any{"repository": "work", "query": "TODO", "_atenea_prefer": "unknown"}}), "search")
	if got["isError"] != true || !strings.Contains(fmt.Sprint(got["content"]), `"invoked":false`) {
		t.Fatalf("%v", got)
	}
}
