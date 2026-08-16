package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/workflow"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// A step nobody could meter reads `unmeasured` in the cost column, never a
// dash or a $0.00 that would print a measurement nothing took.
func TestAnUnmeasuredStepCostReadsUnmeasuredNeverADashOrZero(t *testing.T) {
	if got := stepCost(workflow.Run{}, workflow.StepRow{}); got != "unmeasured" {
		t.Fatalf("stepCost = %q, want %q", got, "unmeasured")
	}
}

// A step measured only in tokens -- nobody priced it -- shows the tokens,
// never a fabricated dollar figure.
func TestATokenOnlyStepCostShowsTokensNotADollarFigure(t *testing.T) {
	row := workflow.StepRow{Spent: contract.Charge{InputTokens: 40, OutputTokens: 12}}
	if got := stepCost(workflow.Run{}, row); got != "52 tok" {
		t.Fatalf("stepCost = %q, want the token count", got)
	}
}

// A priced step shows the dollar figure.
func TestAPricedStepCostShowsTheDollarFigure(t *testing.T) {
	usd := 0.5
	row := workflow.StepRow{Spent: contract.Charge{USD: &usd, PricedBy: "anthropic"}}
	if got := stepCost(workflow.Run{}, row); got != "$0.50" {
		t.Fatalf("stepCost = %q, want the dollar figure", got)
	}
}

// The column has to sum to the total the header prints two lines above it. A
// redo overwrites the live row, so the step's own figure is only its last
// attempt: admin-config finished at $0.6756 having already spent $0.6182 on the
// dispatch it replaced, and the column said $0.68 for a step that cost $1.29.
func TestARedoneStepCostTotalsEveryAttempt(t *testing.T) {
	live, dead := 0.6756, 0.6182
	run := workflow.Run{
		Steps: []workflow.StepRow{{
			Step:  workflow.Step{ID: "admin-config"},
			Spent: contract.Charge{USD: &live, PricedBy: "a test"},
		}},
		Superseded: []workflow.AttemptRow{{
			StepID: "admin-config", Attempt: 1,
			Spent: contract.Charge{USD: &dead, PricedBy: "a test"},
		}},
	}
	if got := stepCost(run, run.Steps[0]); got != "$1.29" {
		t.Errorf("stepCost = %q, want $1.29 -- $0.6756 live plus $0.6182 replaced", got)
	}
	// And the archive of ANOTHER step must not land on this one.
	other := workflow.StepRow{
		Step:  workflow.Step{ID: "census"},
		Spent: contract.Charge{USD: &live, PricedBy: "a test"},
	}
	if got := stepCost(run, other); got != "$0.68" {
		t.Errorf("stepCost = %q, want $0.68 -- census has no archived attempt", got)
	}
}

// A redo sets the step pending, so between dispatch and finish the live row is
// unmeasured while the archive already holds real money. "unmeasured" there
// would report a step that has spent $0.62 as one nothing could meter.
func TestAStepMidRedoReportsWhatItsReplacedAttemptSpent(t *testing.T) {
	dead := 0.6182
	run := workflow.Run{
		Steps: []workflow.StepRow{{Step: workflow.Step{ID: "admin-config"}}},
		Superseded: []workflow.AttemptRow{{
			StepID: "admin-config", Attempt: 1,
			Spent: contract.Charge{USD: &dead, PricedBy: "a test"},
		}},
	}
	if got := stepCost(run, run.Steps[0]); got != "$0.62" {
		t.Errorf("stepCost = %q, want $0.62 -- the attempt it replaced was priced", got)
	}
}

// The run table never shows a measured-looking zero for a step nobody could
// meter, next to a step that really was priced.
func TestPrintRunsCostColumnNeverShowsAMeasuredLookingZero(t *testing.T) {
	usd := 0.12
	run := workflow.Run{
		ID:   "wf-1",
		Task: "test",
		Steps: []workflow.StepRow{
			{
				Step:   workflow.Step{ID: "a", TypeName: "x"},
				Pool:   config.PoolAgent,
				Status: workflow.StatusOK,
				Spent:  contract.Charge{USD: &usd, PricedBy: "anthropic"},
			},
			{
				Step:   workflow.Step{ID: "b", TypeName: "y"},
				Pool:   config.PoolAgent,
				Status: workflow.StatusOK,
			},
		},
	}

	var buf bytes.Buffer
	printRun(&buf, run)
	out := buf.String()
	if !strings.Contains(out, "$0.12") {
		t.Fatalf("output = %q, want the priced step's cost", out)
	}
	if !strings.Contains(out, "unmeasured") {
		t.Fatalf("output = %q, want the unpriced step to read unmeasured", out)
	}
	if strings.Contains(out, "$0.00") {
		t.Fatalf("output = %q: a measured-looking zero for an unmeasured step "+
			"is the exact lie this avoids", out)
	}
}
