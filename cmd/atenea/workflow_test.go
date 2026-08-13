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
	if got := stepCost(workflow.StepRow{}); got != "unmeasured" {
		t.Fatalf("stepCost = %q, want %q", got, "unmeasured")
	}
}

// A step measured only in tokens -- nobody priced it -- shows the tokens,
// never a fabricated dollar figure.
func TestATokenOnlyStepCostShowsTokensNotADollarFigure(t *testing.T) {
	row := workflow.StepRow{Spent: contract.Charge{InputTokens: 40, OutputTokens: 12}}
	if got := stepCost(row); got != "52 tok" {
		t.Fatalf("stepCost = %q, want the token count", got)
	}
}

// A priced step shows the dollar figure.
func TestAPricedStepCostShowsTheDollarFigure(t *testing.T) {
	usd := 0.5
	row := workflow.StepRow{Spent: contract.Charge{USD: &usd, PricedBy: "anthropic"}}
	if got := stepCost(row); got != "$0.50" {
		t.Fatalf("stepCost = %q, want the dollar figure", got)
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
