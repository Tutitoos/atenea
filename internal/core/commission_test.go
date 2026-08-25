package core

import (
	"context"
	"math"
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

type chargingOutsider struct {
	outsider
	cost float64
}

func (o *chargingOutsider) Run(context.Context, contract.RunRequest) (contract.Outcome, error) {
	o.ran++
	// Known, because an adapter reporting an amount is an adapter whose
	// provider said one. The one that reports a figure without saying so has
	// its own type below, so the tests about over-budget charges keep failing
	// for being over budget rather than for being unmeasured.
	return contract.Outcome{SpentUSD: o.cost, SpentUSDKnown: true, Verdict: contract.VerdictOK}, nil
}

// silentCharger reports an amount and never says it measured one. It stands in
// for the adapter that forgot the flag and for the adapter that invented the
// number, which from the core's side are the same thing.
type silentCharger struct {
	outsider
	cost float64
}

func (o *silentCharger) Run(context.Context, contract.RunRequest) (contract.Outcome, error) {
	o.ran++
	return contract.Outcome{SpentUSD: o.cost, Verdict: contract.VerdictOK}, nil
}

type streamingOutsider struct{ ran int }

func (o *streamingOutsider) ID() string                { return "streaming" }
func (o *streamingOutsider) Serves(string) bool        { return true }
func (o *streamingOutsider) Implementations() []string { return []string{"streaming.search"} }
func (o *streamingOutsider) Capabilities() []string    { return []string{"code.search"} }
func (o *streamingOutsider) Run(ctx context.Context, _ contract.RunRequest) (contract.Outcome, error) {
	o.ran++
	if observer := contract.CostObserverFromContext(ctx); observer != nil {
		if !observer(contract.CostUpdate{SpentUSD: 0.26, Known: true}) {
			return contract.Outcome{}, ctx.Err()
		}
	}
	return contract.Outcome{Verdict: contract.VerdictOK}, nil
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
	seam := attach([]contract.Runner{adapter}, nil)

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

func TestTheCoreRejectsAnOverBudgetAdapterEvenWhenItReportsSuccess(t *testing.T) {
	adapter := &chargingOutsider{cost: 0.26}
	seam := attach([]contract.Runner{adapter}, nil)
	req := writingStep(contract.EffectRead, contract.EffectWrite)
	req.Permission.BudgetUSD = 0.25

	outcome, err := seam.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", got)
	}
	if outcome.SpentUSD != 0.26 {
		t.Fatalf("reported charge = %v, want 0.26", outcome.SpentUSD)
	}
}

func TestTheCoreRejectsInvalidProviderCharges(t *testing.T) {
	for _, cost := range []float64{-1, math.Inf(1)} {
		adapter := &chargingOutsider{cost: cost}
		seam := attach([]contract.Runner{adapter}, nil)
		req := writingStep(contract.EffectRead, contract.EffectWrite)
		req.Permission.BudgetUSD = 1
		if _, err := seam.Run(context.Background(), req); contract.KindOf(err) != contract.FailurePermissionDenied {
			t.Fatalf("cost %v: kind = %v, want permission_denied", cost, contract.KindOf(err))
		}
	}
}

func TestTheCoreCancelsWhenAProviderReportsAnIncrementalOverrun(t *testing.T) {
	adapter := &streamingOutsider{}
	seam := attach([]contract.Runner{adapter}, nil)
	req := writingStep(contract.EffectRead, contract.EffectWrite)
	req.Implementation.ID = "streaming.search"
	req.Permission.BudgetUSD = 0.25

	_, err := seam.Run(context.Background(), req)
	if got := contract.KindOf(err); got != contract.FailurePermissionDenied {
		t.Fatalf("kind = %v, want permission_denied", got)
	}
	if adapter.ran != 1 {
		t.Fatal("streaming provider was not called exactly once")
	}
}

// Routing must not be the thing that decides whether the gate applies: one
// attached client takes a different branch of attach than several.
func TestTheGateHoldsWithSeveralClientsAttached(t *testing.T) {
	first, second := &outsider{}, &outsider{}
	seam := attach([]contract.Runner{first, second}, nil)

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
	seam := attach([]contract.Runner{&outsider{}}, nil)
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
	seam := attach([]contract.Runner{adapter}, nil)

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
	seam := attach([]contract.Runner{&outsider{}}, nil)
	_, err := seam.Run(context.Background(), stepIn("/tmp/atenea-no-such-repository"))
	if got := contract.KindOf(err); got == contract.FailureUnavailable {
		t.Error("a missing repository path is reported as the provider being down")
	}
}

// The gate is a gate: a repository that is there passes, and a request with no
// repository at all is not this check's business.
func TestARepositoryThatIsThereStillPasses(t *testing.T) {
	adapter := &outsider{}
	seam := attach([]contract.Runner{adapter}, nil)
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

// The core owns the money boundary, so a charge with nothing behind it is
// spent but never called a price.
//
// SpentUSDKnown is what an adapter sets when the provider actually reported a
// price. One that returns an amount without it has either forgotten the flag
// or invented the number, and from here those are the same thing: a figure
// with no measurement behind it.
//
// It is not refused. By the time this gate sees the outcome the provider has
// already run and already charged, so refusing throws away work the operator
// paid for and recovers none of the money; and refusing would make an adapter
// compiled against contract 3.2.0 -- which has no field to set -- fail on
// every paid call, which is not what a minor bump is allowed to do. So the
// amount is spent against the purse, on the conservative reading that an
// unexplained charge was real, and it travels on with Known still false so
// that nothing downstream reports it as a measurement.
func TestAnUnmeasuredChargeIsSpentButNotCalledAPrice(t *testing.T) {
	adapter := &silentCharger{cost: 0.42}
	seam := attach([]contract.Runner{adapter}, nil)
	req := writingStep(contract.EffectRead, contract.EffectWrite)
	req.Permission.BudgetUSD = 1.00

	outcome, err := seam.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("work the provider had already charged for was thrown away: %v", err)
	}
	if outcome.SpentUSD != 0.42 {
		t.Errorf("spent = %v, want the $0.42 the adapter reported: the purse has to "+
			"assume an unexplained charge was real", outcome.SpentUSD)
	}
	if outcome.SpentUSDKnown {
		t.Error("a figure with no measurement behind it came back claiming to be one")
	}
}

// The same amount, reported as measured, goes through: the check is about
// provenance, not about the number.
func TestTheSameChargeGoesThroughWhenItSaysItWasMeasured(t *testing.T) {
	adapter := &chargingOutsider{cost: 0.42}
	seam := attach([]contract.Runner{adapter}, nil)
	req := writingStep(contract.EffectRead, contract.EffectWrite)
	req.Permission.BudgetUSD = 1.00

	if _, err := seam.Run(context.Background(), req); err != nil {
		t.Fatalf("a measured charge inside its permission was refused: %v", err)
	}
}

// And a provider that reports a real zero is not caught by the rule: the check
// is on a figure with no measurement, and zero with a measurement is a fact.
func TestAMeasuredZeroIsNotAnUnmeasuredCharge(t *testing.T) {
	adapter := &chargingOutsider{cost: 0}
	seam := attach([]contract.Runner{adapter}, nil)
	req := writingStep(contract.EffectRead, contract.EffectWrite)
	req.Permission.BudgetUSD = 1.00

	outcome, err := seam.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("a provider that charged nothing was refused: %v", err)
	}
	if !outcome.SpentUSDKnown {
		t.Error("the measured zero lost its provenance crossing the boundary")
	}
}
