package workflow_test

import (
	"strings"
	"testing"
)

// What a probe costs and what a step costs are two different numbers, and
// until 2026-08-16 only the first was checked. Twenty-three steps on
// taxiprime-backend cleared both probe rules and eighteen died at their
// ceilings having read real files and written nothing: the five that finished
// cost $0.30-$0.44 where the probe rules asked $0.06. These tests hold the
// third rule -- the median of the rows that FINISHED -- and the care it takes
// not to be fooled by rows that prove nothing.
//
// history seeds n finished, cleanly-priced rows of one agent type, generously
// granted so none of them counts as stopped-at-its-ceiling.
func history(t *testing.T, h *harness, repository, typeName string, n int, spent float64) {
	t.Helper()
	for i := range n {
		priced(t, h.state, "wf-seen-"+typeName+"-"+string(rune('a'+i)),
			repository, typeName, 10.00, usd(spent))
	}
}

// A share that clears both probe rules and does not cover what this type has
// actually cost to finish is refused. The floor and the threshold price
// starting a turn; only this one prices doing the work.
func TestAShareBelowWhatTheTypeHasCostToFinishIsRefused(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", probedRow())
	h := floored(t, dir, "taxiprime-backend", table)
	history(t, h, "taxiprime-backend", "explore", 3, 0.40)

	// $0.10 clears the warm floor (~$0.06) and buys 8,300 tokens of reading
	// against a 4,814-token first event, so neither probe rule objects.
	_, _, err := h.engine.Create(t.Context(), commissioned(funded("reads-a-file", "explore", 0.10)))
	if err == nil {
		t.Fatal("Create accepted a step funded below what this type has cost to finish")
	}
	message := err.Error()
	for _, want := range []string{
		"funded $0.10",
		"needs $0.40",
		"cost $0.40 to finish",
		"median of 3 completed runs on taxiprime-backend",
		"the probes price starting a turn, these priced doing the work",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal never says %q:\n%s", want, message)
		}
	}
}

// A median of two is a rumor -- Observed's own doc says so -- and the middle
// of two rows is just an endpoint. Two rows travel as evidence and refuse
// nobody, because a rule that fires on the second row of a new machine is a
// rule that stops work on no evidence.
func TestTwoCompletedRowsAreARumorAndRefuseNobody(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", probedRow())
	h := floored(t, dir, "taxiprime-backend", table)
	history(t, h, "taxiprime-backend", "explore", 2, 0.40)

	if _, _, err := h.engine.Create(t.Context(),
		commissioned(funded("reads-a-file", "explore", 0.10))); err != nil {
		t.Fatalf("Create refused a plan on the strength of two rows: %v", err)
	}
}

// A run that spent its whole grant is a lower bound, not a measurement. Three
// of them are three lower bounds, and letting them set an admission price
// would be this system quoting its own failures back as the cost of success.
func TestRowsThatStoppedAtTheirCeilingDoNotSetTheAdmissionPrice(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", probedRow())
	h := floored(t, dir, "taxiprime-backend", table)
	for _, id := range []string{"wf-ceil-a", "wf-ceil-b", "wf-ceil-c"} {
		priced(t, h.state, id, "taxiprime-backend", "explore", 0.40, usd(0.40))
	}

	if _, _, err := h.engine.Create(t.Context(),
		commissioned(funded("reads-a-file", "explore", 0.10))); err != nil {
		t.Fatalf("Create priced admission off censored rows: %v", err)
	}
}

// A type no probe has priced used to be waved through entirely: no floor, no
// threshold, no check. That exempted exactly the types this machine knows the
// most about -- the ones with finished rows on the record -- because the one
// thing missing was a probe.
func TestATypeNoProbeHasPricedIsStillHeldToWhatItHasCost(t *testing.T) {
	dir := t.TempDir()
	// The table prices explore and says nothing about plan, which spends
	// against a model all the same.
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", probedRow())
	h := floored(t, dir, "taxiprime-backend", table)
	history(t, h, "taxiprime-backend", "plan", 3, 0.55)

	_, _, err := h.engine.Create(t.Context(), commissioned(funded("writes-a-plan", "plan", 0.20)))
	if err == nil {
		t.Fatal("Create waved through an unprobed type that has three finished rows")
	}
	message := err.Error()
	for _, want := range []string{"funded $0.20", "needs $0.55", "cost $0.55 to finish"} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal never says %q:\n%s", want, message)
		}
	}
}

// Three rules, one pass, and the largest wins: a person raises a share once
// rather than clearing one gate and meeting the next on the following try.
func TestTheBindingRuleIsTheLargestOfTheThree(t *testing.T) {
	dir := t.TempDir()
	// A floor of $0.90 is larger than anything three $0.40 rows can say.
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", measuredFloor(0.90))
	h := floored(t, dir, "taxiprime-backend", table)
	history(t, h, "taxiprime-backend", "explore", 3, 0.40)

	_, _, err := h.engine.Create(t.Context(), commissioned(funded("reads-a-file", "explore", 0.10)))
	if err == nil {
		t.Fatal("Create accepted a step under every requirement")
	}
	message := err.Error()
	if !strings.Contains(message, "needs $0.90") {
		t.Errorf("the floor is the larger requirement and the refusal does not say so:\n%s", message)
	}
	if strings.Contains(message, "priced doing the work") {
		t.Errorf("the observed clause bound where the floor was larger:\n%s", message)
	}
}

// The evidence travels even when it did not bind. A share refused by a probe
// rule while real rows say something larger again is a person's next question,
// not a footnote -- so the figure is carried onto every refused step.
func TestTheEngineStillNeverTopsAShareUpToWhatAStepHasCost(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", probedRow())
	h := floored(t, dir, "taxiprime-backend", table)
	history(t, h, "taxiprime-backend", "explore", 3, 0.40)

	if _, _, err := h.engine.Create(t.Context(),
		commissioned(funded("reads-a-file", "explore", 0.10))); err == nil {
		t.Fatal("Create accepted an underfunded step")
	}
	runs, err := h.state.List(t.Context(), 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, run := range runs {
		for _, s := range run.Steps {
			if got := s.Step.Permission.BudgetUSD; got > 0.10 {
				t.Errorf("a share was raised to %v behind its author's back", got)
			}
		}
	}
}

// A step whose type has never finished anything is checked by the two probe
// rules and nothing else. No rows is no claim, and inventing one would be the
// written-down constant this whole check exists to avoid.
func TestNoFinishedRowsMeansNoClaim(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", probedRow())
	h := floored(t, dir, "taxiprime-backend", table)

	if _, _, err := h.engine.Create(t.Context(),
		commissioned(funded("reads-a-file", "explore", 0.10))); err != nil {
		t.Fatalf("Create invented a cost for a type nothing has finished: %v", err)
	}
}
