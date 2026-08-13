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
		reviewing(step("judge", "critic", nil), "look"),
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

// --- subject edges -------------------------------------------------------

// The refusal that replaces the previous version's run-time `incomplete`: a
// reviewer with no subject used to spawn, read an empty card, and say so
// itself. Nothing about that was visible until the earlier steps had run.
func TestAReviewerWithNoSubjectIsRefused(t *testing.T) {
	refuses(t, graphOf(
		step("look", "reader", nil),
		step("judge", "critic", []string{"look"}),
	), "a review needs a subject")
}

// The other direction: an answer handed to something that never reads one.
// The predicate is the type's declared input, not its lane -- a planner reads
// a subject and is not a reviewer.
func TestASubjectOnATypeThatNeverReadsOneIsRefused(t *testing.T) {
	refuses(t, graphOf(
		step("look", "reader", nil),
		reviewing(step("other", "reader", nil), "look"),
	), "never reads one")
}

// And a type that declares the input takes one without being a reviewer.
func TestATypeThatDeclaresItReadsASubjectTakesOneOutsideTheReviewLane(t *testing.T) {
	planner := declared("planner", "/bin/true", config.PoolAgent)
	planner.ReadsSubject = true
	types := append(compileTypes(), planner)

	graph := graphOf(
		step("look", "reader", nil),
		reviewing(step("plan", "planner", nil), "look"),
	)
	plan, err := workflow.Compile(graph, types)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if plan.Pool("plan") != config.PoolAgent {
		t.Errorf("lane = %v, want the agent lane: reading a subject is not reviewing", plan.Pool("plan"))
	}
}

func TestASubjectNamingNothingIsRefused(t *testing.T) {
	refuses(t, graphOf(
		step("look", "reader", nil),
		reviewing(step("judge", "critic", nil), "ghost"),
	), `reads the answer of "ghost"`)
}

func TestAStepThatReviewsItselfIsRefused(t *testing.T) {
	refuses(t, graphOf(reviewing(step("judge", "critic", nil), "judge")), "reviews itself")
}

// `on` is only meaningful with a subject. Ignoring a stray one would leave a
// line its author believes is doing something.
func TestOnWithNoSubjectIsRefused(t *testing.T) {
	stray := step("look", "reader", nil)
	stray.On = workflow.OnOK
	refuses(t, graphOf(stray), "no subject to apply it to")
}

// A subject is an edge as well as a pipe, so it closes a loop like any other.
func TestACycleThroughASubjectIsRefused(t *testing.T) {
	refuses(t, graphOf(
		step("look", "reader", []string{"judge"}),
		reviewing(step("judge", "critic", nil), "look"),
	), "cycle")
}

// The write rule reads the same edges the scheduler does. A reviewer ordered
// after the writer it audits is not concurrent with it, so sharing the file
// is fine -- and if the subject edge were invisible here, this graph would be
// refused for a conflict that cannot happen.
func TestASubjectEdgeOrdersAgainstTheWriteRule(t *testing.T) {
	if _, err := compile(t, graphOf(
		withFiles(step("edit", "writer", nil, contract.EffectRead, contract.EffectWrite), "notes.md"),
		withFiles(reviewing(step("judge", "critic", nil), "edit"), "notes.md"),
	)); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

func TestRequirementParsing(t *testing.T) {
	for _, want := range []workflow.Requirement{workflow.OnAnswered, workflow.OnOK} {
		got, err := workflow.ParseRequirement(want.String())
		if err != nil || got != want {
			t.Fatalf("round trip of %s = %v, %v", want, got, err)
		}
	}
	if _, err := workflow.ParseRequirement("maybe"); err == nil {
		t.Fatal("accepted a requirement that is not a word")
	}
}
