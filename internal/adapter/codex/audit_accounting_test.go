package codex

import (
	"testing"
	"time"
)

func TestAuditCostLostOnExit(t *testing.T) {
	bin := fakeCodex(t, `{"type":"turn.completed","total_cost_usd":0.125,"usage":{"input_tokens":12,"output_tokens":4}}`, 1, 0, "connection lost")
	out, err := newRunner(t, bin, 10*time.Second).Run(t.Context(), request(t, t.TempDir(), map[string]any{"query": "test"}))
	if err == nil || !out.SpentUSDKnown || out.SpentUSD != 0.125 || out.Spent.Tokens != 16 {
		t.Fatalf("unexpected: %+v %v", out, err)
	}
}
