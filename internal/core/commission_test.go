package core

import (
	"context"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// outsider is an adapter that enforces nothing at all: it answers whatever it
// is handed. That is not a broken adapter, it is the ordinary one -- adapters
// are dumb translators by design, and pkg/contract anticipates them being
// supplied from outside this repository, where nobody can make their author
// copy a permission check.
type outsider struct{ ran int }

func (o *outsider) ID() string                { return "outsider" }
func (o *outsider) Serves(string) bool        { return true }
func (o *outsider) Implementations() []string { return []string{"outsider.search"} }
func (o *outsider) Capabilities() []string    { return []string{"code.search"} }
func (o *outsider) Run(context.Context, contract.RunRequest) (contract.Outcome, error) {
	o.ran++
	return contract.Outcome{}, nil
}

func writingStep(allowed ...contract.Effect) contract.RunRequest {
	return contract.RunRequest{
		Capability: contract.Capability{
			ID:      "code.search",
			Effects: []contract.Effect{contract.EffectRead, contract.EffectWrite},
		},
		Implementation: contract.Implementation{ID: "outsider.search"},
		Permission:     contract.Permission{Task: "find login", Effects: allowed},
	}
}

// The gate is on the seam, not in the adapter. Until Phase 8 the same three
// lines lived in five adapters and nowhere on the core's dispatch path, so an
// adapter written elsewhere enforced nothing whatsoever.
func TestAnAdapterThatEnforcesNothingIsStillRefused(t *testing.T) {
	adapter := &outsider{}
	seam := attach([]contract.Runner{adapter})

	_, err := seam.Run(context.Background(), writingStep(contract.EffectRead))
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", got)
	}
	// Refused, not merely reported: the adapter must never have been reached.
	if adapter.ran != 0 {
		t.Errorf("the adapter ran %d time(s) behind a refusal", adapter.ran)
	}

	// And the gate is a gate, not a wall.
	if _, err := seam.Run(context.Background(),
		writingStep(contract.EffectRead, contract.EffectWrite)); err != nil {
		t.Fatalf("a covered step was refused: %v", err)
	}
	if adapter.ran != 1 {
		t.Errorf("the adapter ran %d time(s) for one covered step", adapter.ran)
	}
}

// Routing must not be the thing that decides whether the gate applies: one
// attached client takes a different branch of attach than several.
func TestTheGateHoldsWithSeveralClientsAttached(t *testing.T) {
	first, second := &outsider{}, &outsider{}
	seam := attach([]contract.Runner{first, second})

	_, err := seam.Run(context.Background(), writingStep(contract.EffectRead))
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", got)
	}
	if first.ran+second.ran != 0 {
		t.Error("a refused step reached an adapter through the fan-out")
	}
}

// The status screen names who is actually behind the catalog. A gate that
// answered for itself would put "commissioned" on that line.
func TestTheGateDoesNotRenameTheClientBehindIt(t *testing.T) {
	seam := attach([]contract.Runner{&outsider{}})
	if seam.ID() != "outsider" {
		t.Errorf("ID = %q, want the adapter's own name", seam.ID())
	}
	if !seam.Serves("anything") {
		t.Error("the gate stopped delegating Serves")
	}
}
