package workflow_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every exit from the dispatch loop unwinds the same way, and this reads the
// source to say so.
//
// There is a behavioral test for one of the routes -- closing the store under
// three running steps, so the Finish write fails -- and it is the expensive
// kind: three real agent processes, a stack-dump goroutine count, and a
// thirty-second settle. Writing three more of it, one per remaining route,
// would cost two minutes of suite time to check the same mechanism four times,
// and it could not choose which store call fails first anyway.
//
// So the behavior is checked once and the shape is checked here. The shape is
// the actual invariant: `results` is unbuffered, so a return that does not
// drain it strands every goroutine whose step has finished, and a wait that
// does not drain it deadlocks. Both of those existed -- two exits called
// cancel() and wg.Wait() with nobody reading, and three returned without
// either -- and both stop being possible the moment there is exactly one
// unwind and it is deferred.
func TestEveryExitFromTheDispatchLoopUnwindsTheSameWay(t *testing.T) {
	body, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatalf("read engine.go: %v", err)
	}
	source := string(body)

	start := strings.Index(source, "func (e *Engine) execute(")
	if start < 0 {
		t.Fatal("engine.go has no execute: this test is reading the wrong thing")
	}
	end := strings.Index(source[start+1:], "\nfunc ")
	if end < 0 {
		t.Fatal("execute has no function after it: this test is reading the wrong thing")
	}
	execute := source[start : start+1+end]

	if !strings.Contains(execute, "defer unwind(cancel, &wg, results)") {
		t.Error("execute does not defer its unwind: a return that skips it strands " +
			"every goroutine whose step has already finished")
	}
	// A bare wait, without the drain beside it, is the deadlock. unwind's own
	// wait lives in unwind, which is a different function and not read here.
	if bare := regexp.MustCompile(`(?m)^\s*wg\.Wait\(\)\s*$`).FindString(execute); bare != "" {
		t.Errorf("execute waits on the WaitGroup directly (%q): `results` is unbuffered, "+
			"so a wait with nobody draining it never returns", strings.TrimSpace(bare))
	}
	if strings.Contains(execute, "cancel()\n\t\t\t\twg.Wait()") {
		t.Error("execute still pairs cancel() with a bare wg.Wait(): that pair is the deadlock")
	}
}

// And unwind itself does both halves, in the order that makes the drain
// complete: each goroutine sends and only then defers its Done, so a returned
// Wait means every send was received.
func TestUnwindCancelsAndDrainsBeforeItWaits(t *testing.T) {
	body, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatalf("read engine.go: %v", err)
	}
	start := strings.Index(string(body), "func unwind(")
	if start < 0 {
		t.Fatal("engine.go has no unwind: this test is reading the wrong thing")
	}
	// The tail of the file is a legitimate place for unwind to end up, and
	// Index returning -1 there would silently slice the function down to
	// nothing -- which fails below with three messages saying unwind does not
	// cancel, does not wait and does not drain, about code that does all
	// three.
	rest := string(body)[start+1:]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		end = len(rest)
	}
	unwind := string(body)[start : start+1+end]

	for _, want := range []string{"cancel()", "wg.Wait()", "case <-results:"} {
		if !strings.Contains(unwind, want) {
			t.Errorf("unwind does not %s, so it is not an unwind", want)
		}
	}
	if strings.Index(unwind, "cancel()") > strings.Index(unwind, "case <-results:") {
		t.Error("unwind drains before it cancels: the agents go on running while it reads")
	}
}
