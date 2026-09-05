package core

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Tutitoos/atenea/internal/adapter/kivgraph"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func kivgraphMaintenanceDirectory() string {
	directory, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(directory, "atenea", "kivgraph-maintenance")
}

func kivgraphMaintenanceDirectoryFor(cfg config.Config) string {
	base := kivgraphMaintenanceDirectory()
	if base == "" {
		return ""
	}
	graph := cfg.Orchestrator.Kivgraph
	// Include every execution input without persisting environment values.
	identityBytes, _ := json.Marshal(struct {
		Source string
		Graph  any
	}{cfg.Source, graph})
	identity := string(identityBytes)
	sum := sha256.Sum256([]byte(identity))
	return filepath.Join(base, fmt.Sprintf("%x", sum[:12]))
}

func prepareGraphMaintenance(runners []contract.Runner, role Role) (*kivgraph.Runner, error) {
	for _, runner := range runners {
		for {
			if graph, ok := runner.(*kivgraph.Runner); ok {
				if role == Service {
					if err := graph.EnableBackground(); err != nil {
						return nil, err
					}
				}
				return graph, nil
			}
			wrapped, ok := runner.(interface{ Unwrap() contract.Runner })
			if !ok {
				break
			}
			runner = wrapped.Unwrap()
		}
	}
	return nil, nil
}
