package agentdevice

import "testing"

func TestManifestMatchesAgentDevice20(t *testing.T) {
	if got := len(Tools()); got != 57 {
		t.Fatalf("full catalog = %d tools, want 57", got)
	}
	if got := len(CoreTools()); got != 20 {
		t.Fatalf("core catalog = %d tools, want 20", got)
	}
	for _, tool := range CoreTools() {
		if !CatalogAllows("full", tool) || !CatalogAllows("core", tool) {
			t.Errorf("core tool %q missing from catalog", tool)
		}
	}
	if CatalogAllows("core", "record") {
		t.Error("heavy tool record leaked into core catalog")
	}
}

func TestMissingReportsManifestDrift(t *testing.T) {
	missing := Missing([]string{"devices", "snapshot"})
	if len(missing) != 55 {
		t.Fatalf("missing = %d, want 55", len(missing))
	}
}
