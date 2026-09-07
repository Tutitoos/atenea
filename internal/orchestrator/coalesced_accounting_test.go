package orchestrator

import (
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestCoalescedOutcomeIsExcludedFromProviderAccounting(t *testing.T) {
	for _, test := range []struct {
		name string
		out  contract.Outcome
		err  error
		want bool
	}{
		{name: "leader", out: contract.Outcome{Verdict: contract.VerdictOK}, want: true},
		{name: "waiter", out: contract.Outcome{Verdict: contract.VerdictOK, Coalesced: true}, want: false},
		{name: "stored hit", out: contract.Outcome{Verdict: contract.VerdictOK, CacheHit: true}, want: false},
		{name: "canceled", out: contract.Outcome{Verdict: contract.VerdictFailed}, err: contract.Fail(contract.FailureCanceled, "canceled"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := countsAsProviderSample(test.out, test.err); got != test.want {
				t.Fatalf("countsAsProviderSample = %v, want %v", got, test.want)
			}
		})
	}
}
