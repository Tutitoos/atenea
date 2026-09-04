package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
)

func TestDocumentedSymbolSearchMatchesCatalog(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	available := false
	for _, impl := range cfg.Implementations {
		if impl.ID == "kivgraph.search" {
			available = true
		}
	}
	document, err := os.ReadFile("../../docs/content/v1-policy.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(document)
	if !available || !strings.Contains(text, "`symbol.search` se sirve mediante `kivgraph.search`") {
		t.Fatal("policy and catalog disagree")
	}
}
