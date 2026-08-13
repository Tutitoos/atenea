package workflow_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// types for compile tests: no process ever spawns, so the command can be
// anything a declaration accepts.
func compileTypes() []config.AgentType {
	return []config.AgentType{
		declared("reader", "/bin/true", config.PoolAgent),
		declared("writer", "/bin/true", config.PoolAgent,
			contract.EffectRead, contract.EffectWrite),
		declared("critic", "/bin/true", config.PoolReview),
	}
}

func compile(t *testing.T, graph workflow.Graph) (workflow.Plan, error) {
	t.Helper()
	return workflow.Compile(graph, compileTypes())
}

func refuses(t *testing.T, graph workflow.Graph, want string) {
	t.Helper()
	_, err := compile(t, graph)
	if err == nil {
		t.Fatalf("compiled a graph that should have been refused (%s)", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not mention %q", err, want)
	}
}

func TestAGraphCompiles(t *testing.T) {
	plan, err := compile(t, graphOf(
		step("look", "reader", nil),
		step("judge", "critic", []string{"look"}),
	))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if plan.Pool("look") != config.PoolAgent || plan.Pool("judge") != config.PoolReview {
		t.Fatalf("lanes = %v, want the types' own", plan.Pools)
	}
	// The commission is stamped onto every step: a permission is a slice OF
	// something, and a step that cannot name what it came from is not
	// auditable.
	got, _ := plan.Step("look")
	if got.Permission.Task != "a test commission" {
		t.Fatalf("permission task = %q, want the commission", got.Permission.Task)
	}
}

func TestAnUnknownAgentTypeIsRefused(t *testing.T) {
	refuses(t, graphOf(step("look", "ghost", nil)), `no agent type "ghost"`)
}

func TestAnEdgeToNowhereIsRefused(t *testing.T) {
	refuses(t, graphOf(step("look", "reader", []string{"missing"})), "which no step declares")
}

func TestACycleIsRefused(t *testing.T) {
	refuses(t, graphOf(
		step("a", "reader", []string{"b"}),
		step("b", "reader", []string{"a"}),
	), "cycle")
}

func TestAStepThatWaitsOnItselfIsRefused(t *testing.T) {
	refuses(t, graphOf(step("a", "reader", []string{"a"})), "waits on itself")
}

func TestADuplicateStepIsRefused(t *testing.T) {
	refuses(t, graphOf(
		step("a", "reader", nil),
		step("a", "reader", nil),
	), "declared twice")
}

// A step cannot be granted an effect its agent type never declared. The spawn
// would refuse it later; refusing here means the earlier steps have not run
// yet when it happens.
func TestAnEffectTheTypeDoesNotDeclareIsRefused(t *testing.T) {
	refuses(t, graphOf(
		step("a", "reader", nil, contract.EffectRead, contract.EffectWrite),
	), "which agent type reader does not declare")
}

// Money is split, not copied.
func TestSharesMayNotAddUpPastTheGrant(t *testing.T) {
	graph := graphOf(
		step("a", "reader", nil),
		step("b", "reader", nil),
	)
	graph.GrantUSD = 1
	graph.Steps[0].Permission.BudgetUSD = 0.75
	graph.Steps[1].Permission.BudgetUSD = 0.75
	refuses(t, graph, "past the 1.00 grant")
}

func TestSharesThatFitAreAccepted(t *testing.T) {
	graph := graphOf(
		step("a", "reader", nil),
		step("b", "reader", nil),
	)
	graph.GrantUSD = 1
	graph.Steps[0].Permission.BudgetUSD = 0.5
	graph.Steps[1].Permission.BudgetUSD = 0.5
	if _, err := compile(t, graph); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// The write rule. Two steps that could run at the same time may not both
// touch a file when one of them writes it -- refused when the graph is built,
// so the answer does not depend on how fast the machine is that day.
func TestTwoConcurrentWritersOnOneFileAreRefused(t *testing.T) {
	refuses(t, graphOf(
		withFiles(step("a", "writer", nil, contract.EffectRead, contract.EffectWrite), "notes.md"),
		withFiles(step("b", "writer", nil, contract.EffectRead, contract.EffectWrite), "notes.md"),
	), "both touch notes.md")
}

func TestAConcurrentReaderAndWriterOnOneFileAreRefused(t *testing.T) {
	refuses(t, graphOf(
		withFiles(step("a", "writer", nil, contract.EffectRead, contract.EffectWrite), "notes.md"),
		withFiles(step("b", "reader", nil), "notes.md"),
	), "which a writes")
}

// Two readers are fine: nothing changes under either of them.
func TestTwoConcurrentReadersShareAFile(t *testing.T) {
	if _, err := compile(t, graphOf(
		withFiles(step("a", "reader", nil), "notes.md"),
		withFiles(step("b", "reader", nil), "notes.md"),
	)); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// An edge is the author saying these two are ordered, and ordered steps are
// never concurrent however the lanes are sized.
func TestAnOrderedWriterAndReaderShareAFile(t *testing.T) {
	if _, err := compile(t, graphOf(
		withFiles(step("a", "writer", nil, contract.EffectRead, contract.EffectWrite), "notes.md"),
		withFiles(step("b", "reader", []string{"a"}), "notes.md"),
	)); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// Ordering through a third step counts: the rule asks whether one can reach
// the other at all, not whether they are neighbors.
func TestTransitiveOrderingIsEnough(t *testing.T) {
	if _, err := compile(t, graphOf(
		withFiles(step("a", "writer", nil, contract.EffectRead, contract.EffectWrite), "notes.md"),
		step("mid", "reader", []string{"a"}),
		withFiles(step("b", "writer", []string{"mid"}, contract.EffectRead, contract.EffectWrite), "notes.md"),
	)); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// Two spellings of one path are one path.
func TestPathsAreComparedNormalized(t *testing.T) {
	refuses(t, graphOf(
		withFiles(step("a", "writer", nil, contract.EffectRead, contract.EffectWrite), "./docs/../notes.md"),
		withFiles(step("b", "reader", nil), "notes.md"),
	), "notes.md")
}

func TestAGraphWithNoStepsIsRefused(t *testing.T) {
	refuses(t, workflow.Graph{Task: "nothing"}, "nothing to run")
}

func TestAGraphWithNoTaskIsRefused(t *testing.T) {
	refuses(t, workflow.Graph{Steps: []workflow.Step{step("a", "reader", nil)}}, "task is required")
}
