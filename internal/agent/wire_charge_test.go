package agent

import "testing"

// TestObservedChargeRejectsIncompleteOrInvalidCosts keeps unknown reservations conservative.
func TestObservedChargeRejectsIncompleteOrInvalidCosts(t *testing.T) {
	for _, raw := range []string{`{"spent":{"usd":0.2,"priced_by":"fixture"}`, `{"spent":{"usd":-1,"priced_by":"fixture"}}`, `{"spent":{"usd":1}}`, `{"spent":{"input_tokens":-1}}`} {
		if got := observedCharge([]byte(raw)); got.Measured() {
			t.Fatalf("invalid charge recovered: %+v", got)
		}
	}
}
