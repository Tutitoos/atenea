package orchestrator_test

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// charging answers like the default double but hands back a bill.
func charging(usd float64) func(contract.RunRequest) (contract.Outcome, error) {
	return func(contract.RunRequest) (contract.Outcome, error) {
		out := hits("cmd/main.go")
		out.SpentUSD = usd
		return out, nil
	}
}

// What a commission cost reaches the receipt, added up over every step that
// paid. Without this the number exists on each step and nowhere a human looks.
func TestTheChargeIsTotalledOntoTheReceipt(t *testing.T) {
	const perStep = 0.11
	agent, _ := build(t, &fakeRunner{answer: charging(perStep)}, 0, t.TempDir())

	result, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Steps) == 0 {
		t.Fatal("no steps ran, so the total proves nothing")
	}

	want := perStep * float64(len(result.Steps))
	if diff := result.SpentUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("receipt says $%v over %d step(s), want $%v",
			result.SpentUSD, len(result.Steps), want)
	}
}

// Money must never reach the measurement base. The base is what the funnel
// ranks on, and a dollar folded into the token count would re-rank every
// provider the day a price list changed -- with nothing in the numbers to say
// why. Two identical commissions, one billed and one free, have to leave
// measurements that agree on every axis the selector can see.
func TestTheChargeNeverEntersTheBaseline(t *testing.T) {
	free, freeRows := metered(t, &fakeRunner{})
	if _, err := free.Run(context.Background(), orchestrator.Task{Text: "find TODO"}); err != nil {
		t.Fatalf("free run: %v", err)
	}
	billed, billedRows := metered(t, &fakeRunner{answer: charging(4.99)})
	if _, err := billed.Run(context.Background(), orchestrator.Task{Text: "find TODO"}); err != nil {
		t.Fatalf("billed run: %v", err)
	}

	cheap, _ := freeRows.taken()
	dear, _ := billedRows.taken()
	if len(cheap) == 0 || len(cheap) != len(dear) {
		t.Fatalf("%d free rows against %d billed rows", len(cheap), len(dear))
	}
	for i := range cheap {
		// Duration is wall clock and will not repeat; tokens and memory are
		// the axes a charge could plausibly be folded into.
		if cheap[i].Spent.Tokens != dear[i].Spent.Tokens {
			t.Errorf("step %s: %d tokens free, %d tokens billed -- money reached the baseline",
				dear[i].StepID, cheap[i].Spent.Tokens, dear[i].Spent.Tokens)
		}
		if cheap[i].Spent.PeakRSS != dear[i].Spent.PeakRSS {
			t.Errorf("step %s: %d bytes free, %d bytes billed -- money reached the baseline",
				dear[i].StepID, cheap[i].Spent.PeakRSS, dear[i].Spent.PeakRSS)
		}
	}
}

// The paper copy carries the price. It is the only durable record that does --
// the measurement base deliberately refuses money -- so a run whose receipt on
// disk has no price cannot be audited after the fact.
func TestThePriceIsFiledOnThePaperCopy(t *testing.T) {
	dir := t.TempDir()
	agent, _ := build(t, &fakeRunner{answer: charging(0.07)}, 0, dir)

	result, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, result.RunID+".json"))
	if err != nil {
		t.Fatalf("reading the receipt: %v", err)
	}
	var run checkpoint.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatalf("receipt is not readable: %v", err)
	}
	if len(run.Steps) == 0 {
		t.Fatal("the receipt has no steps on it")
	}
	for _, step := range run.Steps {
		if step.SpentUSD != 0.07 {
			t.Errorf("step %s was filed at $%v, want $0.07", step.ID, step.SpentUSD)
		}
	}
}

// The same paper copy that carries what was spent has to carry how far past
// its granted share that spend went, or an audit has to re-derive the one
// number that says a far side's own ceiling let a turn finish after the
// budget for it was already gone.
func TestTheOverspendIsFiledOnThePaperCopyToo(t *testing.T) {
	dir := t.TempDir()
	agent, _ := build(t, &fakeRunner{answer: charging(0.30)}, 0, dir)

	// Ask is one capability answered as one step, so the whole $0.25 grant
	// is this one step's share -- the plan a Task would build spans more
	// than one step and would divide the grant before the overspend could
	// be pinned to a single, known number.
	result, err := agent.Ask(context.Background(), orchestrator.Question{
		Capability: "code.search", Repository: "api",
		Payload: map[string]any{"query": "TODO"}, BudgetUSD: 0.25,
	})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, result.RunID+".json"))
	if err != nil {
		t.Fatalf("reading the receipt: %v", err)
	}
	var run checkpoint.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatalf("receipt is not readable: %v", err)
	}
	if len(run.Steps) != 1 {
		t.Fatalf("%d step(s) on the receipt, want exactly 1", len(run.Steps))
	}
	step := run.Steps[0]
	if step.SpentUSD != 0.30 {
		t.Errorf("step %s was filed at $%v, want $0.30", step.ID, step.SpentUSD)
	}
	if diff := step.OverspendUSD - 0.05; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("step %s overspend = $%v, want $0.05", step.ID, step.OverspendUSD)
	}
}

// Free work carries no price tag. A receipt where every step reads "$0" is a
// receipt nobody reads, and the omitted key is what keeps the one run that did
// cost money legible among the ones that did not.
func TestFreeWorkIsFiledWithoutAPrice(t *testing.T) {
	dir := t.TempDir()
	agent, _ := build(t, &fakeRunner{}, 0, dir)

	result, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, result.RunID+".json"))
	if err != nil {
		t.Fatalf("reading the receipt: %v", err)
	}
	if body := string(raw); strings.Contains(body, "spent_usd") {
		t.Errorf("a free run wrote a price onto its receipt: %s", body)
	}
}

// A provider that refuses is not a provider that is down. Running out of grant
// -- money, or any other ceiling the user set -- says nothing about whether the
// tool works, so the catalog must not learn a health verdict from it. If it
// did, one exhausted budget would take the provider out of the funnel for
// every later step, including the ones that were never going to cost anything.
func TestARefusalDoesNotMarkTheProviderDown(t *testing.T) {
	runner := &fakeRunner{
		answer: func(contract.RunRequest) (contract.Outcome, error) {
			return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied,
				"claude code stopped at its spending ceiling")
		},
	}
	agent, reg := build(t, runner, 0, t.TempDir())

	result, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Verdict != contract.VerdictFailed {
		t.Fatalf("verdict = %v, want failed", result.Verdict)
	}

	var checked int
	for _, step := range result.Steps {
		if step.Decision.Chosen.ID == "" {
			continue
		}
		impl, err := reg.Implementation(step.Decision.Chosen.ID)
		if err != nil {
			t.Fatalf("Implementation: %v", err)
		}
		checked++
		if impl.Health.State == contract.HealthDown {
			t.Errorf("%s was marked down by a refusal: %s",
				impl.ID, impl.Health.Reason)
		}
	}
	if checked == 0 {
		t.Fatal("no implementation was reached, so nothing was proven")
	}
}

// budgeted builds an agent whose standing grant is usd, which is the number an
// operator writes once in the settings file.
func budgeted(t *testing.T, runner contract.Runner, usd float64) *orchestrator.Agent {
	t.Helper()
	reg := catalog(t)
	if fake, ok := runner.(*fakeRunner); ok && fake.serves == nil {
		for _, capability := range reg.Capabilities() {
			impls, err := reg.ImplementationsFor(capability.ID)
			if err != nil {
				t.Fatalf("ImplementationsFor: %v", err)
			}
			for _, impl := range impls {
				fake.serves = append(fake.serves, impl.ID)
			}
		}
	}
	chooser, err := selector.New(selector.Config{})
	if err != nil {
		t.Fatalf("selector.New: %v", err)
	}
	store, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatalf("checkpoint.New: %v", err)
	}
	agent, err := orchestrator.New(orchestrator.Config{
		Catalog:     reg,
		Chooser:     chooser,
		Runner:      runner,
		Checkpoints: store,
		BudgetUSD:   usd,
	})
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	return agent
}

// spendsItsCeiling is the honest double of a paid far side: it bills exactly
// what it was handed, and it refuses when it was handed nothing. A far side
// that ignored its grant would let this suite pass over a broken core.
func spendsItsCeiling() func(contract.RunRequest) (contract.Outcome, error) {
	return func(req contract.RunRequest) (contract.Outcome, error) {
		if !req.Permission.Funded() {
			return contract.Outcome{}, contract.Fail(contract.FailurePermissionDenied,
				"the commission has nothing left to spend")
		}
		out := hits("cmd/main.go")
		out.SpentUSD = req.Permission.BudgetUSD
		return out, nil
	}
}

// The defect this brick exists to fix. A ceiling that lived on the adapter was
// re-applied to every invocation, so a commission dispatching four steps could
// spend it four times over. The grant belongs to the commission: however many
// steps it takes, the total is what the user agreed to.
func TestACommissionCannotSpendItsGrantMoreThanOnce(t *testing.T) {
	const grant = 0.25
	runner := &fakeRunner{answer: spendsItsCeiling()}
	agent := budgeted(t, runner, grant)

	result, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Steps) < 2 {
		t.Fatalf("%d step(s) ran, so nothing was shared", len(result.Steps))
	}
	if result.SpentUSD > grant+1e-9 {
		t.Errorf("a $%v commission spent $%v over %d steps",
			grant, result.SpentUSD, len(result.Steps))
	}
}

// The share is what the step is actually handed, not a note in the plan. Every
// dispatched request carries a piece of the remainder, and no piece is the
// whole grant: that is the difference between splitting and copying.
func TestEveryDispatchedStepCarriesAShareOfTheGrant(t *testing.T) {
	const grant = 0.25
	runner := &fakeRunner{answer: spendsItsCeiling()}
	agent := budgeted(t, runner, grant)

	if _, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	seen := runner.requests()
	if len(seen) < 2 {
		t.Fatalf("%d request(s) reached the far side", len(seen))
	}
	var total float64
	for _, req := range seen {
		if req.Permission.BudgetUSD > grant+1e-9 {
			t.Errorf("a step was handed $%v out of a $%v grant",
				req.Permission.BudgetUSD, grant)
		}
		total += req.Permission.BudgetUSD
	}
	if total <= grant+1e-9 && seen[0].Permission.BudgetUSD >= grant-1e-9 {
		t.Errorf("the first step was handed the whole grant: $%v",
			seen[0].Permission.BudgetUSD)
	}
}

// What one wave leaves behind is what the next one divides. A commission whose
// first wave came in cheap must not carry that saving to the floor: the money
// was granted to the commission, and it is still the commission's to spend.
func TestTheNextWaveDividesWhatTheLastOneLeft(t *testing.T) {
	const grant = 1.0
	runner := &fakeRunner{answer: func(contract.RunRequest) (contract.Outcome, error) {
		out := hits("cmd/main.go")
		out.SpentUSD = 0.01
		return out, nil
	}}
	agent := budgeted(t, runner, grant)

	result, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.SpentUSD <= 0 {
		t.Fatal("nothing was spent, so there is no remainder to divide")
	}

	// A wave is a barrier, so arrival order is wave order: everything the
	// first wave was handed arrives before anything the second one gets.
	seen := runner.requests()
	if len(seen) < 3 {
		t.Fatalf("%d request(s): too few to span two waves", len(seen))
	}
	first := seen[0].Permission.BudgetUSD
	last := seen[len(seen)-1].Permission.BudgetUSD
	if first <= 0 || last <= 0 {
		t.Fatalf("both waves must have been funded: first $%v, last $%v", first, last)
	}
	if last >= first {
		t.Errorf("a later step was handed $%v after $%v was already spent of $%v",
			last, result.SpentUSD, grant)
	}
}

// A commission that arrives with its own figure carries that one. The settings
// file is a standing grant, not a cap on what the user may authorize in the
// moment: one order beats the default, in both directions.
func TestACommissionCarriesItsOwnGrant(t *testing.T) {
	runner := &fakeRunner{answer: spendsItsCeiling()}
	agent := budgeted(t, runner, 0.25)

	if _, err := agent.Run(context.Background(), orchestrator.Task{
		Text: "find TODO", BudgetUSD: 2,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	seen := runner.requests()
	if len(seen) == 0 {
		t.Fatal("nothing was dispatched")
	}
	var total float64
	for _, req := range seen {
		total += req.Permission.BudgetUSD
	}
	if total <= 0.25 {
		t.Errorf("the commission's own $2 was capped at the standing grant: $%v", total)
	}
}

// Money running out stops paid work, not all work. A free provider has nothing
// to charge against and keeps answering, which is the whole reason a spent
// grant is a refusal on one step rather than a dead run.
func TestAnEmptyGrantStillLetsFreeWorkThrough(t *testing.T) {
	agent := budgeted(t, &fakeRunner{}, 0)

	result, err := agent.Run(context.Background(), orchestrator.Task{Text: "find TODO"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Verdict != contract.VerdictOK {
		t.Errorf("verdict = %v: free work was stopped by an empty grant", result.Verdict)
	}
	if result.SpentUSD != 0 {
		t.Errorf("a free run was billed $%v", result.SpentUSD)
	}
}

// A negative grant is a typo, not an instruction to spend nothing. Clamping it
// would switch off every paid provider for the run and the operator would read
// the refusals as an outage rather than as their own mistake.
func TestANonsenseGrantIsRefusedRatherThanClamped(t *testing.T) {
	agent := budgeted(t, &fakeRunner{}, 0.25)

	for name, usd := range map[string]float64{
		"negative": -1,
		"nan":      math.NaN(),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := agent.Run(context.Background(), orchestrator.Task{
				Text: "find TODO", BudgetUSD: usd,
			})
			if got := contract.KindOf(err); got != contract.FailureInvalidInput {
				t.Errorf("a budget of %v was filed as %v", usd, got)
			}
			_, err = agent.Ask(context.Background(), orchestrator.Question{
				Capability: "code.search", Repository: "api",
				Payload: map[string]any{"query": "TODO"}, BudgetUSD: usd,
			})
			if got := contract.KindOf(err); got != contract.FailureInvalidInput {
				t.Errorf("a question budget of %v was filed as %v", usd, got)
			}
		})
	}
}
