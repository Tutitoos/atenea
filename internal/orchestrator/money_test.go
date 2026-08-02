package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/checkpoint"
	"github.com/Tutitoos/atenea/internal/orchestrator"
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
