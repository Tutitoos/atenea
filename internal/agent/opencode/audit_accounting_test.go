package opencode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAuditFinalizationExceedsBudget checks the regression scenario: audit finalization exceeds budget.
func TestAuditFinalizationExceedsBudget(t *testing.T) {
	bin := executable(t, `case " $* " in
 *" --session "*)
 echo '{"type":"text","part":{"type":"text","text":"done"}}'
 echo '{"type":"step_finish","part":{"type":"step-finish","reason":"stop","tokens":{"input":10,"output":10},"cost":2.0}}';;
 *)
 echo '{"type":"step_finish","sessionID":"audit","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":1,"output":1},"cost":0.1}}'
 sleep 5;;
 esac`)
	r, err := New(Options{Binary: bin, Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.Run(t.Context(), Request{Prompt: "synthetic", BudgetUSD: 1, ReadTokens: 1, MaxTokens: 10})
	if err == nil || a.Spent.USD == nil || *a.Spent.USD < 2 {
		t.Fatalf("not reproduced: %+v %v", a, err)
	}
}

// TestAuditTimeoutDropsObservedCost checks the regression scenario: audit timeout drops observed cost.
func TestAuditTimeoutDropsObservedCost(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ready")
	bin := executable(t, `echo '{"type":"step_finish","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":10,"output":4},"cost":0.12}}'
 touch '`+marker+`'
 sleep 10`)
	r, err := New(Options{Binary: bin, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	var a Answer
	var runErr error
	go func() { a, runErr = r.Run(ctx, Request{Prompt: "synthetic"}); close(done) }()
	limit := time.Now().Add(10 * time.Second)
	for {
		if _, e := os.Stat(marker); e == nil {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("fixture did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	if runErr == nil || a.Spent.USD == nil || *a.Spent.USD != 0.12 || a.Spent.Tokens() != 14 {
		t.Fatalf("not reproduced: %+v %v", a, runErr)
	}
}
