package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// answered is one closed, successful ask carrying the given notices -- an
// outcome an adapter answered fully, with zero or more caveats attached
// alongside it, never in place of it.
func answered(notices ...string) *orchestrator.Result {
	step := orchestrator.StepResult{
		Step:  contract.Step{ID: "calls-current", Capability: "symbol.calls", Repository: "current"},
		Phase: orchestrator.PhaseWork,
		Spent: contract.Sample{Duration: 92 * time.Millisecond},
		Outcome: contract.Outcome{
			Verdict: contract.VerdictOK,
			Result:  map[string]any{"calls": []any{}},
			Notices: notices,
		},
		Review: orchestrator.Review{Child: contract.VerdictOK, Parent: contract.VerdictOK},
	}
	return &orchestrator.Result{
		RunID:   "run-1",
		Task:    "who calls X",
		Verdict: contract.VerdictOK,
		Steps:   []orchestrator.StepResult{step},
		Spent:   step.Spent,
	}
}

// The trace names the step's own caveat, the same way it already names what
// the step charged or how its review went -- a notice is not a metric or a
// verdict, but it is exactly the kind of thing --trace exists to surface.
func TestTheTraceShowsAStepsNotice(t *testing.T) {
	var out bytes.Buffer
	printResult(&out, answered("index may be stale: HEAD has moved since it was built"), true)

	if !strings.Contains(out.String(), "notice   index may be stale: HEAD has moved since it was built") {
		t.Errorf("the trace does not show the step's notice:\n%s", out.String())
	}
}

// An outcome with nothing to flag prints no notice line at all -- not an
// empty one. An adapter's silence and a caveat both need to be visible as
// what they are.
func TestNoNoticesMeansNoNoticeLine(t *testing.T) {
	var out bytes.Buffer
	printResult(&out, answered(), true)

	if strings.Contains(out.String(), "notice") {
		t.Errorf("an outcome with nothing to flag printed a notice line:\n%s", out.String())
	}
}

// A plain `ask`, without --trace, is the common case, and a caveat about the
// very answer on screen must not hide behind a flag most callers never pass.
func TestAskShowsANoticeEvenWithoutTrace(t *testing.T) {
	var out bytes.Buffer
	printAnswer(&out, answered("index may be stale: the working tree has uncommitted changes since it was built"), false)

	body := out.String()
	if !strings.Contains(body, "notice   index may be stale: the working tree has uncommitted changes since it was built") {
		t.Errorf("ask without --trace hid the notice:\n%s", body)
	}
	if strings.Index(body, "notice") > strings.Index(body, "answer") {
		t.Errorf("the notice printed after the answer it qualifies, want it before:\n%s", body)
	}
}

// --trace already shows every step's notices in the per-step trace above;
// printAnswer must not say the same caveat a second time underneath it.
func TestTraceDoesNotRepeatTheNoticeInTheAnswerBlock(t *testing.T) {
	var out bytes.Buffer
	printAnswer(&out, answered("index may be stale: HEAD has moved since it was built"), true)

	if strings.Contains(out.String(), "notice") {
		t.Errorf("--trace already shows this in the per-step trace; printAnswer repeated it:\n%s", out.String())
	}
}
