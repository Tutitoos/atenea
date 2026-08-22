package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestWorkflowCommandReadersAndStateLabels(t *testing.T) {
	if _, err := cli(t, "workflow"); err == nil {
		t.Fatal("workflow without subcommand unexpectedly succeeded")
	}
	if _, err := cli(t, "workflow", "unknown"); err == nil {
		t.Fatal("unknown workflow subcommand unexpectedly succeeded")
	}

	path := filepath.Join(t.TempDir(), "workflow.db")
	out, err := cli(t, "workflow", "list", "--traces", path)
	if err != nil {
		t.Fatalf("workflow list: %v", err)
	}
	if !strings.Contains(out, "no workflows in "+path) {
		t.Fatalf("workflow list = %q", out)
	}
	if _, err := cli(t, "workflow", "show", "missing", "--traces", path); err == nil {
		t.Fatal("workflow show missing unexpectedly succeeded")
	}

	base := workflow.Run{ID: "wf-1", Task: "task"}
	for _, tc := range []struct {
		name string
		run  workflow.Run
		want string
	}{
		{name: "unlaunched", run: base, want: "unlaunched"},
		{name: "finished", run: workflow.Run{Closed: true}, want: "finished"},
		{name: "stopped", run: workflow.Run{Stop: workflow.StopRejected}, want: "rejected"},
		{name: "running", run: workflow.Run{WriterPID: os.Getpid()}, want: "running"},
		{name: "orphaned", run: workflow.Run{WriterPID: 99999999}, want: "orphaned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runState(tc.run); got != tc.want {
				t.Fatalf("runState(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}

	usd := 1.25
	step := workflow.Step{ID: "search", TypeName: "reader", Task: contract.Task{Objective: "find TODO"}, Permission: contract.Permission{BudgetUSD: usd}}
	run := workflow.Run{ID: "wf-1", Task: "inspect", GrantUSD: 2, Steps: []workflow.StepRow{{Step: step, Status: workflow.StatusPending}}}
	gate := workflow.Gate{RunID: run.ID, Ordinal: 0, Kind: workflow.KindLaunch, Digest: "digest", Asked: time.Now(), Proposal: workflow.Proposal{Steps: []workflow.Step{step}, Replaces: []string{"old"}}}
	var buf bytes.Buffer
	printGate(&buf, run, gate)
	for _, want := range []string{"wf-1  inspect", "gate 0 launch", "$1.25 of $2.00", "search", "replaces old", "workflow launch wf-1"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("gate output missing %q: %s", want, buf.String())
		}
	}
	buf.Reset()
	printGates(&buf, []workflow.Gate{{Ordinal: 0, Kind: workflow.KindLaunch, Decision: workflow.DecisionApproved, Digest: "digest", Asked: time.Now(), Hand: "tester"}, {Ordinal: 1, Kind: workflow.KindApprove, Decision: workflow.DecisionRejected, Digest: "digest2", Answered: time.Now(), Hand: "tester", Reason: "too broad"}})
	if !strings.Contains(buf.String(), "too broad") || !strings.Contains(buf.String(), "GATE") {
		t.Fatalf("gates output = %q", buf.String())
	}
	buf.Reset()
	printGates(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("empty gates output = %q", buf.String())
	}
}

func TestWorkflowMutatingCommandsRejectMissingPositionals(t *testing.T) {
	var out bytes.Buffer
	cases := []struct {
		name string
		fn   func() error
	}{
		{name: "create", fn: func() error { return workflowCreate("", nil, &out) }},
		{name: "launch", fn: func() error { return workflowLaunch("", nil, &out) }},
		{name: "propose", fn: func() error { return workflowPropose(nil, &out) }},
		{name: "approve", fn: func() error { return workflowAnswer(nil, &out, workflow.DecisionApproved) }},
		{name: "reject", fn: func() error { return workflowAnswer(nil, &out, workflow.DecisionRejected) }},
		{name: "run", fn: func() error { return workflowRun("", nil, &out) }},
		{name: "resume", fn: func() error { return workflowResume("", nil, &out) }},
		{name: "redo", fn: func() error { return workflowRedo("", nil, &out) }},
		{name: "show", fn: func() error { return workflowShow(nil, &out) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err == nil {
				t.Fatalf("%s without positionals unexpectedly succeeded", tc.name)
			}
		})
	}
}

func TestWorkflowRenderingAndFlagValueHelpers(t *testing.T) {
	partial := 0.5
	run := workflow.Run{Steps: []workflow.StepRow{
		{Step: workflow.Step{ID: "alpha", Needs: []string{"base"}}, Status: workflow.StatusPending},
		{Step: workflow.Step{ID: "result"}, Status: workflow.StatusOK, Result: map[string]any{"answer": "done"}},
		{Step: workflow.Step{ID: "partial"}, Status: workflow.StatusOK, Completeness: &partial, StoppedAt: "remaining files"},
		{Step: workflow.Step{ID: "failed"}, Status: workflow.StatusFailed, Reason: contract.Reason{Kind: contract.FailureUnavailable, Text: "provider down"}},
	}}
	if got := stepDetail(run, run.Steps[0]); got != "after base" {
		t.Fatalf("pending stepDetail = %q", got)
	}
	if got := stepDetail(run, run.Steps[1]); got != "done" {
		t.Fatalf("result stepDetail = %q", got)
	}
	if got := stepDetail(run, run.Steps[2]); !strings.Contains(got, "remaining files") {
		t.Fatalf("partial stepDetail = %q", got)
	}
	if got := stepDetail(run, run.Steps[3]); !strings.Contains(got, "provider down") {
		t.Fatalf("failed stepDetail = %q", got)
	}
	if got := firstKey(map[string]any{"z": 1, "a": 2}); got != "a" {
		t.Fatalf("firstKey = %q", got)
	}
	if got := firstKey(nil); got != "" {
		t.Fatalf("firstKey(empty) = %q", got)
	}

	var redo stringList
	if err := redo.Set(" step-a "); err != nil || redo.String() != "step-a" {
		t.Fatalf("stringList.Set = %v, %q", err, redo.String())
	}
	if err := redo.Set(" "); err == nil {
		t.Fatal("empty stringList value unexpectedly accepted")
	}
	var raises raiseList
	for _, value := range []string{"step-a=$1.25", "step-b=0.50"} {
		if err := raises.Set(value); err != nil {
			t.Fatalf("raiseList.Set(%q): %v", value, err)
		}
	}
	if raises.String() != "step-a=1.25,step-b=0.50" {
		t.Fatalf("raiseList.String = %q", raises.String())
	}
	for _, value := range []string{"bad", "=1", "step=nope", "step=0"} {
		if err := raises.Set(value); err == nil {
			t.Errorf("raiseList.Set(%q) unexpectedly succeeded", value)
		}
	}
}
