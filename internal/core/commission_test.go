package core

import (
	"context"
	"strings"
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

func stepIn(path string) contract.RunRequest {
	req := writingStep(contract.EffectRead, contract.EffectWrite)
	req.Repository = contract.Repository{ID: "libraries", Path: path}
	return req
}

// A repository whose path is not there is a settings mistake, and it used to
// reach the adapter and come back as "omp could not be started: fork/exec
// /home/me/.local/bin/omp: no such file or directory" -- because Go reports a
// missing cmd.Dir by naming the binary, which is the one thing that was fine.
// It sent the operator to reinstall a tool that already existed.
func TestARepositoryThatIsNotThereNamesTheDirectory(t *testing.T) {
	adapter := &outsider{}
	seam := attach([]contract.Runner{adapter})

	_, err := seam.Run(context.Background(), stepIn("/tmp/atenea-no-such-repository"))
	if got := contract.KindOf(err); got != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", got)
	}
	if msg := err.Error(); !strings.Contains(msg, "/tmp/atenea-no-such-repository") ||
		!strings.Contains(msg, "libraries") {
		t.Errorf("the refusal names neither the path nor the repository: %q", msg)
	}
	if adapter.ran != 0 {
		t.Errorf("the adapter ran %d time(s) for a repository that is not there", adapter.ran)
	}
}

// The bin matters as much as the message. unavailable is the bin that condemns
// a provider: three of them in a row take it out of the funnel for every
// repository on the machine, so one wrong path in the settings file would
// disable a working tool everywhere. invalid_input is filtered out of the
// health record entirely, which is the honest reading -- nothing is wrong with
// the provider.
func TestABadPathDoesNotCondemnTheProvider(t *testing.T) {
	seam := attach([]contract.Runner{&outsider{}})
	_, err := seam.Run(context.Background(), stepIn("/tmp/atenea-no-such-repository"))
	if got := contract.KindOf(err); got == contract.FailureUnavailable {
		t.Error("a missing repository path is reported as the provider being down")
	}
}

// The gate is a gate: a repository that is there passes, and a request with no
// repository at all is not this check's business.
func TestARepositoryThatIsThereStillPasses(t *testing.T) {
	adapter := &outsider{}
	seam := attach([]contract.Runner{adapter})
	if _, err := seam.Run(context.Background(), stepIn(t.TempDir())); err != nil {
		t.Fatalf("a repository that exists was refused: %v", err)
	}
	if _, err := seam.Run(context.Background(),
		writingStep(contract.EffectRead, contract.EffectWrite)); err != nil {
		t.Fatalf("a request carrying no repository was refused: %v", err)
	}
	if adapter.ran != 2 {
		t.Errorf("the adapter ran %d time(s), want 2", adapter.ran)
	}
}
