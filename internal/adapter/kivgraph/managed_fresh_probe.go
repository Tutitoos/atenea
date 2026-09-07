package kivgraph

import (
	"context"
	"fmt"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// ManagedFreshReport is the small, auditable result returned by the
// diagnostic seam. It deliberately exposes no indexer or maintenance lock;
// ProbeManagedFresh always goes through Runner.Run and therefore the same
// managedFresh path used by production requests.
type ManagedFreshReport struct {
	Status     string `json:"status"`
	Generation int    `json:"generation"`
	Rebuilt    bool   `json:"rebuilt"`
}

// ProbeManagedFresh runs the explicit graph.ensure_fresh capability through
// the production Runner. It is intended for bounded fixture/copy pilots and
// diagnostics, where the caller supplies an isolated repository and an
// explicit permission stamp. It never selects a mode other than full: the
// production managedFresh path owns that decision.
func (r *Runner) ProbeManagedFresh(ctx context.Context, repository contract.Repository, permission contract.Permission) (ManagedFreshReport, error) {
	capability := contract.Capability{
		ID:      CapabilityEnsureFresh,
		Version: contract.Version{Major: 1},
		Summary: "Verify graph freshness through the managed full rebuild lane.",
		Effects: []contract.Effect{contract.EffectRead, contract.EffectWrite, contract.EffectProcess},
		Outputs: []contract.Field{
			{Name: "status", Type: contract.TypeString, Required: true},
			{Name: "generation", Type: contract.TypeInt, Required: true},
			{Name: "rebuilt", Type: contract.TypeBool, Required: true},
		},
	}
	outcome, err := r.Run(ctx, contract.RunRequest{
		Capability:     capability,
		Implementation: contract.Implementation{ID: ImplEnsureFresh, Provider: "kivgraph", Capability: CapabilityEnsureFresh},
		Repository:     repository,
		Permission:     permission,
	})
	if err != nil {
		return ManagedFreshReport{}, err
	}
	status, ok := outcome.Result["status"].(string)
	if !ok {
		return ManagedFreshReport{}, fmt.Errorf("kivgraph managed freshness returned malformed status")
	}
	generation, ok := outcome.Result["generation"].(int)
	if !ok {
		return ManagedFreshReport{}, fmt.Errorf("kivgraph managed freshness returned malformed generation")
	}
	rebuilt, ok := outcome.Result["rebuilt"].(bool)
	if !ok {
		return ManagedFreshReport{}, fmt.Errorf("kivgraph managed freshness returned malformed rebuild flag")
	}
	return ManagedFreshReport{Status: status, Generation: generation, Rebuilt: rebuilt}, nil
}
