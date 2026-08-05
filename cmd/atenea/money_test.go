package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// receipt is one closed commission, priced or free, with nothing else in it.
func receipt(usd float64) *orchestrator.Result {
	step := orchestrator.StepResult{
		Step:    contract.Step{ID: "search-current", Capability: "code.search", Repository: "current", Permission: contract.Permission{BudgetUSD: usd}},
		Phase:   orchestrator.PhaseWork,
		Spent:   contract.Sample{Duration: 2 * time.Second, Tokens: 160},
		Outcome: contract.Outcome{Verdict: contract.VerdictOK, SpentUSD: usd},
		Review:  orchestrator.Review{Child: contract.VerdictOK, Parent: contract.VerdictOK},
	}
	return &orchestrator.Result{
		RunID:    "run-1",
		Task:     "find TODO",
		Verdict:  contract.VerdictOK,
		Steps:    []orchestrator.StepResult{step},
		Spent:    step.Spent,
		SpentUSD: usd,
	}
}

// A commission that cost money says so on the receipt. The figure is on each
// step and in the run record either way; a screen that never mentions it means
// the only way to learn what a run cost is to go and read a file.
func TestTheReceiptShowsWhatWasCharged(t *testing.T) {
	var out bytes.Buffer
	printResult(&out, receipt(0.0234), false)

	if !strings.Contains(out.String(), "charged   $0.0234") {
		t.Errorf("the receipt does not say what it cost:\n%s", out.String())
	}
}

// A free commission says nothing about money. A "$0.0000" on every run of a
// free tool teaches the eye to skip the line, and the line only exists for the
// runs where it is not zero.
func TestAFreeRunMentionsNoMoney(t *testing.T) {
	var out bytes.Buffer
	printResult(&out, receipt(0), false)

	if strings.Contains(out.String(), "charged") {
		t.Errorf("a free run printed a price:\n%s", out.String())
	}
	if strings.Contains(out.String(), "$") {
		t.Errorf("a free run printed a currency figure:\n%s", out.String())
	}
}

// The trace names the step that paid. A total tells you a run cost money; the
// breakdown is what tells you which provider to go and look at.
func TestTheTraceNamesTheStepThatPaid(t *testing.T) {
	var out bytes.Buffer
	printResult(&out, receipt(0.0234), true)

	body := out.String()
	if !strings.Contains(body, "charged  $0.0234") {
		t.Errorf("the trace does not price the step:\n%s", body)
	}
	// Once as the run total, once against the step that incurred it.
	if got := strings.Count(body, "0.0234"); got != 2 {
		t.Errorf("the charge appears %d time(s), want 2 (total and step):\n%s", got, body)
	}
}

// A step that ran past its share says so beside what it cost. The charge
// alone does not distinguish a call that stayed inside its grant from one
// whose far side let it run over -- only this line does.
func TestTheTraceNamesTheOverspendToo(t *testing.T) {
	over := receipt(0.30)
	over.Steps[0].Step.Permission.BudgetUSD = 0.25

	var out bytes.Buffer
	printResult(&out, over, true)

	body := out.String()
	if !strings.Contains(body, "overspent $0.0500") {
		t.Errorf("the trace does not name the overspend:\n%s", body)
	}
}

// A step that stayed inside its share says nothing extra. The line only
// exists for the run where it is not zero, same reasoning as "charged".
func TestAStepWithinItsShareMentionsNoOverspend(t *testing.T) {
	var out bytes.Buffer
	printResult(&out, receipt(0.0234), true)

	if strings.Contains(out.String(), "overspent") {
		t.Errorf("a step within its share printed an overspend:\n%s", out.String())
	}
}
