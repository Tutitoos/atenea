package claudecode

import (
	"context"
	"strings"
	"testing"
)

// oneTurnAnswer is what a turn that answered without ever reading a tool
// result looks like on the wire: a clean, cheap, well-formed structured
// answer after a single completion. Nothing here is wrong by the envelope's
// own rules -- is_error is false and the schema is satisfied -- which is
// exactly why the doubt has to be raised by num_turns and not by anything
// that looks like a failure.
const oneTurnAnswer = `{"is_error":false,"subtype":"success",
  "structured_output":{"matches":[{"path":"cmd/main.go","line":3,"column":1}]},
  "usage":{"input_tokens":80,"output_tokens":30},
  "total_cost_usd":0.004,"num_turns":1}`

// nearCeilingAnswer spent 90% of a generous grant across several turns and
// still came back with is_error false. Nothing about the envelope says it
// stopped short -- that is the point: the only signal is how close the
// number came to the edge.
const nearCeilingAnswer = `{"is_error":false,"subtype":"success",
  "structured_output":{"matches":[{"path":"cmd/main.go","line":3,"column":1}]},
  "usage":{"input_tokens":9000,"output_tokens":3000},
  "total_cost_usd":0.225,"num_turns":5}`

// A one-turn answer never read a tool result back, because a tool call
// always costs a turn of its own to consume. The prompt tells the far side
// to use Grep and not invent a match, so an answer that skipped straight to
// a result is worth doubting even though nothing about it failed.
func TestASingleTurnAnswerIsFlaggedDoubtful(t *testing.T) {
	runner := billing(t, oneTurnAnswer)
	req := granted(t, 0.25, map[string]any{"query": "TODO"})

	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !anyContains(out.Notices, "single turn") {
		t.Errorf("notices = %q, want one naming the single turn", out.Notices)
	}
	if anyContains(out.Notices, "ceiling") {
		t.Errorf("notices = %q, a cheap call should not also trip the ceiling check", out.Notices)
	}
}

// A call that spent nine tenths of a generous grant is the same shape as the
// runs that died outright, one step earlier. Nothing in the envelope marks
// it as incomplete -- it has to be read off the money.
func TestANearCeilingAnswerIsFlaggedDoubtful(t *testing.T) {
	runner := billing(t, nearCeilingAnswer)
	req := granted(t, 0.25, map[string]any{"query": "TODO"})

	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !anyContains(out.Notices, "90%") {
		t.Errorf("notices = %q, want one naming the fraction spent", out.Notices)
	}
	if anyContains(out.Notices, "single turn") {
		t.Errorf("notices = %q, a five-turn call should not also trip the single-turn check", out.Notices)
	}
}

// An ordinary, cheap, multi-turn answer is the common case, and it has to
// stay quiet: a notice on every call trains the reader to skip the one that
// matters.
func TestAnOrdinaryAnswerCarriesNoCompletenessNotice(t *testing.T) {
	runner := billing(t, answered)
	req := granted(t, 0.25, map[string]any{"query": "TODO"})

	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Notices) != 0 {
		t.Errorf("notices = %q, want none", out.Notices)
	}
}

// The two doubts are independent checks, not a single verdict: a one-turn
// answer that also spent past the ceiling fraction is not less doubtful for
// having two reasons, so both have to be said.
func TestBothDoubtsCanFireOnTheSameAnswer(t *testing.T) {
	const both = `{"is_error":false,"subtype":"success",
  "structured_output":{"matches":[{"path":"cmd/main.go","line":3,"column":1}]},
  "usage":{"input_tokens":9000,"output_tokens":3000},
  "total_cost_usd":0.225,"num_turns":1}`
	runner := billing(t, both)
	req := granted(t, 0.25, map[string]any{"query": "TODO"})

	out, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Notices) != 2 {
		t.Errorf("notices = %q, want exactly two", out.Notices)
	}
}

// anyContains reports whether any notice contains substr. Notices are full
// sentences, not tokens, so this checks substrings rather than membership.
func anyContains(notices []string, substr string) bool {
	for _, n := range notices {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}
