package config_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestRetiredSerenaConfigurationIsRejected(t *testing.T) {
	for name, body := range map[string]string{
		"old contract":   strings.Replace(minimal, `contract = "4.0.0"`, `contract = "3.6.0"`, 1),
		"runner":         minimal + "\n[orchestrator]\nrunners = [\"serena\"]\n",
		"adapter":        minimal + "\n[orchestrator.serena]\nendpoint = \"http://127.0.0.1:40010/mcp\"\n",
		"process":        minimal + "\n[orchestrator.serena.process]\ncommand = \"serena\"\nlifecycle = \"persistent\"\n",
		"mcp":            minimal + "\n[[mcp_server]]\nid = \"serena\"\nurl = \"http://127.0.0.1:40010/mcp\"\nexpose = \"off\"\n",
		"implementation": minimal + "\n[[implementation]]\nid = \"serena.definition\"\nprovider = \"serena\"\ncapability = \"symbol.definition\"\n",
		"index":          strings.Replace(minimal, `vcs = "present"`, "vcs = \"present\"\nindexed_by = [\"serena\"]", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(write(t, body))
			if contract.KindOf(err) != contract.FailureInvalidInput {
				t.Fatalf("retired configuration accepted: %v", err)
			}
		})
	}
}

func TestRetiredProviderKeepsOnlyUnsupportedContractsDormant(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	offered := map[string]bool{}
	for _, impl := range cfg.Implementations {
		offered[impl.Capability] = true
	}
	var dormant []string
	for _, cap := range cfg.Capabilities {
		if !offered[cap.ID] {
			dormant = append(dormant, cap.ID)
		}
	}
	slices.Sort(dormant)
	if !slices.Equal(dormant, []string{"symbol.unresolved"}) {
		t.Fatalf("dormant = %v", dormant)
	}
	if _, err := config.Load(write(t, minimal)); err != nil {
		t.Fatalf("migrated config rejected: %v", err)
	}
}
