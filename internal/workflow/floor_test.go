package workflow_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// What starting a turn costs is measured, never written down, so these tests
// hand the engine a table of measurements rather than a constant -- the shape
// `atenea floor measure` leaves behind. The figures are the ones from the run
// that bought this check: $0.35 a turn on taxiprime-backend with
// claude-opus-5, 25,340 tokens of cache write, taken 2026-08-14 19:40 on
// claude code 2.1.227.

// floorTable answers from a fixed table and remembers what it was asked, so a
// test can prove the check was OFF rather than merely quiet.
type floorTable struct {
	rows  map[string]workflow.Floor
	err   error
	asked []string
}

func floorsOf(repository, agent, model string, measured workflow.Floor) *floorTable {
	table := &floorTable{rows: make(map[string]workflow.Floor, 2)}
	return table.add(repository, agent, model, measured)
}

func (f *floorTable) add(repository, agent, model string, measured workflow.Floor) *floorTable {
	f.rows[repository+"\x00"+agent+"\x00"+model] = measured
	return f
}

func (f *floorTable) Floor(_ context.Context, repository, agent, model string) (workflow.Floor, bool, error) {
	f.asked = append(f.asked, repository+" "+agent+" "+model)
	if f.err != nil {
		return workflow.Floor{}, false, f.err
	}
	measured, ok := f.rows[repository+"\x00"+agent+"\x00"+model]
	return measured, ok, nil
}

// measuredFloor is one row of that table.
func measuredFloor(usd float64) workflow.Floor {
	return workflow.Floor{
		USD: usd,
		// Local, because the refusal prints local wall-clock: a fixed UTC
		// instant here would make the message read differently on a machine
		// in another timezone and the test pass only in this one.
		MeasuredAt:       time.Date(2026, 8, 14, 19, 40, 0, 0, time.Local),
		CLIVersion:       "2.1.227",
		CacheWriteTokens: 25340,
	}
}

// floorTypes are two agent types that spend a model turn and one that does
// not, over stubs that answer ok.
func floorTypes(t *testing.T, dir string) []config.AgentType {
	t.Helper()
	return []config.AgentType{
		declared("explore", answers(t, dir, "explore"), config.PoolAgent),
		declared("plan", answers(t, dir, "plan"), config.PoolAgent),
		declared("filereader", answers(t, dir, "filereader"), config.PoolAgent),
	}
}

// pricedTypes is the mapping Serve builds out of settings: the two types that
// spend a model turn, and "" for everything else.
func pricedTypes(agentType string) string {
	switch agentType {
	case "explore":
		return "claude-opus-5"
	case "plan":
		return "claude-sonnet-5"
	}
	return ""
}

// floored builds an engine that knows what a turn costs.
func floored(t *testing.T, dir, repository string, table workflow.Floors) *harness {
	t.Helper()
	return newHarnessWith(t, workflow.Options{
		Lanes:      noCeiling(),
		Repository: repository,
		Floors:     table,
		ModelFor:   pricedTypes,
	}, dir, floorTypes(t, dir)...)
}

// funded is a step carrying a share of the grant.
func funded(id, typeName string, usd float64) workflow.Step {
	s := step(id, typeName, nil)
	s.Permission.BudgetUSD = usd
	return s
}

// commissioned is a graph whose grant covers what its steps were handed. A
// plan whose shares add up past its grant is refused by the launch gate before
// any of this is reached, and a test that tripped over that would be measuring
// the wrong rule.
func commissioned(steps ...workflow.Step) workflow.Graph {
	graph := graphOf(steps...)
	for _, s := range steps {
		graph.GrantUSD += s.Permission.BudgetUSD
	}
	return graph
}

// lineFor is the refusal's line about one step.
func lineFor(message, id string) string {
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), id+" ") {
			return line
		}
	}
	return ""
}

// A plan whose shares clear the floor is created exactly as before. The check
// is a refusal, not a second opinion on a plan that can pay.
//
// The second step is funded at the floor to the cent: equal is funded, and a
// share refused for the last bit of binary floating point would be a refusal
// nobody could act on -- the author already typed the number the message asks
// them for.
func TestAPlanThatClearsTheFloorIsCreated(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", measuredFloor(0.35))
	h := floored(t, dir, "taxiprime-backend", table)

	run, gate, err := h.engine.Create(t.Context(), commissioned(
		funded("over-the-floor", "explore", 0.40),
		funded("at-the-floor", "explore", 0.35),
	))
	if err != nil {
		t.Fatalf("Create refused a plan that clears the floor: %v", err)
	}
	if gate.Kind != workflow.KindLaunch || !gate.Waiting() {
		t.Fatalf("gate is %s %s, want a waiting launch", gate.Kind, gate.Decision)
	}
	if got := statuses(t, run); got["over-the-floor"] != "pending" || got["at-the-floor"] != "pending" {
		t.Errorf("steps are %v, want both pending", got)
	}
}

// The refusal is the deliverable: the arithmetic for the step that is under,
// and where the floor came from, so the person who would have approved the
// plan can act without going looking for either.
func TestAPlanFundedBelowTheFloorIsRefusedAndSaysTheArithmetic(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", measuredFloor(0.35))
	h := floored(t, dir, "taxiprime-backend", table)

	_, _, err := h.engine.Create(t.Context(), commissioned(
		funded("admin-aux", "explore", 0.18),
		funded("rich-enough", "explore", 0.50),
		funded("reads-a-file", "filereader", 0),
	))
	if err == nil {
		t.Fatal("Create accepted a plan funding a step below the cost of starting a turn")
	}
	if kind := contract.KindOf(err); kind != contract.FailureInvalidInput {
		t.Errorf("the refusal is %s, want invalid input: the plan is wrong, not the machine", kind)
	}
	message := err.Error()
	for _, want := range []string{
		"workflow create refused: 1 of 3 steps is funded below the cost of starting a turn",
		"admin-aux",
		"funded $0.18",
		"starting a turn costs ~$0.35",
		"the floor for taxiprime-backend as explore with claude-opus-5 was measured 2026-08-14 19:40 " +
			"on claude code 2.1.227:",
		"25,340 tokens of cache write",
		"nothing was written and no budget was changed",
		"atenea floor measure --repo taxiprime-backend",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal never says %q:\n%s", want, message)
		}
	}
	if strings.Contains(message, "rich-enough") {
		t.Errorf("the refusal names a step that clears the floor:\n%s", message)
	}
	if strings.Contains(message, "reads-a-file") {
		t.Errorf("the refusal names a step whose model nobody measured:\n%s", message)
	}
}

// A refusal writes nothing at all. Not a run in a refused state, not a gate
// nobody may answer: there is no record, because the plan was refused before
// there was one and a half-written no is worse than either answer.
func TestARefusedPlanWritesNothing(t *testing.T) {
	dir := t.TempDir()
	// A fixed id, so this asks about the row Create would have written rather
	// than about a row nobody can name.
	h := newHarnessWith(t, workflow.Options{
		Lanes:      noCeiling(),
		Repository: "taxiprime-backend",
		Floors:     floorsOf("taxiprime-backend", "explore", "claude-opus-5", measuredFloor(0.35)),
		ModelFor:   pricedTypes,
		IDs:        func() string { return "wf-refused" },
	}, dir, floorTypes(t, dir)...)

	if _, _, err := h.engine.Create(t.Context(),
		commissioned(funded("admin-aux", "explore", 0.18))); err == nil {
		t.Fatal("Create accepted an underfunded plan")
	}
	if _, err := h.state.Load(t.Context(), "wf-refused"); err == nil {
		t.Error("a refused plan left a run behind")
	} else if kind := contract.KindOf(err); kind != contract.FailureNotFound {
		t.Errorf("reading the run that was never written answers %s: %v", kind, err)
	}
	gates, err := h.state.Gates(t.Context(), "wf-refused")
	if err != nil {
		t.Fatalf("Gates: %v", err)
	}
	if len(gates) != 0 {
		t.Errorf("a refused plan left %d gates behind: nobody may be asked to approve "+
			"a plan that was refused", len(gates))
	}
	runs, err := h.state.List(t.Context(), 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("the store holds %d runs after a refusal, want none", len(runs))
	}
}

// Every underfunded step is named, and the measurement is explained once.
//
// No truncation: a person reading this is deciding whether to raise five
// shares or throw the plan away, and a list that stopped early would hide the
// size of that decision. One provenance paragraph for the same reason -- five
// copies of one measurement would bury the list they are there to support.
func TestEveryUnderfundedStepIsNamedAndTheFloorExplainedOnce(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", measuredFloor(0.35))
	h := floored(t, dir, "taxiprime-backend", table)

	under := []string{"admin-aux", "users-dev-routes", "billing-webhooks", "a",
		"seventeen-steps-of-eighteen"}
	steps := []workflow.Step{funded("rich-enough", "explore", 0.60)}
	for i, id := range under {
		steps = append(steps, funded(id, "explore", 0.12+0.01*float64(i)))
	}

	_, _, err := h.engine.Create(t.Context(), commissioned(steps...))
	if err == nil {
		t.Fatal("Create accepted a plan with five underfunded steps")
	}
	message := err.Error()
	if !strings.Contains(message, "5 of 6 steps are funded below the cost of starting a turn") {
		t.Errorf("the count is not in the first line:\n%s", message)
	}
	for _, id := range under {
		if lineFor(message, id) == "" {
			t.Errorf("step %s is under the floor and has no line of its own:\n%s", id, message)
		}
	}
	if n := strings.Count(message, "was measured 2026-08-14 19:40"); n != 1 {
		t.Errorf("the provenance appears %d times, want once:\n%s", n, message)
	}

	// Aligned: the two columns are meant to be compared down the page.
	funds, floors := -1, -1
	for _, line := range strings.Split(message, "\n") {
		at := strings.Index(line, "funded $")
		if at < 0 {
			continue
		}
		costs := strings.Index(line, "starting a turn costs")
		if funds == -1 {
			funds, floors = at, costs
			continue
		}
		if at != funds || costs != floors {
			t.Errorf("the columns move between lines, so the figures cannot be read down "+
				"the page:\n%s", message)
			break
		}
	}
}

// A measurement carrying less provenance says less, rather than making the
// rest up: no version clause when the probe could not read one, and a sentence
// that ends where the evidence does instead of introducing a token count it
// does not have. An engine with no repository id says machine-wide in words,
// and the re-measure line drops a flag it cannot fill.
func TestAThinnerMeasurementSaysLessRatherThanMore(t *testing.T) {
	dir := t.TempDir()
	bare := workflow.Floor{
		USD:        0.35,
		MeasuredAt: time.Date(2026, 8, 14, 19, 40, 0, 0, time.Local),
	}
	h := floored(t, dir, "", floorsOf("", "explore", "claude-opus-5", bare))

	_, _, err := h.engine.Create(t.Context(),
		commissioned(funded("admin-aux", "explore", 0.18)))
	if err == nil {
		t.Fatal("Create accepted an underfunded plan")
	}
	message := err.Error()
	for _, want := range []string{
		"the floor for this machine as explore with claude-opus-5 was measured 2026-08-14 19:40.\n",
		"`atenea floor measure`.",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal never says %q:\n%s", want, message)
		}
	}
	for _, unwanted := range []string{"claude code", "tokens of cache write", "--repo"} {
		if strings.Contains(message, unwanted) {
			t.Errorf("the refusal claims %q from a measurement that carried none:\n%s",
				unwanted, message)
		}
	}
}

// A step whose agent type spends no model this machine has priced is never
// refused, however little it carries: no measurement, no claim. Inventing a
// floor for it would be the written-down constant this check exists to avoid.
func TestAStepWhoseModelNobodyMeasuredIsNeverRefused(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", measuredFloor(0.35))
	h := floored(t, dir, "taxiprime-backend", table)

	if _, _, err := h.engine.Create(t.Context(), commissioned(
		funded("reads-a-file", "filereader", 0),
		funded("reads-another", "filereader", 0.01),
	)); err != nil {
		t.Fatalf("Create refused steps nobody measured a turn for: %v", err)
	}
	if len(table.asked) != 0 {
		t.Errorf("the floor store was asked %v about types that spend no model turn", table.asked)
	}
}

// Nil either half is the check off: what every caller that has measured
// nothing has, and what every test written before this check relies on.
func TestWithoutAMeasurementCreateBehavesAsItAlwaysDid(t *testing.T) {
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", measuredFloor(0.35))
	for _, c := range []struct {
		name string
		opts workflow.Options
	}{
		{"nothing measured", workflow.Options{
			Lanes:      noCeiling(),
			Repository: "taxiprime-backend",
			ModelFor:   pricedTypes,
		}},
		{"no model resolves", workflow.Options{
			Lanes:      noCeiling(),
			Repository: "taxiprime-backend",
			Floors:     table,
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			h := newHarnessWith(t, c.opts, dir, floorTypes(t, dir)...)
			run, gate, err := h.engine.Create(t.Context(),
				commissioned(funded("admin-aux", "explore", 0.18)))
			if err != nil {
				t.Fatalf("Create refused with the check off: %v", err)
			}
			if !gate.Waiting() {
				t.Errorf("gate is %s, want a waiting launch", gate.Decision)
			}
			if got := statuses(t, run)["admin-aux"]; got != "pending" {
				t.Errorf("step admin-aux is %q, want pending", got)
			}
			if len(table.asked) != 0 {
				t.Errorf("the floor store was asked %v with the check off", table.asked)
			}
		})
	}
}

// Each step is held against the floor for the model IT would spend, and each
// measurement is explained on its own terms. One floor standing in for another
// is how a plan gets refused over a price nobody was going to pay.
func TestEachStepIsHeldAgainstTheFloorForItsOwnModel(t *testing.T) {
	dir := t.TempDir()
	cheaper := measuredFloor(0.09)
	cheaper.CLIVersion = "2.1.231"
	cheaper.CacheWriteTokens = 9120
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", measuredFloor(0.35)).
		add("taxiprime-backend", "plan", "claude-sonnet-5", cheaper)
	h := floored(t, dir, "taxiprime-backend", table)

	_, _, err := h.engine.Create(t.Context(), commissioned(
		funded("explores", "explore", 0.20),    // under the opus floor of $0.35
		funded("makes-a-graph", "plan", 0.05),  // under the sonnet floor of $0.09
		funded("clears-both", "explore", 0.60), // clear of both
	))
	if err == nil {
		t.Fatal("Create accepted two steps funded below their own models' floors")
	}
	message := err.Error()
	if got := lineFor(message, "explores"); !strings.Contains(got, "~$0.35") {
		t.Errorf("the opus step reads %q, want the opus floor of $0.35:\n%s", got, message)
	}
	if got := lineFor(message, "makes-a-graph"); !strings.Contains(got, "~$0.09") {
		t.Errorf("the sonnet step reads %q, want the sonnet floor of $0.09:\n%s", got, message)
	}
	for _, want := range []string{
		"as explore with claude-opus-5 was measured 2026-08-14 19:40 on claude code 2.1.227",
		"as plan with claude-sonnet-5 was measured 2026-08-14 19:40 on claude code 2.1.231",
		"25,340 tokens of cache write",
		"9,120 tokens of cache write",
	} {
		if n := strings.Count(message, want); n != 1 {
			t.Errorf("%q appears %d times, want once:\n%s", want, n, message)
		}
	}
	if lineFor(message, "clears-both") != "" {
		t.Errorf("a step that clears both floors is named:\n%s", message)
	}
}

// The engine never tops a step up to the floor. A share its author wrote is
// the share the record holds, to the bit -- so this fails the moment anybody
// makes a plan fundable by quietly raising one.
func TestTheStoredSharesAreExactlyTheOnesTheGraphDeclared(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", measuredFloor(0.35))
	h := floored(t, dir, "taxiprime-backend", table)

	want := map[string]float64{
		"at-the-floor":   0.35,
		"over-the-floor": 0.36,
		"unpriced":       0,
	}
	graph := commissioned(
		funded("at-the-floor", "explore", want["at-the-floor"]),
		funded("over-the-floor", "explore", want["over-the-floor"]),
		funded("unpriced", "filereader", want["unpriced"]),
	)
	graph.GrantUSD = 1.00

	run, _, err := h.engine.Create(t.Context(), graph)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, row := range run.Steps {
		if got := row.Step.Permission.BudgetUSD; got != want[row.Step.ID] {
			t.Errorf("the record holds $%v for step %s, want the declared $%v exactly: "+
				"the engine may never top a share up to the floor",
				got, row.Step.ID, want[row.Step.ID])
		}
	}
	for _, declared := range graph.Steps {
		if got := declared.Permission.BudgetUSD; got != want[declared.ID] {
			t.Errorf("Create changed the caller's own graph: step %s now carries $%v, "+
				"declared $%v", declared.ID, got, want[declared.ID])
		}
	}
}

// A measurement that cannot be read is not a measurement that says yes.
// Reading a corrupt cache as "no floor known" would launch exactly the plan
// the check exists to stop, and it would do it silently.
func TestACacheThatCannotBeReadRefusesRatherThanLaunches(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", measuredFloor(0.35))
	table.err = contract.Fail(contract.FailureInvalidInput,
		"floor: /state/floors.json is not the json this writes")
	h := newHarnessWith(t, workflow.Options{
		Lanes:      noCeiling(),
		Repository: "taxiprime-backend",
		Floors:     table,
		ModelFor:   pricedTypes,
		IDs:        func() string { return "wf-unreadable" },
	}, dir, floorTypes(t, dir)...)

	_, _, err := h.engine.Create(t.Context(),
		commissioned(funded("explores", "explore", 5.00)))
	if err == nil {
		t.Fatal("Create ran on a cache it could not read")
	}
	if !strings.Contains(err.Error(), "is not the json this writes") {
		t.Errorf("the refusal hides what went wrong: %v", err)
	}
	runs, listErr := h.state.List(t.Context(), 0)
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(runs) != 0 {
		t.Errorf("the store holds %d runs after a lookup that failed, want none", len(runs))
	}
}

// The floor is keyed by (repository, agent, model), not (repository, model):
// the tool surface an agent type carries is most of the cost, not the model
// it spends against -- see [workflow.Floors]. Two agent types spending the
// SAME model at different floors is exactly the shape a (repository, model)
// key could not tell apart: measuring one would silently answer for the
// other, and here that would either refuse a step that clears its own
// agent's floor or wave through one that doesn't.
func TestAStepIsHeldAgainstItsOwnAgentsFloorEvenWhenTheModelMatches(t *testing.T) {
	dir := t.TempDir()
	table := floorsOf("taxiprime-backend", "explore", "claude-opus-5", measuredFloor(0.35)).
		add("taxiprime-backend", "filereader", "claude-opus-5", measuredFloor(0.05))
	// Both agent types spend the same model here on purpose: the model alone
	// cannot distinguish them, only the agent can.
	sameModel := func(agentType string) string {
		switch agentType {
		case "explore", "filereader":
			return "claude-opus-5"
		}
		return ""
	}
	h := newHarnessWith(t, workflow.Options{
		Lanes:      noCeiling(),
		Repository: "taxiprime-backend",
		Floors:     table,
		ModelFor:   sameModel,
	}, dir, floorTypes(t, dir)...)

	_, _, err := h.engine.Create(t.Context(), commissioned(
		funded("explores", "explore", 0.20),        // under explore's own floor of $0.35
		funded("reads-a-file", "filereader", 0.20), // clear of filereader's own floor of $0.05
	))
	if err == nil {
		t.Fatal("Create accepted a step funded below its own agent's floor")
	}
	message := err.Error()
	if !strings.Contains(message,
		"workflow create refused: 1 of 2 steps is funded below the cost of starting a turn") {
		t.Errorf("the count is wrong:\n%s", message)
	}
	if lineFor(message, "explores") == "" {
		t.Errorf("the step under its own agent's floor has no line:\n%s", message)
	}
	if strings.Contains(message, "reads-a-file") {
		t.Errorf("a step that clears its own agent's floor, sharing a model with an "+
			"underfunded step of another agent, is named:\n%s", message)
	}
	if !strings.Contains(message, "as explore with claude-opus-5") {
		t.Errorf("the provenance does not name the agent whose floor was measured:\n%s", message)
	}
}
