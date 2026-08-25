package model

import (
	"context"
	"strings"
	"testing"
)

// A turn with no allowance to derive is single-shot, and single-shot never
// read the model's own account of what it covered.
//
// Only converse called claimOf, so Answer.Completeness came back nil -- and
// the planner refuses an answer that states no coverage, with "the model
// answered without stating completeness". ReadTokens is only positive when
// there is a budget behind it, so `atenea agent explore --objective ...`,
// which passes none, failed that way on every run, after paying for the turn.
func TestASingleShotTurnReportsWhatTheModelClaimed(t *testing.T) {
	const claimed = `{"is_error":false,"subtype":"success","result":"see structured_output",
	  "structured_output":{"completeness":0.5,"stopped_at":"the second half","summary":"half"},
	  "usage":{"input_tokens":10,"output_tokens":2},"total_cost_usd":0.001}`

	answer, err := testClient(t, claimed).Turn(context.Background(), Request{
		Role:   RoleExplore,
		Prompt: "look around",
		Schema: map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if answer.Passes != 1 {
		t.Fatalf("passes = %d, want the single-shot path", answer.Passes)
	}
	if answer.Completeness == nil {
		t.Fatal("the turn reports no completeness: the planner refuses this answer " +
			"even though the model stated its coverage")
	}
	if *answer.Completeness != 0.5 {
		t.Errorf("completeness = %v, want the 0.5 the model claimed", *answer.Completeness)
	}
	if answer.StoppedAt != "the second half" {
		t.Errorf("stopped_at = %q, want what the model said it did not reach", answer.StoppedAt)
	}
}

// A model that claims nothing still answers. The absence is the model's, not
// this path's, and it must not be invented into a number.
func TestASingleShotTurnThatClaimsNothingStaysUnclaimed(t *testing.T) {
	answer, err := testClient(t, structuredAnswer).Turn(context.Background(), Request{
		Role:   RoleExplore,
		Prompt: "look around",
		Schema: map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if answer.Completeness != nil {
		t.Errorf("completeness = %v, want nil: the model claimed nothing", *answer.Completeness)
	}
}

// max_tokens bounds the request, not the bill.
//
// It used to be compared against Charge.Tokens(), which sums cache_read and
// cache_write as well. A cache read is context the provider had already
// stored -- 356,434 of them observed on one explore turn against a real
// repository -- so a ceiling meant to bound the size of a request was held
// against a number dominated by what previous requests had cached. Every type
// that calls a model declares 200,000 or 100,000, so this refused completed,
// paid-for answers on ordinary turns.
func TestCachedTokensDoNotCountAgainstTheDeclaredLimit(t *testing.T) {
	const cached = `{"is_error":false,"subtype":"success","result":"the answer",
	  "usage":{"input_tokens":100,"output_tokens":40,
	           "cache_read_input_tokens":356434,"cache_creation_input_tokens":2000},
	  "total_cost_usd":0.02}`

	answer, err := testClient(t, cached).Turn(context.Background(), Request{
		Role:      RoleExplore,
		Prompt:    "look around",
		MaxTokens: 1000,
	})
	if err != nil {
		t.Fatalf("a turn of 140 requested tokens was refused under a 1000 limit "+
			"because the provider's cache was counted against it: %v", err)
	}
	// The charge still carries the cache. It is real money; what it is not is
	// part of the request this ceiling bounds.
	if answer.Spent.CacheReadTokens != 356434 {
		t.Errorf("cache_read = %d, want it kept on the charge", answer.Spent.CacheReadTokens)
	}
}

// And the ceiling still refuses what it is for: a request genuinely bigger
// than the type declared.
func TestARequestOverTheDeclaredLimitIsStillRefused(t *testing.T) {
	const large = `{"is_error":false,"subtype":"success","result":"the answer",
	  "usage":{"input_tokens":9000,"output_tokens":4000},"total_cost_usd":0.02}`

	_, err := testClient(t, large).Turn(context.Background(), Request{
		Role:      RoleExplore,
		Prompt:    "look around",
		MaxTokens: 1000,
	})
	if err == nil {
		t.Fatal("a request of 13,000 tokens passed a 1000-token ceiling")
	}
	if !strings.Contains(err.Error(), "13000") {
		t.Errorf("refusal = %q, want it to name the requested total", err)
	}
}
