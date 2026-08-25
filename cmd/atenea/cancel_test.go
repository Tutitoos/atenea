package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// A run the user stopped is not a failed commission, and the difference has to
// reach the shell. 6 means "the work was carried out and came back failed",
// which a script is entitled to retry or report; 130 is the number a shell
// reports for ctrl-c on its own, and nothing about it is worth retrying.
//
// The context here is alive on purpose. This is the core's own answer arriving
// by itself: the orchestrator folded the steps and said canceled, and that
// word has to be enough. A caller can stop a run through a context this
// function never sees.
func TestStoppingARunLeavesThroughItsOwnExitCode(t *testing.T) {
	result := &orchestrator.Result{Verdict: contract.VerdictCanceled}

	err := commissionError(context.Background(), result, nil)

	if got := contract.KindOf(err); got != contract.FailureCanceled {
		t.Fatalf("kind = %v, want canceled", got)
	}
	if got := exitCode(err); got != 130 {
		t.Errorf("exit code = %d, want 130", got)
	}
	if errors.Is(err, errCommissionFailed) {
		t.Error("a stopped run was reported as a failed commission")
	}
}

// The other witness, and it stands alone. This is the CLI's own knowledge:
// the signal arrived here, whatever the report says. A run can be cut down
// before the orchestrator gets as far as folding its steps into a verdict, so
// a dead context outranks a report that still reads `failed` — and it outranks
// the run error too, which is why a provider being down does not get the blame
// for an interruption that landed first.
func TestAStoppedRunIsNotReadAsAVerdict(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := commissionError(ctx, &orchestrator.Result{Verdict: contract.VerdictFailed},
		contract.Fail(contract.FailureUnavailable, "claude code is not logged in"))

	if got := contract.KindOf(err); got != contract.FailureCanceled {
		t.Errorf("kind = %v, want canceled: the run was interrupted, not unavailable", got)
	}
}

// And the boundary in the other direction: a run that finished before the
// signal arrived did finish. Reporting it as canceled would throw away an
// answer somebody already paid for.
func TestARunThatFinishedIsNotCanceledByALateSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := commissionError(ctx, &orchestrator.Result{Verdict: contract.VerdictOK}, nil)

	if err != nil {
		t.Errorf("err = %v, want nil: the work was done before the signal", err)
	}
}

// A commission nobody stopped keeps every code it had. This is the guard on
// the change itself: the new branch must be reachable only by a cancellation.
func TestTheOrdinaryExitCodesAreUnchanged(t *testing.T) {
	cases := []struct {
		name   string
		result *orchestrator.Result
		runErr error
		want   int
	}{
		{"a failed verdict", &orchestrator.Result{Verdict: contract.VerdictFailed}, nil, 6},
		{"a provider that is down", nil,
			contract.Fail(contract.FailureUnavailable, "down"), 4},
		{"a provider that really did time out", nil,
			contract.Fail(contract.FailureTimeout, "claude code took longer than 5m0s"), 4},
		{"a broken settings file", nil,
			contract.Fail(contract.FailureInvalidInput, "line 12"), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := commissionError(context.Background(), tc.result, tc.runErr)
			if got := exitCode(err); got != tc.want {
				t.Errorf("exit code = %d, want %d (err %v)", got, tc.want, err)
			}
		})
	}
}

// End to end through a real signal: a client that hangs, a real SIGINT, and a
// screen that has to say what happened without inventing a ceiling nobody
// reached. This is the report as it was filed -- ctrl-c after two seconds,
// "claude code took longer than 5m0s".
//
// It re-executes the test binary because run() installs its own signal
// handler, which is the thing under test: a context injected from a test
// would exercise everything except the wiring that goes wrong.
func TestTheScreenSaysCanceledAndNotTimeout(t *testing.T) {
	if os.Getenv(interruptEnv) != "" {
		hangUntilInterrupted()
		return
	}

	dir := t.TempDir()
	child := osexec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.timeout=60s")
	child.Env = append(os.Environ(), interruptEnv+"="+dir)
	var screen strings.Builder
	child.Stdout, child.Stderr = &screen, &screen
	if err := child.Start(); err != nil {
		t.Fatalf("starting the other Atenea: %v", err)
	}

	// Signaled when the far side is genuinely in the state this is about --
	// its grandchild spawned and waiting -- rather than after a fixed delay.
	// The deadline below is not a measurement of how long that should take: it
	// is the point past which something is wrong rather than merely slow.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "omp-running")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the other Atenea never reached the call this interrupts:\n%s", screen.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := child.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signaling: %v", err)
	}
	started := time.Now()
	waitErr := child.Wait()
	stopped := time.Since(started)

	out := screen.String()
	// The half that hid the other: before the fix this returned when the
	// grandchild did, twenty-five seconds after the signal.
	if limit := 10 * time.Second; stopped > limit {
		t.Errorf("stopping took %v after the signal, want under %v:\n%s",
			stopped.Truncate(time.Millisecond), limit, out)
	}
	if waitErr == nil {
		t.Fatalf("an interrupted run left as if it had worked:\n%s", out)
	}
	if strings.Contains(out, "longer than") {
		t.Errorf("the screen quoted a ceiling nobody reached:\n%s", out)
	}
	if !strings.Contains(out, "canceled") {
		t.Errorf("the screen never says what happened:\n%s", out)
	}
	// The other half of the same misattribution, and the one a reader sees
	// first. The bins were right while every line above them still said
	// `failed`: the run's verdict, the step's review, and the step's own
	// result. Nothing failed, and a reader sent looking for a fault wastes
	// the trip.
	if !strings.Contains(out, "verdict   canceled") {
		t.Errorf("the verdict does not say what happened:\n%s", out)
	}
	if strings.Contains(out, "failed") {
		t.Errorf("the screen still blames the work for the interruption:\n%s", out)
	}
	// And no review of something that never came back.
	if strings.Contains(out, "review") {
		t.Errorf("a step nobody let finish was reviewed anyway:\n%s", out)
	}
}

const interruptEnv = "ATENEA_CLI_INTERRUPT_CHILD"

// hangUntilInterrupted is the child half: a real invocation against a client
// that spawns a helper and does not come back, left to be interrupted.
func hangUntilInterrupted() {
	dir := os.Getenv(interruptEnv)
	client := filepath.Join(dir, "slow-omp")
	// The grandchild is deliberate: it inherits the pipe, which is what used
	// to keep the call waiting long after the signal.
	// It says when it is actually running. The parent used to sleep two
	// seconds and hope, which on a loaded machine signals a child that has
	// not spawned this yet -- testing the wrong moment under this test's name.
	running := filepath.Join(dir, "omp-running")
	script := "#!/bin/sh\ncase \"$1\" in --version) echo 'omp 9.9.9'; exit 0;; esac\n" +
		"touch " + running + "\nsleep 25 &\nwait\n"
	if err := os.WriteFile(client, []byte(script), 0o700); err != nil {
		panic(err)
	}
	// The fixture's repository is a path nobody has, and a step that cannot
	// find its repository fails before anything is spawned -- which would
	// make this test pass for the wrong reason, in a millisecond.
	path := filepath.Join(dir, "atenea.toml")
	body := strings.Replace(settings, `path = "/srv/api"`, `path = "`+dir+`"`, 1) +
		"\n[orchestrator.omp]\nbinary = \"" + client + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		panic(err)
	}

	// --trace on purpose: the review and the step's own result only print
	// there, and they are half of what this test is about.
	err := run([]string{"--config", path, "ask", "code.search",
		"--repo", "api", "--set", "query=TODO", "--trace"}, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stdout, "atenea: %v\n", err)
		os.Exit(exitCode(err))
	}
}
