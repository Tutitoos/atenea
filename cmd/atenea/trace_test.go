package main

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func stepWithDrops(id string, drops ...selector.Drop) orchestrator.StepResult {
	return orchestrator.StepResult{
		Step:     contract.Step{ID: id, Capability: "code.search", Repository: "current"},
		Decision: selector.Decision{Stages: []selector.Stage{{Name: "health", Dropped: drops}}},
	}
}

func drop(id, reason string) selector.Drop {
	return selector.Drop{Implementation: id, Reason: reason}
}

// A drop identical in every step is a fact about the catalog on this
// machine, not about any step. Measured on the dogfood run: four of five trace
// lines per step were the same three sentences, which buries the drops that
// did vary -- the only ones worth reading a trace for.
func TestDropsThatNeverVaryAreReportedOnce(t *testing.T) {
	shared := drop("serena.search", "no attached runner serves it")
	steps := []orchestrator.StepResult{
		stepWithDrops("explore", shared, drop("claude.search", "over budget")),
		stepWithDrops("work", shared),
	}

	everywhere, static := staticDrops(steps)

	if len(static) != 1 || static[0].implementation != "serena.search" {
		t.Fatalf("static = %+v, want only serena.search", static)
	}
	if !everywhere[dropKey{"serena.search", "no attached runner serves it", ""}] {
		t.Error("the shared drop was not marked for collapsing")
	}
	// The one that appeared in a single step is the finding. Collapsing it
	// would move a step-specific fact into a heading claiming every step.
	if everywhere[dropKey{"claude.search", "over budget", ""}] {
		t.Error("a drop from one step was reported as happening in every step")
	}
}

// One step cannot repeat itself, so there is nothing to collapse: for a plain
// ask the drops are the whole story of that single funnel and belong inline.
func TestASingleStepKeepsItsDropsInline(t *testing.T) {
	everywhere, static := staticDrops([]orchestrator.StepResult{
		stepWithDrops("ask", drop("serena.search", "no attached runner serves it")),
	})
	if len(everywhere) != 0 || len(static) != 0 {
		t.Errorf("collapsed %+v on a one-step run", static)
	}
}

// A step that never reached the funnel -- blocked, canceled -- has no opinion
// about any provider. Counting it as a step that "did not drop" would stop
// every real repetition from ever collapsing.
func TestAStepWithNoFunnelDecisionDoesNotBlockCollapsing(t *testing.T) {
	shared := drop("serena.search", "no attached runner serves it")
	steps := []orchestrator.StepResult{
		stepWithDrops("explore", shared),
		stepWithDrops("work", shared),
		{Step: contract.Step{ID: "blocked"}},
	}
	_, static := staticDrops(steps)
	if len(static) != 1 {
		t.Errorf("static = %+v, want the shared drop collapsed", static)
	}
}

// A commission reports a count because that is all that composes across
// repositories. A count is not something anybody can act on: measured on the
// dogfood run, learning which files were behind `15 hit(s)` cost a second full
// dispatch as `ask --json`.
func TestTheTracePathsAreTheDistinctFilesInOrder(t *testing.T) {
	answer := map[string]any{
		"matches": []any{
			map[string]any{"path": "internal/a.go", "line": 1},
			map[string]any{"path": "internal/a.go", "line": 9},
			map[string]any{"path": "cmd/b.go", "line": 4},
		},
	}
	got := answerPaths(answer)
	want := []string{"internal/a.go", "cmd/b.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v, want %v -- distinct, in the order the answer named them", got, want)
	}
}

// The walk knows no capability's shape: the symbol capabilities answer with
// locations, not matches, and nest them differently. Every one of them calls
// the field "path" because the contract says so.
func TestTracePathsFindLocationsNestedAnywhere(t *testing.T) {
	answer := map[string]any{
		"location":  map[string]any{"path": "pkg/contract/version.go", "line": 12},
		"locations": []any{map[string]any{"path": "internal/core/core.go"}},
	}
	got := answerPaths(answer)
	if len(got) != 2 {
		t.Fatalf("paths = %v, want both the single location and the list", got)
	}

	// An answer with no file in it contributes nothing rather than a blank.
	if paths := answerPaths(map[string]any{"count": 3}); len(paths) != 0 {
		t.Errorf("paths = %v for an answer naming no file", paths)
	}
	if paths := answerPaths(map[string]any{"matches": []any{map[string]any{"path": ""}}}); len(paths) != 0 {
		t.Errorf("paths = %v, want an empty path ignored rather than listed", paths)
	}
}
