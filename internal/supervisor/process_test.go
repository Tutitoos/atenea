package supervisor

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestProcess builds a *process the same way Supervisor.New would --
// defaults filled in, a real port chosen -- without the multi-server
// bookkeeping a Supervisor adds on top. What is under test here is the
// state machine one process runs, not how many a Supervisor happens to own.
func newTestProcess(t *testing.T, spec Spec) *process {
	t.Helper()
	built, err := spec.withDefaults()
	if err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	port, err := choosePort(built.Host, built.Port)
	if err != nil {
		t.Fatalf("choosePort: %v", err)
	}
	built.Port = port
	p := newProcess(built)
	t.Cleanup(p.stop)
	return p
}

// A well-behaved server starting from cold: ensureReady blocks until the
// probe succeeds, and status reflects a real running process by then.
func TestProcessEnsureReadyStartsAndReportsReady(t *testing.T) {
	p := newTestProcess(t, fakeSpec("ready", OnDemand, nil))

	endpoint, err := p.ensureReady(context.Background())
	if err != nil {
		t.Fatalf("ensureReady: %v", err)
	}
	if endpoint != p.endpoint {
		t.Fatalf("ensureReady returned %q, want the process's own endpoint %q", endpoint, p.endpoint)
	}

	st := p.status()
	if st.State != StateReady {
		t.Fatalf("state = %v, want %v", st.State, StateReady)
	}
	if st.PID == 0 {
		t.Fatal("status reports PID 0 for a running process")
	}
	if st.Started.IsZero() {
		t.Fatal("status reports a zero Started time for a running process")
	}
}

// Two callers racing ensureReady on a cold process must not each spawn their
// own child: both would try to bind the one port New already fixed for this
// process, and only one could win. Restarts staying at zero is the evidence
// that only one spawn ever happened.
func TestProcessEnsureReadyConcurrentCallersShareOneAttempt(t *testing.T) {
	p := newTestProcess(t, fakeSpec("shared", OnDemand, nil))

	const callers = 20
	var wg sync.WaitGroup
	endpoints := make([]string, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			endpoints[i], errs[i] = p.ensureReady(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: ensureReady: %v", i, err)
		}
		if endpoints[i] != p.endpoint {
			t.Fatalf("caller %d: endpoint = %q, want %q", i, endpoints[i], p.endpoint)
		}
	}
	if got := p.status().Restarts; got != 0 {
		t.Fatalf("Restarts = %d, want 0 -- concurrent callers spawned more than one attempt", got)
	}
}

// A server that never answers ready, with no retry budget, goes straight to
// down after its one attempt -- the same "no ceiling, no infinite retry"
// rule the rest of the design applies to a break-in rotation.
func TestProcessNeverReadyGoesDownWithoutRetryWhenBudgetIsZero(t *testing.T) {
	spec := fakeSpec("bad-init", OnDemand, map[string]string{"FAKE_BAD_INITIALIZE": "1"})
	spec.ReadyTimeout = 300 * time.Millisecond
	spec.RestartLimit = 0
	p := newTestProcess(t, spec)

	_, err := p.ensureReady(context.Background())
	if err == nil {
		t.Fatal("ensureReady succeeded against a server that never answers ready")
	}
	if !strings.Contains(err.Error(), "is down") {
		t.Fatalf("error %q does not say the server is down", err)
	}

	st := p.status()
	if st.State != StateDown {
		t.Fatalf("state = %v, want %v", st.State, StateDown)
	}
	if st.Restarts != 1 {
		t.Fatalf("Restarts = %d, want 1 (the one attempt that failed)", st.Restarts)
	}
	if !strings.Contains(st.LastReason, "did not answer ready") {
		t.Fatalf("LastReason = %q, missing the timeout explanation", st.LastReason)
	}
}

// ensureReady's ctx bounds only that caller's wait. The activation it caused
// keeps running in the background and can still succeed later for whoever
// asks again -- canceling one caller's patience is not the same as stopping
// the server.
func TestProcessEnsureReadyContextCanceledLeavesTheAttemptRunning(t *testing.T) {
	spec := fakeSpec("slow-start", OnDemand, map[string]string{"FAKE_DELAY_MS": "250"})
	p := newTestProcess(t, spec)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.ensureReady(ctx)
	if err != context.DeadlineExceeded {
		t.Fatalf("ensureReady error = %v, want context.DeadlineExceeded", err)
	}

	endpoint, err := p.ensureReady(context.Background())
	if err != nil {
		t.Fatalf("a later ensureReady on the same process failed: %v", err)
	}
	if endpoint == "" {
		t.Fatal("a later ensureReady returned an empty endpoint")
	}
}

// A server that comes up fine and then crashes is retried up to the budget
// and marked down once it is exhausted, with the crash's own output folded
// into the reason -- the whole point of capturing it at all.
func TestProcessCrashAfterReadyRestartsThenGoesDown(t *testing.T) {
	spec := fakeSpec("crashy", OnDemand, map[string]string{
		"FAKE_EXIT_AFTER_MS": "400",
		"FAKE_EXIT_CODE":     "7",
	})
	spec.RestartLimit = 2
	spec.RestartDelay = 30 * time.Millisecond
	p := newTestProcess(t, spec)

	_, err := p.ensureReady(context.Background())
	if err != nil {
		t.Fatalf("the first attempt should reach ready before crashing: %v", err)
	}

	waitFor(t, 3*time.Second, func() bool { return p.status().State == StateDown })

	st := p.status()
	if st.Restarts != 3 {
		t.Fatalf("Restarts = %d, want 3 (one initial attempt plus two retries)", st.Restarts)
	}
	if !strings.Contains(st.LastReason, "exited") {
		t.Fatalf("LastReason = %q, missing the exit explanation", st.LastReason)
	}
	if !strings.Contains(st.LastReason, "simulated crash") {
		t.Fatalf("LastReason = %q, missing the child's own captured output", st.LastReason)
	}
}

// A crash that follows a genuinely stable run must not spend down the same
// budget a tight crash loop would have already exhausted: proof is a
// three-attempt sequence -- quick failure, a stable run, quick failure
// again -- that only reaches down on the third attempt. Without the
// stability reset, the second attempt's failure alone would already have
// exceeded a restart limit of one and stopped at attempt two.
func TestProcessStabilityResetsTheRestartBudget(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "invocations")
	spec := fakeSpec("recovers", OnDemand, map[string]string{
		"FAKE_EXIT_AFTER_MS_SEQUENCE": "50,300,50",
		"FAKE_STATE_FILE":             stateFile,
	})
	spec.RestartLimit = 1
	spec.StableAfter = 100 * time.Millisecond
	spec.RestartDelay = 20 * time.Millisecond
	p := newTestProcess(t, spec)

	_, err := p.ensureReady(context.Background())
	if err != nil {
		t.Fatalf("the first attempt should reach ready before crashing: %v", err)
	}

	waitFor(t, 3*time.Second, func() bool { return p.status().State == StateDown })

	st := p.status()
	if st.Restarts != 3 {
		t.Fatalf("Restarts = %d, want 3 -- a budget of 1 that never got the stable reset would go down at 2", st.Restarts)
	}
	if !strings.Contains(st.LastReason, "exited") {
		t.Fatalf("LastReason = %q, missing the exit explanation", st.LastReason)
	}
}

// stop asks, then waits. Calling it from many goroutines at once must land
// on exactly the same outcome as calling it once, never a panic from a
// second close of an already-closed channel.
func TestProcessStopIsIdempotentUnderConcurrentCallers(t *testing.T) {
	p := newTestProcess(t, fakeSpec("multi-stop", OnDemand, nil))
	if _, err := p.ensureReady(context.Background()); err != nil {
		t.Fatalf("ensureReady: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.stop()
		}()
	}
	wg.Wait()

	if got := p.status().State; got != StateStopped {
		t.Fatalf("state = %v, want %v", got, StateStopped)
	}
}

// A server that honors SIGTERM should be gone well before StopGrace elapses:
// gracefulStop must not wait out the whole grace period when the polite
// signal already worked.
func TestProcessGracefulStopReturnsQuicklyWhenSIGTERMIsHonored(t *testing.T) {
	spec := fakeSpec("polite", OnDemand, nil)
	spec.StopGrace = 2 * time.Second
	p := newTestProcess(t, spec)
	if _, err := p.ensureReady(context.Background()); err != nil {
		t.Fatalf("ensureReady: %v", err)
	}

	start := time.Now()
	p.stop()
	elapsed := time.Since(start)

	if got := p.status().State; got != StateStopped {
		t.Fatalf("state = %v, want %v", got, StateStopped)
	}
	if elapsed >= spec.StopGrace {
		t.Fatalf("stop took %s, want well under StopGrace (%s) -- SIGTERM should have ended it", elapsed, spec.StopGrace)
	}
}

// A server that swallows SIGTERM has to be waited out for the full grace
// period and then killed outright -- proof that the escalation actually
// fires rather than a stop that hangs forever on an uncooperative child.
func TestProcessGracefulStopEscalatesToSIGKILLWhenIgnored(t *testing.T) {
	spec := fakeSpec("stubborn", OnDemand, map[string]string{"FAKE_IGNORE_TERM": "1"})
	spec.StopGrace = 300 * time.Millisecond
	p := newTestProcess(t, spec)
	if _, err := p.ensureReady(context.Background()); err != nil {
		t.Fatalf("ensureReady: %v", err)
	}

	start := time.Now()
	p.stop()
	elapsed := time.Since(start)

	if got := p.status().State; got != StateStopped {
		t.Fatalf("state = %v, want %v", got, StateStopped)
	}
	if elapsed < spec.StopGrace-50*time.Millisecond {
		t.Fatalf("stop took only %s, want at least StopGrace (%s) -- it should have waited before escalating", elapsed, spec.StopGrace)
	}
	if elapsed > spec.StopGrace+2*time.Second {
		t.Fatalf("stop took %s, want it to finish promptly after escalating to SIGKILL", elapsed)
	}
}

// The idle reaper must never stop a server something is actively using,
// and must be free to once released -- acquire/release is what stopIfIdle
// checks before anything else.
func TestProcessAcquireBlocksIdleStopAndReleaseAllowsIt(t *testing.T) {
	spec := fakeSpec("idle-guarded", OnDemand, nil)
	spec.IdleTimeout = 50 * time.Millisecond
	p := newTestProcess(t, spec)
	if _, err := p.ensureReady(context.Background()); err != nil {
		t.Fatalf("ensureReady: %v", err)
	}

	p.acquire()
	time.Sleep(200 * time.Millisecond) // well past IdleTimeout
	p.stopIfIdle()
	if got := p.status().State; got != StateReady {
		t.Fatalf("state = %v, want %v -- an acquired process must not be reaped", got, StateReady)
	}

	p.release()
	waitFor(t, 2*time.Second, func() bool {
		p.stopIfIdle()
		return p.status().State == StateStopped
	})
}

// PID is only meaningful while something is actually running under it; once
// stopped, a stale number left in Status would name a process that is gone
// and whose PID the OS is free to hand to something unrelated.
func TestProcessPIDClearsOnceStopped(t *testing.T) {
	p := newTestProcess(t, fakeSpec("pid-clear", OnDemand, nil))
	if _, err := p.ensureReady(context.Background()); err != nil {
		t.Fatalf("ensureReady: %v", err)
	}
	if p.status().PID == 0 {
		t.Fatal("PID is 0 while the process is ready")
	}

	p.stop()
	if got := p.status().PID; got != 0 {
		t.Fatalf("PID = %d after stop, want 0", got)
	}
}
